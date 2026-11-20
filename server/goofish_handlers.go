package main

// 闲鱼同步的 HTTP 接口与入库逻辑（配合 goofish.go 使用）
//
// 配置按店铺隔离：每个店绑自己的闲鱼账号，互不影响。
// 存储键统一加 @<shopID> 后缀，切换店铺后看到的就是这家店自己的绑定状态。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------- 小工具 ----------

// shopIDsAll 所有未归档店铺 id，用于跨店铺查重
func shopIDsAll() []int64 {
	rows, err := db.Query("SELECT id FROM shops WHERE archived_at='' ORDER BY id")
	if err != nil {
		return []int64{defaultShopID}
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		out = append(out, defaultShopID)
	}
	return out
}

// orderNoExistsInShops 单号是否已在任一店铺（买/卖两侧都算）
func orderNoExistsInShops(no string, shops []int64) bool {
	if strings.TrimSpace(no) == "" || len(shops) == 0 {
		return false
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(shops)), ",")
	args := make([]interface{}, 0, len(shops)+2)
	for _, s := range shops {
		args = append(args, s)
	}
	args = append(args, no, no)
	var n int
	q := "SELECT COUNT(1) FROM items WHERE shop_id IN (" + ph + ") AND (buy_order_no=? OR sell_order_no=?)"
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// ---------- 店铺级配置读写 ----------

const (
	gfKeyCookie = "goofish_cookie"
	gfKeyNick   = "goofish_nick"
	gfKeyUID    = "goofish_uid"
	gfKeySaved  = "goofish_saved_at"
	gfKeyLast   = "goofish_last_sync"
	gfKeyCheck  = "goofish_last_check"
	gfKeyDead   = "goofish_expired"
	// gfKeySyncErr：自动同步失败的可读原因，供页面一打开就提示 + SSE 主动推送
	gfKeySyncErr = "goofish_sync_error"
	// gfKeySSOTaobao：淘宝 / 天猫域的 SSO Cookie（一键快速登录的凭证），
	// 用来在服务器端做静默换发（silent login），闲鱼会话过期后自动续期。
	gfKeySSOTaobao = "goofish_sso_taobao"
	// gfKeyUmidToken：阿里设备指纹（扫码登录用，浏览器 localStorage 里收集）
	gfKeyUmidToken = "goofish_umid_token"
	// gfKeyBackfill：立即同步时是否一并做账单回溯。默认开（老行为），存 "0"/"1"。
	gfKeyBackfill = "goofish_backfill"
	// gfKeySyncFails / gfKeyRetryAt：登录态类错误先重试，不要第一次就永久判死
	gfKeySyncFails = "goofish_sync_fails"
	gfKeyRetryAt   = "goofish_retry_at"
)

// gfMarkSyncError 记录自动同步失败原因，并通过 SSE 主动推送给该店铺在线页面。
// 这样用户不用等手动同步失败才知道 Cookie 失效。
func gfMarkSyncError(shop int64, reason string) {
	_ = gfSet(gfKeyDead, shop, "1")
	_ = gfSet(gfKeySyncErr, shop, reason)
	// 有淘宝 SSO 时保留续期开关，后台心跳仍可尝试自愈；
	// 只有确实没有恢复凭证时才关闭，避免无意义地反复请求。
	if gfKeepAliveEnabled() && !gfCanRecoverSession(shop) {
		setSetting(gfKeepAliveKey, "0")
	}
	hint := "（请重新绑定）"
	if gfCanRecoverSession(shop) {
		hint = "（自动续期会继续尝试恢复）"
	}
	realtimeHub.publish("GET", "/api/goofish/status", "system",
		"闲鱼自动同步失败："+reason+hint, shop)
}

// gfClearSyncError 绑定成功或同步成功后清除失效状态。
func gfClearSyncError(shop int64) {
	_ = gfSet(gfKeyDead, shop, "")
	_ = gfSet(gfKeySyncErr, shop, "")
	_ = gfSet(gfKeySyncFails, shop, "")
	_ = gfSet(gfKeyRetryAt, shop, "")
}

// gfSyncRetryOrFail 登录态类错误先自动重试，不要第一次就标记「已失效」。
// 自动同步默认一天一次太稀疏，这里安排 2 小时后重试，最多 3 次（约 6 小时）
// 仍失败才标记失效并提醒用户重新绑定。
func gfSyncRetryOrFail(shop int64, reason string) {
	n := 0
	if v := gfGet(gfKeySyncFails, shop); v != "" {
		n, _ = strconv.Atoi(v)
	}
	n++
	_ = gfSet(gfKeySyncFails, shop, strconv.Itoa(n))
	if n >= 3 {
		gfMarkSyncError(shop, reason)
		return
	}
	_ = gfSet(gfKeyRetryAt, shop, time.Now().Add(2*time.Hour).Format("2006-01-02 15:04:05"))
	_ = gfSet(gfKeySyncErr, shop, fmt.Sprintf("%s，已自动重试 %d/3 次（2 小时后再试）", reason, n))
	fmt.Printf("[闲鱼自动同步] 店铺 %d %s，第 %d 次，2 小时后重试\n", shop, reason, n)
}

// gfKey 把配置键加上店铺后缀。shop<=0 时退回全局键（兼容旧版本留下的数据）。
func gfKey(base string, shop int64) string {
	if shop <= 0 {
		return base
	}
	return base + "@" + strconv.FormatInt(shop, 10)
}

func gfGet(base string, shop int64) string { return getSetting(gfKey(base, shop)) }

