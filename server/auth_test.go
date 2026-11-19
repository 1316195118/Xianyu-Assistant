package main

import (
	"strings"
	"testing"
)

func TestHashPasswordScryptAndVerify(t *testing.T) {
	h, err := hashPassword("s3cret-pass", "")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if !strings.HasPrefix(h, "scrypt$") {
		t.Fatalf("新哈希应为 scrypt 格式，实际 %q", h)
	}
	if !verifyPassword("s3cret-pass", h, "") {
		t.Error("正确密码应通过校验")
	}
	if verifyPassword("wrong-pass", h, "") {
		t.Error("错误密码不应通过")
	}
	h2, _ := hashPassword("s3cret-pass", "")
	if h == h2 {
		t.Error("随机 salt 应产生不同哈希")
	}
}

func TestVerifyLegacySHA256(t *testing.T) {
	legacy := hashPasswordLegacy("adminpw", "s1")
	if !verifyPassword("adminpw", legacy, "s1") {
		t.Error("旧格式正确密码应通过")
	}
	if verifyPassword("bad", legacy, "s1") {
		t.Error("旧格式错误密码应拒绝")
	}
	if verifyPassword("adminpw", "", "s1") {
		t.Error("空哈希应拒绝")
	}
}

func TestLoginUpgradesLegacyHash(t *testing.T) {
	h := newExtrasEnv(t)
	setSetting("auth_salt", "s1")
	setSetting("auth_hash", hashPasswordLegacy("adminpw", "s1"))
	resetLoginLimiter()

	w := doExtrasReq(h, "POST", "/api/auth/login", map[string]string{"password": "adminpw"})
	if w.Code != 200 {
		t.Fatalf("旧密码登录失败: %d %s", w.Code, w.Body.String())
	}
	if !strings.HasPrefix(getSetting("auth_hash"), "scrypt$") {
		t.Errorf("登录后应透明升级为 scrypt，实际 %q", getSetting("auth_hash"))
	}

	resetLoginLimiter()
	w2 := doExtrasReq(h, "POST", "/api/auth/login", map[string]string{"password": "adminpw"})
	if w2.Code != 200 {
		t.Fatalf("升级后再次登录失败: %d %s", w2.Code, w2.Body.String())
	}
}

func TestSetPasswordMinLength(t *testing.T) {
	h := newExtrasEnv(t)
	setSetting("auth_hash", "")
	resetLoginLimiter()

	w := doExtrasReq(h, "PUT", "/api/auth/password", map[string]interface{}{"password": "1234"})
	if w.Code != 400 {
		t.Errorf("4 位密码应 400，实际 %d %s", w.Code, w.Body.String())
	}
	w2 := doExtrasReq(h, "PUT", "/api/auth/password", map[string]interface{}{"password": "12345678"})
	if w2.Code != 200 {
		t.Errorf("8 位密码应 200，实际 %d %s", w2.Code, w2.Body.String())
	}
	if !strings.HasPrefix(getSetting("auth_hash"), "scrypt$") {
		t.Errorf("设置的密码应为 scrypt，实际 %q", getSetting("auth_hash"))
	}
}

// 只读密码不能脱离管理密码单独存在：没有管理密码时，任何请求本就畅通，
// 只读限制没有意义，还会让管理员误以为已经收紧了权限。
func TestReadonlyPasswordRequiresGlobalPassword(t *testing.T) {
	h := newExtrasEnv(t)
	setSetting("auth_hash", "")
	setSetting("auth_salt", "")
	clearReadonlyPassword()

	w := doExtrasReq(h, "PUT", "/api/settings", map[string]interface{}{"readonly_password": "readonly1"})
	if w.Code != 400 {
		t.Fatalf("未设管理密码时设置只读密码应 400，实际 %d %s", w.Code, w.Body.String())
	}
	if getSetting("readonly_password_hash") != "" {
		t.Error("被拒绝时不应写入只读密码")
	}

	if _, err := hashPassword("adminpass", ""); err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	wLogin := doExtrasReq(h, "PUT", "/api/auth/password", map[string]interface{}{"password": "adminpass"})
	if wLogin.Code != 200 {
		t.Fatalf("设置管理密码失败: %d %s", wLogin.Code, wLogin.Body.String())
	}
	// 设置成功会下发新会话；后续写操作要带上，否则按未登录 401
	cookies := wLogin.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("设置管理密码后应下发会话 Cookie")
	}
	w2 := doExtrasReq(h, "PUT", "/api/settings", map[string]interface{}{"readonly_password": "readonly1"}, cookies...)
	if w2.Code != 200 {
		t.Fatalf("已有管理密码时设置只读密码应 200，实际 %d %s", w2.Code, w2.Body.String())
	}
	if getSetting("readonly_password_hash") == "" {
		t.Error("应写入只读密码哈希")
	}

	// 关闭管理密码时，只读密码一并清掉，避免留下一个还能登录的旧入口
	if w := doExtrasReq(h, "PUT", "/api/auth/password", map[string]interface{}{"old": "adminpass", "disable": true}, cookies...); w.Code != 200 {
		t.Fatalf("关闭管理密码失败: %d %s", w.Code, w.Body.String())
	}
	if getSetting("readonly_password_hash") != "" {
		t.Error("关闭管理密码后应清除只读密码")
	}
}
