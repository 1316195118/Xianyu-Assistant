// gf_qr_lab：验证服务器端能否直连闲鱼扫码登录链路。
// 流程：mini_login.htm 拿 cookie+csrf → qrcode/generate.do 拿二维码 → 打印 codeContent。
// 用法：gf_qr_lab [post]
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
	"regexp"
	"strings"

	"golang.org/x/net/http2"
	tlsx "github.com/refraction-networking/utls"
)

var client = &http.Client{Transport: &http2.Transport{
	DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
		host := addr[:strings.LastIndex(addr, ":")]
		raw, err := (&net.Dialer{Timeout: 15e9}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		uconn := tlsx.UClient(raw, &tlsx.Config{ServerName: host}, tlsx.HelloChrome_120)
		if err := uconn.Handshake(); err != nil {
			raw.Close()
			return nil, err
		}
		return uconn, nil
	},
}}

var jar = map[string]string{} // 简单 cookie jar（名字不敏感，值不回显）

func do(method, rawurl string, form url.Values) (int, []byte, error) {
	var body io.Reader
	req, err := http.NewRequest(method, rawurl, body)
	if err != nil {
		return 0, nil, err
	}
	if form != nil {
		req, err = http.NewRequest(method, rawurl, strings.NewReader(form.Encode()))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Referer", "https://www.goofish.com/login")
	parts := []string{}
	for k, v := range jar {
		parts = append(parts, k+"="+v)
	}
	req.Header.Set("Cookie", strings.Join(parts, "; "))
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	// 收 cookie
	for _, sc := range resp.Header.Values("Set-Cookie") {
		kv := strings.SplitN(sc, ";", 2)[0]
		if i := strings.Index(kv, "="); i > 0 {
			jar[kv[:i]] = kv[i+1:]
		}
	}
	return resp.StatusCode, b, nil
}

func main() {
	fmt.Println("=== 1. 打开 mini_login.htm 拿 cookie/csrf ===")
	mini := "https://passport.goofish.com/mini_login.htm?lang=zh_cn&appName=xianyu&appEntrance=web&styleType=vertical&bizParams=&notLoadSsoView=false&notKeepLogin=false&isMobile=false&qrCodeFirst=true&stie=77"
	st, body, err := do("GET", mini, nil)
	fmt.Println("状态:", st, "响应长度:", len(body), "err:", err)
	if err != nil {
		os.Exit(1)
	}
	// 从页面找 _csrf_token
	csrf := ""
	if m := regexp.MustCompile(`_csrf_token["']?\s*[:=]\s*["']?([A-Za-z0-9]+)`).FindSubmatch(body); m != nil {
		csrf = string(m[1])
	}
	if csrf == "" {
		if m := regexp.MustCompile(`name="_csrf_token" value="([^"]+)"`).FindSubmatch(body); m != nil {
			csrf = string(m[1])
		}
	}
	if csrf == "" {
		// cookie 里的 XSRF-TOKEN 也可当 csrf
		csrf = jar["XSRF-TOKEN"]
	}
	fmt.Println("csrf 提取:", csrf != "", "| jar cookie 数:", len(jar))

	fmt.Println("=== 2. 生成二维码 ===")
	genURL := "https://passport.goofish.com/newlogin/qrcode/generate.do?appName=xianyu&fromSite=77&appName=xianyu&appEntrance=web&_csrf_token=" + url.QueryEscape(csrf) + "&umidToken=&hsiz=&bizParams=t"
	st2, body2, err2 := do("GET", genURL, nil)
	fmt.Println("状态:", st2, "err:", err2)
	fmt.Println("响应:", string(body2))
}
