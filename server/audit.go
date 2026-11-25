package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

type auditCapture struct {
	Action     string
	EntityType string
	EntityID   int64
	Summary    string
	Detail     map[string]interface{}
}

type auditContextKey struct{}

func auditCaptureFromRequest(r *http.Request) *auditCapture {
	value, _ := r.Context().Value(auditContextKey{}).(*auditCapture)
	return value
}

func withAuditCapture(r *http.Request) (*http.Request, *auditCapture) {
	audit := &auditCapture{}
	return r.WithContext(context.WithValue(r.Context(), auditContextKey{}, audit)), audit
}

func setAudit(r *http.Request, action, entityType string, entityID int64, summary string, detail map[string]interface{}) {
	audit := auditCaptureFromRequest(r)
	if audit == nil {
		return
	}
	if action != "" {
		audit.Action = action
	}
	if entityType != "" {
		audit.EntityType = entityType
	}
	if entityID > 0 {
		audit.EntityID = entityID
	}
	if summary != "" {
		audit.Summary = summary
	}
	if detail != nil {
		audit.Detail = detail
	}
}

func createAuditTable() error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS operation_audit (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    shop_id     INTEGER NOT NULL DEFAULT 1,
    actor       TEXT NOT NULL DEFAULT '',
    client_id   TEXT NOT NULL DEFAULT '',
    action      TEXT NOT NULL DEFAULT '',
    entity_type TEXT NOT NULL DEFAULT '',
    entity_id   INTEGER NOT NULL DEFAULT 0,
    summary     TEXT NOT NULL DEFAULT '',
    detail      TEXT NOT NULL DEFAULT '{}',
    created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_operation_audit_shop_time
    ON operation_audit(shop_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_operation_audit_entity
    ON operation_audit(shop_id, entity_type, entity_id, id DESC);
`)
	return err
}

func auditActor(r *http.Request) string {
	role := "店铺会话"
	if isGlobalAdmin(r) {
		role = "管理员"
	} else if sessionRole(r) == "readonly" {
		role = "只读账号"
	}
	device := strings.ToUpper(strings.TrimSpace(clientID(r)))
	if len(device) > 6 {
		device = device[len(device)-6:]
	}
	if device == "" {
		return role
	}
	return role + " · 设备 " + device
}

func inferAuditEntity(path string) (string, int64) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[0] != "api" {
		return "system", 0
	}
	entity := strings.TrimSuffix(parts[1], "s")
	var entityID int64
	for _, p := range parts[2:] {
		n, err := strconv.ParseInt(p, 10, 64)
		if err == nil && n > 0 {
			entityID = n
			break
		}
	}
	switch parts[1] {
	case "records":
		entity = "record"
	case "groups":
		entity = "group"
	case "operations":
		entity = "operations"
	case "goofish":
		entity = "sync"
	case "alipay":
		entity = "bill_import"
	case "settings":
		entity = "settings"
	case "import", "export":
		entity = "dataset"
	}
	return entity, entityID
}

func writeRequestAudit(r *http.Request, status int) {
	if status < 200 || status >= 300 || !isWriteMethod(r.Method) || !shouldAuditRequest(r.URL.Path) {
		return
	}
	audit := auditCaptureFromRequest(r)
	entityType, entityID := inferAuditEntity(r.URL.Path)
	action := summarizeChange(r.Method, r.URL.Path)
	summary := action
	detail := map[string]interface{}{"method": r.Method, "path": r.URL.Path}
	if audit != nil {
		if audit.Action != "" {
			action = audit.Action
		}
		if audit.EntityType != "" {
			entityType = audit.EntityType
		}
		if audit.EntityID > 0 {
			entityID = audit.EntityID
		}
		if audit.Summary != "" {
			summary = audit.Summary
		}
		for k, v := range audit.Detail {
			detail[k] = v
		}
	}
	raw, _ := json.Marshal(detail)
	_, err := db.Exec(`INSERT INTO operation_audit
        (shop_id,actor,client_id,action,entity_type,entity_id,summary,detail,created_at)
        VALUES(?,?,?,?,?,?,?,?,?)`,
		shopID(r), auditActor(r), clientID(r), action, entityType, entityID,
		summary, string(raw), nowStr())
	if err != nil {
		fmt.Printf("[操作审计写入失败] %v\n", err)
	}
}

func shouldAuditRequest(path string) bool {
	if noBroadcastWrites[path] {
		return false
	}
	return !strings.HasPrefix(path, "/api/auth/") &&
		!strings.HasPrefix(path, "/api/update/") &&
		path != "/api/goofish/ping"
}

type operationAuditRow struct {
	ID         int64                  `json:"id"`
	Actor      string                 `json:"actor"`
	Action     string                 `json:"action"`
	EntityType string                 `json:"entity_type"`
	EntityID   int64                  `json:"entity_id"`
	Summary    string                 `json:"summary"`
	Detail     map[string]interface{} `json:"detail"`
	CreatedAt  string                 `json:"created_at"`
}

func handleOperationsAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 300 {
		limit = 100
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	where := "shop_id=?"
	args := []interface{}{shopID(r)}
	if q != "" {
		where += " AND (summary LIKE ? OR actor LIKE ? OR action LIKE ? OR entity_type LIKE ?)"
		like := "%" + q + "%"
		args = append(args, like, like, like, like)
	}
	args = append(args, limit)
	rows, err := db.Query(`SELECT id,actor,action,entity_type,entity_id,summary,detail,created_at
        FROM operation_audit WHERE `+where+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取操作审计失败")
		return
	}
	defer rows.Close()
	items := []operationAuditRow{}
	for rows.Next() {
		var row operationAuditRow
		var raw string
		if rows.Scan(&row.ID, &row.Actor, &row.Action, &row.EntityType, &row.EntityID,
			&row.Summary, &raw, &row.CreatedAt) != nil {
			continue
		}
		row.Detail = map[string]interface{}{}
		_ = json.Unmarshal([]byte(raw), &row.Detail)
		items = append(items, row)
	}
	writeJSON(w, map[string]interface{}{"items": items, "limit": limit})
}

func auditItemChanges(r *http.Request, before, after Item) {
	changes := map[string]interface{}{}
	add := func(name string, oldValue, newValue interface{}) {
		if fmt.Sprint(oldValue) != fmt.Sprint(newValue) {
			changes[name] = map[string]interface{}{"before": oldValue, "after": newValue}
		}
	}
	add("product", before.Product, after.Product)
	add("sell_price", before.SellPrice, after.SellPrice)
	add("sell_fee", before.SellFee, after.SellFee)
	add("sell_wuyou_fee", before.SellWuyouFee, after.SellWuyouFee)
	add("sell_net", before.SellNet, after.SellNet)
	add("sell_shipping", before.SellShipping, after.SellShipping)
	add("unit_cost", before.UnitCost, after.UnitCost)
	add("owner", before.Owner, after.Owner)
	add("source", before.Source, after.Source)
	add("cost_free", before.CostFree, after.CostFree)
	if len(changes) == 0 {
		setAudit(r, "修改记录", "record", after.ID, "保存了记录（字段值未变化）", nil)
		return
	}
	setAudit(r, "修改记录", "record", after.ID, "修改了 "+after.Product, map[string]interface{}{"changes": changes})
}
