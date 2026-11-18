package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// 多店铺隔离的契约测试。
//
// 最容易翻车的场景是「两个店铺有同名商品」：商品匹配、联单、统计都按名字聚合，
// 一旦漏了 shop_id，A 店的卖出单会吃掉 B 店的买进单，且表面上数字完全正常，
// 只有对账时才会发现。所以这里的夹具刻意用同名商品做诱导。

func createTestShop(t *testing.T, name string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO shops(name,color,sort,note,archived_at,created_at,updated_at)
        VALUES(?,?,?,?,?,?,?)`, name, "#123456", 2, "", "", nowStr(), nowStr())
	if err != nil {
		t.Fatalf("创建店铺 %s: %v", name, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func doShopReq(t *testing.T, h http.Handler, method, path string, body interface{}, shop int64) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		req = httptest.NewRequest(method, path, strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if shop > 0 {
		req.Header.Set(shopHeader, strconv.FormatInt(shop, 10))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func mapOf(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("解析响应失败: %v（%s）", err, w.Body.String())
	}
	return m
}

func itemIDsOf(m map[string]interface{}) []int64 {
	ids := []int64{}
	arr, _ := m["items"].([]interface{})
	for _, x := range arr {
		if mm, ok := x.(map[string]interface{}); ok {
			if v, ok := mm["id"].(float64); ok {
				ids = append(ids, int64(v))
			}
		}
	}
	return ids
}

func namesOf(m map[string]interface{}, key string) map[string]bool {
	out := map[string]bool{}
	arr, _ := m[key].([]interface{})
	for _, x := range arr {
		if mm, ok := x.(map[string]interface{}); ok {
			if p, ok := mm["product"].(string); ok {
				out[p] = true
			}
		}
	}
	return out
}

func contains(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func TestShopIsolation(t *testing.T) {
	h := newExtrasEnv(t)
	if err := createGroupTables(); err != nil {
		t.Fatalf("createGroupTables: %v", err)
	}
	shopB := createTestShop(t, "店铺B")

	// 两家店都有「同名商品」，B 店的买进价更低（若串店，A 店卖出会挂到 B 店买单上）
	aBuy, err := insertItem(&Item{Product: "同名商品", BuyPrice: 100, BuyTime: "2026-01-01 10:00:00", Quantity: 1}, defaultShopID, nowStr())
	if err != nil {
		t.Fatalf("插入 A 店买进: %v", err)
	}
	bBuy, err := insertItem(&Item{Product: "同名商品", BuyPrice: 50, BuyTime: "2026-02-01 10:00:00", Quantity: 1}, shopB, nowStr())
	if err != nil {
		t.Fatalf("插入 B 店买进: %v", err)
	}
	aSell, err := insertItem(&Item{Product: "同名商品", ParentID: aBuy, SellPrice: 200, SellNet: 200, SellTime: "2026-03-01 10:00:00", Quantity: 1}, defaultShopID, nowStr())
	if err != nil {
		t.Fatalf("插入 A 店卖出: %v", err)
	}
	bOnly, err := insertItem(&Item{Product: "B店独有商品", BuyPrice: 10, Quantity: 1}, shopB, nowStr())
	if err != nil {
		t.Fatalf("插入 B 店独有: %v", err)
	}
	refreshPurchaseIndex()

	t.Run("明细列表", func(t *testing.T) {
		a := mapOf(t, doShopReq(t, h, "GET", "/api/records?page_size=100", nil, defaultShopID))
		ids := itemIDsOf(a)
		if !contains(ids, aBuy) || !contains(ids, aSell) {
			t.Errorf("A 店列表缺少自己的记录: %v", ids)
		}
		if contains(ids, bBuy) || contains(ids, bOnly) {
			t.Errorf("A 店列表出现 B 店记录: %v", ids)
		}

		b := mapOf(t, doShopReq(t, h, "GET", "/api/records?page_size=100", nil, shopB))
		bids := itemIDsOf(b)
		if contains(bids, aBuy) || contains(bids, aSell) {
			t.Errorf("B 店列表出现 A 店记录: %v", bids)
		}
		if !contains(bids, bBuy) || !contains(bids, bOnly) {
			t.Errorf("B 店列表缺少自己的记录: %v", bids)
		}
	})

	t.Run("总数统计", func(t *testing.T) {
		a := mapOf(t, doShopReq(t, h, "GET", "/api/stats", nil, defaultShopID))
		b := mapOf(t, doShopReq(t, h, "GET", "/api/stats", nil, shopB))
		aTotal, _ := a["total_count"].(float64)
		bTotal, _ := b["total_count"].(float64)
		if int(aTotal) != 2 {
			t.Errorf("A 店 total_count = %v, want 2", aTotal)
		}
		if int(bTotal) != 2 {
			t.Errorf("B 店 total_count = %v, want 2", bTotal)
		}
	})

	t.Run("洞察不串店", func(t *testing.T) {
		a := mapOf(t, doShopReq(t, h, "GET", "/api/stats/extras", nil, defaultShopID))
		if namesOf(a, "top_products")["B店独有商品"] {
			t.Error("A 店利润榜出现 B 店商品")
		}
		b := mapOf(t, doShopReq(t, h, "GET", "/api/stats/extras", nil, shopB))
		if namesOf(b, "top_products")["同名商品"] {
			t.Error("B 店利润榜出现 A 店（同名）商品，说明按名字聚合时没带店铺条件")
		}
	})

	t.Run("买进单与匹配", func(t *testing.T) {
		ap := mapOf(t, doShopReq(t, h, "GET", "/api/purchases", nil, defaultShopID))
		for _, id := range itemIDsOf(ap) {
			if id == bBuy {
				t.Error("A 店买进单列表出现 B 店记录")
			}
		}

		// 同名商品必须匹配到本店那张，而不是价格更低的 B 店那张
		m := mapOf(t, doShopReq(t, h, "POST", "/api/match",
			map[string]interface{}{"product": "同名商品"}, defaultShopID))
		if match, ok := m["match"].(map[string]interface{}); ok {
			if id, _ := match["id"].(float64); int64(id) != aBuy {
				t.Errorf("A 店匹配到 id=%v，应为 %d（跨店匹配）", id, aBuy)
			}
		} else {
			t.Error("A 店未匹配到自家买进单")
		}
	})

	t.Run("单条读写越权", func(t *testing.T) {
		if w := doShopReq(t, h, "GET", "/api/records/"+strconv.FormatInt(bBuy, 10), nil, defaultShopID); w.Code != 404 {
			t.Errorf("跨店 GET 返回 %d, want 404", w.Code)
		}
		body := map[string]interface{}{
			"product": "被篡改", "side": "", "buy_price": 1, "quantity": 1,
		}
		if w := doShopReq(t, h, "PUT", "/api/records/"+strconv.FormatInt(bBuy, 10), body, defaultShopID); w.Code != 404 {
			t.Errorf("跨店 PUT 返回 %d, want 404", w.Code)
		}
		var name string
		db.QueryRow("SELECT product FROM items WHERE id=?", bBuy).Scan(&name)
		if name != "同名商品" {
			t.Errorf("B 店记录被跨店改写成 %q", name)
		}

		// 跨店删除：接口可能返回 ok，但绝不能真的删掉
		doShopReq(t, h, "DELETE", "/api/records/"+strconv.FormatInt(bOnly, 10), nil, defaultShopID)
		var n int
		db.QueryRow("SELECT COUNT(*) FROM items WHERE id=?", bOnly).Scan(&n)
		if n != 1 {
			t.Error("A 店删除操作把 B 店记录删掉了")
		}
	})

	t.Run("联单", func(t *testing.T) {
		// A 店建联单时混入 B 店商品，应被静默忽略
		w := doShopReq(t, h, "POST", "/api/groups", map[string]interface{}{
			"title": "跨店尝试", "item_ids": []int64{aBuy, bOnly},
		}, defaultShopID)
		if w.Code != 200 {
			t.Fatalf("创建联单失败: %d %s", w.Code, w.Body.String())
		}
		g := mapOf(t, w)
		ids := []int64{}
		arr, _ := g["item_ids"].([]interface{})
		for _, x := range arr {
			if v, ok := x.(float64); ok {
				ids = append(ids, int64(v))
			}
		}
		if contains(ids, bOnly) {
			t.Error("联单装进了 B 店商品")
		}

		// B 店看不到 A 店的联单
		bl := mapOf(t, doShopReq(t, h, "GET", "/api/groups", nil, shopB))
		if arr, _ := bl["items"].([]interface{}); len(arr) != 0 {
			t.Errorf("B 店看到 %d 个联单（应只属于 A 店）", len(arr))
		}
	})

	t.Run("导出", func(t *testing.T) {
		a := mapOf(t, doShopReq(t, h, "GET", "/api/export", nil, defaultShopID))
		if len(itemIDsOf(a)) != 2 {
			t.Errorf("A 店导出 %d 条，应只含本店 2 条", len(itemIDsOf(a)))
		}
		w := doShopReq(t, h, "GET", "/api/export?format=csv", nil, defaultShopID)
		if strings.Contains(w.Body.String(), "B店独有商品") {
			t.Error("A 店 CSV 导出含 B 店数据")
		}
	})

	t.Run("撤销栈", func(t *testing.T) {
		aw := doShopReq(t, h, "GET", "/api/undo/ops", nil, defaultShopID)
		if aw.Code != 200 {
			t.Fatalf("撤销列表 %d", aw.Code)
		}
		if strings.Contains(aw.Body.String(), "B店独有") {
			t.Error("A 店撤销列表含 B 店内容")
		}
	})

	t.Run("店铺列表", func(t *testing.T) {
		m := mapOf(t, doShopReq(t, h, "GET", "/api/shops", nil, shopB))
		if cur, _ := m["current"].(float64); int64(cur) != shopB {
			t.Errorf("current = %v, want %d", m["current"], shopB)
		}
		if arr, _ := m["items"].([]interface{}); len(arr) != 2 {
			t.Errorf("店铺数 = %d, want 2", len(arr))
		}
	})
}

// 店铺参数校验：非法/不存在必须报错，绝不静默回落到默认店。
func TestShopResolve(t *testing.T) {
	newExtrasEnv(t)

	cases := []struct {
		header string
		want   int64
		bad    bool
	}{
		{"", defaultShopID, false},
		{"1", defaultShopID, false},
		{"abc", 0, true},
		{"0", 0, true},
		{"-3", 0, true},
		{"99999", 0, true},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/api/stats", nil)
		if c.header != "" {
			req.Header.Set(shopHeader, c.header)
		}
		got, err := resolveShop(req)
		if c.bad {
			if err == nil {
				t.Errorf("header=%q 应报错，实际返回 %d", c.header, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("header=%q 意外报错: %v", c.header, err)
			continue
		}
		if got != c.want {
			t.Errorf("header=%q -> %d, want %d", c.header, got, c.want)
		}
	}

	// 归档店铺视为不存在
	db.Exec("UPDATE shops SET archived_at=? WHERE id=?", nowStr(), defaultShopID)
	defer db.Exec("UPDATE shops SET archived_at='' WHERE id=?", defaultShopID)
	req := httptest.NewRequest("GET", "/api/stats", nil)
	req.Header.Set(shopHeader, "1")
	if _, err := resolveShop(req); err == nil {
		t.Error("已归档店铺不应通过校验")
	}
}

// 请求带非法店铺时应返回 400，而不是落到默认店。
func TestShopHeaderRejected(t *testing.T) {
	h := newExtrasEnv(t)
	w := doShopReq(t, h, "GET", "/api/records", nil, 99999)
	if w.Code != 400 {
		t.Fatalf("不存在店铺返回 %d, want 400", w.Code)
	}
	req := httptest.NewRequest("GET", "/api/records", nil)
	req.Header.Set(shopHeader, "not-a-number")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("非法店铺返回 %d, want 400", rec.Code)
	}
}

// 存量数据迁移：老库升级后所有记录都应归到默认店铺，统计口径不变。
func TestShopMigrationDefaults(t *testing.T) {
	newExtrasEnv(t)

	var n int
	db.QueryRow("SELECT COUNT(*) FROM shops WHERE id=?", defaultShopID).Scan(&n)
	if n != 1 {
		t.Fatalf("默认店铺未创建")
	}

	// 模拟旧库：不带 shop_id 的插入会走 DEFAULT 1
	if _, err := db.Exec(`INSERT INTO items(product, product_norm, buy_price, created_at, updated_at)
        VALUES('旧数据','旧数据',100,?,?)`, nowStr(), nowStr()); err != nil {
		t.Fatalf("插入旧格式数据: %v", err)
	}
	var sid int64
	db.QueryRow("SELECT shop_id FROM items WHERE product='旧数据'").Scan(&sid)
	if sid != defaultShopID {
		t.Errorf("存量数据 shop_id = %d, want %d", sid, defaultShopID)
	}
}
