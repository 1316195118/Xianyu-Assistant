package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBridgeOnlineWindow(t *testing.T) {
	now := time.Now()
	if !gfBridgeOnline(now.Add(-2 * time.Minute).Format("2006-01-02 15:04:05")) {
		t.Fatal("两分钟前的心跳应视为在线")
	}
	if gfBridgeOnline(now.Add(-5 * time.Minute).Format("2006-01-02 15:04:05")) {
		t.Fatal("五分钟前的心跳应视为离线")
	}
	if gfBridgeOnline("") {
		t.Fatal("没有心跳记录时不应视为在线")
	}
}

func TestSyncRetryStopsTightLoopAfterThreeFailures(t *testing.T) {
	newExtrasEnv(t)
	const shop int64 = 82
	for i := 0; i < 3; i++ {
		gfSyncRetryOrFail(shop, "Cookie 已失效")
	}
	if gfGet(gfKeyDead, shop) != "1" {
		t.Fatal("连续失败三次后应标记失效")
	}
	raw := gfGet(gfKeyRetryAt, shop)
	next, err := time.ParseInLocation("2006-01-02 15:04:05", raw, time.Local)
	if err != nil {
		t.Fatalf("失败后应保留低频检查时间: %q", raw)
	}
	delay := time.Until(next)
	if delay < 5*time.Hour+55*time.Minute || delay > 6*time.Hour+5*time.Minute {
		t.Fatalf("第三次失败后应改为约 6 小时后检查，实际 %v", delay)
	}
}

func TestBridgeTokenOnlyAuthorizesOwnShop(t *testing.T) {
	h := newExtrasEnv(t)
	setSetting("auth_hash", "scrypt$test")
	admin := &http.Cookie{
		Name:  authCookie,
		Value: signToken(time.Now().Add(time.Hour).Unix()),
	}

	w := doExtrasReq(h, "POST", "/api/goofish/bridge/token", nil, admin)
	if w.Code != 200 {
		t.Fatalf("生成配对码失败: %d %s", w.Code, w.Body.String())
	}
	var generated struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &generated); err != nil || generated.Token == "" {
		t.Fatalf("配对码响应异常: %v %s", err, w.Body.String())
	}
	if stored := gfGet(gfKeyBridgeTokenHash, defaultShopID); stored == "" || stored == generated.Token {
		t.Fatal("数据库应只保存配对码哈希")
	}

	req := httptest.NewRequest("GET", "/api/goofish/status", nil)
	req.Header.Set(shopHeader, "1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("启用密码后无会话、无配对码应 401，实际 %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/api/goofish/status", nil)
	req.Header.Set(shopHeader, "1")
	req.Header.Set("X-Bridge-Token", "wrong-token")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错误配对码应 401，实际 %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/api/goofish/status", nil)
	req.Header.Set(shopHeader, "1")
	req.Header.Set("X-Bridge-Token", generated.Token)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("正确配对码应能读取状态，实际 %d %s", rec.Code, rec.Body.String())
	}
	var status map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &status)
	if status["bridge_token_configured"] != true {
		t.Fatalf("状态应显示已配置配对码: %v", status)
	}

	ts := nowStr()
	if _, err := db.Exec(`INSERT INTO shops(id,name,color,sort,note,archived_at,created_at,updated_at)
		VALUES(2,'第二家店','#000000',2,'','',?,?)`, ts, ts); err != nil {
		t.Fatalf("创建第二家店: %v", err)
	}
	req = httptest.NewRequest("GET", "/api/goofish/status", nil)
	req.Header.Set(shopHeader, "2")
	req.Header.Set("X-Bridge-Token", generated.Token)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("店铺 1 的配对码不能访问店铺 2，实际 %d", rec.Code)
	}

	req = httptest.NewRequest("POST", "/api/goofish/bridge/heartbeat", bytes.NewBufferString("{}"))
	req.Header.Set(shopHeader, "1")
	req.Header.Set("X-Bridge-Token", generated.Token)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("正确配对码应能发送心跳，实际 %d %s", rec.Code, rec.Body.String())
	}

	w = doExtrasReq(h, "DELETE", "/api/goofish/bridge/token", nil, admin)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "true") {
		t.Fatalf("撤销配对码失败: %d %s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest("GET", "/api/goofish/status", nil)
	req.Header.Set(shopHeader, "1")
	req.Header.Set("X-Bridge-Token", generated.Token)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("撤销后旧配对码应失效，实际 %d", rec.Code)
	}
}
