package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"
)

// ============================================================
// 在线更新：从 GitHub Release 拉取对应平台二进制，容器内自替换并重启。
//
// 设计取舍：
//   - 只认「已发布的 Release」，不跟分支走。分支随时可能是半成品，
//     自更新一旦装进半成品，容器就再也起不来了。
//   - 必须校验 sha256。Release 里的 digest 字段或同名的 .sha256 附件，
//     两者都没有就中止——宁可不让更，也不能装一个来路不明的二进制。
//   - 替换动作交给独立脚本：Go 进程自己被重启时，没法可靠地完成
//     「覆盖正在运行的文件 + 重启服务」这两步。
// ============================================================

const (
	defaultUpdateRepo = "1316195118/Xianyu-Assistant"
	updateAssetLinux  = "xianyu-stat-linux-amd64"
	updateAssetWin    = "xianyu-stat.exe"
	updateMaxBytes    = 200 << 20 // 200MB：远超实际体积，只用来挡住异常大文件
)

var (
	// 测试里替换成 httptest 地址；生产固定 GitHub API。
	updateAPIBase = "https://api.github.com"
	updateGOOS    = runtime.GOOS
	updateGOARCH  = runtime.GOARCH
	updateExePath = os.Executable
)

// updateHTTPClient 每次按当前设置构造：容器里多半要经过代理才能连 GitHub，
// 代理写进设置就立即生效，不用重启服务。
// 没设代理时走系统环境变量（HTTPS_PROXY），保持容器编排的常规做法。
func updateHTTPClient(timeout time.Duration) *http.Client {
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if raw := strings.TrimSpace(getSetting("update_proxy")); raw != "" {
		if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

func updateProxy() string { return strings.TrimSpace(getSetting("update_proxy")) }

var errNoRelease = errors.New("no release")

type ghAsset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"` // 新版 API 才有：sha256:<hex>
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []ghAsset `json:"assets"`
}

func updateRepoName() string {
	if v := strings.TrimSpace(getSetting("update_repo")); v != "" {
		return v
	}
	return defaultUpdateRepo
}

// Token 优先用设置里的，其次读环境变量：容器里用环境变量注入可以避免把
// 只读 token 写进数据库；页面里填则方便没有 shell 的场景。
func updateToken() string {
	if v := strings.TrimSpace(getSetting("update_token")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("XY_GH_TOKEN"))
}

func ghRequest(method, path string) (*http.Request, error) {
	req, err := http.NewRequest(method, updateAPIBase+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "xianyu-stat/"+buildVersion)
	if tok := updateToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return req, nil
}

func fetchLatestRelease() (*ghRelease, error) {
	req, err := ghRequest(http.MethodGet, "/repos/"+updateRepoName()+"/releases/latest")
	if err != nil {
		return nil, err
	}
	// 查版本只要几秒：超时设短一点，网络不通时尽快把原因告诉用户，
	// 别让人对着转圈等 45 秒。
	resp, err := updateHTTPClient(12 * time.Second).Do(req)
	if err != nil {
		return nil, fmt.Errorf("连不上 GitHub（容器可能没有外网或需要代理）：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNoRelease
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub 返回 %d", resp.StatusCode)
	}
	var rel ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("发布信息解析失败：%v", err)
	}
	return &rel, nil
}

// versionParts 把 1.2.0.5 / v1.2.0.6 拆成数字段，非数字当分隔符。
func versionParts(v string) []int {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v"))
	fields := strings.FieldsFunc(v, func(r rune) bool { return !unicode.IsDigit(r) })
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		n := 0
		for _, r := range f {
			n = n*10 + int(r-'0')
			if n > 1<<30 { // 防异常长的数字段
				n = 1 << 30
				break
			}
		}
		out = append(out, n)
	}
	return out
}

// compareVersions: a>b 返回 1，相等 0，a<b 返回 -1；缺的段按 0 算。
func compareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}

func pickAsset(rel *ghRelease) (ghAsset, bool) {
	want := updateAssetLinux
	if updateGOOS == "windows" {
		want = updateAssetWin
	}
	for _, a := range rel.Assets {
		if strings.EqualFold(a.Name, want) {
			return a, true
		}
	}
	return ghAsset{}, false
}

