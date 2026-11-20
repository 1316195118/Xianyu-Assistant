package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
)

// 联单统计口径：一个卖单挂多个买单 / 一个买单挂多个卖单都能正确汇总。
func TestComputeGroupStats(t *testing.T) {
	// 场景：2 个买单（同批货分两次进）+ 2 个卖单（分两次卖）
	items := []Item{
		// 买进单 1：1000 货 + 10 运费
		{Product: "货A", Kind: "purchase", BuyPrice: 1000, BuyShipping: 10},
		// 买进单 2：500 货 + 5 运费
		{Product: "货A", Kind: "purchase", BuyPrice: 500, BuyShipping: 5},
		// 卖出单 1：成交 900，软件服务费 9，无忧卖 7，到手 884，卖运费 8
		{Product: "货A", Kind: "sale", SellPrice: 900, SellFee: 9, SellWuyouFee: 7, SellNet: 884, SellShipping: 8},
		// 卖出单 2：成交 800，软件服务费 8，无忧卖 0，到手 792，卖运费 0
		{Product: "货A", Kind: "sale", SellPrice: 800, SellFee: 8, SellWuyouFee: 0, SellNet: 792, SellShipping: 0},
	}
	s := computeGroupStats(items, nil)
	if s.BuyCount != 2 || s.SellCount != 2 {
		t.Errorf("buy=%d sell=%d want 2/2", s.BuyCount, s.SellCount)
	}
	// 销售额 = 900 + 800 = 1700
	if s.Sales != 1700 {
		t.Errorf("sales=%v want 1700", s.Sales)
	}
	// 运费 = (10+5) + (8+0) = 23
	if s.Shipping != 23 {
		t.Errorf("shipping=%v want 23", s.Shipping)
	}
	// 服务费 = 9+7+8 = 24，其中无忧卖 7
	if s.Fee != 24 || s.WuyouFee != 7 {
		t.Errorf("fee=%v wuyou=%v want 24/7", s.Fee, s.WuyouFee)
	}
	// 货品成本 = 1000 + 500 = 1500
	if s.Goods != 1500 {
		t.Errorf("goods=%v want 1500", s.Goods)
	}
	// 净利润 = 1700 − 1500 − 23 − 24 = 153
	if s.Profit != 153 {
		t.Errorf("profit=%v want 153", s.Profit)
	}
	// 口径自洽：Σ到手 − 完全成本 = 1676 − 1523 = 153
	if s.Net != 1676 {
		t.Errorf("net=%v want 1676", s.Net)
	}
	t.Logf("sales=%v net=%v goods=%v shipping=%v fee=%v wuyou=%v profit=%v rate=%v%%",
		s.Sales, s.Net, s.Goods, s.Shipping, s.Fee, s.WuyouFee, s.Profit, s.ProfitRate)
}

// 联单 CRUD 与「记录可属于多个联单」的 N:M 语义
func TestLinkGroupCRUD(t *testing.T) {
	os.Setenv("DB_PATH", t.TempDir()+"/g.db")
	defer os.Unsetenv("DB_PATH")
	if err := initDB(); err != nil {
		t.Fatalf("initDB: %v", err)
	}
	defer db.Close()

	ids := []int64{}
	for i, p := range []string{"商品甲", "商品乙"} {
		it := &Item{Product: p, BuyPrice: float64(100 + i*50), Quantity: 1}
		id, err := insertItem(it, defaultShopID, nowStr())
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		ids = append(ids, id)
	}
	for _, group := range [][]int64{{ids[0], ids[1]}, {ids[0]}} {
		res, err := db.Exec("INSERT INTO link_groups(title,note,created_at,updated_at) VALUES(?,?,?,?)",
			"测试联单", "", nowStr(), nowStr())
		if err != nil {
			t.Fatalf("insert group: %v", err)
		}
		gid, _ := res.LastInsertId()
		if err := setGroupItems(gid, group, defaultShopID); err != nil {
			t.Fatalf("setGroupItems: %v", err)
		}
	}
	// ids[0] 同时属于 2 个联单 → N:M 成立
	var n int
	db.QueryRow("SELECT COUNT(*) FROM group_items WHERE item_id=?", ids[0]).Scan(&n)
	if n != 2 {
		t.Errorf("item belongs to %d groups, want 2 (N:M)", n)
	}
	// 详情能取回记录与统计
	rows, _ := db.Query("SELECT id FROM link_groups ORDER BY id")
	var gids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		gids = append(gids, id)
	}
	rows.Close()
	items, gotIDs, err := loadGroupItems(gids[0], defaultShopID)
	if err != nil || len(items) != 2 || len(gotIDs) != 2 {
		t.Errorf("loadGroupItems=%d items err=%v want 2", len(items), err)
	}
	s := computeGroupStats(items, nil)
	if s.BuyCount != 2 || s.Goods != 250 {
		t.Errorf("stats buy=%d goods=%v want 2/250", s.BuyCount, s.Goods)
	}
}

