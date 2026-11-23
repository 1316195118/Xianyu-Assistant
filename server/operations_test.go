package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func setupOperationsTest(t *testing.T) {
	t.Helper()
	os.Setenv("DB_PATH", t.TempDir()+"/operations.db")
	t.Cleanup(func() {
		os.Unsetenv("DB_PATH")
		if db != nil {
			db.Close()
		}
	})
	if err := initDB(); err != nil {
		t.Fatalf("initDB: %v", err)
	}
}

func TestOperationsHealthFindsActionableIssues(t *testing.T) {
	setupOperationsTest(t)
	_, err := insertItem(&Item{
		Product: "未挂成本测试", SellPrice: 100, SellOrderNo: "A100",
		SellTime: "2026-11-01 10:00:00",
	}, defaultShopID, nowStr())
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO items(shop_id,product,product_norm,sell_price,sell_order_no,sell_time,created_at,updated_at)
        VALUES(?,?,?,?,?,?,?,?)`, defaultShopID, "重复订单测试", normProduct("重复订单测试"), 80, "A100", "2026-11-02 10:00:00", nowStr(), nowStr())
	if err != nil {
		t.Fatal(err)
	}
	_, err = insertItem(&Item{
		Product: "寄卖缺归属", Source: "寄卖", SellPrice: 50, SellTime: "2026-11-03 10:00:00",
	}, defaultShopID, nowStr())
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/operations/health", nil)
	rr := httptest.NewRecorder()
	handleOperationsHealth(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got opsHealth
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, issue := range got.Issues {
		keys[issue.Key] = true
	}
	for _, want := range []string{"unlinked_cost", "fee_pending", "owner_missing", "duplicate_order"} {
		if !keys[want] {
			t.Errorf("missing issue %q: %+v", want, keys)
		}
	}
	if got.Score >= 100 {
		t.Errorf("health score=%d, want below 100", got.Score)
	}
}

func TestOperationsTransportMatchesOrderAndFillsShipping(t *testing.T) {
	setupOperationsTest(t)
	id, err := insertItem(&Item{
		Product: "物流测试", SellPrice: 100, SellFee: 2, SellNet: 98,
		SellOrderNo: "SY202611230001", SellTime: "2026-11-23 10:00:00",
	}, defaultShopID, nowStr())
	if err != nil {
		t.Fatal(err)
	}
	body := `{"apply":true,"source":"顺丰账单","rows":[{"order_no":"SY202611230001","tracking_no":"SF123","carrier":"顺丰","amount":12,"occurred_at":"2026-11-23 10:01:00"}]}`
	req := httptest.NewRequest("POST", "/api/operations/transport/import", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	handleOperationsTransportImport(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["applied"].(float64) != 1 || got["matched"].(float64) != 1 {
		t.Fatalf("unexpected import result: %+v", got)
	}
	it, err := getItem(id, defaultShopID)
	if err != nil {
		t.Fatal(err)
	}
	if it.SellShipping != 12 || it.SellNet != 86 {
		t.Errorf("shipping=%v net=%v, want 12/86", it.SellShipping, it.SellNet)
	}
	var status string
	db.QueryRow("SELECT status FROM transport_charges WHERE shop_id=? AND order_no=?", defaultShopID, "SY202611230001").Scan(&status)
	if status != "matched" {
		t.Errorf("transport status=%q, want matched", status)
	}
}

func TestOperationsFeeReconcileUpdatesFeesWithoutDroppingShipping(t *testing.T) {
	setupOperationsTest(t)
	id, err := insertItem(&Item{
		Product: "服务费测试", SellPrice: 100, SellFee: 2, SellShipping: 12, SellNet: 86,
		SellOrderNo: "FEE202611230001", SellTime: "2026-11-23 11:00:00",
	}, defaultShopID, nowStr())
	if err != nil {
		t.Fatal(err)
	}
	actual, wuyou := 3.0, 1.0
	payload := opsFeeReconcileReq{
		ItemID: id, ActualFee: &actual, WuyouFee: &wuyou,
		Acknowledge: true, Note: "支付宝实扣",
	}
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/api/operations/fees/reconcile", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	handleOperationsFeeReconcile(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	it, err := getItem(id, defaultShopID)
	if err != nil {
		t.Fatal(err)
	}
	if it.SellFee != 3 || it.SellWuyouFee != 1 || it.SellShipping != 12 || it.SellNet != 84 {
		t.Errorf("fee=%v wuyou=%v shipping=%v net=%v, want 3/1/12/84",
			it.SellFee, it.SellWuyouFee, it.SellShipping, it.SellNet)
	}
	var at string
	db.QueryRow("SELECT reconciled_at FROM fee_reconciliations WHERE shop_id=? AND item_id=?", defaultShopID, id).Scan(&at)
	if at == "" {
		t.Error("reconciliation was not recorded")
	}
}

func TestOperationsMultiShopRejectsShopBoundSession(t *testing.T) {
	setupOperationsTest(t)
	req := httptest.NewRequest("GET", "/api/operations/multi-shop", nil)
	rr := httptest.NewRecorder()
	// 无密码部署是全局管理员，先只验证未绑店铺的全局模式可读。
	handleOperationsMultiShop(rr, req)
	if rr.Code != 200 {
		t.Fatalf("global status=%d body=%s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest("GET", "/api/operations/multi-shop", nil)
	req.AddCookie(sessionCookieFor("admin", defaultShopID))
	rr = httptest.NewRecorder()
	handleOperationsMultiShop(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("shop-bound status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestOperationsOwnerStatementHonorsKind(t *testing.T) {
	setupOperationsTest(t)
	_, err := insertItem(&Item{
		Product: "代采测试", Kind: "purchase", Source: "代采", Owner: "扬亦凡",
		BuyPrice: 1200, BuyShipping: 25, BuyTime: "2026-11-10 10:00:00",
	}, defaultShopID, nowStr())
	if err != nil {
		t.Fatal(err)
	}
	_, err = insertItem(&Item{
		Product: "寄卖测试", Kind: "sale", Source: "寄卖", Owner: "扬亦凡",
		SellPrice: 2000, SellFee: 40, SellTime: "2026-11-11 10:00:00",
	}, defaultShopID, nowStr())
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/operations/owners/%E6%89%AC%E4%BA%A6%E5%87%A1/statement?kind=buy", nil)
	rr := httptest.NewRecorder()
	handleOperationsOwnerStatement(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"kind":"buy"`) ||
		!strings.Contains(rr.Body.String(), `"buy_amount":1225`) ||
		strings.Contains(rr.Body.String(), "寄卖测试") {
		t.Fatalf("代采对账单口径错误: %s", rr.Body.String())
	}
}
