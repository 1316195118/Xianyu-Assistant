package main

// 后端增强：新增 handler 尽量集中在本文件，对现有文件只做必要的局部修改。
// 覆盖：回收站、批量编辑、健康检查、统计增强、导出增强辅助、导入校验、
// OCR 反馈、记录附件、备份对账、趋势周期聚合、只读密码辅助、登录限流辅助。

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 构建标识由 tools/version.py 生成的 version_gen.go 提供（var 才能被
// -ldflags "-X main.buildVersion=..." 覆盖，原来的 const 其实覆盖不了）。

// ============================================================
// 1. 回收站：GET /api/undo/ops 列表 + DELETE /api/undo/ops 彻底删除
// ============================================================

var undoOpLabels = map[string]string{
	"delete_one":     "删除 1 条记录",
	"delete_batch":   "批量删除记录",
	"delete_group":   "删除联单",
	"import_replace": "覆盖导入",
	"delete_backup":  "删除备份",
}

func undoOpLabel(op string) string {
	if l, ok := undoOpLabels[op]; ok {
		return l
	}
	return "撤销操作（" + op + "）"
}

type undoOpItem struct {
	Token     string `json:"token"`
	Op        string `json:"op"`
	Label     string `json:"label"`
	CreatedAt string `json:"created_at"`
	Consumed  bool   `json:"consumed"`
	Size      int64  `json:"size"`
}

func handleUndoOps(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		cutoff := time.Now().AddDate(0, 0, -7).Format("2006-01-02 15:04:05")
		rows, err := db.Query(`SELECT token, op, LENGTH(snapshot), created_at, consumed
            FROM undo_ops WHERE shop_id = ? AND created_at >= ? ORDER BY created_at DESC LIMIT 50`, shopID(r), cutoff)
		if err != nil {
			writeError(w, 500, "查询撤销记录失败")
			return
		}
		defer rows.Close()
		items := []undoOpItem{}
		for rows.Next() {
			var token, op, created string
			var size, consumed int64
			if rows.Scan(&token, &op, &size, &created, &consumed) != nil {
				continue
			}
			items = append(items, undoOpItem{
				Token: token, Op: op, Label: undoOpLabel(op), CreatedAt: created,
				Consumed: consumed != 0, Size: size,
			})
		}
		writeJSON(w, map[string]interface{}{"items": items})
	case "DELETE":
		token := strings.TrimSpace(r.URL.Query().Get("token"))
		if token == "" {
			writeError(w, 400, "缺少 token")
			return
		}
		// 真的删行：未消费也允许删除（契约允许）。
		if _, err := db.Exec("DELETE FROM undo_ops WHERE token=?", token); err != nil {
			writeError(w, 500, "删除撤销记录失败")
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		writeError(w, 405, "method not allowed")
	}
}

// ============================================================
// 3. 批量编辑：POST /api/records/batch-update
// ============================================================

// 只允许白名单字段；product 不纳入，避免与 product_norm 索引不一致。
var batchUpdateWhitelist = map[string]bool{
	"note": true, "buy_ship_tag": true, "sell_ship_tag": true,
	"buy_time": true, "sell_time": true,
	"buy_order_no": true, "sell_order_no": true,
	"buy_counterparty": true, "sell_counterparty": true,
	// 运费金额也允许批量改：几十单散着几块钱运费，手工点太慢
	"buy_shipping": true, "sell_shipping": true,
}

func handleBatchUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	defer refreshPurchaseIndex()
	var in struct {
		IDs    []int64           `json:"ids"`
		Fields map[string]string `json:"fields"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, 400, "请求体解析失败")
		return
	}
	// 白名单过滤 + 排序保证生成的 SQL 稳定
	cols := []string{}
	for k := range in.Fields {
		if batchUpdateWhitelist[k] {
			cols = append(cols, k)
		}
	}
	sort.Strings(cols)
	// ids 去重、过滤非法值、限量
	clean := []int64{}
	seen := map[int64]bool{}
	for _, id := range in.IDs {
		if id > 0 && !seen[id] {
			seen[id] = true
			clean = append(clean, id)
		}
	}
	// 已完结联单里的记录不能批量改
	for _, id := range clean {
		if gid, gtitle := settledGroupOf(id, shopID(r)); gid > 0 {
			writeSettledBlock(w, gtitle, "修改")
			return
		}
	}
	if len(clean) > 200 {
		clean = clean[:200]
	}
	if len(cols) == 0 || len(clean) == 0 {
		writeJSON(w, map[string]int{"updated": 0})
		return
	}

	setParts := make([]string, 0, len(cols)+1)
	for _, c := range cols {
		setParts = append(setParts, c+"=?")
	}
	setParts = append(setParts, "updated_at=?")
	// 列名全部来自白名单，值走占位符，杜绝 SQL 注入
	q := "UPDATE items SET " + strings.Join(setParts, ",") + " WHERE id=?"

	tx, err := db.Begin()
	if err != nil {
		writeError(w, 500, "开启事务失败")
		return
	}
	stmt, err := tx.Prepare(q)
	if err != nil {
		tx.Rollback()
		writeError(w, 500, "准备更新失败")
		return
	}
	ts := nowStr()
	updated := 0
	for _, id := range clean {
		args := make([]interface{}, 0, len(cols)+2)
		for _, c := range cols {
			args = append(args, in.Fields[c]) // 空字符串即清空该字段
		}
		args = append(args, ts, id)
		res, e := stmt.Exec(args...)
		if e != nil {
			stmt.Close()
			tx.Rollback()
			writeError(w, 500, "更新失败: "+e.Error())
			return
		}
		if n, _ := res.RowsAffected(); n > 0 {
			updated += int(n)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "提交失败")
		return
	}
	writeJSON(w, map[string]int{"updated": updated})
}

// ============================================================
// 4. 健康检查：GET /api/health（可不包 authWrap）
// ============================================================

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "method not allowed")
		return
	}
	dbOK := false
	if db != nil {
		dbOK = db.Ping() == nil
	}
	writeJSON(w, map[string]interface{}{
		"ok":          true,
		"db":          dbOK,
		"build":       buildVersion,
		"version":     buildVersion,
		"backup_last": getSetting("backup_last"),
	})
}

// ============================================================
// 4b. 版本信息：GET /api/version
//     版本/说明/历史由 tools/version.py 在每次编译时写入 version_gen.go。
// ============================================================

func handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "method not allowed")
		return
	}
	history := []map[string]interface{}{}
	if err := json.Unmarshal([]byte(appVersionHistoryJSON), &history); err != nil {
		history = []map[string]interface{}{}
	}
	// 生成文件按时间正序存，接口给前端倒序（最新在最上）
	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}
	writeJSON(w, map[string]interface{}{
		"version": buildVersion,
		"count":   appVersionCount,
		"time":    appVersionTime,
		"note":    appVersionNote,
		"build":   appVersionBuild,
		"history": history,
	})
}

// ============================================================
// 7. 统计增强：GET /api/stats/extras
// ============================================================

type topProduct struct {
	Product string  `json:"product"`
	Profit  float64 `json:"profit"`
	Sales   float64 `json:"sales"`
	Settled bool    `json:"settled"` // 该商品有完结记录，界面上标「已完结」
}

// ---------- 待办角标：未挂成本卖单数 ----------

// handleTodoCount GET /api/records/todo-count
// 返回当前店铺「未挂成本」的卖单数，给底部导航「明细」的角标用。
// 直接复用 computeProductProfit 的 unlinked 口径，保证与明细筛选、经营建议、商品排行完全一致。
func handleTodoCount(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "方法不允许")
		return
	}
	rows := computeProductProfit(shopID(r))
	count := 0
	for _, row := range rows {
		count += row.Unlinked
	}
	writeJSON(w, map[string]interface{}{"unlinked": count})
}

// ---------- 月度利润目标（按店铺存） ----------

// handleMonthlyTarget GET/POST /api/monthly-target
// GET  ?month=YYYY-MM 返回该月目标；POST {month:"YYYY-MM", target:5000} 保存。
// target<=0 视为清除目标。存在 settings 表里，key 带店铺后缀做隔离。
func handleMonthlyTarget(w http.ResponseWriter, r *http.Request) {
	sid := shopID(r)
	month := strings.TrimSpace(r.URL.Query().Get("month"))
	if r.Method == "POST" {
		var in struct {
			Month  string  `json:"month"`
			Target float64 `json:"target"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
			writeError(w, 400, "请求体解析失败")
			return
		}
		month = strings.TrimSpace(in.Month)
		if month == "" || len(month) != 7 {
			writeError(w, 400, "月份格式应为 YYYY-MM")
			return
		}
		key := gfKey("monthly_target@"+month, sid)
		if in.Target <= 0 {
			db.Exec("DELETE FROM settings WHERE key=?", key)
		} else if err := setSetting(key, strconv.FormatFloat(round2(in.Target), 'f', 2, 64)); err != nil {
			writeError(w, 500, "保存失败")
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "target": round2(in.Target)})
		return
	}
	if r.Method != "GET" {
		writeError(w, 405, "方法不允许")
		return
	}
	if month == "" {
		month = time.Now().Format("2006-01")
	}
	v := getSetting(gfKey("monthly_target@"+month, sid))
	target, _ := strconv.ParseFloat(v, 64)
	writeJSON(w, map[string]interface{}{"month": month, "target": round2(target)})
}

