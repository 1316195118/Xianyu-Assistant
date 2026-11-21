package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
)

// 寄卖 / 代采是外部账本：不从订单页显示待售，也不进入自有成本、利润、
// 在库和「未挂成本」口径；归属人的真实待结 / 待收金额仍由寄卖页保留。
func TestExternalOrdersUseExternalSettlementState(t *testing.T) {
	os.Setenv("DB_PATH", t.TempDir()+"/external-settlement.db")
	defer os.Unsetenv("DB_PATH")
	if err := initDB(); err != nil {
		t.Fatalf("initDB: %v", err)
	}
	defer func() {
		if db != nil {
			db.Close()
			db = nil
		}
	}()

	if got := normSource("代买"); got != "代采" {
		t.Fatalf("代买别名=%q want 代采", got)
	}
	if got := normSource("寄售"); got != "寄卖" {
		t.Fatalf("寄售别名=%q want 寄卖", got)
	}

	ts := "2026-05-10 10:00:00"
	mustIns := func(it Item) int64 {
		id, err := insertItem(&it, defaultShopID, ts)
		if err != nil {
			t.Fatalf("insert %s: %v", it.Product, err)
		}
		return id
	}

	ownID := mustIns(Item{Product: "自营待售机", Source: "自采", BuyPrice: 100, BuyShipping: 10, BuyTime: ts})
	daicaiID := mustIns(Item{Product: "代买垫付机", Source: "代买", Owner: "李四", BuyPrice: 350, BuyShipping: 30, BuyTime: ts})
	jishouID := mustIns(Item{Product: "寄售成交机", Source: "寄售", Owner: "张三", SellPrice: 1150, SellFee: 18.4, SellNet: 1131.6, SellTime: ts})

	own, err := getItem(ownID, defaultShopID)
	if err != nil {
		t.Fatalf("get own: %v", err)
	}
	if own.Status != "pending" || own.Cost != 110 {
		t.Errorf("自营买单状态/成本=%s/%v want pending/110", own.Status, own.Cost)
	}

	daicai, err := getItem(daicaiID, defaultShopID)
	if err != nil {
		t.Fatalf("get daicai: %v", err)
	}
	if daicai.Source != "代采" || daicai.Status != "external" || daicai.Cost != 0 || daicai.Profit != 0 {
		t.Errorf("代采派生状态不对: source=%s status=%s cost=%v profit=%v",
			daicai.Source, daicai.Status, daicai.Cost, daicai.Profit)
	}

	jishou, err := getItem(jishouID, defaultShopID)
	if err != nil {
		t.Fatalf("get jishou: %v", err)
	}
	if jishou.Source != "寄卖" || jishou.Status != "external" || jishou.Cost != 0 || jishou.Profit != 0 {
		t.Errorf("寄售派生状态不对: source=%s status=%s cost=%v profit=%v",
			jishou.Source, jishou.Status, jishou.Cost, jishou.Profit)
	}

	// 待售筛选只返回自己的货。
	{
		rr := httptest.NewRecorder()
		handleRecords(rr, httptest.NewRequest("GET", "/api/records?status=pending", nil))
		var out struct {
			Total int    `json:"total"`
			Items []Item `json:"items"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("pending json: %v", err)
		}
		if out.Total != 1 || len(out.Items) != 1 || out.Items[0].ID != ownID {
			t.Errorf("待售筛选混入外部单: total=%d items=%+v", out.Total, out.Items)
		}
	}

	// 外部寄售也不应被提醒「未挂成本」。
	{
		rr := httptest.NewRecorder()
		handleRecords(rr, httptest.NewRequest("GET", "/api/records?type=unlinked", nil))
		var out struct {
			Total int `json:"total"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("unlinked json: %v", err)
		}
		if out.Total != 0 {
			t.Errorf("外部单进入未挂成本: total=%d", out.Total)
		}
	}

	// 首页只统计自营待售批次，外部垫付与寄售成交都不进成本。
	{
		rr := httptest.NewRecorder()
		handleStats(rr, httptest.NewRequest("GET", "/api/stats", nil))
		var s Stats
		if err := json.Unmarshal(rr.Body.Bytes(), &s); err != nil {
			t.Fatalf("stats json: %v", err)
		}
		if s.TotalCount != 1 || s.PendingCount != 1 || s.TotalInvest != 110 ||
			s.HoldingCost != 110 || s.TotalCost != 0 {
			t.Errorf("外部单污染首页统计: count=%d pending=%d invest=%v holding=%v cost=%v",
				s.TotalCount, s.PendingCount, s.TotalInvest, s.HoldingCost, s.TotalCost)
		}
	}

	// 订单页是「外部结算」，但寄卖页真实的待收 / 待结不能被清零。
	{
		rr := httptest.NewRecorder()
		handleConsignStats(rr, httptest.NewRequest("GET", "/api/consign/stats", nil))
		var out struct {
			Total struct {
				BuyAmount  float64 `json:"buy_amount"`
				BuyPending float64 `json:"buy_pending"`
				Pending    float64 `json:"pending"`
			} `json:"total"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("consign json: %v", err)
		}
		if out.Total.BuyAmount != 380 || out.Total.BuyPending != 380 || out.Total.Pending != 1131.6 {
			t.Errorf("寄卖账本余额被外部状态影响: %+v", out.Total)
		}
	}
}
