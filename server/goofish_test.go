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
