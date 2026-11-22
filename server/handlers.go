package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var productNormRe = regexp.MustCompile(`[\s，,。、:：\-_/()（）]+`)

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// inferFeeFromNet 在服务费字段缺失时，用成交价与到手价的差额反推服务费。
// 到手价口径已扣卖运费，因此必须先减掉卖运费，不能把运费误记成服务费。
func inferFeeFromNet(price, net, shipping float64) float64 {
	if price <= 0 || net <= 0 || price <= net {
		return 0
	}
	fee := round2(price - net - math.Max(0, shipping))
	if fee <= 0.005 {
		return 0
	}
	return fee
}

// sellFeeTotal 返回卖单实际计入成本的软件服务费与无忧卖费用。
// 明确字段优先；字段都缺失时才按「成交价 − 到手价 − 卖运费」兜底。
func sellFeeTotal(it Item) float64 {
	fee := round2(it.SellFee + it.SellWuyouFee)
	if fee > 0.005 {
		return fee
	}
	return inferFeeFromNet(it.SellPrice, it.SellNet, it.SellShipping)
}

// 在库成本台账：先把所有有效采购作为库存占用，再让已售单据按实际带走成本冲减。
// 手工填写的单件成本与联单派生分摊都走同一套冲减，避免一边整批排除、一边重复扣。
type holdingLot struct {
	ID        int64
	Product   string
	Norm      string
	Gross     float64
	Remaining float64
	BuyTime   string
	AgeBand   int
}

type holdingDeduction struct {
	ParentID int64
	Norm     string
	Amount   float64
	SellTime string
}

func holdingAgeBand(buyTime string) int {
	if len(buyTime) < 10 {
		return 0
	}
	t, err := time.Parse("2006-01-02", buyTime[:10])
	if err != nil {
		return 0
	}
	switch days := int(time.Since(t).Hours() / 24); {
	case days <= 30:
		return 0
	case days <= 60:
		return 1
	case days <= 90:
		return 2
	default:
		return 3
	}
}

// applyHoldingDeductions 按卖单时间依次冲减采购批次：
// 先冲同一关联买进单，再优先冲同商品、卖出前已入库的批次，最后才做全库存兜底。
// 这样仍有商品级明细可看，也保证确已填写成本、但商品名不一致的历史数据不会继续虚增在库。
func applyHoldingDeductions(lots []holdingLot, deductions []holdingDeduction) []holdingLot {
	for i := range lots {
		lots[i].Remaining = round2(lots[i].Gross)
		if lots[i].Norm == "" {
			lots[i].Norm = normProduct(lots[i].Product)
		}
	}
	sort.SliceStable(lots, func(i, j int) bool {
		if lots[i].BuyTime == lots[j].BuyTime {
			return lots[i].ID < lots[j].ID
		}
		return lots[i].BuyTime < lots[j].BuyTime
	})
	sort.SliceStable(deductions, func(i, j int) bool {
		return deductions[i].SellTime < deductions[j].SellTime
	})

	consume := func(amount float64, match func(int) bool) float64 {
		for i := range lots {
			if amount <= 0.005 {
				break
			}
			if lots[i].Remaining <= 0.005 || !match(i) {
				continue
			}
			take := math.Min(amount, lots[i].Remaining)
			lots[i].Remaining = round2(lots[i].Remaining - take)
			if lots[i].Remaining < 0.005 {
				lots[i].Remaining = 0
			}
			amount = round2(amount - take)
		}
		return amount
	}

	for _, d := range deductions {
		left := round2(d.Amount)
		if left <= 0.005 {
			continue
		}
		if d.ParentID > 0 {
			left = consume(left, func(i int) bool { return lots[i].ID == d.ParentID })
		}
		if left > 0.005 && d.Norm != "" {
			left = consume(left, func(i int) bool {
				return lots[i].Norm == d.Norm && (d.SellTime == "" || lots[i].BuyTime == "" || lots[i].BuyTime <= d.SellTime)
			})
			left = consume(left, func(i int) bool { return lots[i].Norm == d.Norm })
		}
		left = consume(left, func(i int) bool {
			return d.SellTime == "" || lots[i].BuyTime == "" || lots[i].BuyTime <= d.SellTime
		})
		consume(left, func(int) bool { return true })
	}
	return lots
}

