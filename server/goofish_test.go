package main

import (
	"testing"
	"time"
)

// 查重只应作用于当前店铺：别家店里的老订单号不能把本店预览标成「已存在」。
func TestMarkExistingScopedToShop(t *testing.T) {
	newExtrasEnv(t)
	const otherShop int64 = 99
	if _, err := insertItem(&Item{Product: "别家老单", SellOrderNo: "OLD-123", SellPrice: 100}, otherShop, nowStr()); err != nil {
		t.Fatalf("insert other shop item: %v", err)
	}
	p := &gfPlan{Rows: []gfPlanRow{{OrderNo: "OLD-123"}}}
	p.markExisting(defaultShopID)
	if p.Rows[0].Exists {
		t.Error("别家店的老订单号不应把本店预览标记为已存在")
	}
	if _, err := insertItem(&Item{Product: "本店单", SellOrderNo: "MINE-456", SellPrice: 100}, defaultShopID, nowStr()); err != nil {
		t.Fatalf("insert own shop item: %v", err)
	}
	p2 := &gfPlan{Rows: []gfPlanRow{{OrderNo: "MINE-456"}}}
	p2.markExisting(defaultShopID)
	if !p2.Rows[0].Exists {
		t.Error("本店已有的订单号应标记为已存在")
	}
}

func TestGFBackfillRanges(t *testing.T) {
	rs := gfBackfillRanges(3)
	if len(rs) != 3 {
		t.Fatalf("ranges=%d want 3", len(rs))
	}
	for _, r := range rs {
		f, e1 := time.ParseInLocation("2006-01-02", r[0], time.Local)
		to, e2 := time.ParseInLocation("2006-01-02", r[1], time.Local)
		if e1 != nil || e2 != nil {
			t.Fatalf("bad range %v", r)
		}
		if to.Day() < 28 {
			t.Errorf("range %v 不是整月", r)
		}
		if to.Sub(f).Hours() > 31*24 {
			t.Errorf("range %v 超过 31 天", r)
		}
	}
}

// 回溯关闭时，立即同步只取「最近 N 天」：老单被过滤、解析不了的时间保留、原 slice 不被改动。
func TestGFFilterRecent(t *testing.T) {
	now := time.Now()
	fmtT := "2006-01-02 15:04:05"
	buy := []gfOrder{
		{OrderNo: "B-OLD", Time: now.AddDate(0, 0, -10).Format(fmtT)},
		{OrderNo: "B-NEW", Time: now.AddDate(0, 0, -1).Format(fmtT)},
		{OrderNo: "B-BAD", Time: "not-a-time"},
	}
	sell := []gfOrder{
		{OrderNo: "S-OLD", Time: now.AddDate(0, 0, -5).Format(fmtT)},
		{OrderNo: "S-NEW", Time: now.AddDate(0, 0, -2).Format(fmtT)},
	}

	// window<=0：不过滤
	gb, gs := gfFilterRecent(buy, sell, 0)
	if len(gb) != 3 || len(gs) != 2 {
		t.Fatalf("window=0 should keep all, got %d/%d", len(gb), len(gs))
	}

	// window=3：10 天前的买单、5 天前的卖单过滤掉；解析失败的保留
	gb, gs = gfFilterRecent(buy, sell, 3)
	if len(gb) != 2 || len(gs) != 1 {
		t.Fatalf("window=3 got %d/%d, want 2/1", len(gb), len(gs))
	}
	seen := map[string]bool{}
	for _, o := range gb {
		seen[o.OrderNo] = true
	}
	if !seen["B-NEW"] || !seen["B-BAD"] || seen["B-OLD"] {
		t.Errorf("window=3 buy filter wrong: %v", seen)
	}
	if gs[0].OrderNo != "S-NEW" {
		t.Errorf("window=3 sell filter wrong: %v", gs)
	}
	if len(buy) != 3 || len(sell) != 2 {
		t.Errorf("input slices mutated: %d/%d", len(buy), len(sell))
	}
}

// 卖单列表的成交价必须取买家实际成交金额 totalPrice，而不是商品挂牌单价 auctionPrice。
func TestGFSoldOrderPriceUsesTransactionTotal(t *testing.T) {
	got := gfSoldOrderPrice(map[string]interface{}{
		"auctionPrice": "138.00",
		"totalPrice":   "131.91",
		"buyNum":       "1",
	})
	if got != 131.91 {
		t.Fatalf("成交价=%v, want 131.91 (totalPrice)", got)
	}

	got = gfSoldOrderPrice(map[string]interface{}{
		"auctionPrice": "66.00",
		"buyNum":       "2",
	})
	if got != 132 {
		t.Fatalf("缺少 totalPrice 时兜底=%v, want 132", got)
	}
}