func gfSet(base string, shop int64, v string) error { return setSetting(gfKey(base, shop), v) }

// gfCanRecoverSession 判断当前配置是否具备后台自愈条件。
func gfCanRecoverSession(shop int64) bool {
	return gfKeepAliveEnabled() && strings.TrimSpace(gfGet(gfKeySSOTaobao, shop)) != ""
}

// gfMigrateGlobal 把旧版全局绑定搬到默认店铺上，避免升级后要求重新粘 Cookie。
func gfMigrateGlobal() {
	raw := getSetting(gfKeyCookie)
	if strings.TrimSpace(raw) == "" {
		return
	}
	if strings.TrimSpace(gfGet(gfKeyCookie, defaultShopID)) != "" {
		return
	}
	_ = gfSet(gfKeyCookie, defaultShopID, raw)
	_ = gfSet(gfKeyNick, defaultShopID, getSetting(gfKeyNick))
	_ = gfSet(gfKeyUID, defaultShopID, getSetting(gfKeyUID))
	_ = gfSet(gfKeySaved, defaultShopID, getSetting(gfKeySaved))
	_ = gfSet(gfKeyLast, defaultShopID, getSetting(gfKeyLast))
	// 清掉全局键，避免下次启动又搬一次
	for _, k := range []string{gfKeyCookie, gfKeyNick, gfKeyUID, gfKeySaved, gfKeyLast} {
		setSetting(k, "")
	}
}

func gfSavedClient(shop int64) *gfClient {
	raw := gfGet(gfKeyCookie, shop)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return newGFClient(raw)
}

func gfMaskCookie(raw string) string {
	c := newGFClient(raw)
	n := len(c.cookies)
	if n == 0 {
		return ""
	}
	uid := c.cookieVal("unb")
	if uid == "" {
		return fmt.Sprintf("%d 项凭证", n)
	}
	if len(uid) > 4 {
		uid = uid[:2] + "***" + uid[len(uid)-2:]
	}
	return fmt.Sprintf("%d 项凭证 · 账号 %s", n, uid)
}

// ---------- 状态 ----------

func handleGoofishStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "方法不允许")
		return
	}
	shop := shopID(r)
	raw := gfGet(gfKeyCookie, shop)
	writeJSON(w, map[string]interface{}{
		"shop_id":     shop,
		"shop_name":   shopNameOf(shop),
		"logged_in":   strings.TrimSpace(raw) != "",
		"nick":        gfGet(gfKeyNick, shop),
		"uid":         gfGet(gfKeyUID, shop),
		"masked":      gfMaskCookie(raw),
		"saved_at":    gfGet(gfKeySaved, shop),
		"last_sync":   gfGet(gfKeyLast, shop),
		"last_check":  gfGet(gfKeyCheck, shop),
		"expired":     gfGet(gfKeyDead, shop) == "1",
		"sync_error":  gfGet(gfKeySyncErr, shop),
		"keepalive":   gfKeepAliveEnabled(),
		"auto_days":   gfAutoDays(shop),
		"auto_window": gfAutoWindow(shop),
		"backfill":    gfBackfillEnabled(shop),
		"syncing":     gfIsSyncing(shop),
		"sso_saved":   strings.TrimSpace(gfGet(gfKeySSOTaobao, shop)) != "",
	})
}

