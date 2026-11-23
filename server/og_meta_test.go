package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestServeIndexHTMLUsesRequestOriginForSocialImage(t *testing.T) {
	sub := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(`<meta property="og:image" content="__SITE_ORIGIN__/og-image.png">`)},
	}
	handler := spaHandler(sub, http.FileServer(http.FS(sub)))
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	req.Host = "example.test"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); !strings.Contains(got, `content="https://example.test/og-image.png"`) {
		t.Fatalf("og:image 未按请求源补全: %s", got)
	}
}

func TestPublicPageMetadataAndStatus(t *testing.T) {
	sub := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(`<!doctype html><html><head>
<title>鱼助手 · 闲鱼售卖统计</title>
<meta name="description" content="鱼助手是一款自托管的闲鱼售卖统计工具：自动同步订单，分摊进货成本与联单成本，算清运费、服务费和真实利润。" />
<meta property="og:type" content="website" />
<meta property="og:title" content="鱼助手 · 闲鱼售卖统计" />
<meta property="og:description" content="自动同步闲鱼订单，分摊进货与联单成本，把运费、服务费和真实利润一笔算清。" />
<meta property="og:image" content="__SITE_ORIGIN__/og-image.png" />
<meta name="twitter:title" content="鱼助手 · 闲鱼售卖统计" />
<meta name="twitter:description" content="自动同步闲鱼订单，分摊进货与联单成本，算清真实利润。" />
</head><body><div id=app></div></body></html>`)},
	}
	handler := spaHandler(sub, http.FileServer(http.FS(sub)))

	t.Run("privacy", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://example.test/privacy", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{
			`<title>隐私政策｜鱼助手闲鱼售卖统计</title>`,
			`<meta name="robots" content="index, follow" />`,
			`<link rel="canonical" href="https://example.test/privacy" />`,
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("privacy response missing %q: %s", want, body)
			}
		}
	})

	t.Run("unknown path is real 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/not-a-page", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "页面未找到") {
			t.Fatalf("404 page should still contain SPA shell metadata")
		}
	})
}

func TestRobotsAndSitemapUseRequestOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://example.test/robots.txt", nil)
	rec := httptest.NewRecorder()
	serveRobots(rec, req)
	if !strings.Contains(rec.Body.String(), "Sitemap: https://example.test/sitemap.xml") {
		t.Fatalf("robots missing sitemap origin: %s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "https://example.test/sitemap.xml", nil)
	rec = httptest.NewRecorder()
	serveSitemap(rec, req)
	body := rec.Body.String()
	for _, want := range []string{"https://example.test/privacy", "https://example.test/terms"} {
		if !strings.Contains(body, want) {
			t.Fatalf("sitemap missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "<loc>https://example.test/</loc>") {
		t.Fatalf("private app entry point must not be listed in sitemap: %s", body)
	}
}
