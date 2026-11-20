package main

// ============================================================
// 闲鱼同步（服务端直连）
//
// 设计目标：不再需要浏览器插件、不再手工导出 JSON。
// 用户在设置里粘一次浏览器 Cookie，之后每次点「立即同步」，
// 服务端自己去拉：买到的 / 卖出的订单列表 + 收入账单 + 支出账单，
// 用账单里的「实收金额」和「实际扣费」还原真实到手价，再按单号去重入库。
//
// 口径（用户 2026-11 明确要求）：
//   成交价 = 账单 incomeActualOrderAmount（买家实付，商品部分，不含运费）
//   服务费 = 软件服务费 + 基础软件服务费（1% + 0.6%）
//   无忧卖 = 类目软件服务费（命中类目才收，约 1%）
//   到手价 = 成交价 - 服务费 - 无忧卖
// 账单未生成（当月/次月初）时先按标价入库，note 标注「未结算」，下次同步自动补。
//
// 只导「交易成功」；退款 / 交易关闭 / 未完成 / ¥0.01 占位单一律跳过。
// ============================================================

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	gfAppKey  = "34839810"
	gfMtopH5  = "https://h5api.m.goofish.com/h5/%s/1.0/"
	gfUA      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36 Edg/157.0.0.0"
	gfTimeout = 25 * time.Second
)

type gfClient struct {
	mu      sync.Mutex
	cookies map[string]string
	hc      *http.Client
}

func newGFClient(raw string) *gfClient {
	c := &gfClient{
		cookies: map[string]string{},
		hc:      &http.Client{Timeout: gfTimeout},
	}
	c.loadCookies(raw)
	return c
}

// loadCookies 兼容三种粘贴方式：
//  1. 完整请求头 "Cookie: a=b; c=d"
//  2. 纯 cookie 串 "a=b; c=d"
//  3. 每行一对 "a=b"（DevTools 复制出来的表格）
func (c *gfClient) loadCookies(raw string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := strings.TrimSpace(raw)
	if s == "" {
		return
	}
	s = strings.TrimPrefix(s, "Cookie:")
	s = strings.TrimPrefix(s, "cookie:")
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 表格粘贴：name\tvalue  或  name  value
		if !strings.Contains(line, "=") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				c.cookies[f[0]] = f[1]
			}
			continue
		}
		for _, kv := range strings.Split(line, ";") {
			kv = strings.TrimSpace(kv)
			i := strings.Index(kv, "=")
			if i <= 0 {
				continue
			}
			c.cookies[strings.TrimSpace(kv[:i])] = strings.TrimSpace(kv[i+1:])
		}
	}
}

func (c *gfClient) cookieString() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]string, 0, len(c.cookies))
	for k := range c.cookies {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+c.cookies[k])
	}
	return strings.Join(parts, "; ")
}

func (c *gfClient) cookieVal(name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cookies[name]
}

func (c *gfClient) setCookieVal(name, val string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cookies[name] = val
}

// absorbCookies 把响应里的 Set-Cookie 收下：mtop 每次请求都会刷新 _m_h5_tk
func (c *gfClient) absorbCookies(resp *http.Response) {
	for _, sc := range resp.Header.Values("Set-Cookie") {
		head := sc
		if i := strings.Index(head, ";"); i >= 0 {
			head = head[:i]
		}
		j := strings.Index(head, "=")
		if j <= 0 {
			continue
		}
		name := strings.TrimSpace(head[:j])
		val := strings.TrimSpace(head[j+1:])
		if name != "" && val != "" {
			c.setCookieVal(name, val)
		}
	}
}

