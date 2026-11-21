package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newExtrasEnv 用临时 DB 起一套完整路由；每个测试独立，互不污染。
func newExtrasEnv(t *testing.T) http.Handler {
	t.Helper()
	dir := t.TempDir()
	os.Setenv("DB_PATH", dir+"/test.db")
	if err := initDB(); err != nil {
		t.Fatalf("initDB: %v", err)
	}
	if err := initUndoTable(); err != nil {
		t.Fatalf("initUndoTable: %v", err)
	}
	if err := initExtrasSchema(); err != nil {
		t.Fatalf("initExtrasSchema: %v", err)
	}
	resetLoginLimiter()
	t.Cleanup(func() {
		db.Close()
		os.Unsetenv("DB_PATH")
	})
	mux := http.NewServeMux()
	registerAPI(mux)
	return mux
}

func doExtrasReq(h http.Handler, method, path string, body interface{}, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	var req *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		req = httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func insertExtrasItem(t *testing.T, it *Item) int64 {
	t.Helper()
	id, err := insertItem(it, defaultShopID, nowStr())
	if err != nil {
		t.Fatalf("insertItem: %v", err)
	}
	return id
}

func decodeMap(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal %q: %v", w.Body.String(), err)
	}
	return m
}

// ---------- 契约 1：回收站列表 ----------

