package main

import (
	"net/http"
	"strings"
	"testing"
)

// 静默换发必须把响应里的 Set-Cookie 真正写回本地，
// 否则「换发成功」只是 SSO 还活着，同步时用的还是过期会话。
func TestMergeRenewCookiesPersists(t *testing.T) {
	newExtrasEnv(t)
	const shop int64 = 77
	if err := gfSet(gfKeyCookie, shop, "a=1; _m_h5_tk=OLD; cookie2=keep"); err != nil {
		t.Fatal(err)
	}
	if err := gfSet(gfKeySSOTaobao, shop, "unb=u1; cookie2=OLD2"); err != nil {
		t.Fatal(err)
	}

	h := make(http.Header)
	h.Add("Set-Cookie", "_m_h5_tk=NEW; Domain=.goofish.com; Path=/")
	h.Add("Set-Cookie", "cookie2=NEW2; Domain=.taobao.com; Path=/")
	h.Add("Set-Cookie", "junk=1; Domain=example.com; Path=/")
	n := gfMergeRenewCookies(shop, &http.Response{Header: h})
	if n != 2 {
		t.Fatalf("期望更新 2 项凭证，实际 %d", n)
	}

	gfRaw := gfGet(gfKeyCookie, shop)
	if !strings.Contains(gfRaw, "_m_h5_tk=NEW") || strings.Contains(gfRaw, "_m_h5_tk=OLD") {
		t.Errorf("闲鱼域新令牌没写回: %s", gfRaw)
	}
	if !strings.Contains(gfRaw, "cookie2=keep") {
		t.Errorf("原有凭证被误删: %s", gfRaw)
	}
	tbRaw := gfGet(gfKeySSOTaobao, shop)
	if !strings.Contains(tbRaw, "cookie2=NEW2") || strings.Contains(tbRaw, "cookie2=OLD2") {
		t.Errorf("淘宝域凭证没写回: %s", tbRaw)
	}
	if strings.Contains(gfRaw, "junk=") || strings.Contains(tbRaw, "junk=") {
		t.Error("无关域名的 Cookie 不应被保存")
	}
}

// 响应没带新 Cookie 时不能把本地凭证清空
func TestMergeRenewCookiesNoHeaderKeepsLocal(t *testing.T) {
	newExtrasEnv(t)
	const shop int64 = 78
	const raw = "a=1; b=2"
	if err := gfSet(gfKeyCookie, shop, raw); err != nil {
		t.Fatal(err)
	}
	if n := gfMergeRenewCookies(shop, &http.Response{Header: make(http.Header)}); n != 0 {
		t.Fatalf("没有 Set-Cookie 时不应有更新，实际 %d", n)
	}
	if got := gfGet(gfKeyCookie, shop); got != raw {
		t.Errorf("本地凭证被改动: %s", got)
	}
}

func TestRenewAndLoadClientUsesFreshCookie(t *testing.T) {
	newExtrasEnv(t)
	const shop int64 = 79
	if err := gfSet(gfKeyCookie, shop, "cookie2=OLD; _m_h5_tk=OLD; unb=u1"); err != nil {
		t.Fatal(err)
	}
	c, renewed := gfRenewAndLoadClient(shop, func(id int64) bool {
		_ = gfSet(gfKeyCookie, id, "cookie2=NEW; _m_h5_tk=NEW; unb=u1")
		return true
	})
	if !renewed || c == nil {
		t.Fatal("换发成功后应返回最新客户端")
	}
	if got := c.cookieVal("cookie2"); got != "NEW" {
		t.Fatalf("仍在使用旧内存会话: cookie2=%q", got)
	}
}

func TestRenewAndLoadClientReloadsEvenWhenRenewFails(t *testing.T) {
	newExtrasEnv(t)
	const shop int64 = 80
	if err := gfSet(gfKeyCookie, shop, "cookie2=OLD; _m_h5_tk=OLD"); err != nil {
		t.Fatal(err)
	}
	c, renewed := gfRenewAndLoadClient(shop, func(id int64) bool {
		// 业务码失败前响应仍可能带回新凭证，后续请求必须使用这份落库结果。
		_ = gfSet(gfKeyCookie, id, "cookie2=PARTIAL; _m_h5_tk=PARTIAL")
		return false
	})
	if renewed || c == nil {
		t.Fatal("换发失败时应返回最新客户端，交由真实同步判断")
	}
	if got := c.cookieVal("cookie2"); got != "PARTIAL" {
		t.Fatalf("换发失败后没有重新加载落库凭证: cookie2=%q", got)
	}
}

func TestCanRecoverSessionWithSavedSSO(t *testing.T) {
	newExtrasEnv(t)
	const shop int64 = 81
	if err := setSetting(gfKeepAliveKey, "1"); err != nil {
		t.Fatal(err)
	}
	if err := gfSet(gfKeySSOTaobao, shop, "unb=u1; cookie2=sso"); err != nil {
		t.Fatal(err)
	}
	gfMarkSyncError(shop, "测试失败")
	if !gfCanRecoverSession(shop) {
		t.Fatal("有淘宝 SSO 时应保留后台自愈能力")
	}
	if got := getSetting(gfKeepAliveKey); got != "1" {
		t.Fatalf("失效提示不应关闭续期开关，实际 %q", got)
	}
}
