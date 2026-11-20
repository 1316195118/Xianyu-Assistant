package main

import (
	"net/url"
	"sync"
	"testing"
)

// 这些读接口会派生联单成本或联单成员。回归测试把连接池压到 1：
// 任何“Rows 还没释放就申请下一条连接”的写法都会直接卡住，能稳定拦住同类死锁。
func TestReadHandlersDoNotHoldRowsAcrossQueries(t *testing.T) {
	h := newExtrasEnv(t)

	ts := "2026-11-21 09:00:00"
	pid, err := insertItem(&Item{
		Product: "并发测试路由器", BuyPrice: 100, BuyShipping: 10,
		BuyTime: ts, Quantity: 1,
	}, defaultShopID, nowStr())
	if err != nil {
		t.Fatalf("insert purchase: %v", err)
	}
	sid, err := insertItem(&Item{
		Product: "并发测试路由器", ParentID: pid, SellPrice: 180, SellNet: 170,
		SellTime: ts, Quantity: 1,
	}, defaultShopID, nowStr())
	if err != nil {
		t.Fatalf("insert sale: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin group tx: %v", err)
	}
	gid, err := createGroupTx(tx, defaultShopID, "并发测试联单", "", ts)
	if err != nil {
		tx.Rollback()
		t.Fatalf("createGroupTx: %v", err)
	}
	if err := setGroupItemsTx(tx, gid, []int64{pid, sid}, defaultShopID); err != nil {
		tx.Rollback()
		t.Fatalf("setGroupItemsTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit group tx: %v", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	paths := []string{
		"/api/stats",
		"/api/stats/extras",
		"/api/records?page_size=20",
		"/api/records/todo-count",
		"/api/trend?period=month",
		"/api/product-profit",
		"/api/records/price-hint?product=" + url.QueryEscape("并发测试路由器"),
		"/api/groups?brief=1",
		"/api/groups",
		"/api/groups/candidates?similar=" + url.QueryEscape("并发测试路由器"),
		"/api/purchases",
		"/api/consign/stats",
	}
	for _, path := range paths {
		w := doExtrasReq(h, "GET", path, nil)
		if w.Code != 200 {
			t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body.String())
		}
	}

	// 连接池恢复为线上配置后，再模拟多人同时刷新首页、明细与联单。
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(8)
	var wg sync.WaitGroup
	errs := make(chan string, 32)
	for i := 0; i < 32; i++ {
		path := paths[i%len(paths)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := doExtrasReq(h, "GET", path, nil)
			if w.Code != 200 {
				errs <- path
			}
		}()
	}
	wg.Wait()
	close(errs)
	for path := range errs {
		t.Errorf("concurrent request failed: %s", path)
	}
}

// 多人同时录入和刷新时，SQLite 的写入必须由 busy_timeout 正常排队，
// 不能出现 database is locked，也不能让读请求在写事务期间卡住。
func TestConcurrentWritesAndReads(t *testing.T) {
	h := newExtrasEnv(t)

	const (
		writes      = 24
		reads       = 48
		productName = "并发读写测试"
	)
	readPaths := []string{
		"/api/stats",
		"/api/records?page_size=20",
		"/api/trend?period=month",
		"/api/groups?brief=1",
		"/api/purchases",
	}

	start := make(chan struct{})
	errs := make(chan string, writes+reads)
	var wg sync.WaitGroup

	for i := 0; i < writes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			w := doExtrasReq(h, "POST", "/api/records", map[string]interface{}{
				"product": productName, "side": "buy", "buy_price": 1, "quantity": 1,
			})
			if w.Code != 200 {
				errs <- "POST /api/records: " + w.Body.String()
			}
		}()
	}
	for i := 0; i < reads; i++ {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			<-start
			w := doExtrasReq(h, "GET", path, nil)
			if w.Code != 200 {
				errs <- path + ": " + w.Body.String()
			}
		}(readPaths[i%len(readPaths)])
	}

	close(start)
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM items WHERE product=?", productName).Scan(&count); err != nil {
		t.Fatalf("count concurrent writes: %v", err)
	}
	if count != writes {
		t.Fatalf("concurrent writes count=%d, want %d", count, writes)
	}
}