// ---------- 同商品历史成交价：录入/编辑时给定价参考 ----------

// handlePriceHint GET /api/records/price-hint?product=xxx
//
// 按归一化商品名找出这个店铺历史上卖过多少、卖过多少钱，给录入弹窗做定价参考。
// 只看真正的卖单（有成交价），排除弃单；返回最近一次、均价、最高、最低与单数。
// 名字写法不同（空格/大小写/标点）也能命中：走 product_norm 索引。
func handlePriceHint(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "方法不允许")
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("product"))
	nq := normProduct(name)
	if nq == "" {
		writeJSON(w, map[string]interface{}{"count": 0})
		return
	}
	sid := shopID(r)
	// 联单分摊：手填成本为 0 的卖单，用所在联单按售价占比分摊的单件成本。
	// 先取完派生数据再打开历史卖单查询，避免多人并发输入时占用两条连接互相等待。
	alloc := loadGroupAlloc(sid)
	rows, err := db.Query(`SELECT sell_price, sell_time, id, unit_cost, parent_id FROM items
        WHERE shop_id=? AND product_norm=? AND sell_price>0 AND discarded_at=''
        ORDER BY id DESC LIMIT 100`, sid, nq)
	if err != nil {
		writeJSON(w, map[string]interface{}{"count": 0})
		return
	}
	defer rows.Close()
	var sum, max, min float64
	count := 0
	var lastPrice float64
	var lastTime string
	var lastID int64
	var lastCost float64
	for rows.Next() {
		var price, unit float64
		var tm string
		var id, parent int64
		if rows.Scan(&price, &tm, &id, &unit, &parent) != nil {
			continue
		}
		if count == 0 {
			lastPrice, lastTime, lastID = price, tm, id
			lastCost = unit
			if lastCost <= 0 {
				lastCost = alloc[id]
			}
			max, min = price, price
		}
		sum += price
		if price > max {
			max = price
		}
		if price < min {
			min = price
		}
		count++
	}
	if count == 0 {
		writeJSON(w, map[string]interface{}{"count": 0})
		return
	}
	writeJSON(w, map[string]interface{}{
		"count": count,
		"last":  round2(lastPrice), "last_time": lastTime, "last_id": lastID,
		"last_cost": round2(lastCost),
		"avg":       round2(sum / float64(count)),
		"max":       round2(max), "min": round2(min),
	})
}

