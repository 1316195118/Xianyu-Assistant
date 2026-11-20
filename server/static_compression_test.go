package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSPAHandlerServesPrecompressedAssets(t *testing.T) {
	t.Setenv("XY_TEST_STATIC", "1")
	assets := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><div id=app></div>")},
		"assets/app.js": {
			Data: []byte("console.log('plain')"),
		},
		"assets/app.js.br": {
			Data: []byte("brotli-fixture"),
		},
		"assets/app.js.gz": {
			Data: []byte("gzip-fixture"),
		},
		"assets/style.css": {
			Data: []byte("body{}"),
		},
		"assets/style.css.gz": {
			Data: []byte("gzip-css-fixture"),
		},
	}
	h := gzipMiddleware(spaHandler(assets, http.FileServer(http.FS(assets))))

	t.Run("brotli preferred", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
		req.Header.Set("Accept-Encoding", "gzip, br")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if got := rec.Header().Get("Content-Encoding"); got != "br" {
			t.Fatalf("Content-Encoding = %q, want br", got)
		}
		if got := rec.Body.String(); got != "brotli-fixture" {
			t.Fatalf("body = %q", got)
		}
		if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
			t.Fatalf("Vary = %q", got)
		}
	})

	t.Run("gzip fallback", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding = %q, want gzip", got)
		}
		if got := rec.Body.String(); got != "gzip-fixture" {
			t.Fatalf("body = %q", got)
		}
	})

	t.Run("missing sidecar serves original without dynamic double compression", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/assets/style.css", nil)
		req.Header.Set("Accept-Encoding", "br")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("Content-Encoding = %q, want empty", got)
		}
		if got := rec.Body.String(); got != "body{}" {
			t.Fatalf("body = %q", got)
		}
	})
}

func TestAcceptsEncodingQualityZero(t *testing.T) {
	if acceptsEncoding("gzip;q=0, br", "gzip") {
		t.Fatal("gzip;q=0 should not be accepted")
	}
	if !acceptsEncoding("gzip;q=0, br", "br") {
		t.Fatal("br should be accepted")
	}
}
