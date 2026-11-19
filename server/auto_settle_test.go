package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

// 卖单填了成本就算「自动结算」：进完结单核算、明细「已结算」分类能筛到，
// 但「卖单」分类里仍然看得见它，而且还能加进联单（加进去后完结状态交回联单）。
func TestAutoSettledSale(t *testing.T) {
	os.Setenv("DB_PATH", t.TempDir()+"/autosettle.db")
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

	ts := "2026-02-10 10:00:00"
	mustIns := func(it Item) int64 {
		id, err := insertItem(&it, defaultShopID, ts)
		if err != nil {
			t.Fatalf("insert %s: %v", it.Product, err)
		}
		return id
	}

	// ① 散卖单，自己填了单件成本 500 → 自动结算
	mustIns(Item{Product: "散卖机", SellPrice: 700, SellNet: 690, SellTime: ts, UnitCost: 500, Quantity: 1})
	// ② 散卖单，没填成本 → 未结算（未挂成本）
	mustIns(Item{Product: "未挂成本机", SellPrice: 300, SellNet: 297, SellTime: ts})
	// ③ 挂了买进单的卖单：成本来自父单
	pid := mustIns(Item{Product: "挂买单的机", BuyPrice: 800, BuyShipping: 20, BuyTime: ts})
	mustIns(Item{Product: "挂买单的机", ParentID: pid, SellPrice: 1000, SellNet: 985, SellTime: ts, UnitCost: 820})
	// ④ 标注「无成本」的卖单（赠送 / 代卖）→ 自动结算，成本 0
	mustIns(Item{Product: "赠送的机", SellPrice: 0, SellNet: 0, SellTime: ts, CostFree: true})
	// ⑤ 寄卖单：走寄卖页结算，不进这里的自动结算
	mustIns(Item{Product: "寄卖的机", Source: "寄卖", Owner: "张三", SellPrice: 900, SellNet: 890, SellTime: ts, CostFree: true})

	getStats := func() Stats {
		rr := httptest.NewRecorder()
		handleStats(rr, httptest.NewRequest("GET", "/api/stats", nil))
		var s Stats
		if err := json.Unmarshal(rr.Body.Bytes(), &s); err != nil {
			t.Fatalf("stats json: %v", err)
		}
		return s
	}
	s := getStats()
	// 完结成交额 = 690 + 985 = 1675（未挂成本的 297 不算，寄卖的 890 也不算）
	if s.SettledSales != 1675 {
		t.Errorf("完结成交额=%v want 1675", s.SettledSales)
	}
	// 完结成本 = 500（散卖手填） + 820（父买单买价+运费） = 1320
	if s.SettledCost != 1320 {
		t.Errorf("完结成本=%v want 1320", s.SettledCost)
	}
	if s.SettledProfit != 355 {
		t.Errorf("完结净利润=%v want 355", s.SettledProfit)
	}
	// 完结单数 = 散卖 + 挂买单的 = 2（赠送单没有成交价，不算一次成交）
	if s.SettledSold != 2 {
		t.Errorf("完结单数=%d want 2", s.SettledSold)
	}

	// 明细分类：已结算能筛到 3 条，卖单分类里仍然看得见自动结算的单
	listNames := func(query string) string {
		rr := httptest.NewRecorder()
		handleRecords(rr, httptest.NewRequest("GET", "/api/records?"+query, nil))
		var out struct {
			Items []Item `json:"items"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("records json: %v", err)
		}
		names := make([]string, 0, len(out.Items))
		for _, it := range out.Items {
			names = append(names, it.Product)
		}
		return strings.Join(names, ",")
	}
	countList := func(query string) int {
		rr := httptest.NewRecorder()
		handleRecords(rr, httptest.NewRequest("GET", "/api/records?"+query, nil))
		var out struct {
			Total int `json:"total"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("records json: %v", err)
		}
		return out.Total
	}
	if n := countList("type=settled"); n != 2 {
		t.Errorf("已结算分类条数=%d want 2", n)
	}
	// 自动结算不能让卖单从「卖单」分类里消失：这两条必须还能看到
	sellNames := listNames("type=sell")
	for _, want := range []string{"散卖机", "挂买单的机"} {
		if !strings.Contains(sellNames, want) {
			t.Errorf("卖单分类里找不到 %s: %v", want, sellNames)
		}
	}

	// ⑥ 已完结的记录也能加进联单：加进去后单品完结时间被清掉
	{
		// 造一条「单独完结」的记录，模拟旧数据
		doneID := mustIns(Item{Product: "已单独完结的机", BuyPrice: 100, BuyShipping: 0, BuyTime: ts})
		if _, err := db.Exec("UPDATE items SET settled_at=? WHERE id=?", ts, doneID); err != nil {
			t.Fatalf("settle: %v", err)
		}
		// 建联单：先把父买单放进去（联单至少要有一条记录）
		rr := httptest.NewRecorder()
		body := bytes.NewBufferString(`{"title":"测试联单","item_ids":[` + strconv.FormatInt(pid, 10) + `]}`)
		req := httptest.NewRequest("POST", "/api/groups", body)
		req.Header.Set("Content-Type", "application/json")
		handleGroups(rr, req)
		var g LinkGroup
		if err := json.Unmarshal(rr.Body.Bytes(), &g); err != nil || g.ID == 0 {
			t.Fatalf("建联单失败: %v body=%s", err, rr.Body.String())
		}
		// 把「已单独完结」的记录加进去
		rr2 := httptest.NewRecorder()
		body2 := bytes.NewBufferString(`{"item_ids":[` + strconv.FormatInt(doneID, 10) + `]}`)
		req2 := httptest.NewRequest("POST", "/api/groups/"+strconv.FormatInt(g.ID, 10)+"/items", body2)
		req2.Header.Set("Content-Type", "application/json")
		handleGroupItemsAdd(rr2, req2, g.ID)
		if rr2.Code != 200 {
			t.Fatalf("已完结记录加入联单被拒: code=%d body=%s", rr2.Code, rr2.Body.String())
		}
		var res struct {
			Added   int `json:"added"`
			Cleared int `json:"cleared"`
		}
		if err := json.Unmarshal(rr2.Body.Bytes(), &res); err != nil {
			t.Fatalf("add json: %v", err)
		}
		if res.Added != 1 || res.Cleared != 1 {
			t.Errorf("added=%d cleared=%d want 1/1", res.Added, res.Cleared)
		}
		var settled string
		db.QueryRow("SELECT COALESCE(settled_at,'') FROM items WHERE id=?", doneID).Scan(&settled)
		if settled != "" {
			t.Errorf("转入联单后单品完结时间没清掉: %q", settled)
		}
	}
}

