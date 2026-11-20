package main

import (
	"embed"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

//go:embed webroot
var webrootFS embed.FS

func main() {
	if err := initDB(); err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}
	if err := initUndoTable(); err != nil {
		log.Printf("初始化撤销表失败（不影响主功能）: %v", err)
	}
	if err := initExtrasSchema(); err != nil {
		log.Printf("初始化扩展表失败（不影响主功能）: %v", err)
	}
	initPurchaseIndex() // 买进单内存索引：关联匹配秒开

	// 一次性：把历史记录的运费标签按「>0.1 才算有运费」重新判定（之后不再覆盖手工改动）
	remapShipTags()

	// 忘记访问密码时本机恢复：xianyu-stat --reset-password
	if len(os.Args) > 1 && os.Args[1] == "--reset-password" {
		resetAuthFromCLI()
		return
	}

	go backupLoop()
	// 闲鱼同步：把旧版全局绑定搬到默认店铺，再起后台保活
	gfMigrateGlobal()
	// 独立低频心跳负责续期；自动同步只负责按业务间隔拉订单。
	go goofishSessionMaintainLoop()
	go goofishAutoSyncLoop()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()
	registerAPI(mux)

	sub, err := fs.Sub(webrootFS, "webroot")
	if err != nil {
		log.Fatalf("嵌入前端资源失败: %v", err)
	}
	fileServer := http.FileServer(http.FS(sub))
	mux.Handle("/", spaHandler(sub, fileServer))

	addr := ":" + port
	log.Println("----------------------------------------")
	log.Println(" 闲鱼售卖统计系统 已启动")
	log.Println(" 本地访问: http://localhost" + addr)
	_, key, model := effectiveOCRConfig()
	if key == "" {
		log.Println(" [提示] 未配置识别 API，请在「设置」页填写，或手动录入订单。")
	} else {
		log.Println(" [提示] 截图识别已启用: " + model)
	}
	log.Println("----------------------------------------")
	// 中间件由外到内：安全头 → 请求日志/recover → gzip → 业务路由
	handler := gzipMiddleware(mux)
	handler = accessLog(handler)
	handler = securityHeaders(handler)

	// 显式配置超时：默认的 http.ListenAndServe 不带任何超时，
	// 一个只建立连接却不发完整请求的客户端就能长期占住 goroutine（Slowloris）。
	// 这是内网自用工具，但配好超时是标准做法，成本几乎为零。
	//   ReadHeaderTimeout：限制读完请求头的时间，堵住 Slowloris
	//   ReadTimeout：限制读完整个请求（含上传截图）的时间
	//   IdleTimeout：空闲长连接回收，避免 keep-alive 堆积
	//   WriteTimeout 刻意不设：导出 CSV/XLSX 可能较慢，设死会截断大文件响应
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       120 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

// securityHeaders 全局安全响应头，在 main.go 包住整个 mux。
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), payment=(), usb=()")
		// CSP：只允许同源资源；内联样式用于 Vue 的 :style 绑定，
		// 内联脚本是 index.html 的主题初始化；OCR 需要 wasm 编译与 blob worker。
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data: blob:; "+
				"font-src 'self' data:; "+
				"connect-src 'self'; "+
				"worker-src 'self' blob:; "+
				"media-src 'self' blob:; "+
				"object-src 'none'; "+
				"base-uri 'self'; "+
				"form-action 'self'; "+
				"frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// spaHandler 提供静态文件，找不到时回退到 index.html（支持前端路由）
func spaHandler(sub fs.FS, fileServer http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		// 关键：前端资源文件名固定，必须彻底禁用缓存，否则浏览器 / 手机 bfcache /
		// Cloudflare Tunnel 等中间层会长期保留旧 JS，部署新版本后用户端不更新。
		// HTML 用 no-store（禁止任何存储，含 bfcache）；静态资源用 no-cache + 兼容头。
		// 配合 index.html 里带内容哈希的 ?v= 引用，任何 CDN 都必然回源取新文件。
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean == "" {
			clean = "index.html"
		}
		if clean == "index.html" {
			// 入口 HTML 必须每次回源，才能拿到新的哈希文件名
			w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
		} else if strings.HasPrefix(clean, "assets/") {
			// 文件名含内容哈希，内容永不变，可长期强缓存
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			// /ocr、/licenses、图标等稳定但非哈希资源，仍走协商缓存
			w.Header().Set("Cache-Control", "no-cache, must-revalidate, max-age=0")
			w.Header().Set("Pragma", "no-cache")
		}
		if _, err := fs.Stat(sub, clean); err != nil {
			// assets 下的文件不存在必须返回 404：若回退成 index.html，
			// 浏览器会把 HTML 当 JS/CSS 解析，报 "Unexpected token <"。
			if strings.HasPrefix(clean, "assets/") {
				w.Header().Set("Cache-Control", "no-store")
				http.NotFound(w, r)
				return
			}
			// 其他路径回退 index.html，交给前端路由
			data, e := fs.ReadFile(sub, "index.html")
			if e != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(data)
			return
		}
		// 构建时会为哈希资源预生成 Brotli / gzip 副本。直接发送预压缩文件，
		// 既减少手机与远程访问的传输量，也避免每次请求都现场压缩。
		if servePrecompressed(w, r, sub, clean) {
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func servePrecompressed(w http.ResponseWriter, r *http.Request, sub fs.FS, clean string) bool {
	if !strings.HasPrefix(clean, "assets/") {
		return false
	}
	accept := r.Header.Get("Accept-Encoding")
	candidates := make([]struct{ file, encoding string }, 0, 2)
	if acceptsEncoding(accept, "br") {
		candidates = append(candidates, struct{ file, encoding string }{clean + ".br", "br"})
	}
	if acceptsEncoding(accept, "gzip") {
		candidates = append(candidates, struct{ file, encoding string }{clean + ".gz", "gzip"})
	}
	for _, candidate := range candidates {
		if _, err := fs.Stat(sub, candidate.file); err != nil {
			continue
		}
		data, err := fs.ReadFile(sub, candidate.file)
		if err != nil {
			continue
		}
		if ct := mime.TypeByExtension(path.Ext(clean)); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.Header().Set("Content-Encoding", candidate.encoding)
		w.Header().Add("Vary", "Accept-Encoding")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(data)
		}
		return true
	}
	return false
}

func acceptsEncoding(header, encoding string) bool {
	for _, item := range strings.Split(header, ",") {
		parts := strings.Split(item, ";")
		if !strings.EqualFold(strings.TrimSpace(parts[0]), encoding) {
			continue
		}
		qualityDisabled := false
		for _, parameter := range parts[1:] {
			kv := strings.SplitN(strings.TrimSpace(parameter), "=", 2)
			if len(kv) == 2 && strings.EqualFold(kv[0], "q") && strings.TrimSpace(kv[1]) == "0" {
				qualityDisabled = true
			}
		}
		return !qualityDisabled
	}
	return false
}
