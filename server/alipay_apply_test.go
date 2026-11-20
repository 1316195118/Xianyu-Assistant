package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAlipayApplyExactShippingOverridesOldValue(t *testing.T) {
	newExtrasEnv(t)
	const orderNo = "EXACT-SHIP-1"
	id, err := insertItem(&Item{
		Product:      "精确运费测试",
		SellOrderNo:  orderNo,
		SellPrice:    100,
		SellShipping: 10,
		SellNet:      90,
	}, defaultShopID, nowStr())
	if err != nil {
		t.Fatalf("insertItem: %v", err)
	}

	planID := "exact-ship-test"
	aliMu.Lock()
	aliLast[defaultShopID] = &aliPlan{
		ID:     planID,
		MadeAt: time.Now(),
		Rows: []aliPlanRow{{
			OrderNo: orderNo, RecID: id, Product: "精确运费测试", Kind: "ship",
			Price: 100, OldShip: 10, Shipping: 18.9, ShipExact: true,
		}},
	}
	aliMu.Unlock()
	t.Cleanup(func() {
		aliMu.Lock()
		delete(aliLast, defaultShopID)
		aliMu.Unlock()
	})

	body, _ := json.Marshal(map[string]interface{}{
		"plan_id": planID, "order_nos": []string{orderNo},
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/alipay/apply", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handleAlipayApply(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", w.Code, w.Body.String())
	}

	var shipping, net float64
	if err := db.QueryRow(`SELECT sell_shipping, sell_net FROM items WHERE id=? AND shop_id=?`,
		id, defaultShopID).Scan(&shipping, &net); err != nil {
		t.Fatal(err)
	}
	if shipping != 18.9 || net != 81.1 {
		t.Errorf("精确运费未覆盖旧值: shipping=%v net=%v", shipping, net)
	}
}
