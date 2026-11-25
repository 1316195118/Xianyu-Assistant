package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 经营中心：数据体检、物流费用、服务费核销、归属人对账单、采购批次、
// 智能定价与多店驾驶舱。所有金额都基于现有 items / link_groups 派生，
// 不复制利润算法；只有物流账单、核销确认和批次备注是独立经营元数据。

func createOperationsTables() error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS transport_charges (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    shop_id       INTEGER NOT NULL DEFAULT 1,
    order_no      TEXT NOT NULL DEFAULT '',
    tracking_no   TEXT NOT NULL DEFAULT '',
    carrier       TEXT NOT NULL DEFAULT '',
    amount        REAL NOT NULL DEFAULT 0,
    occurred_at   TEXT NOT NULL DEFAULT '',
    source        TEXT NOT NULL DEFAULT 'CSV',
    match_item_id INTEGER NOT NULL DEFAULT 0,
    match_kind    TEXT NOT NULL DEFAULT 'pending',
    status        TEXT NOT NULL DEFAULT 'pending',
    note          TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_transport_fingerprint
    ON transport_charges(shop_id, order_no, tracking_no, occurred_at, amount);
CREATE INDEX IF NOT EXISTS idx_transport_shop_status
    ON transport_charges(shop_id, status, id DESC);

CREATE TABLE IF NOT EXISTS fee_reconciliations (
    shop_id       INTEGER NOT NULL DEFAULT 1,
    item_id       INTEGER NOT NULL,
    note          TEXT NOT NULL DEFAULT '',
    reconciled_at TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    PRIMARY KEY (shop_id, item_id)
);

CREATE TABLE IF NOT EXISTS purchase_lot_meta (
    shop_id      INTEGER NOT NULL DEFAULT 1,
    item_id      INTEGER NOT NULL,
    batch_no     TEXT NOT NULL DEFAULT '',
    supplier     TEXT NOT NULL DEFAULT '',
    note         TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (shop_id, item_id)
);
`)
	return err
}

func loadShopItems(sid int64) ([]Item, error) {
	rows, err := db.Query("SELECT "+itemCols+" FROM items WHERE shop_id=? ORDER BY id", sid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		it, scanErr := scanItem(rows)
		if scanErr == nil {
			out = append(out, it)
		}
	}
	return out, rows.Err()
}

func itemDisplayName(it Item) string {
	name := strings.TrimSpace(it.Product)
	if name == "" {
		return "未命名商品"
	}
	return name
}

func itemRef(it Item, amount float64) opsIssueItem {
	orderNo := it.SellOrderNo
	if orderNo == "" {
		orderNo = it.BuyOrderNo
	}
	return opsIssueItem{
		ID: it.ID, Product: itemDisplayName(it), OrderNo: orderNo,
		Amount: round2(amount), Time: it.DealTime,
	}
}

type opsIssueItem struct {
	ID      int64   `json:"id"`
	Product string  `json:"product"`
	OrderNo string  `json:"order_no"`
	Amount  float64 `json:"amount"`
	Time    string  `json:"time"`
}

type opsIssue struct {
	Key         string         `json:"key"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Severity    string         `json:"severity"` // danger | warn | info | good
	Count       int            `json:"count"`
	Amount      float64        `json:"amount"`
	Action      string         `json:"action"` // records | consign | operations
	Filter      string         `json:"filter"`
	Items       []opsIssueItem `json:"items"`
}

type opsHealth struct {
	Score        int        `json:"score"`
	IssueCount   int        `json:"issue_count"`
	Affected     int        `json:"affected"`
	Impact       float64    `json:"impact"`
	Issues       []opsIssue `json:"issues"`
	CheckedCount int        `json:"checked_count"`
	GeneratedAt  string     `json:"generated_at"`
}

func opsContainsID(ids []int64, id int64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

func opsUniqueIDs(ids []int64, limit int) []int64 {
	out := []int64{}
	seen := map[int64]bool{}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func opsMedian(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	cp := append([]float64(nil), values...)
	sort.Float64s(cp)
	mid := len(cp) / 2
	if len(cp)%2 == 1 {
		return cp[mid]
	}
	return (cp[mid-1] + cp[mid]) / 2
}

func handleOperationsHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}
	health, err := buildOperationsHealth(shopID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取经营数据失败")
		return
	}
	saveQualitySnapshot(shopID(r), health)
	writeJSON(w, health)
}

