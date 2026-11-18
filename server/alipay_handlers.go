package main

// 支付宝账单导入的 HTTP 接口：预览 → 勾选 → 写回。
//
// 两种数据，两种匹配方式：
//   手续费（分账-软件服务费(订单号)扣款）—— 记录本身带 19 位闲鱼订单号，精确匹配。
//   快递费（闲鱼寄件-寄件费_1850739427_LP…）—— 不带订单号，但付款时间与闲鱼的
//     发货时间（consignTime）只差 2~5 秒，按发货时间就近匹配即可唯一确定。
//     老记录没有发货时间时才退回「按成交时间推测」，并明确标注「推测」。

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type aliPlanRow struct {
	OrderNo  string  `json:"order_no"`
	Product  string  `json:"product"`
	RecID    int64   `json:"rec_id"`
	Kind     string  `json:"kind"` // fee | ship | both
	Price    float64 `json:"price"`
	OldFee   float64 `json:"old_fee"`
	OldWuyou float64 `json:"old_wuyou"`
	OldShip  float64 `json:"old_ship"`
	Fee      float64 `json:"fee"`
	Wuyou    float64 `json:"wuyou"`
	Shipping float64 `json:"shipping"`
	NewNet   float64 `json:"new_net"`
	// 快递费来源：exact=按发货时间精确配上；guess=按成交时间推测
	ShipExact bool   `json:"ship_exact"`
	ShipTime  string `json:"ship_time"`
	ShipGap   string `json:"ship_gap"`
	// 手续费来源：订单号对不上时按「成交时间 + 费率」兜底匹配
	FeeGuess bool    `json:"fee_guess"`
	FeeRate  float64 `json:"fee_rate"` // 实扣 / 成交价，用来核对是不是 1% / 1.6% / 2.6%
	Skip      string `json:"skip"` // 非空=不写入的原因
}

type aliPlan struct {
	ID      string       `json:"plan_id"`
	MadeAt  time.Time    `json:"made_at"`
	Rows    []aliPlanRow `json:"rows"`
	FeeHits int          `json:"fee_hits"`  // 账单里带订单号的分账记录数
	FeeMiss int          `json:"fee_miss"`  // 有手续费但库里没有这单
	Ships   int          `json:"ships"`     // 快递费记录数
	ShipSum float64      `json:"ship_sum"`  // 快递费合计
	Exact   int          `json:"exact"`     // 按发货时间精确配上的笔数
	Guess   int          `json:"guess"`     // 只能靠成交时间推测的笔数
	FeeByTime int        `json:"fee_by_time"` // 手续费按成交时间 + 费率配上的笔数
	Warn    string       `json:"warn"`
	Shop    int64        `json:"-"`
}

type aliSellRec struct {
	ID       int64
	OrderNo  string
	Product  string
	Price    float64
	Fee      float64
	Wuyou    float64
	Net      float64
	Shipping float64
	Time     string
	Consign  string
	Settled  bool
	Discard  bool
}

func loadSellRecs(shop int64) []aliSellRec {
	// 卖单 = 有卖出价的记录：手工录入的常常没填订单号，
	// 它们同样要参与「按成交时间 + 费率」的手续费匹配，所以不能只取有单号的。
	rows, err := db.Query(`SELECT id, sell_order_no, product, sell_price, sell_fee, sell_wuyou_fee,
        sell_net, sell_shipping, sell_time, COALESCE(consign_time,''), settled_at, discarded_at
        FROM items WHERE shop_id=? AND sell_price>0`, shop)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []aliSellRec{}
	for rows.Next() {
		var r aliSellRec
		var st, dc string
		if rows.Scan(&r.ID, &r.OrderNo, &r.Product, &r.Price, &r.Fee, &r.Wuyou,
			&r.Net, &r.Shipping, &r.Time, &r.Consign, &st, &dc) != nil {
			continue
		}
		r.Settled = st != ""
		r.Discard = dc != ""
		out = append(out, r)
	}
	// 已完结联单里的记录也算「不该再动」
	grpSettled := map[int64]bool{}
	if gr, e := db.Query(`SELECT DISTINCT gi.item_id FROM group_items gi
        JOIN link_groups g ON g.id=gi.group_id WHERE g.settled_at<>'' AND g.shop_id=?`, shop); e == nil {
		for gr.Next() {
			var id int64
			if gr.Scan(&id) == nil {
				grpSettled[id] = true
			}
		}
		gr.Close()
	}
	for i := range out {
		if grpSettled[out[i].ID] {
			out[i].Settled = true
		}
	}
	return out
}

