package main

// 闲鱼静默换发（silent login renewal）
//
// 背景：闲鱼网页版的「一键快速登录」= passport.goofish.com 的静默登录接口，
// 靠淘宝域的 SSO Cookie（unb/cookie2 等，寿命按月计）换发闲鱼会话。
// 服务器只要存着淘宝 SSO Cookie，就能在闲鱼会话过期后自己续期，
// 不再依赖用户重新绑定。
//
// 风控对抗要点（实验验证）：
//  1. TLS 握手必须用 Chrome 指纹（uTLS HelloChrome_120），否则被拦；
//  2. 协议走 HTTP/2（服务器端强开 h2，用 http2.Transport）；
//  3. 接口是 POST，参数集必须完整（逆向自 goofish-auto-login/plugin.js）；
//  4. 请求 Cookie 要合并闲鱼域 + 淘宝域的 Cookie，uid 取淘宝的 unb。
//
// 成功标志（与插件判定一致）：resultCode==100 && processFinished==true。

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	tlsx "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// gfSilentHTTPClient 用 Chrome TLS 指纹 + HTTP/2 的客户端（懒加载单例）。
var gfSilentTr *http2.Transport

func gfSilentClient() *http.Client {
	if gfSilentTr == nil {
		gfSilentTr = &http2.Transport{
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				raw, err := (&net.Dialer{Timeout: 15e9}).DialContext(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				uconn := tlsx.UClient(raw, &tlsx.Config{ServerName: "passport.goofish.com"}, tlsx.HelloChrome_120)
				if err := uconn.Handshake(); err != nil {
					raw.Close()
					return nil, err
				}
				return uconn, nil
			},
		}
	}
	return &http.Client{Transport: gfSilentTr, Timeout: 25e9}
}

// gfCookieGet 从 cookie 串里取某个键的值。
func gfCookieGet(cookieStr, name string) string {
	for _, kv := range strings.Split(cookieStr, "; ") {
		if i := strings.Index(kv, "="); i > 0 && kv[:i] == name {
			return kv[i+1:]
		}
	}
	return ""
}

// gfParseCookieKV / gfJoinCookieKV：Cookie 串与 map 互转。
// 存的时候按 key 排序，保证每次写回的字符串稳定，方便比对。
func gfParseCookieKV(raw string) map[string]string {
	m := map[string]string{}
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.IndexByte(part, '='); i > 0 {
			m[part[:i]] = part[i+1:]
		}
	}
	return m
}
func gfJoinCookieKV(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, "; ")
}

// gfMergeRenewCookies 把静默换发响应里的 Set-Cookie 合并回本地凭证并落库。
// 这是整套自动续期的关键一步：只看 body 里的 resultCode 不算续期，
// 必须把服务端新发的 cookie2 / sgcookie / _m_h5_tk 等真正存下来。
// 返回实际发生变化的凭证项数（0 说明响应没带新东西）。
func gfMergeRenewCookies(shop int64, resp *http.Response) int {
	gfMap := gfParseCookieKV(gfGet(gfKeyCookie, shop))
	tbMap := gfParseCookieKV(gfGet(gfKeySSOTaobao, shop))
	updated := 0
	for _, ck := range resp.Cookies() {
		if ck.Name == "" {
			continue
		}
		domain := strings.ToLower(ck.Domain)
		target := gfMap
		if strings.Contains(domain, "taobao.com") || strings.Contains(domain, "tmall.com") {
			target = tbMap
		} else if domain != "" && !strings.Contains(domain, "goofish.com") && !strings.Contains(domain, "alibaba.com") {
			continue
		}
		// 过期 / 删除型 Cookie：从本地也删掉
		// Go 的 MaxAge=0 表示「没写 Max-Age」，<0 才是删除
		if ck.MaxAge < 0 || (!ck.Expires.IsZero() && ck.Expires.Before(time.Now())) {
			if _, ok := target[ck.Name]; ok {
				delete(target, ck.Name)
				updated++
			}
			continue
		}
		if ck.Value == "" || target[ck.Name] == ck.Value {
			continue
		}
		target[ck.Name] = ck.Value
		updated++
	}
	if updated == 0 {
		return 0
	}
	_ = gfSet(gfKeyCookie, shop, gfJoinCookieKV(gfMap))
	_ = gfSet(gfKeySSOTaobao, shop, gfJoinCookieKV(tbMap))
	return updated
}

