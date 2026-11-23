package main

import (
	"net/http"
	"sort"
	"strings"
)

// 联单合并建议：不直接改数据，先把「看起来该合成一张联单」的组合列出来，
// 让用户挑、改标题，再一次性提交。两类建议：
//   1. group：标题同名 / 高度相似的几个联单 —— 合并成一张
//   2. items：同名但还没进任何联单的散单 —— 新建一张联单装起来
// 已完结的联单不参与建议：完结是对过账的快照，先合并再重开等于改历史账。

type mergeGroupBrief struct {
	ID      int64   `json:"id"`
	Title   string  `json:"title"`
	Items   int     `json:"items"`
	Settled bool    `json:"settled"`
	Sales   float64 `json:"sales"`
}

type mergeItemBrief struct {
	ID      int64   `json:"id"`
	Product string  `json:"product"`
	Kind    string  `json:"kind"` // buy | sell
	Amount  float64 `json:"amount"`
	Time    string  `json:"time"`
}

type mergeSuggestion struct {
	Key     string            `json:"key"`
	Kind    string            `json:"kind"`
	Title   string            `json:"title"`
	Score   int               `json:"score"`
	Reason  string            `json:"reason"`
	Groups  []mergeGroupBrief `json:"groups,omitempty"`
	ItemIDs []int64           `json:"item_ids,omitempty"`
	Items   []mergeItemBrief  `json:"items,omitempty"`
	HasBoth bool              `json:"has_both,omitempty"`
}