func handleStatsExtras(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "method not allowed")
		return
	}
	// 利润榜口径：未完结联单里的商品利润尚未闭环，不参与排名。
	// 先查出这些商品名（按名称聚合，同一商品只要挂过未完结联单就整体排除），
	// 且必须在主 rows 打开前查完，避免占用连接期间再开查询。
	unsettledProducts := map[string]bool{}
	if uRows, e := db.Query(`SELECT DISTINCT i.product FROM items i
		JOIN group_items gi ON gi.item_id = i.id
		JOIN link_groups g ON g.id = gi.group_id
		WHERE g.settled_at = '' AND g.shop_id = ? AND i.shop_id = ?`, shopID(r), shopID(r)); e == nil {
		for uRows.Next() {
			var name string
			if uRows.Scan(&name) == nil {
				unsettledProducts[name] = true
			}
		}
		uRows.Close()
	}
	// 联单内的明细统一由 computeGroupStats 按「联单口径」算利润，不再按商品名重复聚合：
	// 单条卖单没扣货品成本、买单利润天然为 0，直接累加单品 profit 会让完结联单永远进不了榜。
	groupItemIDs := map[int64]bool{}
	if gr, e := db.Query(`SELECT gi.item_id FROM group_items gi
		JOIN link_groups g ON g.id = gi.group_id WHERE g.shop_id=?`, shopID(r)); e == nil {
		for gr.Next() {
			var iid int64
			if gr.Scan(&iid) == nil {
				groupItemIDs[iid] = true
			}
		}
		gr.Close()
	}

	// 只统计最近一年入库的商品：更早的货要么早就卖完了，要么已经变成陈年库存，
	// 混在周转天数与利润榜里会把当下的经营情况冲淡。
	windowFrom := time.Now().AddDate(-1, 0, 0).Format("2006-01-02")
	// 已完结联单里的明细：这些商品在榜单上要标「已完结」
	groupSettledItems := map[int64]bool{}
	if gr, e := db.Query(`SELECT DISTINCT gi.item_id FROM group_items gi
        JOIN link_groups g ON g.id=gi.group_id WHERE g.settled_at<>'' AND g.shop_id=?`, shopID(r)); e == nil {
		for gr.Next() {
			var iid int64
			if gr.Scan(&iid) == nil {
				groupSettledItems[iid] = true
			}
		}
		gr.Close()
	}

	// 滞销阈值（天）：默认 60，可在设置中以 stale_threshold 覆盖。
	// 必须在打开明细查询前读取；否则单连接测试和多用户并发都会在这里互相等待。
	staleThreshold := 60
	if v, e := strconv.Atoi(getSetting("stale_threshold")); e == nil && v > 0 {
		staleThreshold = v
	}

	// 在库与周转统一按实际售出成本冲减，联单内尚未卖完的货也必须保留在库存。
	alloc := loadGroupAlloc(shopID(r))
	// 在库与周转必须覆盖全部仍未售出的采购。按记录创建时间截断会把更早入库、
	// 但后来才补录的库存漏掉，造成首页在库金额、件数和周转三者口径不一致。
	rows, err := db.Query("SELECT "+itemCols+" FROM items WHERE shop_id=?", shopID(r))
	if err != nil {
		writeError(w, 500, "统计失败")
		return
	}
	type agg struct {
		profit  float64
		sales   float64
		settled bool // 该商品有完结记录（联单完结或单独完结）
	}
	byProduct := map[string]*agg{}
	// costBacked：该商品名下的记录是否有「买入成本依据」（关联了买进单，或自己填了买价）。
	// 没有依据的利润是虚的，不参与排行。
	costBacked := map[string]bool{}
	var lots []holdingLot
	var saleDeductions []holdingDeduction
	const windowDays = 90
	cutoff := time.Now().AddDate(0, 0, -windowDays).Format("2006-01-02")
	var sales90 float64
	unsold := 0
	for rows.Next() {
		it, e := scanItem(rows)
		if e != nil {
			continue
		}
		// 弃单不进洞察：它不占在库、不计成本，摆进来只会让周转与利润失真
		if it.Kind == "discarded" {
			continue
		}
		// 寄卖 / 代采的货不是自己的，不进首页经营统计。
		if isExternalGoods(it.Source) {
			continue
		}

		inGroup := groupItemIDs[it.ID]
		switch it.Kind {
		case "purchase":
			gross := round2(it.BuyPrice + it.BuyShipping)
			if it.SettledAt == "" && gross > 0 {
				lots = append(lots, holdingLot{
					ID: it.ID, Product: it.Product, Norm: normProduct(it.Product),
					Gross: gross, BuyTime: it.BuyTime, AgeBand: holdingAgeBand(it.BuyTime),
				})
			}
		case "sale":
			qty := it.Quantity
			if qty < 1 {
				qty = 1
			}
			if !it.CostFree {
				deduct := 0.0
				if it.UnitCost > 0 {
					deduct = round2(it.UnitCost * float64(qty))
				} else if v, ok := alloc[it.ID]; ok {
					deduct = round2(v * float64(qty))
				}
				if deduct > 0 {
					saleDeductions = append(saleDeductions, holdingDeduction{
						ParentID: it.ParentID, Norm: normProduct(it.Product), Amount: deduct, SellTime: it.SellTime,
					})
				}
			}
			if d := it.DealTime; len(d) >= 10 && d[:10] >= cutoff {
				sales90 += it.SellPrice
			}
		case "standalone":
			if d := it.DealTime; len(d) >= 10 && d[:10] >= cutoff {
				sales90 += it.SellPrice
			}
		}

		// 联单内的利润由已完结联单统一聚合；这里不再按单条商品重复计算，
		// 但在库成本与周转仍要使用联单里的采购/卖单，否则库存会被整批漏掉。
		if inGroup {
			continue
		}
		a := byProduct[it.Product]
		if a == nil {
			a = &agg{}
			byProduct[it.Product] = a
		}
		if it.SettledAt != "" || groupSettledItems[it.ID] {
			a.settled = true
		}
		if it.ParentID > 0 || it.BuyPrice > 0 {
			costBacked[it.Product] = true
		}
		feeAll := sellFeeTotal(it)
		switch it.Kind {
		case "sale":
			qty := it.Quantity
			if qty < 1 {
				qty = 1
			}
			goods := round2(effectiveUnitCost(it, alloc) * float64(qty))
			a.sales += it.SellPrice
			a.profit += it.SellPrice - goods - it.SellShipping - feeAll
		case "standalone":
			a.sales += it.SellPrice
			a.profit += it.SellPrice - it.BuyPrice - it.BuyShipping - it.SellShipping - feeAll
		}
	}
	rows.Close()
	lots = applyHoldingDeductions(lots, saleDeductions)
	holdingCost := 0.0
	staleItems := []map[string]interface{}{}
	for _, lot := range lots {
		if lot.Remaining <= 0.005 {
			continue
		}
		unsold++
		holdingCost += lot.Remaining
		base := lot.BuyTime
		if len(base) >= 10 {
			if t, e := time.ParseInLocation("2006-01-02", base[:10], time.Local); e == nil {
				if dd := int(time.Since(t).Hours() / 24); dd >= staleThreshold {
					staleItems = append(staleItems, map[string]interface{}{
						"id": lot.ID, "product": lot.Product, "days": dd, "status": "pending",
					})
				}
			}
		}
	}
	holdingCost = round2(holdingCost)

	list := []topProduct{}
	// 已完结联单：联单内明细在单品口径下不算数（卖单没扣货品成本、买单利润天然为 0），
	// 必须用联单自己的口径（销售额 − 货品成本 − 运费 − 服务费）聚合，
	// 否则用户刚完结的联单永远不会出现在利润榜里。
	if gr, e := db.Query(`SELECT id, title FROM link_groups
		WHERE settled_at<>'' AND shop_id=? AND substr(created_at,1,10)>=?`, shopID(r), windowFrom); e == nil {
		type settledGroup struct {
			id    int64
			title string
		}
		groups := []settledGroup{}
		for gr.Next() {
			var gid int64
			var title string
			if gr.Scan(&gid, &title) == nil {
				groups = append(groups, settledGroup{gid, title})
			}
		}
		gr.Close()
		for _, g := range groups {
			gi, _, _ := loadGroupItems(g.id, shopID(r))
			ge := loadGroupExternals(g.id, shopID(r))
			st := computeGroupStats(gi, ge)
			// 没有买进成本的联单利润是虚的（纯卖按卖多少赚多少），与单品榜口径保持一致，不进榜
			if st.Goods <= 0 || st.Profit <= 0 {
				continue
			}
			// 榜单按「商品」排名，显示联单里卖单的商品名；没有卖单时退回联单标题
			name := g.title
			for _, it := range gi {
				if it.Product != "" && it.HasSell {
					name = it.Product
					break
				}
			}
			list = append(list, topProduct{name, round2(st.Profit), round2(st.Sales), true})
		}
	}
	for p, a := range byProduct {
		// 未完结联单里的商品不进榜；只展示真正盈利（>0）的商品，
		// 避免 ¥0.00 / 亏损条目占据 Top 5 名额。
		if unsettledProducts[p] {
			continue
		}
		// 没有买进成本的商品不进榜：同步进来的纯卖单没填买价、也没关联买进单，
		// 按「卖多少赚多少」算出来动辄几千，会把真正有成本、有联单完结的商品挤掉。
		if !costBacked[p] {
			continue
		}
		profit := round2(a.profit)
		if profit <= 0 {
			continue
		}
		list = append(list, topProduct{p, profit, round2(a.sales), a.settled})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Profit > list[j].Profit })
	if len(list) > 5 {
		list = list[:5]
	}
	sort.Slice(staleItems, func(i, j int) bool {
		return staleItems[i]["days"].(int) > staleItems[j]["days"].(int)
	})

	// 周转天数 = 实际在库占用资金 / 近 90 天日均销售额。
	// 用固定 90 天窗口而不是「最早~最晚成交日」的跨度：
	// 后者在只录了几天数据的店铺里会把日均销售额算得极小，周转天数飙到上万天。
	daily := sales90 / float64(windowDays)
	turnover := 0.0
	if daily > 0 {
		turnover = round2(holdingCost / daily)
		if turnover > 999 {
			turnover = 999
		}
	}
	writeJSON(w, map[string]interface{}{
		"top_products":    list,
		"turnover_days":   turnover,
		"unsold_count":    unsold,
		"stale_items":     staleItems,
		"stale_threshold": staleThreshold,
		"stale_count":     len(staleItems),
	})
}