// gfSilentRenew 用淘宝 SSO Cookie 静默换发闲鱼会话。
// 返回 (换发成功?, 错误)。成功不代表同步一定可用，但失败一定不值得再试。
func gfSilentRenew(shop int64) (bool, error) {
	gfCookie := strings.TrimSpace(gfGet(gfKeyCookie, shop))
	tbCookie := strings.TrimSpace(gfGet(gfKeySSOTaobao, shop))
	if gfCookie == "" || tbCookie == "" {
		return false, fmt.Errorf("缺少闲鱼或淘宝 SSO Cookie，无法换发")
	}

	// uid：淘宝 SSO 的 unb（插件 JS 里的首选序列：unb/aui/cnaui/munb）
	uid := gfCookieGet(tbCookie, "unb")
	if uid == "" {
		uid = gfCookieGet(tbCookie, "aui")
	}
	if uid == "" {
		uid = gfCookieGet(tbCookie, "cnaui")
	}
	if uid == "" {
		uid = gfCookieGet(tbCookie, "munb")
	}

	pageURL := "https://www.goofish.com/"
	q := url.Values{}
	q.Set("pid", "member-login")
	q.Set("uid", uid)
	q.Set("env", "prod")
	q.Set("ua", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	q.Set("origin_url", pageURL)
	q.Set("url", pageURL)
	q.Set("p11", "post")
	q.Set("c1", "xianyu_sdkSilent")
	q.Set("c2", "xianyu")
	q.Set("c3", "lib_autoLogin")
	q.Set("c8", "false")
	q.Set("appName", "xianyu")
	q.Set("appEntrance", "xianyu_sdkSilent")
	q.Set("fromSite", "0")
	q.Set("skipSessionFilter", "true")
	q.Set("c2r", "true")
	q.Set("documentReferer", pageURL)

	req, err := http.NewRequest("POST",
		"https://passport.goofish.com/newlogin/silentHasLogin.do?"+q.Encode(), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Referer", pageURL)
	req.Header.Set("Origin", "https://www.goofish.com")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", gfCookie+"; "+tbCookie)

	resp, err := gfSilentClient().Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	// 先落库新 Cookie，再看业务结果：即使这次业务码不理想，
	// 服务端新发的会话凭证也应该留下，下次请求就能用上。
	updated := gfMergeRenewCookies(shop, resp)
	if updated > 0 {
		fmt.Printf("[闲鱼换发] 店铺 %d 响应带回 %d 项新凭证，已写回本地\n", shop, updated)
	} else {
		fmt.Printf("[闲鱼换发] 店铺 %d 响应未带回新凭证，继续用已存会话\n", shop)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))

	var out struct {
		Content struct {
			Data struct {
				ResultCode      int    `json:"resultCode"`
				ProcessFinished bool   `json:"processFinished"`
				TitleMsg        string `json:"titleMsg"`
				ErrorCode       string `json:"errorCode"`
			} `json:"data"`
		} `json:"content"`
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return false, fmt.Errorf("响应解析失败: %v", err)
	}
	if out.Content.Data.ProcessFinished && out.Content.Data.ResultCode == 100 {
		return true, nil
	}
	return false, fmt.Errorf("换发未完成: %s (%s)", out.Content.Data.TitleMsg, out.Content.Data.ErrorCode)
}

// gfRenewBeforeSync 同步前的统一入口：
// 先静默换发（有淘宝 SSO Cookie 时），换发成功就继续；失败只在会话确属过期时标记失效。
// 返回值：true=换发成功或不需要换发（已有有效会话），false=需要重新绑定。
func gfRenewBeforeSync(shop int64) bool {
	// 用户关掉「自动续期」开关时不做换发，按老逻辑走
	if !gfKeepAliveEnabled() {
		return true
	}
	if gfGet(gfKeySSOTaobao, shop) == "" {
		return true // 没存过 SSO，按老逻辑走
	}
	ok, err := gfSilentRenew(shop)
	if err != nil {
		fmt.Printf("[闲鱼换发] 店铺 %d 静默换发失败: %v\n", shop, err)
		return false
	}
	if ok {
		fmt.Printf("[闲鱼换发] 店铺 %d 静默换发成功\n", shop)
		_ = gfSet(gfKeyCheck, shop, time.Now().Format("2006-01-02 15:04:05"))
	}
	return ok
}