// GET /api/groups/merge-suggestions
func handleGroupMergeSuggestions(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "method not allowed")
		return
	}
	sid := shopID(r)

	type gmeta struct {
		id      int64
		title   string
		settled bool
		count   int
		sales   float64
	}
	metas := map[int64]*gmeta{}
	order := []int64{}
	if rows, err := db.Query(`SELECT id, title, COALESCE(settled_at,'') FROM link_groups
        WHERE shop_id=? ORDER BY id`, sid); err == nil {
		for rows.Next() {
			var id int64
			var title, settled string
			if rows.Scan(&id, &title, &settled) == nil {
				metas[id] = &gmeta{id: id, title: title, settled: settled != ""}
				order = append(order, id)
			}
		}
		rows.Close()
	}
	// 成员数 + 成交额
	if rows, err := db.Query(`SELECT gi.group_id, COUNT(DISTINCT i.id),
            COALESCE(SUM(CASE WHEN i.sell_net>0 THEN i.sell_net ELSE 0 END),0)
        FROM group_items gi JOIN items i ON i.id=gi.item_id
        WHERE i.shop_id=? AND COALESCE(i.discarded_at,'')=''
        GROUP BY gi.group_id`, sid); err == nil {
		for rows.Next() {
			var gid int64
			var n int
			var sales float64
			if rows.Scan(&gid, &n, &sales) == nil {
				if m := metas[gid]; m != nil {
					m.count = n
					m.sales = round2(sales)
				}
			}
		}
		rows.Close()
	}

	// ---- ① 标题相同 / 相似 → 合并联单 ----
	// 先用归一化标题精确分组，再把剩下的按相似度聚类，避免只认字面完全一致。
	settledSkipped := 0
	live := make([]int64, 0, len(order))
	for _, id := range order {
		m := metas[id]
		if m == nil || m.count == 0 {
			continue
		}
		if m.settled {
			settledSkipped++
			continue
		}
		live = append(live, id)
	}
	parent := map[int64]int64{}
	var find func(int64) int64
	find = func(x int64) int64 {
		p, ok := parent[x]
		if !ok || p == x {
			parent[x] = x
			return x
		}
		parent[x] = find(p)
		return parent[x]
	}
	union := func(a, b int64) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	for _, id := range live {
		find(id)
	}
	// 两两比较：同名（归一化后一样）直接相连；否则看编辑距离相似度
	for i := 0; i < len(live); i++ {
		for j := i + 1; j < len(live); j++ {
			a, b := metas[live[i]], metas[live[j]]
			na, nb := normProduct(a.title), normProduct(b.title)
			if na == "" || nb == "" {
				continue
			}
			if na == nb || similarity(na, nb) >= 0.72 {
				union(a.id, b.id)
			}
		}
	}
	comps := map[int64][]int64{}
	for _, id := range live {
		root := find(id)
		comps[root] = append(comps[root], id)
	}
	sugg := []mergeSuggestion{}
	for _, members := range comps {
		if len(members) < 2 {
			continue
		}
		sort.Slice(members, func(i, j int) bool { return members[i] < members[j] })
		briefs := make([]mergeGroupBrief, 0, len(members))
		title := ""
		scoreSum, scoreN := 0.0, 0
		for _, id := range members {
			m := metas[id]
			briefs = append(briefs, mergeGroupBrief{
				ID: m.id, Title: m.title, Items: m.count, Settled: m.settled, Sales: m.sales,
			})
			if title == "" || len([]rune(m.title)) < len([]rune(title)) {
				title = m.title
			}
		}
		for i := 0; i < len(members); i++ {
			for j := i + 1; j < len(members); j++ {
				na, nb := normProduct(metas[members[i]].title), normProduct(metas[members[j]].title)
				if na == nb {
					scoreSum += 1
				} else {
					scoreSum += similarity(na, nb)
				}
				scoreN++
			}
		}
		score := 100
		if scoreN > 0 {
			score = int(scoreSum/float64(scoreN)*100 + 0.5)
		}
		sugg = append(sugg, mergeSuggestion{
			Key:    "g-" + itoa64(members[0]),
			Kind:   "group",
			Title:  strings.TrimSpace(title),
			Score:  score,
			Reason: "标题很像的 " + itoa64(int64(len(members))) + " 张联单",
			Groups: briefs,
		})
	}
	// 相似度高的在前；同分的按成员多的在前
	sort.SliceStable(sugg, func(i, j int) bool {
		if sugg[i].Score != sugg[j].Score {
			return sugg[i].Score > sugg[j].Score
		}
		return len(sugg[i].Groups) > len(sugg[j].Groups)
	})
	if len(sugg) > 6 {
		sugg = sugg[:6]
	}

	// ---- ② 同名散单 → 新建联单 ----
	groupedSet := map[int64]bool{}
	if rows, err := db.Query(`SELECT DISTINCT gi.item_id FROM group_items gi
        JOIN link_groups g ON g.id=gi.group_id WHERE g.shop_id=?`, sid); err == nil {
		for rows.Next() {
			var iid int64
			if rows.Scan(&iid) == nil {
				groupedSet[iid] = true
			}
		}
		rows.Close()
	}
	type bucket struct {
		norm   string
		ids    []int64
		briefs []mergeItemBrief
		buyN   int
		sellN  int
		total  float64
		title  string
	}
	buckets := map[string]*bucket{}
	if rows, err := db.Query("SELECT "+itemCols+" FROM items WHERE shop_id=?"+
		" AND COALESCE(discarded_at,'')='' AND COALESCE(settled_at,'')=''", sid); err == nil {
		for rows.Next() {
			it, e := scanItem(rows)
			if e != nil {
				continue
			}
			if groupedSet[it.ID] || isExternalGoods(it.Source) {
				continue
			}
			norm := itemProductNorm(it)
			if norm == "" {
				continue
			}
			b := buckets[norm]
			if b == nil {
				b = &bucket{norm: norm, title: it.Product}
				buckets[norm] = b
			}
			kind := "sell"
			amount := it.SellNet
			t := it.SellTime
			if it.Kind == "purchase" {
				kind = "buy"
				amount = round2(it.BuyPrice + it.BuyShipping)
				t = it.BuyTime
				b.buyN++
			} else if it.Kind == "standalone" {
				kind = "buy"
				amount = round2(it.BuyPrice + it.BuyShipping)
				t = it.BuyTime
				b.buyN++
				b.sellN++
			} else {
				b.sellN++
			}
			b.ids = append(b.ids, it.ID)
			b.total += amount
			b.briefs = append(b.briefs, mergeItemBrief{ID: it.ID, Product: it.Product, Kind: kind, Amount: round2(amount), Time: t})
		}
		rows.Close()
	}
	itemSugg := []mergeSuggestion{}
	for _, b := range buckets {
		if len(b.ids) < 2 {
			continue
		}
		both := b.buyN > 0 && b.sellN > 0
		score := 60 + 10*(len(b.ids)-2)
		reason := "同名散单 " + itoa64(int64(len(b.ids))) + " 条，都还没进联单"
		if both {
			score = 90 + 10*(len(b.ids)-2)
			reason = "同名散单 " + itoa64(int64(len(b.ids))) + " 条（买 " + itoa64(int64(b.buyN)) + " / 卖 " + itoa64(int64(b.sellN)) + "），还没进联单"
		}
		if score > 99 {
			score = 99
		}
		sort.Slice(b.briefs, func(i, j int) bool { return b.briefs[i].Time < b.briefs[j].Time })
		itemSugg = append(itemSugg, mergeSuggestion{
			Key:     "i-" + b.norm,
			Kind:    "items",
			Title:   strings.TrimSpace(b.title),
			Score:   score,
			Reason:  reason,
			ItemIDs: b.ids,
			Items:   b.briefs,
			HasBoth: both,
		})
	}
	// 买卖都有的优先，再看条数
	sort.SliceStable(itemSugg, func(i, j int) bool {
		if itemSugg[i].HasBoth != itemSugg[j].HasBoth {
			return itemSugg[i].HasBoth
		}
		if len(itemSugg[i].ItemIDs) != len(itemSugg[j].ItemIDs) {
			return len(itemSugg[i].ItemIDs) > len(itemSugg[j].ItemIDs)
		}
		return itemSugg[i].Title < itemSugg[j].Title
	})
	if len(itemSugg) > 6 {
		itemSugg = itemSugg[:6]
	}

	writeJSON(w, map[string]interface{}{
		"groups":          sugg,
		"items":           itemSugg,
		"skipped_settled": settledSkipped,
	})
}

