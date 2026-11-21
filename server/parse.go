package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// 常见 OCR 噪声：¥ 被识别成 x / y / xy / Y / ￥ 等
var currencyMisread = regexp.MustCompile(`(?i)([xy￥]{1,2})\s*(\d+\.\d{1,2})`)

// 注意：Go RE2 不支持 (?<!..) / (?!..) 环视，边界由代码判断
// 同时支持「.」与「,」作小数分隔（OCR 常把 ¥120.00 读成 ¥120,00）
var amountRe = regexp.MustCompile(`¥\s*(\d+[.,]\d{1,2})|(\d+[.,]\d{2})`)

// 长数字串：用于定位支付宝交易号（约 28 位，前 8 位为 yyyyMMdd）
var longNumRe = regexp.MustCompile(`\d{20,32}`)

// 用于切分「字段窗口」的关键词（订单页固定文案）
var fieldKeywords = []string{
	"成交价", "商品总价", "无忧卖类目软件服务费", "无忧卖服务费", "无忧卖", "软件服务费", "服务费",
	"预计到手", "预估到手", "实际到账", "到手",
	"运费", "支付优惠", "红包", "订单编号", "订单号", "支付宝交易号", "交易快照", "收货地址", "配送方式",
	"卖家昵称", "买家昵称", "下单时间", "付款时间", "发货时间", "成交时间",
	"服务保障", "退货运费险", "48小时发货", "保障结束", "承诺按时", "联系买家",
	"联系卖家", "查看钱款", "去评价", "更多", "全场免息", "你有", "还能赚", "交易成功",
}

// 提示条锚点（keep-after）：商品标题一定在这些提示文案“之后”（取最后一次出现的位置），
// 单独列词而非整句，是为了兼容 OCR 把「闲鱼」识别成「亲鱼」、把「评价完成」拆开等误差。
// 注意：这里只放“标题确实在提示语之后”的锚点（页面顶部状态条/底部操作条）。
var bannerAfterAnchors = []string{
	"感谢使用", "评价完成", "快给对方", "内完成评价", "完成评价",
	"交易成功", "去评价", "查看钱款", "分享一下",
	// 卖出单「已发货 / 待买家确认收货」状态条：商品标题在其后
	"待买家确认收货", "确认收货", "已发货", "提醒收货", "延长收货",
}

// 评价提示语区块（blank-out）：成交后「这次购物体验如何 / 交易体验满意吗 / 非常满意」
// 这类提示语 OCR 常错当成商品名。关键点：它们**绝不**在商品名之前（闲鱼里永远在页面底部），
// 因此不能用“保留其后内容”的截断——否则会把提示语前面那段真实的商品名一并删掉。
// 正确做法是整段抹除，保留两侧的真实标题。即便 OCR 把「购物体验」读花（体+验仍连续）也能命中。
var bannerSkipAnchors = []string{
	"这次购物体验如何", "购物体验", "交易体验", "体验如何", "满意吗", "非常满意",
}

// 状态栏噪声：手机截图顶部的「01:31画 / 22:55 / HE 5」等，常混进商品名。
// 命中即整段丢弃（只匹配很短的片段，避免误伤正常标题里的时间/型号）。
var statusBarNoiseRe = regexp.MustCompile(`^\d{1,2}[:：]\d{2}[\p{Han}A-Za-z]{0,2}$|^[A-Z]{1,3}\s*\d{1,2}$`)

