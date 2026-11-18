package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 删除撤销：删除类接口在执行前把被删的整行 + 相关 group_items 关联存成快照，
// 返回一个 token；前端凭 token 调 /api/undo 把数据原样插回（显式 id 的原生 INSERT），
// 绝不复用 createRecord/insertItem 等会触发自动配对与联单合并的写入路径。

type undoSnapshot struct {
	Op         string           `json:"op"`
	Items      []map[string]any `json:"items,omitempty"`
	GroupItems []map[string]any `json:"group_items,omitempty"`
	Groups     []map[string]any `json:"groups,omitempty"`
	ShipTags   []map[string]any `json:"ship_tags,omitempty"`   // import_replace：ship_tags 全表
	BackupName string           `json:"backup_name,omitempty"` // delete_backup：备份文件名
	BackupData string           `json:"backup_data,omitempty"` // delete_backup：文件内容 base64
}

// errUndoConflict 表示恢复的目标 id 已被占用，handleUndo 会整体回滚并返回 409。
var errUndoConflict = errors.New("undo: id conflict")

// initUndoTable 幂等建表，并执行容量治理（尽力而为）。
func initUndoTable() error {
	q := `
CREATE TABLE IF NOT EXISTS undo_ops (
    token      TEXT PRIMARY KEY,
    shop_id    INTEGER NOT NULL DEFAULT 1,  -- 撤销记录归属店铺，避免 A 店撤销到 B 店的删除
    op         TEXT NOT NULL,
    snapshot   TEXT NOT NULL,
    created_at TEXT NOT NULL,
    consumed   INTEGER NOT NULL DEFAULT 0
);
`
	if _, err := db.Exec(q); err != nil {
		return err
	}
	// 旧库升级：幂等补 shop_id
	var n int
	db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('undo_ops') WHERE name='shop_id'").Scan(&n)
	if n == 0 {
		if _, err := db.Exec("ALTER TABLE undo_ops ADD COLUMN shop_id INTEGER NOT NULL DEFAULT 1"); err != nil {
			return err
		}
	}
	db.Exec("CREATE INDEX IF NOT EXISTS idx_undo_shop ON undo_ops(shop_id, created_at)")
	pruneUndoOps()
	return nil
}

// pruneUndoOps 撤销容量治理：
//  1) 删除 7 天前的所有记录；
//  2) 其余按「未消费 + 新」优先保留，超过 200 条或 snapshot 累计超过 64MB 时，
//     删除优先级低的记录（已消费、较旧优先被清）。
//
// 排序 consumed ASC, created_at DESC 即「保留优先级从高到低」；
// 第一条（最新）始终保留，避免刚生成的撤销被自身清理误删。
func pruneUndoOps() {
	cutoff := time.Now().AddDate(0, 0, -7).Format("2006-01-02 15:04:05")
	_, _ = db.Exec("DELETE FROM undo_ops WHERE created_at < ?", cutoff)

	rows, err := db.Query("SELECT token, LENGTH(snapshot) FROM undo_ops ORDER BY consumed ASC, created_at DESC")
	if err != nil {
		return
	}
	defer rows.Close()
	toDelete := []string{}
	kept := 0
	var keptSize int64
	for rows.Next() {
		var token string
		var sz int64
		if rows.Scan(&token, &sz) != nil {
			continue
		}
		if kept >= 200 || (kept > 0 && keptSize+sz > 64<<20) {
			toDelete = append(toDelete, token)
		} else {
			keptSize += sz
			kept++
		}
	}
	rows.Close() // 提前释放连接，便于后续 DELETE
	for _, token := range toDelete {
		_, _ = db.Exec("DELETE FROM undo_ops WHERE token=?", token)
	}
}