func (c *gfClient) token() string {
	raw := c.cookieVal("_m_h5_tk")
	if i := strings.Index(raw, "_"); i >= 0 {
		return raw[:i]
	}
	return raw
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// call 调一次 mtop。返回 data 段（已解成 map），令牌过期会自动重试。
// gfFetchBuyPostFee 控制「闲鱼同步买单时，是否逐单调订单详情取商品邮费(POST_FEE)作为买运费」。
// 默认关闭：实测绝大多数闲鱼单是包邮，POST_FEE 全为 0.00，而逐单调详情会给每个买单多一次请求，
// 白白增加风控风险。以后若要启用，把它改成读 settings（如 gfSettingBool("goofish_buy_postfee", shop)）
// 即可，其余链路（缓存/写库/导入）都已预留好，无需再改别处。
func gfFetchBuyPostFee() bool {
	return false
}

// gfOrderPostFee 从订单详情的 priceInfo.billList 里取 POST_FEE（商品邮费）。
// 详情接口形如：data.components[*].data.priceInfo.billList[] = {code:"POST_FEE", value:"0.00"}。
// 找不到或解析失败都返回 0，调用方按「无邮费」处理，绝不影响同步主流程。
func (c *gfClient) gfOrderPostFee(orderNo string) float64 {
	var d map[string]interface{}
	if err := c.call("mtop.idle.web.trade.order.detail", map[string]interface{}{"tid": orderNo}, &d); err != nil {
		return 0
	}
	var found float64
	var walk func(interface{})
	walk = func(v interface{}) {
		if found != 0 {
			return
		}
		switch x := v.(type) {
		case map[string]interface{}:
			if code := gfStr(x["code"]); code == "POST_FEE" {
				found = gfFloat(x["value"])
				return
			}
			for _, child := range x {
				walk(child)
			}
		case []interface{}:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(d)
	return found
}

func (c *gfClient) call(api string, payload interface{}, out interface{}) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		body, err := c.callOnce(api, payload)
		if err != nil {
			lastErr = err
			// 令牌类错误：响应里已经带回新 _m_h5_tk，直接用新令牌重试
			if strings.Contains(err.Error(), "TOKEN") {
				continue
			}
			return err
		}
		raw, ok := body["data"]
		if !ok {
			return fmt.Errorf("接口返回缺少 data")
		}
		if out != nil {
			// 必须用 UseNumber：订单号是 19 位整数，走 float64 会变成 3316585406014014000
			b, err := json.Marshal(raw)
			if err != nil {
				return fmt.Errorf("解析返回失败: %v", err)
			}
			dec := json.NewDecoder(bytes.NewReader(b))
			dec.UseNumber()
			if err := dec.Decode(out); err != nil {
				return fmt.Errorf("解析返回失败: %v", err)
			}
		}
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("接口调用失败")
	}
	return lastErr
}

func (c *gfClient) callOnce(api string, payload interface{}) (map[string]interface{}, error) {
	dj := "{}"
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		dj = string(b)
	}
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	tok := c.token()
	sign := md5hex(tok + "&" + ts + "&" + gfAppKey + "&" + dj)

	q := url.Values{}
	q.Set("jsv", "2.7.2")
	q.Set("appKey", gfAppKey)
	q.Set("t", ts)
	q.Set("sign", sign)
	q.Set("api", api)
	q.Set("v", "1.0")
	q.Set("type", "originaljson")
	q.Set("dataType", "json")
	q.Set("timeout", "20000")
	q.Set("data", dj)

	req, err := http.NewRequest("GET", fmt.Sprintf(gfMtopH5, api)+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", gfUA)
	req.Header.Set("Cookie", c.cookieString())
	// 尽量贴近真实 H5 客户端的请求特征：mtop 会看这些头，
	// 缺得太多更容易被判定成脚本、令牌也更容易被判失效。
	req.Header.Set("Referer", "https://www.goofish.com/")
	req.Header.Set("Origin", "https://www.goofish.com")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-site")

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	c.absorbCookies(resp)
	buf, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var env map[string]interface{}
	dec := json.NewDecoder(strings.NewReader(string(buf)))
	dec.UseNumber() // 订单号是 19 位整数，float64 会丢精度
	if err := dec.Decode(&env); err != nil {
		return nil, fmt.Errorf("接口返回不是 JSON（可能登录已失效）")
	}
	ret := gfStrSlice(env["ret"])
	if len(ret) > 0 && !strings.Contains(ret[0], "SUCCESS") {
		return nil, fmt.Errorf("%s", ret[0])
	}
	return env, nil
}

// ---------- 取值助手（JSON 数字用 json.Number，避免大整数丢精度） ----------

func gfStr(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(t)
	}
}

func gfFloat(v interface{}) float64 {
	switch t := v.(type) {
	case json.Number:
		f, _ := t.Float64()
		return f
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	case bool:
		return 0
	}
	return 0
}

func gfStrSlice(v interface{}) []string {
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		out = append(out, gfStr(x))
	}
	return out
}

// gfPath 按路径取嵌套值，任一层缺失返回 nil
func gfPath(root interface{}, keys ...string) interface{} {
	cur := root
	for _, k := range keys {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return nil
		}
		cur, ok = m[k]
		if !ok {
			return nil
		}
	}
	return cur
}

func gfRound2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}

// ---------- 业务接口 ----------

type gfAccount struct {
	Nick string `json:"nick"`
	UID  string `json:"uid"`
}

func (c *gfClient) account() (*gfAccount, error) {
	var out map[string]interface{}
	if err := c.call("mtop.taobao.idlemessage.pc.loginuser.get", map[string]interface{}{}, &out); err != nil {
		return nil, err
	}
	// 注意：call() 返回的已经是 data 段，路径不要再带一层 data
	uid := gfStr(out["userId"])
	if uid == "" {
		uid = gfStr(gfPath(out, "userDO", "userId"))
	}
	if uid == "" {
		return nil, fmt.Errorf("登录态无效，请重新复制 Cookie")
	}
	// 这个接口不返回昵称，昵称直接在 cookie 里（tracknick 是登录名，_nk_ 是昵称）
	nick := c.cookieVal("tracknick")
	if nick == "" {
		nick = c.cookieVal("_nk_")
	}
	if nick == "" {
		nick = uid
	}
	return &gfAccount{Nick: nick, UID: uid}, nil
}

// gfOrder 一条订单（列表口径）
type gfOrder struct {
	OrderNo     string
	Product     string
	Side        string // buy | sell
	Status      string
	StatusEnum  string
	Time        string
	ConsignTime string // 发货时间（仅卖单，闲鱼 consignTime，精确到秒）
	Peer        string
	ListPrice   float64 // 标价 / 实付款
	PostFee     float64 // 商品邮费（POST_FEE）——预留：买运费来源。列表接口没有此字段，只有打开 gfFetchBuyPostFee 开关时才逐单调详情补齐。
	Backfill    bool    // 来自「收入明细」账单补建（列表接口只回最近 40 单，更早的靠这里补）
}

