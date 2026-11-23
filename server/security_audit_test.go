package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func sessionCookieFor(role string, shop int64) *http.Cookie {
	return &http.Cookie{
		Name:  authCookie,
		Value: signTokenShop(time.Now().Add(time.Hour).Unix(), role, shop),
	}
}

func globalAdminCookie() *http.Cookie {
	return &http.Cookie{
		Name:  authCookie,
		Value: signToken(time.Now().Add(time.Hour).Unix()),
	}
}

func ensureSecondShop(t *testing.T) {
	t.Helper()
	ts := nowStr()
	if _, err := db.Exec(`INSERT OR IGNORE INTO shops
        (id,name,color,sort,note,archived_at,created_at,updated_at)
        VALUES(2,'第二家店','#00aa88',2,'','',?,?)`, ts, ts); err != nil {
		t.Fatalf("创建第二家店失败: %v", err)
	}
}

func setShopPassword(t *testing.T, shop int64, password string) {
	t.Helper()
	hash, err := hashPassword(password, "")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if _, err := db.Exec("UPDATE shops SET password_hash=? WHERE id=?", hash, shop); err != nil {
		t.Fatalf("设置店铺密码: %v", err)
	}
}

func insertShopItem(t *testing.T, shop int64, product string) int64 {
	t.Helper()
	id, err := insertItem(&Item{Product: product, Source: "闲鱼"}, shop, nowStr())
	if err != nil {
		t.Fatalf("insertItem(%d): %v", shop, err)
	}
	return id
}