// 型号片段：纯拉丁+数字+符号但含数字、长度≥5，如「RB5009UG+S+IN」「U7-ProMax」。
// 这类是商品型号，不能因「中文占比不足」丢掉；纯乱码如「闷ReeU」「WE」不含数字会被排除。
var modelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+\-_./]{3,31}$`)

// 片段级噪声词：命中任意一个即整段丢弃（比整句正则抗 OCR 误差）
var pieceNoiseWords = []string{
	"评价", "感谢", "闲鱼", "亲鱼", "请在", "快给", "完成", "交易成功",
	"宝贝可卖", "还能赚", "收货", "支付宝", "全场", "日付", "免息",
	"保障", "发货", "复制", "编号", "订单", "卖家", "买家", "昵称",
	"时间", "服务", "地址", "快照", "联系", "退货", "承诺", "赔付",
	"当前引擎", "本地 OCR", "识别", "去设置", "评价完成", "感谢使用",
	"虽号", "编号", "忆忆", "锡息", "昌", "人人",
	"标题", "宝贝标题", "商品标题", "名称", "描述", "规格", "成色",
	// 紧贴金额的价签（成交价 / 商品总价）不应混进商品名
	"成交价", "商品总价",
	"特别好", "这次交易体验吧", "个人闲置",
	// 手机端长截图 OCR 常把商品卡片下方的小标签/图标读成叠字噪声
	"目目", "口口", "品品", "人人", "下下", "上上",
	// 商品卡片上的黄色营销标签（爆款/预售等）被 OCR 读成的同形噪声
	"爆赤", "爆款", "预售", "碟前", "碟后", "碟赤", "碟亦", "趣前", "趣赤",
	"赤前", "赤亦", "镍前", "镤前", "48小时发货", "退货运费险",
	// 配送方式 / 物流类标签：正常文案或 OCR 同形噪声，都不应进入商品名
	"配送", "上门", "自提", "到付", "包邮", "免邮", "快递", "邮寄", "物流",
	"送货", "自寄", "平邮", "EMS", "垫付", "实付", "上引下", "上引",
	// 成交后「待评价」区块的提示语：即使 banner 锚点没切干净，这些短语/关键词也整体或逐片丢弃
	"非常满意", "这次购物体验如何", "购物体验", "交易体验", "体验如何", "满意吗", "满意", "如何",
}

// 片段级前缀噪声：这些是闲鱼 UI 的类目标签（个人闲置等），可能紧贴商品名
// 出现（OCR 未切分逗号时），必须作为子串剥除，否则会连累整段商品名被丢弃。
var piecePrefixNoise = []string{"个人闲置"}

// cleanPiece 清洗单个片段：去掉首尾标点/括号乱码，返回有效内容
func cleanPiece(s string) string {
	s = strings.TrimSpace(s)
	// 先剥除紧贴商品名的类目标签（如「个人闲置」），避免整段被误丢
	for _, w := range piecePrefixNoise {
		if i := strings.Index(s, w); i >= 0 {
			s = strings.TrimSpace(s[:i]) + strings.TrimSpace(s[i+len(w):])
		}
	}
	// 去掉开头非「中文/字母/数字」的乱码，例如 "(ED"、"| 48"
	s = strings.TrimLeftFunc(s, func(r rune) bool {
		return !(r >= '一' && r <= '鿿') && !(r >= 'a' && r <= 'z') &&
			!(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9')
	})
	s = strings.TrimSpace(strings.Trim(s, "，,。.、:：-|~～ ><“”\""))
	return s
}

// looksLikePrice 判断片段是否像一个金额（被 OCR 误并入标题的金额片段应排除）
func looksLikePrice(s string) bool {
	t := strings.ReplaceAll(s, " ", "")
	if t == "" {
		return false
	}
	// 货币符号 + 数字（¥238 / x3.20 / ￥187 / 型号前的 ¥ 误读）
	if regexp.MustCompile(`[¥xXyY￥]\s*\d`).MatchString(t) {
		return true
	}
	// 形如 238.00 / 213.32 的带两位小数数字
	if regexp.MustCompile(`\d+\.\d{2}`).MatchString(t) {
		return true
	}
	// 金额被遮挡/误读：数字之间夹了 * @ 等杂符（187****21 / 213*）
	if regexp.MustCompile(`\d[*@]\d`).MatchString(t) {
		return true
	}
	return false
}

// amountGarbled 检测金额是否被遮挡或识别不全：金额关键词后紧邻的片段里出现 * @ 等杂符
func amountGarbled(flat string) bool {
	for _, kw := range []string{"成交价", "商品总价", "软件服务费", "服务费", "预计到手", "预估到手", "实际到账", "运费"} {
		i := strings.Index(flat, kw)
		if i < 0 {
			continue
		}
		seg := flat[i+len(kw):]
		if len(seg) > 18 {
			seg = seg[:18]
		}
		if regexp.MustCompile(`[*@]`).MatchString(seg) {
			return true
		}
	}
	return false
}

// isProductPiece 判断片段是否像商品标题的一部分
func isProductPiece(p string) bool {
	if p == "" {
		return false
	}
	// 金额类片段（如被 OCR 误并入标题的「x238.00 / ¥213.32 / 187****21」）一律不算标题，
	// 避免“标题里混进金额”导致标题与金额都错。
	if looksLikePrice(p) {
		return false
	}
	runes := []rune(p)
	hasCJK := regexp.MustCompile(`[\x{4e00}-\x{9fff}]`).MatchString(p)
	// 短片段必须含中文才保留：保留「设备」，排除「ED」「48」等乱码
	if len(runes) < 2 || (len(runes) == 2 && !hasCJK) {
		return false
	}
	// 纯数字 / 纯时间 / 纯符号
	if regexp.MustCompile(`^[\d\s:：.、,，%]+$`).MatchString(p) {
		return false
	}
	for _, w := range pieceNoiseWords {
		if strings.Contains(p, w) {
			return false
		}
	}
	// 状态栏时间 / 电量等噪声（如「01:31画」）不进商品名
	if statusBarNoiseRe.MatchString(p) {
		return false
	}
	// 至少要有中文或字母数字
	hasAlnum := regexp.MustCompile(`[A-Za-z0-9]`).MatchString(p)
	if !hasCJK && !hasAlnum {
		return false
	}
	// 去掉所有标点后仍需有实质内容，避免只剩 "(ED" 之类
	stripped := regexp.MustCompile(`[^\p{L}\p{N}]`).ReplaceAllString(p, "")
	return len([]rune(stripped)) >= 2
}

// extractProduct 从 OCR 文本中提取商品标题
func extractProduct(flat string) string {
	firstMoney := amountRe.FindStringIndex(flat)
	headEnd := len(flat)
	if firstMoney != nil {
		headEnd = firstMoney[0]
	}
	head := flat[:headEnd]

	// 1) 标题在状态条锚点“之后”：取最后一次出现的位置，保留其后内容
	start := 0
	for _, a := range bannerAfterAnchors {
		if i := strings.LastIndex(head, a); i >= 0 {
			if j := i + len(a); j > start {
				start = j
			}
		}
	}
	head = head[start:]
	// 1b) 抹除「待评价」提示语区块（保留两侧真实标题，不能截断其后）
	for _, a := range bannerSkipAnchors {
		head = strings.ReplaceAll(head, a, " ")
	}

	// 2) 商品标题主体来自「金额之前」的 head 区域（闲鱼卡片：商品名紧跟价格）。
	//    金额之后仅作极少量换行续写补充，且必须中文占比过半，否则视为 OCR 垃圾丢弃
	//    （如「闷ReeU」这类纯拉丁乱码，避免混进商品名）。
	tail := ""
	if firstMoney != nil && firstMoney[1] < len(flat) {
		lim := firstMoney[1] + 120
		if lim > len(flat) {
			lim = len(flat)
		}
		tailStr := flat[firstMoney[1]:lim]
		cut := len(tailStr) // 字节索引
		for _, kw := range fieldKeywords {
			if j := strings.Index(tailStr, kw); j >= 0 && j < cut {
				cut = j
			}
		}
		tail = tailStr[:cut]
	}

	// 3) 片段切分 + 噪声过滤，**保留原始分隔符**：
	//    之前把所有片段无脑用「，」拼接，会把「UBNT 优倍快 unifi u7promax金属」
	//    这种空格/换行分隔的连续词打散成「UBNT，优倍快，unifi…」，用户看到就是"标题不对"。
	//    现在：原文是逗号/顿号 → 保留「，」；原文是空格/换行/竖线 → 保留空格。
	type seg struct{ text, sep string }
	splitRe := regexp.MustCompile(`[\s||、，,]+`)
	collect := func(region string) []seg {
		var out []seg
		var raws []string
		var seps []string
		idx := 0
		for _, loc := range splitRe.FindAllStringIndex(region, -1) {
			raws = append(raws, region[idx:loc[0]])
			seps = append(seps, region[loc[0]:loc[1]])
			idx = loc[1]
		}
		raws = append(raws, region[idx:])
		for i, raw := range raws {
			p := cleanPiece(raw)
			if !isProductPiece(p) {
				continue
			}
			sep := ""
			if i < len(seps) {
				if strings.ContainsAny(seps[i], "、，,") {
					sep = "，"
				} else {
					sep = " "
				}
			}
			out = append(out, seg{p, sep})
		}
		return out
	}
	headSegs := collect(head)
	if len(headSegs) > 6 {
		headSegs = headSegs[len(headSegs)-6:] // 靠近金额的最后 6 段（放宽，避免长标题丢前缀）
	}
	// 续写片段必须中文占比过半，否则视为 OCR 噪声丢弃（抗「闷ReeU」类乱码）。
	// 例外：含数字的字母数字型号（如 RB5009UG+S+IN）是真实标题的一部分，必须保留。
	tailSegs := collect(tail)
	var tailKeep []seg
	for _, s := range tailSegs {
		isModel := modelRe.MatchString(s.text) && regexp.MustCompile(`\d`).MatchString(s.text)
		if cjkRatio(s.text) >= 0.5 || isModel {
			tailKeep = append(tailKeep, s)
		}
	}
	if len(tailKeep) > 4 {
		tailKeep = tailKeep[:4]
	}
	// head 与 tail 之间原本隔着金额，用空格连接（不再硬插逗号造成断词）
	parts := append([]seg{}, headSegs...)
	if len(parts) > 0 && parts[len(parts)-1].sep == "" {
		parts[len(parts)-1].sep = " "
	}
	parts = append(parts, tailKeep...)
	if len(parts) == 0 {
		return ""
	}
	parts[len(parts)-1].sep = ""
	var b strings.Builder
	for _, s := range parts {
		b.WriteString(s.text)
		b.WriteString(s.sep)
	}
	joined := strings.TrimSpace(regexp.MustCompile(`\s{2,}`).ReplaceAllString(b.String(), " "))
	if len([]rune(joined)) > 70 {
		joined = string([]rune(joined)[:70])
	}
	return strings.TrimRight(joined, "，, ")
}

// cjkRatio 返回字符串中中文字符占比（用于区分真实商品名片段与 OCR 乱码）
func cjkRatio(s string) float64 {
	rs := []rune(s)
	if len(rs) == 0 {
		return 0
	}
	c := 0
	for _, r := range rs {
		if r >= '一' && r <= '鿿' {
			c++
		}
	}
	return float64(c) / float64(len(rs))
}

// normalize 把 OCR 文本归一化：统一货币符号、压平换行外的噪声
func normalize(raw string) string {
	s := strings.ReplaceAll(raw, "￥", "¥")
	// x138.00 / xy129.80 / Y34471.8 -> ¥138.00 ...
	s = currencyMisread.ReplaceAllString(s, "¥$2")
	// 全角括号统一，便于剥离账户说明
	s = strings.ReplaceAll(s, "（", "(")
	s = strings.ReplaceAll(s, "）", ")")
	return s
}

// firstAmount 在片段中找第一个金额（跳过 <=0，用于成交价/手续费等只关心正金额）
func firstAmount(seg string) float64 {
	for _, m := range amountRe.FindAllStringSubmatch(seg, -1) {
		v := m[1]
		if v == "" {
			v = m[2]
		}
		v = strings.ReplaceAll(v, ",", ".")
		f, _ := strconv.ParseFloat(v, 64)
		if f > 0 {
			return f
		}
	}
	return 0
}

// firstAmountAny 取片段中第一个金额，即使为 0 也返回（用于「运费」：
// 包邮订单运费字面就是 ¥0.00，不能因为 <=0 跳过而误抓后面的「支付优惠」等正数）
func firstAmountAny(seg string) float64 {
	for _, m := range amountRe.FindAllStringSubmatch(seg, -1) {
		v := m[1]
		if v == "" {
			v = m[2]
		}
		v = strings.ReplaceAll(v, ",", ".")
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}

// windowAfter 取关键词之后、下一个已知关键词之前的片段
func windowAfter(text, keyword string) string {
	i := strings.Index(text, keyword)
	if i < 0 {
		return ""
	}
	seg := text[i+len(keyword):]
	end := len(seg)
	for _, kw := range fieldKeywords {
		if kw == keyword {
			continue
		}
		if j := strings.Index(seg, kw); j >= 0 && j < end {
			end = j
		}
	}
	return seg[:end]
}

// 对方昵称容错正则：允许「家」与「昵」之间混入噪字（OCR 常误读成「卖家了昵称」），
// 且「昵」常被误识别成 呢/尼/伲 等同形字，一并容忍
var cpSellRe = regexp.MustCompile(`卖\s*家[\s\S]{0,6}?[呢尼伲昵]\s*称\s*([^\s，,。|~～]{1,24})`)
var cpBuyRe = regexp.MustCompile(`买\s*家[\s\S]{0,6}?[呢尼伲昵]\s*称\s*([^\s，,。|~～]{1,24})`)

// cpBadRe：明显不是昵称的串——OCR 漏掉真昵称时，会误把「下单时间/成交时间」或日期抓进来
var cpBadRe = regexp.MustCompile(`时间|^\d{4}[-/.年]\d{1,2}[-/.月]\d{1,2}|^\d{1,2}:\d{2}`)

// extractCounterparty 抓取对方昵称（卖家昵称优先，其次买家昵称）；OCR 噪字不影响
func extractCounterparty(flat string) string {
	for _, re := range []*regexp.Regexp{cpSellRe, cpBuyRe} {
		if m := re.FindStringSubmatch(flat); m != nil && m[1] != "" {
			cp := strings.Trim(m[1], "，,。|~～ ")
			// 去掉 OCR 在昵称尾部带来的杂数字（如「一羽酒先4」末尾的 4）
			cp = strings.TrimRight(cp, "0123456789")
			cp = strings.Trim(cp, "，,。|~～ ")
			// 拒收时间标签 / 日期：OCR 漏掉真昵称时「卖家昵称」后会直接跟「下单时间 …」
			if cp == "" || cpBadRe.MatchString(cp) {
				continue
			}
			return cp
		}
	}
	return ""
}

// parseText 把本地 OCR 纯文本解析成与视觉识别相同的字段。
// 兼容两种 OCR 输出：整段无换行（tesseract 常见）与逐行文本。
func parseText(raw string) OCRResult {
	r := OCRResult{Side: "sell"}
	text := normalize(raw)
	flat := strings.ReplaceAll(text, "\n", " ")

	// ---------- 买卖侧判定（加权打分，抗 OCR 漏字/串词）----------
	// 买进单：出现卖家昵称、收货地址、下单/付款/发货时间、支付宝交易号等
	// 卖出单：出现买家昵称、预计到手、软件服务费、已到达您的支付宝等
	buyScore, sellScore := 0, 0
	buySignals := []struct {
		re *regexp.Regexp
		w  int
	}{
		{regexp.MustCompile(`卖\s*家[\s\S]{0,4}?昵\s*称`), 6},
		{regexp.MustCompile(`已到达卖家账户|已到达卖家|卖家账户`), 4},
		// 收货地址在「卖家视角」也会展示（买家收货地址），故权重压低，仅作辅助信号
		{regexp.MustCompile(`收货地址`), 1},
		{regexp.MustCompile(`下单时间|付款时间|发货时间|支付宝交易号|交易快照`), 2},
		{regexp.MustCompile(`商品总价`), 1},
	}
	sellSignals := []struct {
		re *regexp.Regexp
		w  int
	}{
		{regexp.MustCompile(`买\s*家[\s\S]{0,4}?昵\s*称`), 6},
		{regexp.MustCompile(`预计到手|预估到手|实际到账`), 3},
		{regexp.MustCompile(`软件服务费`), 3},
		{regexp.MustCompile(`已到达您的支付宝`), 4},
		{regexp.MustCompile(`联系买家|去评价`), 1},
	}
	for _, s := range buySignals {
		if s.re.MatchString(flat) {
			buyScore += s.w
		}
	}
	for _, s := range sellSignals {
		if s.re.MatchString(flat) {
			sellScore += s.w
		}
	}
	if buyScore > sellScore {
		r.Side = "buy"
	} else {
		r.Side = "sell"
	}
	r.SideBuyScore, r.SideSellScore = buyScore, sellScore
	// 金额疑似被遮挡/识别不全（关键词后紧接 * @ 等杂符），提醒用户核对
	r.AmountGarbled = amountGarbled(flat)

	// ---------- 金额字段（关键词窗口，兼容整段/逐行）----------
	if seg := windowAfter(flat, "成交价"); seg != "" {
		// 剥掉括号里的账户说明，避免 "支付宝187****21" 干扰
		seg = regexp.MustCompile(`\([^)]*\)`).ReplaceAllString(seg, "")
		r.Price = firstAmount(seg)
	}
	if r.Price == 0 {
		if seg := windowAfter(flat, "商品总价"); seg != "" {
			r.Price = firstAmount(seg)
		}
	}
	// 无忧卖类目软件服务费：必须单独提取（它的文案里含「软件服务费」子串，
	// 若先按「软件服务费」抓会把 ¥7.00 误当成普通手续费）。
	for _, kw := range []string{"无忧卖类目软件服务费", "无忧卖服务费", "无忧卖"} {
		if seg := windowAfter(flat, kw); seg != "" {
			if v := firstAmount(seg); v > 0 {
				r.WuyouFee = v
				break
			}
		}
	}
	// 软件服务费：在原文中排除「无忧卖…」那一整行后再抓，避免串到 7.00。
	plain := flat
	if lines := strings.Split(flat, "\n"); len(lines) > 1 {
		var keep []string
		for _, ln := range lines {
			if !strings.Contains(ln, "无忧卖") {
				keep = append(keep, ln)
			}
		}
		plain = strings.Join(keep, "\n")
	}
	// 整段无换行（tesseract 常见）时，用「无忧卖…金额」区间剔除
	plain = regexp.MustCompile(`无忧卖[^，,。\d]{0,12}[-\-]?[¥xX✗]?\s*\d+\.\d{2}`).ReplaceAllString(plain, " ")
	for _, kw := range []string{"软件服务费", "服务费"} {
		if seg := windowAfter(plain, kw); seg != "" {
			if v := firstAmount(seg); v > 0 {
				r.Fee = v
				break
			}
		}
	}
	for _, kw := range []string{"预计到手", "预估到手", "实际到账"} {
		if seg := windowAfter(flat, kw); seg != "" {
			if v := firstAmount(seg); v > 0 {
				r.NetPrice = v
				break
			}
		}
	}
	if r.NetPrice == 0 {
		if seg := windowAfter(flat, "到手"); seg != "" {
			r.NetPrice = firstAmount(seg)
		}
	}
	if r.Side == "buy" {
		r.NetPrice = 0 // 买进侧：成交价即实付，作为买进价
	}
	if r.Side == "sell" && r.NetPrice == 0 && r.Price > 0 {
		// 到手价 = 成交价 − 软件服务费 − 无忧卖类目软件服务费
		r.NetPrice = round2(r.Price - r.Fee - r.WuyouFee)
	}

	// 运费：买单=我(买家)付的运费，卖单=订单显示的运费；包邮/免运费则为 0。
	// 用 firstAmountAny 取「运费」字眼后第一个金额（含 0），避免包邮(¥0.00)被跳过
	// 后误抓后面的「支付优惠/红包」正数；「支付优惠」已加入 fieldKeywords 使窗口在此截断。
	if seg := windowAfter(flat, "运费"); seg != "" {
		r.Shipping = firstAmountAny(seg)
	}

	// 标价：商品标题旁第一个金额（在「成交价」之前）
	if idx := strings.Index(flat, "成交价"); idx > 0 {
		if v := firstAmount(flat[:idx]); v > 0 {
			r.ListedPrice = v
		}
	} else {
		r.ListedPrice = firstAmount(flat)
	}

	// ---------- 订单编号 ----------
	orderRe := regexp.MustCompile(`\d{15,20}`)
	findOrder := func(s string) string { return orderRe.FindString(s) }
	for _, kw := range []string{"订单编号", "订单号"} {
		if seg := windowAfter(flat, kw); seg != "" {
			if o := findOrder(seg); o != "" {
				r.OrderNo = o
				break
			}
		}
	}
	if r.OrderNo == "" {
		// 兜底：全文中找 15~20 位纯数字订单号，排除紧邻「交易号」的支付宝交易号（位数更长）
		for _, m := range orderRe.FindAllString(flat, -1) {
			idx := strings.Index(flat, m)
			lo, hi := idx-6, idx+len(m)+6
			if lo < 0 {
				lo = 0
			}
			if hi > len(flat) {
				hi = len(flat)
			}
			if strings.Contains(flat[lo:hi], "交易号") {
				continue
			}
			r.OrderNo = m
			break
		}
	}

	// ---------- 交易时间 ----------
	tsRe := regexp.MustCompile(`(20\d{2})[-/年](\d{1,2})[-/月](\d{1,2})日?\s*(\d{1,2}):(\d{2})(?::(\d{2}))?`)
	norm := func(m []string) string {
		mm, _ := strconv.Atoi(m[2])
		dd, _ := strconv.Atoi(m[3])
		hh, _ := strconv.Atoi(m[4])
		ss := "00"
		if m[6] != "" {
			ss = m[6]
		}
		return m[1] + "-" + two(mm) + "-" + two(dd) + " " + two(hh) + ":" + m[5] + ":" + ss
	}
	// 买进单优先「下单时间」(买入动作发生时)；卖出单优先「成交时间」(卖出成交时)
	preferOrder := []string{"成交时间", "下单时间", "付款时间", ""}
	if r.Side == "buy" {
		preferOrder = []string{"下单时间", "付款时间", "成交时间", ""}
	}
	for _, prefer := range preferOrder {
		scope := flat
		if prefer != "" {
			scope = windowAfter(flat, prefer)
		}
		if m := tsRe.FindStringSubmatch(scope); m != nil {
			r.DealTime = norm(m)
			break
		}
	}
	// 兜底：手机端长截图 OCR 常把「下单时间」右侧的浅灰数值整片漏掉，只留标签。
	// 支付宝交易号格式固定为 yyyyMMdd + 20 余位（约 28 位），前 8 位即交易/下单日期，
	// 用它至少还原出准确的日期部分（时分秒留 00:00:00，并在前端标注来源）。
	// 只在完全没有识别到时间时启用，且严格限定 26~30 位、以 20 开头的数字串。
	if r.DealTime == "" {
		for _, m := range longNumRe.FindAllString(flat, -1) {
			if len(m) >= 26 && len(m) <= 30 && strings.HasPrefix(m, "20") {
				y, mo, d := m[0:4], m[4:6], m[6:8]
				if y >= "2000" && mo >= "01" && mo <= "12" && d >= "01" && d <= "31" {
					r.DealTime = y + "-" + mo + "-" + d + " 00:00:00"
					break
				}
			}
		}
	}

	// ---------- 对方昵称 ----------
	// OCR 常把「卖家昵称/买家昵称」误读成「卖家了昵称」等（家 与 昵 之间混入 1~2 个噪字），
	// 故用容错正则直接抓取标签后的昵称串，而不是依赖精确关键词 windowAfter。
	r.Counterparty = extractCounterparty(flat)

	// ---------- 商品名 ----------
	r.Product = extractProduct(flat)
	finalizeSellFees(&r)
	return r
}

// finalizeSellFees：卖出单金额一致性校正，解决两类识别误差：
//  1. 手续费（软件服务费 + 无忧卖）都漏识别，但成交价 > 到手价 → 用差额反推总服务费；
//  2. 手续费已识别，但到手价未识别 / 被误填成成交价（到手价应为 成交价 − 手续费）
//     → 重算 到手价 = 成交价 − 手续费。
//
// 成交价、到手价是识别最稳的两个金额，据此兜底可避免「服务费统计为 0」或净利润虚高。
func finalizeSellFees(r *OCRResult) {
	if r.Side != "sell" {
		return
	}
	// 1) 手续费漏识别但成交价 > 到手价：反推总服务费。
	//    到手价按当前页面口径已扣卖运费，不能把运费算成服务费。
	if r.Fee == 0 && r.WuyouFee == 0 && r.NetPrice > 0 && r.Price > r.NetPrice {
		r.Fee = inferFeeFromNet(r.Price, r.NetPrice, r.Shipping)
	}
	// 2) 手续费已识别，但到手价缺失或 ≥ 成交价（明显错）：重算到手价
	if r.Fee+r.WuyouFee > 0 && (r.NetPrice == 0 || r.NetPrice >= r.Price) {
		r.NetPrice = round2(r.Price - r.Fee - r.WuyouFee)
	}
}

func two(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func handleParseText(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, 400, "请求体解析失败")
		return
	}
	if strings.TrimSpace(in.Text) == "" {
		writeError(w, 400, "OCR 文本为空")
		return
	}
	writeJSON(w, parseText(in.Text))
}