func TestUndoOpsList(t *testing.T) {
	h := newExtrasEnv(t)
	tok1, err := saveUndo("delete_one", undoSnapshot{Op: "delete_one", Items: []map[string]any{{"id": 1}}}, defaultShopID)
	if err != nil {
		t.Fatalf("saveUndo: %v", err)
	}
	tok2, _ := saveUndo("import_replace", undoSnapshot{Op: "import_replace"}, defaultShopID)
	db.Exec("UPDATE undo_ops SET consumed=1 WHERE token=?", tok2)
	// 10 天前的记录不应出现在近 7 天列表里
	db.Exec("INSERT INTO undo_ops(token,op,snapshot,created_at,consumed) VALUES(?,?,?,?,?)",
		"old-token", "delete_batch", "{}", time.Now().AddDate(0, 0, -10).Format("2006-01-02 15:04:05"), 0)

	w := doExtrasReq(h, "GET", "/api/undo/ops", nil)
	if w.Code != 200 {
		t.Fatalf("GET /api/undo/ops = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Items []undoOpItem `json:"items"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Items) != 2 {
		t.Fatalf("items=%d want 2 (近7天、排除过期): %+v", len(out.Items), out.Items)
	}
	byToken := map[string]undoOpItem{}
	for _, it := range out.Items {
		byToken[it.Token] = it
	}
	if _, ok := byToken["old-token"]; ok {
		t.Error("7 天前的记录不应出现在回收站")
	}
	if it, ok := byToken[tok1]; !ok || it.Label != "删除 1 条记录" || it.Size <= 0 {
		t.Errorf("delete_one label/size 不正确: %+v", it)
	}
	if it, ok := byToken[tok2]; !ok || it.Label != "覆盖导入" || !it.Consumed {
		t.Errorf("import_replace label/consumed 不正确: %+v", it)
	}
}

func TestUndoOpsDeleteRemovesRow(t *testing.T) {
	h := newExtrasEnv(t)
	tok, _ := saveUndo("delete_one", undoSnapshot{Op: "delete_one"}, defaultShopID)
	w := doExtrasReq(h, "DELETE", "/api/undo/ops?token="+tok, nil)
	if w.Code != 200 || !decodeMap(t, w)["ok"].(bool) {
		t.Fatalf("DELETE /api/undo/ops = %d %s", w.Code, w.Body.String())
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM undo_ops WHERE token=?", tok).Scan(&n)
	if n != 0 {
		t.Errorf("DELETE 未真的删行，剩余 %d 行", n)
	}
}

// ---------- 契约 3：批量编辑 ----------

func TestBatchUpdate(t *testing.T) {
	h := newExtrasEnv(t)
	id1 := insertExtrasItem(t, &Item{Product: "商品A", Quantity: 1})
	id2 := insertExtrasItem(t, &Item{Product: "商品B", Quantity: 1})
	db.Exec("UPDATE items SET sell_ship_tag='旧标签' WHERE id=?", id1)

	body := map[string]interface{}{
		"ids": []int64{id1, id2},
		"fields": map[string]string{
			"note": "备注X", "buy_ship_tag": "包邮", "sell_ship_tag": "",
			"buy_time": "", "sell_time": "",
		},
	}
	w := doExtrasReq(h, "POST", "/api/records/batch-update", body)
	if w.Code != 200 {
		t.Fatalf("POST batch-update = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Updated int `json:"updated"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Updated != 2 {
		t.Errorf("updated=%d want 2", out.Updated)
	}
	var note, bTag, sTag string
	db.QueryRow("SELECT note, buy_ship_tag, sell_ship_tag FROM items WHERE id=?", id1).Scan(&note, &bTag, &sTag)
	if note != "备注X" || bTag != "包邮" || sTag != "" {
		t.Errorf("字段未正确写入/清空: note=%q bTag=%q sTag=%q", note, bTag, sTag)
	}
	// 白名单外字段应被忽略（不报错）
	w2 := doExtrasReq(h, "POST", "/api/records/batch-update", map[string]interface{}{
		"ids": []int64{id1}, "fields": map[string]string{"unknown_col": "x", "id": "999"},
	})
	if w2.Code != 200 {
		t.Errorf("白名单外字段应被忽略而非报错: %d %s", w2.Code, w2.Body.String())
	}
}

// ---------- 契约 6：搜索多关键词 AND + 金额区间 ----------

func TestSearchMultiKeywordAndAmount(t *testing.T) {
	h := newExtrasEnv(t)
	idHit1 := insertExtrasItem(t, &Item{Product: "苹果手机壳", Note: "蓝色", Quantity: 1})
	insertExtrasItem(t, &Item{Product: "苹果", Note: "充电", Quantity: 1})
	idHit2 := insertExtrasItem(t, &Item{Product: "手机", Note: "苹果", Quantity: 1})

	q := url.QueryEscape("苹果 手机")
	w := doExtrasReq(h, "GET", "/api/records?q="+q, nil)
	if w.Code != 200 {
		t.Fatalf("GET /api/records = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Total int    `json:"total"`
		Items []Item `json:"items"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Total != 2 {
		t.Errorf("多关键词 AND total=%d want 2: %+v", out.Total, out.Items)
	}
	got := map[int64]bool{}
	for _, it := range out.Items {
		got[it.ID] = true
	}
	if !got[idHit1] || !got[idHit2] {
		t.Errorf("命中集合不符: got=%v want {%d,%d}", got, idHit1, idHit2)
	}

	// 金额区间：MAX(buy_price,sell_price,sell_net) 过滤
	insertExtrasItem(t, &Item{Product: "高价货", BuyPrice: 5000, Quantity: 1})
	insertExtrasItem(t, &Item{Product: "低价货", BuyPrice: 10, Quantity: 1})
	w2 := doExtrasReq(h, "GET", "/api/records?min_amount=1000", nil)
	var out2 struct {
		Total int `json:"total"`
	}
	json.Unmarshal(w2.Body.Bytes(), &out2)
	if out2.Total != 1 {
		t.Errorf("金额区间 total=%d want 1", out2.Total)
	}
}

// ---------- 契约 5：导出自定义列 / 利润表 ----------

func TestExportColumnsCSV(t *testing.T) {
	h := newExtrasEnv(t)
	insertExtrasItem(t, &Item{
		Product: "导出测试商品", BuyPrice: 12.5, SellNet: 99.9,
		Note: "备注NNN", SellOrderNo: "UNIQUE-ORDER-777", Quantity: 1,
	})
	w := doExtrasReq(h, "GET", "/api/export?format=csv&columns=product,buy_price,sell_net,note", nil)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("export csv = %d ct=%q", w.Code, w.Header().Get("Content-Type"))
	}
	body := strings.TrimPrefix(w.Body.String(), "\xEF\xBB\xBF")
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("解析 CSV: %v", err)
	}
	if len(rows) != 2 || len(rows[0]) != 4 {
		t.Fatalf("CSV 行数/列数不符: rows=%d cols=%d %v", len(rows), len(rows[0]), rows)
	}
	if rows[0][0] != "product" || rows[0][1] != "buy_price" || rows[0][2] != "sell_net" || rows[0][3] != "note" {
		t.Errorf("自定义列表头不符: %v", rows[0])
	}
	if strings.Contains(body, "UNIQUE-ORDER-777") {
		t.Error("未包含在 columns 的列不应被导出")
	}
	// 无 columns 时保持原行为（完整列）
	w2 := doExtrasReq(h, "GET", "/api/export?format=csv", nil)
	if !strings.Contains(w2.Body.String(), "UNIQUE-ORDER-777") {
		t.Error("无 columns 时应保持原完整 CSV 行为")
	}
}

func TestExportProfitCSV(t *testing.T) {
	h := newExtrasEnv(t)
	insertExtrasItem(t, &Item{Product: "利润商品", BuyPrice: 30, SellPrice: 100, SellNet: 95, Quantity: 1})
	w := doExtrasReq(h, "GET", "/api/export?format=profit", nil)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "商品,买进价,运费,服务费,到手价,利润") {
		t.Fatalf("profit csv = %d %s", w.Code, body)
	}
	if !strings.Contains(body, "利润商品") {
		t.Error("利润表应包含记录行")
	}
}

// ---------- 契约 4：健康检查 ----------

func TestHealth(t *testing.T) {
	h := newExtrasEnv(t)
	w := doExtrasReq(h, "GET", "/api/health", nil)
	if w.Code != 200 {
		t.Fatalf("GET /api/health = %d %s", w.Code, w.Body.String())
	}
	m := decodeMap(t, w)
	if m["ok"] != true || m["db"] != true {
		t.Errorf("health ok/db 应为 true: %v", m)
	}
	if s, _ := m["build"].(string); s == "" {
		t.Error("build 字段缺失")
	}
	if _, ok := m["backup_last"]; !ok {
		t.Error("backup_last 字段缺失")
	}
}

// ---------- 契约 15：重复撤销被拒 ----------

func TestUndoRejectReplay(t *testing.T) {
	h := newExtrasEnv(t)
	tok, _ := saveUndo("delete_one", undoSnapshot{Op: "delete_one"}, defaultShopID)
	w1 := doExtrasReq(h, "POST", "/api/undo", map[string]string{"token": tok})
	if w1.Code != 200 {
		t.Fatalf("第一次撤销应成功: %d %s", w1.Code, w1.Body.String())
	}
	w2 := doExtrasReq(h, "POST", "/api/undo", map[string]string{"token": tok})
	if w2.Code != 400 {
		t.Errorf("重复撤销应被拒(400)，实际 %d %s", w2.Code, w2.Body.String())
	}
}

// ---------- 契约 7：统计增强 ----------

func TestStatsExtras(t *testing.T) {
	h := newExtrasEnv(t)
	insertExtrasItem(t, &Item{Product: "货甲", BuyPrice: 100, SellPrice: 200, SellNet: 195, SellTime: "2024-01-10", Quantity: 1})
	insertExtrasItem(t, &Item{Product: "货乙", BuyPrice: 50, Quantity: 1})
	w := doExtrasReq(h, "GET", "/api/stats/extras", nil)
	if w.Code != 200 {
		t.Fatalf("GET /api/stats/extras = %d %s", w.Code, w.Body.String())
	}
	m := decodeMap(t, w)
	if _, ok := m["top_products"]; !ok {
		t.Error("缺少 top_products")
	}
	if _, ok := m["turnover_days"]; !ok {
		t.Error("缺少 turnover_days")
	}
	if _, ok := m["unsold_count"]; !ok {
		t.Error("缺少 unsold_count")
	}
}

// 经营洞察也必须按卖单实际带走的成本冲减联单采购。
// 不能因为记录进了联单就把整批采购从在库中漏掉。
func TestStatsExtrasUsesRemainingLinkedHolding(t *testing.T) {
	h := newExtrasEnv(t)
	if err := createGroupTables(); err != nil {
		t.Fatalf("createGroupTables: %v", err)
	}

	buyTime := time.Now().AddDate(0, 0, -61).Format("2006-01-02 15:04:05")
	buyID := insertExtrasItem(t, &Item{
		Product: "联单周转货", BuyPrice: 900, Quantity: 2, BuyTime: buyTime,
	})
	sellID := insertExtrasItem(t, &Item{
		Product: "联单周转货", ParentID: buyID, Quantity: 1,
		SellPrice: 300, SellNet: 300, SellTime: nowStr(),
	})
	res, err := db.Exec("INSERT INTO link_groups(title,created_at,updated_at) VALUES(?,?,?)",
		"周转测试联单", nowStr(), nowStr())
	if err != nil {
		t.Fatalf("insert group: %v", err)
	}
	gid, _ := res.LastInsertId()
	if err := setGroupItems(gid, []int64{buyID, sellID}, defaultShopID); err != nil {
		t.Fatalf("setGroupItems: %v", err)
	}

	w := doExtrasReq(h, "GET", "/api/stats/extras", nil)
	if w.Code != 200 {
		t.Fatalf("GET /api/stats/extras = %d %s", w.Code, w.Body.String())
	}
	m := decodeMap(t, w)
	// 采购 900 两件，卖出 1 件分摊 450；剩余 450 仍应占用库存。
	// 近 90 天销售额 300，日均 300/90，周转 = 450 / (300/90) = 135 天。
	if got := m["turnover_days"].(float64); got != 135 {
		t.Errorf("turnover_days=%v want 135", got)
	}
	if got := m["stale_count"].(float64); got != 1 {
		t.Errorf("stale_count=%v want 1", got)
	}
	if got := m["unsold_count"].(float64); got != 1 {
		t.Errorf("unsold_count=%v want 1（联单未售部分仍应计入待售）", got)
	}

	ws := doExtrasReq(h, "GET", "/api/stats", nil)
	if ws.Code != 200 {
		t.Fatalf("GET /api/stats = %d %s", ws.Code, ws.Body.String())
	}
	if got := decodeMap(t, ws)["pending_count"].(float64); got != 1 {
		t.Errorf("pending_count=%v want 1（首页待售批次应与在库成本一致）", got)
	}
}

// 寄卖 / 代采不是自己的经营成本，也不能出现在首页商品净利润排行。
func TestProductProfitExcludesExternalGoods(t *testing.T) {
	h := newExtrasEnv(t)
	insertExtrasItem(t, &Item{
		Product: "自营商品", BuyPrice: 100, SellPrice: 200, SellNet: 195, Quantity: 1,
	})
	insertExtrasItem(t, &Item{
		Product: "寄卖商品", Source: "寄卖", CostFree: true,
		SellPrice: 3000, SellNet: 2950, Quantity: 1,
	})
	insertExtrasItem(t, &Item{
		Product: "代采商品", Source: "代采", BuyPrice: 500, Quantity: 1,
	})

	w := doExtrasReq(h, "GET", "/api/product-profit", nil)
	if w.Code != 200 {
		t.Fatalf("GET /api/product-profit = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Items []productProfitRow `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := map[string]bool{}
	for _, row := range out.Items {
		got[row.Product] = true
	}
	if !got["自营商品"] {
		t.Error("自营商品应保留在商品利润中")
	}
	if got["寄卖商品"] {
		t.Error("寄卖商品不应进入自己的商品利润排行")
	}
	if got["代采商品"] {
		t.Error("代采商品不应进入自己的商品利润排行")
	}
}

// 利润榜口径：未完结联单里的商品不进榜，只展示已闭环且盈利（>0）的商品。
func TestStatsExtrasTopProductsFilter(t *testing.T) {
	h := newExtrasEnv(t)
	if err := createGroupTables(); err != nil {
		t.Fatalf("createGroupTables: %v", err)
	}

	// A：盈利，但挂在「未完结」联单里 —— 应排除
	aID := insertExtrasItem(t, &Item{Product: "未完结联单货", BuyPrice: 100, SellPrice: 300, SellNet: 295, Quantity: 1})
	// B：独立成交且盈利 —— 应保留
	insertExtrasItem(t, &Item{Product: "独立盈利货", BuyPrice: 100, SellPrice: 200, SellNet: 195, Quantity: 1})
	// C：成交但利润为 0 —— 应排除
	insertExtrasItem(t, &Item{Product: "零利润货", BuyPrice: 100, SellPrice: 100, SellNet: 100, Quantity: 1})
	// D：挂在「已完结」联单里且盈利 —— 应保留
	dID := insertExtrasItem(t, &Item{Product: "已完结联单货", BuyPrice: 100, SellPrice: 400, SellNet: 395, Quantity: 1})

	newGroup := func(title string, settled bool) int64 {
		res, err := db.Exec("INSERT INTO link_groups(title,created_at,updated_at) VALUES(?,?,?)", title, nowStr(), nowStr())
		if err != nil {
			t.Fatalf("insert group: %v", err)
		}
		gid, _ := res.LastInsertId()
		if settled {
			if _, err := db.Exec("UPDATE link_groups SET settled_at=? WHERE id=?", nowStr(), gid); err != nil {
				t.Fatalf("settle: %v", err)
			}
		}
		return gid
	}
	gOpen := newGroup("未完结联单", false)
	if err := setGroupItems(gOpen, []int64{aID}, defaultShopID); err != nil {
		t.Fatalf("setGroupItems: %v", err)
	}
	gDone := newGroup("已完结联单", true)
	if err := setGroupItems(gDone, []int64{dID}, defaultShopID); err != nil {
		t.Fatalf("setGroupItems: %v", err)
	}

	w := doExtrasReq(h, "GET", "/api/stats/extras", nil)
	if w.Code != 200 {
		t.Fatalf("GET /api/stats/extras = %d %s", w.Code, w.Body.String())
	}
	m := decodeMap(t, w)
	arr, _ := m["top_products"].([]interface{})
	names := map[string]bool{}
	for _, x := range arr {
		mm, ok := x.(map[string]interface{})
		if !ok {
			continue
		}
		if p, ok := mm["product"].(string); ok {
			names[p] = true
		}
		if v, ok := mm["profit"].(float64); ok && v <= 0 {
			t.Errorf("榜单出现非正利润条目: %+v", mm)
		}
	}
	if names["未完结联单货"] {
		t.Error("未完结联单里的商品不应出现在利润榜")
	}
	if names["零利润货"] {
		t.Error("零利润商品不应出现在利润榜")
	}
	if !names["独立盈利货"] {
		t.Error("独立盈利商品应出现在利润榜")
	}
	if !names["已完结联单货"] {
		t.Error("已完结联单里的盈利商品应出现在利润榜")
	}
}

// ---------- 契约 8：趋势周期 ----------

func TestTrendPeriod(t *testing.T) {
	h := newExtrasEnv(t)
	insertExtrasItem(t, &Item{Product: "A", SellPrice: 100, SellTime: "2024-01-10", Quantity: 1})
	insertExtrasItem(t, &Item{Product: "B", SellPrice: 200, SellTime: "2024-04-10", Quantity: 1})
	for _, p := range []string{"month", "quarter", "week"} {
		w := doExtrasReq(h, "GET", "/api/trend?period="+p, nil)
		if w.Code != 200 {
			t.Errorf("period=%s 应返回 200，实际 %d %s", p, w.Code, w.Body.String())
			continue
		}
		if _, ok := decodeMap(t, w)["months"]; !ok {
			t.Errorf("period=%s 缺少 months", p)
		}
	}
}

// ---------- 契约 9：OCR 反馈 ----------

func TestOCRFeedback(t *testing.T) {
	h := newExtrasEnv(t)
	w := doExtrasReq(h, "POST", "/api/ocr-feedback", map[string]interface{}{
		"record_id": 1, "correct": true, "note": "识别对了",
	})
	if w.Code != 200 || decodeMap(t, w)["ok"] != true {
		t.Fatalf("POST ocr-feedback = %d %s", w.Code, w.Body.String())
	}
	w2 := doExtrasReq(h, "GET", "/api/ocr-feedback", nil)
	var out struct {
		Items []map[string]interface{} `json:"items"`
	}
	json.Unmarshal(w2.Body.Bytes(), &out)
	if len(out.Items) != 1 || out.Items[0]["correct"] != true {
		t.Errorf("GET ocr-feedback items 不符: %+v", out.Items)
	}
}

// ---------- 契约 10：记录附件 ----------

func TestRecordAttachment(t *testing.T) {
	h := newExtrasEnv(t)
	id := insertExtrasItem(t, &Item{Product: "带图商品", Quantity: 1})

	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	hdr := make(textproto.MIMEHeader)
	hdr.Set("Content-Disposition", `form-data; name="image"; filename="pic.png"`)
	hdr.Set("Content-Type", "image/png")
	part, _ := mw.CreatePart(hdr)
	part.Write(png)
	mw.Close()
	req := httptest.NewRequest("POST", "/api/records/"+itoa(id)+"/attachment", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("POST attachment = %d %s", w.Code, w.Body.String())
	}

	// GET 应返回同一文件
	w2 := doExtrasReq(h, "GET", "/api/records/"+itoa(id)+"/attachment", nil)
	if w2.Code != 200 || !bytes.Equal(w2.Body.Bytes(), png) {
		t.Errorf("GET attachment = %d body=%v", w2.Code, w2.Body.Bytes())
	}
	// 无附件的记录 -> 404
	id2 := insertExtrasItem(t, &Item{Product: "无图商品", Quantity: 1})
	w3 := doExtrasReq(h, "GET", "/api/records/"+itoa(id2)+"/attachment", nil)
	if w3.Code != 404 {
		t.Errorf("无附件应 404，实际 %d", w3.Code)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// ---------- 契约 11：导入校验 ----------

func TestValidateImportItems(t *testing.T) {
	cases := []struct {
		name string
		item Item
		want string
	}{
		{"负金额", Item{BuyPrice: -1}, "负数"},
		{"非法时间", Item{BuyTime: "2024/01/01"}, "格式不合法"},
	}
	for _, c := range cases {
		if err := validateImportItems([]Item{c.item}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err=%v want 含 %q", c.name, err, c.want)
		}
	}
	if err := validateImportItems([]Item{{BuyOrderNo: "DUP"}, {SellOrderNo: "DUP"}}); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Errorf("重复订单号未报错: %v", err)
	}
	// 合法数据应通过
	if err := validateImportItems([]Item{{Product: "OK", BuyPrice: 10, BuyTime: "2024-01-02"}}); err != nil {
		t.Errorf("合法数据被误拒: %v", err)
	}
}

func TestImportRejectsInvalidViaAPI(t *testing.T) {
	h := newExtrasEnv(t)
	body := map[string]interface{}{
		"mode": "merge",
		"data": map[string]interface{}{
			"items": []map[string]interface{}{{"product": "X", "buy_price": -1}},
		},
	}
	w := doExtrasReq(h, "POST", "/api/import", body)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "负数") {
		t.Errorf("负金额导入应 400 中文错误，实际 %d %s", w.Code, w.Body.String())
	}
}

// ---------- 契约 13：备份对账 ----------

func TestBackupVerify(t *testing.T) {
	h := newExtrasEnv(t)
	insertExtrasItem(t, &Item{Product: "A", Quantity: 1})
	insertExtrasItem(t, &Item{Product: "B", Quantity: 1})
	backupOK := false
	if _, err := createBackup("test"); err == nil {
		backupOK = true
	} else {
		t.Logf("createBackup 跳过: %v", err)
	}
	w := doExtrasReq(h, "GET", "/api/backup/verify", nil)
	m := decodeMap(t, w)
	if m["ok"] != true || int(m["items_in_db"].(float64)) != 2 {
		t.Fatalf("verify 基础字段不符: %v", m)
	}
	if backupOK {
		if m["items_in_backup"] == nil {
			t.Error("备份存在但 items_in_backup 为 null（只读打开可能失败）")
		} else if m["consistent"] != true {
			t.Errorf("备份应一致: %v", m)
		}
	}
}

// ---------- 契约 12：安全响应头 ----------

func TestSecurityHeaders(t *testing.T) {
	mux := newExtrasEnv(t)
	h := securityHeaders(mux)
	w := doExtrasReq(h, "GET", "/api/health", nil)
	if w.Header().Get("X-Content-Type-Options") != "nosniff" ||
		w.Header().Get("X-Frame-Options") != "DENY" ||
		w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("安全响应头缺失: %v", w.Header())
	}
}

// ---------- 契约 14：只读账号 ----------

func TestReadonlyBlocksWrites(t *testing.T) {
	h := newExtrasEnv(t)
	setSetting("auth_salt", "s1")
	if ah, err := hashPassword("adminpw", "s1"); err == nil {
		setSetting("auth_hash", ah)
	} else {
		t.Fatalf("hashPassword: %v", err)
	}
	if err := setReadonlyPassword("readonlypw"); err != nil {
		t.Fatalf("setReadonlyPassword: %v", err)
	}
	resetLoginLimiter()

	w := doExtrasReq(h, "POST", "/api/auth/login", map[string]string{"password": "readonlypw"})
	if w.Code != 200 {
		t.Fatalf("只读登录失败: %d %s", w.Code, w.Body.String())
	}
	var session *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == authCookie {
			session = c
		}
	}
	if session == nil {
		t.Fatal("未签发会话 cookie")
	}
	// 写操作 -> 403
	w2 := doExtrasReq(h, "POST", "/api/records/batch-update",
		map[string]interface{}{"ids": []int64{1}, "fields": map[string]string{"note": "x"}}, session)
	if w2.Code != 403 {
		t.Errorf("只读账号写操作应 403，实际 %d %s", w2.Code, w2.Body.String())
	}
	// 读操作 -> 200
	w3 := doExtrasReq(h, "GET", "/api/records", nil, session)
	if w3.Code != 200 {
		t.Errorf("只读账号读操作应 200，实际 %d %s", w3.Code, w3.Body.String())
	}
	// 管理员登录 -> 写操作放行
	w4 := doExtrasReq(h, "POST", "/api/auth/login", map[string]string{"password": "adminpw"})
	var admin *http.Cookie
	for _, c := range w4.Result().Cookies() {
		if c.Name == authCookie {
			admin = c
		}
	}
	w5 := doExtrasReq(h, "POST", "/api/records/batch-update",
		map[string]interface{}{"ids": []int64{1}, "fields": map[string]string{"note": "x"}}, admin)
	if w5.Code != 200 {
		t.Errorf("管理员写操作应 200，实际 %d %s", w5.Code, w5.Body.String())
	}
}

// ---------- 契约：版本号接口 ----------

func TestVersionAPI(t *testing.T) {
	h := newExtrasEnv(t)
	w := doExtrasReq(h, "GET", "/api/version", nil)
	if w.Code != 200 {
		t.Fatalf("GET /api/version = %d %s", w.Code, w.Body.String())
	}
	m := decodeMap(t, w)
	for _, k := range []string{"version", "count", "time", "note", "build", "history"} {
		if _, ok := m[k]; !ok {
			t.Errorf("/api/version 缺少字段 %q", k)
		}
	}
	ver, _ := m["version"].(string)
	if !regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+$`).MatchString(ver) {
		t.Errorf("版本号格式非法: %q", ver)
	}
	if _, ok := m["history"].([]interface{}); !ok {
		t.Error("history 应为数组")
	}

	// 健康检查同时暴露版本号，便于运维脚本无需鉴权读取
	hw := doExtrasReq(h, "GET", "/api/health", nil)
	if hw.Code != 200 {
		t.Fatalf("GET /api/health = %d", hw.Code)
	}
	hm := decodeMap(t, hw)
	if hm["version"] != ver {
		t.Errorf("health.version=%v 与 /api/version=%q 不一致", hm["version"], ver)
	}
	if hm["build"] != ver {
		t.Errorf("health.build=%v 应为版本号 %q", hm["build"], ver)
	}

	// 非 GET 应 405
	if pw := doExtrasReq(h, "POST", "/api/version", map[string]interface{}{}); pw.Code != 405 {
		t.Errorf("POST /api/version = %d, want 405", pw.Code)
	}
}