// Item = 一条记录。
// kind=purchase：买进单（整批货，parent_id=0，可挂 N 个卖出单）
// kind=sale：卖出单（parent_id>0，挂到某个买进单）
// kind=standalone：独立完整记录（旧数据 / 手动两侧一起填）
type Item struct {
	ID       int64   `json:"id"`
	Product  string  `json:"product"`
	ParentID int64   `json:"parent_id"`
	Quantity int     `json:"quantity"`
	UnitCost float64 `json:"unit_cost"`

	BuyPrice        float64 `json:"buy_price"`
	BuyShipping     float64 `json:"buy_shipping"`
	BuyShipTag      string  `json:"buy_ship_tag"`
	BuyOrderNo      string  `json:"buy_order_no"`
	BuyTime         string  `json:"buy_time"`
	BuyCounterparty string  `json:"buy_counterparty"`

	SellPrice        float64 `json:"sell_price"`
	SellFee          float64 `json:"sell_fee"`       // 软件服务费
	SellWuyouFee     float64 `json:"sell_wuyou_fee"` // 无忧卖类目软件服务费
	SellNet          float64 `json:"sell_net"`
	SellShipping     float64 `json:"sell_shipping"`
	SellShipTag      string  `json:"sell_ship_tag"`
	SellOrderNo      string  `json:"sell_order_no"`
	SellTime         string  `json:"sell_time"`
	SellCounterparty string  `json:"sell_counterparty"`

	Note       string `json:"note"`
	Attachment string `json:"attachment"` // 附件文件名（空=无）；随列表返回，编辑弹窗才能显示已有截图
	Source     string `json:"source"`     // 货物来源：自采 / 回收 / 寄卖 / 闲鱼（空=未标注）
	Owner      string `json:"owner"`      // 归属人（寄卖时=寄卖人，如「张三」；其它来源可选填）

	// 发货时间（闲鱼 consignTime，精确到秒）。空=还没发货 / 非闲鱼单据。
	ConsignTime string `json:"consign_time"`
	// 单商品状态：这两个都是「非空即生效」的时间戳
	SettledAt   string `json:"settled_at"`   // 单品完结（不走联单）
	DiscardedAt string `json:"discarded_at"` // 弃单
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`

	// 派生字段
	Kind       string  `json:"kind"`   // purchase | sale | standalone
	Status     string  `json:"status"` // sold=已成交 pending=待售 external=外部账本结算
	HasBuy     bool    `json:"has_buy"`
	HasSell    bool    `json:"has_sell"`
	DealTime   string  `json:"deal_time"`   // 卖出时间优先，买进时间兜底
	Cost       float64 `json:"cost"`        // 本条计入总成本的金额
	Profit     float64 `json:"profit"`      // 本条参考利润
	ChildCount int     `json:"child_count"` // 买进单已挂的卖出单数
	ParentName string  `json:"parent_name"` // 卖出单所属买进单商品名
	// 联单分摊的单件成本：卖单自己没填成本、但所在联单有买进单时，
	// 按成交价占比算出的单件成本。只用于展示与核算，不写回数据库。
	AllocCost float64 `json:"alloc_cost,omitempty"`
	// 用户主动标记「这单没有货品成本」（代卖 / 寄卖 / 赠送）。
	// 标记后不再进「未挂成本」，利润按销售额全额计，成本只剩运费与服务费。
	CostFree bool `json:"cost_free"`
	// 归一化商品名：明细页「同商品」用它精确筛出同名不同写法的全部单据
	ProductNorm string `json:"product_norm"`
}

func (it *Item) derive() {
	it.HasBuy = it.BuyPrice > 0 || it.BuyOrderNo != "" || it.BuyCounterparty != ""
	it.HasSell = it.SellNet > 0 || it.SellPrice > 0 || it.SellOrderNo != "" || it.SellCounterparty != ""
	if it.Quantity < 1 {
		it.Quantity = 1
	}
	it.DealTime = it.SellTime
	if it.DealTime == "" {
		it.DealTime = it.BuyTime
	}
	// 完全成本口径：服务费（软件服务费 + 无忧卖服务费）计入成本，
	// 销售额取成交价（未扣费）；两者相减时服务费只扣一次，与汇总保持一致。
	// 历史数据可能只填了到手价没填成交价，此时用到手价兜底。
	feeAll := sellFeeTotal(*it)
	revenue := it.SellPrice
	if revenue == 0 {
		revenue = it.SellNet + feeAll
	}

	// 弃单只是「不进账」，不是「金额归零」：
	// 明细里买价 / 成本 / 到手价照常显示（方便事后回看），
	// 统计侧已经在 handleStats 里对 Kind=discarded 直接 continue，不会算进任何合计。
	discarded := it.DiscardedAt != ""

	switch {
	case it.ParentID > 0:
		// 卖出单：成本用分摊的单件成本，整批买价已在买进单里计过，不重复计
		it.Kind = "sale"
		it.HasBuy = false
		it.HasSell = true
		it.Status = "sold"
		it.Cost = round2(it.UnitCost + it.SellShipping + feeAll)
		it.Profit = round2(revenue - it.UnitCost - it.SellShipping - feeAll)
	case it.HasSell && !it.HasBuy:
		// 独立卖出单（未关联买进单）：纯卖，归类 sale，避免被当成“两侧都有”的 standalone，
		// 否则联单会把它同时计成 1 买 1 卖（几买几卖虚高），编辑弹窗也会误显买进信息。
		it.Kind = "sale"
		it.Status = "sold"
		// 独立卖单可以直接填「单件成本」，一并算进去
		sq := it.Quantity
		if sq < 1 {
			sq = 1
		}
		goods := it.BuyPrice + it.BuyShipping
		if goods == 0 && it.UnitCost > 0 {
			goods = round2(it.UnitCost * float64(sq))
		}
		it.Cost = round2(goods + it.SellShipping + feeAll)
		it.Profit = round2(revenue - goods - it.SellShipping - feeAll)
	case it.HasBuy && !it.HasSell:
		// 买进单：整批总价 + 买运费计入总成本
		it.Kind = "purchase"
		if isExternalGoods(it.Source) {
			it.Status = "external"
		} else {
			it.Status = "pending"
		}
		it.Cost = round2(it.BuyPrice + it.BuyShipping)
		it.Profit = 0
	default:
		// 旧数据 / 两侧一起填的独立完整记录
		it.Kind = "standalone"
		it.Status = "sold"
		it.Cost = round2(it.BuyPrice + it.BuyShipping + it.SellShipping + feeAll)
		it.Profit = round2(revenue - it.BuyPrice - it.BuyShipping - it.SellShipping - feeAll)
	}

	// 寄卖 / 代采是外部账本：金额仍保留在原始单据里供寄卖页结算，
	// 但不进入自己的成本、利润和待售口径。状态单独标成 external，
	// 避免在订单页出现「待售」；这里只做派生，不写库、不动 settled_at。
	if isExternalGoods(it.Source) {
		it.Cost = 0
		it.Profit = 0
		it.Status = "external"
	}

	// 弃单：类型与状态都标成 discarded，但 Cost / Profit 保持上面算出来的值。
	if discarded {
		it.Kind = "discarded"
		it.Status = "discarded"
		return
	}

	// 单品完结：单据类型不变（买卖单仍是买卖单），只是状态改为完结，
	// 由上层据此把它从「在库 / 待售 / 联单候选」里摘出去。
	if it.SettledAt != "" && !isExternalGoods(it.Source) {
		it.Status = "settled"
	}
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func registerAPI(mux *http.ServeMux) {
	// 鉴权相关接口本身不设防（否则无法登录）
	mux.HandleFunc("/api/auth/status", handleAuthStatus)
	mux.HandleFunc("/api/auth/login", handleAuthLogin)
	mux.HandleFunc("/api/auth/shop-login", handleShopLogin)
	mux.HandleFunc("/api/auth/logout", handleAuthLogout)
	mux.HandleFunc("/api/auth/password", handleAuthPassword)

	// 所有业务接口统一鉴权
	mux.HandleFunc("/api/recognize", authWrap(handleRecognize))
	mux.HandleFunc("/api/parse-text", authWrap(handleParseText))
	mux.HandleFunc("/api/records", authWrap(handleRecords))
	mux.HandleFunc("/api/records/", authWrap(handleRecordByID))
	mux.HandleFunc("/api/records/batch-delete", authWrap(handleBatchDelete))
	mux.HandleFunc("/api/records/link-purchases", authWrap(handleLinkPurchases))
	mux.HandleFunc("/api/records/cost-free", authWrap(handleCostFree))
	mux.HandleFunc("/api/records/auto-link/preview", authWrap(handleAutoLinkPreview))
	mux.HandleFunc("/api/records/auto-link/apply", authWrap(handleAutoLinkApply))
	mux.HandleFunc("/api/records/estimate-cost", authWrap(handleEstimateCost))
	mux.HandleFunc("/api/records/price-hint", authWrap(handlePriceHint))
	mux.HandleFunc("/api/monthly-target", authWrap(handleMonthlyTarget))
	mux.HandleFunc("/api/records/todo-count", authWrap(handleTodoCount))
	// 必须在 /api/records/ 前缀之前更具体，ServeMux 按最长前缀匹配，这里显式注册
	mux.HandleFunc("/api/records/exists", authWrap(handleRecordsExists))
	mux.HandleFunc("/api/undo", authWrap(handleUndo))
	mux.HandleFunc("/api/match", authWrap(handleMatch))
	mux.HandleFunc("/api/purchases", authWrap(handlePurchases))
	mux.HandleFunc("/api/shops", authWrap(handleShops))
	mux.HandleFunc("/api/shops/", authWrap(handleShopByID))
	mux.HandleFunc("/api/stats", authWrap(handleStats))
	mux.HandleFunc("/api/trend", authWrap(handleTrend))
	mux.HandleFunc("/api/product-profit", authWrap(handleProductProfit))
	mux.HandleFunc("/api/product-profit/export", authWrap(handleProductProfitExport))
	mux.HandleFunc("/api/groups", authWrap(handleGroups))
	mux.HandleFunc("/api/groups/candidates", authWrap(handleGroupCandidates))
	mux.HandleFunc("/api/groups/by-item/", authWrap(handleGroupsByItem))
	mux.HandleFunc("/api/groups/link-sale", authWrap(handleLinkSale))
	mux.HandleFunc("/api/groups/auto-merge", authWrap(handleAutoMerge))
	mux.HandleFunc("/api/groups/merge-suggestions", authWrap(handleGroupMergeSuggestions))
	mux.HandleFunc("/api/groups/merge-apply", authWrap(handleGroupMergeApply))
	mux.HandleFunc("/api/groups/", authWrap(handleGroupByID))
	mux.HandleFunc("/api/settings", authWrap(handleSettings))
	mux.HandleFunc("/api/settings/test", authWrap(handleSettingsTest))
	mux.HandleFunc("/api/tags", authWrap(handleTags))
	mux.HandleFunc("/api/tags/", authWrap(handleTagByID))
	mux.HandleFunc("/api/backups", authWrap(handleBackups))
	mux.HandleFunc("/api/backups/", authWrap(handleBackupFile))
	mux.HandleFunc("/api/export", authWrap(handleExport))
	mux.HandleFunc("/api/import", authWrap(handleImport))

	// 后端增强：新增端点，handler 集中在 extras.go
	mux.HandleFunc("/api/undo/ops", authWrap(handleUndoOps))
	mux.HandleFunc("/api/records/batch-update", authWrap(handleBatchUpdate))
	mux.HandleFunc("/api/records/{id}/attachment", authWrap(handleAttachment))
	mux.HandleFunc("/api/alipay/preview", authWrap(handleAlipayPreview))
	mux.HandleFunc("/api/alipay/apply", authWrap(handleAlipayApply))
	mux.HandleFunc("/api/alipay/undo", authWrap(handleAlipayUndo))
	mux.HandleFunc("/api/records/{id}/settle", authWrap(handleAttachmentSettle))
	mux.HandleFunc("/api/records/{id}/discard", authWrap(handleAttachmentDiscard))
	mux.HandleFunc("/api/health", handleHealth) // 健康检查可不鉴权
	mux.HandleFunc("/api/version", authWrap(handleVersion))
	// 在线更新：查 GitHub Release → 下载 → 校验 → 容器内替换并重启
	mux.HandleFunc("/api/update/check", authWrap(handleUpdateCheck))
	mux.HandleFunc("/api/update/apply", authWrap(handleUpdateApply))
	mux.HandleFunc("/api/update/progress", authWrap(handleUpdateProgress))
	mux.HandleFunc("/api/update/config", authWrap(handleUpdateConfig))
	mux.HandleFunc("/api/stats/extras", authWrap(handleStatsExtras))
	mux.HandleFunc("/api/consign/stats", authWrap(handleConsignStats))
	mux.HandleFunc("/api/consign/owners", authWrap(handleConsignOwners))
	mux.HandleFunc("/api/consign/assign-owner", authWrap(handleConsignAssignOwner))
	mux.HandleFunc("/api/consign/settle", authWrap(handleConsignSettle))
	mux.HandleFunc("/api/consign/settlements", authWrap(handleConsignSettlements))
	mux.HandleFunc("/api/consign/settlements/", authWrap(handleConsignSettlementByID))
	mux.HandleFunc("/api/ocr-feedback", authWrap(handleOCRFeedback))
	mux.HandleFunc("/api/backup/verify", authWrap(handleBackupVerify))

	// 多人实时同步：SSE 变更流 + 在线状态
	mux.HandleFunc("/api/events", authWrap(handleEvents))
	mux.HandleFunc("/api/presence", authWrap(handlePresence))

	// 闲鱼同步：服务端直连 mtop，凭证由用户在设置里粘一次
	mux.HandleFunc("/api/goofish/status", goofishBridgeAuthWrap(handleGoofishStatus))
	mux.HandleFunc("/api/goofish/login", authWrap(handleGoofishLogin))
	mux.HandleFunc("/api/goofish/rebind", goofishBridgeAuthWrap(handleGoofishRebind))
	mux.HandleFunc("/api/goofish/bridge/heartbeat", goofishBridgeAuthWrap(handleGoofishBridgeHeartbeat))
	mux.HandleFunc("/api/goofish/bridge/token", authWrap(handleGoofishBridgeToken))
	mux.HandleFunc("/api/goofish/qr/start", authWrap(handleGoofishQRStart))
	mux.HandleFunc("/api/goofish/umid", authWrap(handleGoofishUmid))
	mux.HandleFunc("/api/goofish/qr/poll", authWrap(handleGoofishQRPoll))
	mux.HandleFunc("/api/goofish/logout", authWrap(handleGoofishLogout))
	mux.HandleFunc("/api/goofish/preview", authWrap(handleGoofishPreview))
	mux.HandleFunc("/api/goofish/apply", authWrap(handleGoofishApply))
	mux.HandleFunc("/api/goofish/keepalive", authWrap(handleGoofishKeepAlive))
	mux.HandleFunc("/api/goofish/ping", authWrap(handleGoofishPing))
	mux.HandleFunc("/api/goofish/auto", authWrap(handleGoofishAuto))

	// 支付宝账单导入：按月账单接口拿不到的手续费，从支付宝明细里按订单号补
}

// ---------- 识别 ----------

func handleRecognize(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	// 12MB 硬上限：ParseMultipartForm(maxMemory) 只控制内存阈值，
	// 超出部分会落临时文件且不封顶，必须限制请求体总量，防止超大文件打满磁盘。
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeError(w, 413, "文件过大（上限 12MB）或表单解析失败")
		return
	}
	file, header, err := r.FormFile("image")
	if err != nil {
		writeError(w, 400, "缺少 image 字段")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeError(w, 500, "读取图片失败")
		return
	}
	ct := header.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/jpeg"
	}
	res, err := recognizeImage(data, ct)
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	writeJSON(w, res)
}

// ---------- 列表 / 新增（含自动配对）----------

// handleMatch：卖出单录入时，按商品名找可关联的买进单。
// exact=true 表示有精确同名买进单（前端应自动关联）；否则返回相似候选供用户选择。
func handleMatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	var in struct {
		Product string `json:"product"`
		Side    string `json:"side"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, 400, "请求体解析失败")
		return
	}
	in.Product = strings.TrimSpace(in.Product)
	if in.Product == "" {
		writeError(w, 400, "商品名称不能为空")
		return
	}
	// 走内存索引：精确匹配 O(1)，相似匹配只算剪枝后的候选，子单数也从索引取（无 N+1 查询）
	exactList, similar := matchPurchasesIndexed(in.Product, shopID(r), 10)
	exact := len(exactList) > 0
	var match purchaseEntry
	if exact {
		match = exactList[0]
		similar = append(exactList[1:], similar...)
	}
	if len(similar) > 10 {
		similar = similar[:10]
	}
	writeJSON(w, map[string]interface{}{
		"exact":      exact,
		"match":      match,
		"candidates": similar,
	})
}

// handlePurchases：一次性返回全部可关联买进单（来自内存索引，前端预加载后选择器秒开）
func handlePurchases(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "method not allowed")
		return
	}
	writeJSON(w, map[string]interface{}{"items": listPurchases(shopID(r))})
}

// handleBatchDelete：批量删除（事务），返回实际删除条数
func handleBatchDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	defer refreshPurchaseIndex()
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, 400, "请求体解析失败")
		return
	}
	clean := in.IDs[:0]
	seen := map[int64]bool{}
	for _, id := range in.IDs {
		if id > 0 && !seen[id] {
			seen[id] = true
			clean = append(clean, id)
		}
	}
	if len(clean) == 0 {
		writeError(w, 400, "未选择要删除的记录")
		return
	}
	if len(clean) > 200 {
		writeError(w, 400, "单次最多删除 200 条")
		return
	}
	// 已完结联单里的记录不能删：先整批拦下，避免删一半留一半
	for _, id := range clean {
		if gid, gtitle := settledGroupOf(id, shopID(r)); gid > 0 {
			writeSettledBlock(w, gtitle, "删除")
			return
		}
	}
	// 删除前取快照（独立查询，须在开启事务前完成，避免连接池里挂着 rows）
	token := snapshotItemsUndo("delete_batch", clean, shopID(r))
	tx, err := db.Begin()
	if err != nil {
		writeError(w, 500, "开启事务失败")
		return
	}
	// 删除前把附件文件一并处理，避免磁盘上留下永远访问不到的孤儿文件
	removeAttachmentFiles(clean, shopID(r))
	n := 0
	for _, id := range clean {
		// 带 shop_id：即使前端传来别家店铺的 id，也只会影响 0 行，不会误删。
		res, e := tx.Exec("DELETE FROM items WHERE id=? AND shop_id=?", id, shopID(r))
		if e != nil {
			tx.Rollback()
			writeError(w, 500, "删除失败: "+e.Error())
			return
		}
		if c, _ := res.RowsAffected(); c > 0 {
			n++
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "提交失败")
		return
	}
	out := map[string]interface{}{"deleted": n}
	if token != "" {
		out["undo_token"] = token
	}
	writeJSON(w, out)
}

