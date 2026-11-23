package main

// ============================================================
// 单商品完结 / 弃单
//
// 完结（settle）：这条单据自己结案，不组联单。
//   - 从「买单 / 卖单」列表里移到「完结单」
//   - 买进单不再占用在库资金、不再算待售
//   - 不再出现在联单候选与自动匹配里
//   - 与「联单完结」在统计口径上等价（都进 完成单净利润）
//
// 弃单（discard）：这单作废。
//   - 记录保留（备注、截图、订单号都在），可以随时取消
//   - 不占在库、不计成本、不计销售额与利润率，等于没录过
//
// 两者都用时间戳字段表示，空=未生效，所以取消就是写回空串。
// ============================================================

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

type itemFlagReq struct {
	On bool `json:"on"`
}

// itemIDFromPath 从 /api/records/{id}/settle 这类路径里取 id
func itemIDFromPath(r *http.Request, suffix string) int64 {
	s := r.PathValue("id")
	if s == "" {
		p := strings.TrimPrefix(r.URL.Path, "/api/records/")
		p = strings.TrimSuffix(p, "/"+suffix)
		s = p
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func handleAttachmentSettle(w http.ResponseWriter, r *http.Request) {
	if id := itemIDFromPath(r, "settle"); id > 0 {
		handleItemSettle(w, r, id)
		return
	}
	writeError(w, 400, "无效 id")
}

func handleAttachmentDiscard(w http.ResponseWriter, r *http.Request) {
	if id := itemIDFromPath(r, "discard"); id > 0 {
		handleItemDiscard(w, r, id)
		return
	}
	writeError(w, 400, "无效 id")
}

// handleItemSettle POST /api/records/{id}/settle  {"on":true|false}
func handleItemSettle(w http.ResponseWriter, r *http.Request, id int64) {
	setItemFlag(w, r, id, "settle")
}

// handleItemDiscard POST /api/records/{id}/discard {"on":true|false}
func handleItemDiscard(w http.ResponseWriter, r *http.Request, id int64) {
	setItemFlag(w, r, id, "discard")
}

func setItemFlag(w http.ResponseWriter, r *http.Request, id int64, kind string) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	var in itemFlagReq
	if !decodeJSONBody(w, r, &in, maxTinyJSONBody) {
		return
	}
	it, err := getItem(id, shopID(r))
	if err != nil {
		writeError(w, 404, "记录不存在")
		return
	}

	// 属于联单的单据不能在明细里单独改状态：联单是一个整体，
	// 单边改动会让「完成单净利润」对不上账，所以统一回联单页操作。
	if gid, gtitle := groupOfItem(id, shopID(r)); gid > 0 {
		writeError(w, 409, "「"+gtitle+"」是联单里的记录，请到「联单」页操作（完结或移出联单）后再试")
		return
	}

	// 没有成本单的卖单（父记录为空、也没录买价/买运费）不需要完结：
	// 完结的意义是「这批货的成本可以出库了」，而这类单据本来就没有占用的资金，
	// 让它完结反而会误导统计，所以直接拒绝。
	if kind == "settle" && in.On && it.ParentID == 0 && it.HasSell && !it.HasBuy {
		writeError(w, 409, "这条卖单没有买进成本，不需要完结；它不计入在库占用，也不会影响利润口径")
		return
	}

	var col string
	switch kind {
	case "settle":
		col = "settled_at"
	case "discard":
		col = "discarded_at"
	default:
		writeError(w, 400, "无效操作")
		return
	}

	// 弃单与完结互斥：弃单时顺手清掉完结时间，避免出现「既弃单又完结」的怪状态。
	val := ""
	if in.On {
		val = nowStr()
	}
	setSQL := "UPDATE items SET " + col + "=?, updated_at=? WHERE id=? AND shop_id=?"
	args := []interface{}{val, nowStr(), id, shopID(r)}
	if in.On {
		other := "settled_at"
		if kind == "settle" {
			other = "discarded_at"
		}
		setSQL = "UPDATE items SET " + col + "=?, " + other + "='', updated_at=? WHERE id=? AND shop_id=?"
		args = []interface{}{val, nowStr(), id, shopID(r)}
	}
	if _, err := db.Exec(strings.TrimSpace(setSQL), args...); err != nil {
		writeError(w, 500, "更新失败")
		return
	}
	refreshPurchaseIndex() // 完结/弃单会改变在库与联单候选，索引必须跟着更新

	// 变更广播由统一中间件负责（路径以 /api/records 开头），这里不再重复发一次。
	writeJSON(w, map[string]interface{}{"ok": true, "on": in.On, "product": it.Product})
}

// groupOfItem 取这条明细所属的联单（不区分是否完结）
func groupOfItem(itemID int64, shopID int64) (int64, string) {
	var gid int64
	var title string
	err := db.QueryRow(`SELECT g.id, g.title FROM group_items gi
        JOIN link_groups g ON g.id=gi.group_id
        WHERE gi.item_id=? AND g.shop_id=? LIMIT 1`, itemID, shopID).Scan(&gid, &title)
	if err != nil {
		return 0, ""
	}
	return gid, title
}

// boolToInt：SQLite 没有 bool 列，统一按 0/1 存
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---------- 批量标记无成本：代卖 / 寄卖 / 赠送这类本来就没有货品成本的卖单 ----------

type costFreeReq struct {
	IDs  []int64 `json:"ids"`
	Free bool    `json:"free"` // true=标记无成本，false=取消标记
}

// handleCostFree POST /api/records/cost-free
//
// 用户确认某些卖单本来就没有货品成本（代卖 / 寄卖 / 赠送），标记后不再进「未挂成本」，
// 利润按销售额全额计（成本只剩运费与服务费）。标记时把手填的单件成本清零，避免两个口径打架。
// 只处理独立卖单：已经挂了买进单、或在含买进单联单里的卖单成本来源明确，不需要也不允许标。
func handleCostFree(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	var in costFreeReq
	if !decodeJSONBody(w, r, &in, maxTinyJSONBody) {
		return
	}
	if len(in.IDs) == 0 {
		writeError(w, 400, "先勾选要标记的卖单")
		return
	}
	if len(in.IDs) > 200 {
		in.IDs = in.IDs[:200]
	}
	sid := shopID(r)

	// 先整体校验再落库：中途遇到已完结联单不会留下改了一半的数据
	// 所在联单已有买进单的卖单，成本由联单核算，不允许再标无成本。
	groupedWithBuy := loadGroupedWithBuy(sid)
	ids := []int64{}
	seen := map[int64]bool{}
	skipped := 0
	for _, id := range in.IDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		it, e := getItem(id, sid)
		if e != nil || !it.HasSell || it.ParentID > 0 || groupedWithBuy[id] {
			skipped++ // 不是独立卖单、或成本已由联单买单承担，跳过不报错（用户可能多选了）
			continue
		}
		if gid, gtitle := settledGroupOf(id, sid); gid > 0 {
			writeSettledBlock(w, gtitle, "标记无成本")
			return
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		writeError(w, 400, "选中的记录里没有可标记的独立卖单")
		return
	}

	ts := nowStr()
	updated := 0
	for _, id := range ids {
		if in.Free {
			res, e := db.Exec("UPDATE items SET cost_free=1, unit_cost=0, updated_at=? WHERE id=? AND shop_id=? AND parent_id=0", ts, id, sid)
			if e == nil {
				if n, _ := res.RowsAffected(); n > 0 {
					updated++
				}
			}
		} else {
			res, e := db.Exec("UPDATE items SET cost_free=0, updated_at=? WHERE id=? AND shop_id=?", ts, id, sid)
			if e == nil {
				if n, _ := res.RowsAffected(); n > 0 {
					updated++
				}
			}
		}
	}
	writeJSON(w, map[string]interface{}{"updated": updated, "skipped": skipped, "free": in.Free})
}

// ---------- 按售价估算成本：没有买进单的卖单，直接把成本填到卖单上 ----------

type estimateCostReq struct {
	Items []struct {
		SaleID   int64   `json:"sale_id"`
		UnitCost float64 `json:"unit_cost"`
	} `json:"items"`
}

// handleEstimateCost POST /api/records/estimate-cost
//
// 给「找不到买进单」的未挂成本卖单直接写单件成本（用户按售价比例估算后再确认）。
// 不新建买进单：这类单多半是代卖或没录进货，凭空造进货记录只会让账更乱。
// 只写 unit_cost，运单关联、完结、弃单状态一律不碰。
func handleEstimateCost(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	var in estimateCostReq
	if !decodeJSONBody(w, r, &in, 256<<10) {
		return
	}
	if len(in.Items) == 0 {
		writeError(w, 400, "没有要填成本的卖单")
		return
	}
	if len(in.Items) > 200 {
		in.Items = in.Items[:200]
	}
	sid := shopID(r)
	groupedWithBuy := loadGroupedWithBuy(sid)
	ts := nowStr()

	updated := 0
	skipped := 0
	for _, it := range in.Items {
		if it.SaleID <= 0 || it.UnitCost <= 0 {
			skipped++
			continue
		}
		if groupedWithBuy[it.SaleID] {
			skipped++ // 成本已经由联单买单核算，不重复填
			continue
		}
		if gid, gtitle := settledGroupOf(it.SaleID, sid); gid > 0 {
			writeSettledBlock(w, gtitle, "填成本")
			return
		}
		res, e := db.Exec(`UPDATE items SET unit_cost=?, cost_free=0, updated_at=?
            WHERE id=? AND shop_id=? AND parent_id=0 AND settled_at='' AND discarded_at=''`,
			round2(it.UnitCost), ts, it.SaleID, sid)
		if e != nil {
			skipped++
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			updated++
		} else {
			skipped++
		}
	}
	refreshPurchaseIndex()
	realtimeHub.publish("POST", "/api/records/estimate-cost", "", fmt.Sprintf("按估算补了 %d 条成本", updated), sid)
	writeJSON(w, map[string]interface{}{"updated": updated, "skipped": skipped})
}

// ---------- 智能挂成本：按商品名把未挂成本的卖单匹配到买进单 ----------

type autoLinkCand struct {
	PurchaseID int64   `json:"purchase_id"`
	Product    string  `json:"product"`
	BuyPrice   float64 `json:"buy_price"`
	BuyShip    float64 `json:"buy_shipping"`
	Quantity   int     `json:"quantity"`
	Used       int     `json:"used"`
	Left       int     `json:"left"`
	UnitCost   float64 `json:"unit_cost"`
	Match      string  `json:"match"` // exact=同名 / similar=名字相近
	Score      float64 `json:"score"`
}

type autoLinkRow struct {
	SaleID     int64          `json:"sale_id"`
	Product    string         `json:"product"`
	SalePrice  float64        `json:"sale_price"`
	SaleTime   string         `json:"sale_time"`
	Suggest    *autoLinkCand  `json:"suggest"`
	Candidates []autoLinkCand `json:"candidates"`
	Reason     string         `json:"reason"` // ""=可挂 / no_purchase=没有可配的买进单 / no_stock=同名但件数已用完
}

// handleAutoLinkPreview GET /api/records/auto-link/preview
//
// 给「未挂成本」的卖单找买进单：先按归一化商品名精确配，配不到再按名字相似度给候选。
// 只做建议、不写库；用户在弹窗里确认后走 /api/records/auto-link/apply。
func handleAutoLinkPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "方法不允许")
		return
	}
	sid := shopID(r)

	// 每个买进单已经挂出去多少件：同名的货可能分批卖出，件数用完就不能再配
	usedBy := map[int64]int{}
	// quantity 列允许是 0（录入默认值，展示时才补 1），求和时必须按 1 计，
	// 否则「已挂件数」永远算成 0，同一个买进单会被无限重复匹配。
	if uRows, e := db.Query(`SELECT parent_id, SUM(CASE WHEN quantity>0 THEN quantity ELSE 1 END)
        FROM items WHERE shop_id=? AND parent_id>0 GROUP BY parent_id`, sid); e == nil {
		for uRows.Next() {
			var pid int64
			var n int
			if uRows.Scan(&pid, &n) == nil {
				usedBy[pid] = n
			}
		}
		uRows.Close()
	}

	// 可配的买进单（在库、未完结、未弃单）
	type buy struct {
		it   Item
		left int
	}
	var buys []buy
	if brow, e := db.Query("SELECT " + itemCols + " FROM items WHERE " + purchaseWhere(sid) + " ORDER BY id DESC"); e == nil {
		for brow.Next() {
			it, err := scanItem(brow)
			if err != nil {
				continue
			}
			q := it.Quantity
			if q < 1 {
				q = 1
			}
			left := q - usedBy[it.ID]
			if left < 0 {
				left = 0
			}
			buys = append(buys, buy{it, left})
		}
		brow.Close()
	}

	groupedWithBuy := loadGroupedWithBuy(sid)
	rows := []autoLinkRow{}
	srows, err := db.Query("SELECT "+itemCols+" FROM items WHERE shop_id=? AND parent_id=0 AND unit_cost<=0 "+
		"AND COALESCE(cost_free,0)=0 AND settled_at='' AND discarded_at='' "+
		"AND (sell_price>0 OR sell_net>0 OR sell_order_no<>'') ORDER BY id DESC", sid)
	if err != nil {
		writeError(w, 500, "查询失败")
		return
	}
	defer srows.Close()
	for srows.Next() {
		it, e := scanItem(srows)
		if e != nil {
			continue
		}
		if groupedWithBuy[it.ID] {
			continue // 成本已经由联单买单核算
		}
		if len(rows) >= 200 {
			break
		}
		row := autoLinkRow{
			SaleID: it.ID, Product: it.Product, SalePrice: it.SellPrice, SaleTime: it.SellTime,
			Candidates: []autoLinkCand{},
		}
		nq := itemProductNorm(it)
		var exact, similar []autoLinkCand
		for _, b := range buys {
			if b.left <= 0 {
				continue
			}
			np := itemProductNorm(b.it)
			if np == "" || nq == "" {
				continue
			}
			q := b.it.Quantity
			if q < 1 {
				q = 1
			}
			c := autoLinkCand{
				PurchaseID: b.it.ID, Product: b.it.Product,
				BuyPrice: b.it.BuyPrice, BuyShip: b.it.BuyShipping,
				Quantity: q, Used: usedBy[b.it.ID], Left: b.left,
				UnitCost: round2((b.it.BuyPrice + b.it.BuyShipping) / float64(q)),
			}
			if np == nq {
				c.Match = "exact"
				c.Score = 1
				exact = append(exact, c)
			} else {
				sc := similarity(nq, np)
				// 0.8 而不是 0.6：归一化之后还剩这么大差异的，多半是不同东西
				// （「U7 Pro Max 主机」对「U7 Pro Max 金属支架」相似度也有 0.7+），
				// 宁可少给建议，也别让人误挂一笔成本。
				if sc >= 0.8 {
					c.Match = "similar"
					c.Score = round2(sc*100) / 100
					similar = append(similar, c)
				}
			}
		}
		switch {
		case len(exact) > 0:
			row.Candidates = exact
			s := exact[0]
			row.Suggest = &s
			if len(exact) > 1 {
				row.Reason = "ambiguous"
			}
		case len(similar) > 0:
			if len(similar) > 3 {
				similar = similar[:3]
			}
			row.Candidates = similar
			s := similar[0]
			row.Suggest = &s
			row.Reason = "ambiguous"
		default:
			// 同名买进单存在但件数已经用完的，单独说明，避免用户以为没录过这批货
			hasSameNoStock := false
			for _, b := range buys {
				if itemProductNorm(b.it) == nq && b.left <= 0 {
					hasSameNoStock = true
					break
				}
			}
			if hasSameNoStock {
				row.Reason = "no_stock"
			} else {
				row.Reason = "no_purchase"
			}
		}
		rows = append(rows, row)
	}

	matched, ambiguous, unmatched := 0, 0, 0
	for _, row := range rows {
		switch row.Reason {
		case "":
			matched++
		case "ambiguous":
			ambiguous++
		default:
			unmatched++
		}
	}
	writeJSON(w, map[string]interface{}{
		"items": rows, "total": len(rows),
		"matched": matched, "ambiguous": ambiguous, "unmatched": unmatched,
	})
}

