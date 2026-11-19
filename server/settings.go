package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ---------- 应用设置（识别 API 前端配置）----------

func getSetting(key string) string {
	var v string
	if err := db.QueryRow("SELECT value FROM settings WHERE key=?", key).Scan(&v); err != nil {
		return ""
	}
	return v
}

func setSetting(key, value string) error {
	_, err := db.Exec("INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}

// effectiveEngine：识别引擎，local(本地 OCR 默认) | vision(视觉大模型)
func effectiveEngine() string {
	if v := getSetting("ocr_engine"); v == "vision" {
		return "vision"
	}
	if os.Getenv("OCR_ENGINE") == "vision" {
		return "vision"
	}
	return "local"
}

// effectiveOCRConfig：数据库设置优先，未设置则回退环境变量
func effectiveOCRConfig() (base, key, model string) {
	base = getSetting("ocr_api_base")
	if base == "" {
		base = os.Getenv("OCR_API_BASE")
	}
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	key = getSetting("ocr_api_key")
	if key == "" {
		key = os.Getenv("OCR_API_KEY")
	}
	model = getSetting("ocr_model")
	if model == "" {
		model = os.Getenv("OCR_MODEL")
	}
	if model == "" {
		model = "gpt-4o-mini"
	}
	return
}

func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 8 {
		return strings.Repeat("*", len(k))
	}
	return k[:4] + strings.Repeat("*", 6) + k[len(k)-4:]
}

func handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		base, key, model := effectiveOCRConfig()
		source := "env"
		if getSetting("ocr_api_key") != "" || getSetting("ocr_api_base") != "" || getSetting("ocr_model") != "" {
			source = "db"
		}
		writeJSON(w, map[string]interface{}{
			"ocr_engine":   effectiveEngine(),
			"ocr_api_base": base,
			"ocr_model":    model,
			"has_key":      key != "",
			"masked_key":   maskKey(key),
			"source":       source,
			"env_fallback": os.Getenv("OCR_API_KEY") != "",
			"has_readonly": getSetting("readonly_password_hash") != "",
		})
	case "PUT":
		var in struct {
			OCREngine             string  `json:"ocr_engine"`
			OCRAPIBase            *string `json:"ocr_api_base"`
			OCRAPIKey             string  `json:"ocr_api_key"`
			OCRModel              *string `json:"ocr_model"`
			ClearKey              bool    `json:"clear_key"`
			ReadOnlyPassword      string  `json:"readonly_password"`
			ClearReadOnlyPassword bool    `json:"clear_readonly_password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, 400, "请求体解析失败")
			return
		}
		if in.OCREngine == "local" || in.OCREngine == "vision" {
			if err := setSetting("ocr_engine", in.OCREngine); err != nil {
				writeError(w, 500, "保存失败")
				return
			}
		}
		if in.OCRAPIBase != nil {
			if err := setSetting("ocr_api_base", strings.TrimSpace(*in.OCRAPIBase)); err != nil {
				writeError(w, 500, "保存失败")
				return
			}
		}
		if in.OCRModel != nil {
			if err := setSetting("ocr_model", strings.TrimSpace(*in.OCRModel)); err != nil {
				writeError(w, 500, "保存失败")
				return
			}
		}
		if in.ClearKey {
			setSetting("ocr_api_key", "")
		} else if strings.TrimSpace(in.OCRAPIKey) != "" {
			setSetting("ocr_api_key", strings.TrimSpace(in.OCRAPIKey))
		}
		// 只读密码：存 settings 表的 readonly_password_hash（+ 独立 salt）。
		// 必须先有管理密码：否则「只读」这个限制没有主语，任何人都能直接读写，
		// 只读密码只会变成一个看似安全、实际毫无边界的入口。
		if in.ClearReadOnlyPassword {
			clearReadonlyPassword()
		} else if in.ReadOnlyPassword != "" {
			if !authEnabled() {
				writeError(w, 400, "请先设置管理密码（访问密码），再设置只读密码")
				return
			}
			if len([]rune(in.ReadOnlyPassword)) < 4 {
				writeError(w, 400, "只读密码至少 4 位")
				return
			}
			if err := setReadonlyPassword(in.ReadOnlyPassword); err != nil {
				writeError(w, 500, "保存失败")
				return
			}
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		writeError(w, 405, "method not allowed")
	}
}

// handleSettingsTest 用当前生效配置发一个最小文本请求，验证端点/Key/模型
func handleSettingsTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	base, key, model := effectiveOCRConfig()
	if key == "" {
		writeError(w, 400, "尚未配置 API Key")
		return
	}
	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": "回复 OK 两个字母即可"},
		},
		"max_tokens":  10,
		"temperature": 0,
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", strings.TrimRight(base, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeError(w, 502, "连接失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		writeError(w, 502, fmt.Sprintf("接口返回 %d: %s", resp.StatusCode, truncate(string(rb), 300)))
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "model": model, "message": "连接正常，识别功能可用"})
}

// ---------- 运费标签 ----------

func handleTags(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		rows, err := db.Query("SELECT id, name FROM ship_tags ORDER BY id")
		if err != nil {
			writeError(w, 500, "查询失败")
			return
		}
		defer rows.Close()
		tags := []map[string]interface{}{}
		for rows.Next() {
			var id int64
			var name string
			rows.Scan(&id, &name)
			tags = append(tags, map[string]interface{}{"id": id, "name": name})
		}
		writeJSON(w, tags)
	case "POST":
		var in struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, 400, "请求体解析失败")
			return
		}
		name := strings.TrimSpace(in.Name)
		if name == "" {
			writeError(w, 400, "标签名不能为空")
			return
		}
		res, err := db.Exec("INSERT OR IGNORE INTO ship_tags(name, created_at) VALUES(?,?)", name, nowStr())
		if err != nil {
			writeError(w, 500, "新增失败")
			return
		}
		id, _ := res.LastInsertId()
		writeJSON(w, map[string]interface{}{"id": id, "name": name})
	default:
		writeError(w, 405, "method not allowed")
	}
}

func handleTagByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != "DELETE" {
		writeError(w, 405, "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/tags/")
	if id == "" {
		writeError(w, 400, "无效 id")
		return
	}
	if _, err := db.Exec("DELETE FROM ship_tags WHERE id=?", id); err != nil {
		writeError(w, 500, "删除失败")
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