// handleUpdateCheck 只读：告诉前端「有没有新版、能不能在这儿装」。
// 出错也返回 200 + error 字段，前端能直接展示原因，不必区分 HTTP 语义。
func handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "method not allowed")
		return
	}
	out := map[string]interface{}{
		"current":   buildVersion,
		"repo":      updateRepoName(),
		"has_token": updateToken() != "",
		"has_proxy": updateProxy() != "",
		"proxy":     updateProxy(),
		"platform":  updateGOOS + "/" + updateGOARCH,
		"can_apply": updateGOOS == "linux" && updateGOARCH == "amd64",
	}
	rel, err := fetchLatestRelease()
	if err != nil {
		switch {
		case errors.Is(err, errNoRelease):
			if updateToken() == "" {
				out["error"] = "没找到发布版本。仓库若是私有的，需要在下面填写 GitHub Token；若还没有发布过，请先打一个版本标签。"
			} else {
				out["error"] = "这个仓库还没有发布版本，先在仓库里打一个 v 开头的标签。"
			}
		default:
			out["error"] = err.Error()
		}
		writeJSON(w, out)
		return
	}
	latest := strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v")
	out["latest"] = latest
	out["notes"] = rel.Body
	out["url"] = rel.HTMLURL
	if !rel.PublishedAt.IsZero() {
		out["published_at"] = rel.PublishedAt.Local().Format("2006-01-02 15:04")
	}
	out["has_update"] = compareVersions(latest, buildVersion) > 0
	if a, ok := pickAsset(rel); ok {
		out["asset"] = map[string]interface{}{"name": a.Name, "size": a.Size}
	} else {
		out["asset_missing"] = true
	}
	writeJSON(w, out)
}

// handleUpdateApply 下载 → 校验 → 交给脚本替换并重启。
func handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed")
		return
	}
	// 只读账号、店铺账号都不该有能力换程序：那等于换掉所有人的运行环境。
	if sessionRole(r) == "readonly" {
		writeError(w, 403, "只读账号不能更新程序")
		return
	}
	if authEnabled() && !isGlobalAdmin(r) {
		writeError(w, 403, "只有用管理密码登录的账号可以更新程序")
		return
	}
	if updateGOOS != "linux" || updateGOARCH != "amd64" {
		writeError(w, 400, "一键更新只支持 Linux (amd64) 容器；当前是 "+updateGOOS+"/"+updateGOARCH+"，请手动下载替换")
		return
	}
	rel, err := fetchLatestRelease()
	if err != nil {
		if errors.Is(err, errNoRelease) {
			writeError(w, 400, "没有可用的发布版本")
			return
		}
		writeError(w, 502, err.Error())
		return
	}
	latest := strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v")
	if compareVersions(latest, buildVersion) <= 0 {
		writeError(w, 400, "当前已经是最新版本（"+buildVersion+"）")
		return
	}
	asset, ok := pickAsset(rel)
	if !ok {
		writeError(w, 400, "这次发布里没有 "+updateAssetLinux+"，请检查发布产物")
		return
	}
	if asset.Size <= 0 || asset.Size > updateMaxBytes {
		writeError(w, 400, "安装包大小异常，已取消更新")
		return
	}

	exe, err := updateExePath()
	if err != nil {
		writeError(w, 500, "找不到程序路径："+err.Error())
		return
	}
	dir := filepath.Dir(exe)
	tmpDir := filepath.Join(dir, ".update")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		writeError(w, 500, "创建更新目录失败：可能是容器内没有写权限")
		return
	}
	tmpBin := filepath.Join(tmpDir, "xianyu-stat.new")

	sum, err := downloadAsset(asset, tmpBin)
	if err != nil {
		os.Remove(tmpBin)
		writeError(w, 502, err.Error())
		return
	}
	want, err := expectedDigest(rel, asset)
	if err != nil {
		os.Remove(tmpBin)
		writeError(w, 400, err.Error())
		return
	}
	if !strings.EqualFold(sum, want) {
		os.Remove(tmpBin)
		writeError(w, 400, "校验失败：下载到的文件和发布时的 sha256 不一致，已中止更新")
		return
	}
	if err := checkExecutable(tmpBin); err != nil {
		os.Remove(tmpBin)
		writeError(w, 400, err.Error())
		return
	}

	script := filepath.Join(tmpDir, "apply.sh")
	if err := writeApplyScript(script, dir, exe); err != nil {
		writeError(w, 500, "写更新脚本失败："+err.Error())
		return
	}
	if err := spawnDetached(script); err != nil {
		writeError(w, 500, "启动更新脚本失败："+err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{
		"ok": true, "from": buildVersion, "to": latest,
		"message": "已开始更新，程序会在几秒内重启",
	})
}