// POST /api/groups/merge-apply
// 入参：{kind:"group"|"items", title, group_ids:[], item_ids:[]}
// kind=group 把 group_ids 并成一张（最小 id 作主联单，标题用用户填的）；
// kind=items 用 item_ids 新建一张联单。
func handleGroupMergeApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	var in struct {
		Kind     string  `json:"kind"`
		Title    string  `json:"title"`
		GroupIDs []int64 `json:"group_ids"`
		ItemIDs  []int64 `json:"item_ids"`
	}
	if !decodeJSONBody(w, r, &in, maxNormalJSONBody) {
		return
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		writeError(w, 400, "联单标题不能为空")
		return
	}
	sid := shopID(r)
	ts := nowStr()

	switch in.Kind {
	case "group":
		ids := uniqueIDs(in.GroupIDs)
		if len(ids) < 2 {
			writeError(w, 400, "至少要选两张联单才能合并")
			return
		}
		// 主联单取 id 最小的，其余并入
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		target := ids[0]
		var title0, settled string
		if err := db.QueryRow("SELECT title, COALESCE(settled_at,'') FROM link_groups WHERE id=? AND shop_id=?",
			target, sid).Scan(&title0, &settled); err != nil {
			writeError(w, 404, "联单不存在")
			return
		}
		if settled != "" {
			writeError(w, 400, "「"+title0+"」已完结，先重新开启再合并")
			return
		}
		all := []int64{}
		for _, gid := range ids {
			var st string
			if db.QueryRow("SELECT COALESCE(settled_at,'') FROM link_groups WHERE id=? AND shop_id=?", gid, sid).Scan(&st) != nil {
				continue
			}
			if st != "" {
				writeError(w, 400, "所选联单里有已完结的，请先重新开启再合并")
				return
			}
			if got, e := loadGroupItemIDs(gid, sid); e == nil {
				all = append(all, got...)
			}
		}
		all = uniqueIDs(all)
		if len(all) == 0 {
			writeError(w, 400, "这些联单里没有记录可合并")
			return
		}
		tx, err := db.Begin()
		if err != nil {
			writeError(w, 500, "开启事务失败")
			return
		}
		defer tx.Rollback()
		if err := mergeGroupsTx(tx, target, ids, all, sid, title); err != nil {
			writeError(w, 500, "合并失败: "+err.Error())
			return
		}
		if err := tx.Commit(); err != nil {
			writeError(w, 500, "提交失败: "+err.Error())
			return
		}
		refreshPurchaseIndex()
		writeJSON(w, map[string]interface{}{"ok": true, "group_id": target, "title": title, "moved": len(all)})

	case "items":
		ids := uniqueIDs(in.ItemIDs)
		if len(ids) < 2 {
			writeError(w, 400, "至少要选两条记录才能生成联单")
			return
		}
		// 只认本店、没进联单、没弃单的记录
		ok := []int64{}
		for _, iid := range ids {
			var gid int64
			if db.QueryRow("SELECT COALESCE(group_id,0) FROM group_items WHERE item_id=? LIMIT 1", iid).Scan(&gid) == nil && gid > 0 {
				continue
			}
			var discarded, settled string
			if db.QueryRow("SELECT COALESCE(discarded_at,''), COALESCE(settled_at,'') FROM items WHERE id=? AND shop_id=?",
				iid, sid).Scan(&discarded, &settled) != nil {
				continue
			}
			if discarded != "" || settled != "" {
				continue
			}
			ok = append(ok, iid)
		}
		if len(ok) < 2 {
			writeError(w, 400, "这些记录里已经进联单或已作废，可用的不足两条")
			return
		}
		tx, err := db.Begin()
		if err != nil {
			writeError(w, 500, "开启事务失败")
			return
		}
		defer tx.Rollback()
		gid, err := createGroupTx(tx, sid, title, "", ts)
		if err != nil {
			writeError(w, 500, "创建联单失败: "+err.Error())
			return
		}
		if err := setGroupItemsTx(tx, gid, ok, sid); err != nil {
			writeError(w, 500, "关联记录失败: "+err.Error())
			return
		}
		if err := tx.Commit(); err != nil {
			writeError(w, 500, "提交失败: "+err.Error())
			return
		}
		refreshPurchaseIndex()
		writeJSON(w, map[string]interface{}{"ok": true, "group_id": gid, "title": title, "moved": len(ok)})

	default:
		writeError(w, 400, "未知的合并类型")
	}
}

// itoa64 小工具：拼 key / 文案用，避免到处 strconv.FormatInt
func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := [20]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