// saveUndo 生成唯一 token，序列化快照写入 undo_ops 并返回 token。
func saveUndo(op string, snap interface{}, shopID int64) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	data, err := json.Marshal(snap)
	if err != nil {
		return "", err
	}
	if _, err := db.Exec("INSERT INTO undo_ops(token, shop_id, op, snapshot, created_at) VALUES(?,?,?,?,?)",
		token, shopID, op, string(data), nowStr()); err != nil {
		return "", err
	}
	// 每次写入后做一次容量治理（尽力而为；saveUndo 均在事务外调用）。
	pruneUndoOps()
	return token, nil
}

// queryMaps 把查询结果按列名取成 map 列表：[]byte 转 string，其余保留驱动原类型。
// 删除前用它把整行读成 map，避免依赖固定的列顺序或结构。
func queryMaps(query string, args ...any) ([]map[string]any, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			switch v := vals[i].(type) {
			case []byte:
				m[c] = string(v)
			default:
				m[c] = v
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// insertMaps 用显式列名生成 INSERT INTO <table>(cols...) VALUES(?,...) 并逐行写入。
// 列名与取值都按同一份 cols 顺序，保证即使 map 无序也能正确对应。
func insertMaps(tx *sql.Tx, table string, cols []string, rows []map[string]any) error {
	return insertMapsVerb(tx, "INSERT INTO", table, cols, rows)
}

// insertMapsOrIgnore 同 insertMaps，但用 INSERT OR IGNORE：给带 UNIQUE / 联合主键的
// ship_tags、group_items 插回快照时用，避免重复键让整笔撤销失败。
func insertMapsOrIgnore(tx *sql.Tx, table string, cols []string, rows []map[string]any) error {
	return insertMapsVerb(tx, "INSERT OR IGNORE INTO", table, cols, rows)
}

func insertMapsVerb(tx *sql.Tx, verb, table string, cols []string, rows []map[string]any) error {
	if len(cols) == 0 || len(rows) == 0 {
		return nil
	}
	ph := make([]string, len(cols))
	for i := range cols {
		ph[i] = "?"
	}
	q := verb + " " + table + "(" + strings.Join(cols, ",") + ") VALUES(" + strings.Join(ph, ",") + ")"
	stmt, err := tx.Prepare(q)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, row := range rows {
		args := make([]any, len(cols))
		for i, c := range cols {
			args[i] = row[c]
		}
		if _, err := stmt.Exec(args...); err != nil {
			return err
		}
	}
	return nil
}

// idsWhere 按待删除 id 生成 WHERE 子句：单条用等值，多条用 IN (?,...)（占位符数量与 ids 一致）。
func idsWhere(col string, ids []int64) (string, []any) {
	if len(ids) == 1 {
		return col + "=?", []any{ids[0]}
	}
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	return col + " IN (" + strings.Join(ph, ",") + ")", args
}

// snapshotItemsUndo 在删除 items 前取整行 + 相关 group_items 关联，保存快照并返回 token。
// 失败只记日志并返回空 token，绝不阻断删除。
func snapshotItemsUndo(op string, ids []int64, shopID int64) string {
	snap := undoSnapshot{Op: op}
	if len(ids) > 0 {
		itemWhere, args := idsWhere("id", ids)
		// 快照也只取本店行：即使上游传入了别家 id，也不会被快照带出、再被撤销插回本店。
		if rows, err := queryMaps("SELECT * FROM items WHERE "+itemWhere+" AND shop_id=?", append(args, shopID)...); err == nil {
			snap.Items = rows
		} else {
			log.Printf("撤销快照 items 读取失败(%s): %v", op, err)
		}
		giWhere, giArgs := idsWhere("item_id", ids)
		if rows, err := queryMaps("SELECT * FROM group_items WHERE "+giWhere, giArgs...); err == nil {
			snap.GroupItems = rows
		} else {
			log.Printf("撤销快照 group_items 读取失败(%s): %v", op, err)
		}
	}
	token, err := saveUndo(op, snap, shopID)
	if err != nil {
		log.Printf("保存撤销记录失败: %v", err)
		return ""
	}
	return token
}

// snapshotGroupUndo 在删除联单前取 link_groups 行 + group_items 关联，保存快照并返回 token。
func snapshotGroupUndo(op string, groupID int64, shopID int64) string {
	snap := undoSnapshot{Op: op}
	if rows, err := queryMaps("SELECT * FROM link_groups WHERE id=?", groupID); err == nil {
		snap.Groups = rows
	} else {
		log.Printf("撤销快照 link_groups 读取失败: %v", err)
	}
	if rows, err := queryMaps("SELECT * FROM group_items WHERE group_id=?", groupID); err == nil {
		snap.GroupItems = rows
	} else {
		log.Printf("撤销快照 group_items 读取失败: %v", err)
	}
	token, err := saveUndo(op, snap, shopID)
	if err != nil {
		log.Printf("保存撤销记录失败: %v", err)
		return ""
	}
	return token
}

// decodeUndoSnapshot 用 UseNumber 反序列化，避免 JSON 数字全变 float64 导致整数 id 精度/类型问题，
// 再把 json.Number 归一化成 int64 / float64 供数据库绑定使用。
func decodeUndoSnapshot(data string) (*undoSnapshot, error) {
	var snap undoSnapshot
	dec := json.NewDecoder(strings.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&snap); err != nil {
		return nil, err
	}
	normalizeUndoMaps(snap.Items)
	normalizeUndoMaps(snap.GroupItems)
	normalizeUndoMaps(snap.Groups)
	normalizeUndoMaps(snap.ShipTags)
	return &snap, nil
}

func normalizeUndoMaps(rows []map[string]any) {
	for _, m := range rows {
		for k, v := range m {
			if num, ok := v.(json.Number); ok {
				if i, err := num.Int64(); err == nil {
					m[k] = i
				} else if f, err := num.Float64(); err == nil {
					m[k] = f
				} else {
					m[k] = num.String()
				}
			}
		}
	}
}

// mapInt64 从快照 map 里取整数 id（兼容归一化后的 int64/float64 及防御性的 string）。
func mapInt64(m map[string]any, key string) (int64, bool) {
	v, ok := m[key]
	if !ok || v == nil {
		return 0, false
	}
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case float64:
		return int64(x), true
	case json.Number:
		i, err := x.Int64()
		return i, err == nil
	case string:
		i, err := strconv.ParseInt(x, 10, 64)
		return i, err == nil
	default:
		return 0, false
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// restoreGroupItems 用 INSERT OR IGNORE 把 group_items 关联插回，避免重复主键。
func restoreGroupItems(tx *sql.Tx, rows []map[string]any) error {
	for _, gi := range rows {
		gid, ok1 := mapInt64(gi, "group_id")
		iid, ok2 := mapInt64(gi, "item_id")
		if !ok1 || !ok2 {
			continue
		}
		if _, err := tx.Exec("INSERT OR IGNORE INTO group_items(group_id,item_id) VALUES(?,?)", gid, iid); err != nil {
			return err
		}
	}
	return nil
}

// restoreDeletedItems 恢复 delete_one / delete_batch：先逐个检查 items.id 冲突，
// 再按 map 列名显式插回（含 id、created_at、updated_at、product_norm 等全部列），最后插回关联。
func restoreDeletedItems(tx *sql.Tx, snap *undoSnapshot) (int, error) {
	for _, it := range snap.Items {
		id, ok := mapInt64(it, "id")
		if !ok {
			return 0, fmt.Errorf("撤销快照缺少 items.id")
		}
		var n int
		if err := tx.QueryRow("SELECT COUNT(*) FROM items WHERE id=?", id).Scan(&n); err != nil {
			return 0, err
		}
		if n > 0 {
			return 0, errUndoConflict
		}
	}
	if len(snap.Items) > 0 {
		if err := insertMaps(tx, "items", sortedKeys(snap.Items[0]), snap.Items); err != nil {
			return 0, err
		}
	}
	if err := restoreGroupItems(tx, snap.GroupItems); err != nil {
		return 0, err
	}
	return len(snap.Items), nil
}

// restoreDeletedGroup 恢复 delete_group：先检查 link_groups.id 冲突，再插回 Groups（含 settled_at 等全部列）与关联。
func restoreDeletedGroup(tx *sql.Tx, snap *undoSnapshot) (int, error) {
	for _, g := range snap.Groups {
		id, ok := mapInt64(g, "id")
		if !ok {
			return 0, fmt.Errorf("撤销快照缺少 link_groups.id")
		}
		var n int
		if err := tx.QueryRow("SELECT COUNT(*) FROM link_groups WHERE id=?", id).Scan(&n); err != nil {
			return 0, err
		}
		if n > 0 {
			return 0, errUndoConflict
		}
	}
	if len(snap.Groups) > 0 {
		if err := insertMaps(tx, "link_groups", sortedKeys(snap.Groups[0]), snap.Groups); err != nil {
			return 0, err
		}
	}
	if err := restoreGroupItems(tx, snap.GroupItems); err != nil {
		return 0, err
	}
	return len(snap.Groups), nil
}

// snapshotBackupUndo 在删除备份文件前把文件全部字节 base64 存成快照并返回 token。
// 文件超过 64MB、读取或保存失败只记日志并返回空 token，绝不阻断删除。
func snapshotBackupUndo(name, path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("撤销快照读取备份失败(%s): %v", name, err)
		return ""
	}
	if len(data) > 64<<20 {
		log.Printf("备份 %s 超过 64MB，不提供撤销", name)
		return ""
	}
	snap := undoSnapshot{
		Op:         "delete_backup",
		BackupName: name,
		BackupData: base64.StdEncoding.EncodeToString(data),
	}
	token, err := saveUndo("delete_backup", snap, defaultShopID)
	if err != nil {
		log.Printf("保存撤销记录失败(delete_backup): %v", err)
		return ""
	}
	return token
}

// restoreDeletedBackup 把 delete_backup 快照里的文件内容写回备份目录同名文件。
// 沿用 handleBackupFile 的文件名强校验，防快照被篡改导致路径穿越。
func restoreDeletedBackup(snap *undoSnapshot) (int, error) {
	if !validBackupName(snap.BackupName) {
		return 0, fmt.Errorf("撤销快照中的备份文件名无效")
	}
	data, err := base64.StdEncoding.DecodeString(snap.BackupData)
	if err != nil {
		return 0, fmt.Errorf("撤销快照备份内容解码失败: %w", err)
	}
	dir := backupDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(dir, snap.BackupName), data, 0644); err != nil {
		return 0, err
	}
	return 1, nil
}

// restoreImportReplace 恢复 replace 覆盖导入：在一个事务里先清空四张表，再按快照
// 显式列名逐表插回（含原 id）。items / link_groups 用普通 INSERT 并检查主键冲突，
// ship_tags / group_items 用 INSERT OR IGNORE 规避唯一键误冲突。
// 语义是「恢复到导入前状态」：导入后新增/编辑的数据随清空一并丢弃。
func restoreImportReplace(tx *sql.Tx, snap *undoSnapshot, shopID int64) (int, error) {
	// 显式 id 插回前检查是否被当前库占用，冲突整体回滚（与删除类撤销一致，返回 409）。
	for _, it := range snap.Items {
		id, ok := mapInt64(it, "id")
		if !ok {
			return 0, fmt.Errorf("撤销快照缺少 items.id")
		}
		var n int
		if err := tx.QueryRow("SELECT COUNT(*) FROM items WHERE id=?", id).Scan(&n); err != nil {
			return 0, err
		}
		if n > 0 {
			return 0, errUndoConflict
		}
	}
	for _, g := range snap.Groups {
		id, ok := mapInt64(g, "id")
		if !ok {
			return 0, fmt.Errorf("撤销快照缺少 link_groups.id")
		}
		var n int
		if err := tx.QueryRow("SELECT COUNT(*) FROM link_groups WHERE id=?", id).Scan(&n); err != nil {
			return 0, err
		}
		if n > 0 {
			return 0, errUndoConflict
		}
	}
	// 只清空当前店铺：多店共库后，一次导入回退绝不能影响其它店铺的数据。
	// ship_tags 是全局共享配置，不在此清空（快照里的标签用 INSERT OR IGNORE 补回）。
	for _, q := range []string{
		"DELETE FROM group_items WHERE group_id IN (SELECT id FROM link_groups WHERE shop_id=?)",
		"DELETE FROM items WHERE shop_id=?",
		"DELETE FROM link_groups WHERE shop_id=?",
	} {
		if _, err := tx.Exec(q, shopID); err != nil {
			return 0, err
		}
	}
	if len(snap.Items) > 0 {
		if err := insertMaps(tx, "items", sortedKeys(snap.Items[0]), snap.Items); err != nil {
			return 0, err
		}
	}
	if len(snap.Groups) > 0 {
		if err := insertMaps(tx, "link_groups", sortedKeys(snap.Groups[0]), snap.Groups); err != nil {
			return 0, err
		}
	}
	if len(snap.ShipTags) > 0 {
		if err := insertMapsOrIgnore(tx, "ship_tags", sortedKeys(snap.ShipTags[0]), snap.ShipTags); err != nil {
			return 0, err
		}
	}
	if len(snap.GroupItems) > 0 {
		if err := insertMapsOrIgnore(tx, "group_items", sortedKeys(snap.GroupItems[0]), snap.GroupItems); err != nil {
			return 0, err
		}
	}
	return len(snap.Items) + len(snap.Groups) + len(snap.ShipTags) + len(snap.GroupItems), nil
}

// handleUndo：POST /api/undo {token}，在一个事务里按 op 分派恢复，成功后标记 consumed。
func handleUndo(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	defer refreshPurchaseIndex()

	var in struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, 400, "请求体解析失败")
		return
	}
	in.Token = strings.TrimSpace(in.Token)
	if in.Token == "" {
		writeError(w, 400, "撤销已失效或已被使用")
		return
	}

	var op, snapshot string
	var consumed int
	// 带 shop_id：别家店铺的撤销令牌在这里直接查不到，表现为 404，不会被跨店重放。
	err := db.QueryRow("SELECT op, snapshot, consumed FROM undo_ops WHERE token=? AND shop_id=?", in.Token, shopID(r)).
		Scan(&op, &snapshot, &consumed)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "撤销已失效或已被使用")
		return
	}
	if err != nil {
		writeError(w, 500, "查询撤销记录失败")
		return
	}
	if consumed != 0 {
		writeError(w, 400, "撤销已失效或已被使用")
		return
	}
	switch op {
	case "delete_one", "delete_batch", "delete_group", "import_replace", "delete_backup":
	default:
		writeError(w, 400, "未知的撤销类型")
		return
	}

	snap, err := decodeUndoSnapshot(snapshot)
	if err != nil {
		writeError(w, 500, "撤销快照解析失败")
		return
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, 500, "开启事务失败")
		return
	}
	var restored int
	switch op {
	case "delete_one", "delete_batch":
		restored, err = restoreDeletedItems(tx, snap)
	case "delete_group":
		restored, err = restoreDeletedGroup(tx, snap)
	case "import_replace":
		restored, err = restoreImportReplace(tx, snap, shopID(r))
	case "delete_backup":
		restored, err = restoreDeletedBackup(snap)
	}
	if err != nil {
		tx.Rollback()
		if errors.Is(err, errUndoConflict) {
			writeError(w, 409, "记录 id 已存在，无法恢复")
		} else {
			writeError(w, 500, "恢复失败: "+err.Error())
		}
		return
	}
	if err := tx.Commit(); err != nil {
		tx.Rollback()
		writeError(w, 500, "提交失败")
		return
	}
	if _, err := db.Exec("UPDATE undo_ops SET consumed=1 WHERE token=?", in.Token); err != nil {
		log.Printf("标记撤销记录已消费失败: %v", err)
	}
	writeJSON(w, map[string]interface{}{"ok": true, "restored": restored})
}