// ============================================================
// 5. 导出增强辅助：自定义列 CSV / 利润表 CSV
// ============================================================

// exportColumnGetters 是 columns= 允许的白名单列（列名 -> 取值函数）。
var exportColumnGetters = map[string]func(Item) string{
	"id":                func(it Item) string { return strconv.FormatInt(it.ID, 10) },
	"product":           func(it Item) string { return it.Product },
	"buy_price":         func(it Item) string { return f2(it.BuyPrice) },
	"buy_shipping":      func(it Item) string { return f2(it.BuyShipping) },
	"buy_ship_tag":      func(it Item) string { return it.BuyShipTag },
	"buy_order_no":      func(it Item) string { return it.BuyOrderNo },
	"buy_time":          func(it Item) string { return it.BuyTime },
	"buy_counterparty":  func(it Item) string { return it.BuyCounterparty },
	"sell_price":        func(it Item) string { return f2(it.SellPrice) },
	"sell_fee":          func(it Item) string { return f2(it.SellFee) },
	"sell_wuyou_fee":    func(it Item) string { return f2(it.SellWuyouFee) },
	"sell_net":          func(it Item) string { return f2(it.SellNet) },
	"sell_shipping":     func(it Item) string { return f2(it.SellShipping) },
	"sell_ship_tag":     func(it Item) string { return it.SellShipTag },
	"sell_order_no":     func(it Item) string { return it.SellOrderNo },
	"sell_time":         func(it Item) string { return it.SellTime },
	"sell_counterparty": func(it Item) string { return it.SellCounterparty },
	"note":              func(it Item) string { return it.Note },
	"cost":              func(it Item) string { return f2(it.Cost) },
	"profit":            func(it Item) string { return f2(it.Profit) },
	"status":            func(it Item) string { return it.Status },
	"quantity":          func(it Item) string { return strconv.Itoa(it.Quantity) },
	"unit_cost":         func(it Item) string { return f2(it.UnitCost) },
	"created_at":        func(it Item) string { return it.CreatedAt },
	"updated_at":        func(it Item) string { return it.UpdatedAt },
}

func csvResponse(w http.ResponseWriter, buf *bytes.Buffer, ascii, zh string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	setDownloadName(w, ascii, zh)
	w.Write(buf.Bytes())
}

// writeColumnsCSV 只导出白名单内、按请求顺序排列的列。
func writeColumnsCSV(w http.ResponseWriter, items []Item, cols []string) {
	var buf bytes.Buffer
	buf.Write([]byte("\xEF\xBB\xBF")) // Excel 中文 BOM
	cw := csv.NewWriter(&buf)
	cw.UseCRLF = true
	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = c
	}
	writeCSVRow(cw, header)
	for _, it := range items {
		row := make([]string, len(cols))
		for i, c := range cols {
			row[i] = exportColumnGetters[c](it)
		}
		writeCSVRow(cw, row)
	}
	cw.Flush()
	stamp := time.Now().Format("20060102-150405")
	csvResponse(w, &buf, "xianyu-"+stamp+".csv", "闲鱼导出-"+stamp+".csv")
}

// writeProfitCSV 利润表：商品、买进价、运费、服务费、到手价、利润。
func writeProfitCSV(w http.ResponseWriter, items []Item) {
	var buf bytes.Buffer
	buf.Write([]byte("\xEF\xBB\xBF"))
	cw := csv.NewWriter(&buf)
	cw.UseCRLF = true
	writeCSVRow(cw, []string{"商品", "买进价", "运费", "服务费", "到手价", "利润"})
	for _, it := range items {
		writeCSVRow(cw, []string{
			it.Product,
			f2(it.BuyPrice),
			f2(it.BuyShipping + it.SellShipping),
			f2(it.SellFee + it.SellWuyouFee),
			f2(it.SellNet),
			f2(it.Profit),
		})
	}
	cw.Flush()
	stamp := time.Now().Format("20060102-150405")
	csvResponse(w, &buf, "xianyu-profit-"+stamp+".csv", "闲鱼利润表-"+stamp+".csv")
}

// ============================================================
// 11. 导入校验：写入前校验，错误信息中文
// ============================================================

var datePrefixRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)

func validateImportItems(items []Item) error {
	for i := range items {
		it := &items[i]
		for _, f := range []struct {
			name string
			v    float64
		}{
			{"买进价", it.BuyPrice}, {"买进运费", it.BuyShipping},
			{"卖出成交价", it.SellPrice}, {"软件服务费", it.SellFee},
			{"无忧卖服务费", it.SellWuyouFee}, {"卖出到手价", it.SellNet},
			{"卖出运费", it.SellShipping}, {"单件成本", it.UnitCost},
		} {
			if f.v < 0 {
				return fmt.Errorf("第 %d 条记录的%s为负数（%.2f），金额不能为负", i+1, f.name, f.v)
			}
		}
		for _, t := range []struct{ name, v string }{
			{"买进时间", it.BuyTime}, {"卖出时间", it.SellTime},
		} {
			if t.v != "" && !datePrefixRE.MatchString(t.v) {
				return fmt.Errorf("第 %d 条记录的%s格式不合法（%s），应为 YYYY-MM-DD 开头", i+1, t.name, t.v)
			}
		}
	}
	// 订单号重复（仅限本次导入数据内，跨 buy/sell 两侧）。
	seen := map[string]int{}
	for i := range items {
		it := &items[i]
		per := map[string]bool{}
		if it.BuyOrderNo != "" {
			per[it.BuyOrderNo] = true
		}
		if it.SellOrderNo != "" {
			per[it.SellOrderNo] = true
		}
		for no := range per {
			if prev, ok := seen[no]; ok {
				return fmt.Errorf("订单号「%s」重复：出现在第 %d 条与第 %d 条记录", no, prev+1, i+1)
			}
			seen[no] = i
		}
	}
	return nil
}

// ============================================================
// 9 & 10. 扩展表结构：ocr_feedback 表、items.attachment 幂等补列
// ============================================================

func ensureAttachmentColumn() {
	var n int
	db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('items') WHERE name='attachment'").Scan(&n)
	if n == 0 {
		db.Exec("ALTER TABLE items ADD COLUMN attachment TEXT NOT NULL DEFAULT ''")
	}
}

func ensureOCRFeedbackTable() {
	db.Exec(`CREATE TABLE IF NOT EXISTS ocr_feedback (
        id         INTEGER PRIMARY KEY AUTOINCREMENT,
        record_id  INTEGER NOT NULL DEFAULT 0,
        correct    INTEGER NOT NULL DEFAULT 0,
        note       TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL
    )`)
}

// initExtrasSchema 服务启动时调用一次；handler 内也会幂等调用，兼容旧库与测试。
func initExtrasSchema() error {
	ensureOCRFeedbackTable()
	ensureAttachmentColumn()
	return nil
}

// ============================================================
// 9. OCR 反馈：POST/GET /api/ocr-feedback
// ============================================================