// handleTrend：按月聚合销售额/成本/利润/笔数，供概览趋势图使用。
// 支持与明细相同的 q/status/from/to 过滤（默认全量）。
func handleTrend(w http.ResponseWriter, r *http.Request) {
	where, args := buildWhere(r) // shop-scope: buildWhere 内部已带 shop_id 条件
	sid := shopID(r)
	if r.URL.Query().Get("all") == "1" {
		sid = -1
	}
	// 派生分摊数据必须在主查询之前取完。不能拿着 items 的 Rows 再去申请第二条连接，
	// 否则多用户同时刷新趋势时会把连接池里的连接互相锁死。
	alloc := loadGroupAlloc(sid)
	rows, err := db.Query("SELECT "+itemCols+" FROM items WHERE "+where, args...)
	if err != nil {
		writeError(w, 500, "趋势统计失败")
		return
	}
	defer rows.Close()
	// 与总账口径一致：成本在「卖出」时结转，不在「进货」时先记一笔。
	// 否则进货月利润被压成负数、不进货的月份利润率虚高，和「已售成本」的说法也对不上。
	byMonth := map[string]*trendBucket{}
	var noTime trendBucket
	for rows.Next() {
		it, e := scanItem(rows)
		if e != nil {
			continue
		}
		// 弃单等于没录过：趋势里连条数都不该出现
		if it.Kind == "discarded" {
			continue
		}
		// 寄卖 / 代采不计入经营趋势（货是别人的）
		if isExternalGoods(it.Source) {
			continue
		}
		key := it.DealTime
		if len(key) >= 7 {
			key = key[:7]
		} else {
			key = ""
		}
		var b *trendBucket
		if key == "" {
			b = &noTime
		} else {
			b = byMonth[key]
			if b == nil {
				b = &trendBucket{Month: key}
				byMonth[key] = b
			}
		}
		sales, cost := 0.0, 0.0
		feeAll := sellFeeTotal(it)
		switch it.Kind {
		case "purchase":
			// 进货只是把货变成库存，不产生已售成本；卖出去时才结转。
		case "sale":
			qty := it.Quantity
			if qty < 1 {
				qty = 1
			}
			sales = it.SellPrice
			cost = round2(effectiveUnitCost(it, alloc)*float64(qty)) + it.SellShipping + feeAll
		case "standalone":
			sales = it.SellPrice
			cost = it.BuyPrice + it.BuyShipping + it.SellShipping + feeAll
		}
		b.Sales += sales
		b.Cost += cost
		// 「本月成交」是卖单口径，进货不能算一单成交
		if it.HasSell {
			b.Count++
		}
	}
	keys := make([]string, 0, len(byMonth))
	for k := range byMonth {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]trendBucket, 0, len(keys)+1)
	for _, k := range keys {
		b := byMonth[k]
		out = append(out, trendBucket{k, round2(b.Sales), round2(b.Cost), round2(b.Sales - b.Cost), b.Count})
	}
	if noTime.Count > 0 {
		out = append(out, trendBucket{"", round2(noTime.Sales), round2(noTime.Cost), round2(noTime.Sales - noTime.Cost), noTime.Count})
	}
	// 周期：week/quarter 按月数据再聚合，数据不足退回原样，不报错。
	out = applyTrendPeriod(out, r.URL.Query().Get("period"))
	writeJSON(w, map[string]interface{}{"months": out})
}

func handleRecords(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		listRecords(w, r)
	case "POST":
		// 录入即重建买进单索引；defer 保证在本次写入完成后执行
		defer refreshPurchaseIndex()
		createRecord(w, r)
	default:
		writeError(w, 405, "method not allowed")
	}
}

// ItemInput 是编辑/新增时两侧字段的全量提交
type ItemInput struct {
	Product string `json:"product"`

	BuyPrice        float64 `json:"buy_price"`
	BuyShipping     float64 `json:"buy_shipping"`
	BuyShipTag      string  `json:"buy_ship_tag"`
	BuyOrderNo      string  `json:"buy_order_no"`
	BuyTime         string  `json:"buy_time"`
	BuyCounterparty string  `json:"buy_counterparty"`

	SellPrice        float64 `json:"sell_price"`
	SellFee          float64 `json:"sell_fee"`       // 软件服务费
	SellWuyouFee     float64 `json:"sell_wuyou_fee"` // 无忧卖类目软件服务费
	SellNet          float64 `json:"sell_net"`
	SellShipping     float64 `json:"sell_shipping"`
	SellShipTag      string  `json:"sell_ship_tag"`
	SellOrderNo      string  `json:"sell_order_no"`
	SellTime         string  `json:"sell_time"`
	SellCounterparty string  `json:"sell_counterparty"`

	Note  string `json:"note"`
	Owner string `json:"owner"` // 归属人：寄卖单的寄卖人；其它卖单可选填

	// 货物来源：自采 / 回收 / 寄卖 / 闲鱼。闲鱼同步导入的记录固定写「闲鱼」。
	Source string `json:"source"`

	// Side 非空时启用「按商品名自动配对」：buy=录入买进侧 sell=录入卖出侧
	Side    string `json:"side"`
	NoMerge bool   `json:"no_merge"` // true=跳过自动配对，强制新建独立记录

	// 1:N 关联
	ParentID int64   `json:"parent_id"` // 卖出单关联的买进单 id（0=独立）
	Quantity int     `json:"quantity"`  // 买进单件数 / 卖出单本次卖出件数
	UnitCost float64 `json:"unit_cost"` // 卖出单单件成本（关联时自动带出）

	// 这单没有货品成本（代卖 / 寄卖 / 赠送）。标记后不再提示挂成本，
	// 利润按销售额全额计；一旦关联了买进单会自动清掉这个标记。
	CostFree bool `json:"cost_free"`

	// 乐观锁：编辑时带上打开弹窗看到的 updated_at。为空则跳过检查（向后兼容）。
	ExpectedUpdatedAt string `json:"expected_updated_at"`
}

// isExternalGoods：货不是自己的（寄卖代卖 / 代采代买），不计入自己的经营口径。
func isExternalGoods(source string) bool {
	source = normSource(source)
	return source == "寄卖" || source == "代采"
}

// externalSourceSQL 与 isExternalGoods 保持同一口径，同时兼容历史别名。
const externalSourceSQL = "COALESCE(source,'') NOT IN ('寄卖','寄售','代采','代买')"

// autoSettled：卖单只要成本说得清，就算「已结算」——不用再包联单，也不用手动点完结。
// 成本说得清 = 手填了单件成本 / 勾了「无成本」/ 挂了买进单 / 单据本身两侧都填。
// 这是派生状态，不写库：成本改回去，它就自动退回未结算。
// 寄卖、代采这类货是别人的，走寄卖页的结算，不掺和这里。
func autoSettled(it Item) bool {
	if it.SettledAt != "" || (it.Kind != "sale" && it.Kind != "standalone") {
		return false
	}
	if isExternalGoods(it.Source) || !it.HasSell {
		return false
	}
	// 只认「成本确实有数」的：手填单件成本 / 勾了无成本 / 单据本身两侧都填了。
	// 光挂了买进单但没写单件成本的不算——那属于成本还没落到这单上，别虚增完结利润。
	return it.UnitCost > 0 || it.CostFree || it.BuyPrice > 0
}

// autoSettledSQL 是 autoSettled 的 SQL 版（明细「已结算」分类要用）。
// 两处口径必须一致：改一个就同步改另一个。
const autoSettledSQL = "(COALESCE(settled_at,'')='' AND COALESCE(discarded_at,'')=''" +
	" AND (sell_net>0 OR sell_price>0 OR sell_order_no<>'' OR sell_counterparty<>'')" +
	" AND (COALESCE(unit_cost,0)>0 OR COALESCE(cost_free,0)=1 OR buy_price>0)" +
	" AND COALESCE(source,'') NOT IN ('寄卖','代采'))"