func downloadAsset(asset ghAsset, dest string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, asset.URL, nil)
	if err != nil {
		return "", err
	}
	if tok := updateToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "xianyu-stat/"+buildVersion)
	// 下载给足时间：几十兆的包在慢网下也可能要一两分钟
	resp, err := updateHTTPClient(3 * time.Minute).Do(req)
	if err != nil {
		return "", fmt.Errorf("下载失败（容器可能访问不了 GitHub）：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载失败：GitHub 返回 %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", fmt.Errorf("写入安装包失败：%v", err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, updateMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("下载中断：%v", err)
	}
	if n > updateMaxBytes {
		return "", errors.New("安装包超过大小上限，已中止")
	}
	if asset.Size > 0 && n != asset.Size {
		return "", fmt.Errorf("下载不完整：应为 %d 字节，实际 %d", asset.Size, n)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// expectedDigest 优先用 Release 自带的 digest，其次找同名 .sha256 附件。
func expectedDigest(rel *ghRelease, asset ghAsset) (string, error) {
	if d := strings.TrimSpace(asset.Digest); d != "" {
		d = strings.TrimPrefix(d, "sha256:")
		if len(d) == 64 {
			return d, nil
		}
	}
	companion := asset.Name + ".sha256"
	for _, a := range rel.Assets {
		if !strings.EqualFold(a.Name, companion) {
			continue
		}
		req, err := http.NewRequest(http.MethodGet, a.URL, nil)
		if err != nil {
			return "", err
		}
		if tok := updateToken(); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		req.Header.Set("User-Agent", "xianyu-stat/"+buildVersion)
		resp, err := updateHTTPClient(15 * time.Second).Do(req)
		if err != nil {
			return "", fmt.Errorf("读取校验文件失败：%v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("读取校验文件失败：GitHub 返回 %d", resp.StatusCode)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		fields := strings.Fields(string(raw))
		if len(fields) > 0 && len(fields[0]) == 64 {
			return fields[0], nil
		}
		return "", errors.New("校验文件格式不对，已取消更新")
	}
	return "", errors.New("这次发布没有提供 sha256 校验（digest 或 .sha256 附件），为了安全已取消更新")
}

// checkExecutable 确认下载到的是 Linux 可执行文件，避免把 HTML 错误页当程序装上去。
func checkExecutable(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil {
		return errors.New("安装包内容为空，已取消更新")
	}
	if string(head) != "\x7fELF" {
		return errors.New("安装包不是 Linux 可执行文件，已取消更新")
	}
	return nil
}

// writeApplyScript 生成替换脚本：先备份旧文件，mv 失败能原样退回。
func writeApplyScript(path, dir, exe string) error {
	script := fmt.Sprintf(`#!/bin/sh
# 由 xianyu-stat 自更新生成：等旧进程把 HTTP 响应发完，再替换并重启
sleep 2
cd %q || exit 1
cp -f %q %q.bak 2>/dev/null
mv -f .update/xianyu-stat.new %q || { cp -f %q.bak %q 2>/dev/null; exit 1; }
chmod 755 %q
if command -v systemctl >/dev/null 2>&1; then
  systemctl restart xianyu-stat
else
  kill -TERM %d 2>/dev/null
fi
`, dir, exe, exe, exe, exe, exe, exe, os.Getpid())
	return os.WriteFile(path, []byte(script), 0o755)
}

// handleUpdateConfig 保存仓库与 Token。只写设置表，不回显 Token。
func handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeError(w, 405, "method not allowed")
		return
	}
	if authEnabled() && !isGlobalAdmin(r) {
		writeError(w, 403, "只有用管理密码登录的账号可以修改更新设置")
		return
	}
	var in struct {
		Repo       string `json:"repo"`
		Token      string `json:"token"`
		ClearToken bool   `json:"clear_token"`
		Proxy      string `json:"proxy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, 400, "请求体解析失败")
		return
	}
	repo := strings.TrimSpace(in.Repo)
	if repo != "" {
		if !validRepoName(repo) {
			writeError(w, 400, "仓库格式应为 owner/name，例如 1316195118/Xianyu-Assistant")
			return
		}
		if err := setSetting("update_repo", repo); err != nil {
			writeError(w, 500, "保存失败")
			return
		}
	} else {
		setSetting("update_repo", "")
	}
	if in.ClearToken {
		setSetting("update_token", "")
	} else if t := strings.TrimSpace(in.Token); t != "" {
		setSetting("update_token", t)
	}
	if px := strings.TrimSpace(in.Proxy); px != "" {
		u, err := url.Parse(px)
		if err != nil || u.Scheme == "" || u.Host == "" {
			writeError(w, 400, "代理地址格式应为 http://host:port 或 socks5://host:port")
			return
		}
		if err := setSetting("update_proxy", px); err != nil {
			writeError(w, 500, "保存失败")
			return
		}
	} else {
		setSetting("update_proxy", "")
	}
	writeJSON(w, map[string]interface{}{"ok": true, "repo": updateRepoName(), "has_token": updateToken() != "", "has_proxy": updateProxy() != ""})
}

func validRepoName(s string) bool {
	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, p := range parts {
		for _, r := range p {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' && r != '.' {
				return false
			}
		}
	}
	return true
}
