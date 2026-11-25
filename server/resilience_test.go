package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadCacheHitInvalidationAndNoCache(t *testing.T) {
	apiReadCache.clearAll()
	var calls atomic.Int32
	next := cachedGET(time.Minute, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeJSON(w, map[string]int{"value": int(calls.Load())})
	})
	do := func(cacheControl string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/cache-test?scope=one", nil)
		if cacheControl != "" {
			req.Header.Set("Cache-Control", cacheControl)
		}
		rr := httptest.NewRecorder()
		next(rr, req)
		return rr
	}

	first := do("")
	second := do("")
	if got := first.Header().Get("X-Response-Cache"); got != "MISS" {
		t.Fatalf("first cache status=%q want MISS", got)
	}
	if got := second.Header().Get("X-Response-Cache"); got != "HIT" {
		t.Fatalf("second cache status=%q want HIT", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls=%d want 1", calls.Load())
	}

	apiReadCache.clearAll()
	third := do("")
	if got := third.Header().Get("X-Response-Cache"); got != "MISS" || calls.Load() != 2 {
		t.Fatalf("after invalidation status=%q calls=%d", got, calls.Load())
	}
	fourth := do("no-cache")
	if got := fourth.Header().Get("X-Response-Cache"); got != "" || calls.Load() != 3 {
		t.Fatalf("no-cache status=%q calls=%d", got, calls.Load())
	}
}

func TestReadCacheDoesNotPersistResponseAcrossWrite(t *testing.T) {
	apiReadCache.clearAll()
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	next := cachedGET(time.Minute, func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			close(started)
			<-release
		}
		writeJSON(w, map[string]int{"value": int(n)})
	})

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		next(rr, httptest.NewRequest(http.MethodGet, "/api/cache-race", nil))
		done <- rr
	}()
	<-started
	apiReadCache.clearAll() // 模拟计算期间发生写入
	close(release)
	<-done

	after := httptest.NewRecorder()
	next(after, httptest.NewRequest(http.MethodGet, "/api/cache-race", nil))
	if got := after.Header().Get("X-Response-Cache"); got != "MISS" {
		t.Fatalf("stale response was cached, status=%q", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("handler calls=%d want 2", calls.Load())
	}
}

func TestGroupUpdateRejectsStaleVersion(t *testing.T) {
	setupOperationsTest(t)
	itemID, err := insertItem(&Item{Product: "并发联单测试", BuyPrice: 100, Quantity: 1}, defaultShopID, nowStr())
	if err != nil {
		t.Fatal(err)
	}
	initial := nowStr()
	res, err := db.Exec("INSERT INTO link_groups(shop_id,title,created_at,updated_at) VALUES(?,?,?,?)",
		defaultShopID, "并发联单", initial, initial)
	if err != nil {
		t.Fatal(err)
	}
	groupID, _ := res.LastInsertId()

	put := func(title string, expected string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]interface{}{
			"title": title, "item_ids": []int64{itemID}, "expected_updated_at": expected,
		})
		req := httptest.NewRequest(http.MethodPut, "/api/groups/"+strconv.FormatInt(groupID, 10), strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		handleGroupByID(rr, req)
		return rr
	}

	first := put("第一位同事的标题", initial)
	if first.Code != http.StatusOK {
		t.Fatalf("first update=%d %s", first.Code, first.Body.String())
	}
	second := put("第二位同事的标题", initial)
	if second.Code != http.StatusConflict {
		t.Fatalf("stale update=%d %s", second.Code, second.Body.String())
	}
	var conflict map[string]interface{}
	if err := json.Unmarshal(second.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if conflict["code"] != "conflict" {
		t.Fatalf("conflict code=%v", conflict["code"])
	}
	current, _ := conflict["current"].(map[string]interface{})
	if current == nil || current["updated_at"] == initial {
		t.Fatalf("conflict did not return latest version: %+v", conflict)
	}
}

func TestLotMetaRejectsStaleVersion(t *testing.T) {
	setupOperationsTest(t)
	itemID, err := insertItem(&Item{Product: "批次并发测试", BuyPrice: 80, Quantity: 1}, defaultShopID, nowStr())
	if err != nil {
		t.Fatal(err)
	}
	save := func(batch, expected string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]interface{}{
			"item_id": itemID, "batch_no": batch, "supplier": "测试供应商",
			"expected_updated_at": expected,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/operations/lots/meta", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		handleOperationsLotMeta(rr, req)
		return rr
	}
	first := save("B-001", "")
	if first.Code != http.StatusOK {
		t.Fatalf("first lot save=%d %s", first.Code, first.Body.String())
	}
	var saved map[string]interface{}
	_ = json.Unmarshal(first.Body.Bytes(), &saved)
	version, _ := saved["updated_at"].(string)
	if version == "" {
		t.Fatal("missing updated_at")
	}
	second := save("B-002", version)
	if second.Code != http.StatusOK {
		t.Fatalf("second lot save=%d %s", second.Code, second.Body.String())
	}
	stale := save("B-003", version)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale lot save=%d %s", stale.Code, stale.Body.String())
	}
}

func TestTodoCenterCreatesQualitySnapshotAndAuditListsWrites(t *testing.T) {
	setupOperationsTest(t)
	_, err := insertItem(&Item{Product: "待办测试", SellPrice: 120, SellTime: nowStr()}, defaultShopID, nowStr())
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handleOperationsTodos(rr, httptest.NewRequest(http.MethodGet, "/api/operations/todos", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("todos=%d %s", rr.Code, rr.Body.String())
	}
	var todos todoCenterResult
	if err := json.Unmarshal(rr.Body.Bytes(), &todos); err != nil {
		t.Fatal(err)
	}
	if todos.Health.CheckedCount != 1 || todos.Total == 0 || len(todos.Trend) == 0 {
		t.Fatalf("unexpected todo center result: %+v", todos)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/records/99", nil)
	req, audit := withAuditCapture(req)
	setAudit(req, "修改记录", "record", 99, "测试审计写入", map[string]interface{}{
		"changes": map[string]interface{}{"sell_price": map[string]interface{}{"before": 1, "after": 2}},
	})
	writeRequestAudit(req, http.StatusOK)
	if audit == nil {
		t.Fatal("audit capture missing")
	}
	auditRR := httptest.NewRecorder()
	handleOperationsAudit(auditRR, httptest.NewRequest(http.MethodGet, "/api/operations/audit?limit=10", nil))
	if auditRR.Code != http.StatusOK || !strings.Contains(auditRR.Body.String(), "测试审计写入") {
		t.Fatalf("audit response=%d %s", auditRR.Code, auditRR.Body.String())
	}
}