// boughtOrders 我买到的。翻页参数是 pageNumber（不是 pageNum，实测 pageNum 会被忽略），
// 接口固定每页 10 条且 nextPage 恒为 true、totalCount 恒为 0，只能按「这一页全是老单号」收尾。
func (c *gfClient) boughtOrders(maxPages int) ([]gfOrder, error) {
	var out []gfOrder
	seen := map[string]bool{}
	// 预留：开关打开时，用缓存+详情补齐买运费（商品邮费 POST_FEE）。
	// 默认 gfFetchBuyPostFee()=false，下面这段完全不执行，零额外请求。
	fetchPostFee := gfFetchBuyPostFee()
	if fetchPostFee {
		defer func() {
			// 本轮新查到的邮费写回缓存，下一轮同步不用再逐单调详情
			for _, o := range out {
				if o.Side == "buy" && o.PostFee > 0 {
					savePostFee(o.OrderNo, o.PostFee)
				}
			}
		}()
	}
	for page := 1; page <= maxPages; page++ {
		var d map[string]interface{}
		if err := c.call("mtop.idle.web.trade.bought.list",
			map[string]interface{}{"pageSize": 10, "pageNumber": page}, &d); err != nil {
			return out, err
		}
		items, _ := d["items"].([]interface{})
		if len(items) == 0 {
			break
		}
		added := 0
		for _, raw := range items {
			cd := gfPath(raw, "commonData")
			no := gfStr(gfPath(cd, "orderIdStr"))
			if no == "" {
				no = gfStr(gfPath(cd, "orderId"))
			}
			if no == "" || seen[no] {
				continue
			}
			seen[no] = true
			added++
			title := gfStr(gfPath(raw, "content", "data", "detailInfo", "auctionTitle"))
			title = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(title), "标题:"))
			title = strings.TrimSpace(strings.TrimPrefix(title, "标题："))
			price := gfFloat(gfPath(raw, "content", "data", "priceInfo", "price"))
			// 预留：买运费（商品邮费）。开关关闭时恒为 0，不产生任何详情请求。
			var postFee float64
			if fetchPostFee {
				if row := loadBillCache([]string{no})[no]; row != nil && row.PostFee > 0 {
					postFee = row.PostFee
				} else {
					postFee = c.gfOrderPostFee(no) // 详情取 POST_FEE，失败返回 0
					if postFee < 0 {
						postFee = 0
					}
				}
			}
			out = append(out, gfOrder{
				OrderNo:    no,
				Product:    title,
				Side:       "buy",
				Status:     gfStr(gfPath(raw, "head", "data", "statusViewMsg")),
				StatusEnum: gfStr(gfPath(cd, "tradeStatusEnum")),
				Time:       gfStr(gfPath(raw, "head", "data", "createTime")),
				Peer:       gfStr(gfPath(raw, "head", "data", "userInfo", "userNick")),
				ListPrice:  price,
				PostFee:    postFee,
			})
		}
		if added == 0 {
			break
		}
		time.Sleep(180 * time.Millisecond) // 温和一点，别把接口打急
	}
	return out, nil
}

// soldOrders 我卖出的。每页 20 条。
func (c *gfClient) soldOrders(maxPages int) ([]gfOrder, error) {
	var out []gfOrder
	seen := map[string]bool{}
	for page := 1; page <= maxPages; page++ {
		var d map[string]interface{}
		if err := c.call("mtop.taobao.idle.trade.merchant.sold.get",
			map[string]interface{}{"pageSize": 20, "pageNumber": page}, &d); err != nil {
			return out, err
		}
		items, _ := gfPath(d, "module", "items").([]interface{})
		if len(items) == 0 {
			break
		}
		added := 0
		for _, raw := range items {
			cd := gfPath(raw, "commonData")
			no := gfStr(gfPath(cd, "orderIdStr"))
			if no == "" {
				no = gfStr(gfPath(cd, "orderId"))
			}
			if no == "" || seen[no] {
				continue
			}
			seen[no] = true
			added++
			status := gfStr(gfPath(cd, "orderStatus"))
			if status == "" {
				// 兜底用列信息里的第一列
				status = gfStr(gfPath(raw, "columnVOList"))
			}
			priceVO, _ := gfPath(raw, "priceVO").(map[string]interface{})
			out = append(out, gfOrder{
				OrderNo:     no,
				Product:     gfStr(gfPath(raw, "itemVO", "title")),
				Side:        "sell",
				Status:      status,
				StatusEnum:  "",
				Time:        gfStr(gfPath(cd, "createTime")),
				ConsignTime: gfStr(gfPath(cd, "consignTime")),
				Peer:        gfStr(gfPath(raw, "buyerInfoVO", "userNick")),
				ListPrice:   gfSoldOrderPrice(priceVO),
			})
		}
		if added == 0 {
			break
		}
		time.Sleep(180 * time.Millisecond)
	}
	return out, nil
}

// gfSoldOrderPrice 取买家实际成交金额。
// auctionPrice 是商品挂牌单价，不能直接当作成交价；订单列表的 totalPrice
// 才是含议价结果的实际订单金额。旧响应缺少 totalPrice 时，才用单价乘件数兜底。
func gfSoldOrderPrice(priceVO map[string]interface{}) float64 {
	if total := gfFloat(priceVO["totalPrice"]); total > 0 {
		return total
	}
	auction := gfFloat(priceVO["auctionPrice"])
	count := gfFloat(priceVO["buyNum"])
	if count < 1 {
		count = 1
	}
	return auction * count
}