// handleGoofishRebind POST /api/goofish/rebind
//
// 一键绑定工具专用：除了闲鱼域的 Cookie，还一起收下淘宝 / 天猫域的 SSO Cookie。
// 闲鱼域的走原有绑定校验；SSO Cookie 原样存起来，供服务器端静默换发实验与自动续期。
// Cookie 只存本地数据库，不回显。
func handleGoofishRebind(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	var in struct {
		Goofish   string `json:"goofish"`
		Taobao    string `json:"taobao"`
		UmidToken string `json:"umid_token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&in); err != nil {
		writeError(w, 400, "请求体解析失败")
		return
	}
	shop := shopID(r)

	// 1) 闲鱼 Cookie：与手动绑定同一套校验（bought.list 验证 h5 登录态）
	gfRaw := strings.TrimSpace(in.Goofish)
	if gfRaw == "" {
		writeError(w, 400, "没有读到闲鱼 Cookie，请确认浏览器里已完成登录")
		return
	}
	c := newGFClient(gfRaw)
	if len(c.cookies) < 3 {
		writeError(w, 400, "闲鱼 Cookie 内容不完整，请确认登录完成后再试")
		return
	}
	if _, err := c.boughtOrders(1); err != nil {
		writeError(w, 400, "闲鱼登录态校验失败："+gfExplainErr(err))
		return
	}
	acc, err := c.account()
	if err != nil {
		writeError(w, 400, "闲鱼登录态校验失败："+err.Error())
		return
	}
	if err := gfSet(gfKeyCookie, shop, c.cookieString()); err != nil {
		writeError(w, 500, "保存失败")
		return
	}
	_ = gfSet(gfKeyNick, shop, acc.Nick)
	if acc.UID != "" {
		_ = gfSet(gfKeyUID, shop, acc.UID)
	}
	_ = gfSet(gfKeySaved, shop, time.Now().Format("2006-01-02 15:04:05"))
	_ = gfSet(gfKeyCheck, shop, time.Now().Format("2006-01-02 15:04:05"))
	gfClearSyncError(shop)
	if !gfKeepAliveEnabled() {
		setSetting(gfKeepAliveKey, "1")
	}

	// 2) 淘宝 SSO Cookie：原样保存（关键：静默换发靠它）
	ssoSaved := false
	if tb := strings.TrimSpace(in.Taobao); tb != "" {
		if gfSet(gfKeySSOTaobao, shop, tb) == nil {
			ssoSaved = true
		}
	}

	// 3) 阿里设备指纹：扫码登录二维码必须带，否则闲鱼判 EXPIRED
	if umid := strings.TrimSpace(in.UmidToken); umid != "" {
		_ = gfSet(gfKeyUmidToken, shop, umid)
	}

	writeJSON(w, map[string]interface{}{
		"ok": true, "nick": acc.Nick, "uid": acc.UID, "shop_id": shop,
		"sso_saved": ssoSaved,
	})
}

func handleGoofishLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	shop := shopID(r)
	var in struct {
		Cookie string `json:"cookie"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, 400, "请求体解析失败")
		return
	}
	raw := strings.TrimSpace(in.Cookie)
	if raw == "" {
		writeError(w, 400, "请粘贴 Cookie")
		return
	}
	c := newGFClient(raw)
	if len(c.cookies) < 3 {
		writeError(w, 400, "Cookie 内容太短，请确认复制完整（应包含 _m_h5_tk、cookie2、unb 等）")
		return
	}
	// 校验用与同步同源的「我买到的」列表接口，而不是 PC 端接口：
	// 两端登录 cookie 的有效期不同，PC 活着不代表同步能用。
	// 绑定即验证 h5 登录态，避免绑了个「马上就会失效」的凭证。
	if _, err := c.boughtOrders(1); err != nil {
		writeError(w, 400, "登录态校验失败："+gfExplainErr(err))
		return
	}
	acc, err := c.account()
	if err != nil {
		writeError(w, 400, "登录态校验失败："+err.Error())
		return
	}
	// 校验通过：把刷新过的 cookie（_m_h5_tk 已换新）存下来
	if err := gfSet(gfKeyCookie, shop, c.cookieString()); err != nil {
		writeError(w, 500, "保存失败")
		return
	}
	_ = gfSet(gfKeyNick, shop, acc.Nick)
	_ = gfSet(gfKeyUID, shop, acc.UID)
	_ = gfSet(gfKeySaved, shop, time.Now().Format("2006-01-02 15:04:05"))
	_ = gfSet(gfKeyCheck, shop, time.Now().Format("2006-01-02 15:04:05"))
	gfClearSyncError(shop)
	// 失效时保活会被自动关掉；重新绑定说明用户想要继续自动同步，把保活恢复
	if !gfKeepAliveEnabled() {
		setSetting(gfKeepAliveKey, "1")
	}
	writeJSON(w, map[string]interface{}{"ok": true, "nick": acc.Nick, "uid": acc.UID, "shop_id": shop})
}

func handleGoofishLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	shop := shopID(r)
	for _, k := range []string{gfKeyCookie, gfKeyNick, gfKeyUID, gfKeySaved, gfKeyLast, gfKeyCheck, gfKeyDead} {
		_ = gfSet(k, shop, "")
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

// ---------- 拉取预览 ----------

// 同步互斥：同一家店同一时间只允许一次拉取。
// 用户连点、两个标签页同时点、自动同步正好撞上手动同步，都靠这把锁挡掉，
// 避免并发打同一批接口被闲鱼判定成异常流量。
var (
	gfSyncMu   sync.Mutex
	gfSyncBusy = map[int64]time.Time{}
)

func gfTryLockSync(shop int64) bool {
	gfSyncMu.Lock()
	defer gfSyncMu.Unlock()
	if t, ok := gfSyncBusy[shop]; ok && time.Since(t) < 3*time.Minute {
		return false
	}
	gfSyncBusy[shop] = time.Now()
	return true
}

func gfUnlockSync(shop int64) {
	gfSyncMu.Lock()
	delete(gfSyncBusy, shop)
	gfSyncMu.Unlock()
}

func gfIsSyncing(shop int64) bool {
	gfSyncMu.Lock()
	defer gfSyncMu.Unlock()
	t, ok := gfSyncBusy[shop]
	return ok && time.Since(t) < 3*time.Minute
}

type gfPlanCache struct {
	mu    sync.Mutex
	items map[int64]*gfPlan
}

var gfPlans = &gfPlanCache{items: map[int64]*gfPlan{}}

func (c *gfPlanCache) put(shop int64, p *gfPlan) {
	c.mu.Lock()
	c.items[shop] = p
	c.mu.Unlock()
}

func (c *gfPlanCache) get(shop int64) *gfPlan {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.items[shop]
}

// handleGoofishPreview 拉全量 + 对账，返回计划（不写库）
func handleGoofishPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	shop := shopID(r)
	if !gfTryLockSync(shop) {
		writeError(w, 429, "正在同步中，请等这一次跑完再点；频繁点不会更快，反而容易触发闲鱼风控")
		return
	}
	defer gfUnlockSync(shop)

	var in struct {
		BackfillMonths int `json:"backfill_months"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	gfProgress(shop, 5, "正在刷新登录态…")
	// 换发失败不能直接阻断：旧会话可能仍然有效，换发响应也可能已经落库了新凭证。
	c, _ := gfRenewAndLoadClient(shop, gfRenewBeforeSync)
	if c == nil {
		writeError(w, 401, "这家店还没有绑定闲鱼账号，请先绑定")
		return
	}
	plan, err := buildPlanWith(c, 0, in.BackfillMonths, shop)
	if err != nil {
		msg := err.Error()
		sessionGone := strings.Contains(msg, "SESSION_EXPIRED") || strings.Contains(msg, "Session过期") ||
			strings.Contains(msg, "FAIL_SYS_SESSION_EXPIRED")
		if sessionGone {
			_ = gfSet(gfKeyDead, shop, "1")
			if gfKeepAliveEnabled() && !gfCanRecoverSession(shop) {
				setSetting(gfKeepAliveKey, "0")
			}
		}
		writeError(w, 502, gfExplainErr(err))
		return
	}
	// 回写刷新后的 cookie，避免过期
	_ = gfSet(gfKeyCookie, shop, c.cookieString())
	_ = gfSet(gfKeyCheck, shop, time.Now().Format("2006-01-02 15:04:05"))
	gfClearSyncError(shop)
	gfPlans.put(shop, plan)

	writeJSON(w, plan)
}

// handleGoofishApply 把计划里选中的订单写进当前店铺
func handleGoofishApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	shop := shopID(r)
	var in struct {
		PlanID  string   `json:"plan_id"`
		ShopID  int64    `json:"shop_id"`
		OrderNo []string `json:"order_nos"`
		// Refresh 是否按最新账单补正「已在库」记录的金额。
		// 前端默认开启：卖出价 / 服务费 / 无忧卖 / 到手价都按账单走。
		// 注意：只有金额与备注会变，联单关联、完结、弃单状态永远不会被同步改动。
		Refresh bool `json:"refresh"`
		// RefreshOrderNos 预览页逐条勾选要补正的订单号；为空表示补正全部可补正项。
		RefreshOrderNos []string `json:"refresh_order_nos"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeError(w, 400, "请求体解析失败")
		return
	}
	plan := gfPlans.get(shop)
	if plan == nil || plan.ID != in.PlanID {
		writeError(w, 409, "同步计划已失效，请重新拉取")
		return
	}
	if time.Since(plan.MadeAt) > 30*time.Minute {
		writeError(w, 409, "同步计划已过期（超过 30 分钟），请重新拉取")
		return
	}
	// 店铺级绑定：只允许写进本店，避免 A 店的账号把货导到 B 店
	if in.ShopID > 0 && in.ShopID != shop {
		writeError(w, 400, "只能导入到当前店铺")
		return
	}
	// 补正只在用户勾选「覆盖」时执行。默认不动任何已在库的记录，
	// 这样手动改过的金额、自己核对过的数据都不会被同步冲掉。
	refreshed, keptManual := 0, 0
	if in.Refresh {
		var only map[string]bool
		if len(in.RefreshOrderNos) > 0 {
			only = map[string]bool{}
			for _, no := range in.RefreshOrderNos {
				only[no] = true
			}
		}
		refreshed, keptManual = gfRefreshBilled(shop, plan.Rows, only)
	}

	want := map[string]bool{}
	for _, s := range in.OrderNo {
		want[s] = true
	}
	if len(want) == 0 {
		if refreshed > 0 {
			refreshPurchaseIndex()
			_ = gfSet(gfKeyLast, shop, time.Now().Format("2006-01-02 15:04:05"))
			writeJSON(w, map[string]interface{}{"ok": true, "added": 0, "skipped": 0,
				"refreshed": refreshed, "kept_manual": keptManual})
			return
		}
		if in.Refresh {
			// 勾了「按账单补正」但金额已经与账单一致：这不是错误，明确告诉用户已是最新。
			refreshPurchaseIndex()
			_ = gfSet(gfKeyLast, shop, time.Now().Format("2006-01-02 15:04:05"))
			writeJSON(w, map[string]interface{}{"ok": true, "added": 0, "skipped": 0,
				"refreshed": 0, "kept_manual": keptManual, "message": "账单已是最新，没有需要补正的金额"})
			return
		}
		writeError(w, 400, "没有选中任何订单")
		return
	}

	ts := nowStr()
	added, skipped := 0, 0
	var ids []int64
	existing := gfExistingOrders(shop)
	for _, row := range plan.Rows {
		if row.SkipReason != "" {
			continue
		}
		if !want[row.OrderNo] {
			continue
		}
		// 预览之后可能又点了一次导入（或自动同步同时跑）：写库前再查一次订单号，
		// 已经在库里就跳过，保证幂等。之前只靠预览时的 exists 标记，连点两次会插两份。
		key := row.Side + "|" + row.OrderNo
		if existing[key] {
			skipped++
			continue
		}
		id, err := insertItem(gfItemOf(row), shop, ts)
		if err != nil {
			skipped++
			continue
		}
		ids = append(ids, id)
		existing[key] = true
		added++
	}
	refreshPurchaseIndex()
	_ = gfSet(gfKeyLast, shop, time.Now().Format("2006-01-02 15:04:05"))
	realtimeHub.publish("POST", "/api/goofish/apply", "", fmt.Sprintf("从闲鱼同步了 %d 条订单", added), shop)
	writeJSON(w, map[string]interface{}{"ok": true, "added": added, "skipped": skipped,
		"refreshed": refreshed, "kept_manual": keptManual, "ids": ids})
}

// gfExistingOrders 一次取出本店所有已入库的订单号（买/卖两侧）。
// 导入时逐行查库是 N 次往返，几十上百单会很慢；这里一次查询换成一个 set。
func gfExistingOrders(shop int64) map[string]bool {
	out := map[string]bool{}
	rows, err := db.Query(`SELECT sell_order_no, buy_order_no FROM items
        WHERE shop_id=? AND (sell_order_no<>'' OR buy_order_no<>'')`, shop)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var sell, buy string
		if rows.Scan(&sell, &buy) == nil {
			if sell != "" {
				out["sell|"+sell] = true
			}
			if buy != "" {
				out["buy|"+buy] = true
			}
		}
	}
	return out
}