func TestPasswordBootstrapCannotBypassShopPassword(t *testing.T) {
	h := newExtrasEnv(t)
	setShopPassword(t, defaultShopID, "shop-pass")

	w := doExtrasReq(h, "PUT", "/api/auth/password", map[string]interface{}{"password": "attacker-pass"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("匿名设置全局密码应 401，实际 %d %s", w.Code, w.Body.String())
	}

	cookie := sessionCookieFor("admin", defaultShopID)
	w = doExtrasReq(h, "PUT", "/api/auth/password", map[string]interface{}{"password": "attacker-pass"}, cookie)
	if w.Code != http.StatusForbidden {
		t.Fatalf("店铺会话设置全局密码应 403，实际 %d %s", w.Code, w.Body.String())
	}
	if getSetting("auth_hash") != "" {
		t.Fatal("被拒绝的请求不应写入全局密码")
	}
}

func TestShopSessionCannotReadAllShops(t *testing.T) {
	h := newExtrasEnv(t)
	ensureSecondShop(t)
	setShopPassword(t, defaultShopID, "shop-pass")
	insertShopItem(t, defaultShopID, "本店商品")
	insertShopItem(t, 2, "别店商品")
	cookie := sessionCookieFor("admin", defaultShopID)

	w := doExtrasReq(h, "GET", "/api/records?all=1", nil, cookie)
	if w.Code != http.StatusForbidden {
		t.Fatalf("店铺会话 all=1 应 403，实际 %d %s", w.Code, w.Body.String())
	}

	w = doExtrasReq(h, "GET", "/api/records", nil, cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("本店查询失败: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "别店商品") || !strings.Contains(w.Body.String(), "本店商品") {
		t.Fatalf("店铺会话应只看到本店数据: %s", w.Body.String())
	}

	w = doExtrasReq(h, "GET", "/api/records?all=1", nil, globalAdminCookie())
	if w.Code != http.StatusOK {
		t.Fatalf("全局管理员 all=1 失败: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "本店商品") || !strings.Contains(w.Body.String(), "别店商品") {
		t.Fatalf("全局管理员应看到两家店数据: %s", w.Body.String())
	}
}

func TestBatchUpdateCannotCrossShop(t *testing.T) {
	h := newExtrasEnv(t)
	ensureSecondShop(t)
	setShopPassword(t, defaultShopID, "shop-pass")
	mine := insertShopItem(t, defaultShopID, "本店商品")
	other := insertShopItem(t, 2, "别店商品")
	cookie := sessionCookieFor("admin", defaultShopID)

	w := doExtrasReq(h, "POST", "/api/records/batch-update", map[string]interface{}{
		"ids":    []int64{mine, other},
		"fields": map[string]string{"note": "批量备注"},
	}, cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("批量更新失败: %d %s", w.Code, w.Body.String())
	}
	if note := itemNote(t, mine); note != "批量备注" {
		t.Fatalf("本店记录应更新，实际备注=%q", note)
	}
	if note := itemNote(t, other); note != "" {
		t.Fatalf("别店记录被跨店修改，备注=%q", note)
	}
}

func itemNote(t *testing.T, id int64) string {
	t.Helper()
	var note string
	if err := db.QueryRow("SELECT note FROM items WHERE id=?", id).Scan(&note); err != nil {
		t.Fatalf("读取记录备注: %v", err)
	}
	return note
}

func TestGroupDeleteCannotClearOtherShopMembers(t *testing.T) {
	h := newExtrasEnv(t)
	ensureSecondShop(t)
	setShopPassword(t, defaultShopID, "shop-pass")
	itemID := insertShopItem(t, 2, "别店商品")
	ts := nowStr()
	res, err := db.Exec(`INSERT INTO link_groups(shop_id,title,note,settled_at,created_at,updated_at)
        VALUES(2,'别店联单','','',?,?)`, ts, ts)
	if err != nil {
		t.Fatalf("创建联单: %v", err)
	}
	groupID, _ := res.LastInsertId()
	if _, err := db.Exec("INSERT INTO group_items(group_id,item_id) VALUES(?,?)", groupID, itemID); err != nil {
		t.Fatalf("关联联单成员: %v", err)
	}

	w := doExtrasReq(h, "DELETE", "/api/groups/"+strconvInt64(groupID), nil, sessionCookieFor("admin", defaultShopID))
	if w.Code != http.StatusOK {
		t.Fatalf("跨店删除请求不应触发 500: %d %s", w.Code, w.Body.String())
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM group_items WHERE group_id=? AND item_id=?", groupID, itemID).Scan(&n)
	if n != 1 {
		t.Fatal("别店联单成员被跨店删除")
	}
	db.QueryRow("SELECT COUNT(*) FROM link_groups WHERE id=? AND shop_id=2", groupID).Scan(&n)
	if n != 1 {
		t.Fatal("别店联单被跨店删除")
	}
}

func strconvInt64(v int64) string {
	return strconv.FormatInt(v, 10)
}

func TestExportOnlyContainsCurrentShopMonthlyTarget(t *testing.T) {
	h := newExtrasEnv(t)
	_ = h
	ensureSecondShop(t)
	for key, value := range map[string]string{
		"goofish_cookie@1":               "cookie-one",
		"goofish_cookie@2":               "cookie-two",
		"ocr_api_key":                    "sk-secret",
		"update_proxy":                   "http://proxy-user:pass@example.test",
		"monthly_target@2026-11@1":       "1000",
		"monthly_target@2026-11@2":       "2000",
		"monthly_target@2026-12@1":       "3000",
		"monthly_target@2026-11@1-extra": "4000",
	} {
		if err := setSetting(key, value); err != nil {
			t.Fatalf("setSetting(%s): %v", key, err)
		}
	}
	bundle, err := collectExport(1)
	if err != nil {
		t.Fatalf("collectExport: %v", err)
	}
	if len(bundle.Settings) != 2 {
		t.Fatalf("导出设置应只含本店月度目标，实际 %+v", bundle.Settings)
	}
	if bundle.Settings["monthly_target@2026-11@1"] != "1000" || bundle.Settings["monthly_target@2026-12@1"] != "3000" {
		t.Fatalf("本店月度目标导出不完整: %+v", bundle.Settings)
	}
	for _, forbidden := range []string{"goofish_cookie@1", "goofish_cookie@2", "ocr_api_key", "update_proxy", "monthly_target@2026-11@2"} {
		if _, ok := bundle.Settings[forbidden]; ok {
			t.Fatalf("导出泄露禁止设置 %s", forbidden)
		}
	}
}

func TestShopSessionCannotChangeGlobalSettingsOrUpdates(t *testing.T) {
	h := newExtrasEnv(t)
	setShopPassword(t, defaultShopID, "shop-pass")
	cookie := sessionCookieFor("admin", defaultShopID)
	for _, tc := range []struct {
		method string
		path   string
		body   interface{}
	}{
		{"GET", "/api/settings", nil},
		{"PUT", "/api/settings", map[string]string{"ocr_api_base": "http://attacker.test/v1"}},
		{"POST", "/api/settings/test", nil},
		{"GET", "/api/update/check", nil},
	} {
		w := doExtrasReq(h, tc.method, tc.path, tc.body, cookie)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s 应 403，实际 %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

func TestImportSettingsUsesCurrentShopWhitelist(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("BACKUP_DIR", tmp)
	h := newExtrasEnv(t)
	ensureSecondShop(t)
	setShopPassword(t, defaultShopID, "shop-pass")
	_ = setSetting("goofish_cookie@2", "keep-cookie")
	bundle := exportBundle{
		Version: "1",
		Items:   []Item{},
		Tags:    []string{"不应由店铺会话写入的标签"},
		Settings: map[string]string{
			"ocr_api_base":                  "http://attacker.test/v1",
			"goofish_cookie@2":              "replace-cookie",
			"monthly_target@2026-11@1":      "1200",
			"monthly_target@2026-11@2":      "9999",
			"monthly_target@2026-11@1-junk": "7777",
		},
	}
	w := doExtrasReq(h, "POST", "/api/import", map[string]interface{}{
		"mode": "merge",
		"data": bundle,
	}, sessionCookieFor("admin", defaultShopID))
	if w.Code != http.StatusOK {
		t.Fatalf("店铺会话导入失败: %d %s", w.Code, w.Body.String())
	}
	if got := getSetting("ocr_api_base"); got != "" {
		t.Fatalf("导入写入了全局 OCR 配置: %q", got)
	}
	if got := getSetting("goofish_cookie@2"); got != "keep-cookie" {
		t.Fatalf("导入覆盖了别店 Cookie: %q", got)
	}
	if got := getSetting("monthly_target@2026-11@1"); got != "1200" {
		t.Fatalf("本店月度目标未导入: %q", got)
	}
	if got := getSetting("monthly_target@2026-11@2"); got != "" {
		t.Fatalf("别店月度目标被写入: %q", got)
	}
	if got := getSetting("monthly_target@2026-11@1-junk"); got != "" {
		t.Fatalf("伪造月度目标键被写入: %q", got)
	}
	var tagCount int
	db.QueryRow("SELECT COUNT(*) FROM ship_tags WHERE name=?", "不应由店铺会话写入的标签").Scan(&tagCount)
	if tagCount != 0 {
		t.Fatal("店铺会话通过导入修改了全局运费标签")
	}
}

func TestReplaceImportClearsAndUndoRestoresCurrentShopGroups(t *testing.T) {
	t.Setenv("BACKUP_DIR", t.TempDir())
	h := newExtrasEnv(t)
	ensureSecondShop(t)
	item1 := insertShopItem(t, 1, "本店旧记录")
	item2 := insertShopItem(t, 2, "别店记录")
	ts := nowStr()
	group1Res, _ := db.Exec(`INSERT INTO link_groups(shop_id,title,note,settled_at,created_at,updated_at)
        VALUES(1,'本店旧联单','','',?,?)`, ts, ts)
	group1, _ := group1Res.LastInsertId()
	group2Res, _ := db.Exec(`INSERT INTO link_groups(shop_id,title,note,settled_at,created_at,updated_at)
        VALUES(2,'别店联单','','',?,?)`, ts, ts)
	group2, _ := group2Res.LastInsertId()
	db.Exec("INSERT INTO group_items(group_id,item_id) VALUES(?,?)", group1, item1)
	db.Exec("INSERT INTO group_items(group_id,item_id) VALUES(?,?)", group2, item2)
	db.Exec(`INSERT INTO group_externals(group_id,shop_id,kind,product,amount,quantity,counterparty,deal_time,note,source,created_at,updated_at)
        VALUES(?,1,'sell','本店线下成交',88,1,'买家','2026-11-01','','自采',?,?)`, group1, ts, ts)
	db.Exec(`INSERT INTO group_externals(group_id,shop_id,kind,product,amount,quantity,counterparty,deal_time,note,source,created_at,updated_at)
        VALUES(?,2,'sell','别店线下成交',66,1,'买家','2026-11-01','','自采',?,?)`, group2, ts, ts)

	w := doExtrasReq(h, "POST", "/api/import", map[string]interface{}{
		"mode": "replace",
		"data": exportBundle{Version: "1", Items: []Item{}, Tags: []string{}, Settings: map[string]string{}},
	}, globalAdminCookie())
	if w.Code != http.StatusOK {
		t.Fatalf("覆盖导入失败: %d %s", w.Code, w.Body.String())
	}
	var out map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	token, _ := out["undo_token"].(string)
	if token == "" {
		t.Fatal("覆盖导入应返回撤销令牌")
	}
	for _, q := range []struct {
		sql  string
		args []interface{}
		want int
	}{
		{"SELECT COUNT(*) FROM items WHERE shop_id=1", nil, 0},
		{"SELECT COUNT(*) FROM link_groups WHERE shop_id=1", nil, 0},
		{"SELECT COUNT(*) FROM group_externals WHERE shop_id=1", nil, 0},
		{"SELECT COUNT(*) FROM items WHERE shop_id=2", nil, 1},
		{"SELECT COUNT(*) FROM link_groups WHERE shop_id=2", nil, 1},
		{"SELECT COUNT(*) FROM group_items WHERE group_id=?", []interface{}{group2}, 1},
	} {
		var n int
		if err := db.QueryRow(q.sql, q.args...).Scan(&n); err != nil {
			t.Fatalf("查询覆盖结果: %v", err)
		}
		if n != q.want {
			t.Fatalf("%s = %d，期望 %d", q.sql, n, q.want)
		}
	}

	w = doExtrasReq(h, "POST", "/api/undo", map[string]string{"token": token}, globalAdminCookie())
	if w.Code != http.StatusOK {
		t.Fatalf("撤销覆盖导入失败: %d %s", w.Code, w.Body.String())
	}
	for _, q := range []struct {
		sql  string
		args []interface{}
		want int
	}{
		{"SELECT COUNT(*) FROM items WHERE id=? AND shop_id=1", []interface{}{item1}, 1},
		{"SELECT COUNT(*) FROM link_groups WHERE id=? AND shop_id=1", []interface{}{group1}, 1},
		{"SELECT COUNT(*) FROM group_items WHERE group_id=?", []interface{}{group1}, 1},
		{"SELECT COUNT(*) FROM group_externals WHERE group_id=? AND shop_id=1", []interface{}{group1}, 1},
	} {
		var n int
		if err := db.QueryRow(q.sql, q.args...).Scan(&n); err != nil {
			t.Fatalf("查询撤销结果: %v", err)
		}
		if n != q.want {
			t.Fatalf("%s = %d，期望 %d", q.sql, n, q.want)
		}
	}
}

func TestLoginBodyLimitReturns413(t *testing.T) {
	h := newExtrasEnv(t)
	hash, _ := hashPassword("admin-pass", "")
	_ = setSetting("auth_hash", hash)
	_ = setSetting("auth_salt", "")
	resetLoginLimiter()
	body := []byte(`{"password":"` + strings.Repeat("x", maxTinyJSONBody) + `"}`)
	req := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大登录请求应 413，实际 %d %s", rec.Code, rec.Body.String())
	}
}

func TestTrustedProxyHeadersOnlyUsedFromConfiguredCIDR(t *testing.T) {
	t.Setenv("TRUSTED_PROXY_CIDRS", "10.0.0.0/24")
	resetTrustedProxyCacheForTest()
	t.Cleanup(resetTrustedProxyCacheForTest)

	direct := httptest.NewRequest("GET", "/", nil)
	direct.RemoteAddr = "192.0.2.9:1234"
	direct.Header.Set("X-Forwarded-For", "203.0.113.77")
	if got := remoteIP(direct); got != "192.0.2.9" {
		t.Fatalf("不可信来源伪造 XFF 被采用: %s", got)
	}

	proxied := httptest.NewRequest("GET", "/", nil)
	proxied.RemoteAddr = "10.0.0.2:1234"
	proxied.Header.Set("X-Forwarded-For", "198.51.100.20, 10.0.0.3")
	if got := remoteIP(proxied); got != "198.51.100.20" {
		t.Fatalf("可信代理后的真实客户端解析错误: %s", got)
	}
}

func TestCSPDisallowsInlineScripts(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, req)
	csp := rec.Header().Get("Content-Security-Policy")
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("CSP 仍允许内联脚本: %s", csp)
	}
	if !strings.Contains(csp, "script-src 'self' 'wasm-unsafe-eval'") {
		t.Fatalf("CSP 脚本策略不完整: %s", csp)
	}
	if !strings.Contains(csp, "style-src 'self' 'unsafe-inline'") {
		t.Fatalf("Vue 动态样式所需 style-src 不应被误删: %s", csp)
	}
}