func handleOCRFeedback(w http.ResponseWriter, r *http.Request) {
	initExtrasSchema()
	switch r.Method {
	case "POST":
		var in struct {
			RecordID int64  `json:"record_id"`
			Correct  bool   `json:"correct"`
			Note     string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, 400, "请求体解析失败")
			return
		}
		c := 0
		if in.Correct {
			c = 1
		}
		if _, err := db.Exec("INSERT INTO ocr_feedback(record_id,correct,note,created_at) VALUES(?,?,?,?)",
			in.RecordID, c, in.Note, nowStr()); err != nil {
			writeError(w, 500, "保存反馈失败")
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	case "GET":
		rows, err := db.Query("SELECT id, record_id, correct, note, created_at FROM ocr_feedback ORDER BY id DESC LIMIT 50")
		if err != nil {
			writeError(w, 500, "查询反馈失败")
			return
		}
		defer rows.Close()
		items := []map[string]interface{}{}
		for rows.Next() {
			var id, rid int64
			var correct int
			var note, created string
			if rows.Scan(&id, &rid, &correct, &note, &created) != nil {
				continue
			}
			items = append(items, map[string]interface{}{
				"id": id, "record_id": rid, "correct": correct != 0, "note": note, "created_at": created,
			})
		}
		writeJSON(w, map[string]interface{}{"items": items})
	default:
		writeError(w, 405, "method not allowed")
	}
}

// ============================================================
// 10. 记录附件：POST/GET /api/records/{id}/attachment
// ============================================================

var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true, ".bmp": true, ".heic": true,
}

var attachNameRE = regexp.MustCompile(`^\d+_[0-9a-f]+\.[a-z]+$`)

// attachHashRE 取文件名里的随机段（<id>_<hex>.<ext>），用作缓存版本号。
var attachHashRE = regexp.MustCompile(`^\d+_([0-9a-f]+)\.[a-z]+$`)

func attachHash(name string) string {
	if m := attachHashRE.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}

// removeAttachmentFiles 删除这些记录对应的附件文件。
// 必须在 DELETE 之前调用：行一旦删掉就查不到文件名，文件会永久成为孤儿。
func removeAttachmentFiles(ids []int64, shopID int64) {
	dir := attachmentsDir()
	for _, id := range ids {
		var name string
		if err := db.QueryRow("SELECT COALESCE(attachment,'') FROM items WHERE id=? AND shop_id=?", id, shopID).Scan(&name); err != nil || name == "" {
			continue
		}
		// 只删文件名本身，避免任何路径穿越
		if filepath.Base(name) != name {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
		removeThumbs(name) // 缩略图跟着一起清，不留缓存垃圾
	}
}

func attachmentsDir() string {
	// CONFIG_DIR：把「运行期非数据库文件」单独放一个目录（Docker 部署时挂 config 卷）。
	// 不设置时退回老行为（跟数据库同级的 attachments/），PVE 上的现有部署不受影响。
	if d := os.Getenv("CONFIG_DIR"); d != "" {
		return filepath.Join(d, "attachments")
	}
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "xianyu.db"
	}
	return filepath.Join(filepath.Dir(dbPath), "attachments")
}

func attachmentID(r *http.Request) (int64, error) {
	s := r.PathValue("id")
	if s == "" {
		p := strings.TrimPrefix(r.URL.Path, "/api/records/")
		p = strings.TrimSuffix(p, "/attachment")
		s = p
	}
	return strconv.ParseInt(s, 10, 64)
}

func extFromContentType(ct string) string {
	switch strings.ToLower(ct) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	case "image/heic":
		return ".heic"
	}
	return ""
}

func contentTypeFromName(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".heic":
		return "image/heic"
	}
	return "application/octet-stream"
}

func validAttachmentName(name string) bool { return attachNameRE.MatchString(name) }

func handleAttachment(w http.ResponseWriter, r *http.Request) {
	id, err := attachmentID(r)
	if err != nil || id <= 0 {
		writeError(w, 400, "无效 id")
		return
	}
	ensureAttachmentColumn()
	switch r.Method {
	case "POST":
		var exists int
		db.QueryRow("SELECT COUNT(*) FROM items WHERE id=? AND shop_id=?", id, shopID(r)).Scan(&exists)
		if exists == 0 {
			writeError(w, 404, "记录不存在")
			return
		}
		// 5MB 限制：给 multipart 边界留 1MB 余量，读取后再精确判断。
		r.Body = http.MaxBytesReader(w, r.Body, (5<<20)+(1<<20))
		if err := r.ParseMultipartForm(5 << 20); err != nil {
			writeError(w, 400, "解析表单失败或文件超过 5MB")
			return
		}
		file, header, err := r.FormFile("image")
		if err != nil {
			writeError(w, 400, "缺少 image 字段")
			return
		}
		defer file.Close()
		ct := header.Header.Get("Content-Type")
		if !strings.HasPrefix(strings.ToLower(ct), "image/") {
			writeError(w, 400, "仅支持 image/* 图片文件")
			return
		}
		data, err := io.ReadAll(io.LimitReader(file, (5<<20)+1))
		if err != nil {
			writeError(w, 500, "读取图片失败")
			return
		}
		if len(data) > 5<<20 {
			writeError(w, 400, "文件超过 5MB")
			return
		}
		ext := strings.ToLower(filepath.Ext(header.Filename))
		if !imageExts[ext] {
			ext = extFromContentType(ct)
		}
		if ext == "" {
			writeError(w, 400, "无法识别图片格式")
			return
		}
		buf := make([]byte, 8)
		rand.Read(buf)
		name := fmt.Sprintf("%d_%s%s", id, hex.EncodeToString(buf), ext)
		dir := attachmentsDir()
		if err := os.MkdirAll(dir, 0755); err != nil {
			writeError(w, 500, "创建附件目录失败")
			return
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			writeError(w, 500, "保存附件失败")
			return
		}
		// 换图时把旧文件与旧缩略图一起清掉，否则旧图永远是磁盘上的孤儿
		var oldName string
		db.QueryRow("SELECT COALESCE(attachment,'') FROM items WHERE id=? AND shop_id=?", id, shopID(r)).Scan(&oldName)
		if _, err := db.Exec("UPDATE items SET attachment=?, updated_at=? WHERE id=?", name, nowStr(), id); err != nil {
			writeError(w, 500, "记录附件失败")
			return
		}
		if oldName != "" && oldName != name && validAttachmentName(oldName) {
			_ = os.Remove(filepath.Join(dir, oldName))
			removeThumbs(oldName)
		}
		// 预先算好列表用的小图：否则第一次打开列表时，每条记录都要现场解码
		// 1MB 原图（约 200ms/张）。放后台做，保存接口本身不用等。
		go func(n string) {
			ensureThumb(n, 160)
			ensureThumb(n, 720)
		}(name)
		writeJSON(w, map[string]interface{}{"ok": true, "filename": name})
	case "GET":
		var name string
		if err := db.QueryRow("SELECT attachment FROM items WHERE id=? AND shop_id=?", id, shopID(r)).Scan(&name); err != nil {
			writeError(w, 404, "附件不存在")
			return
		}
		if name == "" || !validAttachmentName(name) {
			writeError(w, 404, "附件不存在")
			return
		}
		// 文件名里带一段随机 hex，内容永不改变；前端把这段 hex 当 ?v= 传回来，
		// 就能安全地强缓存一年。没带 v 时只允许协商缓存，避免换图后看到旧图。
		hash := attachHash(name)
		if hash != "" && r.URL.Query().Get("v") == hash {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		// ?w=320 走缩略图：手机上列表只要几十像素，没必要拉 1MB 原图
		if tw, _ := strconv.Atoi(r.URL.Query().Get("w")); tw > 0 {
			if p := ensureThumb(name, tw); p != "" {
				serveAttachmentFile(w, r, p, "image/jpeg")
				return
			}
			// webp / heic 等没有标准库解码器：回落原图，功能不受影响
		}
		serveAttachmentFile(w, r, filepath.Join(attachmentsDir(), name), contentTypeFromName(name))
	default:
		writeError(w, 405, "method not allowed")
	}
}

// serveAttachmentFile 以 ServeContent 输出，自带 Range 与 If-Modified-Since（304）。
func serveAttachmentFile(w http.ResponseWriter, r *http.Request, path, contentType string) {
	f, err := os.Open(path)
	if err != nil {
		writeError(w, 404, "附件不存在")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeError(w, 404, "附件不存在")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), f)
}