func buildWhere(r *http.Request) (string, []interface{}) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	from := strings.TrimSpace(r.URL.Query().Get("from"))
	to := strings.TrimSpace(r.URL.Query().Get("to"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))

	var conds []string
	var args []interface{}

	// 店铺隔离的第一道条件：明细列表的 COUNT 与分页取数共用这个 where，
	// 放在数组首位而不是末尾拼接，后续任何新条件都不会把它挤掉。
	// all=1 时放开店铺条件（概览「全部店铺合计」视图用，仅统计接口传入）。
	if r.URL.Query().Get("all") == "1" {
		conds = append(conds, "1=1")
	} else {
		conds = append(conds, "shop_id = ?")
		args = append(args, shopID(r))
	}

	// 按归一化商品名精确过滤：从「商品利润排行」点进来时用。
	// 排行里同名的不同写法（大小写/空格/标点差异）已经聚合成一行，
	// 用 q 做 LIKE 会让这一行只筛出其中一部分单据，所以这里直接命中 product_norm 索引列。
	if pn := strings.TrimSpace(r.URL.Query().Get("pn")); pn != "" {
		conds = append(conds, "product_norm = ?")
		args = append(args, pn)
		// 有 pn 时 q 只用于搜索框回显，不参与过滤（否则会把同组的其他写法排除掉）
	} else if q != "" {
		// 任意信息检索：覆盖两侧全部文本字段。q 按空格拆成多个关键词，
		// 每个词都要命中（词内 OR 全部字段，词间 AND）。
		const searchGroup = `(product LIKE ? OR buy_order_no LIKE ? OR sell_order_no LIKE ? OR
            buy_counterparty LIKE ? OR sell_counterparty LIKE ? OR
            buy_ship_tag LIKE ? OR sell_ship_tag LIKE ? OR
            buy_time LIKE ? OR sell_time LIKE ? OR note LIKE ?)`
		for _, word := range strings.Fields(q) {
			like := "%" + word + "%"
			conds = append(conds, searchGroup)
			for i := 0; i < 10; i++ {
				args = append(args, like)
			}
		}
	}
	if from != "" {
		// 日期筛选直接用完整交易时间表达式做范围比较：
		// ISO 样式时间按字典序天然按天排序，能命中 idx_items_shop_deal；
		// 先 substr 再比较会丢失索引，数据量上来后每次翻页都要全量重排。
		args = append(args, from)
		conds = append(conds, dealTimeExpr+" <> '' AND "+dealTimeExpr+" >= ?")
	}
	if to != "" {
		// “至”是按天闭区间。追加一个字典序很大的终结符，既能包含当天
		// 2026-11-22 / 2026-11-22T23:59:59，也不会混入次日记录；
		// 空时间显式排除，保持旧口径一致。
		conds = append(conds, dealTimeExpr+" <> '' AND "+dealTimeExpr+" <= ?")
		args = append(args, to+"\uFFFD")
	}
	// 定位单条记录：从首页「最近记录」点进来时，按 id 精确取这一条，
	// 不管它排在第几页、当前筛选是什么，保证一定跳得到。
	if fid := strings.TrimSpace(r.URL.Query().Get("focus")); fid != "" {
		if n, e := strconv.ParseInt(fid, 10, 64); e == nil && n > 0 {
			conds = append(conds, "id = ?")
			args = append(args, n)
		}
	}
	hasBuy := "(buy_price>0 OR buy_order_no<>'' OR buy_counterparty<>'')"
	hasSell := "(sell_net>0 OR sell_price>0 OR sell_order_no<>'' OR sell_counterparty<>'')"
	switch status {
	case "sold":
		conds = append(conds, hasBuy+" AND "+hasSell)
	case "pending":
		conds = append(conds, hasBuy+" AND NOT "+hasSell, externalSourceSQL)
	case "sell_only":
		conds = append(conds, "NOT "+hasBuy+" AND "+hasSell)
	}
	// 金额筛选：在几种「钱」里任选其一命中即可
	//   buy_price 买进价 / sell_price 卖出价 / sell_net 到手价 / cost 本条成本
	// 精确（amount）用 ±0.005 的区间容纳浮点误差；
	// 范围（min_amount / max_amount）按「同一个字段落在区间内」匹配，
	// 否则「买价 50、卖价 5000」会被 100~200 误判命中。
	// cost 不是数据库列（在 Go 里按 derive() 算出来的），这里用等价表达式代替，
	// 否则 SQL 会直接报 no such column 并让整页 500。
	costExpr := "(buy_price + buy_shipping + sell_shipping + sell_fee + sell_wuyou_fee)"
	amtCols := []string{"buy_price", "sell_price", "sell_net", costExpr}
	if s := strings.TrimSpace(r.URL.Query().Get("amount")); s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			ors := make([]string, 0, len(amtCols))
			for _, c := range amtCols {
				ors = append(ors, c+" BETWEEN ? AND ?")
				args = append(args, v-0.005, v+0.005)
			}
			conds = append(conds, "("+strings.Join(ors, " OR ")+")")
		}
	}
	minS := strings.TrimSpace(r.URL.Query().Get("min_amount"))
	maxS := strings.TrimSpace(r.URL.Query().Get("max_amount"))
	if minS != "" || maxS != "" {
		minV, errMin := strconv.ParseFloat(minS, 64)
		maxV, errMax := strconv.ParseFloat(maxS, 64)
		hasMin, hasMax := minS != "" && errMin == nil, maxS != "" && errMax == nil
		if hasMin || hasMax {
			ors := make([]string, 0, len(amtCols))
			for _, c := range amtCols {
				switch {
				case hasMin && hasMax:
					ors = append(ors, c+" BETWEEN ? AND ?")
					args = append(args, minV, maxV)
				case hasMin:
					ors = append(ors, c+" >= ?")
					args = append(args, minV)
				default:
					ors = append(ors, c+" <= ?")
					args = append(args, maxV)
				}
			}
			conds = append(conds, "("+strings.Join(ors, " OR ")+")")
		}
	}
	// 明细分类：买单 / 卖单 / 已结算 / 弃单（与前端标签一致）。
	// 买单、卖单、已结算三者互斥；弃单单独一类，只在「全部」里和它们并存。
	typ := strings.TrimSpace(r.URL.Query().Get("type"))
	// 已结算有两种：整张联单完结（group_items 里有已完结联单）/ 这条单据单独完结，或
	// 卖单成本已经说得清（自动结算，派生状态，不写库）。
	settledSub := "(id IN (SELECT gi.item_id FROM group_items gi JOIN link_groups g ON g.id=gi.group_id WHERE g.settled_at<>'')" +
		" OR settled_at<>'')"
	// 注意：自动结算只加在「已结算」分类里，不参与 live 的排除——
	// 否则大部分卖单会从「卖单」分类里消失，用户就找不到自己刚录的单了。
	settledAny := "(" + settledSub + " OR " + autoSettledSQL + ")"
	notSettled := "NOT " + settledSub
	live := notSettled + " AND discarded_at=''"
	switch typ {
	case "buy":
		conds = append(conds, purchaseWhere(shopID(r)), live)
	case "sell":
		conds = append(conds, "NOT ("+purchaseWhere(shopID(r))+")", live)
	case "unlinked":
		// 没有成本单的卖单：独立卖出、没挂到任何买进单上，货品成本按 0 计。
		// 单独筛出来，方便批量补关联或确认本来就是无成本代卖。
		//
		// 但「所在联单里已经有买进单」的卖单不算：这类单的成本由联单的买单核算，
		// 再提示它没挂成本会诱导用户重复挂一遍，把联单的账算乱。
		groupWithBuy := `id IN (SELECT gi.item_id FROM group_items gi WHERE EXISTS (
            SELECT 1 FROM group_items gi2 JOIN items i2 ON i2.id=gi2.item_id
            WHERE gi2.group_id=gi.group_id AND i2.shop_id=` + strconv.FormatInt(shopID(r), 10) + ` AND ` + purchaseCond + `))`
		conds = append(conds, "NOT ("+purchaseWhere(shopID(r))+")", "parent_id=0", "unit_cost<=0", "NOT ("+groupWithBuy+")", "COALESCE(cost_free,0)=0", externalSourceSQL, live)
	case "settled":
		// 只列卖单：已完结联单里的买单虽然也「结算了」，但用户看的是「这单赚了多少」，
		// 把进货单混进来会让条数和完结单数对不上（之前 7 条 vs 6 单就是这么来的）。
		conds = append(conds, settledAny, hasSell, "discarded_at=''")
	case "discarded":
		conds = append(conds, "discarded_at<>''")
	case "":
		// 全部：弃单也列出来（带灰色标记），否则用户找不到自己作废的那条
	default:
		// 未知分类按「全部」处理，但不出弃单，避免脏参数把弃单混进买卖单
		conds = append(conds, "discarded_at=''")
	}
	if len(conds) == 0 {
		return "1=1", args
	}
	return strings.Join(conds, " AND "), args
}

// orderClause：明细默认按交易时间倒序（用户明确要求），空时间排最后，
// 同一时间用 id 兜底保证分页稳定。支持 sort=time|amount|profit|created 与 order=asc|desc。
// 全部走白名单映射，绝不拼接用户输入，避免 SQL 注入。
func orderClause(r *http.Request) string {
	sort := r.URL.Query().Get("sort")
	dir := "DESC"
	if strings.EqualFold(r.URL.Query().Get("order"), "asc") {
		dir = "ASC"
	}
	// 无交易时间的记录统一沉底（仅时间排序时）。
	// 必须与 idx_items_shop_deal 索引的表达式完全一致，否则索引用不上。
	timeExpr := dealTimeExpr
	var expr string
	switch sort {
	case "amount":
		expr = "COALESCE(NULLIF(sell_net,0), NULLIF(sell_price,0), buy_price, 0)"
	case "profit":
		expr = "(sell_net - buy_price - buy_shipping - sell_shipping - unit_cost)"
	case "created":
		expr = "created_at"
	default: // time
		if dir == "ASC" {
			// 升序时把空时间排到最后
			return "CASE WHEN " + timeExpr + "='' THEN 1 ELSE 0 END ASC, " + timeExpr + " ASC, id ASC"
		}
		return timeExpr + " DESC, id DESC"
	}
	return expr + " " + dir + ", id DESC"
}

func listRecords(w http.ResponseWriter, r *http.Request) {
	where, args := buildWhere(r) // shop-scope: buildWhere 内部已带 shop_id 条件

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	var total int
	if err := db.QueryRow("SELECT COUNT(*) FROM items WHERE "+where, args...).Scan(&total); err != nil {
		writeError(w, 500, "查询失败")
		return
	}

	sqlStr := "SELECT " + itemCols + " FROM items WHERE " + where +
		" ORDER BY " + orderClause(r) + " LIMIT ? OFFSET ?"
	args2 := append(append([]interface{}{}, args...), pageSize, offset)
	// 联单分摊成本：列表里把「自己没填成本、但联单有买进单」的卖单按分摊值显示，
	// 否则编辑弹窗与行内成本会显示 0，和首页/排行的口径对不上。
	// 必须在打开 items 查询前取完，避免多用户刷新明细时嵌套占用连接。
	alloc := loadGroupAlloc(shopID(r))
	rows, err := db.Query(sqlStr, args2...)
	if err != nil {
		writeError(w, 500, "查询失败")
		return
	}
	defer rows.Close()

	items := []Item{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			writeError(w, 500, "读取失败")
			return
		}
		if it.UnitCost <= 0 {
			if v, ok := alloc[it.ID]; ok && v > 0 {
				it.AllocCost = v
				// 派生成本与利润一并按分摊值重算，保证行内数字与汇总一致
				if it.Kind == "sale" {
					qty := it.Quantity
					if qty < 1 {
						qty = 1
					}
					goods := round2(v * float64(qty))
					feeAll := sellFeeTotal(it)
					rev := it.SellPrice
					if rev == 0 {
						rev = it.SellNet + feeAll
					}
					it.Cost = round2(goods + it.SellShipping + feeAll)
					it.Profit = round2(rev - goods - it.SellShipping - feeAll)
				}
			}
		}
		items = append(items, it)
	}
	writeJSON(w, map[string]interface{}{
		"items": items, "total": total, "page": page, "page_size": pageSize,
	})
}

// createRecord：按 side 创建买进单 / 卖出单。
// 卖出单可关联到某个买进单（parent_id），一个买进单可挂 N 个卖出单。
func createRecord(w http.ResponseWriter, r *http.Request) {
	var in ItemInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, 400, "请求体解析失败")
		return
	}
	sanitizeItemInput(&in) // 过滤 NUL/C0/DEL/C1 控制字符，避免写进 SQLite TEXT
	in.Product = strings.TrimSpace(in.Product)
	if in.Product == "" {
		writeError(w, 400, "商品名称不能为空（用于自动配对）")
		return
	}
	ts := nowStr()

	// 买进单：整批货，parent_id=0
	if in.Side == "buy" {
		it := &Item{}
		applySide(it, &in, "buy")
		id, err := insertItem(it, shopID(r), ts)
		if err != nil {
			writeError(w, 500, "保存失败")
			return
		}
		saved, _ := getItem(id, shopID(r))
		writeJSON(w, map[string]interface{}{"merged": false, "item": saved})
		return
	}

	// 卖出单
	if in.Side == "sell" {
		// 1) 前端已选定买进单（parent_id）且未要求独立 -> 直接关联
		if in.ParentID > 0 && !in.NoMerge {
			if parent, perr := getItem(in.ParentID, shopID(r)); perr == nil && parent.ParentID == 0 {
				it := &Item{}
				applySide(it, &in, "sell")
				linkSaleToPurchase(it, &parent)
				id, err := insertItem(it, shopID(r), ts)
				if err != nil {
					writeError(w, 500, "保存失败")
					return
				}
				saved, _ := getItem(id, shopID(r))
				gid := autoGroupForSale(id, parent.ID, in.Product, shopID(r))
				writeJSON(w, map[string]interface{}{"merged": false, "linked": true, "item": saved, "group_id": gid})
				return
			}
		}
		// 2) 未指定 parent -> 按商品名精确匹配现有买进单并自动关联
		if !in.NoMerge {
			if parent, ok := matchExactPurchase(in.Product, shopID(r)); ok {
				it := &Item{}
				applySide(it, &in, "sell")
				linkSaleToPurchase(it, &parent)
				id, err := insertItem(it, shopID(r), ts)
				if err != nil {
					writeError(w, 500, "保存失败")
					return
				}
				saved, _ := getItem(id, shopID(r))
				gid := autoGroupForSale(id, parent.ID, in.Product, shopID(r))
				writeJSON(w, map[string]interface{}{"merged": false, "linked": true, "item": saved, "group_id": gid})
				return
			}
		}
		// 3) 无匹配 / 强制独立 -> 独立卖出单（parent_id=0）
		it := &Item{}
		applySide(it, &in, "sell")
		id, err := insertItem(it, shopID(r), ts)
		if err != nil {
			writeError(w, 500, "保存失败")
			return
		}
		saved, _ := getItem(id, shopID(r))
		writeJSON(w, map[string]interface{}{"merged": false, "linked": false, "item": saved})
		return
	}

	// side 为空：两侧一起填 -> 独立完整记录。
	// 用户要求：这种一次录完买卖的单据，保存后自动生成一张联单（标题取商品名），
	// 不用再手动去联单页建一次。
	it := &Item{}
	applySide(it, &in, "")
	id, err := insertItem(it, shopID(r), ts)
	if err != nil {
		writeError(w, 500, "保存失败")
		return
	}
	saved, _ := getItem(id, shopID(r))
	gid := autoGroupForStandalone(id, saved.Product, shopID(r))
	writeJSON(w, map[string]interface{}{"merged": false, "item": saved, "group_id": gid})
}

