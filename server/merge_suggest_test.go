package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
)

// 合并建议：相似标题的联单要给出建议（含多个候选），提交时按用户选的合并；
// 同名散单要给出「生成联单」建议，提交后真的建成一张联单。
func TestGroupMergeSuggestions(t *testing.T) {
	os.Setenv("DB_PATH", t.TempDir()+"/mergesug.db")
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

	ts := "2026-03-02 10:00:00"
	mkItem := func(product string, buy float64) int64 {
		id, err := insertItem(&Item{Product: product, BuyPrice: buy, BuyTime: ts}, defaultShopID, ts)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		return id
	}
	mkGroup := func(title string, ids ...int64) int64 {
		res, err := db.Exec("INSERT INTO link_groups(shop_id,title,note,created_at,updated_at) VALUES(?,?,?,?,?)",
			defaultShopID, title, "", ts, ts)
		if err != nil {
			t.Fatalf("group: %v", err)
		}
		gid, _ := res.LastInsertId()
		if err := setGroupItems(gid, ids, defaultShopID); err != nil {
			t.Fatalf("setGroupItems: %v", err)
		}
		return gid
	}

	g1 := mkGroup("网心云OEC Turbo", mkItem("网心云OEC Turbo", 500))
	g2 := mkGroup("网心云OEC-Turbo", mkItem("网心云OEC Turbo", 520))
	// 同名散单（都不在联单里）：2 买 1 卖
	mkItem("群晖DS220+", 1380)
	mkItem("群晖DS220+", 1400)
	if _, err := insertItem(&Item{Product: "群晖DS220+", SellPrice: 1900, SellNet: 1880, SellTime: ts}, defaultShopID, ts); err != nil {
		t.Fatalf("insert sell: %v", err)
	}

	type sug struct {
		Groups []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
			Items int    `json:"items"`
		} `json:"groups"`
		Items []struct {
			ID      int64   `json:"id"`
			Product string  `json:"product"`
			Kind    string  `json:"kind"`
			Amount  float64 `json:"amount"`
		} `json:"items"`
		Key     string  `json:"key"`
		Kind    string  `json:"kind"`
		Title   string  `json:"title"`
		Score   int     `json:"score"`
		ItemIDs []int64 `json:"item_ids"`
		HasBoth bool    `json:"has_both"`
	}
	getSug := func() (out struct {
		Groups []sug `json:"groups"`
		Items  []sug `json:"items"`
	}) {
		rr := httptest.NewRecorder()
		handleGroupMergeSuggestions(rr, httptest.NewRequest("GET", "/api/groups/merge-suggestions", nil))
		if rr.Code != 200 {
			t.Fatalf("suggestions code=%d body=%s", rr.Code, rr.Body.String())
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("suggestions json: %v", err)
		}
		return out
	}

	s := getSug()
	// ① 相似标题的两张联单要出现一条建议
	var hit *sug
	for i := range s.Groups {
		if len(s.Groups[i].Groups) == 2 {
			hit = &s.Groups[i]
			break
		}
	}
	if hit == nil {
		t.Fatalf("没有给出相似标题合并建议: %+v", s.Groups)
	}
	if hit.Kind != "group" || hit.Score < 60 {
		t.Errorf("建议结构不对: kind=%s score=%d", hit.Kind, hit.Score)
	}
	// ② 同名散单要出现「生成联单」建议，且标记买卖都有
	var itemHit *sug
	for i := range s.Items {
		if s.Items[i].Title == "群晖DS220+" {
			itemHit = &s.Items[i]
		}
	}
	if itemHit == nil {
		t.Fatalf("没有给出同名散单建议: %+v", s.Items)
	}
	if len(itemHit.ItemIDs) != 3 || !itemHit.HasBoth {
		t.Errorf("同名散单建议不对: ids=%v has_both=%v", itemHit.ItemIDs, itemHit.HasBoth)
	}

	// ③ 提交合并联单
	{
		body, _ := json.Marshal(map[string]interface{}{
			"kind": "group", "title": "网心云OEC Turbo 一批", "group_ids": []int64{g1, g2},
		})
		req := httptest.NewRequest("POST", "/api/groups/merge-apply", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		handleGroupMergeApply(rr, req)
		if rr.Code != 200 {
			t.Fatalf("合并失败 code=%d body=%s", rr.Code, rr.Body.String())
		}
		var res struct {
			GroupID int64  `json:"group_id"`
			Title   string `json:"title"`
			Moved   int    `json:"moved"`
		}
		json.Unmarshal(rr.Body.Bytes(), &res)
		if res.GroupID != g1 || res.Title != "网心云OEC Turbo 一批" || res.Moved != 2 {
			t.Errorf("合并结果不对: %+v", res)
		}
		var n int
		db.QueryRow("SELECT COUNT(*) FROM link_groups WHERE shop_id=?", defaultShopID).Scan(&n)
		if n != 1 {
			t.Errorf("合并后联单数=%d want 1", n)
		}
		var cnt int
		db.QueryRow("SELECT COUNT(*) FROM group_items WHERE group_id=?", g1).Scan(&cnt)
		if cnt != 2 {
			t.Errorf("合并后成员数=%d want 2", cnt)
		}
	}

	// ④ 提交「同名散单生成联单」
	{
		body, _ := json.Marshal(map[string]interface{}{
			"kind": "items", "title": "群晖DS220+ 一批", "item_ids": itemHit.ItemIDs,
		})
		req := httptest.NewRequest("POST", "/api/groups/merge-apply", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		handleGroupMergeApply(rr, req)
		if rr.Code != 200 {
			t.Fatalf("生成联单失败 code=%d body=%s", rr.Code, rr.Body.String())
		}
		var res struct {
			GroupID int64 `json:"group_id"`
			Moved   int   `json:"moved"`
		}
		json.Unmarshal(rr.Body.Bytes(), &res)
		if res.GroupID == 0 || res.Moved != 3 {
			t.Errorf("生成联单结果不对: %+v", res)
		}
	}

	// ⑤ 已经进联单的记录不再出现在同名散单建议里
	s = getSug()
	for _, it := range s.Items {
		if it.Title == "群晖DS220+" {
			t.Errorf("已生成联单的同名散单还在建议里: %+v", it)
		}
	}
	_ = strconv.Itoa(0)
}