// gfOrderExists 该店铺里是否已有这个订单号（卖单看 sell_order_no，买单看 buy_order_no）
func gfOrderExists(shop int64, orderNo, side string) bool {
	col := "sell_order_no"
	if side == "buy" {
		col = "buy_order_no"
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM items WHERE shop_id=? AND "+col+"=?", shop, orderNo).Scan(&n)
	return n > 0
}

// gfRefreshBilled 把「已在库、但当时账单没生成」的卖单按最新账单补正。
// 按账单补正已有卖单：只改「卖出价 / 服务费 / 无忧卖 / 到手价 / 备注」。
// 不再跳过联单关联、已完结、已弃单的记录——金额该对就对，但状态一律不碰。
// only 非空时只处理这些订单号（预览页逐条勾选覆盖）。
func gfRefreshBilled(shop int64, rows []gfPlanRow, only map[string]bool) (changed, kept int) {
	for _, r := range rows {
		if r.Side != "sell" || r.SkipReason != "" || !r.Exists {
			continue
		}
		if only != nil && !only[r.OrderNo] {
			continue
		}
		// 发货时间与账单无关：老记录只要这次拿到了 consignTime 就补上，
		// 支付宝快递费要靠它精确对单（差 2~5 秒），漏掉就只能靠时间猜。
		if r.ConsignTime != "" {
			db.Exec(`UPDATE items SET consign_time=?, updated_at=?
                WHERE shop_id=? AND sell_order_no=? AND (consign_time='' OR consign_time IS NULL)`,
				r.ConsignTime, nowStr(), shop, r.OrderNo)
		}
		if !r.Billed {
			continue
		}
		// 金额没变的不写库，避免无谓的 updated_at 变动。
		var price, fee, wuyou float64
		var cnt int
		if e := db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(sell_price),0), COALESCE(MAX(sell_fee),0), COALESCE(MAX(sell_wuyou_fee),0)
            FROM items WHERE shop_id=? AND sell_order_no=?`, shop, r.OrderNo).Scan(&cnt, &price, &fee, &wuyou); e != nil || cnt == 0 {
			continue
		}
		if absf(price-r.Price) < 0.005 && absf(fee-r.Fee) < 0.005 && absf(wuyou-r.Wuyou) < 0.005 {
			kept++
			continue
		}
		// 用户手动改过的字段（归属 owner、运费标签 buy/sell_ship_tag、货物来源 source）
		// 一律不在自动同步里出现：同步只补价格、服务费与备注，避免覆盖人工整理。
		note := fmt.Sprintf("闲鱼同步；服务费已按支出账单核对（无忧卖 %.2f / 服务费 %.2f）", r.Wuyou, r.Fee)
		// SET 里只有金额与备注：parent_id（联单关联）、settled_at（完结）、discarded_at（弃单）一个都不碰。
		// 到手价与明细页同口径：成交价 − 服务费 − 无忧卖 − 卖运费（运费可能来自支付宝导入）
		res, e := db.Exec(`UPDATE items SET sell_price=?, sell_fee=?, sell_wuyou_fee=?,
            sell_net=round(? - ? - ? - sell_shipping, 2),
            note=CASE WHEN note='' OR note LIKE '闲鱼同步%' THEN ? ELSE note END, updated_at=?
            WHERE shop_id=? AND sell_order_no=?`,
			r.Price, r.Fee, r.Wuyou, r.Price, r.Fee, r.Wuyou, note, nowStr(), shop, r.OrderNo)
		if e == nil {
			if n, _ := res.RowsAffected(); n > 0 {
				changed += int(n)
			}
		}
	}
	return
}
func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// gfItemOf 把同步行转成待入库记录
func gfItemOf(row gfPlanRow) *Item {
	note := "从闲鱼同步"
	switch {
	case row.Side == "sell" && row.Backfill && row.Billed:
		note = fmt.Sprintf("闲鱼账单补建（列表接口只回最近 40 单）；服务费已按账单核对（无忧卖 %.2f / 服务费 %.2f）", row.Wuyou, row.Fee)
	case row.Side == "sell" && row.Backfill:
		note = "闲鱼账单补建（列表接口只回最近 40 单）；该月账单尚未生成，服务费待补"
	case row.Side == "sell" && row.Billed:
		note = fmt.Sprintf("闲鱼同步；服务费已按支出账单核对（无忧卖 %.2f / 服务费 %.2f）", row.Wuyou, row.Fee)
	case row.Side == "sell":
		note = "闲鱼同步；该单账单尚未生成，到手暂未扣服务费，之后同步会自动更正"
	case row.Side == "buy":
		note = "闲鱼同步（我买到的）"
	}
	// 闲鱼同步进来的货，来源固定标「闲鱼」；买进侧才有货物来源的概念，卖出侧跟随其买进单。
	it := &Item{Product: row.Product, Quantity: 1, Note: note, Source: "闲鱼"}
	if row.Side == "buy" {
		it.BuyPrice = row.Price
		it.BuyOrderNo = row.OrderNo
		it.BuyTime = row.Time
		it.BuyCounterparty = row.Peer
		// 买运费：预留字段 PostFee（商品邮费 POST_FEE）。开关关闭时恒为 0，
		// 与原先的默认值一致，不改变现有行为；开关打开后非0邮费会写进买运费。
		it.BuyShipping = row.PostFee
		if row.PostFee > 0 {
			it.BuyShipTag = "商品邮费"
		} else {
			it.BuyShipTag = "包邮"
		}
	} else {
		it.SellPrice = row.Price
		it.SellFee = row.Fee
		it.SellWuyouFee = row.Wuyou
		it.SellNet = row.Net
		it.SellOrderNo = row.OrderNo
		it.SellTime = row.Time
		it.ConsignTime = row.ConsignTime
		it.SellCounterparty = row.Peer
		it.SellShipTag = "买家承担"
	}
	return it
}

// ---------- 会话保活 ----------

const (
	// 心跳间隔取 40 分钟 ~ 5 小时 40 分钟 之间的随机值。
	// 固定周期的心跳本身就是一种特征：每天都是整点、间隔分秒不差，最容易被判成脚本。
	// 随机化之后既能把令牌续上，又不会显得比真人有规律。
	gfKeepAliveKey   = "goofish_keepalive"
	gfAutoDaysKey    = "goofish_auto_days"
	gfAutoWindowKey  = "goofish_auto_window"
	gfAutoTickPeriod = 10 * time.Minute
)

func gfKeepAliveEnabled() bool {
	v := getSetting(gfKeepAliveKey)
	return v == "" || v == "1" // 默认开启
}

func handleGoofishKeepAlive(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	var in struct {
		On bool `json:"on"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in)
	v := "0"
	if in.On {
		v = "1"
	}
	_ = setSetting(gfKeepAliveKey, v)
	writeJSON(w, map[string]interface{}{"ok": true, "on": in.On})
}