func buildOperationsHealth(sid int64) (opsHealth, error) {
	items, err := loadShopItems(sid)
	if err != nil {
		return opsHealth{}, err
	}
	groupedWithBuy := loadGroupedWithBuy(sid)
	alloc := loadGroupAlloc(sid)

	groupItems := map[int64]bool{}
	if rows, qerr := db.Query(`SELECT gi.item_id FROM group_items gi
        JOIN link_groups g ON g.id=gi.group_id WHERE g.shop_id=?`, sid); qerr == nil {
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				groupItems[id] = true
			}
		}
		rows.Close()
	}

	pricesByNorm := map[string][]float64{}
	for _, it := range items {
		if it.Kind == "discarded" || it.SellPrice <= 0 || it.ProductNorm == "" {
			continue
		}
		pricesByNorm[it.ProductNorm] = append(pricesByNorm[it.ProductNorm], it.SellPrice)
	}

	issues := []opsIssue{}
	addIssue := func(issue opsIssue, refs []opsIssueItem) {
		if issue.Count == 0 {
			return
		}
		sort.SliceStable(refs, func(i, j int) bool {
			if refs[i].Amount == refs[j].Amount {
				return refs[i].Time > refs[j].Time
			}
			return refs[i].Amount > refs[j].Amount
		})
		if len(refs) > 30 {
			refs = refs[:30]
		}
		issue.Items = refs
		issue.Amount = round2(issue.Amount)
		issues = append(issues, issue)
	}

	var unlinkedRefs []opsIssueItem
	var unlinkedAmount float64
	var feeRefs []opsIssueItem
	var feeAmount float64
	var ownerRefs []opsIssueItem
	var ownerAmount float64
	var priceRefs []opsIssueItem
	var priceAmount float64
	var badRefs []opsIssueItem
	var badAmount float64
	var ungroupedRefs []opsIssueItem
	var ungroupedAmount float64
	seenOrders := map[string][]Item{}

	for _, it := range items {
		if it.Kind == "discarded" {
			continue
		}
		if it.SellOrderNo != "" {
			seenOrders[it.SellOrderNo] = append(seenOrders[it.SellOrderNo], it)
		}
		if it.SellPrice > 0 {
			actualFee := it.SellFee + it.SellWuyouFee
			if actualFee <= 0.005 {
				feeRefs = append(feeRefs, itemRef(it, it.SellPrice))
				feeAmount += it.SellPrice
			}
		}
		if isExternalGoods(it.Source) && strings.TrimSpace(it.Owner) == "" {
			ownerRefs = append(ownerRefs, itemRef(it, it.SellPrice+it.BuyPrice))
			ownerAmount += it.SellPrice + it.BuyPrice
		}
		if it.Kind == "sale" && it.ParentID == 0 && !it.CostFree &&
			it.SettledAt == "" && it.UnitCost <= 0 && !groupedWithBuy[it.ID] {
			if _, hasAlloc := alloc[it.ID]; !hasAlloc {
				unlinkedRefs = append(unlinkedRefs, itemRef(it, it.SellPrice))
				unlinkedAmount += it.SellPrice
			}
		}
		// 已经填写过成本（单件成本/买价）或明确标记无成本的卖单，不再提示必须关联联单。
		if it.Kind == "sale" && it.ParentID == 0 && !groupItems[it.ID] && it.SellPrice > 0 &&
			it.UnitCost <= 0 && it.BuyPrice <= 0 && !it.CostFree {
			ungroupedRefs = append(ungroupedRefs, itemRef(it, it.SellPrice))
			ungroupedAmount += it.SellPrice
		}
		if it.SellPrice > 0 && it.ProductNorm != "" {
			history := pricesByNorm[it.ProductNorm]
			if len(history) >= 3 {
				median := opsMedian(history)
				if median > 0 && (it.SellPrice >= median*3 || it.SellPrice <= median/3) {
					diff := math.Abs(it.SellPrice - median)
					priceRefs = append(priceRefs, itemRef(it, diff))
					priceAmount += diff
				}
			}
		}
		if it.SellPrice < 0 || it.BuyPrice < 0 || it.SellFee < 0 || it.SellWuyouFee < 0 ||
			it.SellShipping < 0 || (it.SellPrice > 0 && it.SellNet > it.SellPrice+0.01) {
			badRefs = append(badRefs, itemRef(it, math.Abs(it.SellNet-it.SellPrice)))
			badAmount += math.Abs(it.SellNet - it.SellPrice)
		}
	}

	addIssue(opsIssue{
		Key: "unlinked_cost", Title: "卖单未挂成本", Severity: "danger",
		Description: "成本按 0 计会让利润虚高，建议挂买进单或标记无成本。",
		Count:       len(unlinkedRefs), Amount: unlinkedAmount, Action: "records", Filter: "unlinked",
	}, unlinkedRefs)
	addIssue(opsIssue{
		Key: "fee_pending", Title: "服务费待补账单", Severity: "warn",
		Description: "成交单尚未匹配软件服务费和无忧卖服务费，可导入支付宝账单核销。",
		Count:       len(feeRefs), Amount: feeAmount, Action: "operations", Filter: "fees",
	}, feeRefs)
	addIssue(opsIssue{
		Key: "owner_missing", Title: "寄卖/代采归属缺失", Severity: "warn",
		Description: "这些单据会落到「未指定归属」，结算和对账单无法准确归档。",
		Count:       len(ownerRefs), Amount: ownerAmount, Action: "consign", Filter: "owner",
	}, ownerRefs)
	addIssue(opsIssue{
		Key: "price_outlier", Title: "成交价明显偏离历史", Severity: "info",
		Description: "同一商品成交价低于或高于历史中位数的 3 倍，建议核对是否识别错价格。",
		Count:       len(priceRefs), Amount: priceAmount, Action: "records", Filter: "",
	}, priceRefs)
	addIssue(opsIssue{
		Key: "invalid_amount", Title: "金额关系异常", Severity: "danger",
		Description: "发现负金额或到手价高于成交价，可能是导入或手工录入错误。",
		Count:       len(badRefs), Amount: badAmount, Action: "records", Filter: "",
	}, badRefs)
	addIssue(opsIssue{
		Key: "ungrouped_sale", Title: "卖单未关联联单", Severity: "info",
		Description: "只在卖单尚未填写成本时提示；已经填写单件成本、买价或标记无成本的订单不会计入。",
		Count:       len(ungroupedRefs), Amount: ungroupedAmount, Action: "records", Filter: "",
	}, ungroupedRefs)

	var duplicateRefs []opsIssueItem
	var duplicateAmount float64
	dupCount := 0
	for no, list := range seenOrders {
		if len(list) < 2 {
			continue
		}
		dupCount += len(list)
		for _, it := range list {
			duplicateRefs = append(duplicateRefs, itemRef(it, it.SellPrice))
			duplicateAmount += it.SellPrice
		}
		_ = no
	}
	addIssue(opsIssue{
		Key: "duplicate_order", Title: "重复订单号", Severity: "danger",
		Description: "同一店铺存在重复卖单号，可能重复计入成交额和利润。",
		Count:       dupCount, Amount: duplicateAmount, Action: "records", Filter: "",
	}, duplicateRefs)

	sort.SliceStable(issues, func(i, j int) bool {
		rank := map[string]int{"danger": 4, "warn": 3, "info": 2, "good": 1}
		if rank[issues[i].Severity] == rank[issues[j].Severity] {
			return issues[i].Count > issues[j].Count
		}
		return rank[issues[i].Severity] > rank[issues[j].Severity]
	})

	score := 100
	affected := 0
	impact := 0.0
	for _, issue := range issues {
		switch issue.Severity {
		case "danger":
			score -= 18
		case "warn":
			score -= 9
		default:
			score -= 3
		}
		affected += issue.Count
		impact += issue.Amount
	}
	if score < 0 {
		score = 0
	}
	return opsHealth{
		Score: score, IssueCount: len(issues), Affected: affected,
		Impact: round2(impact), Issues: issues, CheckedCount: len(items),
		GeneratedAt: nowStr(),
	}, nil
}

type opsTransportRow struct {
	ID          int64   `json:"id"`
	OrderNo     string  `json:"order_no"`
	TrackingNo  string  `json:"tracking_no"`
	Carrier     string  `json:"carrier"`
	Amount      float64 `json:"amount"`
	OccurredAt  string  `json:"occurred_at"`
	Source      string  `json:"source"`
	MatchItemID int64   `json:"match_item_id"`
	MatchKind   string  `json:"match_kind"`
	Status      string  `json:"status"`
	Note        string  `json:"note"`
	Product     string  `json:"product"`
}

