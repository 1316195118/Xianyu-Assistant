package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
)

// 日期筛选改为可直接命中交易时间索引的范围查询后，
// 必须保持“按天筛选”的原有口径：起始日和结束日都包含，次日不包含。
func TestRecordDateFilterRemainsInclusiveByDay(t *testing.T) {
	os.Setenv("DB_PATH", t.TempDir()+"/date-filter.db")
	defer os.Unsetenv("DB_PATH")
	if err := initDB(); err != nil {
		t.Fatalf("initDB: %v", err)
	}
	defer db.Close()

	_, err := insertItem(&Item{
		Product:   "日期筛选测试货",
		SellPrice: 128,
		SellNet:   120,
		SellTime:  "2026-11-22 23:59:59",
	}, defaultShopID, nowStr())
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	fetch := func(query string) []Item {
		req := httptest.NewRequest("GET", "/api/records?"+query, nil)
		req.Header.Set("X-Shop-Id", strconv.FormatInt(defaultShopID, 10))
		rr := httptest.NewRecorder()
		listRecords(rr, req)
		if rr.Code != 200 {
			t.Fatalf("listRecords(%q): status=%d body=%s", query, rr.Code, rr.Body.String())
		}
		var out struct {
			Items []Item `json:"items"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out.Items
	}

	if got := fetch("from=2026-11-22&to=2026-11-22"); len(got) != 1 {
		t.Fatalf("当天闭区间命中 %d 条，want 1", len(got))
	}
	if got := fetch("from=2026-11-23"); len(got) != 0 {
		t.Fatalf("次日误命中 %d 条，want 0", len(got))
	}
	if got := fetch("to=2026-11-21"); len(got) != 0 {
		t.Fatalf("结束日前误命中 %d 条，want 0", len(got))
	}
	if got := fetch("from=2026-11-22&to=2026-11-22"); got[0].ID == 0 {
		t.Fatal("日期筛选返回的记录缺少 id")
	}
}

// 联单分摊改为只读轻量字段后，仍要按买单成本、卖单成交价占比派生成本。
// 附件/备注等大字段不应参与这个热路径。
func TestGroupAllocUsesSlimQueryWithoutChangingCost(t *testing.T) {
	os.Setenv("DB_PATH", t.TempDir()+"/alloc-slim.db")
	defer os.Unsetenv("DB_PATH")
	if err := initDB(); err != nil {
		t.Fatalf("initDB: %v", err)
	}
	defer db.Close()

	buyID, err := insertItem(&Item{
		Product:     "轻量分摊测试货",
		BuyPrice:    1000,
		BuyShipping: 20,
		Quantity:    1,
	}, defaultShopID, nowStr())
	if err != nil {
		t.Fatalf("insert buy: %v", err)
	}
	saleID, err := insertItem(&Item{
		Product:   "轻量分摊测试货",
		ParentID:  buyID,
		SellPrice: 500,
		SellNet:   480,
		Quantity:  1,
	}, defaultShopID, nowStr())
	if err != nil {
		t.Fatalf("insert sale: %v", err)
	}
	res, err := db.Exec("INSERT INTO link_groups(title, created_at, updated_at) VALUES(?,?,?)",
		"轻量分摊测试联单", nowStr(), nowStr())
	if err != nil {
		t.Fatalf("insert group: %v", err)
	}
	gid, _ := res.LastInsertId()
	if err := setGroupItems(gid, []int64{buyID, saleID}, defaultShopID); err != nil {
		t.Fatalf("setGroupItems: %v", err)
	}

	alloc := loadGroupAlloc(defaultShopID)
	unit, ok := alloc[saleID]
	if !ok {
		t.Fatalf("联单卖单没有获得分摊成本: alloc=%+v", alloc)
	}
	// 单卖单应带走整张买单的 1020 成本。
	if unit < 1019.995 || unit > 1020.005 {
		t.Fatalf("分摊单件成本=%v，want 1020", unit)
	}
}
