package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// exportBundle 是导出的完整数据结构
type exportBundle struct {
	Version    string            `json:"version"`
	ExportedAt string            `json:"exported_at"`
	Items      []Item            `json:"items"`
	Tags       []string          `json:"tags"`
	Settings   map[string]string `json:"settings"`
}

// 这些设置项属于敏感/环境相关，导出时排除
var privateSettings = map[string]bool{
	"auth_hash": true, "auth_salt": true, "auth_secret": true,
	"readonly_password_hash": true, "readonly_salt": true,
	"ocr_api_key": true,
}

// collectExport 导出当前店铺的数据。运费标签等全局配置照常带上，
// 但明细/联单只导出本店——导出文件一旦含别家数据，就等于把隔离边界送出去了。
func collectExport(shopID int64) (exportBundle, error) {
	b := exportBundle{
		Version: "1", ExportedAt: time.Now().Format("2006-01-02 15:04:05"),
		Items: []Item{}, Tags: []string{}, Settings: map[string]string{},
	}
	rows, err := db.Query("SELECT "+itemCols+" FROM items WHERE shop_id=? ORDER BY id", shopID)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return b, err
		}
		b.Items = append(b.Items, it)
	}
	tagRows, err := db.Query("SELECT name FROM ship_tags ORDER BY id")
	if err == nil {
		defer tagRows.Close()
		for tagRows.Next() {
			var n string
			tagRows.Scan(&n)
			b.Tags = append(b.Tags, n)
		}
	}
	setRows, err := db.Query("SELECT key, value FROM settings")
	if err == nil {
		defer setRows.Close()
		for setRows.Next() {
			var k, v string
			setRows.Scan(&k, &v)
			if !privateSettings[k] && v != "" {
				b.Settings[k] = v
			}
		}
	}
	return b, nil
}

func handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "method not allowed")
		return
	}
	b, err := collectExport(shopID(r))
	if err != nil {
		writeError(w, 500, "导出失败: "+err.Error())
		return
	}
	// scope=filtered：导出当前筛选条件下的记录（CSV），JSON 完整包仍是全量备份
	if r.URL.Query().Get("scope") == "filtered" {
		where, args := buildWhere(r) // shop-scope: buildWhere 内部已带 shop_id 条件
		rows, e := db.Query("SELECT "+itemCols+" FROM items WHERE "+where+" ORDER BY "+orderClause(r), args...)
		if e != nil {
			writeError(w, 500, "筛选导出失败: "+e.Error())
			return
		}
		defer rows.Close()
		filtered := []Item{}
		for rows.Next() {
			it, e2 := scanItem(rows)
			if e2 == nil {
				filtered = append(filtered, it)
			}
		}
		b.Items = filtered
	}
	format := r.URL.Query().Get("format")
	if format == "profit" {
		writeProfitCSV(w, b.Items)
		return
	}
	stamp := time.Now().Format("20060102-150405")
	shop := shopNameOf(shopID(r))
	if shop == "" {
		shop = "默认店铺"
	}
	if format == "csv" {
		// columns= 自定义列：只导出白名单内、按请求顺序排列的列（无 columns 时保持原行为）。
		if cp := r.URL.Query().Get("columns"); cp != "" {
			cols := []string{}
			seen := map[string]bool{}
			for _, c := range strings.Split(cp, ",") {
				c = strings.TrimSpace(c)
				if _, ok := exportColumnGetters[c]; ok && !seen[c] {
					seen[c] = true
					cols = append(cols, c)
				}
			}
			if len(cols) > 0 {
				writeColumnsCSV(w, b.Items, cols)
				return
			}
		}
		writeItemsCSV(w, b.Items, shop, stamp)
		return
	}
	// Excel：交给服务端生成，样式（表头配色 / 冻结首行 / 筛选 / 数字格式 /
	// 状态配色 / 合计行）由服务端一并写入，前端不再需要 300KB 的表格库。
	if format == "xlsx" {
		// 两个 sheet 共用同一个样式表，避免样式索引错位
		styles := newXLStyleBook()
		detail := buildItemsSheet(styles, b.Items, "交易明细")
		summary := buildMonthlySummarySheet(styles, shopID(r), b.Items)
		data, err := buildXLSXMulti([]*xlSheet{detail, summary}, styles)
		if err != nil {
			writeError(w, 500, "生成 Excel 失败: "+err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		setDownloadName(w, "xianyu-"+stamp+".xlsx", "闲鱼明细-"+shop+"-"+stamp+".xlsx")
		w.Write(data)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	setDownloadName(w, "xianyu-"+stamp+".json", "闲鱼备份-"+shop+"-"+stamp+".json")
	json.NewEncoder(w).Encode(b)
}

// writeItemsCSV 明细 CSV。列定义与 Excel 共用 itemColumns：
// 两处导出的列序、列名、空值规则由同一份定义保证一致。
// 用 CRLF 换行：Windows 记事本与 Excel 对纯 \n 的兼容性并不好。
func writeItemsCSV(w http.ResponseWriter, items []Item, shop, stamp string) {
	var buf bytes.Buffer
	buf.Write([]byte("\xEF\xBB\xBF")) // Excel 中文 BOM
	cw := csv.NewWriter(&buf)
	cw.UseCRLF = true
	header := make([]string, len(itemColumns))
	for i, c := range itemColumns {
		header[i] = c.header
	}
	writeCSVRow(cw, header)
	for _, it := range items {
		row := make([]string, len(itemColumns))
		for i, c := range itemColumns {
			if c.kind == xlMoney && c.zeroBlank && c.number(it) == 0 {
				row[i] = "" // 未发生的买/卖金额留空，避免满屏 0.00
				continue
			}
			row[i] = c.value(it)
		}
		writeCSVRow(cw, row)
	}
	cw.Flush()
	csvResponse(w, &buf, "xianyu-"+stamp+".csv", "闲鱼明细-"+shop+"-"+stamp+".csv")
}

func f2(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

// handleImport 支持两种模式：merge(追加) / replace(清空后导入)
// 导入前自动生成一份备份；全部在事务内完成，失败可回滚。
func handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	defer refreshPurchaseIndex()
	var in struct {
		Mode string        `json:"mode"` // merge | replace
		Data *exportBundle `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, 400, "文件解析失败：不是有效的导出 JSON")
		return
	}
	if in.Data == nil || in.Data.Items == nil {
		writeError(w, 400, "文件解析失败：缺少 items 数据")
		return
	}
	if in.Mode != "replace" {
		in.Mode = "merge"
	}

	// 导入的 JSON 不经过 ItemInput，先做同样的控制字符清理再校验。
	for i := range in.Data.Items {
		sanitizeItem(&in.Data.Items[i])
	}

	// 写入前校验（金额、时间、订单号重复），不合法直接 400，不生成备份。
	if err := validateImportItems(in.Data.Items); err != nil {
		writeError(w, 400, err.Error())
		return
	}

	if _, err := createBackup("before-import"); err != nil {
		writeError(w, 500, "导入前备份失败，已中止导入："+err.Error())
		return
	}

	// replace 会清空 items / ship_tags，先把四张相关表全表快照存成撤销记录，
	// 供 /api/undo 恢复到导入前状态。必须在任何 DELETE 之前读取；queryMaps 返回时
	// rows 已关闭，不会占着连接影响后面的事务写入。
	undoToken := ""
	if in.Mode == "replace" {
		snap := undoSnapshot{Op: "import_replace"}
		var e error
		if snap.Items, e = queryMaps("SELECT * FROM items WHERE shop_id=?", shopID(r)); e != nil {
			writeError(w, 500, "导入前快照失败: "+e.Error())
			return
		}
		if snap.GroupItems, e = queryMaps(`SELECT gi.* FROM group_items gi
            JOIN link_groups g ON g.id=gi.group_id WHERE g.shop_id=?`, shopID(r)); e != nil {
			writeError(w, 500, "导入前快照失败: "+e.Error())
			return
		}
		if snap.Groups, e = queryMaps("SELECT * FROM link_groups WHERE shop_id=?", shopID(r)); e != nil {
			writeError(w, 500, "导入前快照失败: "+e.Error())
			return
		}
		if snap.ShipTags, e = queryMaps("SELECT * FROM ship_tags"); e != nil {
			writeError(w, 500, "导入前快照失败: "+e.Error())
			return
		}
		if undoToken, e = saveUndo("import_replace", snap, shopID(r)); e != nil {
			writeError(w, 500, "保存撤销记录失败: "+e.Error())
			return
		}
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, 500, "开启事务失败")
		return
	}
	defer tx.Rollback()

	if in.Mode == "replace" {
		// 覆盖导入只清空当前店铺；运费标签是全局共享配置，不随单店导入清空。
		if _, err := tx.Exec("DELETE FROM items WHERE shop_id=?", shopID(r)); err != nil {
			writeError(w, 500, "清空数据失败")
			return
		}
	}

	// 导入的记录一律归入当前店铺：包里即使带了 shop_id 也不采信，
	// 否则一个构造过的导出文件就能往别家店铺写数据。
	insertItem := `INSERT INTO items
        (shop_id, product, product_norm, buy_price,buy_shipping,buy_ship_tag,buy_order_no,buy_time,buy_counterparty,
         sell_price,sell_fee,sell_wuyou_fee,sell_net,sell_shipping,sell_ship_tag,sell_order_no,sell_time,sell_counterparty,
         note, source, created_at, updated_at)
        VALUES (?,?,?,?,?,?,?,?,?, ?,?,?,?,?,?,?,?,?,?, ?,?,?,?)`
	now := nowStr()
	for _, it := range in.Data.Items {
		created := it.CreatedAt
		if created == "" {
			created = now
		}
		updated := it.UpdatedAt
		if updated == "" {
			updated = created
		}
		// 导出包不含图片文件，清空附件名避免指向不存在的文件
		it.Attachment = ""
		if _, err := tx.Exec(insertItem, shopID(r),
			it.Product, normProduct(it.Product), it.BuyPrice, it.BuyShipping, it.BuyShipTag, it.BuyOrderNo, it.BuyTime, it.BuyCounterparty,
			it.SellPrice, it.SellFee, it.SellWuyouFee, it.SellNet, it.SellShipping, it.SellShipTag, it.SellOrderNo, it.SellTime, it.SellCounterparty,
			it.Note, normSource(it.Source), created, updated,
		); err != nil {
			writeError(w, 500, "导入记录失败: "+err.Error())
			return
		}
	}
	for _, t := range in.Data.Tags {
		if strings.TrimSpace(t) != "" {
			tx.Exec("INSERT OR IGNORE INTO ship_tags(name, created_at) VALUES(?,?)", strings.TrimSpace(t), now)
		}
	}
	// 只恢复非敏感设置，且 merge 模式下不覆盖已有值
	for k, v := range in.Data.Settings {
		if privateSettings[k] || k == "" {
			continue
		}
		if in.Mode == "merge" && getSetting(k) != "" {
			continue
		}
		tx.Exec("INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", k, v)
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "提交失败: "+err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{
		"ok": true, "mode": in.Mode, "imported": len(in.Data.Items),
		"message": fmt.Sprintf("已导入 %d 条记录（%s），导入前已自动备份", len(in.Data.Items),
			map[string]string{"merge": "追加", "replace": "覆盖"}[in.Mode]),
		"undo_token": undoToken,
	})
}
