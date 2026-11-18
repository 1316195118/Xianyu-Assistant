package main

import (
	"compress/gzip"
	"io"
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// ============================================================
// gzip 压缩
// ============================================================

// 这些后缀本身已是压缩格式，再压一次只会浪费 CPU（甚至变大）
var preCompressedExts = []string{
	".png", ".jpg", ".jpeg", ".webp", ".gif", ".ico",
	".woff", ".woff2", ".ttf", ".otf",
	".wasm", ".gz", ".zip", ".br", ".zst",
}

func shouldSkipGzip(r *http.Request) bool {
	if r.Header.Get("Range") != "" { // Range 请求（分段下载）不能整体压缩
		return true
	}
	p := strings.ToLower(r.URL.Path)
	// 附件与缩略图是已压缩的图片，再走一遍 gzip 只白烧 CPU（还可能变大）
	if strings.HasSuffix(p, "/attachment") || strings.HasPrefix(p, "/api/thumb") {
		return true
	}
	for _, ext := range preCompressedExts {
		if strings.HasSuffix(p, ext) {
			return true
		}
	}
	return false
}

type gzipResponseWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	wroteHeader bool
}

func (g *gzipResponseWriter) WriteHeader(code int) {
	if !g.wroteHeader {
		g.wroteHeader = true
		// 压缩后长度不可预知，删掉 FileServer 设置的 Content-Length，交给 chunked
		g.Header().Del("Content-Length")
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	if !g.wroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	return g.gz.Write(b)
}

func (g *gzipResponseWriter) Flush() {
	g.gz.Flush()
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// gzipMiddleware 对文本类响应做 gzip 压缩；已压缩资源与 Range 请求直接放行。
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// HEAD 不返回 body，压缩会让 Content-Length 变成空 gzip 流长度（误导）
		if r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") || shouldSkipGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		gz, err := gzip.NewWriterLevel(w, gzip.DefaultCompression)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		defer gz.Close()
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, gz: gz}, r)
	})
}

// ============================================================
// 请求日志 + panic 兜底
// ============================================================

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.status = http.StatusOK
		s.wroteHeader = true
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// accessLog 记录请求耗时与状态码；panic 时返回 500 并打印堆栈，避免连接被直接掐断。
// 只记录 API、异常状态和慢请求，避免静态资源刷屏。
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[PANIC] %s %s: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())
				if !sw.wroteHeader {
					sw.Header().Set("Content-Type", "application/json; charset=utf-8")
					sw.WriteHeader(http.StatusInternalServerError)
					io.WriteString(sw, `{"error":"服务器内部错误"}`)
				}
			}
			dur := time.Since(start)
			if strings.HasPrefix(r.URL.Path, "/api/") || sw.status >= 400 || dur > 500*time.Millisecond {
				log.Printf("%s %s %d %s", r.Method, r.URL.Path, sw.status, dur.Round(time.Millisecond))
			}
		}()
		next.ServeHTTP(sw, r)
	})
}
