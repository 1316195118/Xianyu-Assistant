package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
)

// 代采：我垫钱替别人买（买进单 + source=代采 + owner=委托人）。
// 这里验证三件事：① 只进「寄卖」页的代采账本，不进首页统计；
// ② 应付金额 = 买价 + 买入运费，且按人汇总；③ 收款后待收减少，且与寄卖结算互不串账。
func TestConsignDaicai(t *testing.T) {
	os.Setenv("DB_PATH", t.TempDir()+"/daicai.db")
	defer os.Unsetenv("DB_PATH")
	if err := initDB(); err != nil {
		t.Fatalf("initDB: %v", err)
	}
	// Windows 上临时目录要等连接关掉才能删
	defer func() {
		if db != nil {
			db.Close()
			db = nil
		}
	}()

	ts := "2026-01-05 10:00:00"
	mustIns := func(it Item) {
		if _, err := insertItem(&it, defaultShopID, ts); err != nil {
			t.Fatalf("insert %s: %v", it.Product, err)
		}
	}
	// 代采：替李四买两台，垫付 500+10 / 300+0
	mustIns(Item{Product: "机器A", Source: "代采", Owner: "李四", BuyPrice: 500, BuyShipping: 10, BuyTime: ts, BuyCounterparty: "卖家X"})
	mustIns(Item{Product: "机器B", Source: "代采", Owner: "李四", BuyPrice: 300, BuyTime: ts})
	// 寄卖：张三的货，卖 700 到手 690
	mustIns(Item{Product: "机器C", Source: "寄卖", Owner: "张三", SellPrice: 700, SellNet: 690, SellTime: ts})

	// ① 首页统计不含这两类外部货
	{
		rr := httptest.NewRecorder()
		handleStats(rr, httptest.NewRequest("GET", "/api/stats", nil))
		if rr.Code != 200 {
			t.Fatalf("stats code=%d", rr.Code)
		}
		var s Stats
		if err := json.Unmarshal(rr.Body.Bytes(), &s); err != nil {
			t.Fatalf("stats json: %v", err)
		}
		if s.TotalCount != 0 || s.TotalInvest != 0 || s.TotalSales != 0 || s.TotalGoods != 0 {
			t.Errorf("首页不该统计外部货: count=%d invest=%v sales=%v goods=%v",
				s.TotalCount, s.TotalInvest, s.TotalSales, s.TotalGoods)
		}
	}

	// ② 代采 / 寄卖 两本账
	type statResp struct {
		Owners []struct {
			Owner   string  `json:"owner"`
			Orders  int     `json:"orders"`
			Sales   float64 `json:"sales"`
			Pending float64 `json:"pending"`
		} `json:"owners"`
		Buyers []struct {
			Owner   string  `json:"owner"`
			Orders  int     `json:"orders"`
			Amount  float64 `json:"amount"`
			Pending float64 `json:"pending"`
			Items   []struct {
				Product string `json:"product"`
			} `json:"items"`
		} `json:"buyers"`
		Total struct {
			BuyOrders  int     `json:"buy_orders"`
			BuyAmount  float64 `json:"buy_amount"`
			BuyPending float64 `json:"buy_pending"`
			Pending    float64 `json:"pending"`
		} `json:"total"`
	}
	getStats := func() statResp {
		rr := httptest.NewRecorder()
		handleConsignStats(rr, httptest.NewRequest("GET", "/api/consign/stats", nil))
		var out statResp
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("consign stats json: %v", err)
		}
		return out
	}
	st := getStats()
	if len(st.Buyers) != 1 || st.Buyers[0].Owner != "李四" {
		t.Fatalf("代采归属人不对: %+v", st.Buyers)
	}
	b := st.Buyers[0]
	if b.Orders != 2 || b.Amount != 810 || b.Pending != 810 {
		t.Errorf("李四 orders=%d amount=%v pending=%v want 2/810/810", b.Orders, b.Amount, b.Pending)
	}
	if len(b.Items) != 2 {
		t.Errorf("代采明细条数=%d want 2", len(b.Items))
	}
	if st.Total.BuyOrders != 2 || st.Total.BuyAmount != 810 || st.Total.BuyPending != 810 {
		t.Errorf("代采汇总不对: %+v", st.Total)
	}
	if len(st.Owners) != 1 || st.Owners[0].Owner != "张三" || st.Owners[0].Pending != 690 {
		t.Errorf("寄卖账本被代采污染: %+v", st.Owners)
	}

	// ③ 收款 500（kind=buy）：待收 810 → 310
	{
		body := bytes.NewBufferString(`{"owner":"李四","kind":"buy","amount":500}`)
		req := httptest.NewRequest("POST", "/api/consign/settle", body)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		handleConsignSettle(rr, req)
		if rr.Code != 200 {
			t.Fatalf("settle code=%d body=%s", rr.Code, rr.Body.String())
		}
	}
	st = getStats()
	if st.Buyers[0].Pending != 310 {
		t.Errorf("收款后待收=%v want 310", st.Buyers[0].Pending)
	}
	// 寄卖那本账不能被这次收款影响
	if st.Owners[0].Pending != 690 {
		t.Errorf("寄卖待结被代采收款改动: %v want 690", st.Owners[0].Pending)
	}

	// 结算记录按 kind 分流
	{
		rr := httptest.NewRecorder()
		handleConsignSettlements(rr, httptest.NewRequest("GET", "/api/consign/settlements?owner=%E6%9D%8E%E5%9B%9B&kind=buy", nil))
		var out struct {
			Items []struct {
				Amount float64 `json:"amount"`
				Kind   string  `json:"kind"`
			} `json:"items"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("settlements json: %v", err)
		}
		if len(out.Items) != 1 || out.Items[0].Kind != "buy" || out.Items[0].Amount != 500 {
			t.Errorf("代采收款记录不对: %+v", out.Items)
		}

		rr2 := httptest.NewRecorder()
		handleConsignSettlements(rr2, httptest.NewRequest("GET", "/api/consign/settlements?owner=%E6%9D%8E%E5%9B%9B&kind=sell", nil))
		var out2 struct {
			Items []struct{} `json:"items"`
		}
		if err := json.Unmarshal(rr2.Body.Bytes(), &out2); err != nil {
			t.Fatalf("settlements2 json: %v", err)
		}
		if len(out2.Items) != 0 {
			t.Errorf("寄卖结算记录里不该出现代采收款: %+v", out2.Items)
		}
	}

	// 归属候选：寄卖人与委托人都要能选到
	{
		rr := httptest.NewRecorder()
		handleConsignOwners(rr, httptest.NewRequest("GET", "/api/consign/owners", nil))
		var out struct {
			Owners []string `json:"owners"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("owners json: %v", err)
		}
		seen := map[string]bool{}
		for _, n := range out.Owners {
			seen[n] = true
		}
		if !seen["李四"] || !seen["张三"] {
			t.Errorf("归属候选缺人: %v", out.Owners)
		}
	}
}