// autoGroupForSale 卖出单关联买进单后，自动建 / 复用联单（best-effort，失败不影响主记录保存）。
func autoGroupForSale(saleID, parentID int64, product string, shopID int64) int64 {
	if parentID <= 0 {
		return 0
	}
	gid, err := ensureSaleGroup(saleID, []int64{parentID}, product, shopID)
	if err != nil {
		return 0
	}
	return gid
}

// normProduct 归一化商品名：去空格/标点/大小写，用于相似度匹配
func normProduct(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = productNormRe.ReplaceAllString(s, "")
	return s
}

// itemProductNorm 优先使用数据库已回填的归一化列，避免统计和匹配循环里
// 对同一个商品反复做小写、去标点处理；只有旧数据列为空时才现场计算。
func itemProductNorm(it Item) string {
	if it.ProductNorm != "" {
		return it.ProductNorm
	}
	return normProduct(it.Product)
}

// purchaseWhere 选出「买进单」：parent_id=0 且有买信息、尚无卖信息。
//
// shopID 直接拼进 SQL（而非占位符）：它是 int64，不存在注入面，
// 而该片段会被嵌进 "NOT (...)"、子查询等多种上下文，占位符无法统一参数顺序。
// 买进单判据：还没卖出、没被单独完结、也没被弃单。
// 完结的货要么已经进联单对账、要么用户明确结案，都不该再出现在在库与联单候选里。
const purchaseCond = "parent_id=0 AND (buy_price>0 OR buy_order_no<>'' OR buy_counterparty<>'') " +
	"AND NOT (sell_net>0 OR sell_price>0 OR sell_order_no<>'' OR sell_counterparty<>'') " +
	"AND settled_at='' AND discarded_at=''"

func purchaseWhere(shopID int64) string {
	return "shop_id=" + strconv.FormatInt(shopID, 10) + " AND " + purchaseCond
}

// matchExactPurchase 按归一化商品名精确匹配一个未售出的买进单。
// 直接命中 product_norm 索引列（idx_items_norm），不再全表扫描 + 逐条归一化。
// 必须限定店铺：否则 A 店的卖出单会吃掉 B 店同名的买进单。
func matchExactPurchase(product string, shopID int64) (Item, bool) {
	nq := normProduct(product)
	if nq == "" {
		return Item{}, false
	}
	rows, err := db.Query("SELECT "+itemCols+" FROM items WHERE product_norm=? AND "+purchaseWhere(shopID)+" ORDER BY id DESC LIMIT 1", nq)
	if err != nil {
		return Item{}, false
	}
	defer rows.Close()
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			continue
		}
		if itemProductNorm(it) == nq {
			return it, true
		}
	}
	return Item{}, false
}