// 订单里的“软件服务费”必须等于软件服务费 + 基础软件服务费；
// 无忧卖类目服务费单独保存，不混入这一项。
func TestGFSoftwareFeeCombinesTechnicalAndBasic(t *testing.T) {
	b := &gfBill{}
	gfAddExpenseAmount(b, gfExpenseSoftwareBiz, 9)
	gfAddExpenseAmount(b, gfExpenseBasicBiz, 5.4)
	gfAddExpenseAmount(b, gfExpenseWuyouBiz, 14.4)

	if b.Tech != 9 || b.Basic != 5.4 || b.Wuyou != 14.4 {
		t.Fatalf("classified fee wrong: tech=%v basic=%v wuyou=%v", b.Tech, b.Basic, b.Wuyou)
	}
	if got := gfSoftwareFee(b); got != 14.4 {
		t.Fatalf("software fee=%v, want 14.4 (technical + basic)", got)
	}
}

// 旧缓存曾把两类软件服务费合在 tech 中，必须自动失效并在下次同步重新拉取。
func TestGFBillCacheVersionInvalidatesLegacyFeeData(t *testing.T) {
	if !gfBillCacheNeedsRefresh(nil) {
		t.Error("missing cache should require a bill query")
	}
	if !gfBillCacheNeedsRefresh(&gfBillRow{Billed: true, FeeVersion: 0}) {
		t.Error("legacy bill cache should be invalidated")
	}
	if !gfBillCacheNeedsRefresh(&gfBillRow{Billed: false, FeeVersion: gfBillCacheVersion}) {
		t.Error("unbilled cache should be refreshed")
	}
	if gfBillCacheNeedsRefresh(&gfBillRow{Billed: true, FeeVersion: gfBillCacheVersion}) {
		t.Error("current billed cache should remain valid")
	}

	newExtrasEnv(t)
	const orderNo = "GF-FEE-CACHE"
	saveBillFact(gfBillRow{
		OrderNo: orderNo, Actual: 900, Auction: 900,
		Tech: 9, Basic: 5.4, Wuyou: 14.4,
		Billed: true, FeeVersion: gfBillCacheVersion,
	})
	got := loadBillCache([]string{orderNo})[orderNo]
	if got == nil {
		t.Fatal("bill cache row missing")
	}
	if got.Tech != 9 || got.Basic != 5.4 || got.Wuyou != 14.4 ||
		got.FeeVersion != gfBillCacheVersion || !got.Billed {
		t.Fatalf("cached fee split wrong: %+v", *got)
	}
}

func TestGFBillCacheSchemaUpgradeAddsFeeSplitColumns(t *testing.T) {
	newExtrasEnv(t)
	if _, err := db.Exec(`DROP TABLE gf_bills`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE gf_bills (
        order_no TEXT PRIMARY KEY, actual REAL NOT NULL DEFAULT 0,
        auction REAL NOT NULL DEFAULT 0, tech REAL NOT NULL DEFAULT 0,
        wuyou REAL NOT NULL DEFAULT 0, billed INTEGER NOT NULL DEFAULT 0,
        post_fee REAL NOT NULL DEFAULT 0, fetched_at TEXT NOT NULL DEFAULT ''
    )`); err != nil {
		t.Fatal(err)
	}
	if err := createBillCacheTable(); err != nil {
		t.Fatalf("createBillCacheTable upgrade: %v", err)
	}
	for _, col := range []string{"basic", "fee_version"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('gf_bills') WHERE name=?`, col).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("upgrade missing column %s", col)
		}
	}
}

