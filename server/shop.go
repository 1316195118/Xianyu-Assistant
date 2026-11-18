package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 多店铺：一个闲鱼账号 = 一个店铺。数据按 shop_id 逻辑隔离（单库单表），
// 不走「每店一个 .db」——那需要把备份、导入导出、联单、全局搜索全部改造成多库路由，
// 而商品匹配这类跨表逻辑一旦漏改就是静默串店，排查成本远高于加一列。

const (
	// defaultShopID 默认店铺。存量数据按 DEFAULT 1 归入，升级后统计口径不变。
	defaultShopID int64 = 1
	// shopHeader 前端统一通过该请求头声明当前店铺。
	shopHeader = "X-Shop-Id"
)

// Shop 店铺。archived_at 非空表示已归档（不在切换器出现，数据保留）。
type Shop struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Color      string `json:"color"`
	Sort       int    `json:"sort"`
	Note       string `json:"note"`
	ArchivedAt  string `json:"archived_at"`
	HasPassword bool   `json:"has_password"` // 只暴露"有没有密码"，绝不返回哈希
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type ctxKey string

const shopCtxKey ctxKey = "xy-shop-id"

var errShopInvalid = errors.New("店铺参数非法")

// ensureDefaultShop 首次启动创建默认店铺，幂等。
func ensureDefaultShop() error {
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM shops").Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	ts := nowStr()
	_, err := db.Exec(`INSERT INTO shops(id,name,color,sort,note,archived_at,created_at,updated_at)
        VALUES(?,?,?,?,?,?,?,?)`, defaultShopID, "我的闲鱼", "#f5b400", 1, "", "", ts, ts)
	return err
}

// resolveShop 解析本次请求所属店铺。
//
//	无 X-Shop-Id（老书签、脚本、PWA 旧缓存）→ 回落默认店铺，保证兼容；
//	显式指定但非法/不存在/已归档   → 报错，绝不静默回落：
//	  静默回落会让用户以为在看 A 店、实际读的是默认店的数据，是这类功能最危险的失败模式。
func resolveShop(r *http.Request) (int64, error) {
	// 已登录会话优先：令牌里绑定的店铺就是唯一可访问的店铺。
	// 请求头只在没有会话时才有意义（外部脚本、导出直链、无密码店铺）。
	if shop, ok := sessionShop(r); ok {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM shops WHERE id=? AND archived_at=''", shop).Scan(&n); err == nil && n > 0 {
			return shop, nil
		}
	}
	raw := strings.TrimSpace(r.Header.Get(shopHeader))
	if raw == "" { // 兼容无法设置 header 的场景（下载链接、<a> 直链）
		raw = strings.TrimSpace(r.URL.Query().Get("shop"))
	}
	if raw == "" {
		return defaultShopID, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errShopInvalid
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM shops WHERE id=? AND archived_at=''", id).Scan(&n); err != nil {
		return 0, errors.New("店铺校验失败")
	}
	if n == 0 {
		return 0, errors.New("店铺不存在或已归档")
	}
	return id, nil
}

// shopID 取当前店铺。authWrap 已校验并写入 context，缺失时用默认店兜底
// （单元测试直接调 handler 时会走到这里）。
func shopID(r *http.Request) int64 {
	if v, ok := r.Context().Value(shopCtxKey).(int64); ok && v > 0 {
		return v
	}
	return defaultShopID
}

func withShop(r *http.Request, id int64) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), shopCtxKey, id))
}

// shopNameOf 取店铺名，用于导出文件名；查不到时退回空串（调用方自行兜底）。
func shopNameOf(id int64) string {
	var name string
	if err := db.QueryRow("SELECT name FROM shops WHERE id=?", id).Scan(&name); err != nil {
		return ""
	}
	return strings.TrimSpace(name)
}