// gfBill 账单聚合结果：一个订单的真实成交额与实际扣费
type gfBill struct {
	Actual  float64 // 真实成交额（商品部分，不含运费）
	Auction float64 // 商品标价
	Cash    float64 // 货款收入（若有）
	Tech    float64 // 软件服务费 + 基础软件服务费
	Wuyou   float64 // 无忧卖服务费
	HasInc  bool
}

func (c *gfClient) incomeDetail(from, to string) (map[string]*gfBill, error) {
	out := map[string]*gfBill{}
	seen := map[string]bool{}
	for page := 1; page <= 40; page++ {
		var d map[string]interface{}
		err := c.call("mtop.idle.finance.billdetail.income.query", map[string]interface{}{
			"request": map[string]interface{}{
				"billCode": "goods_income", "bizDateFrom": from, "bizDateTo": to,
				"pageNo": page, "pageSize": 50,
			},
		}, &d)
		if err != nil {
			return out, err
		}
		rows := gfRows(d)
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			biz := gfStr(gfPath(r, "bizId"))
			if biz != "" && seen[biz] {
				continue // 分页会重复吐同一条，按 bizId 去重
			}
			seen[biz] = true
			no := gfStr(gfPath(r, "orderId"))
			if no == "" {
				continue
			}
			b := out[no]
			if b == nil {
				b = &gfBill{}
				out[no] = b
			}
			b.HasInc = true
			b.Actual += gfFloat(gfPath(r, "incomeActualOrderAmount"))
			b.Auction += gfFloat(gfPath(r, "incomeAuctionPrice"))
		}
		if len(rows) < 50 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	return out, nil
}

// gfBillOrder 收入明细里的一条（比列表接口多出商品名与下单时间，
// 而且账单期可以一直往前翻，用来补列表接口只回 40 单漏掉的历史订单）
type gfBillOrder struct {
	OrderNo string
	Title   string
	Time    string // 下单时间
	Confirm string // 确认收货时间
	Actual  float64
	Auction float64
}

func (c *gfClient) incomeOrders(from, to string) ([]gfBillOrder, error) {
	out := []gfBillOrder{}
	seen := map[string]bool{}
	for page := 1; page <= 60; page++ {
		var d map[string]interface{}
		err := c.call("mtop.idle.finance.billdetail.income.query", map[string]interface{}{
			"request": map[string]interface{}{
				"billCode": "goods_income", "bizDateFrom": from, "bizDateTo": to,
				"pageNo": page, "pageSize": 50,
			},
		}, &d)
		if err != nil {
			return out, err
		}
		rows := gfRows(d)
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			// 收入明细里除了「交易货款」，还混着退货运费赔付、闲鱼转账、红包，
			// 这些都按订单号进账单，但绝不是一笔卖单，补建时必须排掉。
			biz := gfStr(gfPath(r, "bizCode"))
			if biz != "" && biz != "goods_income" {
				continue
			}
			title := gfStr(gfPath(r, "itemDesc"))
			if strings.Contains(title, "赔付") || strings.Contains(title, "转账") ||
				strings.Contains(title, "红包") || strings.Contains(title, "退款") {
				continue
			}
			no := gfStr(gfPath(r, "orderId"))
			if no == "" || seen[no] {
				continue
			}
			seen[no] = true
			t := gfStr(gfPath(r, "incomeOrderCreateTime"))
			if t == "" {
				t = gfStr(gfPath(r, "gmtCreate"))
			}
			out = append(out, gfBillOrder{
				OrderNo: no,
				Title:   gfStr(gfPath(r, "itemDesc")),
				Time:    t,
				Confirm: gfStr(gfPath(r, "incomeConfirmReceivingTime")),
				Actual:  gfFloat(gfPath(r, "incomeActualOrderAmount")),
				Auction: gfFloat(gfPath(r, "incomeAuctionPrice")),
			})
		}
		if len(rows) < 50 {
			break
		}
		time.Sleep(220 * time.Millisecond)
	}
	return out, nil
}

var gfExpenseBiz = []string{
	"IDLE_WYM_CATEGORY_FEE_STD_PROCESS",      // 无忧卖服务费
	"IDLE_TECHNICAL_SERVICE_FEE_STD_PROCESS", // 软件服务费
	"IDLE_ALL_SERVICE_FEE_STD_PROCESS",       // 基础软件服务费
}