// 「已结算」分类只列卖单：完结联单里的买进单不该混进来。
func TestSettledTabExcludesPurchases(t *testing.T) {
	os.Setenv("DB_PATH", t.TempDir()+"/settledtab.db")
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

	ts := "2026-04-01 10:00:00"
	pid, err := insertItem(&Item{Product: "成套机", BuyPrice: 900, BuyShipping: 10, BuyTime: ts}, defaultShopID, ts)
	if err != nil {
		t.Fatalf("insert buy: %v", err)
	}
	if _, err := insertItem(&Item{Product: "成套机", ParentID: pid, SellPrice: 1300, SellNet: 1280,
		SellTime: ts, UnitCost: 910}, defaultShopID, ts); err != nil {
		t.Fatalf("insert sell: %v", err)
	}

	res, err := db.Exec("INSERT INTO link_groups(shop_id,title,note,created_at,updated_at) VALUES(?,?,?,?,?)",
		defaultShopID, "成套机一批", "", ts, ts)
	if err != nil {
		t.Fatalf("group: %v", err)
	}
	gid, _ := res.LastInsertId()
	if err := setGroupItems(gid, []int64{pid}, defaultShopID); err != nil {
		t.Fatalf("setGroupItems: %v", err)
	}
	if _, err := db.Exec("UPDATE link_groups SET settled_at=? WHERE id=?", ts, gid); err != nil {
		t.Fatalf("settle: %v", err)
	}

	rr := httptest.NewRecorder()
	handleRecords(rr, httptest.NewRequest("GET", "/api/records?type=settled", nil))
	var out struct {
		Total int    `json:"total"`
		Items []Item `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("json: %v", err)
	}
	if out.Total != 1 {
		t.Fatalf("已结算分类条数=%d want 1（只该有卖单）", out.Total)
	}
	if !out.Items[0].HasSell {
		t.Errorf("已结算分类里混进了买进单: %+v", out.Items[0].Product)
	}
}