// handleGoofishPing 手动探一次登录态，给界面上的「检测连接」用
func handleGoofishPing(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	shop := shopID(r)
	nick, err := gfPingShop(shop)
	if err != nil {
		reason := gfExplainErr(err)
		if gfSessionExpired(err) {
			writeError(w, 400, "连接已失效："+reason+"，请重新绑定")
		} else {
			writeError(w, 502, "连接检测失败："+reason+"，请稍后重试")
		}
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "nick": nick})
}

func gfSessionExpired(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SESSION_EXPIRED") ||
		strings.Contains(msg, "Session过期") ||
		strings.Contains(msg, "FAIL_SYS_SESSION_EXPIRED")
}

// gfExplainErr 把 mtop 的 ret 串翻译成人能看懂的原因。
// 闲鱼登录态由 cookie2 / sgcookie / unb 这几个长期凭证决定，
// _m_h5_tk 只是签名令牌——保活能续令牌，续不了登录态本身。
func gfExplainErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "SESSION_EXPIRED"), strings.Contains(msg, "Session过期"), strings.Contains(msg, "FAIL_SYS_SESSION_EXPIRED"):
		return "登录态已过期：闲鱼那边的登录凭证（cookie2）失效了，重新粘贴一次 Cookie 就能恢复"
	case strings.Contains(msg, "FAIL_SYS_TOKEN_EXOIRED"), strings.Contains(msg, "TOKEN_EXOIRED"):
		return "签名令牌过期：先点一次「检测连接」刷新令牌，再重试"
	case strings.Contains(msg, "FAIL_SYS_ILLEGAL_ACCESS"), strings.Contains(msg, "USER_VALIDATE"):
		return "被闲鱼风控拦下了：这次请求被判成异常，建议先在手机上正常用一会儿再试"
	case strings.Contains(msg, "FAIL_BIZ_IDLE_USER_UNAUTHORIZED"):
		return "账号未被识别为闲鱼卖家身份，确认登的是卖家账号"
	}
	return msg
}

// gfPingShop 探测登录态，同时把刷新后的 _m_h5_tk 存回去。
//
// 2026-11-18 修正：以前用 PC 端接口（mtop.taobao.idlemessage.pc.loginuser.get）探测，
// 但同步真正用的是 h5 端「我买到的」列表（mtop.idle.web.trade.bought.list）。
// 实测出现过「心跳一直成功、同步却报 Session过期」——两端的登录 cookie 有效期不同，
// PC 接口活着不代表 h5 登录态活着，心跳等于白保。
// 现在主探测直接拉「我买到的」第 1 页：既探测同步真正依赖的登录态，
// 也顺便带着 cookie2 定期活跃它，最大程度延续同步凭证的寿命。
// account() 只用来拿昵称，失败不判死（它不是同步的关键路径）。
func gfPingShop(shop int64) (string, error) {
	c := gfSavedClient(shop)
	if c == nil {
		return "", fmt.Errorf("这家店还没有绑定闲鱼账号")
	}
	// 主探测：与同步同源的接口
	if _, err := c.boughtOrders(1); err != nil {
		// 手动检测是用户主动发起的恢复动作：如果绑定过淘宝 SSO，
		// 先静默换发一次再重试，避免会话刚过期就要求用户重新扫码/粘贴。
		if strings.TrimSpace(gfGet(gfKeySSOTaobao, shop)) != "" {
			if _, renewErr := gfSilentRenew(shop); renewErr != nil {
				fmt.Printf("[闲鱼检测] 店铺 %d 静默换发失败: %v\n", shop, renewErr)
			}
			// 无论业务码如何都使用落库后的最新会话重试；换发响应可能已带来新凭证。
			if fresh := gfSavedClient(shop); fresh != nil {
				c = fresh
				if _, retryErr := c.boughtOrders(1); retryErr == nil {
					err = nil
				} else {
					err = retryErr
				}
			}
		}
		if err != nil {
			// 只有确认是会话过期才关闭保活；网络波动、风控拦截等临时错误
			// 不应把 Cookie 标成永久失效。
			if gfSessionExpired(err) {
				_ = gfSet(gfKeyDead, shop, "1")
				if gfKeepAliveEnabled() && !gfCanRecoverSession(shop) {
					setSetting(gfKeepAliveKey, "0")
				}
			}
			return "", err
		}
	}
	_ = gfSet(gfKeyCookie, shop, c.cookieString())
	_ = gfSet(gfKeyCheck, shop, time.Now().Format("2006-01-02 15:04:05"))
	gfClearSyncError(shop)

	// 昵称：优先 cookie 里的 tracknick / _nk_，拿不到再走 PC 接口（失败不算失效）
	nick := c.cookieVal("tracknick")
	if nick == "" {
		nick = c.cookieVal("_nk_")
	}
	// 昵称和 UID 都已保存时不再额外请求账号接口；检测连接保持轻量。
	if nick == "" || gfGet(gfKeyUID, shop) == "" {
		if acc, err := c.account(); err == nil && acc != nil {
			if nick == "" {
				nick = acc.Nick
			}
			if acc.UID != "" {
				_ = gfSet(gfKeyUID, shop, acc.UID)
			}
		}
	}
	if nick != "" {
		_ = gfSet(gfKeyNick, shop, nick)
	}
	return nick, nil
}

