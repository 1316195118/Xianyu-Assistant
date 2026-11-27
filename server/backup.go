package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// backupDir 返回备份目录：优先 BACKUP_DIR 环境变量，否则 DB 同目录下的 backups/
func backupDir() string {
	if d := os.Getenv("BACKUP_DIR"); d != "" {
		return d
	}
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "xianyu.db"
	}
	return filepath.Join(filepath.Dir(dbPath), "backups")
}

func backupEnabled() bool { return getSetting("backup_enabled") == "1" }

func backupIntervalHours() int {
	h, _ := strconv.Atoi(getSetting("backup_interval_hours"))
	if h < 1 {
		h = 24
	}
	if h > 720 {
		h = 720
	}
	return h
}

func backupKeep() int {
	n, _ := strconv.Atoi(getSetting("backup_keep"))
	if n < 1 {
		n = 7
	}
	if n > 90 {
		n = 90
	}
	return n
}

// createBackup 用 SQLite 的 VACUUM INTO 生成一致性快照（不锁库、不中断服务）
func createBackup(tag string) (string, error) {
	dir := backupDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("xianyu-%s-%s.db", time.Now().Format("20060102-150405"), tag)
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		os.Remove(path) // 同秒重名，覆盖
	}
	// VACUUM INTO 不支持绑定参数，路径由本函数生成，转义单引号即可
	escaped := strings.ReplaceAll(path, "'", "''")
	if _, err := db.Exec("VACUUM INTO '" + escaped + "'"); err != nil {
		return "", err
	}
	setSetting("backup_last", time.Now().Format("2006-01-02 15:04:05"))
	pruneBackups()
	return name, nil
}

type BackupFile struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Created string `json:"created"`
}

func listBackupFiles() []BackupFile {
	dir := backupDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []BackupFile{}
	}
	out := []BackupFile{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "xianyu-") || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupFile{
			Name: e.Name(), Size: info.Size(),
			Created: info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out
}

func pruneBackups() {
	files := listBackupFiles()
	keep := backupKeep()
	for i, f := range files {
		if i >= keep {
			os.Remove(filepath.Join(backupDir(), f.Name))
		}
	}
}

// backupLoop 定时自动备份，启动时先检查一次
func backupLoop() {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-shutdownCh:
			return
		case <-ticker.C:
		}
		if !backupEnabled() {
			continue
		}
		last, _ := time.ParseInLocation("2006-01-02 15:04:05", getSetting("backup_last"), time.Local)
		if time.Since(last) < time.Duration(backupIntervalHours())*time.Hour {
			continue
		}
		if _, err := createBackup("auto"); err != nil {
			fmt.Println("[自动备份失败]", err)
		}
	}
}

func handleBackups(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		writeJSON(w, map[string]interface{}{
			"enabled":        backupEnabled(),
			"interval_hours": backupIntervalHours(),
			"keep":           backupKeep(),
			"last":           getSetting("backup_last"),
			"dir":            backupDir(),
			"files":          listBackupFiles(),
		})
	case "POST":
		name, err := createBackup("manual")
		if err != nil {
			writeError(w, 500, "备份失败: "+err.Error())
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "name": name})
	case "PUT":
		var in struct {
			Enabled       *bool `json:"enabled"`
			IntervalHours *int  `json:"interval_hours"`
			Keep          *int  `json:"keep"`
		}
		if !decodeJSONBody(w, r, &in, maxBackupJSONBody) {
			return
		}
		if in.Enabled != nil {
			v := "0"
			if *in.Enabled {
				v = "1"
			}
			setSetting("backup_enabled", v)
		}
		if in.IntervalHours != nil {
			setSetting("backup_interval_hours", strconv.Itoa(*in.IntervalHours))
		}
		if in.Keep != nil {
			setSetting("backup_keep", strconv.Itoa(*in.Keep))
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		writeError(w, 405, "method not allowed")
	}
}

// validBackupName 校验备份文件名：前缀/后缀固定且不含路径分隔符，防目录穿越。
// 删除接口与撤销恢复写回共用同一份校验。
func validBackupName(name string) bool {
	return name != "" && !strings.ContainsAny(name, `/\`) &&
		strings.HasPrefix(name, "xianyu-") && strings.HasSuffix(name, ".db")
}

// handleBackupFile 下载 / 删除单个备份。文件名做严格校验，防目录穿越。
func handleBackupFile(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/backups/")
	if !validBackupName(name) {
		writeError(w, 400, "无效文件名")
		return
	}
	path := filepath.Join(backupDir(), name)
	switch r.Method {
	case "GET":
		f, err := os.Open(path)
		if err != nil {
			writeError(w, 404, "备份不存在")
			return
		}
		defer f.Close()
		info, _ := f.Stat()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		http.ServeContent(w, r, name, info.ModTime(), f)
	case "DELETE":
		// 删除前把文件内容存成撤销快照；超 64MB 或读失败只记日志，不阻断删除。
		undoToken := snapshotBackupUndo(name, path)
		if err := os.Remove(path); err != nil {
			writeError(w, 404, "备份不存在")
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "undo_token": undoToken})
	default:
		writeError(w, 405, "method not allowed")
	}
}