// ============================================================
// 13. 备份对账：GET /api/backup/verify
// ============================================================

// countItemsInBackup 只读打开最新备份（query_only，绝不写备份文件），数 items。
func countItemsInBackup(path string) (int, error) {
	if _, err := os.Stat(path); err != nil {
		return 0, err
	}
	d, err := sql.Open("sqlite", path+"?_pragma=query_only(1)&_pragma=busy_timeout(3000)")
	if err != nil {
		return 0, err
	}
	defer d.Close()
	d.SetMaxOpenConns(1)
	var n int
	err = d.QueryRow("SELECT COUNT(*) FROM items").Scan(&n)
	return n, err
}

func handleBackupVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "method not allowed")
		return
	}
	var inDB int
	db.QueryRow("SELECT COUNT(*) FROM items").Scan(&inDB)

	var inBackup interface{} // 失败时为 nil -> JSON null
	consistent := false
	files := listBackupFiles() // 已按文件名（含时间戳）倒序，[0] 为最新
	if len(files) > 0 {
		path := filepath.Join(backupDir(), files[0].Name)
		if n, err := countItemsInBackup(path); err == nil {
			inBackup = n
			consistent = n == inDB
		}
	}
	writeJSON(w, map[string]interface{}{
		"ok": true, "items_in_db": inDB, "items_in_backup": inBackup, "consistent": consistent,
	})
}

// ============================================================
// 8. 趋势周期：week/quarter 按月数据再聚合（数据不足退回原样）
// ============================================================

type trendBucket struct {
	Month  string  `json:"month"`
	Sales  float64 `json:"sales"`
	Cost   float64 `json:"cost"`
	Profit float64 `json:"profit"`
	Count  int     `json:"count"`
}

// applyTrendPeriod 在 handleTrend 得到月度数据后做二次聚合。
// quarter 把月份合并为季度；week 因月数据无法还原真实周，近似把每月拆成 4 个周桶。
// 有效月桶不足时原样返回，绝不报错。
func applyTrendPeriod(points []trendBucket, period string) []trendBucket {
	if period != "quarter" && period != "week" {
		return points
	}
	valid := 0
	for _, b := range points {
		if len(b.Month) >= 7 {
			valid++
		}
	}
	if (period == "quarter" && valid < 2) || (period == "week" && valid < 1) {
		return points
	}

	agg := map[string]*trendBucket{}
	var noTime trendBucket
	for _, b := range points {
		if len(b.Month) < 7 {
			noTime.Sales += b.Sales
			noTime.Cost += b.Cost
			noTime.Count += b.Count
			continue
		}
		if period == "quarter" {
			y := b.Month[:4]
			m, _ := strconv.Atoi(b.Month[5:7])
			key := fmt.Sprintf("%s-Q%d", y, (m-1)/3+1)
			a := agg[key]
			if a == nil {
				a = &trendBucket{Month: key}
				agg[key] = a
			}
			a.Sales += b.Sales
			a.Cost += b.Cost
			a.Count += b.Count
		} else {
			for w := 1; w <= 4; w++ {
				key := fmt.Sprintf("%s-W%d", b.Month, w)
				a := agg[key]
				if a == nil {
					a = &trendBucket{Month: key}
					agg[key] = a
				}
				a.Sales += b.Sales / 4
				a.Cost += b.Cost / 4
				a.Count += b.Count / 4
			}
		}
	}
	keys := make([]string, 0, len(agg))
	for k := range agg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]trendBucket, 0, len(keys)+1)
	for _, k := range keys {
		a := agg[k]
		out = append(out, trendBucket{k, round2(a.Sales), round2(a.Cost), round2(a.Sales - a.Cost), a.Count})
	}
	if noTime.Count > 0 {
		out = append(out, trendBucket{"", round2(noTime.Sales), round2(noTime.Cost), round2(noTime.Sales - noTime.Cost), noTime.Count})
	}
	return out
}

// ============================================================
// 14. 只读账号辅助（token 逻辑在 auth.go）
// ============================================================

func setReadonlyPassword(pw string) error {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	salt := hex.EncodeToString(b)
	if err := setSetting("readonly_salt", salt); err != nil {
		return err
	}
	h, err := hashPassword(pw, salt)
	if err != nil {
		return err
	}
	return setSetting("readonly_password_hash", h)
}

func clearReadonlyPassword() {
	setSetting("readonly_password_hash", "")
	setSetting("readonly_salt", "")
}

// ============================================================
// 12. 登录限流辅助：同 IP 10 次/分钟（滑动窗口）
// ============================================================

var loginLimiter = struct {
	sync.Mutex
	hits map[string][]time.Time
}{hits: map[string][]time.Time{}}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func allowLogin(ip string) bool {
	loginLimiter.Lock()
	defer loginLimiter.Unlock()
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	arr := loginLimiter.hits[ip]
	i := 0
	for i < len(arr) && arr[i].Before(cutoff) {
		i++
	}
	arr = arr[i:]
	if len(arr) >= 10 {
		loginLimiter.hits[ip] = arr
		return false
	}
	loginLimiter.hits[ip] = append(arr, now)
	return true
}

// resetLoginLimiter 仅供测试复位全局状态。
func resetLoginLimiter() {
	loginLimiter.Lock()
	loginLimiter.hits = map[string][]time.Time{}
	loginLimiter.Unlock()
}

// ---------- 寄卖（归属人）统计 ----------