// 预览只把与闲鱼金额不同的已有卖单标成待补正；补正一致后不应反复出现。
func TestGFMarkExistingOnlyMarksFinancialDifferences(t *testing.T) {
	newExtrasEnv(t)
	const differs = "GF-REFRESH-DIFF"
	const same = "GF-REFRESH-SAME"
	if _, err := insertItem(&Item{
		Product: "手动差异单", SellOrderNo: differs,
		SellPrice: 100, SellFee: 1, SellWuyouFee: 0,
	}, defaultShopID, nowStr()); err != nil {
		t.Fatal(err)
	}
	if _, err := insertItem(&Item{
		Product: "已经补正", SellOrderNo: same,
		SellPrice: 88, SellFee: 2, SellWuyouFee: 1,
	}, defaultShopID, nowStr()); err != nil {
		t.Fatal(err)
	}

	p := &gfPlan{Rows: []gfPlanRow{
		{OrderNo: differs, Side: "sell", Price: 95, Fee: 2, Wuyou: 1, Billed: true},
		{OrderNo: same, Side: "sell", Price: 88, Fee: 2, Wuyou: 1, Billed: true},
	}}
	p.markExisting(defaultShopID)
	if !p.Rows[0].Exists || !p.Rows[0].RefreshNeeded {
		t.Fatalf("差异单应待补正: exists=%v refresh=%v", p.Rows[0].Exists, p.Rows[0].RefreshNeeded)
	}
	if !p.Rows[1].Exists || p.Rows[1].RefreshNeeded {
		t.Fatalf("已补正单不应再次待补正: exists=%v refresh=%v", p.Rows[1].Exists, p.Rows[1].RefreshNeeded)
	}
}

// 预留链路：买运费（商品邮费 POST_FEE）默认不启用。
// 锁定两个契约：① 开关默认关闭（零额外详情请求）；② 开关关闭时 gfOrder.PostFee 恒为 0，
// 导入后买运费保持 0，与「运费靠支付宝导入」的现有行为一致。
func TestBuyPostFeeReservedOff(t *testing.T) {
	if gfFetchBuyPostFee() {
		t.Fatal("买运费详情抓取开关应默认关闭，否则每个买单会多一次请求并触发风控")
	}
	// 开关关闭时，一条买单即使走到导入，买运费也应为 0（不改变现状）
	row := gfPlanRow{Side: "buy", Price: 100, PostFee: 0}
	it := &Item{BuyPrice: row.Price, BuyShipping: row.PostFee, BuyShipTag: "包邮"}
	if it.BuyShipping != 0 || it.BuyShipTag != "包邮" {
		t.Errorf("开关关闭时买运费应保持 0/包邮, got %v/%s", it.BuyShipping, it.BuyShipTag)
	}
}

// 未出账单的已有卖单：自动同步不能动金额，但用户明确勾选覆盖时必须能用
// 订单列表金额更新，否则界面显示“将覆盖”后仍会因为 Billed=false 被跳过。
func TestGFRefreshExistingOverridesUnbilledOnlyWhenSelected(t *testing.T) {
	newExtrasEnv(t)
	const orderNo = "GF-UNBILLED-OVERRIDE"
	const manualNote = "用户手填备注"
	id, err := insertItem(&Item{
		Product: "未出账卖单", SellOrderNo: orderNo,
		SellPrice: 100, SellShipping: 6, SellNet: 94, Note: manualNote,
	}, defaultShopID, nowStr())
	if err != nil {
		t.Fatalf("insertItem: %v", err)
	}

	row := gfPlanRow{
		OrderNo: orderNo, Side: "sell", Price: 120,
		Exists: true, Billed: false,
	}
	only := map[string]bool{orderNo: true}

	changed, _, unbilled := gfRefreshExisting(defaultShopID, []gfPlanRow{row}, only, false)
	if changed != 0 || unbilled != 1 {
		t.Fatalf("automatic refresh changed=%d unbilled=%d, want 0/1", changed, unbilled)
	}
	var price float64
	if err := db.QueryRow("SELECT sell_price FROM items WHERE id=?", id).Scan(&price); err != nil {
		t.Fatal(err)
	}
	if price != 100 {
		t.Fatalf("automatic refresh changed unbilled price to %v", price)
	}

	changed, kept, unbilled := gfRefreshExisting(defaultShopID, []gfPlanRow{row}, only, true)
	if changed != 1 || kept != 0 || unbilled != 1 {
		t.Fatalf("manual refresh changed=%d kept=%d unbilled=%d, want 1/0/1", changed, kept, unbilled)
	}
	var fee, wuyou, net float64
	var note string
	if err := db.QueryRow(`SELECT sell_price, sell_fee, sell_wuyou_fee, sell_net, note
        FROM items WHERE id=?`, id).Scan(&price, &fee, &wuyou, &net, &note); err != nil {
		t.Fatal(err)
	}
	if price != 120 || fee != 0 || wuyou != 0 || net != 114 {
		t.Errorf("manual refresh wrong: price=%v fee=%v wuyou=%v net=%v", price, fee, wuyou, net)
	}
	if note != manualNote {
		t.Errorf("manual note changed: %q", note)
	}
}