func handleOperationsTransport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}
	sid := shopID(r)
	rows, err := db.Query(`SELECT t.id,t.order_no,t.tracking_no,t.carrier,t.amount,t.occurred_at,
        t.source,t.match_item_id,t.match_kind,t.status,t.note,COALESCE(i.product,'')
        FROM transport_charges t LEFT JOIN items i
        ON i.id=t.match_item_id AND i.shop_id=t.shop_id
        WHERE t.shop_id=? ORDER BY t.occurred_at DESC,t.id DESC LIMIT 500`, sid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取物流账单失败")
		return
	}
	list := []opsTransportRow{}
	counts := map[string]int{}
	var total, matchedAmount, pendingAmount float64
	for rows.Next() {
		var v opsTransportRow
		if rows.Scan(&v.ID, &v.OrderNo, &v.TrackingNo, &v.Carrier, &v.Amount,
			&v.OccurredAt, &v.Source, &v.MatchItemID, &v.MatchKind, &v.Status,
			&v.Note, &v.Product) != nil {
			continue
		}
		v.Amount = round2(v.Amount)
		list = append(list, v)
		counts[v.Status]++
		total += v.Amount
		if v.Status == "pending" {
			pendingAmount += v.Amount
		} else {
			matchedAmount += v.Amount
		}
	}
	rows.Close()

	pendingSales := []opsIssueItem{}
	if sr, qerr := db.Query(`SELECT id,product,sell_order_no,sell_price,COALESCE(NULLIF(sell_time,''),consign_time)
        FROM items WHERE shop_id=? AND sell_price>0 AND sell_shipping<=0.1
        AND discarded_at='' ORDER BY COALESCE(NULLIF(sell_time,''),consign_time) DESC,id DESC LIMIT 80`, sid); qerr == nil {
		for sr.Next() {
			var v opsIssueItem
			var orderNo, tm string
			if sr.Scan(&v.ID, &v.Product, &orderNo, &v.Amount, &tm) == nil {
				v.OrderNo, v.Time, v.Amount = orderNo, tm, round2(v.Amount)
				pendingSales = append(pendingSales, v)
			}
		}
		sr.Close()
	}
	writeJSON(w, map[string]interface{}{
		"items": list,
		"summary": map[string]interface{}{
			"total": round2(total), "matched": counts["matched"], "estimated": counts["estimated"],
			"pending": counts["pending"], "matched_amount": round2(matchedAmount),
			"pending_amount": round2(pendingAmount),
		},
		"pending_sales": pendingSales,
	})
}

type opsTransportInput struct {
	OrderNo    string  `json:"order_no"`
	TrackingNo string  `json:"tracking_no"`
	Carrier    string  `json:"carrier"`
	Amount     float64 `json:"amount"`
	OccurredAt string  `json:"occurred_at"`
	Note       string  `json:"note"`
}

type opsTransportImportReq struct {
	Rows   []opsTransportInput `json:"rows"`
	Source string              `json:"source"`
	Apply  bool                `json:"apply"`
	Force  bool                `json:"force"`
}

func opsParseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	s = strings.ReplaceAll(s, "T", " ")
	for _, layout := range []string{
		"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02",
		"2006/01/02 15:04:05", "2006/01/02 15:04", "2006/01/02",
	} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t
		}
	}
	return time.Time{}
}

func opsBestTimeMatch(items []Item, when time.Time) (Item, string) {
	if when.IsZero() {
		return Item{}, ""
	}
	best := Item{}
	bestGap := time.Duration(1<<62 - 1)
	kind := ""
	for _, it := range items {
		if it.Kind != "sale" || it.Kind == "discarded" || it.SellPrice <= 0 {
			continue
		}
		if ct := opsParseTime(it.ConsignTime); !ct.IsZero() {
			gap := when.Sub(ct)
			if gap < 0 {
				gap = -gap
			}
			if gap <= 4*time.Minute && gap < bestGap {
				best, bestGap, kind = it, gap, "consign"
			}
		}
	}
	if kind != "" {
		return best, kind
	}
	for _, it := range items {
		if it.Kind != "sale" || it.SellPrice <= 0 {
			continue
		}
		st := opsParseTime(it.SellTime)
		if st.IsZero() {
			continue
		}
		gap := when.Sub(st)
		if gap < 0 || gap > 10*24*time.Hour {
			continue
		}
		if gap < bestGap {
			best, bestGap = it, gap
		}
	}
	if best.ID > 0 {
		return best, "estimated"
	}
	return Item{}, ""
}

func handleOperationsTransportImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}
	var in opsTransportImportReq
	if !decodeJSONBody(w, r, &in, 2<<20) {
		return
	}
	if len(in.Rows) == 0 {
		writeError(w, http.StatusBadRequest, "没有可导入的运费记录")
		return
	}
	if len(in.Rows) > 500 {
		in.Rows = in.Rows[:500]
	}
	sid := shopID(r)
	items, err := loadShopItems(sid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取订单失败")
		return
	}
	byOrder := map[string]Item{}
	for _, it := range items {
		if it.SellOrderNo != "" {
			byOrder[strings.TrimSpace(it.SellOrderNo)] = it
		}
	}
	source := strings.TrimSpace(in.Source)
	if source == "" {
		source = "CSV"
	}
	imported, matched, estimated, pending, applied := 0, 0, 0, 0, 0
	var appliedAmount float64
	ts := nowStr()
	for _, row := range in.Rows {
		row.OrderNo = strings.TrimSpace(row.OrderNo)
		row.TrackingNo = strings.TrimSpace(row.TrackingNo)
		row.Carrier = strings.TrimSpace(row.Carrier)
		row.OccurredAt = strings.TrimSpace(row.OccurredAt)
		row.Note = strings.TrimSpace(row.Note)
		if row.Amount <= 0 {
			continue
		}
		it := Item{}
		kind := ""
		if row.OrderNo != "" {
			if found, ok := byOrder[row.OrderNo]; ok {
				it, kind = found, "order"
			}
		}
		if it.ID == 0 && row.TrackingNo != "" {
			var matchedID int64
			if db.QueryRow(`SELECT match_item_id FROM transport_charges
                WHERE shop_id=? AND tracking_no=? AND match_item_id>0 LIMIT 1`,
				sid, row.TrackingNo).Scan(&matchedID) == nil && matchedID > 0 {
				for _, candidate := range items {
					if candidate.ID == matchedID {
						it, kind = candidate, "tracking"
						break
					}
				}
			}
		}
		if it.ID == 0 {
			it, kind = opsBestTimeMatch(items, opsParseTime(row.OccurredAt))
		}
		status := "pending"
		if it.ID > 0 {
			if kind == "estimated" {
				status = "estimated"
				estimated++
			} else {
				status = "matched"
				matched++
			}
		} else {
			pending++
		}
		res, execErr := db.Exec(`INSERT OR IGNORE INTO transport_charges
            (shop_id,order_no,tracking_no,carrier,amount,occurred_at,source,match_item_id,
             match_kind,status,note,created_at,updated_at)
            VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			sid, row.OrderNo, row.TrackingNo, row.Carrier, round2(row.Amount), row.OccurredAt,
			source, it.ID, kind, status, row.Note, ts, ts)
		if execErr != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			imported++
		}
		if !in.Apply || it.ID == 0 {
			continue
		}
		if it.DiscardedAt != "" {
			continue
		}
		if !in.Force {
			if it.SettledAt != "" {
				continue
			}
			if gid, _ := settledGroupOf(it.ID, sid); gid > 0 {
				continue
			}
			if kind == "estimated" && it.SellShipping > 0.1 {
				continue
			}
		}
		shipping := round2(row.Amount)
		net := round2(it.SellPrice - it.SellFee - it.SellWuyouFee - shipping)
		if _, err := db.Exec(`UPDATE items SET sell_shipping=?,sell_net=?,
            sell_ship_tag=CASE WHEN sell_ship_tag='自提' THEN sell_ship_tag ELSE '卖家承担' END,
            updated_at=? WHERE id=? AND shop_id=?`,
			shipping, net, ts, it.ID, sid); err == nil {
			applied++
			appliedAmount += shipping
		}
	}
	if applied > 0 {
		refreshPurchaseIndex()
		realtimeHub.publish("POST", "/api/operations/transport/import", "",
			fmt.Sprintf("导入物流费用并补填 %d 笔运费", applied), sid)
	}
	writeJSON(w, map[string]interface{}{
		"ok": true, "imported": imported, "matched": matched, "estimated": estimated,
		"pending": pending, "applied": applied, "applied_amount": round2(appliedAmount),
	})
}

type opsFeeRow struct {
	ItemID       int64   `json:"item_id"`
	Product      string  `json:"product"`
	OrderNo      string  `json:"order_no"`
	Time         string  `json:"time"`
	Price        float64 `json:"price"`
	SoftwareFee  float64 `json:"software_fee"`
	WuyouFee     float64 `json:"wuyou_fee"`
	ActualFee    float64 `json:"actual_fee"`
	ExpectedFee  float64 `json:"expected_fee"`
	Difference   float64 `json:"difference"`
	ActualRate   float64 `json:"actual_rate"`
	ExpectedRate float64 `json:"expected_rate"`
	Status       string  `json:"status"`
	Acknowledged bool    `json:"acknowledged"`
	Note         string  `json:"note"`
	ReconciledAt string  `json:"reconciled_at"`
}

func opsFeeRates(items []Item) map[string]float64 {
	byNorm := map[string][]float64{}
	for _, it := range items {
		if it.Kind == "discarded" || it.SellPrice <= 0 {
			continue
		}
		actual := it.SellFee + it.SellWuyouFee
		if actual <= 0 {
			continue
		}
		key := it.ProductNorm
		if key == "" {
			key = normProduct(it.Product)
		}
		if key == "" {
			continue
		}
		byNorm[key] = append(byNorm[key], actual/it.SellPrice)
	}
	out := map[string]float64{}
	for key, rates := range byNorm {
		// 同一商品的历史费率取中位数，避免一次异常账单把预计值带偏。
		out[key] = opsMedian(rates)
	}
	return out
}

func handleOperationsFees(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}
	sid := shopID(r)
	items, err := loadShopItems(sid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取费用数据失败")
		return
	}
	rates := opsFeeRates(items)
	recon := map[int64]struct {
		note string
		at   string
	}{}
	if rows, qerr := db.Query(`SELECT item_id,note,reconciled_at FROM fee_reconciliations WHERE shop_id=?`, sid); qerr == nil {
		for rows.Next() {
			var id int64
			var note, at string
			if rows.Scan(&id, &note, &at) == nil {
				recon[id] = struct {
					note string
					at   string
				}{note: note, at: at}
			}
		}
		rows.Close()
	}
	rowsOut := []opsFeeRow{}
	counts := map[string]int{}
	var actualSum, expectedSum, differenceSum float64
	for _, it := range items {
		if it.Kind == "discarded" || it.SellPrice <= 0 {
			continue
		}
		key := it.ProductNorm
		if key == "" {
			key = normProduct(it.Product)
		}
		rate, ok := rates[key]
		if !ok || rate < 0 || rate > 0.2 {
			rate = 0.026 // 软件 + 基础软件默认 2.6%，无忧卖有账单时再用实际值覆盖
		}
		actual := round2(it.SellFee + it.SellWuyouFee)
		expected := round2(it.SellPrice * rate)
		if expected <= 0 {
			expected = round2(it.SellPrice * 0.026)
		}
		diff := round2(actual - expected)
		reconRow := recon[it.ID]
		status := "pending_fix"
		if actual <= 0.005 {
			status = "pending_bill"
		} else if math.Abs(diff) > math.Max(1, expected*0.02) {
			status = "discrepancy"
		}
		if reconRow.at != "" {
			status = "reconciled"
		}
		actualRate := 0.0
		if it.SellPrice > 0 {
			actualRate = round2(actual / it.SellPrice * 100)
		}
		row := opsFeeRow{
			ItemID: it.ID, Product: itemDisplayName(it), OrderNo: it.SellOrderNo,
			Time: it.SellTime, Price: round2(it.SellPrice),
			SoftwareFee: round2(it.SellFee), WuyouFee: round2(it.SellWuyouFee),
			ActualFee: actual, ExpectedFee: expected, Difference: diff,
			ActualRate: actualRate, ExpectedRate: round2(rate * 100),
			Status: status, Acknowledged: reconRow.at != "",
			Note: reconRow.note, ReconciledAt: reconRow.at,
		}
		rowsOut = append(rowsOut, row)
		counts[status]++
		actualSum += actual
		expectedSum += expected
		differenceSum += diff
	}
	sort.SliceStable(rowsOut, func(i, j int) bool {
		rank := map[string]int{"discrepancy": 4, "pending_bill": 3, "pending_fix": 2, "reconciled": 1}
		if rank[rowsOut[i].Status] == rank[rowsOut[j].Status] {
			return rowsOut[i].Time > rowsOut[j].Time
		}
		return rank[rowsOut[i].Status] > rank[rowsOut[j].Status]
	})
	writeJSON(w, map[string]interface{}{
		"items": rowsOut,
		"summary": map[string]interface{}{
			"pending_bill": counts["pending_bill"], "pending_fix": counts["pending_fix"],
			"discrepancy": counts["discrepancy"], "reconciled": counts["reconciled"],
			"actual": round2(actualSum), "expected": round2(expectedSum),
			"difference": round2(differenceSum),
		},
	})
}

type opsFeeReconcileReq struct {
	ItemIDs     []int64  `json:"item_ids"`
	ItemID      int64    `json:"item_id"`
	Note        string   `json:"note"`
	ActualFee   *float64 `json:"actual_fee"`
	WuyouFee    *float64 `json:"wuyou_fee"`
	Force       bool     `json:"force"`
	Acknowledge bool     `json:"acknowledge"`
}

func handleOperationsFeeReconcile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}
	var in opsFeeReconcileReq
	if !decodeJSONBody(w, r, &in, 256<<10) {
		return
	}
	ids := append([]int64(nil), in.ItemIDs...)
	if in.ItemID > 0 {
		ids = append(ids, in.ItemID)
	}
	ids = opsUniqueIDs(ids, 200)
	if len(ids) == 0 {
		writeError(w, http.StatusBadRequest, "请选择要核销的费用记录")
		return
	}
	sid := shopID(r)
	ts := nowStr()
	tx, err := db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "开启核销事务失败")
		return
	}
	defer tx.Rollback()
	updated, corrected := 0, 0
	for _, id := range ids {
		var product string
		var price, software, wuyou, shipping, net float64
		var settled, discarded string
		if err := tx.QueryRow(`SELECT product,sell_price,sell_fee,sell_wuyou_fee,sell_shipping,sell_net,
            COALESCE(settled_at,''),COALESCE(discarded_at,'') FROM items WHERE id=? AND shop_id=?`,
			id, sid).Scan(&product, &price, &software, &wuyou, &shipping, &net, &settled, &discarded); err != nil {
			continue
		}
		if discarded != "" {
			continue
		}
		if !in.Force && settled != "" {
			continue
		}
		if !in.Force {
			var gid int64
			tx.QueryRow(`SELECT g.id FROM group_items gi JOIN link_groups g ON g.id=gi.group_id
                WHERE gi.item_id=? AND g.shop_id=? AND g.settled_at<>'' LIMIT 1`, id, sid).Scan(&gid)
			if gid > 0 {
				continue
			}
		}
		if in.ActualFee != nil || in.WuyouFee != nil {
			if in.ActualFee != nil {
				software = round2(*in.ActualFee)
			}
			if in.WuyouFee != nil {
				wuyou = round2(*in.WuyouFee)
			}
			if software < 0 || wuyou < 0 {
				writeError(w, http.StatusBadRequest, "费用不能为负数")
				return
			}
			net = round2(price - software - wuyou - shipping)
			if _, err := tx.Exec(`UPDATE items SET sell_fee=?,sell_wuyou_fee=?,sell_net=?,
                updated_at=? WHERE id=? AND shop_id=?`, software, wuyou, net, ts, id, sid); err != nil {
				writeError(w, http.StatusInternalServerError, "保存实际费用失败")
				return
			}
			corrected++
		}
		if in.Acknowledge || in.ActualFee != nil || in.WuyouFee != nil {
			if _, err := tx.Exec(`INSERT INTO fee_reconciliations
                (shop_id,item_id,note,reconciled_at,created_at,updated_at)
                VALUES(?,?,?,?,?,?)
                ON CONFLICT(shop_id,item_id) DO UPDATE SET
                note=excluded.note,reconciled_at=excluded.reconciled_at,updated_at=excluded.updated_at`,
				sid, id, strings.TrimSpace(in.Note), ts, ts, ts); err != nil {
				writeError(w, http.StatusInternalServerError, "保存核销状态失败")
				return
			}
			updated++
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "提交核销失败")
		return
	}
	if corrected > 0 {
		refreshPurchaseIndex()
	}
	realtimeHub.publish("POST", "/api/operations/fees/reconcile", "",
		fmt.Sprintf("核销 %d 笔服务费", updated), sid)
	writeJSON(w, map[string]interface{}{"ok": true, "updated": updated, "corrected": corrected})
}

type opsOwnerStatementItem struct {
	ID           int64   `json:"id"`
	Product      string  `json:"product"`
	OrderNo      string  `json:"order_no"`
	Time         string  `json:"time"`
	Price        float64 `json:"price"`
	Fee          float64 `json:"fee"`
	Shipping     float64 `json:"shipping"`
	Net          float64 `json:"net"`
	BuyAmount    float64 `json:"buy_amount"`
	Counterparty string  `json:"counterparty"`
	Source       string  `json:"source"`
}

type opsSettlement struct {
	ID        int64   `json:"id"`
	Amount    float64 `json:"amount"`
	Note      string  `json:"note"`
	SettledAt string  `json:"settled_at"`
}

func handleOperationsOwnerStatement(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}
	rawOwner := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/operations/owners/"))
	rawOwner = strings.TrimSuffix(rawOwner, "/statement")
	if rawOwner == "" {
		writeError(w, http.StatusBadRequest, "缺少归属人")
		return
	}
	owner := strings.TrimSpace(strings.ReplaceAll(rawOwner, "%20", " "))
	if unescaped, err := url.PathUnescape(owner); err == nil {
		owner = unescaped
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind != "buy" {
		kind = "sell"
	}
	from := strings.TrimSpace(r.URL.Query().Get("from"))
	to := strings.TrimSpace(r.URL.Query().Get("to"))
	source := "寄卖"
	if kind == "buy" {
		source = "代采"
	}
	sid := shopID(r)
	items, err := loadShopItems(sid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取对账数据失败")
		return
	}
	outItems := []opsOwnerStatementItem{}
	var periodTotal, cumulativeTotal float64
	for _, it := range items {
		if it.Kind == "discarded" || it.Source != source || strings.TrimSpace(it.Owner) != owner {
			continue
		}
		amount := 0.0
		tm := it.SellTime
		price, fee, ship, net := it.SellPrice, it.SellFee+it.SellWuyouFee, it.SellShipping, it.SellNet
		buyAmount := it.BuyPrice + it.BuyShipping
		if kind == "buy" {
			tm = it.BuyTime
			amount = buyAmount
		} else {
			amount = net
		}
		cumulativeTotal += amount
		day := tm
		if len(day) > 10 {
			day = day[:10]
		}
		if from != "" && day < from {
			continue
		}
		if to != "" && day > to {
			continue
		}
		periodTotal += amount
		outItems = append(outItems, opsOwnerStatementItem{
			ID: it.ID, Product: itemDisplayName(it), OrderNo: it.SellOrderNo,
			Time: tm, Price: round2(price), Fee: round2(fee), Shipping: round2(ship),
			Net: round2(net), BuyAmount: round2(buyAmount), Counterparty: it.SellCounterparty,
			Source: it.Source,
		})
	}
	rows, err := db.Query(`SELECT id,amount,note,settled_at FROM consign_settlements
        WHERE shop_id=? AND owner=? AND kind=? ORDER BY settled_at DESC,id DESC`, sid, owner, kind)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取结算历史失败")
		return
	}
	settlements := []opsSettlement{}
	var settledTotal float64
	for rows.Next() {
		var s opsSettlement
		if rows.Scan(&s.ID, &s.Amount, &s.Note, &s.SettledAt) == nil {
			s.Amount = round2(s.Amount)
			settlements = append(settlements, s)
			settledTotal += s.Amount
		}
	}
	rows.Close()
	writeJSON(w, map[string]interface{}{
		"owner": owner, "kind": kind, "from": from, "to": to,
		"items": outItems, "settlements": settlements,
		"summary": map[string]interface{}{
			"orders": len(outItems), "period_total": round2(periodTotal),
			"cumulative_total": round2(cumulativeTotal), "settled": round2(settledTotal),
			"pending": round2(math.Max(0, cumulativeTotal-settledTotal)),
		},
		"generated_at": nowStr(),
	})
}

type opsLot struct {
	ID            int64   `json:"id"`
	Product       string  `json:"product"`
	OrderNo       string  `json:"order_no"`
	BuyTime       string  `json:"buy_time"`
	AgeDays       int     `json:"age_days"`
	AgeBand       string  `json:"age_band"`
	Quantity      int     `json:"quantity"`
	LandedCost    float64 `json:"landed_cost"`
	UnitLanded    float64 `json:"unit_landed"`
	RemainingCost float64 `json:"remaining_cost"`
	RemainingQty  float64 `json:"remaining_qty"`
	SoldOut       bool    `json:"sold_out"`
	Settled       bool    `json:"settled"`
	BatchNo       string  `json:"batch_no"`
	Supplier      string  `json:"supplier"`
	Note          string  `json:"note"`
	UpdatedAt     string  `json:"updated_at"`
}

func opsAgeDays(s string) int {
	t := opsParseTime(s)
	if t.IsZero() {
		return 0
	}
	days := int(time.Since(t).Hours() / 24)
	if days < 0 {
		return 0
	}
	return days
}

func opsAgeBand(days int) string {
	switch {
	case days <= 30:
		return "0-30天"
	case days <= 60:
		return "31-60天"
	case days <= 90:
		return "61-90天"
	default:
		return "90天以上"
	}
}

func handleOperationsLots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}
	sid := shopID(r)
	items, err := loadShopItems(sid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取采购批次失败")
		return
	}
	alloc := loadGroupAlloc(sid)
	base := map[int64]Item{}
	lots := []holdingLot{}
	deductions := []holdingDeduction{}
	for _, it := range items {
		if it.Kind == "discarded" || isExternalGoods(it.Source) {
			continue
		}
		base[it.ID] = it
		switch it.Kind {
		case "purchase":
			if it.SettledAt == "" {
				gross := round2(it.BuyPrice + it.BuyShipping)
				if gross > 0 {
					lots = append(lots, holdingLot{
						ID: it.ID, Product: it.Product, Norm: itemProductNorm(it),
						Gross: gross, BuyTime: it.BuyTime, AgeBand: holdingAgeBand(it.BuyTime),
					})
				}
			}
		case "sale":
			if it.CostFree {
				continue
			}
			qty := it.Quantity
			if qty < 1 {
				qty = 1
			}
			unit := effectiveUnitCost(it, alloc)
			amount := round2(unit * float64(qty))
			if amount > 0 {
				deductions = append(deductions, holdingDeduction{
					ParentID: it.ParentID, Norm: itemProductNorm(it),
					Amount: amount, SellTime: it.SellTime,
				})
			}
		}
	}
	lots = applyHoldingDeductions(lots, deductions)
	remaining := map[int64]float64{}
	for _, lot := range lots {
		remaining[lot.ID] = lot.Remaining
	}
	meta := map[int64]opsLot{}
	if rows, qerr := db.Query(`SELECT item_id,batch_no,supplier,note,updated_at FROM purchase_lot_meta WHERE shop_id=?`, sid); qerr == nil {
		for rows.Next() {
			var id int64
			var batchNo, supplier, note, updatedAt string
			if rows.Scan(&id, &batchNo, &supplier, &note, &updatedAt) == nil {
				meta[id] = opsLot{BatchNo: batchNo, Supplier: supplier, Note: note, UpdatedAt: updatedAt}
			}
		}
		rows.Close()
	}
	out := []opsLot{}
	var totalRemaining, totalLanded float64
	oldRemaining := 0.0
	for _, it := range items {
		if it.Kind != "purchase" || isExternalGoods(it.Source) || it.DiscardedAt != "" {
			continue
		}
		gross := round2(it.BuyPrice + it.BuyShipping)
		rem := remaining[it.ID]
		if it.SettledAt != "" {
			rem = 0
		}
		qty := it.Quantity
		if qty < 1 {
			qty = 1
		}
		m := meta[it.ID]
		days := opsAgeDays(it.BuyTime)
		remQty := 0.0
		if gross > 0 {
			remQty = float64(qty) * rem / gross
		}
		row := opsLot{
			ID: it.ID, Product: itemDisplayName(it), OrderNo: it.BuyOrderNo,
			BuyTime: it.BuyTime, AgeDays: days, AgeBand: opsAgeBand(days),
			Quantity: qty, LandedCost: gross, UnitLanded: round2(gross / float64(qty)),
			RemainingCost: round2(rem), RemainingQty: round2(remQty),
			SoldOut: rem <= 0.005, Settled: it.SettledAt != "", BatchNo: m.BatchNo,
			Supplier: m.Supplier, Note: m.Note, UpdatedAt: m.UpdatedAt,
		}
		if row.SoldOut && it.SettledAt == "" {
			row.Supplier = m.Supplier
		}
		if !it.HasBuy && gross <= 0 {
			continue
		}
		out = append(out, row)
		totalLanded += gross
		totalRemaining += row.RemainingCost
		if days > 90 && row.RemainingCost > 0 {
			oldRemaining += row.RemainingCost
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RemainingCost > 0.005 && out[j].RemainingCost <= 0.005 {
			return true
		}
		if out[i].RemainingCost <= 0.005 && out[j].RemainingCost > 0.005 {
			return false
		}
		return out[i].BuyTime > out[j].BuyTime
	})
	writeJSON(w, map[string]interface{}{
		"items": out,
		"summary": map[string]interface{}{
			"batches": len(out), "active": func() int {
				n := 0
				for _, lot := range out {
					if lot.RemainingCost > 0.005 {
						n++
					}
				}
				return n
			}(),
			"total_landed": round2(totalLanded), "remaining": round2(totalRemaining),
			"old_remaining": round2(oldRemaining),
		},
	})
}

func handleOperationsLotMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}
	var in struct {
		ItemID            int64  `json:"item_id"`
		BatchNo           string `json:"batch_no"`
		Supplier          string `json:"supplier"`
		Note              string `json:"note"`
		ExpectedUpdatedAt string `json:"expected_updated_at"`
	}
	if !decodeJSONBody(w, r, &in, maxSmallJSONBody) {
		return
	}
	if in.ItemID <= 0 {
		writeError(w, http.StatusBadRequest, "无效批次")
		return
	}
	sid := shopID(r)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM items WHERE id=? AND shop_id=? AND `+purchaseCond,
		in.ItemID, sid).Scan(&n); err != nil || n == 0 {
		writeError(w, http.StatusNotFound, "采购批次不存在")
		return
	}
	ts := versionStr()
	tx, err := db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "开启事务失败")
		return
	}
	defer tx.Rollback()
	var currentUpdatedAt string
	_ = tx.QueryRow(`SELECT updated_at FROM purchase_lot_meta WHERE shop_id=? AND item_id=?`, sid, in.ItemID).
		Scan(&currentUpdatedAt)
	if in.ExpectedUpdatedAt != "" && in.ExpectedUpdatedAt != currentUpdatedAt {
		_ = tx.Rollback()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "批次信息已被其他人修改，请刷新后再保存",
			"code":  "conflict",
			"current": map[string]interface{}{
				"item_id": in.ItemID, "updated_at": currentUpdatedAt,
			},
		})
		return
	}
	if _, err := tx.Exec(`INSERT INTO purchase_lot_meta
        (shop_id,item_id,batch_no,supplier,note,created_at,updated_at)
        VALUES(?,?,?,?,?,?,?)
        ON CONFLICT(shop_id,item_id) DO UPDATE SET
        batch_no=excluded.batch_no,supplier=excluded.supplier,
        note=excluded.note,updated_at=excluded.updated_at`,
		sid, in.ItemID, strings.TrimSpace(in.BatchNo), strings.TrimSpace(in.Supplier),
		strings.TrimSpace(in.Note), ts, ts); err != nil {
		writeError(w, http.StatusInternalServerError, "保存批次信息失败")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "保存批次信息失败")
		return
	}
	setAudit(r, "修改采购批次", "lot", in.ItemID, "更新批次资料："+strings.TrimSpace(in.BatchNo),
		map[string]interface{}{"batch_no": in.BatchNo, "supplier": in.Supplier})
	writeJSON(w, map[string]interface{}{"ok": true, "updated_at": ts})
}