func (c *gfClient) expenseDetail(from, to string) (map[string]*gfBill, error) {
	out := map[string]*gfBill{}
	seen := map[string]bool{}
	for _, biz := range gfExpenseBiz {
		for page := 1; page <= 40; page++ {
			var d map[string]interface{}
			err := c.call("mtop.idle.finance.billdetail.expense.query", map[string]interface{}{
				"request": map[string]interface{}{
					"billCode": "common_settle", "bizDateFrom": from, "bizDateTo": to,
					"pageNo": page, "pageSize": 50, "bizCode": biz,
				},
			}, &d)
			if err != nil {
				return out, err
			}
			rows := gfRows(d)
			if len(rows) == 0 {
				break
			}
			for _, r := range rows {
				key := gfStr(gfPath(r, "bizId"))
				if key == "" {
					key = biz + "|" + gfStr(gfPath(r, "orderId")) + "|" + gfStr(gfPath(r, "bizDate"))
				}
				if seen[key] {
					continue
				}
				seen[key] = true
				no := gfStr(gfPath(r, "orderId"))
				if no == "" {
					no = gfStr(gfPath(r, "primaryOrderId"))
				}
				if no == "" {
					continue
				}
				b := out[no]
				if b == nil {
					b = &gfBill{}
					out[no] = b
				}
				amt := gfFloat(gfPath(r, "expendDeductAmount"))
				switch biz {
				case "IDLE_WYM_CATEGORY_FEE_STD_PROCESS":
					b.Wuyou += amt
				default:
					b.Tech += amt
				}
			}
			if len(rows) < 50 {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	return out, nil
}

// gfBillPending：账单还没生成（通常当月账单要到次月 3 号后才出）。
// 这不是故障，不该弹告警——对应的订单会被标成「未结算」，下次同步自动补。
func gfBillPending(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return strings.Contains(m, "BILL_QUERY_DELAY_ERROR") || strings.Contains(m, "暂无法查看")
}

// gfRows 取 model.data（账单明细的数组）
func gfRows(d map[string]interface{}) []map[string]interface{} {
	arr, _ := gfPath(d, "model", "data").([]interface{})
	out := make([]map[string]interface{}, 0, len(arr))
	for _, x := range arr {
		if m, ok := x.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out
}

// ---------- 同步计划 ----------

// gfPlanRow 一条待导入订单（已按账单还原口径）
type gfPlanRow struct {
	OrderNo       string  `json:"order_no"`
	Side          string  `json:"side"`
	Product       string  `json:"product"`
	Status        string  `json:"status"`
	Time          string  `json:"time"`
	ConsignTime   string  `json:"consign_time"` // 发货时间（卖单才有）
	Peer          string  `json:"peer"`
	ListPrice     float64 `json:"list_price"`     // 标价
	Price         float64 `json:"price"`          // 成交价（有账单用实收，否则用标价）
	Fee           float64 `json:"fee"`            // 软件服务费 + 基础软件服务费
	Wuyou         float64 `json:"wuyou"`          // 无忧卖服务费
	Net           float64 `json:"net"`            // 到手
	Billed        bool    `json:"billed"`         // 是否已拿到账单（费用是否已扣）
	PostFee       float64 `json:"post_fee"`       // 商品邮费，导入时作为买运费写入（预留，默认 0）
	Exists        bool    `json:"exists"`         // 系统里已有同单号
	Managed       bool    `json:"managed"`        // 已有记录是「闲鱼同步」建的（可安全按账单覆盖）；手动录入的为 false
	RefreshNeeded bool    `json:"refresh_needed"` // 已有卖单与本次闲鱼金额确实不同，才需要补正
	SkipReason    string  `json:"skip_reason"`    // 非空则不导入
	Backfill      bool    `json:"backfill"`       // 来自「收入明细」账单补建（列表接口只回最近 40 单）
}

type gfPlan struct {
	ID       string      `json:"plan_id"`
	MadeAt   time.Time   `json:"made_at"`
	Months   int         `json:"months"`
	Rows     []gfPlanRow `json:"rows"`
	Stat     gfPlanStat  `json:"stat"`
	Warnings []string    `json:"warnings"`
}

type gfPlanStat struct {
	Total     int `json:"total"`
	Fresh     int `json:"fresh"`     // 可导入
	Duplicate int `json:"duplicate"` // 系统已有
	Skipped   int `json:"skipped"`   // 状态不符/金额过小
	Billed    int `json:"billed"`    // 已扣费
	Unbilled  int `json:"unbilled"`  // 待结算
	Buy       int `json:"buy"`
	Sell      int `json:"sell"`
}

var (
	gfPlanMu   sync.Mutex
	gfPlanLast *gfPlan
)

// gfBillRanges 生成账单查询区间。
//
// 两条硬约束（实测）：
//  1. 单次查询的业务日期跨度不能超过 31 天，否则 INVALID_PARAM；
//  2. 区间的**结束日**必须比今天早至少 3 天，否则 BILL_QUERY_DELAY_ERROR。
//
// 所以：过去的月份整月查；本月切成 7 天一段、只查到今天-3 天。
// 只看「有订单的月份 + 它的下个月」（账单会延到次月），既不会漏，也不用把
// 好几年没有交易的空月份挨个查一遍。
// gfBackfillRanges 历史补建要扫的账单期：从 N 个月前到上个月，按自然月切分
// （账单接口约束：单次跨度 ≤31 天、结束日 ≤ 今天-3，整月天然满足）。
func gfBackfillRanges(months int) [][2]string {
	out := [][2]string{}
	now := time.Now()
	for i := months; i >= 1; i-- {
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local).AddDate(0, -i, 0)
		end := first.AddDate(0, 1, -1)
		out = append(out, [2]string{first.Format("2006-01-02"), end.Format("2006-01-02")})
	}
	return out
}

func gfBillRanges(orderMonths []string) [][2]string {
	want := map[string]bool{}
	for _, m := range orderMonths {
		if len(m) < 7 {
			continue
		}
		want[m[:7]] = true
		if t, e := time.Parse("2006-01", m[:7]); e == nil {
			want[t.AddDate(0, 1, 0).Format("2006-01")] = true
		}
	}
	now := time.Now()
	lastOK := now.AddDate(0, 0, -3)
	thisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 36 { // 封顶 3 年，避免异常数据把请求量拉爆
		keys = keys[len(keys)-36:]
	}

	out := make([][2]string, 0, len(keys))
	for _, k := range keys {
		t, e := time.Parse("2006-01", k)
		if e != nil {
			continue
		}
		start := t
		end := t.AddDate(0, 1, -1)
		if start.After(lastOK) {
			continue // 整月还没到可查时间
		}
		if end.After(lastOK) {
			// 本月：只查到「今天-3」。约束是看区间的**结束日**，
			// 所以从月初到 lastOK 一个区间就够，不用再按天切（实测 11-01~11-12 正常返回）。
			out = append(out, [2]string{start.Format("2006-01-02"), lastOK.Format("2006-01-02")})
			continue
		}
		out = append(out, [2]string{start.Format("2006-01-02"), end.Format("2006-01-02")})
	}
	_ = thisMonth
	return out
}

// gfFilterRecent 按「最近 N 天」过滤买卖单；window<=0 不过滤。
// 时间解析不了就保留，宁可多对一次，也不因格式问题漏单。
func gfFilterRecent(buy, sell []gfOrder, window int) ([]gfOrder, []gfOrder) {
	if window <= 0 {
		return buy, sell
	}
	cutoff := time.Now().AddDate(0, 0, -window)
	inWindow := func(t string) bool {
		tm, e := time.ParseInLocation("2006-01-02 15:04:05", t, time.Local)
		if e != nil {
			return true
		}
		return !tm.Before(cutoff)
	}
	nb := make([]gfOrder, 0, len(buy))
	for _, o := range buy {
		if inWindow(o.Time) {
			nb = append(nb, o)
		}
	}
	ns := make([]gfOrder, 0, len(sell))
	for _, o := range sell {
		if inWindow(o.Time) {
			ns = append(ns, o)
		}
	}
	return nb, ns
}

// buildPlan 拉全量并按口径还原
func buildPlan(c *gfClient, months int, shop int64) (*gfPlan, error) {
	return buildPlanWith(c, months, 0, shop)
}

// buildPlanWith backfillMonths>0 时，除了列表接口，还会按账单期往前翻收入明细，
// 把「已经成交但列表接口拿不到」的历史订单补进来。
// shop 必须传当前店铺：查重只在店铺内部做，别家的老数据不该挡住本店导入。
func gfProgress(shop int64, pct int, text string) {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	realtimeHub.publishProgress(shop, pct, text)
}

func buildPlanWith(c *gfClient, months, backfillMonths int, shop int64) (*gfPlan, error) {
	gfProgress(shop, 10, "正在拉取订单列表…")
	buy, err := c.boughtOrders(60)
	if err != nil && len(buy) == 0 {
		return nil, fmt.Errorf("拉取「我买到的」失败：%v", err)
	}
	sell, err := c.soldOrders(60)
	if err != nil && len(sell) == 0 {
		return nil, fmt.Errorf("拉取「我卖出的」失败：%v", err)
	}

	// 账单回溯关闭时（手动「立即同步」没勾选回溯），只处理「只同步最近 N 天」里选的天数，
	// 口径与自动同步一致；回溯开启时保持原行为——拉全量再按账单补历史。
	// 注意：自动同步自己已在 gfAutoSyncOne 里按 window 过滤，那里 backfillMonths>0，不受这里影响。
	if backfillMonths <= 0 {
		buy, sell = gfFilterRecent(buy, sell, gfAutoWindow(shop))
	}

	gfProgress(shop, 28, "订单列表已取回，正在按账单回溯…")
	// ---------- 历史补建：按账单期往前翻，补上列表接口 40 单以外的已成交订单 ----------
	var backfillBills map[string]*gfBill
	if backfillMonths > 0 {
		if backfillMonths > 24 {
			backfillMonths = 24
		}
		have := map[string]bool{}
		for _, o := range sell {
			have[o.OrderNo] = true
		}
		backfillBills = map[string]*gfBill{}
		type monthNew struct{ from, to string }
		needExp := []monthNew{}
		for _, r := range gfBackfillRanges(backfillMonths) {
			orders, e1 := c.incomeOrders(r[0], r[1])
			added := 0
			if e1 == nil {
				for _, o := range orders {
					if have[o.OrderNo] {
						continue
					}
					have[o.OrderNo] = true
					added++
					sell = append(sell, gfOrder{
						OrderNo: o.OrderNo, Product: o.Title, Side: "sell",
						Status: "交易成功", Time: o.Time, ListPrice: o.Auction, Backfill: true,
					})
					backfillBills[o.OrderNo] = &gfBill{Actual: o.Actual, Auction: o.Auction, HasInc: true}
				}
			}
			// 支出账单要按 3 种 bizCode 各拉一次，代价比收入高得多。
			// 只有这个月确实补出了新订单，才去拉它的服务费与无忧卖。
			if added > 0 {
				needExp = append(needExp, monthNew{r[0], r[1]})
			}
			time.Sleep(120 * time.Millisecond)
		}
		for _, m := range needExp {
			if exp, e2 := c.expenseDetail(m.from, m.to); e2 == nil {
				for k, v := range exp {
					b := backfillBills[k]
					if b == nil {
						backfillBills[k] = v
						continue
					}
					b.Tech += v.Tech
					b.Wuyou += v.Wuyou
				}
			}
			time.Sleep(120 * time.Millisecond)
		}
	}

	gfProgress(shop, 50, "正在核对收入 / 支出账单…")
	// ---------- 账单：先看本地缓存，只补「还没出账」的月份 ----------
	orderNos := make([]string, 0, len(buy)+len(sell))
	orderTime := make(map[string]string, len(buy)+len(sell))
	for _, o := range buy {
		orderNos = append(orderNos, o.OrderNo)
		orderTime[o.OrderNo] = o.Time
	}
	for _, o := range sell {
		orderNos = append(orderNos, o.OrderNo)
		orderTime[o.OrderNo] = o.Time
	}
	cache := loadBillCache(orderNos)

	// 需要联网查的订单：只看卖单——买单是「我付出去的钱」，闲鱼不会给它开服务费账单，
	// 把买单也算进来会让每个月永远「没缓存」，缓存就形同虚设。
	needMonths := make([]string, 0, len(sell))
	for _, o := range sell {
		c := cache[o.OrderNo]
		if c == nil || !c.Billed {
			needMonths = append(needMonths, o.Time)
		}
	}
	ranges := gfBillRanges(needMonths)

	bills := map[string]*gfBill{}
	var warn []string
	if len(ranges) > 0 {
		for _, r := range ranges {
			inc, e1 := c.incomeDetail(r[0], r[1])
			if e1 != nil {
				if !gfBillPending(e1) {
					warn = append(warn, fmt.Sprintf("收入账单 %s~%s 拉取失败：%v", r[0], r[1], e1))
				}
			} else {
				for k, v := range inc {
					b := bills[k]
					if b == nil {
						bills[k] = v
						continue
					}
					b.HasInc = true
					b.Actual += v.Actual
					b.Auction += v.Auction
				}
			}
			exp, e2 := c.expenseDetail(r[0], r[1])
			if e2 != nil {
				if !gfBillPending(e2) {
					warn = append(warn, fmt.Sprintf("支出账单 %s~%s 拉取失败：%v", r[0], r[1], e2))
				}
			} else {
				for k, v := range exp {
					b := bills[k]
					if b == nil {
						bills[k] = v
						continue
					}
					b.Tech += v.Tech
					b.Wuyou += v.Wuyou
				}
			}
		}
	}

	// 联网结果写回缓存：以后同一单不用再查，费用也不会因为某次查询失败而丢失
	for k, v := range bills {
		if v.HasInc || v.Tech > 0 || v.Wuyou > 0 {
			saveBillFact(gfBillRow{OrderNo: k, Actual: v.Actual, Auction: v.Auction,
				Tech: v.Tech, Wuyou: v.Wuyou, Billed: v.HasInc})
		}
	}
	// 缓存里有、这次没查到的，用缓存兜底
	merged := map[string]*gfBill{}
	for k, c := range cache {
		if c.Billed || c.Tech > 0 || c.Wuyou > 0 {
			merged[k] = &gfBill{Actual: c.Actual, Auction: c.Auction, Tech: c.Tech, Wuyou: c.Wuyou, HasInc: c.Billed}
		}
	}
	for k, v := range bills {
		merged[k] = v
	}
	for k, v := range backfillBills {
		merged[k] = v
	}
	bills = merged

	gfProgress(shop, 82, "账单已核对，正在整理预览…")
	plan := &gfPlan{ID: strconv.FormatInt(time.Now().UnixNano(), 36), MadeAt: time.Now(), Months: months}
	plan.Warnings = warn
	seen := map[string]bool{}

	add := func(o gfOrder) {
		if seen[o.Side+"|"+o.OrderNo] {
			return
		}
		seen[o.Side+"|"+o.OrderNo] = true
		plan.Stat.Total++
		row := gfPlanRow{
			OrderNo: o.OrderNo, Side: o.Side, Product: o.Product,
			Status: o.Status, Time: o.Time, ConsignTime: o.ConsignTime, Peer: o.Peer, ListPrice: o.ListPrice,
			PostFee: o.PostFee, Backfill: o.Backfill,
		}
		b := bills[o.OrderNo]
		row.Billed = b != nil && b.HasInc
		if row.Billed {
			row.Price = gfRound2(b.Actual)
			row.Fee = gfRound2(b.Tech)
			row.Wuyou = gfRound2(b.Wuyou)
		} else {
			row.Price = gfRound2(o.ListPrice)
		}
		row.Net = gfRound2(row.Price - row.Fee - row.Wuyou)

		// 卖单的状态文案来自列表接口，没有英文枚举（se 基本为空）：
		//   交易成功 / 已发货 / 待发货  → 都已经付过钱，必须导进来（之前只认「交易成功」，
		//   把已发货、待发货的单都当成「未完成」跳过了，用户反馈漏单就是这个原因）
		//   交易关闭 / 退款 / 取消 / 待付款 → 真的没有成交，跳过
		st := strings.TrimSpace(o.Status)
		se := strings.TrimSpace(o.StatusEnum)
		switch {
		case strings.Contains(st, "退款") || strings.Contains(st, "关闭") ||
			strings.Contains(st, "取消") || se == "refund_success" || se == "close_by_buyer":
			row.SkipReason = "退款/关闭"
		case strings.Contains(st, "待付款") || strings.Contains(st, "未付款") || se == "wait_buyer_pay":
			row.SkipReason = "未付款"
		case se != "" && se != "trade_success" && se != "wait_confirm" && se != "send_goods" && se != "wait_send":
			row.SkipReason = "未完成"
		case row.Price <= 0.05:
			row.SkipReason = "金额过小"
		}
		if o.Side == "buy" {
			plan.Stat.Buy++
		} else {
			plan.Stat.Sell++
		}
		plan.Rows = append(plan.Rows, row)
	}
	for _, o := range buy {
		add(o)
	}
	for _, o := range sell {
		add(o)
	}

	// 标记系统里已有的单号
	plan.markExisting(shop)

	for i := range plan.Rows {
		r := &plan.Rows[i]
		switch {
		case r.SkipReason != "":
			plan.Stat.Skipped++
		case r.Exists:
			plan.Stat.Duplicate++
		default:
			plan.Stat.Fresh++
		}
		if r.Billed {
			plan.Stat.Billed++
		} else {
			plan.Stat.Unbilled++
		}
	}
	gfProgress(shop, 92, "预览已生成")
	// 有账单的、能导的排前面
	sort.SliceStable(plan.Rows, func(i, j int) bool {
		a, b := plan.Rows[i], plan.Rows[j]
		fa := a.SkipReason == "" && !a.Exists
		fb := b.SkipReason == "" && !b.Exists
		if fa != fb {
			return fa
		}
		return a.Time > b.Time
	})
	return plan, nil
}

// markExisting 标记单号是否已存在，并判断已有卖单是否确实需要补正。
// 只查当前店铺：各店铺是独立账本，别家店里的老订单号不能挡住本店导入，
// 否则会出现「明明没有却提示已存在、列表只剩别家老数据」的假重复。
func (p *gfPlan) markExisting(shop int64) {
	notes := orderNotesInShops([]int64{shop})
	money := gfExistingSellMoney(shop)
	for i := range p.Rows {
		r := &p.Rows[i]
		if note, ok := notes[r.OrderNo]; ok {
			r.Exists = true
			r.Managed = gfIsSyncNote(note)
			if m, ok := money[r.OrderNo]; ok && r.Side == "sell" && r.SkipReason == "" {
				r.RefreshNeeded = gfPlanNeedsRefresh(*r, m)
			}
		}
	}
}

type gfExistingMoney struct {
	Price, Fee, Wuyou float64
	PriceVaries       bool
	FeeVaries         bool
	WuyouVaries       bool
}

// gfExistingSellMoney 汇总当前店铺每个卖出订单号的金额。
// 同一订单号若对应多条记录，用 MIN/MAX 识别是否存在不一致，避免只修正其中一条。
func gfExistingSellMoney(shop int64) map[string]gfExistingMoney {
	out := map[string]gfExistingMoney{}
	rows, err := db.Query(`SELECT sell_order_no,
        COALESCE(MAX(sell_price),0), COALESCE(MIN(sell_price),0),
        COALESCE(MAX(sell_fee),0), COALESCE(MIN(sell_fee),0),
        COALESCE(MAX(sell_wuyou_fee),0), COALESCE(MIN(sell_wuyou_fee),0)
        FROM items WHERE shop_id=? AND sell_order_no<>''
        GROUP BY sell_order_no`, shop)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var no string
		var maxPrice, minPrice, maxFee, minFee, maxWuyou, minWuyou float64
		if rows.Scan(&no, &maxPrice, &minPrice, &maxFee, &minFee, &maxWuyou, &minWuyou) != nil {
			continue
		}
		out[no] = gfExistingMoney{
			Price: maxPrice, Fee: maxFee, Wuyou: maxWuyou,
			PriceVaries: absf(maxPrice-minPrice) >= 0.005,
			FeeVaries:   absf(maxFee-minFee) >= 0.005,
			WuyouVaries: absf(maxWuyou-minWuyou) >= 0.005,
		}
	}
	return out
}

func gfPlanNeedsRefresh(r gfPlanRow, m gfExistingMoney) bool {
	if !r.Exists || r.Side != "sell" || r.SkipReason != "" {
		return false
	}
	if absf(m.Price-r.Price) >= 0.005 || m.PriceVaries {
		return true
	}
	// 未出账时保留已有服务费，只核对订单列表能提供的成交价。
	if !r.Billed {
		return false
	}
	return absf(m.Fee-r.Fee) >= 0.005 || absf(m.Wuyou-r.Wuyou) >= 0.005 ||
		m.FeeVaries || m.WuyouVaries
}

// gfIsSyncNote 判断这条记录是不是「同步建的」。
// 除了当前版本写的「闲鱼同步；…」，还要认历史上几版留下的备注，
// 否则老数据会被当成手动录入，账单永远补正不了。
func gfIsSyncNote(note string) bool {
	if note == "" {
		return false
	}
	for _, marker := range []string{"闲鱼同步", "服务费未扣", "来自卖家订单列表", "服务费未扣，待核"} {
		if strings.Contains(note, marker) {
			return true
		}
	}
	return false
}

// orderNotesInShops 取这些店铺里所有带订单号的记录的备注，用于判断来源
func orderNotesInShops(shops []int64) map[string]string {
	out := map[string]string{}
	if len(shops) == 0 {
		return out
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(shops)), ",")
	args := make([]interface{}, 0, len(shops))
	for _, s := range shops {
		args = append(args, s)
	}
	rows, err := db.Query("SELECT buy_order_no, sell_order_no, COALESCE(note,'') FROM items WHERE shop_id IN ("+ph+")", args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var b, s2, note string
		if rows.Scan(&b, &s2, &note) != nil {
			continue
		}
		if b != "" {
			out[b] = note
		}
		if s2 != "" {
			out[s2] = note
		}
	}
	return out
}