type autoLinkReq struct {
	Links []struct {
		SaleID     int64 `json:"sale_id"`
		PurchaseID int64 `json:"purchase_id"`
	} `json:"links"`
}

// handleAutoLinkApply POST /api/records/auto-link/apply
//
// 按用户确认的配对批量挂成本：单件成本 =（买价 + 买运费）/ 件数，挂完并入同一张联单。
// 中途遇到已完结联单整批拒绝，不会改一半。
func handleAutoLinkApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	var in autoLinkReq
	if !decodeJSONBody(w, r, &in, 256<<10) {
		return
	}
	if len(in.Links) == 0 {
		writeError(w, 400, "没有要挂的配对")
		return
	}
	if len(in.Links) > 200 {
		in.Links = in.Links[:200]
	}
	sid := shopID(r)

	type job struct {
		sale   Item
		parent Item
		unit   float64
	}
	var jobs []job
	seen := map[int64]bool{}
	for _, l := range in.Links {
		if l.SaleID <= 0 || l.PurchaseID <= 0 || seen[l.SaleID] {
			continue
		}
		seen[l.SaleID] = true
		sale, e := getItem(l.SaleID, sid)
		if e != nil || !sale.HasSell || sale.ParentID > 0 {
			continue
		}
		if gid, gtitle := settledGroupOf(l.SaleID, sid); gid > 0 {
			writeSettledBlock(w, gtitle, "挂成本")
			return
		}
		parent, e := getItem(l.PurchaseID, sid)
		if e != nil || parent.ParentID > 0 || parent.SettledAt != "" {
			continue
		}
		q := parent.Quantity
		if q < 1 {
			q = 1
		}
		jobs = append(jobs, job{sale: sale, parent: parent, unit: round2((parent.BuyPrice + parent.BuyShipping) / float64(q))})
	}
	if len(jobs) == 0 {
		writeError(w, 400, "选中的配对都不可用（可能已经挂过成本或已完结）")
		return
	}

	ts := nowStr()
	linked := 0
	var lastGroup int64
	for _, j := range jobs {
		res, e := db.Exec("UPDATE items SET parent_id=?, unit_cost=?, cost_free=0, updated_at=? WHERE id=? AND shop_id=? AND parent_id=0",
			j.parent.ID, j.unit, ts, j.sale.ID, sid)
		if e != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			linked++
			if g := autoGroupForSale(j.sale.ID, j.parent.ID, j.sale.Product, sid); g > 0 {
				lastGroup = g
			}
		}
	}
	refreshPurchaseIndex()
	realtimeHub.publish("POST", "/api/records/auto-link/apply", "", fmt.Sprintf("智能挂成本 %d 条", linked), sid)
	writeJSON(w, map[string]interface{}{"linked": linked, "group_id": lastGroup, "requested": len(in.Links)})
}

