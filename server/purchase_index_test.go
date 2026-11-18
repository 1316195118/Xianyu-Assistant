package main

import (
	"os"
	"testing"
)

// 用临时库验证：归一化列回填、索引命中、相似剪枝、子单数批量统计、写后索引刷新。
func TestPurchaseIndex(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("DB_PATH", dir+"/test.db")
	defer os.Unsetenv("DB_PATH")
	if err := initDB(); err != nil {
		t.Fatalf("initDB: %v", err)
	}
	defer db.Close()

	add := func(product string, buyPrice float64) int64 {
		it := &Item{Product: product, BuyPrice: buyPrice, Quantity: 1}
		id, err := insertItem(it, defaultShopID, nowStr())
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		return id
	}

	idA := add("MikroTik RB5009UG+S+IN 有线路由器", 1000)
	add("UBNT 优倍快 UACC 电源", 420)
	add("华硕 PN41 迷你主机", 220)
	// 挂一个卖出单到 idA，验证子单数批量统计
	_, err := insertItem(&Item{Product: "MikroTik RB5009UG+S+IN 有线路由器", ParentID: idA,
		UnitCost: 1000, SellPrice: 1200, SellNet: 1180, Quantity: 1}, defaultShopID, nowStr())
	if err != nil {
		t.Fatalf("insert sale: %v", err)
	}
	refreshPurchaseIndex()

	// 1) 归一化列已写入（去空格/标点/小写）
	var norm string
	if err := db.QueryRow("SELECT product_norm FROM items WHERE id=?", idA).Scan(&norm); err != nil {
		t.Fatalf("query norm: %v", err)
	}
	if norm != normProduct("MikroTik RB5009UG+S+IN 有线路由器") || norm == "" {
		t.Errorf("product_norm=%q 未正确写入", norm)
	}

	// 2) 精确索引命中（含空格/标点差异也应命中同一 norm）
	ex, sim := matchPurchasesIndexed("MikroTikRB5009UG+S+IN有线路由器", defaultShopID, 10)
	if len(ex) != 1 || ex[0].ID != idA {
		t.Errorf("精确索引未命中: exact=%+v similar=%+v", ex, sim)
	}
	if ex[0].ChildCount != 1 {
		t.Errorf("child_count=%d want 1（应由批量 GROUP BY 得出）", ex[0].ChildCount)
	}

	// 3) 列表快照含 3 个买进单（卖出单不应混入）
	list := listPurchases(defaultShopID)
	if len(list) != 3 {
		t.Errorf("listPurchases=%d want 3", len(list))
	}

	// 4) 录入即刷新：新增一个买进单后（模拟 defer refresh）索引立即可见
	add("新增测试商品 XYZ", 99)
	refreshPurchaseIndex()
	if len(listPurchases(defaultShopID)) != 4 {
		t.Errorf("新增后索引未刷新: %d", len(listPurchases(defaultShopID)))
	}
	ex2, _ := matchPurchasesIndexed("新增测试商品XYZ", defaultShopID, 10)
	if len(ex2) != 1 {
		t.Errorf("新增商品未进入精确索引: %+v", ex2)
	}

	// 5) matchExactPurchase 走索引列能命中
	if _, ok := matchExactPurchase("  MikroTik RB5009UG+S+IN 有线路由器 ", defaultShopID); !ok {
		t.Error("matchExactPurchase 未命中")
	}
}
