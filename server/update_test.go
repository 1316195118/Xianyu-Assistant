package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.0.5", "1.2.0.6", -1},
		{"v1.2.0.6", "1.2.0.6", 0},
		{"1.2.0.10", "1.2.0.9", 1},
		{"1.2", "1.2.0", 0},
		{"2.0", "1.9.9", 1},
		{"", "0.0.1", -1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q,%q)=%d，期望 %d", c.a, c.b, got, c.want)
		}
	}
}

func TestFixedPublicUpdateRepo(t *testing.T) {
	newExtrasEnv(t)
	setSetting("update_repo", "legacy/private")
	if got := updateRepoName(); got != defaultUpdateRepo {
		t.Fatalf("更新源应固定为公开仓库，实际 %q", got)
	}
	req, err := ghRequest(http.MethodGet, "/repos")
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("公开更新源不应发送 Token，实际认证头 %q", got)
	}
}

// mockReleaseAPI 起一个假 GitHub：返回发布 JSON，并提供二进制与校验文件下载。
func mockReleaseAPI(t *testing.T, tag string, body []byte, withDigest, withCompanion bool) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(body)
	sumHex := hex.EncodeToString(sum[:])
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/bin", func(w http.ResponseWriter, r *http.Request) { w.Write(body) })
	mux.HandleFunc("/sum", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", sumHex, updateAssetLinux)
	})
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		assets := []map[string]interface{}{
			{"name": updateAssetLinux, "size": len(body), "browser_download_url": srv.URL + "/bin"},
		}
		if withDigest {
			assets[0]["digest"] = "sha256:" + sumHex
		}
		if withCompanion {
			assets = append(assets, map[string]interface{}{
				"name": updateAssetLinux + ".sha256", "size": int64(len(sumHex) + 4),
				"browser_download_url": srv.URL + "/sum",
			})
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"tag_name": tag, "body": "测试发布", "published_at": "2026-11-20T04:00:00Z",
			"html_url": "https://example.com/release", "assets": assets,
		})
	})
	srv = httptest.NewServer(mux)
	return srv
}

func useUpdateAPI(t *testing.T, url string) {
	t.Helper()
	old := updateAPIBase
	updateAPIBase = url
	t.Cleanup(func() { updateAPIBase = old })
}

func TestUpdateCheckFindsNewerRelease(t *testing.T) {
	h := newExtrasEnv(t)
	srv := mockReleaseAPI(t, "v9.9.9", []byte("\x7fELFfakebinary"), true, false)
	defer srv.Close()
	useUpdateAPI(t, srv.URL)

	w := doExtrasReq(h, "GET", "/api/update/check", nil)
	if w.Code != 200 {
		t.Fatalf("check 应 200，实际 %d %s", w.Code, w.Body.String())
	}
	var out map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["latest"] != "9.9.9" {
		t.Errorf("latest=%v，期望 9.9.9", out["latest"])
	}
	if out["has_update"] != true {
		t.Errorf("应提示有新版本：%v", out)
	}
	if out["can_apply"] != (updateGOOS == "linux") {
		t.Errorf("can_apply 与平台不符：%v", out["can_apply"])
	}
}

func TestUpdateCheckNoRelease(t *testing.T) {
	h := newExtrasEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()
	useUpdateAPI(t, srv.URL)

	w := doExtrasReq(h, "GET", "/api/update/check", nil)
	var out map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["error"] == nil || out["error"] == "" {
		t.Fatalf("没有发布时应给出可读原因：%s", w.Body.String())
	}
}

func TestUpdateApplyRejectsNonLinux(t *testing.T) {
	h := newExtrasEnv(t)
	oldOS := updateGOOS
	updateGOOS = "windows"
	t.Cleanup(func() { updateGOOS = oldOS })

	w := doExtrasReq(h, "POST", "/api/update/apply", map[string]interface{}{})
	if w.Code != 400 {
		t.Fatalf("非 Linux 应 400，实际 %d %s", w.Code, w.Body.String())
	}
}

func TestUpdateProgressEndpoint(t *testing.T) {
	h := newExtrasEnv(t)
	finishUpdateProgress("idle", "", "", 0)
	t.Cleanup(func() { finishUpdateProgress("idle", "", "", 0) })

	if !beginUpdateProgress(buildVersion) {
		t.Fatal("空闲状态应能开始更新进度")
	}
	setUpdateDownloadProgress(512, 1024)
	w := doExtrasReq(h, "GET", "/api/update/progress", nil)
	if w.Code != 200 {
		t.Fatalf("progress 应 200，实际 %d %s", w.Code, w.Body.String())
	}
	var out updateProgressInfo
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("progress json: %v", err)
	}
	if !out.Active || out.Stage != "downloading" || out.DownloadedBytes != 512 || out.TotalBytes != 1024 {
		t.Errorf("进度状态不对：%+v", out)
	}
	if out.Percent < 40 || out.Percent > 60 {
		t.Errorf("下载到一半的总体进度不合理：%d", out.Percent)
	}
}