// ---------- 批量挂成本：把一批没有成本单的卖单挂到同一个买进单 ----------

type linkPurchasesReq struct {
	SaleIDs    []int64 `json:"sale_ids"`
	PurchaseID int64   `json:"purchase_id"`
}

// handleLinkPurchases POST /api/records/link-purchases
// 给「未挂成本」的卖单批量补上成本：按买进单的件数把（买价+买运费）分摊成单件成本。
// 挂完顺手并入同一张联单，这样「完结单净利润」也能算到这批货。
func handleLinkPurchases(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	var in linkPurchasesReq
	if !decodeJSONBody(w, r, &in, maxTinyJSONBody) {
		return
	}
	if in.PurchaseID <= 0 || len(in.SaleIDs) == 0 {
		writeError(w, 400, "请先选一个买进单和要挂的卖单")
		return
	}
	if len(in.SaleIDs) > 200 {
		in.SaleIDs = in.SaleIDs[:200]
	}
	sid := shopID(r)
	parent, err := getItem(in.PurchaseID, sid)
	if err != nil || parent.ParentID > 0 {
		writeError(w, 400, "买进单不存在或不是买进单")
		return
	}
	qty := parent.Quantity
	if qty < 1 {
		qty = 1
	}
	unit := round2((parent.BuyPrice + parent.BuyShipping) / float64(qty))

	// 先整体校验，再统一落库：中途遇到已完结联单不会留下改了一半的数据。
	type target struct {
		id      int64
		product string
	}
	var targets []target
	seen := map[int64]bool{}
	for _, id := range in.SaleIDs {
		if id <= 0 || id == in.PurchaseID || seen[id] {
			continue
		}
		seen[id] = true
		it, e := getItem(id, sid)
		if e != nil || !it.HasSell || it.HasBuy {
			continue // 不是卖单就跳过，不报错（用户可能多选了）
		}
		if gid, gtitle := settledGroupOf(id, sid); gid > 0 {
			writeSettledBlock(w, gtitle, "挂成本")
			return
		}
		targets = append(targets, target{id, it.Product})
	}
	if len(targets) == 0 {
		writeError(w, 400, "选中的记录里没有可挂成本的卖单")
		return
	}

	ts := nowStr()
	linked := 0
	var gid int64
	for _, t := range targets {
		if _, e := db.Exec("UPDATE items SET parent_id=?, unit_cost=?, updated_at=? WHERE id=? AND shop_id=?",
			in.PurchaseID, unit, ts, t.id, sid); e != nil {
			continue
		}
		linked++
		if g := autoGroupForSale(t.id, in.PurchaseID, t.product, sid); g > 0 {
			gid = g
		}
	}
	refreshPurchaseIndex()
	writeJSON(w, map[string]interface{}{"linked": linked, "group_id": gid})
}