// handleConsignStats GET /api/consign/stats?from=&to=
// 代卖（寄卖）+ 代采 两本账：区间内卖了多少 / 买了多少，累计已结与待结/应付。
func handleConsignStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "方法不允许")
		return
	}
	from := strings.TrimSpace(r.URL.Query().Get("from"))
	to := strings.TrimSpace(r.URL.Query().Get("to"))
	inRange := func(t string) bool {
		d := t
		if len(d) > 10 {
			d = d[:10]
		}
		if from != "" && d < from {
			return false
		}
		if to != "" && d > to {
			return false
		}
		return true
	}

	type sellItem struct {
		ID           int64   `json:"id"`
		Product      string  `json:"product"`
		SellPrice    float64 `json:"sell_price"`
		SellNet      float64 `json:"sell_net"`
		SellFee      float64 `json:"sell_fee"`
		SellShipping float64 `json:"sell_shipping"`
		SellTime     string  `json:"sell_time"`
		Counterparty string  `json:"counterparty"`
	}
	type buyItem struct {
		ID           int64   `json:"id"`
		Product      string  `json:"product"`
		BuyPrice     float64 `json:"buy_price"`
		BuyShipping  float64 `json:"buy_shipping"`
		BuyTime      string  `json:"buy_time"`
		Counterparty string  `json:"counterparty"`
	}
	type sellOwner struct {
		Owner   string     `json:"owner"`
		Orders  int        `json:"orders"`
		Sales   float64    `json:"sales"`
		Net     float64    `json:"net"`
		Fee     float64    `json:"fee"`
		Settled float64    `json:"settled"`
		Pending float64    `json:"pending"`
		Items   []sellItem `json:"items"`
	}
	type buyOwner struct {
		Owner   string    `json:"owner"`
		Orders  int       `json:"orders"`
		Amount  float64   `json:"amount"`
		Settled float64   `json:"settled"`
		Pending float64   `json:"pending"`
		Items   []buyItem `json:"items"`
	}

	// 已结算金额：按 owner + kind 分组（sell=我付给寄卖人，buy=代采人还给我）
	settledSell := map[string]float64{}
	settledBuy := map[string]float64{}
	if sr, e := db.Query(`SELECT owner, kind, COALESCE(SUM(amount),0) FROM consign_settlements
        WHERE shop_id=? GROUP BY owner, kind`, shopID(r)); e == nil {
		for sr.Next() {
			var n, k string
			var v float64
			if sr.Scan(&n, &k, &v) == nil {
				n = strings.TrimSpace(n)
				if k == "buy" {
					settledBuy[n] += v
				} else {
					settledSell[n] += v
				}
			}
		}
		sr.Close()
	}

	rows, err := db.Query("SELECT "+itemCols+" FROM items WHERE shop_id=? AND source IN ('寄卖','代采') AND discarded_at='' ORDER BY COALESCE(sell_time, buy_time) DESC", shopID(r))
	if err != nil {
		writeError(w, 500, "寄卖统计失败")
		return
	}
	defer rows.Close()

	sellAgg := map[string]*sellOwner{}
	sellOrder := []string{}
	buyAgg := map[string]*buyOwner{}
	buyOrder := []string{}
	allSellNet := map[string]float64{}
	allBuyAmount := map[string]float64{}
	allSellNames := map[string]int{}
	allBuyNames := map[string]int{}
	historySellOrders, historyBuyOrders := 0, 0
	historySellLatest, historyBuyLatest := "", ""

	for rows.Next() {
		it, e := scanItem(rows)
		if e != nil || it.Kind == "discarded" {
			continue
		}
		name := strings.TrimSpace(it.Owner)
		if name == "" {
			name = "未指定归属"
		}
		if it.Source == "寄卖" {
			allSellNet[name] += it.SellNet
			if name != "未指定归属" {
				allSellNames[name]++
			}
			historySellOrders++
			if it.SellTime > historySellLatest {
				historySellLatest = it.SellTime
			}
			if !inRange(it.SellTime) {
				continue
			}
			o := sellAgg[name]
			if o == nil {
				o = &sellOwner{Owner: name}
				sellAgg[name] = o
				sellOrder = append(sellOrder, name)
			}
			o.Orders++
			o.Sales += it.SellPrice
			o.Net += it.SellNet
			o.Fee += it.SellFee + it.SellWuyouFee + it.SellShipping
			o.Items = append(o.Items, sellItem{
				ID: it.ID, Product: it.Product, SellPrice: it.SellPrice, SellNet: it.SellNet,
				SellFee: it.SellFee + it.SellWuyouFee, SellShipping: it.SellShipping,
				SellTime: it.SellTime, Counterparty: it.SellCounterparty,
			})
		} else if it.Source == "代采" {
			// 代采是「我垫钱帮人买」：垫付 = 买进价 + 买入运费。
			// 没买进信息的单据（卖单被误标代采）不产生应付，跳过免得出现 0 元条目。
			if !it.HasBuy {
				continue
			}
			amount := it.BuyPrice + it.BuyShipping
			allBuyAmount[name] += amount
			if name != "未指定归属" {
				allBuyNames[name]++
			}
			historyBuyOrders++
			if it.BuyTime > historyBuyLatest {
				historyBuyLatest = it.BuyTime
			}
			if !inRange(it.BuyTime) {
				continue
			}
			o := buyAgg[name]
			if o == nil {
				o = &buyOwner{Owner: name}
				buyAgg[name] = o
				buyOrder = append(buyOrder, name)
			}
			o.Orders++
			o.Amount += amount
			o.Items = append(o.Items, buyItem{
				ID: it.ID, Product: it.Product, BuyPrice: it.BuyPrice, BuyShipping: it.BuyShipping,
				BuyTime: it.BuyTime, Counterparty: it.BuyCounterparty,
			})
		}
	}

	owners := make([]sellOwner, 0, len(sellAgg))
	var tOrders int
	var tSales, tNet, tPending, tSettled float64
	for _, k := range sellOrder {
		v := sellAgg[k]
		v.Sales = round2(v.Sales)
		v.Net = round2(v.Net)
		v.Fee = round2(v.Fee)
		v.Settled = round2(settledSell[k])
		v.Pending = round2(allSellNet[k] - settledSell[k])
		if v.Pending < 0 {
			v.Pending = 0
		}
		owners = append(owners, *v)
		tOrders += v.Orders
		tSales += v.Sales
		tNet += v.Net
		tPending += v.Pending
		tSettled += v.Settled
	}
	buyers := make([]buyOwner, 0, len(buyAgg))
	var bOrders int
	var bAmount, bPending, bSettled float64
	for _, k := range buyOrder {
		v := buyAgg[k]
		v.Amount = round2(v.Amount)
		v.Settled = round2(settledBuy[k])
		v.Pending = round2(allBuyAmount[k] - settledBuy[k])
		if v.Pending < 0 {
			v.Pending = 0
		}
		buyers = append(buyers, *v)
		bOrders += v.Orders
		bAmount += v.Amount
		bPending += v.Pending
		bSettled += v.Settled
	}
	sortedNames := func(counts map[string]int) []string {
		names := make([]string, 0, len(counts))
		for name := range counts {
			names = append(names, name)
		}
		sort.Slice(names, func(i, j int) bool {
			if counts[names[i]] == counts[names[j]] {
				return names[i] < names[j]
			}
			return counts[names[i]] > counts[names[j]]
		})
		return names
	}
	ownerNames := sortedNames(allSellNames)
	buyerNames := sortedNames(allBuyNames)

	writeJSON(w, map[string]interface{}{
		"owners":             owners,
		"buyers":             buyers,
		"owner_names":        ownerNames,
		"buyer_names":        buyerNames,
		"service_people":     len(ownerNames),
		"buy_service_people": len(buyerNames),
		"total": map[string]interface{}{
			"orders": tOrders, "sales": round2(tSales), "net": round2(tNet),
			"pending": round2(tPending), "settled": round2(tSettled),
			"buy_orders": bOrders, "buy_amount": round2(bAmount),
			"buy_pending": round2(bPending), "buy_settled": round2(bSettled),
			"history_orders": historySellOrders, "history_latest": historySellLatest,
			"buy_history_orders": historyBuyOrders, "buy_history_latest": historyBuyLatest,
		},
	})
}