// 完结联单后，其买单应从全局「待售在库占用资金」中释放；取消完结应恢复。
func TestSettleReleasesHolding(t *testing.T) {
	os.Setenv("DB_PATH", t.TempDir()+"/settle.db")
	defer os.Unsetenv("DB_PATH")
	if err := initDB(); err != nil {
		t.Fatalf("initDB: %v", err)
	}
	defer db.Close()

	// 一个买进单：货 1000 + 运费 20 = 1020 占用资金
	pid, _ := insertItem(&Item{Product: "完结测试货", BuyPrice: 1000, BuyShipping: 20, Quantity: 1}, defaultShopID, nowStr())
	// 一个卖出单（挂在上面买进单下）
	sid, _ := insertItem(&Item{Product: "完结测试货", ParentID: pid, UnitCost: 1000, SellPrice: 1200, SellNet: 1180}, defaultShopID, nowStr())

	// 建一个含该买单的联单
	res, _ := db.Exec("INSERT INTO link_groups(title,created_at,updated_at) VALUES(?,?,?)", "完结测试联单", nowStr(), nowStr())
	gid, _ := res.LastInsertId()
	if err := setGroupItems(gid, []int64{pid, sid}, defaultShopID); err != nil {
		t.Fatalf("setGroupItems: %v", err)
	}

	// 新口径：进了联单的买单不再占在库，不需要完结
	before := callStats(t)
	if before.HoldingCost > 0.01 {
		t.Errorf("进联单后 holding=%v want 0", before.HoldingCost)
	}

	// 完结联单
	if _, err := db.Exec("UPDATE link_groups SET settled_at=? WHERE id=?", nowStr(), gid); err != nil {
		t.Fatalf("settle: %v", err)
	}
	after := callStats(t)
	if after.ReleasedCost != 0 {
		t.Errorf("released=%v want 0（不再有完结释放）", after.ReleasedCost)
	}
	if after.HoldingCost > 0.01 {
		t.Errorf("完结后 holding=%v want 0", after.HoldingCost)
	}

	// 取消完结：恢复占用资金
	if _, err := db.Exec("UPDATE link_groups SET settled_at='' WHERE id=?", gid); err != nil {
		t.Fatalf("unsettle: %v", err)
	}
	restore := callStats(t)
	if restore.HoldingCost > 0.01 {
		t.Errorf("重新开启联单后 holding=%v want 0（仍在联单里）", restore.HoldingCost)
	}
	if restore.ReleasedCost != 0 {
		t.Errorf("恢复后 released=%v want 0", restore.ReleasedCost)
	}
}

func callStats(t *testing.T) Stats {
	req := httptest.NewRequest("GET", "/api/stats", nil)
	w := httptest.NewRecorder()
	handleStats(w, req)
	var s Stats
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatalf("unmarshal stats: %v", err)
	}
	return s
}

func TestStandaloneShippingCountedOnce(t *testing.T) {
	os.Setenv("DB_PATH", t.TempDir()+"/shipping.db")
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

	_, err := insertItem(&Item{
		Product: "两侧一起填", BuyPrice: 100, BuyShipping: 10,
		SellPrice: 150, SellShipping: 5, SellNet: 150,
	}, defaultShopID, nowStr())
	if err != nil {
		t.Fatalf("insertItem: %v", err)
	}
	s := callStats(t)
	if s.TotalShipping != 15 {
		t.Errorf("total_shipping=%v want 15（买运费 10 + 卖运费 5 各一次）", s.TotalShipping)
	}
	if s.TotalCost != 115 {
		t.Errorf("total_cost=%v want 115", s.TotalCost)
	}
	if s.TotalProfit != 35 {
		t.Errorf("total_profit=%v want 35", s.TotalProfit)
	}
}