// matchSimilarPurchases 返回与商品名相似的买进单（精确匹配排第一），最多 limit 条
func matchSimilarPurchases(product string, shopID int64, limit int) []Item {
	nq := normProduct(product)
	if nq == "" {
		return nil
	}
	rows, err := db.Query("SELECT " + itemCols + " FROM items WHERE " + purchaseWhere(shopID) + " ORDER BY id DESC")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			continue
		}
		np := itemProductNorm(it)
		if np == nq {
			out = append([]Item{it}, out...) // 精确匹配置顶
			continue
		}
		if similarity(nq, np) >= 0.6 {
			out = append(out, it)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// similarity 基于编辑距离的相似度（0~1）
func similarity(a, b string) float64 {
	if a == b {
		return 1
	}
	la, lb := len([]rune(a)), len([]rune(b))
	if la == 0 || lb == 0 {
		return 0
	}
	dist := editDistance(a, b)
	return 1 - float64(dist)/float64(maxInt(la, lb))
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	n, m := len(ra), len(rb)
	if n == 0 {
		return m
	}
	if m == 0 {
		return n
	}
	prev := make([]int, m+1)
	cur := make([]int, m+1)
	for j := 0; j <= m; j++ {
		prev[j] = j
	}
	for i := 1; i <= n; i++ {
		cur[0] = i
		for j := 1; j <= m; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = minInt(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[m]
}

func minInt(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// childCount 返回买进单已挂的卖出单数
func childCount(parentID int64) int {
	var n int
	db.QueryRow("SELECT COUNT(*) FROM items WHERE parent_id=?", parentID).Scan(&n)
	return n
}

// linkSaleToPurchase 把卖出单挂到买进单，自动带出单件成本（批量总价/件数 + 批量运费/件数）
func linkSaleToPurchase(it *Item, parent *Item) {
	it.ParentID = parent.ID
	q := parent.Quantity
	if q < 1 {
		q = 1
	}
	it.UnitCost = round2((parent.BuyPrice + parent.BuyShipping) / float64(q))
	it.CostFree = false // 挂上买进单就有成本了，无成本标记自动失效
}

// insertItem 插入一条完整记录（含 1:N 字段）。shopID 显式传入，
// 避免依赖 Item 结构体字段而在某条分支里漏设。
func insertItem(it *Item, shopID int64, ts string) (int64, error) {
	res, err := db.Exec(`INSERT INTO items
        (shop_id, product, product_norm, parent_id, quantity, unit_cost,
         buy_price,buy_shipping,buy_ship_tag,buy_order_no,buy_time,buy_counterparty,
         sell_price,sell_fee,sell_wuyou_fee,sell_net,sell_shipping,sell_ship_tag,sell_order_no,sell_time,sell_counterparty,
         note, consign_time, source, owner, cost_free, created_at, updated_at)
        VALUES (?,?,?,?,?,?, ?,?,?,?,?,?, ?,?,?,?,?,?,?,?,?, ?,?,?,?,?,?,?)`,
		shopID, it.Product, normProduct(it.Product), it.ParentID, it.Quantity, it.UnitCost,
		it.BuyPrice, it.BuyShipping, it.BuyShipTag, it.BuyOrderNo, it.BuyTime, it.BuyCounterparty,
		it.SellPrice, it.SellFee, it.SellWuyouFee, it.SellNet, it.SellShipping, it.SellShipTag, it.SellOrderNo, it.SellTime, it.SellCounterparty,
		it.Note, it.ConsignTime, normSource(it.Source), it.Owner, boolToInt(it.CostFree), ts, ts)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// applySide 把录入的一侧字段写入 item（side 为空表示全量提交）
func applySide(it *Item, in *ItemInput, side string) {
	if in.Product != "" {
		it.Product = in.Product
	}
	// 货物来源与归属：卖单/买单都要保存（卖单常标寄卖，之前只写在买进分支会丢）
	it.Source = normSource(in.Source)
	it.Owner = strings.TrimSpace(in.Owner)
	if side == "" || side == "buy" {
		it.Source = normSource(in.Source)
		it.Owner = strings.TrimSpace(in.Owner)
		it.BuyPrice = in.BuyPrice
		it.BuyShipping = in.BuyShipping
		it.BuyShipTag = in.BuyShipTag
		it.BuyOrderNo = in.BuyOrderNo
		it.BuyTime = in.BuyTime
		it.BuyCounterparty = in.BuyCounterparty
	}
	if side == "" || side == "sell" {
		it.SellPrice = in.SellPrice
		it.SellFee = in.SellFee
		it.SellWuyouFee = in.SellWuyouFee
		it.SellNet = in.SellNet
		it.SellShipping = in.SellShipping
		it.SellShipTag = in.SellShipTag
		it.SellOrderNo = in.SellOrderNo
		it.SellTime = in.SellTime
		it.SellCounterparty = in.SellCounterparty
	}
	// 1:N 关联字段
	if side == "buy" || side == "" {
		it.ParentID = 0
		if in.Quantity >= 1 {
			it.Quantity = in.Quantity
		} else if it.Quantity < 1 {
			it.Quantity = 1
		}
	}
	if side == "sell" {
		it.ParentID = in.ParentID
		if in.Quantity >= 1 {
			it.Quantity = in.Quantity
		} else if it.Quantity < 1 {
			it.Quantity = 1
		}
		if in.UnitCost > 0 {
			it.UnitCost = in.UnitCost
		}
		// 只有独立卖单才能标「无成本」：挂了买进单就有成本了
		it.CostFree = in.CostFree && in.ParentID == 0
	}
	if in.Note != "" {
		it.Note = in.Note
	}
	it.derive()
}

// ---------- 单条：编辑 / 删除 ----------

func handleRecordByID(w http.ResponseWriter, r *http.Request) {
	if r.Method == "PUT" || r.Method == "DELETE" {
		defer refreshPurchaseIndex()
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/records/"), 10, 64)
	if err != nil {
		writeError(w, 400, "无效 id")
		return
	}
	switch r.Method {
	case "GET":
		it, err := getItem(id, shopID(r))
		if err != nil {
			writeError(w, 404, "记录不存在")
			return
		}
		writeJSON(w, it)
	case "PUT":
		if gid, gtitle := settledGroupOf(id, shopID(r)); gid > 0 {
			writeSettledBlock(w, gtitle, "修改")
			return
		}
		if it, e := getItem(id, shopID(r)); e == nil && it.SettledAt != "" {
			writeError(w, 409, "这条记录已单独完结，先点「取消完结」再修改")
			return
		}
		var in ItemInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, 400, "请求体解析失败")
			return
		}
		sanitizeItemInput(&in)
		it, err := getItem(id, shopID(r))
		if err != nil {
			writeError(w, 404, "记录不存在")
			return
		}
		// 乐观锁：多人同时编辑同一条时，后提交者会拿到 409 而不是静默覆盖。
		// 前端不传 expected_updated_at 时跳过（兼容旧客户端与脚本调用）。
		if in.ExpectedUpdatedAt != "" && it.UpdatedAt != "" && in.ExpectedUpdatedAt != it.UpdatedAt {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":   "这条记录已被其他人修改，请刷新后重试",
				"code":    "conflict",
				"current": it,
			})
			return
		}
		// 编辑是全量覆盖：把两侧都替换
		it.Product = strings.TrimSpace(in.Product)
		it.BuyPrice, it.BuyShipping, it.BuyShipTag = in.BuyPrice, in.BuyShipping, in.BuyShipTag
		it.BuyOrderNo, it.BuyTime, it.BuyCounterparty = in.BuyOrderNo, in.BuyTime, in.BuyCounterparty
		it.SellPrice, it.SellFee, it.SellWuyouFee, it.SellNet = in.SellPrice, in.SellFee, in.SellWuyouFee, in.SellNet
		it.SellShipping, it.SellShipTag = in.SellShipping, in.SellShipTag
		it.SellOrderNo, it.SellTime, it.SellCounterparty = in.SellOrderNo, in.SellTime, in.SellCounterparty
		it.Note = in.Note
		it.Source = normSource(in.Source)
		it.Owner = strings.TrimSpace(in.Owner)
		// 单件成本：独立卖单可以直接填（改成本），0 也要能写回去
		if in.UnitCost >= 0 && in.ParentID == 0 {
			it.UnitCost = in.UnitCost
		}
		// 只有独立卖单能标无成本；勾了「无成本」就把手填成本清零，避免两个口径打架
		it.CostFree = in.CostFree && in.ParentID == 0
		if it.CostFree {
			it.UnitCost = 0
		}
		if in.Quantity >= 1 {
			it.Quantity = in.Quantity
		}
		if in.ParentID != it.ParentID {
			it.ParentID = in.ParentID
			if in.ParentID > 0 {
				if p, perr := getItem(in.ParentID, shopID(r)); perr == nil && p.ParentID == 0 {
					linkSaleToPurchase(&it, &p)
					autoGroupForSale(id, in.ParentID, it.Product, shopID(r))
				}
			}
		}
		if in.UnitCost > 0 {
			it.UnitCost = in.UnitCost
		}
		it.derive()
		if err := updateItem(&it, shopID(r)); err != nil {
			writeError(w, 500, "更新失败")
			return
		}
		writeJSON(w, it)
	case "DELETE":
		if gid, gtitle := settledGroupOf(id, shopID(r)); gid > 0 {
			writeSettledBlock(w, gtitle, "删除")
			return
		}
		if it, e := getItem(id, shopID(r)); e == nil && it.SettledAt != "" {
			writeError(w, 409, "这条记录已单独完结，先点「取消完结」再删除")
			return
		}
		// 删除前取快照（saveUndo 失败只记日志、不返回 undo_token，不阻断删除）
		token := snapshotItemsUndo("delete_one", []int64{id}, shopID(r))
		removeAttachmentFiles([]int64{id}, shopID(r))
		if _, err := db.Exec("DELETE FROM items WHERE id=? AND shop_id=?", id, shopID(r)); err != nil {
			writeError(w, 500, "删除失败")
			return
		}
		out := map[string]interface{}{"ok": true}
		if token != "" {
			out["undo_token"] = token
		}
		writeJSON(w, out)
	default:
		writeError(w, 405, "method not allowed")
	}
}

// ---------- 统计 ----------

type Stats struct {
	TotalCount    int     `json:"total_count"`
	SoldCount     int     `json:"sold_count"`
	PendingCount  int     `json:"pending_count"`
	TotalSales    float64 `json:"total_sales"`     // 销售额 = Σ成交总额（未扣平台费）
	TotalNet      float64 `json:"total_net"`       // 到手合计 = Σ到手价（已扣平台费）
	TotalCost     float64 `json:"total_cost"`      // 完全成本 = 货 + 运费 + 软件服务费 + 无忧卖服务费
	TotalProfit   float64 `json:"total_profit"`    // 利润 = 销售额 − 总成本
	TotalPrice    float64 `json:"total_price"`     // 成交总额（未扣费）
	HoldingCost   float64 `json:"holding_cost"`    // 在库占用 = 有效采购 − 已填成本卖单 − 联单已分摊成本
	ReleasedCost  float64 `json:"released_cost"`   // 已完结联单释放的占用资金（透明展示）
	TotalShipping float64 `json:"total_shipping"`  // 运费支出合计 = Σ(买运费 + 卖运费)
	TotalWuyouFee float64 `json:"total_wuyou_fee"` // 无忧卖类目软件服务费合计
	TotalFee      float64 `json:"total_fee"`       // 手续费合计 = Σ(软件服务费 + 无忧卖服务费)
	TotalGoods    float64 `json:"total_goods"`     // 已售货品成本（卖单带走的单件成本）
	TotalInvest   float64 `json:"total_invest"`    // 总投入 = 所有买单（买价 + 买运费）
	// 在库按库龄分档，看哪批货压得久
	Stock30      float64 `json:"stock_30"`
	Stock60      float64 `json:"stock_60"`
	Stock90      float64 `json:"stock_90"`
	StockOld     float64 `json:"stock_old"`
	ProfitMargin float64 `json:"profit_margin"` // 利润率 = 利润 / 销售额（%）
	AvgProfit    float64 `json:"avg_profit"`    // 已成交单笔平均利润
	// 完成单口径（仅统计已完结联单内的明细）
	SettledSales  float64 `json:"settled_sales"`  // 完结单成交额
	SettledCost   float64 `json:"settled_cost"`   // 完结单总成本
	SettledNet    float64 `json:"settled_net"`    // 完结单到手合计
	SettledProfit float64 `json:"settled_profit"` // 完结单净利润 = 成交额 − 总成本
	SettledSold   int     `json:"settled_sold"`   // 完结成交单数（sale/standalone 的完结明细数）
	// 弃单：保留记录但不参与任何金额口径，单独计数只为在界面上说明「有 N 单已弃」
	DiscardedCount int `json:"discarded_count"`
	// 注：早期这里有「无成本卖单」两个字段，但从未累加过、前端也没用，
	// 现在「未挂成本 / 无成本」由 /api/product-profit 的 unlinked / cost_free_count 提供。
	// 销售额分档：周 / 月 / 今日，按卖出时间统计所有登记的卖单
	TodaySales float64 `json:"today_sales"`
	TodayCount int     `json:"today_count"`
	WeekSales  float64 `json:"week_sales"`
	MonthSales float64 `json:"month_sales"`
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	where, args := buildWhere(r) // shop-scope: buildWhere 内部已带 shop_id 条件

	all := r.URL.Query().Get("all") == "1"
	sid := shopID(r)
	if all {
		sid = -1
	}
	// 已完结联单的明细 id 集合（用于「完成单净利润」统计）
	settledSet := map[int64]bool{}
	var sRows *sql.Rows
	var e error
	if sid < 0 {
		sRows, e = db.Query(`SELECT DISTINCT gi.item_id FROM group_items gi
            JOIN link_groups g ON g.id=gi.group_id WHERE g.settled_at<>''`)
	} else {
		sRows, e = db.Query(`SELECT DISTINCT gi.item_id FROM group_items gi
            JOIN link_groups g ON g.id=gi.group_id WHERE g.settled_at<>'' AND g.shop_id=?`, sid)
	}
	if e == nil {
		for sRows.Next() {
			var iid int64
			if sRows.Scan(&iid) == nil {
				settledSet[iid] = true
			}
		}
		sRows.Close()
	}

	// 联单内卖单的成本分摊：整单买单按卖单成交价占比落到单件上。
	// 与商品利润排行同一口径，否则首页净利润会因为联单卖单成本是 0 而虚高。
	alloc := loadGroupAlloc(sid)

	var s Stats
	// 待售“件数”历史上按未进联单的采购批次统计，保留这一展示口径；
	// 在库成本则按全部采购减已售成本计算，两者用途不同。
	groupItemIDs := map[int64]bool{}
	var gr *sql.Rows
	var gre error
	if sid < 0 {
		gr, gre = db.Query(`SELECT DISTINCT gi.item_id FROM group_items gi
			JOIN link_groups g ON g.id=gi.group_id`)
	} else {
		gr, gre = db.Query(`SELECT DISTINCT gi.item_id FROM group_items gi
			JOIN link_groups g ON g.id=gi.group_id WHERE g.shop_id=?`, sid)
	}
	if gre == nil {
		for gr.Next() {
			var iid int64
			if gr.Scan(&iid) == nil {
				groupItemIDs[iid] = true
			}
		}
		gr.Close()
	}
	var lots []holdingLot
	var saleDeductions []holdingDeduction

	// 所有派生集合都准备好后再读主表。这样任何并发请求都只需占用一条连接，
	// 多人同时刷新首页不会出现每个请求各拿一条、再一起等第二条连接的死锁。
	rows, err := db.Query("SELECT "+itemCols+" FROM items WHERE "+where, args...)
	if err != nil {
		writeError(w, 500, "统计失败")
		return
	}
	today := time.Now().Format("2006-01-02")
	weekStart := time.Now().AddDate(0, 0, -int((time.Now().Weekday()+6)%7)).Format("2006-01-02")
	monthStart := time.Now().Format("2006-01") + "-01"
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			continue
		}
		// 弃单等于没录过：不进记录数、不进任何金额口径，但保留在库可查
		if it.Kind == "discarded" {
			s.DiscardedCount++
			continue
		}
		// 寄卖 / 代采：货是别人的，不进任何利润 / 成本 / 运费 / 服务费口径
		if isExternalGoods(it.Source) {
			continue
		}
		s.TotalCount++
		// 服务费（软件服务费 + 无忧卖）。若两条费率为 0 但到手价 < 成交价，
		// 用 成交价 − 到手价 反推总服务费（识别漏抓手续费行时的兜底，避免统计为 0）。
		feeAll := sellFeeTotal(it)
		// 周 / 月 / 今日销售额：按卖出时间归档，统计所有登记的卖单
		if it.HasSell && it.SellTime != "" {
			d := it.SellTime
			if len(d) >= 10 {
				d = d[:10]
			}
			if d == today {
				s.TodaySales += it.SellPrice
				s.TodayCount++
			}
			if d >= weekStart {
				s.WeekSales += it.SellPrice
			}
			if d >= monthStart {
				s.MonthSales += it.SellPrice
			}
		}

		switch it.Kind {
		case "purchase":
			// 经营口径：进货不算已售成本，只有卖出去才结转。
			// 在库先记全部有效采购，循环结束后再按卖单实际成本冲减。
			gross := it.BuyPrice + it.BuyShipping
			s.TotalInvest += gross
			if it.SettledAt == "" && gross > 0 {
				lots = append(lots, holdingLot{
					ID: it.ID, Product: it.Product, Norm: itemProductNorm(it),
					Gross: round2(gross), BuyTime: it.BuyTime, AgeBand: holdingAgeBand(it.BuyTime),
				})
			}
		case "sale":
			// 销售额取成交价；已售成本 = 这一单带走的货品成本 + 卖运费 + 服务费。
			qty := it.Quantity
			if qty < 1 {
				qty = 1
			}
			goods := round2(effectiveUnitCost(it, alloc) * float64(qty))
			s.TotalSales += it.SellPrice
			s.TotalNet += it.SellNet
			s.TotalPrice += it.SellPrice
			s.TotalFee += feeAll
			s.TotalWuyouFee += it.SellWuyouFee
			s.TotalGoods += goods
			s.TotalShipping += it.SellShipping
			s.TotalCost += goods + it.SellShipping + feeAll
			s.SoldCount++
			if !it.CostFree {
				deduct := 0.0
				if it.UnitCost > 0 {
					deduct = round2(it.UnitCost * float64(qty))
				} else if v, ok := alloc[it.ID]; ok {
					deduct = round2(v * float64(qty))
				}
				if deduct > 0 {
					saleDeductions = append(saleDeductions, holdingDeduction{
						ParentID: it.ParentID, Norm: itemProductNorm(it), Amount: deduct, SellTime: it.SellTime,
					})
				}
			}
		case "standalone":
			// 独立完整记录（旧数据/两侧一起填）
			s.TotalSales += it.SellPrice
			s.TotalNet += it.SellNet
			s.TotalPrice += it.SellPrice
			s.TotalFee += feeAll
			s.TotalWuyouFee += it.SellWuyouFee
			s.TotalCost += it.BuyPrice + it.BuyShipping + it.SellShipping + feeAll
			s.TotalGoods += it.BuyPrice
			s.TotalShipping += it.BuyShipping + it.SellShipping
			s.TotalInvest += it.BuyPrice + it.BuyShipping
			s.SoldCount++
		}
		// 完成单净利润：算「已完结联单」内的明细 + 成本已经说得清的卖单（自动结算）。
		// 以前只认联单完结，散卖单填了成本也不进这个口径，用户得为了看一眼完结利润去包联单；
		// 现在卖单只要成本明确就直接算，联单仍然管「一批货一起完结」的场景。
		if settledSet[it.ID] {
			switch it.Kind {
			case "purchase":
				s.SettledCost += it.BuyPrice + it.BuyShipping
			case "sale":
				// 完结成交额取「卖单到手价」——到手价里已经扣过服务费、无忧卖和卖运费，
				// 所以完结成本只再减联单里买单的买价+买运费，不重复扣费。
				s.SettledSales += it.SellNet
				s.SettledNet += it.SellNet
				s.SettledSold++
			case "standalone":
				s.SettledSales += it.SellNet
				s.SettledNet += it.SellNet
				s.SettledCost += it.BuyPrice + it.BuyShipping
				s.SettledSold++
			}
		} else if autoSettled(it) && !settledSet[it.ParentID] {
			// 自带成本的卖单：到手价进完结成交额，成本按这单带走的货品成本算。
			// 父买单已经在完结联单里时跳过，那份成本由联单统一结，避免重复计。
			sq := it.Quantity
			if sq < 1 {
				sq = 1
			}
			s.SettledSales += it.SellNet
			s.SettledNet += it.SellNet
			s.SettledSold++
			if it.Kind == "standalone" {
				s.SettledCost += it.BuyPrice + it.BuyShipping
			} else {
				s.SettledCost += round2(effectiveUnitCost(it, alloc) * float64(sq))
			}
		}
	}
	rows.Close() // 关闭后再查已完结联单，避免占用连接
	lots = applyHoldingDeductions(lots, saleDeductions)
	for _, lot := range lots {
		if lot.Remaining <= 0.005 {
			continue
		}
		s.PendingCount++
		s.HoldingCost += lot.Remaining
		switch lot.AgeBand {
		case 1:
			s.Stock60 += lot.Remaining
		case 2:
			s.Stock90 += lot.Remaining
		case 3:
			s.StockOld += lot.Remaining
		default:
			s.Stock30 += lot.Remaining
		}
	}

	// 口径自洽：销售额用成交总额（未扣费），完全成本 = 货品 + 运费 + 服务费，
	// 因此 总成本 = TotalGoods + TotalShipping + TotalFee，利润不会因加了服务费而重复扣减。
	s.TotalProfit = round2(s.TotalSales - s.TotalCost)
	s.TotalSales = round2(s.TotalSales)
	s.TotalNet = round2(s.TotalNet)
	s.TotalCost = round2(s.TotalCost)
	s.TotalFee = round2(s.TotalFee)
	s.TotalPrice = round2(s.TotalPrice)
	s.HoldingCost = round2(s.HoldingCost)
	s.TotalShipping = round2(s.TotalShipping)
	s.TotalWuyouFee = round2(s.TotalWuyouFee)
	s.TotalGoods = round2(s.TotalGoods)
	s.TotalInvest = round2(s.TotalInvest)
	s.Stock30 = round2(s.Stock30)
	s.Stock60 = round2(s.Stock60)
	s.Stock90 = round2(s.Stock90)
	s.StockOld = round2(s.StockOld)
	s.SettledSales = round2(s.SettledSales)
	s.SettledCost = round2(s.SettledCost)
	s.SettledNet = round2(s.SettledNet)
	s.SettledProfit = round2(s.SettledSales - s.SettledCost)
	s.TodaySales = round2(s.TodaySales)
	s.WeekSales = round2(s.WeekSales)
	s.MonthSales = round2(s.MonthSales)
	if s.TotalSales > 0 {
		s.ProfitMargin = round2(s.TotalProfit / s.TotalSales * 100)
	}
	if s.SoldCount > 0 {
		s.AvgProfit = round2(s.TotalProfit / float64(s.SoldCount))
	}
	writeJSON(w, s)
}