// handleConsignSettle POST /api/consign/settle {owner, kind, amount, note}
// kind=sell：我付给寄卖人；kind=buy：代采人把垫付的钱还给我。
func handleConsignSettle(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	var in struct {
		Owner  string  `json:"owner"`
		Kind   string  `json:"kind"`
		Amount float64 `json:"amount"`
		Note   string  `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&in); err != nil {
		writeError(w, 400, "请求体解析失败")
		return
	}
	owner := strings.TrimSpace(in.Owner)
	if owner == "" {
		writeError(w, 400, "缺少归属人")
		return
	}
	kind := in.Kind
	if kind != "buy" {
		kind = "sell"
	}
	amount := round2(in.Amount)
	if amount <= 0 {
		writeError(w, 400, "结算金额必须大于 0")
		return
	}
	ts := nowStr()
	if _, err := db.Exec(`INSERT INTO consign_settlements(shop_id, owner, kind, amount, note, settled_at, created_at)
        VALUES(?,?,?,?,?,?,?)`, shopID(r), owner, kind, amount, strings.TrimSpace(in.Note), ts, ts); err != nil {
		writeError(w, 500, "保存结算记录失败")
		return
	}
	action := "寄卖结算："
	if kind == "buy" {
		action = "代采收款："
	}
	realtimeHub.publish("POST", "/api/consign/settle", "", action+owner, shopID(r))
	writeJSON(w, map[string]interface{}{"ok": true, "owner": owner, "kind": kind, "amount": amount, "settled_at": ts})
}

// handleConsignSettlements GET /api/consign/settlements?owner=&kind=&from=&to=
func handleConsignSettlements(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "方法不允许")
		return
	}
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	from := strings.TrimSpace(r.URL.Query().Get("from"))
	to := strings.TrimSpace(r.URL.Query().Get("to"))
	q := `SELECT id, owner, kind, amount, note, settled_at FROM consign_settlements WHERE shop_id=?`
	args := []interface{}{shopID(r)}
	if owner != "" {
		q += " AND owner=?"
		args = append(args, owner)
	}
	if kind == "buy" || kind == "sell" {
		q += " AND kind=?"
		args = append(args, kind)
	}
	if from != "" {
		q += " AND substr(settled_at,1,10)>=?"
		args = append(args, from)
	}
	if to != "" {
		q += " AND substr(settled_at,1,10)<=?"
		args = append(args, to)
	}
	q += " ORDER BY settled_at DESC, id DESC LIMIT 300"
	rows, err := db.Query(q, args...)
	if err != nil {
		writeError(w, 500, "读取结算记录失败")
		return
	}
	defer rows.Close()
	type row struct {
		ID        int64   `json:"id"`
		Owner     string  `json:"owner"`
		Kind      string  `json:"kind"`
		Amount    float64 `json:"amount"`
		Note      string  `json:"note"`
		SettledAt string  `json:"settled_at"`
	}
	out := []row{}
	for rows.Next() {
		var v row
		if rows.Scan(&v.ID, &v.Owner, &v.Kind, &v.Amount, &v.Note, &v.SettledAt) == nil {
			v.Amount = round2(v.Amount)
			out = append(out, v)
		}
	}
	writeJSON(w, map[string]interface{}{"items": out})
}

// handleConsignOwners GET /api/consign/owners
// 归属人候选：本店所有填过归属的记录（含非寄卖），录入弹窗自动补全用。
func handleConsignOwners(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "方法不允许")
		return
	}
	rows, err := db.Query(`SELECT DISTINCT TRIM(owner) AS owner FROM items
		WHERE shop_id=? AND TRIM(owner)<>'' ORDER BY owner`, shopID(r))
	if err != nil {
		writeError(w, 500, "读取归属列表失败")
		return
	}
	defer rows.Close()
	names := []string{}
	seen := map[string]bool{}
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			n = strings.TrimSpace(n)
			if n != "" && !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
	}
	writeJSON(w, map[string]interface{}{"owners": names})
}

// handleConsignAssignOwner POST /api/consign/assign-owner
// 给历史遗留的「未指定归属」单据批量补录归属；只改仍然为空的对应来源单据。
func handleConsignAssignOwner(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	var in struct {
		Owner   string  `json:"owner"`
		Kind    string  `json:"kind"`
		ItemIDs []int64 `json:"item_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, 400, "请求体解析失败")
		return
	}
	owner := strings.TrimSpace(in.Owner)
	if owner == "" {
		writeError(w, 400, "请填写归属人")
		return
	}
	source := map[string]string{"sell": "寄卖", "buy": "代采"}[in.Kind]
	if source == "" {
		writeError(w, 400, "账本类型无效")
		return
	}
	if len(in.ItemIDs) == 0 || len(in.ItemIDs) > 500 {
		writeError(w, 400, "没有待补归属的单据")
		return
	}

	seen := map[int64]bool{}
	ids := make([]int64, 0, len(in.ItemIDs))
	for _, id := range in.ItemIDs {
		if id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		writeError(w, 400, "没有待补归属的单据")
		return
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, 500, "补录归属失败")
		return
	}
	defer tx.Rollback()
	ts := nowStr()
	updated := 0
	for _, id := range ids {
		res, e := tx.Exec(`UPDATE items SET owner=?, updated_at=?
			WHERE id=? AND shop_id=? AND source=? AND TRIM(owner)=''`,
			owner, ts, id, shopID(r), source)
		if e != nil {
			writeError(w, 500, "补录归属失败")
			return
		}
		n, _ := res.RowsAffected()
		updated += int(n)
	}
	if updated == 0 {
		writeError(w, 409, "这些单据已经有归属，请刷新后重试")
		return
	}
	// 同一账本的全部空归属都补录完时，把历史上落在占位名下的结算流水一起迁移。
	// 否则订单换了归属人，之前的已结/已收金额会留在「未指定归属」，余额会凭空变大。
	var remaining int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM items
		WHERE shop_id=? AND source=? AND discarded_at='' AND TRIM(owner)=''`,
		shopID(r), source).Scan(&remaining); err != nil {
		writeError(w, 500, "补录归属失败")
		return
	}
	moved := int64(0)
	if remaining == 0 {
		res, e := tx.Exec(`UPDATE consign_settlements SET owner=?
			WHERE shop_id=? AND owner='未指定归属' AND kind=?`,
			owner, shopID(r), in.Kind)
		if e != nil {
			writeError(w, 500, "迁移历史收款失败")
			return
		}
		moved, _ = res.RowsAffected()
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "补录归属失败")
		return
	}
	realtimeHub.publish("POST", "/api/consign/assign-owner", "", "补录寄卖归属："+owner, shopID(r))
	writeJSON(w, map[string]interface{}{"ok": true, "owner": owner, "updated": updated, "settlements_moved": moved})
}

// handleConsignSettlementByID DELETE /api/consign/settlements/{id}
// 结算记错了可以删掉，待结会自动加回来。
func handleConsignSettlementByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != "DELETE" {
		writeError(w, 405, "方法不允许")
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/consign/settlements/"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, 400, "无效记录")
		return
	}
	res, e := db.Exec("DELETE FROM consign_settlements WHERE id=? AND shop_id=?", id, shopID(r))
	if e != nil {
		writeError(w, 500, "删除失败")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, 404, "记录不存在")
		return
	}
	realtimeHub.publish("DELETE", "/api/consign/settlements", "", "删除寄卖结算记录", shopID(r))
	writeJSON(w, map[string]interface{}{"ok": true})
}