func TestMergeGroupsRollbackKeepsAllWrites(t *testing.T) {
	os.Setenv("DB_PATH", t.TempDir()+"/merge-rollback.db")
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

	ts := nowStr()
	mkItem := func(product string) int64 {
		id, err := insertItem(&Item{Product: product, BuyPrice: 100}, defaultShopID, ts)
		if err != nil {
			t.Fatalf("insertItem: %v", err)
		}
		return id
	}
	mkGroup := func(title string, itemID int64) int64 {
		res, err := db.Exec("INSERT INTO link_groups(shop_id,title,note,created_at,updated_at) VALUES(?,?,?,?,?)",
			defaultShopID, title, "", ts, ts)
		if err != nil {
			t.Fatalf("insert group: %v", err)
		}
		gid, _ := res.LastInsertId()
		if err := setGroupItems(gid, []int64{itemID}, defaultShopID); err != nil {
			t.Fatalf("setGroupItems: %v", err)
		}
		return gid
	}

	targetItem := mkItem("主联单货")
	sourceItem := mkItem("待并联单货")
	target := mkGroup("主联单", targetItem)
	source := mkGroup("待并联单", sourceItem)
	if err := saveGroupExternals(source, defaultShopID, []GroupExternal{
		{Product: "外部成交", Amount: 88},
	}, "sell"); err != nil {
		t.Fatalf("saveGroupExternals: %v", err)
	}

	if _, err := db.Exec(`
        CREATE TRIGGER fail_group_delete
        BEFORE DELETE ON link_groups
        WHEN OLD.id = ` + strconv.FormatInt(source, 10) + `
        BEGIN SELECT RAISE(ABORT, 'injected delete failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	err = mergeGroupsTx(tx, target, []int64{target, source}, []int64{targetItem, sourceItem}, defaultShopID, "合并后标题")
	if err == nil {
		t.Fatal("注入删除失败后，mergeGroupsTx 应返回错误")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	var targetItems, sourceItems, sourceGroups, sourceExternals int
	db.QueryRow("SELECT COUNT(*) FROM group_items WHERE group_id=?", target).Scan(&targetItems)
	db.QueryRow("SELECT COUNT(*) FROM group_items WHERE group_id=?", source).Scan(&sourceItems)
	db.QueryRow("SELECT COUNT(*) FROM link_groups WHERE id=?", source).Scan(&sourceGroups)
	db.QueryRow("SELECT COUNT(*) FROM group_externals WHERE group_id=? AND shop_id=?", source, defaultShopID).Scan(&sourceExternals)
	if targetItems != 1 || sourceItems != 1 || sourceGroups != 1 || sourceExternals != 1 {
		t.Errorf("回滚后数据不完整: targetItems=%d sourceItems=%d sourceGroups=%d sourceExternals=%d",
			targetItems, sourceItems, sourceGroups, sourceExternals)
	}
}

// 外部成交（线下 / 其他平台）只存在联单里：不写 items、不出现在明细，
// 但要照常进联单的销售额 / 货品成本 / 利润。
func TestGroupExternals(t *testing.T) {
	os.Setenv("DB_PATH", t.TempDir()+"/ext.db")
	defer os.Unsetenv("DB_PATH")
	if err := initDB(); err != nil {
		t.Fatalf("initDB: %v", err)
	}
	defer db.Close()

	ts := nowStr()
	res, err := db.Exec("INSERT INTO link_groups(shop_id,title,note,created_at,updated_at) VALUES(?,?,?,?,?)",
		defaultShopID, "外部成交联单", "", ts, ts)
	if err != nil {
		t.Fatalf("insert group: %v", err)
	}
	gid, _ := res.LastInsertId()

	if err := saveGroupExternals(gid, defaultShopID, []GroupExternal{
		{Product: "离线卖的货", Amount: 800, Quantity: 2, Counterparty: "老王", Time: "2026-11-10"},
	}, "sell"); err != nil {
		t.Fatalf("save sell: %v", err)
	}
	if err := saveGroupExternals(gid, defaultShopID, []GroupExternal{
		{Product: "线下收的货", Amount: 500, Quantity: 3},
	}, "buy"); err != nil {
		t.Fatalf("save buy: %v", err)
	}

	// 关键约束：一条 items 都不该产生
	var itemCount int
	db.QueryRow("SELECT COUNT(*) FROM items").Scan(&itemCount)
	if itemCount != 0 {
		t.Errorf("外部成交不应写入 items，实际 %d 条", itemCount)
	}

	exts := loadGroupExternals(gid, defaultShopID)
	if len(exts) != 2 {
		t.Fatalf("externals=%d want 2", len(exts))
	}
	s := computeGroupStats(nil, exts)
	if s.BuyCount != 1 || s.SellCount != 1 {
		t.Errorf("buy=%d sell=%d want 1/1", s.BuyCount, s.SellCount)
	}
	if s.Sales != 800 || s.Goods != 500 {
		t.Errorf("sales=%v goods=%v want 800/500", s.Sales, s.Goods)
	}
	if s.Profit != 300 {
		t.Errorf("profit=%v want 300", s.Profit)
	}

	// 整体替换只影响自己那一侧：卖出改金额后，买入的成本仍在
	if err := saveGroupExternals(gid, defaultShopID, []GroupExternal{
		{Product: "离线卖的货", Amount: 900},
	}, "sell"); err != nil {
		t.Fatalf("replace sell: %v", err)
	}
	exts = loadGroupExternals(gid, defaultShopID)
	s = computeGroupStats(nil, exts)
	if s.Sales != 900 || s.Goods != 500 {
		t.Errorf("after replace sales=%v goods=%v want 900/500", s.Sales, s.Goods)
	}

	// 清空买入：货品成本归零
	if err := saveGroupExternals(gid, defaultShopID, nil, "buy"); err != nil {
		t.Fatalf("clear buy: %v", err)
	}
	s = computeGroupStats(nil, loadGroupExternals(gid, defaultShopID))
	if s.Goods != 0 || s.BuyCount != 0 {
		t.Errorf("after clear goods=%v buy=%d want 0/0", s.Goods, s.BuyCount)
	}
}

// 无成本卖单（没有买进成本、也没进联单）：
//  1. 不进总销售额 / 总成本 / 净利润；
//  2. 单独统计在 NoCostSales 里；
//  3. 不占也不释放在库资金（以前会按商品名抵扣同名买进单，属于错误口径）。
func TestNoCostSaleIsolated(t *testing.T) {
	os.Setenv("DB_PATH", t.TempDir()+"/nocost.db")
	defer os.Unsetenv("DB_PATH")
	if err := initDB(); err != nil {
		t.Fatalf("initDB: %v", err)
	}
	defer db.Close()

	// 一个在库买单：1000 + 20 运费
	pid, _ := insertItem(&Item{Product: "同名货", BuyPrice: 1000, BuyShipping: 20, Quantity: 1}, defaultShopID, nowStr())
	// 一个没有成本、也没进联单的卖单，商品名故意与买单相同
	nid, _ := insertItem(&Item{Product: "同名货", SellPrice: 500, SellNet: 490, SellFee: 10, SellShipping: 0}, defaultShopID, nowStr())
	// 把它标记成「已完结」：老数据里可能有，但不能因此释放买单的占用资金
	db.Exec("UPDATE items SET settled_at=? WHERE id=?", nowStr(), nid)

	s := callStats(t)
	// 无成本卖单口径已合并回普通卖单（字段已删除，不再单独统计）
	// 新口径：销售额统计所有卖单，成本 = 所有买单 + 运费 + 服务费
	if s.TotalSales != 500 {
		t.Errorf("总销售额=%v want 500（所有卖单都算）", s.TotalSales)
	}
	if s.TotalCost != 10 {
		t.Errorf("已售成本=%v want 10（这条卖单没有货品成本，只有服务费）", s.TotalCost)
	}
	// 卖单不该抵扣买进单：在库仍是 1020
	if s.HoldingCost < 1019.99 {
		t.Errorf("holding=%v want 1020（卖单不得释放占用资金）", s.HoldingCost)
	}
	if s.ReleasedCost != 0 {
		t.Errorf("released=%v want 0", s.ReleasedCost)
	}
	if pid == 0 {
		t.Fatal("买单未写入")
	}
}

// 今日销售额按卖出时间的日期统计，含无成本卖单。
func TestTodaySales(t *testing.T) {
	os.Setenv("DB_PATH", t.TempDir()+"/today.db")
	defer os.Unsetenv("DB_PATH")
	if err := initDB(); err != nil {
		t.Fatalf("initDB: %v", err)
	}
	defer db.Close()

	today := nowStr()[:10]
	insertItem(&Item{Product: "今天卖的", SellPrice: 300, SellNet: 300, SellTime: today + " 10:00:00"}, defaultShopID, nowStr())
	insertItem(&Item{Product: "昨天卖的", SellPrice: 700, SellNet: 700, SellTime: "2020-01-01 10:00:00"}, defaultShopID, nowStr())

	s := callStats(t)
	if s.TodaySales != 300 || s.TodayCount != 1 {
		t.Errorf("today=%v/%d want 300/1", s.TodaySales, s.TodayCount)
	}
}
