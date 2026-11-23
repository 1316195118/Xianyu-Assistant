package main

// 闲鱼扫码登录（服务器端直连，uTLS Chrome 指纹 + HTTP/2）
//
// 链路（逆向自 havana-nlogin 登录组件，实测通过）：
//  1. GET passport.goofish.com/mini_login.htm → 拿 cookie（XSRF-TOKEN 等）+ _csrf_token
//  2. GET newlogin/qrcode/generate.do → { codeContent(二维码URL), ck(轮询key) }
//  3. POST newlogin/qrcode/query.do → qrCodeStatus: NEW / SCANNED / CONFIRMED
//  4. CONFIRMED 后响应带回调 URL，访问后 Set-Cookie 落到 jar，再用 bought.list 校验并保存
//
// 前端把 codeContent 渲染成二维码图，用户用闲鱼 App 扫。

import (
	"context"
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	tlsx "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// ---------- 共享的 uTLS Chrome 指纹客户端（与静默换发共用底层） ----------

var gfQRTLSOnce sync.Once
var gfQRClient *http.Client

func gfBrowserClient() *http.Client {
	gfQRTLSOnce.Do(func() {
		gfQRClient = &http.Client{Timeout: 25e9, Transport: &http2.Transport{
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				host := addr
				if i := strings.LastIndex(addr, ":"); i > 0 {
					host = addr[:i]
				}
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
	})
	return gfQRClient
}

// ---------- 扫码登录会话 ----------

type gfQRSession struct {
	mu       sync.Mutex
	jar      map[string]string // cookie jar
	umid     string            // 设备指纹（必填，否则二维码 EXPIRED）
	csrf     string
	ck       string
	hsiz     string // 生成二维码时传入的会话标识（真实浏览器 ck==hsiz）
	lgToken  string
	deviceID string // 设备标识（query.do 埋点）
	status   string // new / scanned / confirmed / failed
	errMsg   string
	done     bool
}

var gfQRSessions sync.Map // shop -> *gfQRSession

func md5Hex(v string) string {
	h := md5.Sum([]byte(v))
	return hex.EncodeToString(h[:])
}

// gfPickQRCk 以接口实际返回的轮询 key 为准；只有缺失时才回退到生成请求的 hsiz。
func gfPickQRCk(generated, hsiz string) (string, bool) {
	ck := strings.TrimSpace(generated)
	if ck == "" {
		ck = strings.TrimSpace(hsiz)
	}
	return ck, ck != "" && ck == strings.TrimSpace(hsiz)
}

func gfQRGet(shop int64) *gfQRSession {
	v, _ := gfQRSessions.Load(shop)
	if v == nil {
		return nil
	}
	return v.(*gfQRSession)
}

// gfQRDo 带 cookie jar 的请求
func gfQRDo(s *gfQRSession, method, rawurl string, form url.Values, referer string) (int, []byte, error) {
	var req *http.Request
	var err error
	if form != nil {
		req, err = http.NewRequest(method, rawurl, strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	} else {
		req, err = http.NewRequest(method, rawurl, nil)
	}
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/html;q=0.9, */*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	parts := []string{}
	for k, v := range s.jar {
		parts = append(parts, k+"="+v)
	}
	req.Header.Set("Cookie", strings.Join(parts, "; "))

	resp, err := gfBrowserClient().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
	for _, sc := range resp.Header.Values("Set-Cookie") {
		kv := strings.SplitN(sc, ";", 2)[0]
		if i := strings.Index(kv, "="); i > 0 {
			s.jar[kv[:i]] = kv[i+1:]
		}
	}
	return resp.StatusCode, body, nil
}

// gfQRStart 初始化扫码登录会话，返回二维码内容 URL。
func gfQRStart(shop int64) (string, error) {
	s := &gfQRSession{jar: map[string]string{}, status: "new", umid: strings.TrimSpace(gfGet(gfKeyUmidToken, shop))}
	// 把保存的闲鱼/淘宝登录态 Cookie 注入 jar：真实浏览器轮询二维码时带的是完整登录 Cookie，
	// 纯匿名会话查询会被闲鱼判 EXPIRED。
	for _, key := range []string{gfKeyCookie, gfKeySSOTaobao} {
		raw := gfGet(key, shop)
		for _, kv := range strings.Split(raw, ";") {
			kv = strings.TrimSpace(kv)
			if i := strings.Index(kv, "="); i > 0 {
				s.jar[kv[:i]] = kv[i+1:]
			}
		}
	}
	gfQRSessions.Store(shop, s)

	mini := "https://passport.goofish.com/mini_login.htm?lang=zh_cn&appName=xianyu&appEntrance=web" +
		"&styleType=vertical&bizParams=&notLoadSsoView=false&notKeepLogin=false&isMobile=false" +
		"&qrCodeFirst=true&stie=77"
	st, body, err := gfQRDo(s, "GET", mini, nil, "https://www.goofish.com/login")
	if err != nil {
		return "", err
	}
	if st != 200 {
		return "", fmt.Errorf("登录页加载失败(%d)", st)
	}
	// csrf：优先页面变量，其次 hidden input，再兜底 XSRF-TOKEN
	if m := regexp.MustCompile(`_csrf_token["']?\s*[:=]\s*["']?([A-Za-z0-9]+)`).FindSubmatch(body); m != nil {
		s.csrf = string(m[1])
	} else if m := regexp.MustCompile(`name="_csrf_token" value="([^"]+)"`).FindSubmatch(body); m != nil {
		s.csrf = string(m[1])
	} else {
		s.csrf = s.jar["XSRF-TOKEN"]
	}
	if s.csrf == "" {
		return "", fmt.Errorf("csrf 提取失败")
	}
	// deviceId：页面里有就取，否则用 umid 派生一个稳定的 22 位标识
	if m := regexp.MustCompile(`deviceId["'\s:=]+([A-Za-z0-9]{16,40})`).FindSubmatch(body); m != nil {
		s.deviceID = string(m[1])
	} else if len(s.umid) >= 22 {
		s.deviceID = s.umid[:22]
	}

	// 真实浏览器：hsiz 在 generate 前就生成好传入，返回的 ck 就等于它。
	// hsiz 是 32 位十六进制（MD5 样），这里用设备标识派生一个稳定值。
	hsiz := md5Hex(s.umid + s.csrf)
	s.hsiz = hsiz
	biz := "taobaoBizLoginFrom%3Dweb%26renderRefer%3Dhttps%253A%252F%252Fwww.goofish.com%252F"
	genURL := "https://passport.goofish.com/newlogin/qrcode/generate.do?appName=xianyu&fromSite=77" +
		"&appName=xianyu&appEntrance=web&_csrf_token=" + url.QueryEscape(s.csrf) +
		"&umidToken=" + url.QueryEscape(s.umid) + "&hsiz=" + hsiz + "&bizParams=" + biz +
		"&mainPage=false&isMobile=false&lang=zh_CN&returnUrl=&umidTag=SERVER"
	_, genBody, err := gfQRDo(s, "GET", genURL, nil, mini)
	if err != nil {
		return "", err
	}
	var out struct {
		Content struct {
			Data struct {
				CodeContent string `json:"codeContent"`
				Ck          string `json:"ck"`
				ResultCode  int    `json:"resultCode"`
			} `json:"data"`
		} `json:"content"`
	}
	if err := json.Unmarshal(genBody, &out); err != nil {
		return "", fmt.Errorf("二维码响应解析失败: %v", err)
	}
	if out.Content.Data.CodeContent == "" || out.Content.Data.Ck == "" {
		return "", fmt.Errorf("二维码生成失败，请重试")
	}
	var ckMatchesHsiz bool
	s.ck, ckMatchesHsiz = gfPickQRCk(out.Content.Data.Ck, s.hsiz)
	if m := regexp.MustCompile(`lgToken=([^&"]+)`).FindStringSubmatch(out.Content.Data.CodeContent); m != nil {
		s.lgToken = m[1]
	}
	fmt.Printf("[闲鱼扫码] 店铺 %d 生成新码: resultCode=%d ck_equals_hsiz=%t 时间=%s\n",
		shop, out.Content.Data.ResultCode, ckMatchesHsiz, time.Now().Format("15:04:05"))
	return out.Content.Data.CodeContent, nil
}

// gfQRPoll 轮询扫码状态；CONFIRMED 时自动完成绑定（拿 cookie 存 settings）。
// 返回 status：new / scanned / confirmed / failed。
func gfQRPoll(shop int64) (string, error) {
	s := gfQRGet(shop)
	if s == nil {
		return "failed", fmt.Errorf("会话不存在，请重新生成二维码")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return s.status, nil
	}

	form := url.Values{}
	form.Set("t", fmt.Sprint(time.Now().UnixMilli()))
	form.Set("ck", s.ck)
	form.Set("ua", "")
	form.Set("appName", "xianyu")
	form.Set("appEntrance", "web")
	form.Set("_csrf_token", s.csrf)
	form.Set("umidToken", s.umid)
	// 真实浏览器抓包：query.do 的 hsiz 与 generate.do 传入的 hsiz 相同（=ck）
	form.Set("hsiz", s.hsiz)
	form.Set("bizParams", "taobaoBizLoginFrom%3Dweb%26renderRefer%3Dhttps%253A%252F%252Fwww.goofish.com%252F")
	form.Set("mainPage", "false")
	form.Set("isMobile", "false")
	form.Set("lang", "zh_CN")
	form.Set("returnUrl", "")
	form.Set("fromSite", "77")
	form.Set("umidTag", "SERVER")
	form.Set("navlanguage", "zh-CN")
	form.Set("navUserAgent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	form.Set("navPlatform", "Win32")
	form.Set("isIframe", "true")
	form.Set("documentReferer", "https://www.goofish.com/")
	form.Set("defaultView", "sms")
	if s.deviceID != "" {
		form.Set("deviceId", s.deviceID)
	}

	qURL := "https://passport.goofish.com/newlogin/qrcode/query.do?appName=xianyu&fromSite=77"
	_, body, err := gfQRDo(s, "POST", qURL, form, "https://passport.goofish.com/mini_login.htm")
	if err != nil {
		s.status = "failed"
		s.errMsg = err.Error()
		s.done = true
		return "failed", err
	}
	var out struct {
		Content struct {
			Data struct {
				QrCodeStatus string `json:"qrCodeStatus"`
				ResultCode   int    `json:"resultCode"`
				URL          string `json:"url"`
				TitleMsg     string `json:"titleMsg"`
			} `json:"data"`
		} `json:"content"`
	}
	_ = json.Unmarshal(body, &out)
	st := strings.ToUpper(out.Content.Data.QrCodeStatus)
	// 只记录定位状态所需字段，不输出 ck、lgToken、umidToken、Cookie 或完整响应体。
	fmt.Printf("[闲鱼扫码] 店铺 %d 轮询响应: status=%s resultCode=%d title=%q url=%t ck_equals_hsiz=%t 时间=%s\n",
		shop, st, out.Content.Data.ResultCode, out.Content.Data.TitleMsg,
		out.Content.Data.URL != "", s.ck == s.hsiz, time.Now().Format("15:04:05"))
	switch st {
	case "SCANNED":
		s.status = "scanned"
	case "CONFIRMED", "SUCCESS":
		s.status = "confirmed"
		s.done = true
		// 访问回调 URL 拿登录 Cookie
		cb := out.Content.Data.URL
		if cb == "" {
			cb = "https://passport.goofish.com/qrcodeCheck.htm?lgToken=" + url.QueryEscape(s.lgToken)
		}
		if cb != "" {
			gfQRDo(s, "GET", cb, nil, "https://passport.goofish.com/")
		}
		// 组装 cookie 并绑定（校验 bought.list）
		parts := []string{}
		for k, v := range s.jar {
			parts = append(parts, k+"="+v)
		}
		cookieStr := strings.Join(parts, "; ")
		if err := gfQRBind(shop, cookieStr); err != nil {
			s.status = "failed"
			s.errMsg = err.Error()
			return "failed", err
		}
	case "EXPIRED":
		// 二维码被闲鱼判失效（多为设备指纹/环境校验不通过），明确报错，
		// 不再伪装成 new 让前端空等 5 分钟。
		s.status = "failed"
		s.errMsg = "二维码已失效，请重新生成；若反复出现，请改用一键绑定工具"
		s.done = true
		return "failed", fmt.Errorf("%s", s.errMsg)
	default:
		s.status = "new"
		if out.Content.Data.TitleMsg != "" {
			s.status = "failed"
			s.errMsg = out.Content.Data.TitleMsg
			s.done = true
			return "failed", fmt.Errorf("%s", out.Content.Data.TitleMsg)
		}
	}
	return s.status, nil
}

// gfQRBind 用扫码得到的 Cookie 完成绑定（与手动绑定同一套校验）。
func gfQRBind(shop int64, cookieStr string) error {
	c := newGFClient(cookieStr)
	if len(c.cookies) < 3 {
		return fmt.Errorf("登录 Cookie 不完整")
	}
	if _, err := c.boughtOrders(1); err != nil {
		return fmt.Errorf("登录态校验失败：%s", gfExplainErr(err))
	}
	acc, err := c.account()
	if err != nil {
		return fmt.Errorf("登录态校验失败：%s", err.Error())
	}
	if err := gfSet(gfKeyCookie, shop, c.cookieString()); err != nil {
		return err
	}
	_ = gfSet(gfKeyNick, shop, acc.Nick)
	if acc.UID != "" {
		_ = gfSet(gfKeyUID, shop, acc.UID)
	}
	_ = gfSet(gfKeySaved, shop, time.Now().Format("2006-01-02 15:04:05"))
	_ = gfSet(gfKeyCheck, shop, time.Now().Format("2006-01-02 15:04:05"))
	_ = gfSet(gfKeyDead, shop, "")
	if !gfKeepAliveEnabled() {
		setSetting(gfKeepAliveKey, "1")
	}
	return nil
}

// ---------- HTTP 处理 ----------

// handleGoofishQRStart POST /api/goofish/qr/start
func handleGoofishQRStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	shop := shopID(r)
	code, err := gfQRStart(shop)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "code_content": code})
}

// handleGoofishUmid POST /api/goofish/umid —— 一键绑定工具从真实浏览器抓到 umidToken 后存这里
func handleGoofishUmid(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if !decodeJSONBody(w, r, &in, maxTinyJSONBody) {
		return
	}
	shop := shopID(r)
	tok := strings.TrimSpace(in.Token)
	if tok == "" || len(tok) != 40 {
		writeError(w, 400, "umidToken 格式不正确（应为 40 位十六进制）")
		return
	}
	if err := gfSet(gfKeyUmidToken, shop, tok); err != nil {
		writeError(w, 500, "保存失败")
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

// handleGoofishQRPoll POST /api/goofish/qr/poll
func handleGoofishQRPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	shop := shopID(r)
	st, err := gfQRPoll(shop)
	if err != nil {
		writeJSON(w, map[string]interface{}{"status": st, "error": err.Error()})
		return
	}
	nick := gfGet(gfKeyNick, shop)
	writeJSON(w, map[string]interface{}{"status": st, "nick": nick})
}