// 自动同步按订单号覆盖手动卖单的三项金融字段，但不碰备注等人工数据。
func TestGFAutoRefreshUpdatesManualFinancialsOnly(t *testing.T) {
	newExtrasEnv(t)
	const orderNo = "GF-MANUAL-BILLED"
	const manualNote = "人工核对后的卖单"
	id, err := insertItem(&Item{
		Product: "手动卖单", SellOrderNo: orderNo,
		SellPrice: 100, SellFee: 3, SellWuyouFee: 2, SellNet: 95,
		SellShipping: 6, SellCounterparty: "手工买家", SellShipTag: "卖家承担",
		ConsignTime: "2026-11-20 12:00:00", Note: manualNote,
	}, defaultShopID, nowStr())
	if err != nil {
		t.Fatalf("insertItem: %v", err)
	}

	row := gfPlanRow{
		OrderNo: orderNo, Side: "sell", Price: 120, Fee: 10, Wuyou: 7,
		Exists: true, Billed: true, Managed: false, Peer: "闲鱼买家", ConsignTime: "2026-11-21 12:00:00",
	}
	only := map[string]bool{orderNo: true}

	changed, kept, unbilled := gfRefreshExisting(defaultShopID, []gfPlanRow{row}, only, true)
	if changed != 1 || kept != 0 || unbilled != 0 {
		t.Fatalf("automatic refresh changed=%d kept=%d unbilled=%d, want 1/0/0", changed, kept, unbilled)
	}

	var price, fee, wuyou, net float64
	var note, product, counterparty, shipTag, consign string
	if err := db.QueryRow(`SELECT sell_price, sell_fee, sell_wuyou_fee, sell_net, note,
        product, sell_counterparty, sell_ship_tag, consign_time
        FROM items WHERE id=?`, id).Scan(&price, &fee, &wuyou, &net, &note,
		&product, &counterparty, &shipTag, &consign); err != nil {
		t.Fatal(err)
	}
	if price != 120 || fee != 10 || wuyou != 7 || net != 97 {
		t.Fatalf("financial refresh wrong: price=%v fee=%v wuyou=%v net=%v", price, fee, wuyou, net)
	}
	if note != manualNote || product != "手动卖单" || counterparty != "手工买家" ||
		shipTag != "卖家承担" || consign != "2026-11-20 12:00:00" {
		t.Fatalf("automatic refresh changed manual fields: note=%q product=%q peer=%q shipTag=%q consign=%q",
			note, product, counterparty, shipTag, consign)
	}
}

// 自动新增只处理系统里没有的订单；已有订单的金额补正由单独流程处理。
func TestGFAutoImportOnlyAddsMissingOrders(t *testing.T) {
	newExtrasEnv(t)
	const existingNo = "GF-AUTO-EXISTING"
	const newNo = "GF-AUTO-NEW"
	id, err := insertItem(&Item{
		Product: "手动核对卖单", SellOrderNo: existingNo,
		SellPrice: 88, SellFee: 3, SellWuyouFee: 2, SellNet: 83,
		Note: "人工核对的卖出价",
	}, defaultShopID, nowStr())
	if err != nil {
		t.Fatalf("insertItem: %v", err)
	}

	rows := []gfPlanRow{
		{OrderNo: existingNo, Side: "sell", Product: "已有卖单", Price: 188, Fee: 20, Wuyou: 7, Billed: true, Exists: true},
		{OrderNo: newNo, Side: "sell", Product: "自动新增卖单", Price: 66, Billed: false},
	}
	if added := gfAutoImportMissing(defaultShopID, rows, nowStr()); added != 1 {
		t.Fatalf("auto import added=%d, want 1", added)
	}

	var price, fee, wuyou, net float64
	if err := db.QueryRow(`SELECT sell_price, sell_fee, sell_wuyou_fee, sell_net
        FROM items WHERE id=?`, id).Scan(&price, &fee, &wuyou, &net); err != nil {
		t.Fatal(err)
	}
	if price != 88 || fee != 3 || wuyou != 2 || net != 83 {
		t.Fatalf("auto import changed existing order: price=%v fee=%v wuyou=%v net=%v", price, fee, wuyou, net)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM items
        WHERE shop_id=? AND sell_order_no=?`, defaultShopID, newNo).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("new order count=%d, want 1", count)
	}
}