func TestUpdateApplyRejectsMissingDigest(t *testing.T) {
	h := newExtrasEnv(t)
	body := []byte("\x7fELFfakebinary")
	srv := mockReleaseAPI(t, "v9.9.9", body, false, false)
	defer srv.Close()

	oldOS, oldExe := updateGOOS, updateExePath
	updateGOOS = "linux"
	dir := t.TempDir()
	exe := filepath.Join(dir, "xianyu-stat")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	updateExePath = func() (string, error) { return exe, nil }
	t.Cleanup(func() { updateGOOS, updateExePath = oldOS, oldExe })
	useUpdateAPI(t, srv.URL)

	w := doExtrasReq(h, "POST", "/api/update/apply", map[string]interface{}{})
	if w.Code != 400 {
		t.Fatalf("缺少校验应 400，实际 %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".update", "xianyu-stat.new")); err == nil {
		t.Error("被拒绝时不应留下临时文件")
	}
}

func TestUpdateApplyRejectsWrongDigest(t *testing.T) {
	h := newExtrasEnv(t)
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/bin", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("\x7fELFfakebinary"))
	})
	mux.HandleFunc("/sum", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("0", 64)+"  x\n")
	})
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"tag_name": "v9.9.9", "assets": []map[string]interface{}{
				{"name": updateAssetLinux, "browser_download_url": srv.URL + "/bin"},
				{"name": updateAssetLinux + ".sha256", "browser_download_url": srv.URL + "/sum"},
			},
		})
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	oldOS, oldExe := updateGOOS, updateExePath
	updateGOOS = "linux"
	dir := t.TempDir()
	exe := filepath.Join(dir, "xianyu-stat")
	os.WriteFile(exe, []byte("old"), 0o755)
	updateExePath = func() (string, error) { return exe, nil }
	t.Cleanup(func() { updateGOOS, updateExePath = oldOS, oldExe })
	useUpdateAPI(t, srv.URL)

	w := doExtrasReq(h, "POST", "/api/update/apply", map[string]interface{}{})
	if w.Code != 400 {
		t.Fatalf("校验不一致应 400，实际 %d %s", w.Code, w.Body.String())
	}
}

func TestExpectedDigestFromCompanion(t *testing.T) {
	body := []byte("hello")
	srv := mockReleaseAPI(t, "v1.0.0", body, false, true)
	defer srv.Close()
	sum := sha256.Sum256(body)
	rel := &ghRelease{Assets: []ghAsset{
		{Name: updateAssetLinux, URL: srv.URL + "/bin"},
		{Name: updateAssetLinux + ".sha256", URL: srv.URL + "/sum"},
	}}
	got, err := expectedDigest(rel, rel.Assets[0])
	if err != nil {
		t.Fatalf("应能从 .sha256 附件取到校验值：%v", err)
	}
	if got != hex.EncodeToString(sum[:]) {
		t.Errorf("校验值不对：%s", got)
	}
}

func TestUpdateConfigProxyValidation(t *testing.T) {
	h := newExtrasEnv(t)
	setSetting("update_repo", "legacy/private")
	setSetting("update_token", "legacy-token")

	w := doExtrasReq(h, "PUT", "/api/update/config", map[string]interface{}{"proxy": "not-a-url"})
	if w.Code != 400 {
		t.Fatalf("非法代理应 400，实际 %d %s", w.Code, w.Body.String())
	}
	ok := doExtrasReq(h, "PUT", "/api/update/config", map[string]interface{}{"proxy": "http://127.0.0.1:7890"})
	if ok.Code != 200 {
		t.Fatalf("合法代理应 200，实际 %d %s", ok.Code, ok.Body.String())
	}
	if getSetting("update_proxy") != "http://127.0.0.1:7890" {
		t.Errorf("代理没写进设置：%q", getSetting("update_proxy"))
	}
	if getSetting("update_repo") != "" || getSetting("update_token") != "" {
		t.Errorf("保存配置时应清除旧仓库与 Token：repo=%q token=%q", getSetting("update_repo"), getSetting("update_token"))
	}
	// 传空表示关闭代理
	doExtrasReq(h, "PUT", "/api/update/config", map[string]interface{}{"proxy": ""})
	if getSetting("update_proxy") != "" {
		t.Errorf("空值应清空代理，实际 %q", getSetting("update_proxy"))
	}
}

func TestAssetURLUsesAPI(t *testing.T) {
	oldBase := updateAPIBase
	updateAPIBase = "https://api.example.com"
	defer func() { updateAPIBase = oldBase }()
	want := "https://api.example.com/repos/" + defaultUpdateRepo + "/releases/assets/7"
	if got := assetURL(ghAsset{ID: 7}); got != want {
		t.Errorf("资产地址不对：%s", got)
	}
	// 没有 id 时退回浏览器地址，兼容旧响应
	if got := assetURL(ghAsset{URL: "https://x/y"}); got != "https://x/y" {
		t.Errorf("退化地址不对：%s", got)
	}
}