type opsPricingRow struct {
	Product          string  `json:"product"`
	Norm             string  `json:"norm"`
	Sales            int     `json:"sales"`
	AveragePrice     float64 `json:"average_price"`
	LastPrice        float64 `json:"last_price"`
	LowPrice         float64 `json:"low_price"`
	HighPrice        float64 `json:"high_price"`
	UnitCost         float64 `json:"unit_cost"`
	Shipping         float64 `json:"shipping"`
	FeeRate          float64 `json:"fee_rate"`
	FloorPrice       float64 `json:"floor_price"`
	RecommendedPrice float64 `json:"recommended_price"`
	ExpectedProfit   float64 `json:"expected_profit"`
	ExpectedMargin   float64 `json:"expected_margin"`
	StockCost        float64 `json:"stock_cost"`
	StockAgeDays     int     `json:"stock_age_days"`
	Restock          bool    `json:"restock"`
	Reason           string  `json:"reason"`
}

func opsRoundPrice(v float64) float64 {
	if v <= 0 {
		return 0
	}
	// 闲鱼实际挂牌价通常取整到元；低于 20 元时保留一位小数。
	if v < 20 {
		return math.Round(v*10) / 10
	}
	return math.Round(v)
}

func handleOperationsPricing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}
	targetMargin := 25.0
	if raw := strings.TrimSpace(r.URL.Query().Get("target_margin")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			targetMargin = v
		}
	}
	if targetMargin < 0 {
		targetMargin = 0
	}
	if targetMargin > 80 {
		targetMargin = 80
	}
	productFilter := normProduct(r.URL.Query().Get("product"))
	sid := shopID(r)
	items, err := loadShopItems(sid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取定价数据失败")
		return
	}
	alloc := loadGroupAlloc(sid)
	rates := opsFeeRates(items)
	type agg struct {
		name      string
		norm      string
		prices    []float64
		lastPrice float64
		lastTime  string
		shipping  []float64
		costs     []float64
		stockCost float64
		stockAge  int
	}
	byNorm := map[string]*agg{}
	get := func(it Item) *agg {
		key := it.ProductNorm
		if key == "" {
			key = normProduct(it.Product)
		}
		if key == "" {
			key = "__empty__"
		}
		a := byNorm[key]
		if a == nil {
			a = &agg{name: itemDisplayName(it), norm: key}
			byNorm[key] = a
		}
		return a
	}
	for _, it := range items {
		if it.Kind == "discarded" || isExternalGoods(it.Source) {
			continue
		}
		a := get(it)
		switch it.Kind {
		case "sale":
			if it.SellPrice <= 0 {
				continue
			}
			a.prices = append(a.prices, it.SellPrice)
			if it.SellTime >= a.lastTime {
				a.lastPrice, a.lastTime = it.SellPrice, it.SellTime
			}
			if it.SellShipping > 0 {
				a.shipping = append(a.shipping, it.SellShipping)
			}
			if unit := effectiveUnitCost(it, alloc); unit > 0 {
				a.costs = append(a.costs, unit)
			}
		case "purchase":
			rem := it.BuyPrice + it.BuyShipping
			if it.SettledAt == "" && rem > 0 {
				a.stockCost += rem
				if days := opsAgeDays(it.BuyTime); days > a.stockAge {
					a.stockAge = days
				}
				qty := float64(it.Quantity)
				if qty < 1 {
					qty = 1
				}
				a.costs = append(a.costs, rem/qty)
			}
		case "standalone":
			if it.SellPrice > 0 {
				a.prices = append(a.prices, it.SellPrice)
				if it.SellTime >= a.lastTime {
					a.lastPrice, a.lastTime = it.SellPrice, it.SellTime
				}
			}
		}
	}
	out := []opsPricingRow{}
	for key, a := range byNorm {
		if len(a.prices) == 0 && a.stockCost <= 0 {
			continue
		}
		if productFilter != "" && key != productFilter {
			continue
		}
		cost := opsMedian(a.costs)
		if cost <= 0 {
			continue
		}
		shipping := opsMedian(a.shipping)
		rate := rates[key]
		if rate <= 0 || rate > 0.2 {
			rate = 0.026
		}
		average := 0.0
		low, high := 0.0, 0.0
		if len(a.prices) > 0 {
			sort.Float64s(a.prices)
			low, high = a.prices[0], a.prices[len(a.prices)-1]
			sum := 0.0
			for _, v := range a.prices {
				sum += v
			}
			average = sum / float64(len(a.prices))
		}
		targetRate := targetMargin / 100
		floorMargin := 0.08
		floorPrice := 0.0
		if 1-rate-floorMargin > 0 {
			floorPrice = opsRoundPrice((cost + shipping) / (1 - rate - floorMargin))
		}
		recommended := 0.0
		if 1-rate-targetRate > 0 {
			recommended = opsRoundPrice((cost + shipping) / (1 - rate - targetRate))
		}
		if average > 0 {
			marketFloor := opsRoundPrice(average * 0.95)
			if recommended < marketFloor {
				recommended = marketFloor
			}
		}
		if recommended < floorPrice {
			recommended = floorPrice
		}
		profit := round2(recommended*(1-rate) - cost - shipping)
		margin := 0.0
		if recommended > 0 {
			margin = round2(profit / recommended * 100)
		}
		restock := len(a.prices) >= 2 && margin >= targetMargin && a.stockCost <= 0.005
		reason := "按目标利润率、历史成交价和实际运费计算"
		if restock {
			reason = "历史至少成交 2 单且当前无在库，可优先补货"
		} else if a.stockAge > 60 {
			reason = fmt.Sprintf("在库已 %d 天，建议先清库存再补货", a.stockAge)
		}
		out = append(out, opsPricingRow{
			Product: a.name, Norm: key, Sales: len(a.prices),
			AveragePrice: round2(average), LastPrice: round2(a.lastPrice),
			LowPrice: round2(low), HighPrice: round2(high),
			UnitCost: round2(cost), Shipping: round2(shipping), FeeRate: round2(rate * 100),
			FloorPrice: floorPrice, RecommendedPrice: recommended,
			ExpectedProfit: profit, ExpectedMargin: margin,
			StockCost: round2(a.stockCost), StockAgeDays: a.stockAge,
			Restock: restock, Reason: reason,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Restock != out[j].Restock {
			return out[i].Restock
		}
		if out[i].Sales == out[j].Sales {
			return out[i].ExpectedProfit > out[j].ExpectedProfit
		}
		return out[i].Sales > out[j].Sales
	})
	if productFilter == "" && len(out) > 24 {
		out = out[:24]
	}
	writeJSON(w, map[string]interface{}{
		"items": out, "target_margin": targetMargin,
		"assumption": "预计费用率优先取同商品历史中位数，无历史时按 2.6%；运费取历史实际值。",
	})
}

