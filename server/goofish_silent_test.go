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