// ---------- 工具 ----------

const itemCols = `id, product, COALESCE(product_norm,'') AS product_norm, parent_id, quantity, unit_cost,
    buy_price, buy_shipping, buy_ship_tag, buy_order_no, buy_time, buy_counterparty,
    sell_price, sell_fee, sell_wuyou_fee, sell_net, sell_shipping, sell_ship_tag, sell_order_no, sell_time, sell_counterparty,
    note, COALESCE(attachment,'') AS attachment, COALESCE(consign_time,'') AS consign_time,
    COALESCE(settled_at,'') AS settled_at, COALESCE(discarded_at,'') AS discarded_at,
    COALESCE(source,'') AS source, COALESCE(owner,'') AS owner,
    COALESCE(cost_free,0) AS cost_free,
    created_at, updated_at`

func scanItem(rows *sql.Rows) (Item, error) {
	var it Item
	err := rows.Scan(
		&it.ID, &it.Product, &it.ProductNorm, &it.ParentID, &it.Quantity, &it.UnitCost,
		&it.BuyPrice, &it.BuyShipping, &it.BuyShipTag, &it.BuyOrderNo, &it.BuyTime, &it.BuyCounterparty,
		&it.SellPrice, &it.SellFee, &it.SellWuyouFee, &it.SellNet, &it.SellShipping, &it.SellShipTag, &it.SellOrderNo, &it.SellTime, &it.SellCounterparty,
		&it.Note, &it.Attachment, &it.ConsignTime, &it.SettledAt, &it.DiscardedAt,
		&it.Source, &it.Owner, &it.CostFree, &it.CreatedAt, &it.UpdatedAt,
	)
	if err == nil {
		it.derive()
	}
	return it, err
}

// getItem 取单条记录。必须带 shopID：仅凭 id 就能读到别家店铺的数据是最典型的越权。
func getItem(id int64, shopID int64) (Item, error) {
	rows, err := db.Query("SELECT "+itemCols+" FROM items WHERE id=? AND shop_id=?", id, shopID)
	if err != nil {
		return Item{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return Item{}, sql.ErrNoRows
	}
	return scanItem(rows)
}

func updateItem(it *Item, shopID int64) error {
	now := nowStr()
	it.UpdatedAt = now // 回写，保证响应里的 updated_at 可用于下一次乐观锁比较
	_, err := db.Exec(`UPDATE items SET
        product=?, product_norm=?, parent_id=?, quantity=?, unit_cost=?,
        buy_price=?, buy_shipping=?, buy_ship_tag=?, buy_order_no=?, buy_time=?, buy_counterparty=?,
        sell_price=?, sell_fee=?, sell_wuyou_fee=?, sell_net=?, sell_shipping=?, sell_ship_tag=?, sell_order_no=?, sell_time=?, sell_counterparty=?,
        note=?, source=?, owner=?, cost_free=?, updated_at=? WHERE id=? AND shop_id=?`,
		it.Product, normProduct(it.Product), it.ParentID, it.Quantity, it.UnitCost,
		it.BuyPrice, it.BuyShipping, it.BuyShipTag, it.BuyOrderNo, it.BuyTime, it.BuyCounterparty,
		it.SellPrice, it.SellFee, it.SellWuyouFee, it.SellNet, it.SellShipping, it.SellShipTag, it.SellOrderNo, it.SellTime, it.SellCounterparty,
		it.Note, normSource(it.Source), it.Owner, boolToInt(it.CostFree), now, it.ID, shopID)
	return err
}

// writeSettledBlock 已完结联单内的记录不允许直接改/删，统一返回 409 + 中文提示。
// 用 409 而不是 400/403：前端拿到 code=settled 就能给出「去重新开启」的引导，
// 也不会被「乐观锁冲突」那套逻辑误判成并发修改。
func writeSettledBlock(w http.ResponseWriter, title, action string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusConflict)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": settledBlockMsg(title, action),
		"code":  "settled",
	})
}

