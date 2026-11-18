package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
)

// 智能挂成本：预览只给建议、不写库；确认后才挂，并正确处理件数用完的情况。
func TestAutoLinkPreviewAndApply(t *testing.T) {
	os.Setenv("DB_PATH", t.TempDir()+"/autolink.db")
	defer os.Unsetenv("DB_PATH")
	if err := initDB(); err != nil {
		t.Fatalf("initDB: %v", err)
	}
	defer db.Close()

	// 买进：Switch 一套 1000 + 20 运费（1 件）；罗技鼠标 200 两个（2 件）
	p1, _ := insertItem(&Item{Product: "Switch 游戏机", BuyPrice: 1000, BuyShipping: 20, Quantity: 1}, defaultShopID, nowStr())
	p2, _ := insertItem(&Item{Product: "罗技鼠标", BuyPrice: 200, Quantity: 2}, defaultShopID, nowStr())
	// 卖出：名字写法不同（空格差异）也要能配到
	s1, _ := insertItem(&Item{Product: "Switch游戏机", SellPrice: 1400, SellNet: 1380, SellFee: 20}, defaultShopID, nowStr())
	s2, _ := insertItem(&Item{Product: "罗技鼠标", SellPrice: 300, SellNet: 295, SellFee: 5}, defaultShopID, nowStr())

	preview := func() map[string]interface{} {
		w := httptest.NewRecorder()
		handleAutoLinkPreview(w, httptest.NewRequest("GET", "/api/records/auto-link/preview", nil))
		if w.Code != 200 {
			t.Fatalf("preview status=%d body=%s", w.Code, w.Body.String())
		}
		var out map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("preview json: %v", err)
		}
		return out
	}

	out := preview()
	if int(out["total"].(float64)) != 2 {
		t.Fatalf("preview total=%v want 2", out["total"])
	}
	if int(out["matched"].(float64)) != 2 {
		t.Errorf("matched=%v want 2（两个都能精确匹配）", out["matched"])
	}

	// 应用用户确认的两组配对
	body, _ := json.Marshal(map[string]interface{}{
		"links": []map[string]int64{
			{"sale_id": s1, "purchase_id": p1},
			{"sale_id": s2, "purchase_id": p2},
		},
	})
	w := httptest.NewRecorder()
	handleAutoLinkApply(w, httptest.NewRequest("POST", "/api/records/auto-link/apply", bytes.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("apply status=%d body=%s", w.Code, w.Body.String())
	}
	var res struct {
		Linked int `json:"linked"`
	}
	json.Unmarshal(w.Body.Bytes(), &res)
	if res.Linked != 2 {
		t.Fatalf("linked=%d want 2", res.Linked)
	}

	it1, _ := getItem(s1, defaultShopID)
	if it1.ParentID != p1 {
		t.Errorf("sale1 parent=%d want %d", it1.ParentID, p1)
	}
	// 单件成本 =（1000+20）/1 = 1020
	if it1.UnitCost != 1020 {
		t.Errorf("sale1 unit=%v want 1020", it1.UnitCost)
	}
	it2, _ := getItem(s2, defaultShopID)
	// 单件成本 = 200/2 = 100
	if it2.UnitCost != 100 {
		t.Errorf("sale2 unit=%v want 100", it2.UnitCost)
	}

	// 已挂完的卖单不再出现在预览里
	if n := int(preview()["total"].(float64)); n != 0 {
		t.Errorf("挂完后 preview total=%d want 0", n)
	}

	// 再卖一个同名鼠标：买进单只剩 1 件，仍可配；挂完后再来一个应当提示件数用完
	s3, _ := insertItem(&Item{Product: "罗技鼠标", SellPrice: 280, SellNet: 276, SellFee: 4}, defaultShopID, nowStr())
	out = preview()
	if int(out["matched"].(float64)) != 1 {
		t.Errorf("第二次 matched=%v want 1", out["matched"])
	}
	body, _ = json.Marshal(map[string]interface{}{
		"links": []map[string]int64{{"sale_id": s3, "purchase_id": p2}},
	})
	w = httptest.NewRecorder()
	handleAutoLinkApply(w, httptest.NewRequest("POST", "/api/records/auto-link/apply", bytes.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("apply2 status=%d", w.Code)
	}

	s4, _ := insertItem(&Item{Product: "罗技鼠标", SellPrice: 260, SellNet: 256, SellFee: 4}, defaultShopID, nowStr())
	out = preview()
	rows := out["items"].([]interface{})
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	row := rows[0].(map[string]interface{})
	if row["sale_id"].(float64) != float64(s4) {
		t.Errorf("row sale=%v want %d", row["sale_id"], s4)
	}
	if row["reason"] != "no_stock" {
		t.Errorf("reason=%v want no_stock（两件都挂完了）", row["reason"])
	}
}

// 按售价估算成本：只写 unit_cost，不改关联状态；已完结的单拒绝。
func TestEstimateCost(t *testing.T) {
	os.Setenv("DB_PATH", t.TempDir()+"/estimate.db")
	defer os.Unsetenv("DB_PATH")
	if err := initDB(); err != nil {
		t.Fatalf("initDB: %v", err)
	}
	defer db.Close()

	s1, _ := insertItem(&Item{Product: "旧键盘", SellPrice: 300, SellNet: 290, SellFee: 10}, defaultShopID, nowStr())
	s2, _ := insertItem(&Item{Product: "已完结耳机", SellPrice: 500, SellNet: 490, SellFee: 10}, defaultShopID, nowStr())
	db.Exec("UPDATE items SET settled_at=? WHERE id=?", nowStr(), s2)

	body, _ := json.Marshal(map[string]interface{}{
		"items": []map[string]interface{}{
			{"sale_id": s1, "unit_cost": 210},
			{"sale_id": s2, "unit_cost": 300},
		},
	})
	w := httptest.NewRecorder()
	handleEstimateCost(w, httptest.NewRequest("POST", "/api/records/estimate-cost", bytes.NewReader(body)))
	// 已完结的单跳过、其余照常写入（不整批回滚：一条结案不该拖累其他几十条）
	if w.Code != 200 {
		t.Fatalf("status=%d want 200 body=%s", w.Code, w.Body.String())
	}
	var first struct {
		Updated int `json:"updated"`
		Skipped int `json:"skipped"`
	}
	json.Unmarshal(w.Body.Bytes(), &first)
	if first.Updated != 1 || first.Skipped != 1 {
		t.Fatalf("updated=%d skipped=%d want 1/1", first.Updated, first.Skipped)
	}
	it1, _ := getItem(s1, defaultShopID)
	if it1.UnitCost != 210 {
		t.Errorf("unit=%v want 210", it1.UnitCost)
	}
	it2, _ := getItem(s2, defaultShopID)
	if it2.UnitCost != 0 {
		t.Errorf("已完结的单不该被改，unit=%v", it2.UnitCost)
	}

	// 再单独提交一次正常的那条，确认幂等
	body, _ = json.Marshal(map[string]interface{}{
		"items": []map[string]interface{}{{"sale_id": s1, "unit_cost": 210}},
	})
	w = httptest.NewRecorder()
	handleEstimateCost(w, httptest.NewRequest("POST", "/api/records/estimate-cost", bytes.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var res struct {
		Updated int `json:"updated"`
	}
	json.Unmarshal(w.Body.Bytes(), &res)
	if res.Updated != 1 {
		t.Fatalf("updated=%d want 1", res.Updated)
	}
	it1, _ = getItem(s1, defaultShopID)
	if it1.UnitCost != 210 {
		t.Errorf("unit=%v want 210", it1.UnitCost)
	}
	// 填过成本后不再算「未挂成本」
	w = httptest.NewRecorder()
	handleAutoLinkPreview(w, httptest.NewRequest("GET", "/api/records/auto-link/preview", nil))
	var pv map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &pv)
	if int(pv["total"].(float64)) != 0 {
		t.Errorf("填完成本后 preview total=%v want 0", pv["total"])
	}
}