// gfBackfillEnabled 立即同步时是否一并做账单回溯。默认开（沿用老行为），
// 只有明确存了 "0" 才关。按店铺持久化，避免刷新页面后开关被重置。
func gfBackfillEnabled(shop int64) bool {
	return strings.TrimSpace(gfGet(gfKeyBackfill, shop)) != "0"
}

// gfAutoDays 自动同步间隔（天）。0=关闭，取值 1..30。
func gfAutoDays(shop int64) int {
	v := getSetting(gfKey(gfAutoDaysKey, shop))
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return 0
	}
	if n > 30 {
		n = 30
	}
	return n
}

// gfAutoWindow 自动同步只处理「最近 N 天」的订单。默认 30 天。
// 目的是让自动同步保持轻量：老订单不会每天重复参与对账，漏的历史靠手动同步的账单回溯补。
func gfAutoWindow(shop int64) int {
	v := getSetting(gfKey(gfAutoWindowKey, shop))
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 30
	}
	if n > 365 {
		n = 365
	}
	return n
}

func handleGoofishAuto(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	shop := shopID(r)
	var in struct {
		Days     int   `json:"days"`
		Window   int   `json:"window"`
		Backfill *bool `json:"backfill"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in)
	d := in.Days
	if d < 0 {
		d = 0
	}
	if d > 30 {
		d = 30
	}
	_ = gfSet(gfAutoDaysKey, shop, strconv.Itoa(d))
	// window 是「只同步最近 N 天」；不传就不改，避免旧客户端把它清掉
	if in.Window > 0 {
		if in.Window > 365 {
			in.Window = 365
		}
		_ = gfSet(gfAutoWindowKey, shop, strconv.Itoa(in.Window))
	}
	// backfill 是「立即同步时一并做账单回溯」开关；不传就不改
	if in.Backfill != nil {
		v := "1"
		if !*in.Backfill {
			v = "0"
		}
		_ = gfSet(gfKeyBackfill, shop, v)
	}
	writeJSON(w, map[string]interface{}{"ok": true, "days": d, "window": gfAutoWindow(shop), "backfill": gfBackfillEnabled(shop)})
}

// ---------- 自动同步 ----------

// goofishAutoSyncLoop 按店铺设置的间隔自动跑一次「拉取 + 导入」。
// 只在服务端做，不依赖有没有人打开页面；每次 tick 只判断是否到期，不做额外请求。
func goofishAutoSyncLoop() {
	time.Sleep(90 * time.Second) // 让启动期的其他任务先跑完
	for {
		rows, err := db.Query("SELECT key FROM settings WHERE key LIKE 'goofish_auto_days@%' AND value<>'' AND value<>'0'")
		if err == nil {
			shops := make([]int64, 0, 4)
			for rows.Next() {
				var k string
				if rows.Scan(&k) == nil {
					if i := strings.LastIndex(k, "@"); i > 0 {
						if id, e := strconv.ParseInt(k[i+1:], 10, 64); e == nil {
							shops = append(shops, id)
						}
					}
				}
			}
			rows.Close()
			for _, shop := range shops {
				gfAutoSyncOne(shop)
			}
		}
		time.Sleep(gfAutoTickPeriod)
	}
}

const gfSessionMaintainPeriod = 6 * time.Hour

// goofishSessionMaintainLoop 独立于订单同步维护登录态。
// 即使自动同步设为 7 天一次，也会每 6 小时尝试一次静默换发和同源接口校验。
func goofishSessionMaintainLoop() {
	time.Sleep(2 * time.Minute)
	for {
		gfMaintainAllSessions()
		time.Sleep(gfSessionMaintainPeriod)
	}
}

func gfMaintainAllSessions() {
	if !gfKeepAliveEnabled() {
		return
	}
	rows, err := db.Query("SELECT key FROM settings WHERE key LIKE 'goofish_cookie@%' AND value<>''")
	if err != nil {
		return
	}
	shops := make([]int64, 0, 4)
	for rows.Next() {
		var k string
		if rows.Scan(&k) != nil {
			continue
		}
		i := strings.LastIndex(k, "@")
		if i <= 0 {
			continue
		}
		id, e := strconv.ParseInt(k[i+1:], 10, 64)
		if e != nil {
			continue
		}
		shops = append(shops, id)
	}
	rows.Close()
	for _, shop := range shops {
		gfMaintainSessionOne(shop)
	}
}

func gfMaintainSessionOne(shop int64) {
	if strings.TrimSpace(gfGet(gfKeyCookie, shop)) == "" ||
		strings.TrimSpace(gfGet(gfKeySSOTaobao, shop)) == "" ||
		!gfKeepAliveEnabled() {
		return
	}
	if !gfTryLockSync(shop) {
		return
	}
	defer gfUnlockSync(shop)

	c, renewed := gfRenewAndLoadClient(shop, gfRenewBeforeSync)
	if c == nil {
		return
	}
	if _, err := c.boughtOrders(1); err != nil {
		if gfSessionExpired(err) {
			gfSyncRetryOrFail(shop, "自动续期后登录态仍失效")
		} else {
			fmt.Printf("[闲鱼续期] 店铺 %d 本次校验失败，保留现有会话稍后重试: %v\n", shop, err)
		}
		return
	}
	_ = gfSet(gfKeyCookie, shop, c.cookieString())
	_ = gfSet(gfKeyCheck, shop, time.Now().Format("2006-01-02 15:04:05"))
	gfClearSyncError(shop)
	if renewed {
		fmt.Printf("[闲鱼续期] 店铺 %d 登录态已刷新并校验通过\n", shop)
	}
}

func gfAutoSyncOne(shop int64) {
	days := gfAutoDays(shop)
	if days <= 0 {
		return
	}
	// 没有恢复凭证时，已判失效的店铺跳过；有淘宝 SSO 时继续让同步链路尝试自愈。
	if gfGet(gfKeyDead, shop) == "1" && !gfCanRecoverSession(shop) {
		return
	}
	// 有排队的重试且已到点：无视日常间隔，立刻再试一次
	forceRetry := false
	if v := gfGet(gfKeyRetryAt, shop); v != "" {
		if t, e := time.ParseInLocation("2006-01-02 15:04:05", v, time.Local); e == nil {
			if time.Now().Before(t) {
				return
			}
			forceRetry = true
		}
	}
	// 距上次同步不足间隔就跳过；从没同步过则立刻跑一次
	if !forceRetry {
		if last := gfGet(gfKeyLast, shop); last != "" {
			if t, e := time.ParseInLocation("2006-01-02 15:04:05", last, time.Local); e == nil {
				if time.Since(t) < time.Duration(days)*24*time.Hour {
					return
				}
			}
		}
	}
	if !gfTryLockSync(shop) {
		return
	}
	defer gfUnlockSync(shop)

	// 同步前静默换发并重新加载落库后的最新会话。
	// 换发失败不直接判死：旧会话可能仍有效，最终以真实同步接口结果为准。
	c, _ := gfRenewAndLoadClient(shop, gfRenewBeforeSync)
	if c == nil {
		gfSyncRetryOrFail(shop, "未找到可用的闲鱼登录态")
		return
	}
	plan, err := buildPlanWith(c, 0, 2, shop)
	if err != nil {
		msg := err.Error()
		sessionGone := strings.Contains(msg, "SESSION_EXPIRED") || strings.Contains(msg, "Session过期") ||
			strings.Contains(msg, "FAIL_SYS_SESSION_EXPIRED") || strings.Contains(msg, "FAIL_SYS_TOKEN_EXOIRED")
		if sessionGone {
			gfSyncRetryOrFail(shop, "Cookie 已失效")
		}
		fmt.Printf("[闲鱼自动同步] 店铺 %d 拉取失败: %v\n", shop, err)
		return
	}
	_ = gfSet(gfKeyCookie, shop, c.cookieString())
	_ = gfSet(gfKeyCheck, shop, time.Now().Format("2006-01-02 15:04:05"))

	// 只处理「最近 N 天」：自动同步每天跑，没必要把几个月前的单再对一遍账，
	// 既省请求也避免历史数据被无谓地更新。漏掉的老单靠手动同步的「账单基准回溯」补。
	window := gfAutoWindow(shop)
	cutoff := time.Now().AddDate(0, 0, -window)
	inWindow := func(t string) bool {
		if window <= 0 {
			return true
		}
		tm, e := time.ParseInLocation("2006-01-02 15:04:05", t, time.Local)
		if e != nil {
			return true // 时间解析不了就别拦，宁可多对一次
		}
		return !tm.Before(cutoff)
	}
	recent := make([]gfPlanRow, 0, len(plan.Rows))
	for _, row := range plan.Rows {
		if inWindow(row.Time) {
			recent = append(recent, row)
		}
	}

	added, refreshed := 0, 0
	ts := nowStr()
	existing := gfExistingOrders(shop)
	for _, row := range recent {
		if row.SkipReason != "" || row.Exists {
			continue
		}
		key := row.Side + "|" + row.OrderNo
		if existing[key] {
			continue // 手动导入可能刚好跑完，避免同一单写两份
		}
		if _, e := insertItem(gfItemOf(row), shop, ts); e == nil {
			existing[key] = true
			added++
		}
	}
	if ch, _ := gfRefreshBilled(shop, recent, nil); ch > 0 {
		refreshed = ch
	}
	refreshPurchaseIndex()
	_ = gfSet(gfKeyLast, shop, time.Now().Format("2006-01-02 15:04:05"))
	gfClearSyncError(shop)
	if added > 0 || refreshed > 0 {
		realtimeHub.publish("POST", "/api/goofish/auto", "",
			fmt.Sprintf("自动同步：新增 %d 条、补正 %d 条", added, refreshed), shop)
		fmt.Printf("[闲鱼自动同步] 店铺 %d 新增 %d 条、补正 %d 条\n", shop, added, refreshed)
	}
}