var (
	aliMu   sync.Mutex
	aliLast = map[int64]*aliPlan{}
	// 撤销快照：写入前记下每条记录的原始四列，导错了可以一键还原
	aliUndo = map[int64][]aliUndoRow{}
)

type aliUndoRow struct {
	ID      int64
	Fee     float64
	Wuyou   float64
	Ship    float64
	Net     float64
	Product string
}

func handleAlipayPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	shop := shopID(r)
	r.Body = http.MaxBytesReader(w, r.Body, 24<<20)
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeError(w, 400, "没有收到文件，请选择支付宝导出的 .xlsx")
		return
	}
	defer file.Close()
	sheet, err := readXLSX(file, hdr.Size)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	txns, err := parseAlipayDetail(sheet)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if len(txns) == 0 {
		writeError(w, 400, "账单里没有解析到任何交易记录")
		return
	}

	fees := collectFees(txns)
	ships := collectShipping(txns)
	recs := loadSellRecs(shop)

	byOrder := map[string]*aliSellRec{}
	for i := range recs {
		if recs[i].OrderNo != "" {
			byOrder[recs[i].OrderNo] = &recs[i]
		}
	}

	plan := &aliPlan{ID: strconv.FormatInt(time.Now().UnixNano(), 36), MadeAt: time.Now(), Shop: shop}
	rowOf := map[int64]int{} // recID -> 在 plan.Rows 里的下标，运费可并进已有手续费行
	shipTaken := map[int64]bool{}

	// 1) 手续费：按订单号精确匹配
	type feeMiss struct {
		no string
		f  *aliFeeAgg
	}
	misses := []feeMiss{}
	feeMatched := map[int64]bool{}
	for no, f := range fees {
		plan.FeeHits++
		rec := byOrder[no]
		if rec == nil {
			misses = append(misses, feeMiss{no, f})
			continue
		}
		fee := round2(f.Tech + f.Basic + f.Other)
		wuyou := round2(f.Wuyou)
		row := aliPlanRow{
			OrderNo: no, Product: rec.Product, RecID: rec.ID, Kind: "fee", Price: rec.Price,
			OldFee: rec.Fee, OldWuyou: rec.Wuyou, OldShip: rec.Shipping,
			Fee: fee, Wuyou: wuyou, Shipping: rec.Shipping,
		}
		row.NewNet = round2(rec.Price - fee - wuyou - rec.Shipping)
		switch {
		case rec.Settled:
			row.Skip = "已完结，不改动"
		case rec.Discard:
			row.Skip = "已弃单，不改动"
		case absf(rec.Fee-fee) < 0.005 && absf(rec.Wuyou-wuyou) < 0.005:
			row.Skip = "与现有费用一致"
		}
		rowOf[rec.ID] = len(plan.Rows)
		plan.Rows = append(plan.Rows, row)
		feeMatched[rec.ID] = true
	}

	// 1b) 订单号对不上（手工录入没填订单号 / 老单号格式不同）时，
	// 用「成交时间 + 费率」兜底：扣费记录比成交晚 1~2 分钟，
	// 实扣金额 / 成交价 落在 0.5%~3.2%（1% 软件、1.6% 含基础、2.6% 再含无忧卖）。
	sort.Slice(misses, func(i, j int) bool { return misses[i].f.Time.Before(misses[j].f.Time) })
	for _, m := range misses {
		total := m.f.Tech + m.f.Basic + m.f.Other + m.f.Wuyou
		best, bestGap := -1, 0.0
		for i := range recs {
			r := &recs[i]
			if r.Settled || r.Discard || feeMatched[r.ID] || r.Price <= 0 {
				continue
			}
			t := aliParseTime(r.Time)
			if t.IsZero() {
				continue
			}
			gap := m.f.Time.Sub(t).Minutes()
			if gap < -10 || gap > 30 {
				continue
			}
			rate := total / r.Price * 100
			if rate < 0.5 || rate > 3.2 {
				continue
			}
			if best < 0 || math.Abs(gap) < math.Abs(bestGap) {
				best, bestGap = i, gap
			}
		}
		if best < 0 {
			plan.FeeMiss++
			continue
		}
		rec := &recs[best]
		fee := round2(m.f.Tech + m.f.Basic + m.f.Other)
		wuyou := round2(m.f.Wuyou)
		row := aliPlanRow{
			OrderNo: rec.OrderNo, Product: rec.Product, RecID: rec.ID, Kind: "fee", Price: rec.Price,
			OldFee: rec.Fee, OldWuyou: rec.Wuyou, OldShip: rec.Shipping,
			Fee: fee, Wuyou: wuyou, Shipping: rec.Shipping,
			FeeGuess: true, FeeRate: round2(fee / rec.Price * 100),
		}
		row.NewNet = round2(rec.Price - fee - wuyou - rec.Shipping)
		switch {
		case absf(rec.Fee-fee) < 0.005 && absf(rec.Wuyou-wuyou) < 0.005:
			row.Skip = "与现有费用一致"
		}
		rowOf[rec.ID] = len(plan.Rows)
		plan.Rows = append(plan.Rows, row)
		feeMatched[rec.ID] = true
		plan.FeeByTime++
	}

	// 2) 快递费：优先按「发货时间」精确匹配（实测只差 2~5 秒）
	addShip := func(rec *aliSellRec, sh aliShip, exact bool, gapText string) bool {
		plan.Ships++
		plan.ShipSum += sh.Amount
		if i, ok := rowOf[rec.ID]; ok {
			// 这一单已经因为手续费进表了：合并成一行，避免同一单出现两条。
			// 手续费「与现有费用一致」不算锁死——运费还是要写进去，所以清掉跳过标记。
			row := &plan.Rows[i]
			if row.Skip == "已完结，不改动" || row.Skip == "已弃单，不改动" {
				return false
			}
			row.Skip = ""
			row.Kind = "both"
			row.Shipping = round2(sh.Amount)
			row.NewNet = round2(row.Price - row.Fee - row.Wuyou - row.Shipping)
			row.ShipExact = exact
			row.ShipTime = sh.Time.Format("2006-01-02 15:04:05")
			row.ShipGap = gapText
		} else {
			row := aliPlanRow{
				OrderNo: rec.OrderNo, Product: rec.Product, RecID: rec.ID, Kind: "ship", Price: rec.Price,
				OldFee: rec.Fee, OldWuyou: rec.Wuyou, OldShip: rec.Shipping,
				Fee: rec.Fee, Wuyou: rec.Wuyou, Shipping: round2(sh.Amount),
				NewNet:    round2(rec.Price - rec.Fee - rec.Wuyou - sh.Amount),
				ShipExact: exact, ShipTime: sh.Time.Format("2006-01-02 15:04:05"), ShipGap: gapText,
			}
			switch {
			case rec.Settled:
				row.Skip = "已完结，不改动"
			case rec.Discard:
				row.Skip = "已弃单，不改动"
			case rec.Shipping > 0.1 && !exact:
				row.Skip = "已有运费"
			}
			rowOf[rec.ID] = len(plan.Rows)
			plan.Rows = append(plan.Rows, row)
		}
		shipTaken[rec.ID] = true
		if exact {
			plan.Exact++
		} else {
			plan.Guess++
		}
		return true
	}

	rest := []aliShip{}
	for _, sh := range ships {
		best := -1
		bestGap := 0.0
		for i := range recs {
			r := &recs[i]
			// 精确匹配时允许覆盖已有运费：之前按成交时间猜的值可能配错单
			// （例如 11-02 那单猜成 10.00，实际 18.90）。改动会在核对表里标出「原 X」。
			if r.Consign == "" || r.Settled || r.Discard || shipTaken[r.ID] {
				continue
			}
			ct := aliParseTime(r.Consign)
			if ct.IsZero() {
				continue
			}
			gap := math.Abs(sh.Time.Sub(ct).Seconds())
			if gap > 180 { // 3 分钟：实测 2~5 秒，留足容差但不至於撞到别的单
				continue
			}
			if best < 0 || gap < bestGap {
				best, bestGap = i, gap
			}
		}
		if best < 0 {
			rest = append(rest, sh)
			continue
		}
		d := sh.Time.Sub(aliParseTime(recs[best].Consign)).Seconds()
		gapText := "与发货同一秒"
		if d > 0 {
			gapText = fmt.Sprintf("比发货晚 %.0f 秒", d)
		} else if d < 0 {
			gapText = fmt.Sprintf("比发货早 %.0f 秒", -d)
		}
		addShip(&recs[best], sh, true, gapText)
	}

	// 3) 没有发货时间的老记录：按成交时间就近推测（明确标注，且排在精确匹配之后）
	if len(rest) > 0 {
		free := []*aliSellRec{}
		for i := range recs {
			r := &recs[i]
			// 已经有手续费行的记录同样可以配上快递费（addShip 会合并成一行），
			// 只有「已经有运费」或已经被别的快递费占用才跳过。
			if r.Settled || r.Discard || r.Shipping > 0.1 || shipTaken[r.ID] {
				continue
			}
			free = append(free, r)
		}
		sort.Slice(free, func(i, j int) bool { return free[i].Time > free[j].Time })
		for _, sh := range rest {
			best := -1
			bestGap := 0.0
			for i, rec := range free {
				it := aliParseTime(rec.Time)
				if it.IsZero() {
					continue
				}
				// 揽件一定发生在成交之后；超过 10 天基本不是这一单
				gap := sh.Time.Sub(it).Hours()
				if gap < -12 || gap > 240 {
					continue
				}
				if best < 0 || gap < bestGap {
					best, bestGap = i, gap
				}
			}
			if best < 0 {
				continue
			}
			rec := free[best]
			gapText := "当天成交后"
			if bestGap >= 24 {
				gapText = fmt.Sprintf("成交 %.0f 天后", bestGap/24)
			} else if bestGap >= 1 {
				gapText = fmt.Sprintf("成交 %.0f 小时后", bestGap)
			}
			addShip(rec, sh, false, gapText)
			free = append(free[:best], free[best+1:]...)
		}
	}

	plan.ShipSum = round2(plan.ShipSum)
	if len(fees) == 0 {
		plan.Warn = "没有在账单里找到「分账-软件服务费(订单号)」这类记录，请确认导出的是「全部」交易类型、且时间范围覆盖订单"
	}
	if plan.Guess > 0 {
		plan.Warn = strings.TrimSpace(plan.Warn + " 有 " + strconv.Itoa(plan.Guess) + " 笔快递费只能按成交时间推测（这些单还没有发货时间，先同步一次闲鱼就能精确匹配）。")
	}

	sort.SliceStable(plan.Rows, func(i, j int) bool {
		return plan.Rows[i].Skip == "" && plan.Rows[j].Skip != ""
	})
	aliMu.Lock()
	aliLast[shop] = plan
	aliMu.Unlock()
	writeJSON(w, plan)
}

func handleAlipayApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	shop := shopID(r)
	var in struct {
		PlanID   string   `json:"plan_id"`
		OrderNos []string `json:"order_nos"`
		// Force=true 时允许覆盖「已完结」的记录（用户在面板上显式勾了「覆盖已完结」）。
		// 已弃单仍然永远不动。
		Force bool `json:"force"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeError(w, 400, "请求体解析失败")
		return
	}
	aliMu.Lock()
	plan := aliLast[shop]
	aliMu.Unlock()
	if plan == nil || plan.ID != in.PlanID {
		writeError(w, 409, "导入计划已失效，请重新上传账单")
		return
	}
	if time.Since(plan.MadeAt) > time.Hour {
		writeError(w, 409, "导入计划已过期（超过 1 小时），请重新上传账单")
		return
	}
	want := map[string]bool{}
	for _, s := range in.OrderNos {
		want[s] = true
	}
	if len(want) == 0 {
		writeError(w, 400, "没有选中任何记录")
		return
	}

	// 逐单合并：同一单可能既有手续费又有运费，合成一次更新
	type upd struct {
		recID          int64
		product        string
		fee, wuyou, sh float64
	}
	ups := map[string]*upd{}
	for _, row := range plan.Rows {
		allow := row.Skip == "" || (in.Force && row.Skip == "已完结，不改动")
		if !allow || !want[row.OrderNo] {
			continue
		}
		u := ups[row.OrderNo]
		if u == nil {
			u = &upd{recID: row.RecID, product: row.Product,
				fee: row.OldFee, wuyou: row.OldWuyou, sh: row.OldShip}
			ups[row.OrderNo] = u
		}
		u.fee, u.wuyou = row.Fee, row.Wuyou
		u.sh = row.Shipping
	}

	snap := []aliUndoRow{}
	n := 0
	for _, u := range ups {
		var oldFee, oldWuyou, oldShip, oldNet float64
		if e := db.QueryRow(`SELECT sell_fee, sell_wuyou_fee, sell_shipping, sell_net FROM items
            WHERE id=? AND shop_id=?`, u.recID, shop).Scan(&oldFee, &oldWuyou, &oldShip, &oldNet); e != nil {
			continue
		}
		var price float64
		db.QueryRow(`SELECT sell_price FROM items WHERE id=? AND shop_id=?`, u.recID, shop).Scan(&price)
		// 运费：已经填过（>0）的一律保留，账单只在原值为 0 时补。
		// 手动记账的运费（议价包邮、自己贴的快递费）跟账单里的快递费口径不一定一样，
		// 同步顺手覆盖会把用户核过的数字冲掉。
		ship := u.sh
		if oldShip > 0 {
			ship = oldShip
		}
		net := round2(price - u.fee - u.wuyou - ship)
		// 只写金额四列：联单关联、完结、弃单状态一律不碰。
		// 默认跳过已完结（settled_at<>''），勾了「覆盖已完结」才会改到它们；已弃单永远跳过。
		sqlStr := `UPDATE items SET sell_fee=?, sell_wuyou_fee=?, sell_shipping=?, sell_net=?,
            updated_at=? WHERE id=? AND shop_id=? AND discarded_at=''`
		if !in.Force {
			sqlStr += ` AND settled_at=''`
		}
		if _, e := db.Exec(sqlStr,
			round2(u.fee), round2(u.wuyou), round2(ship), net, nowStr(), u.recID, shop); e == nil {
			n++
			snap = append(snap, aliUndoRow{ID: u.recID, Fee: oldFee, Wuyou: oldWuyou, Ship: oldShip, Net: oldNet, Product: u.product})
		}
	}
	aliMu.Lock()
	aliUndo[shop] = snap
	aliMu.Unlock()

	refreshPurchaseIndex()
	realtimeHub.publish("POST", "/api/alipay/apply", "",
		fmt.Sprintf("按支付宝账单更新了 %d 条的费用", n), shop)
	writeJSON(w, map[string]interface{}{"ok": true, "updated": n, "undo": len(snap)})
}

func handleAlipayUndo(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "方法不允许")
		return
	}
	shop := shopID(r)
	aliMu.Lock()
	snap := aliUndo[shop]
	delete(aliUndo, shop)
	aliMu.Unlock()
	if len(snap) == 0 {
		writeError(w, 400, "没有可撤销的导入记录")
		return
	}
	n := 0
	for _, s := range snap {
		if _, e := db.Exec(`UPDATE items SET sell_fee=?, sell_wuyou_fee=?, sell_shipping=?, sell_net=?,
            updated_at=? WHERE id=? AND shop_id=?`,
			s.Fee, s.Wuyou, s.Ship, s.Net, nowStr(), s.ID, shop); e == nil {
			n++
		}
	}
	refreshPurchaseIndex()
	realtimeHub.publish("POST", "/api/alipay/undo", "", "已撤销支付宝账单导入", shop)
	writeJSON(w, map[string]interface{}{"ok": true, "restored": n})
}