// handleRecordsExists 批量查这些订单号是否已经在库里，给「批量导入」做重复预检。
//
// 一张截图可能被导入两次（换设备、重试、手动补录），但订单号在闲鱼侧唯一，
// 拿它当去重键最可靠。只查当前店铺：别家店有同一个订单号不算重复。
func handleRecordsExists(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	var in struct {
		OrderNos []string `json:"order_nos"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, 400, "请求体解析失败")
		return
	}
	// 去空白、去重、封顶 200 条：再多也没必要，且 IN 列表过长会拖慢查询
	list := make([]string, 0, len(in.OrderNos))
	seen := make(map[string]bool, len(in.OrderNos))
	for _, s := range in.OrderNos {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		list = append(list, s)
		if len(list) >= 200 {
			break
		}
	}
	out := map[string]int64{}
	if len(list) == 0 {
		writeJSON(w, map[string]interface{}{"existing": out})
		return
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(list)), ",")
	args := make([]interface{}, 0, len(list)*2+1)
	args = append(args, shopID(r))
	for _, s := range list {
		args = append(args, s)
	}
	for _, s := range list {
		args = append(args, s)
	}
	q := "SELECT id, buy_order_no, sell_order_no FROM items WHERE shop_id=? AND (buy_order_no IN (" + ph +
		") OR sell_order_no IN (" + ph + ")) LIMIT 500"
	rows, err := db.Query(q, args...)
	if err != nil {
		writeError(w, 500, "查询失败")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var bno, sno string
		if err := rows.Scan(&id, &bno, &sno); err != nil {
			continue
		}
		if bno != "" {
			if _, ok := out[bno]; !ok {
				out[bno] = id
			}
		}
		if sno != "" {
			if _, ok := out[sno]; !ok {
				out[sno] = id
			}
		}
	}
	writeJSON(w, map[string]interface{}{"existing": out})
}

// ---------- 商品利润排行 ----------

type productProfitRow struct {
	Product  string  `json:"product"`
	Norm     string  `json:"norm"`            // 归一化商品名：明细页用 product_norm 精确过滤，避免同名不同写法漏单
	Sales    float64 `json:"sales"`           // 成交价合计
	Cost     float64 `json:"cost"`            // 已售成本（货品 + 卖运费 + 服务费）
	Profit   float64 `json:"profit"`          // 利润 = 销售额 − 已售成本
	Margin   float64 `json:"margin"`          // 利润率（%）
	Sold     int     `json:"sold"`            // 成交单数
	Stock    float64 `json:"stock"`           // 该商品还在库的进货成本
	StockQty int     `json:"stock_qty"`       // 在库批次数
	Unlinked int     `json:"unlinked"`        // 其中没有成本单的卖单数（成本按 0 计，利润虚高）
	CostFree int     `json:"cost_free_count"` // 其中用户主动标记「无成本」（代卖/寄卖/赠送）的卖单数
}

// handleProductProfit 按商品聚合「卖了多少 / 成本多少 / 赚多少」。
// 口径与首页一致：销售额用成交价，已售成本 = 卖单单件成本×件数 + 卖运费 + 服务费。
// 没有挂买进单的卖单按成本 0 计，同时单独计数，避免把虚高的利润当成真的。
func handleProductProfit(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "method not allowed")
		return
	}
	writeJSON(w, map[string]interface{}{"items": computeProductProfit(shopID(r))})
}

// handleProductProfitExport 导出成 Excel（一行一个商品 + 合计行），给对账/复盘用。
func handleProductProfitExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "method not allowed")
		return
	}
	rows := computeProductProfit(shopID(r))
	data, err := buildProductProfitXLSX(rows)
	if err != nil {
		writeError(w, 500, "生成报表失败")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	stamp := time.Now().Format("20060102-1504")
	setDownloadName(w, "product-profit-"+stamp+".xlsx", "商品利润-"+stamp+".xlsx")
	w.Write(data)
}

// loadGroupedWithBuy 返回「所在联单里有买进单」的明细 id 集合。
// 这类明细的成本由联单买单核算：既不算未挂成本，也不允许再标「无成本」。
func loadGroupedWithBuy(sid int64) map[int64]bool {
	out := map[int64]bool{}
	rows, err := db.Query(`SELECT gi.item_id FROM group_items gi WHERE EXISTS (
        SELECT 1 FROM group_items gi2 JOIN items i2 ON i2.id=gi2.item_id
        WHERE gi2.group_id=gi.group_id AND i2.shop_id=? AND `+purchaseCond+`)`, sid)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var iid int64
		if rows.Scan(&iid) == nil {
			out[iid] = true
		}
	}
	return out
}

// loadGroupAlloc 计算「联单里没有单独成本的卖单」应分摊到的单件成本。
//
// 规则（用户确认）：联单内有买进单时，把买单的买价 + 买运费按各卖单的成交价占比分摊。
// 可分摊总额按件数收敛（不超过买单件数），避免备货只卖出几件时把整批进货成本压到已卖的单上。
// 只在内存里派生、不写库：联单成员或价格一变，下次请求自动按新数据重算，不会留下脏成本。
func loadGroupAlloc(sid int64) map[int64]float64 {
	out := map[int64]float64{}
	if db == nil {
		return out
	}
	members := map[int64][]int64{}
	shopCond := "WHERE g.shop_id=?"
	var mRows *sql.Rows
	var err error
	if sid < 0 {
		mRows, err = db.Query(`SELECT gi.group_id, gi.item_id FROM group_items gi
            JOIN link_groups g ON g.id=gi.group_id`)
	} else {
		mRows, err = db.Query(`SELECT gi.group_id, gi.item_id FROM group_items gi
            JOIN link_groups g ON g.id=gi.group_id WHERE g.shop_id=?`, sid)
	}
	_ = shopCond
	if err != nil {
		return out
	}
	for mRows.Next() {
		var gid, iid int64
		if mRows.Scan(&gid, &iid) == nil {
			members[gid] = append(members[gid], iid)
		}
	}
	mRows.Close()
	if len(members) == 0 {
		return out
	}

	idx := map[int64]Item{}
	var iRows *sql.Rows
	if sid < 0 {
		iRows, err = db.Query("SELECT " + allocItemCols + " FROM items")
	} else {
		iRows, err = db.Query("SELECT "+allocItemCols+" FROM items WHERE shop_id=?", sid)
	}
	if err != nil {
		return out
	}
	for iRows.Next() {
		it, e := scanAllocItem(iRows)
		if e == nil {
			idx[it.ID] = it
		}
	}
	iRows.Close()

	for _, ids := range members {
		items := make([]Item, 0, len(ids))
		for _, id := range ids {
			it, ok := idx[id]
			if ok {
				items = append(items, it)
			}
		}
		for id, unit := range groupAllocForItems(items) {
			out[id] = unit
		}
	}
	return out
}

// allocItemCols 只读取联单成本分摊需要的字段。
// 分摊会被明细分页、首页统计、趋势、价格提示和导出反复调用；
// 这里绝不读取 note / attachment / 各类标签与时间戳，避免为了几个金额字段
// 把整张宽表（尤其是附件字段）搬进内存。派生 kind / has_* 的条件必须与
// itemCols + scanItem 保持同一业务口径。
const allocItemCols = `id, product, COALESCE(product_norm,'') AS product_norm, parent_id, quantity, unit_cost,
    buy_price, buy_shipping, buy_order_no, buy_counterparty,
    sell_price, sell_net, sell_order_no, sell_counterparty,
    COALESCE(cost_free,0) AS cost_free, COALESCE(discarded_at,'') AS discarded_at`

func scanAllocItem(rows *sql.Rows) (Item, error) {
	var it Item
	err := rows.Scan(
		&it.ID, &it.Product, &it.ProductNorm, &it.ParentID, &it.Quantity, &it.UnitCost,
		&it.BuyPrice, &it.BuyShipping, &it.BuyOrderNo, &it.BuyCounterparty,
		&it.SellPrice, &it.SellNet, &it.SellOrderNo, &it.SellCounterparty,
		&it.CostFree, &it.DiscardedAt,
	)
	if err == nil {
		// 复用统一派生逻辑，但不覆盖 discarded 标记以外的不相关字段。
		it.derive()
	}
	return it, err
}

// groupAllocForItems 只算一组联单成员之间的成本分摊，供全局统计与联单卡片共用。
func groupAllocForItems(items []Item) map[int64]float64 {
	out := map[int64]float64{}
	type sellEntry struct {
		id    int64
		price float64
		qty   float64
	}
	var buySum, buyQty float64
	var sells []sellEntry
	for _, it := range items {
		if it.Kind == "discarded" {
			continue
		}
		qty := float64(it.Quantity)
		if qty < 1 {
			qty = 1
		}
		switch it.Kind {
		case "purchase":
			buySum += it.BuyPrice + it.BuyShipping
			buyQty += qty
		case "sale":
			// 自己填过单件成本、或明确标记了「无成本」的，不吃联单分摊。
			if it.UnitCost > 0 || it.CostFree {
				continue
			}
			sells = append(sells, sellEntry{it.ID, it.SellPrice, qty})
		}
	}
	if buySum <= 0 || len(sells) == 0 {
		return out
	}
	var sellQty, sellPriceSum float64
	for _, s := range sells {
		sellQty += s.qty
		sellPriceSum += s.price
	}
	allocTotal := buySum
	if buyQty > 0 && sellQty < buyQty {
		allocTotal = buySum * sellQty / buyQty
	}
	for _, s := range sells {
		share := 0.0
		if sellPriceSum > 0 {
			share = s.price / sellPriceSum
		} else {
			share = s.qty / sellQty
		}
		out[s.id] = round2(allocTotal * share / s.qty)
	}
	return out
}

// effectiveUnitCost 取卖单实际用于核算的单件成本：自己填的优先，否则用联单分摊值。
func effectiveUnitCost(it Item, alloc map[int64]float64) float64 {
	if it.UnitCost > 0 {
		return it.UnitCost
	}
	if v, ok := alloc[it.ID]; ok {
		return v
	}
	return 0
}

func computeProductProfit(sid int64) []productProfitRow {
	// 所在联单里有买进单的明细：成本由联单买单核算，不能再算「未挂成本」。
	// 与明细页 type=unlinked 的 SQL 保持同一口径，两处数字才对得上。
	groupedWithBuy := loadGroupedWithBuy(sid)

	// 联单内卖单的成本分摊（按成交价占比），把整单买单的成本落到单品上
	alloc := loadGroupAlloc(sid)

	rows, err := db.Query("SELECT "+itemCols+" FROM items WHERE shop_id=?", sid)
	if err != nil {
		return []productProfitRow{}
	}
	defer rows.Close()

	agg := map[string]*productProfitRow{}
	var lots []holdingLot
	var saleDeductions []holdingDeduction
	get := func(name string) *productProfitRow {
		raw := normProduct(name)
		key := raw
		if key == "" {
			key = "未命名"
		}
		if v, ok := agg[key]; ok {
			return v
		}
		v := &productProfitRow{Product: strings.TrimSpace(name), Norm: raw}
		if v.Product == "" {
			v.Product = "未命名商品"
		}
		agg[key] = v
		return v
	}

	for rows.Next() {
		it, e := scanItem(rows)
		if e != nil || it.Kind == "discarded" {
			continue
		}
		// 寄卖 / 代采的货不是自己的，既不进商品利润，也不占自己的在库。
		if isExternalGoods(it.Source) {
			continue
		}
		switch it.Kind {
		case "purchase":
			// 先汇总有效采购，卖单循环结束后按实际带走成本冲减。
			gross := it.BuyPrice + it.BuyShipping
			if !isExternalGoods(it.Source) && it.SettledAt == "" && gross > 0 {
				lots = append(lots, holdingLot{
					ID: it.ID, Product: it.Product, Norm: itemProductNorm(it),
					Gross: round2(gross), BuyTime: it.BuyTime, AgeBand: holdingAgeBand(it.BuyTime),
				})
			}
		case "sale":
			p := get(it.Product)
			qty := it.Quantity
			if qty < 1 {
				qty = 1
			}
			feeAll := sellFeeTotal(it)
			goods := round2(effectiveUnitCost(it, alloc) * float64(qty))
			p.Sales += it.SellPrice
			p.Cost += goods + it.SellShipping + feeAll
			p.Sold++
			if it.CostFree {
				p.CostFree++
			}
			if !it.CostFree && !isExternalGoods(it.Source) {
				deduct := 0.0
				if it.UnitCost > 0 {
					deduct = round2(it.UnitCost * float64(qty))
				} else if v, ok := alloc[it.ID]; ok {
					deduct = round2(v * float64(qty))
				}
				if deduct > 0 {
					saleDeductions = append(saleDeductions, holdingDeduction{
						ParentID: it.ParentID, Norm: itemProductNorm(it), Amount: deduct, SellTime: it.SellTime,
					})
				}
			}
			// 只算「还能处理、也确实没有成本来源」的单：
			//   - 已单独完结 / 进了完结联单的，再挂会破坏联单核算；
			//   - 已经在联单里、且联单有买进单的，成本由联单核算，不该重复提示；
			//   - 自己填了单件成本的，本来就有成本。
			// 与明细页 type=unlinked 的 SQL 同一口径，排行/建议上的数字和点进去的条数才对得上。
			if !isExternalGoods(it.Source) && it.ParentID == 0 && it.SettledAt == "" && it.UnitCost <= 0 && !it.CostFree && !groupedWithBuy[it.ID] {
				p.Unlinked++
			}
		case "standalone":
			p := get(it.Product)
			feeAll := sellFeeTotal(it)
			p.Sales += it.SellPrice
			p.Cost += it.BuyPrice + it.BuyShipping + it.SellShipping + feeAll
			p.Sold++
		}
	}
	lots = applyHoldingDeductions(lots, saleDeductions)
	for _, lot := range lots {
		if lot.Remaining <= 0.005 {
			continue
		}
		p := get(lot.Product)
		p.Stock += lot.Remaining
		p.StockQty++
	}

	out := make([]productProfitRow, 0, len(agg))
	for _, v := range agg {
		v.Sales = round2(v.Sales)
		v.Cost = round2(v.Cost)
		v.Profit = round2(v.Sales - v.Cost)
		v.Stock = round2(v.Stock)
		if v.Sales > 0 {
			v.Margin = round2(v.Profit / v.Sales * 100)
		}
		out = append(out, *v)
	}
	// 赚得多的在前；同利润时按销售额大的先
	sort.Slice(out, func(i, j int) bool {
		if out[i].Profit == out[j].Profit {
			return out[i].Sales > out[j].Sales
		}
		return out[i].Profit > out[j].Profit
	})
	return out
}