// listShops 店铺列表（切换器数据源）。
func listShops() ([]Shop, error) {
	rows, err := db.Query(`SELECT id, name, color, sort, note, archived_at,
        COALESCE(password_hash,'')<>'' AS has_password, created_at, updated_at
        FROM shops WHERE archived_at='' ORDER BY sort, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Shop{}
	for rows.Next() {
		var s Shop
		if err := rows.Scan(&s.ID, &s.Name, &s.Color, &s.Sort, &s.Note,
			&s.ArchivedAt, &s.HasPassword, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// handleShops：GET /api/shops 列表；POST 新建；PUT/DELETE 走 /api/shops/{id}
func handleShops(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		shops, err := listShops()
		if err != nil {
			writeError(w, 500, "读取店铺失败")
			return
		}
		writeJSON(w, map[string]interface{}{"items": shops, "current": shopID(r)})
	case http.MethodPost:
		// 新建店铺是全局能力：用店铺密码登录的账号不该能造出新店
		if !isGlobalAdmin(r) {
			writeError(w, 403, "只有全局密码登录后可以新建店铺")
			return
		}
		var in struct {
			Name  string `json:"name"`
			Color string `json:"color"`
			Note  string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, 400, "请求体解析失败")
			return
		}
		name := strings.TrimSpace(in.Name)
		if name == "" {
			writeError(w, 400, "店铺名称不能为空")
			return
		}
		if utf8.RuneCountInString(name) > 30 {
			writeError(w, 400, "店铺名称最多 30 个字")
			return
		}
		color := strings.TrimSpace(in.Color)
		if !validShopColor(color) {
			color = "#f5b400"
		}
		var maxSort int
		db.QueryRow("SELECT COALESCE(MAX(sort),0) FROM shops").Scan(&maxSort)
		ts := nowStr()
		res, err := db.Exec(`INSERT INTO shops(name,color,sort,note,archived_at,created_at,updated_at)
            VALUES(?,?,?,?,?,?,?)`, name, color, maxSort+1, strings.TrimSpace(in.Note), "", ts, ts)
		if err != nil {
			writeError(w, 500, "创建店铺失败")
			return
		}
		id, _ := res.LastInsertId()
		writeJSON(w, map[string]interface{}{"ok": true, "id": id})
	default:
		writeError(w, 405, "方法不允许")
	}
}

// handleShopByID：PUT 改名/配色；DELETE 归档（软删，数据保留）
func handleShopByID(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/shops/"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, 400, "无效店铺 id")
		return
	}
	var exists int
	db.QueryRow("SELECT COUNT(*) FROM shops WHERE id=?", id).Scan(&exists)
	if exists == 0 {
		writeError(w, 404, "店铺不存在")
		return
	}
	// 越权保护：店铺会话只能改自己那一家；改别家必须全局密码会话。
	if !isGlobalAdmin(r) {
		if bound, ok := sessionShop(r); !ok || bound != id {
			writeError(w, 403, "无权操作其他店铺")
			return
		}
	}

	switch r.Method {
	case http.MethodPut:
		var in struct {
			Name     string `json:"name"`
			Color    string `json:"color"`
			Note     string `json:"note"`
			Sort     int    `json:"sort"`
			Password *string `json:"password"` // nil=不改；""=清除密码；其它=设置新密码
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, 400, "请求体解析失败")
			return
		}
		name := strings.TrimSpace(in.Name)
		if name == "" {
			writeError(w, 400, "店铺名称不能为空")
			return
		}
		if utf8.RuneCountInString(name) > 30 {
			writeError(w, 400, "店铺名称最多 30 个字")
			return
		}
		color := strings.TrimSpace(in.Color)
		if !validShopColor(color) {
			color = "#f5b400"
		}
		sort := in.Sort
		if sort <= 0 {
			sort = 1
		}
		if _, err := db.Exec("UPDATE shops SET name=?, color=?, note=?, sort=?, updated_at=? WHERE id=?",
			name, color, strings.TrimSpace(in.Note), sort, nowStr(), id); err != nil {
			writeError(w, 500, "保存失败")
			return
		}
		// 密码单独处理：不传表示不变，空串表示清除
		if in.Password != nil {
			pw := *in.Password
			hash := ""
			if pw != "" {
				if len([]rune(pw)) < 4 {
					writeError(w, 400, "密码至少 4 位")
					return
				}
				h, err := hashPassword(pw, "")
				if err != nil {
					writeError(w, 500, "密码保存失败")
					return
				}
				hash = h
			}
			if _, err := db.Exec("UPDATE shops SET password_hash=?, updated_at=? WHERE id=?", hash, nowStr(), id); err != nil {
				writeError(w, 500, "密码保存失败")
				return
			}
			// 店铺密码变更后，旧会话立即作废
			bumpAuthVersion()
		}
		writeJSON(w, map[string]interface{}{"ok": true})
	case http.MethodDelete:
		// 默认店铺是缺省回落目标，归档后老客户端会 400，禁止。
		if id == defaultShopID {
			writeError(w, 400, "默认店铺不能删除")
			return
		}
		if _, err := db.Exec("UPDATE shops SET archived_at=?, updated_at=? WHERE id=?",
			nowStr(), nowStr(), id); err != nil {
			writeError(w, 500, "删除失败")
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true})
	default:
		writeError(w, 405, "方法不允许")
	}
}

// validShopColor 只接受 #RGB / #RRGGBB，避免把任意字符串写进样式。
func validShopColor(c string) bool {
	if c == "" {
		return false
	}
	if len(c) != 4 && len(c) != 7 {
		return false
	}
	if c[0] != '#' {
		return false
	}
	for _, r := range c[1:] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}
