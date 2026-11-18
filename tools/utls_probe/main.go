// utls_probe：用 Chrome 的 TLS 指纹（uTLS）请求闲鱼 passport 接口，
// 验证「服务器直连被拦」是否因为 TLS 指纹不像浏览器。
// 用法：utls_probe <cookie文件> <url>
// 输出：状态码 + 响应前 300 字节 + set-cookie 摘要（不打印 cookie 值）
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"golang.org/x/net/http2"
	tlsx "github.com/refraction-networking/utls"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("用法: utls_probe <cookie文件> <url>")
		os.Exit(2)
	}
	cookieB, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Println("读cookie失败:", err)
		os.Exit(1)
	}
	cookie := strings.TrimSpace(string(cookieB))
	target := os.Args[2]
	u, err := url.Parse(target)
	if err != nil {
		fmt.Println("URL 无效:", err)
		os.Exit(1)
	}

	// uTLS 自定义 Dial：HelloChrome_120 模拟 Chrome 120 的 ClientHello（JA3）
	// http2.Transport：TLS 由 uTLS 拨号（Chrome 指纹），协议层走 h2
	tr := &http2.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			raw, err := (&net.Dialer{Timeout: 15e9}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			uconn := tlsx.UClient(raw, &tlsx.Config{ServerName: u.Hostname()}, tlsx.HelloChrome_120)
			if err := uconn.Handshake(); err != nil {
				raw.Close()
				return nil, err
			}
			return uconn, nil
		},
	}
	// silentHasLogin 需要完整参数集（从插件 JS 逆向）：
	// pid/uid/ua/origin_url/url/p11/c1/c2/c3/c8 + documentReferer
	cookieMap := map[string]string{}
	for _, kv := range strings.Split(cookie, "; ") {
		if i := strings.Index(kv, "="); i > 0 {
			cookieMap[kv[:i]] = kv[i+1:]
		}
	}
	// 第二个 cookie 文件（淘宝 SSO）参与 uid 提取与请求 Cookie 组装
	cookieSend := cookie
	if len(os.Args) > 4 && os.Args[4] != "" {
		if tbB, err := os.ReadFile(os.Args[4]); err == nil {
			for _, kv := range strings.Split(strings.TrimSpace(string(tbB)), "; ") {
				if i := strings.Index(kv, "="); i > 0 {
					cookieMap[kv[:i]] = kv[i+1:]
				}
			}
			if len(os.Args) > 5 && os.Args[5] == "combine" {
				cookieSend = cookie + "; " + strings.TrimSpace(string(tbB))
			}
		}
	}
	uid := cookieMap["unb"]
	if uid == "" { uid = cookieMap["aui"] }
	if uid == "" { uid = cookieMap["cnaui"] }
	if uid == "" { uid = cookieMap["munb"] }
	if uid == "" { uid = cookieMap["arms_uid"] }
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
	rawMode := len(os.Args) > 3 && os.Args[3] == "raw"
	fullURL := target
	if !rawMode {
		if strings.Contains(fullURL, "?") {
			fullURL += "&" + q.Encode()
		} else {
			fullURL += "?" + q.Encode()
		}
	}
	// 插件里 p11:"post" 是埋点参数，实际 fetch 走 GET 也试过；两个都试
	method := "GET"
	if len(os.Args) > 3 && os.Args[3] == "post" {
		method = "POST"
	}
	req, _ := http.NewRequest(method, fullURL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Referer", "https://www.goofish.com/")
	req.Header.Set("Cookie", cookieSend)
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")

	client := &http.Client{Transport: tr, Timeout: 25e9}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("请求失败:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 800))
	fmt.Println("状态码:", resp.StatusCode)
	fmt.Println("响应体:", string(body))
	fmt.Println("=== 全部 Set-Cookie ===")
	for _, sc := range resp.Header["Set-Cookie"] {
		// 只显示 cookie 名与过期，不回显值
		name := sc
		if i := strings.Index(sc, "="); i > 0 {
			name = sc[:i]
		}
		exp := ""
		if i := strings.Index(sc, "Expires="); i > 0 {
			exp = sc[i+8 : min(len(sc), i+40)]
		}
		fmt.Println("  cookie:", name, "|", exp)
	}
}

func min(a, b int) int { if a < b { return a }; return b }