func handleOperationsMultiShop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}
	if !isGlobalAdmin(r) {
		writeError(w, http.StatusForbidden, "仅全局管理员可查看多店汇总")
		return
	}
	rows, err := db.Query(`SELECT id,name,color FROM shops WHERE archived_at='' ORDER BY sort,id`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取店铺失败")
		return
	}
	type shopRow struct {
		ID          int64   `json:"id"`
		Name        string  `json:"name"`
		Color       string  `json:"color"`
		Sales       float64 `json:"sales"`
		Cost        float64 `json:"cost"`
		Profit      float64 `json:"profit"`
		Margin      float64 `json:"margin"`
		Holding     float64 `json:"holding"`
		PendingCost int     `json:"pending_cost"`
		FeePending  int     `json:"fee_pending"`
	}
	type shopRef struct {
		id    int64
		name  string
		color string
	}
	shops := []shopRef{}
	for rows.Next() {
		var s shopRef
		if rows.Scan(&s.id, &s.name, &s.color) == nil {
			shops = append(shops, s)
		}
	}
	rows.Close()
	out := []shopRow{}
	for _, s := range shops {
		profitRows := computeProductProfit(s.id)
		row := shopRow{ID: s.id, Name: s.name, Color: s.color}
		for _, p := range profitRows {
			row.Sales += p.Sales
			row.Cost += p.Cost
			row.Profit += p.Profit
			row.Holding += p.Stock
			row.PendingCost += p.Unlinked
		}
		if row.Sales > 0 {
			row.Margin = round2(row.Profit / row.Sales * 100)
		}
		items, _ := loadShopItems(s.id)
		for _, it := range items {
			if it.Kind != "discarded" && it.SellPrice > 0 && it.SellFee+it.SellWuyouFee <= 0.005 {
				row.FeePending++
			}
		}
		row.Sales = round2(row.Sales)
		row.Cost = round2(row.Cost)
		row.Profit = round2(row.Profit)
		row.Holding = round2(row.Holding)
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Profit > out[j].Profit })
	writeJSON(w, map[string]interface{}{"items": out})
}
