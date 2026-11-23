package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// 联单：N:M 关系的载体。
// 一个联单可以挂任意多个买进单 + 任意多个卖出单；
// 同一条记录也可以属于多个联单（例如一批货分几次卖，每次单独看利润）。
// 统计口径与全局一致：
//   销售额 = Σ卖单成交价（未扣费）
//   手续费 = Σ(软件服务费 + 无忧卖服务费)
//   运费   = Σ(买运费 + 卖运费)
//   货品成本 = Σ买单价（卖出单若已挂买进单，其 unit_cost 不再重复计）
//   净利润 = 销售额 − 货品成本 − 运费 − 手续费

type GroupStats struct {
	BuyCount   int     `json:"buy_count"`
	SellCount  int     `json:"sell_count"`
	Sales      float64 `json:"sales"`
	Net        float64 `json:"net"`
	Goods      float64 `json:"goods"`
	Shipping   float64 `json:"shipping"`
	Fee        float64 `json:"fee"`
	WuyouFee   float64 `json:"wuyou_fee"`
	Profit     float64 `json:"profit"`
	ProfitRate float64 `json:"profit_rate"`
	UnsoldCost float64 `json:"unsold_cost"` // 尚未卖出部分的买进占用
}

type LinkGroup struct {
	ID        int64           `json:"id"`
	Title     string          `json:"title"`
	Note      string          `json:"note"`
	SettledAt string          `json:"settled_at"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
	ItemIDs   []int64         `json:"item_ids"`
	Items     []Item          `json:"items"`
	Externals []GroupExternal `json:"externals"` // 只属于本联单的外部买入 / 卖出，不进明细
	Stats     GroupStats      `json:"stats"`
}

func createGroupTables() error {
	q := `
CREATE TABLE IF NOT EXISTS link_groups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    shop_id INTEGER NOT NULL DEFAULT 1,     -- 所属店铺；联单不允许跨店
    title TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    settled_at TEXT NOT NULL DEFAULT '',   -- 非空=已完结（存完结时间），只锁定联单对账，不整批释放未售库存
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS group_items (
    group_id INTEGER NOT NULL,
    item_id INTEGER NOT NULL,
    PRIMARY KEY (group_id, item_id)
);
CREATE INDEX IF NOT EXISTS idx_group_items_item ON group_items(item_id);
-- 外部成交：不在闲鱼成交的买卖（线下 / 其他平台 / 熟人）。
-- 只存在于联单内部，不写 items，所以绝不出现在明细列表、也不影响全局统计，
-- 但联单自己的销售额 / 货品成本 / 利润会把它算进去。
CREATE TABLE IF NOT EXISTS group_externals (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    group_id     INTEGER NOT NULL,
    shop_id      INTEGER NOT NULL DEFAULT 1,
    kind         TEXT NOT NULL DEFAULT 'sell',  -- sell=外部卖出 buy=外部买入
    product      TEXT NOT NULL DEFAULT '',
    amount       REAL NOT NULL DEFAULT 0,        -- 整单总价
    quantity     INTEGER NOT NULL DEFAULT 1,     -- 件数，仅记录
    counterparty TEXT NOT NULL DEFAULT '',       -- 买家/卖家 / 渠道
    deal_time    TEXT NOT NULL DEFAULT '',
    note         TEXT NOT NULL DEFAULT '',
    source       TEXT NOT NULL DEFAULT '',       -- 货物来源：自采 / 回收 / 寄卖 / 闲鱼
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_group_externals_group ON group_externals(group_id);
`
	if _, err := db.Exec(q); err != nil {
		return err
	}
	// 旧库升级：group_externals 是后加的表，老库没有 source 列。幂等补列。
	var srcCols int
	db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('group_externals') WHERE name='source'").Scan(&srcCols)
	if srcCols == 0 {
		if _, err := db.Exec("ALTER TABLE group_externals ADD COLUMN source TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	return nil
}

func computeGroupStats(items []Item, exts []GroupExternal) GroupStats {
	var s GroupStats
	alloc := groupAllocForItems(items)
	for _, it := range items {
		feeAll := it.SellFee + it.SellWuyouFee
		s.Shipping += it.BuyShipping + it.SellShipping
		switch it.Kind {
		case "purchase":
			s.BuyCount++
			s.Goods += it.BuyPrice
			s.UnsoldCost += it.BuyPrice + it.BuyShipping
		case "sale":
			s.SellCount++
			s.Sales += it.SellPrice
			s.Net += it.SellNet
			s.Fee += feeAll
			s.WuyouFee += it.SellWuyouFee
			if !it.CostFree {
				qty := it.Quantity
				if qty < 1 {
					qty = 1
				}
				if it.UnitCost > 0 {
					s.UnsoldCost -= round2(it.UnitCost * float64(qty))
				} else if unit, ok := alloc[it.ID]; ok {
					s.UnsoldCost -= round2(unit * float64(qty))
				}
			}
		case "standalone":
			// 独立完整记录：买 / 卖两侧都在一条里
			s.BuyCount++
			s.SellCount++
			s.Goods += it.BuyPrice
			s.Sales += it.SellPrice
			s.Net += it.SellNet
			s.Fee += feeAll
			s.WuyouFee += it.SellWuyouFee
		}
	}
	// 外部成交没有平台服务费也没有运费，只在销售额 / 货品成本里各记一笔
	for _, e := range exts {
		if e.Kind == "buy" {
			s.BuyCount++
			s.Goods += e.Amount
			s.UnsoldCost += e.Amount
		} else {
			s.SellCount++
			s.Sales += e.Amount
			s.Net += e.Amount
		}
	}
	if s.UnsoldCost < 0 {
		s.UnsoldCost = 0
	}
	s.Sales = round2(s.Sales)
	s.Net = round2(s.Net)
	s.Goods = round2(s.Goods)
	s.Shipping = round2(s.Shipping)
	s.Fee = round2(s.Fee)
	s.WuyouFee = round2(s.WuyouFee)
	s.UnsoldCost = round2(s.UnsoldCost)
	s.Profit = round2(s.Sales - s.Goods - s.Shipping - s.Fee)
	if s.Sales > 0 {
		s.ProfitRate = round2(s.Profit / s.Sales * 100)
	}
	return s
}

// loadGroupItems 取联单内商品。带 shop_id 是最后一道防线：
// 若历史脏数据把别家商品挂进了本店联单，也不会在此泄漏出来。
func loadGroupItems(groupID int64, shopID int64) ([]Item, []int64, error) {
	rows, err := db.Query(`SELECT `+itemCols+` FROM items
        WHERE id IN (SELECT item_id FROM group_items WHERE group_id=?) AND shop_id=?
        ORDER BY COALESCE(NULLIF(sell_time,''), NULLIF(buy_time,''), '') DESC, id DESC`, groupID, shopID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	items := []Item{}
	ids := []int64{}
	for rows.Next() {
		it, e := scanItem(rows)
		if e == nil {
			items = append(items, it)
			ids = append(ids, it.ID)
		}
	}
	return items, ids, nil
}

func hydrateGroup(g *LinkGroup, shopID int64) {
	items, ids, err := loadGroupItems(g.ID, shopID)
	if err != nil {
		return
	}
	g.Items = items
	g.ItemIDs = ids
	g.Externals = loadGroupExternals(g.ID, shopID)
	g.Stats = computeGroupStats(items, g.Externals)
}

// fillGroupItemIDs 一次查询把本店所有联单的成员 id 填好（brief 模式用）。
// 逐单查询是 N+1：50 个联单就是 50 次往返，而这里恒定 1 次。
func fillGroupItemIDs(groups []LinkGroup, shopID int64) {
	if len(groups) == 0 {
		return
	}
	ix := make(map[int64]int, len(groups))
	for i := range groups {
		ix[groups[i].ID] = i
	}
	rows, err := db.Query(`SELECT gi.group_id, gi.item_id FROM group_items gi
        JOIN link_groups g ON g.id=gi.group_id WHERE g.shop_id=? ORDER BY gi.item_id`, shopID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var gid, iid int64
		if rows.Scan(&gid, &iid) != nil {
			continue
		}
		if i, ok := ix[gid]; ok {
			groups[i].ItemIDs = append(groups[i].ItemIDs, iid)
		}
	}
}

// GET /api/groups  列表（含统计）  POST /api/groups 创建
// ?brief=1 只返回 id / 标题 / 完结状态 / 成员 id：明细页和概览只需要这些来打「联单」
// 标签，没必要把每个联单的商品明细和统计都算一遍（那是 N 次查询 + N 次统计）。
func handleGroups(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		brief := r.URL.Query().Get("brief") == "1"
		rows, err := db.Query("SELECT id, title, note, settled_at, created_at, updated_at FROM link_groups WHERE shop_id=? ORDER BY id DESC", shopID(r))
		if err != nil {
			writeError(w, 500, "查询联单失败")
			return
		}
		out := []LinkGroup{}
		for rows.Next() {
			var g LinkGroup
			if rows.Scan(&g.ID, &g.Title, &g.Note, &g.SettledAt, &g.CreatedAt, &g.UpdatedAt) == nil {
				out = append(out, g)
			}
		}
		rows.Close() // 必须先关闭再 hydrate：SQLite 单连接池，嵌套查询会死锁
		if brief {
			fillGroupItemIDs(out, shopID(r))
			writeJSON(w, map[string]interface{}{"items": out, "brief": true})
			return
		}
		for i := range out {
			hydrateGroup(&out[i], shopID(r))
		}
		writeJSON(w, map[string]interface{}{"items": out})
	case "POST":
		var in struct {
			Title     string          `json:"title"`
			Note      string          `json:"note"`
			ItemIDs   []int64         `json:"item_ids"`
			Externals []GroupExternal `json:"externals"`     // 外部卖出
			ExtBuys   []GroupExternal `json:"externals_buy"` // 外部买入
		}
		if !decodeJSONBody(w, r, &in, maxNormalJSONBody) {
			return
		}
		in.Title = strings.TrimSpace(in.Title)
		if in.Title == "" {
			writeError(w, 400, "联单标题不能为空")
			return
		}
		ids := uniqueIDs(in.ItemIDs)
		if len(ids) == 0 && len(in.Externals) == 0 && len(in.ExtBuys) == 0 {
			writeError(w, 400, "请至少选择一条买单或卖单")
			return
		}
		ts := nowStr()
		tx, err := db.Begin()
		if err != nil {
			writeError(w, 500, "开启事务失败")
			return
		}
		defer tx.Rollback()
		id, err := createGroupTx(tx, shopID(r), in.Title, in.Note, ts)
		if err != nil {
			writeError(w, 500, "创建失败: "+err.Error())
			return
		}
		// 外部成交只写 group_externals，不产生明细记录
		if err := saveGroupExternalsTx(tx, id, shopID(r), in.Externals, "sell"); err != nil {
			writeError(w, 500, "保存外部卖出失败: "+err.Error())
			return
		}
		if err := saveGroupExternalsTx(tx, id, shopID(r), in.ExtBuys, "buy"); err != nil {
			writeError(w, 500, "保存外部买入失败: "+err.Error())
			return
		}
		if err := setGroupItemsTx(tx, id, ids, shopID(r)); err != nil {
			writeError(w, 500, "关联记录失败: "+err.Error())
			return
		}
		if err := tx.Commit(); err != nil {
			writeError(w, 500, "提交失败: "+err.Error())
			return
		}
		mergeAfterMutation(shopID(r))
		id = resolveGroupAfterMerge(id, ids, shopID(r))
		var g LinkGroup
		db.QueryRow("SELECT id,title,note,settled_at,created_at,updated_at FROM link_groups WHERE id=? AND shop_id=?", id, shopID(r)).
			Scan(&g.ID, &g.Title, &g.Note, &g.SettledAt, &g.CreatedAt, &g.UpdatedAt)
		hydrateGroup(&g, shopID(r))
		writeJSON(w, g)
	default:
		writeError(w, 405, "method not allowed")
	}
}

func uniqueIDs(ids []int64) []int64 {
	seen := map[int64]bool{}
	out := []int64{}
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}

func ensureGroupInShopTx(tx *sql.Tx, groupID, shopID int64) error {
	var n int
	if err := tx.QueryRow("SELECT COUNT(*) FROM link_groups WHERE id=? AND shop_id=?", groupID, shopID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("联单 %d 不存在", groupID)
	}
	return nil
}

// setGroupItemsTx 在调用方事务内重建联单成员。
// 只允许关联本店记录，跨店 id 会被静默忽略。
func setGroupItemsTx(tx *sql.Tx, groupID int64, ids []int64, shopID int64) error {
	if err := ensureGroupInShopTx(tx, groupID, shopID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM group_items
        WHERE group_id=? AND group_id IN (SELECT id FROM link_groups WHERE shop_id=?)`,
		groupID, shopID); err != nil {
		return err
	}
	for _, id := range ids {
		// 只关联真实存在的记录，忽略脏 id
		var n int
		if err := tx.QueryRow("SELECT COUNT(*) FROM items WHERE id=? AND shop_id=?", id, shopID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			if _, err := tx.Exec("INSERT OR IGNORE INTO group_items(group_id, item_id) VALUES(?,?)", groupID, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func setGroupItems(groupID int64, ids []int64, shopID int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setGroupItemsTx(tx, groupID, ids, shopID); err != nil {
		return err
	}
	return tx.Commit()
}

// GroupExternal 联单里手工补的「外部成交」：不在闲鱼成交、没有订单号，
// 但钱确实进出了（线下、其他平台、熟人转让都算）。
//
//	Kind=sell  外部卖出（进联单销售额）
//	Kind=buy   外部买入（进联单货品成本 / 在库占用）
//
// 关键点：它只存在 group_externals 表里，**不写 items**，
// 所以绝不出现在明细列表里，也不参与全局统计——只在本联单的账里生效。
type GroupExternal struct {
	ID           int64   `json:"id"`
	Kind         string  `json:"kind"`         // buy | sell
	Product      string  `json:"product"`      // 商品 / 说明
	Amount       float64 `json:"amount"`       // 整单总价（卖出=卖价，买入=买价）
	Quantity     int     `json:"quantity"`     // 件数，仅记录与展示，不参与金额计算
	Counterparty string  `json:"counterparty"` // 买家 / 卖家 / 渠道，选填
	Time         string  `json:"time"`         // 成交时间，空=当前时间
	Note         string  `json:"note"`         // 备注，选填
	Source       string  `json:"source"`       // 货物来源：自采 / 回收 / 寄卖 / 闲鱼
}

// 货物来源固定为这几种，空值按「未标注」处理，避免前端传进来的杂字符串进库。
// 兼容用户常说的「寄售 / 代买」，入库时统一成寄卖 / 代采，统计只认一套口径。
var validSources = map[string]bool{"自采": true, "回收": true, "寄卖": true, "代采": true, "闲鱼": true}

func normSource(v string) string {
	v = strings.TrimSpace(v)
	switch v {
	case "寄售":
		return "寄卖"
	case "代买":
		return "代采"
	}
	if validSources[v] {
		return v
	}
	return ""
}

// loadGroupExternals 取某联单的外部成交。必须带 shop_id：联单 id 是自增的，
// 只按 group_id 查在跨店场景下可能读到别家数据。
func loadGroupExternals(groupID, shopID int64) []GroupExternal {
	out := []GroupExternal{}
	rows, err := db.Query(`SELECT id, kind, product, amount, quantity, counterparty, deal_time, note, source
        FROM group_externals WHERE group_id=? AND shop_id=? ORDER BY deal_time, id`, groupID, shopID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var e GroupExternal
		if rows.Scan(&e.ID, &e.Kind, &e.Product, &e.Amount, &e.Quantity,
			&e.Counterparty, &e.Time, &e.Note, &e.Source) == nil {
			out = append(out, e)
		}
	}
	return out
}

// saveGroupExternals 用编辑器提交的列表整体替换某一 kind 的外部成交。
// 这些行只活在 group_externals 里：不写 items、不进明细、不参与全局统计，
// 所以「整体替换」是安全的——删掉的只是本联单自己的备注级数据，不会动到任何单据。
func saveGroupExternalsTx(tx *sql.Tx, groupID, sid int64, list []GroupExternal, kind string) error {
	if kind != "buy" {
		kind = "sell"
	}
	if _, err := tx.Exec("DELETE FROM group_externals WHERE group_id=? AND shop_id=? AND kind=?", groupID, sid, kind); err != nil {
		return err
	}
	ts := nowStr()
	for _, e := range list {
		product := strings.TrimSpace(e.Product)
		if product == "" || e.Amount <= 0 {
			continue // 半填的行直接忽略，前端已校验
		}
		qty := e.Quantity
		if qty < 1 {
			qty = 1
		}
		when := strings.TrimSpace(e.Time)
		if when == "" {
			when = ts
		}
		if _, err := tx.Exec(`INSERT INTO group_externals
            (group_id, shop_id, kind, product, amount, quantity, counterparty, deal_time, note, source, created_at, updated_at)
            VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			groupID, sid, kind, product, round2(e.Amount), qty,
			strings.TrimSpace(e.Counterparty), when, strings.TrimSpace(e.Note),
			normSource(e.Source), ts, ts); err != nil {
			return err
		}
	}
	return nil
}

func saveGroupExternals(groupID, sid int64, list []GroupExternal, kind string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := saveGroupExternalsTx(tx, groupID, sid, list, kind); err != nil {
		return err
	}
	return tx.Commit()
}

func createGroupTx(tx *sql.Tx, shopID int64, title, note, ts string) (int64, error) {
	res, err := tx.Exec("INSERT INTO link_groups(shop_id, title, note, created_at, updated_at) VALUES(?,?,?,?,?)",
		shopID, title, note, ts, ts)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// mergeGroupsTx 把一组联单并入 target。成员、外部成交、标题和旧联单删除
// 都在同一事务里完成，任何一步失败都会整体回滚。
func mergeGroupsTx(tx *sql.Tx, target int64, sources, ids []int64, shopID int64, newTitle string) error {
	if err := ensureGroupInShopTx(tx, target, shopID); err != nil {
		return err
	}
	for _, sourceID := range sources {
		if sourceID == target {
			continue
		}
		if err := ensureGroupInShopTx(tx, sourceID, shopID); err != nil {
			return err
		}
	}
	if err := setGroupItemsTx(tx, target, ids, shopID); err != nil {
		return err
	}
	for _, sourceID := range sources {
		if sourceID == target {
			continue
		}
		if _, err := tx.Exec("UPDATE group_externals SET group_id=? WHERE group_id=? AND shop_id=?", target, sourceID, shopID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM group_items
            WHERE group_id=? AND group_id IN (SELECT id FROM link_groups WHERE shop_id=?)`,
			sourceID, shopID); err != nil {
			return err
		}
		res, err := tx.Exec("DELETE FROM link_groups WHERE id=? AND shop_id=?", sourceID, shopID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return fmt.Errorf("联单 %d 删除失败", sourceID)
		}
	}
	ts := nowStr()
	var res sql.Result
	var err error
	if newTitle != "" {
		res, err = tx.Exec("UPDATE link_groups SET title=?, updated_at=? WHERE id=? AND shop_id=?", newTitle, ts, target, shopID)
	} else {
		res, err = tx.Exec("UPDATE link_groups SET updated_at=? WHERE id=? AND shop_id=?", ts, target, shopID)
	}
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return fmt.Errorf("联单 %d 更新失败", target)
	}
	return nil
}

// resolveGroupAfterMerge 自动合并可能把刚建/刚改的联单并进另一张，
// 这时原 id 已经不存在了。返回真正活下来的那个 id，避免前端拿着死 id 继续编辑
// （表现就是「保存后候选列表空了、点了没反应」）。
func resolveGroupAfterMerge(id int64, ids []int64, shop int64) int64 {
	var n int
	db.QueryRow("SELECT COUNT(*) FROM link_groups WHERE id=? AND shop_id=?", id, shop).Scan(&n)
	if n > 0 {
		return id
	}
	for _, iid := range ids {
		var gid int64
		if db.QueryRow(`SELECT gi.group_id FROM group_items gi
            JOIN link_groups g ON g.id=gi.group_id
            WHERE gi.item_id=? AND g.shop_id=? ORDER BY g.id LIMIT 1`, iid, shop).Scan(&gid) == nil {
			return gid
		}
	}
	return id
}

// /api/groups/{id}        GET 详情 / PUT 修改 / DELETE 删除
// /api/groups/{id}/settle POST 完结 / 重开（{settled:bool}，缺省则切换）
func handleGroupByID(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimPrefix(r.URL.Path, "/api/groups/")
	// 完结子路由
	if strings.HasSuffix(raw, "/settle") {
		idStr := strings.TrimSuffix(raw, "/settle")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeError(w, 400, "无效 id")
			return
		}
		handleGroupSettle(w, r, id)
		return
	}
	// 外部成交子路由：只存 / 只改这一块，不用整单保存
	if strings.HasSuffix(raw, "/externals") {
		idStr := strings.TrimSuffix(raw, "/externals")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeError(w, 400, "无效 id")
			return
		}
		handleGroupExternalsSave(w, r, id)
		return
	}
	// 加入成员子路由：明细里「加入联单」用它追加，不需要整单覆盖
	if strings.HasSuffix(raw, "/items") {
		idStr := strings.TrimSuffix(raw, "/items")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeError(w, 400, "无效 id")
			return
		}
		handleGroupItemsAdd(w, r, id)
		return
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		writeError(w, 400, "无效 id")
		return
	}
	// 已完结的联单只能「重新开启」后再改：改标题/增减成员/删除都会让已对账的统计变样
	if r.Method == "PUT" || r.Method == "DELETE" {
		var title, settledAt string
		if db.QueryRow("SELECT title, settled_at FROM link_groups WHERE id=? AND shop_id=?", id, shopID(r)).
			Scan(&title, &settledAt) == nil && settledAt != "" {
			action := "修改"
			if r.Method == "DELETE" {
				action = "删除"
			}
			writeSettledBlock(w, title, action)
			return
		}
	}
	switch r.Method {
	case "GET":
		var g LinkGroup
		if err := db.QueryRow("SELECT id,title,note,settled_at,created_at,updated_at FROM link_groups WHERE id=? AND shop_id=?", id, shopID(r)).
			Scan(&g.ID, &g.Title, &g.Note, &g.SettledAt, &g.CreatedAt, &g.UpdatedAt); err != nil {
			writeError(w, 404, "联单不存在")
			return
		}
		hydrateGroup(&g, shopID(r))
		writeJSON(w, g)
	case "PUT":
		var in struct {
			Title     string          `json:"title"`
			Note      string          `json:"note"`
			ItemIDs   []int64         `json:"item_ids"`
			Externals []GroupExternal `json:"externals"`     // 外部卖出
			ExtBuys   []GroupExternal `json:"externals_buy"` // 外部买入
		}
		if !decodeJSONBody(w, r, &in, maxNormalJSONBody) {
			return
		}
		in.Title = strings.TrimSpace(in.Title)
		if in.Title == "" {
			writeError(w, 400, "联单标题不能为空")
			return
		}
		ids := uniqueIDs(in.ItemIDs)
		if len(ids) == 0 && len(in.Externals) == 0 && len(in.ExtBuys) == 0 {
			writeError(w, 400, "请至少选择一条记录")
			return
		}
		tx, err := db.Begin()
		if err != nil {
			writeError(w, 500, "开启事务失败")
			return
		}
		defer tx.Rollback()
		if _, err := tx.Exec("UPDATE link_groups SET title=?, note=?, updated_at=? WHERE id=? AND shop_id=?",
			in.Title, in.Note, nowStr(), id, shopID(r)); err != nil {
			writeError(w, 500, "更新失败: "+err.Error())
			return
		}
		// 先同步外部成交（可能新增 / 修改 / 删除），再重建成员表
		if err := saveGroupExternalsTx(tx, id, shopID(r), in.Externals, "sell"); err != nil {
			writeError(w, 500, "保存外部卖出失败: "+err.Error())
			return
		}
		if err := saveGroupExternalsTx(tx, id, shopID(r), in.ExtBuys, "buy"); err != nil {
			writeError(w, 500, "保存外部买入失败: "+err.Error())
			return
		}
		if err := setGroupItemsTx(tx, id, ids, shopID(r)); err != nil {
			writeError(w, 500, "关联记录失败: "+err.Error())
			return
		}
		if err := tx.Commit(); err != nil {
			writeError(w, 500, "提交失败: "+err.Error())
			return
		}
		mergeAfterMutation(shopID(r))
		id = resolveGroupAfterMerge(id, ids, shopID(r))
		var g LinkGroup
		db.QueryRow("SELECT id,title,note,settled_at,created_at,updated_at FROM link_groups WHERE id=? AND shop_id=?", id, shopID(r)).
			Scan(&g.ID, &g.Title, &g.Note, &g.SettledAt, &g.CreatedAt, &g.UpdatedAt)
		hydrateGroup(&g, shopID(r))
		writeJSON(w, g)
	case "DELETE":
		// 删除前取快照（独立查询，须在开启事务前完成，避免连接池里挂着 rows）
		token := snapshotGroupUndo("delete_group", id, shopID(r))
		tx, err := db.Begin()
		if err != nil {
			writeError(w, 500, "开启事务失败")
			return
		}
		if _, err := tx.Exec(`DELETE FROM group_items
            WHERE group_id=? AND group_id IN (SELECT id FROM link_groups WHERE shop_id=?)`,
			id, shopID(r)); err != nil {
			tx.Rollback()
			writeError(w, 500, "删除关联失败")
			return
		}
		// 外部成交只属于这个联单，联单一并带走
		if _, err := tx.Exec("DELETE FROM group_externals WHERE group_id=? AND shop_id=?", id, shopID(r)); err != nil {
			tx.Rollback()
			writeError(w, 500, "删除外部成交失败")
			return
		}
		if _, err := tx.Exec("DELETE FROM link_groups WHERE id=? AND shop_id=?", id, shopID(r)); err != nil {
			tx.Rollback()
			writeError(w, 500, "删除失败")
			return
		}
		if err := tx.Commit(); err != nil {
			writeError(w, 500, "提交失败")
			return
		}
		out := map[string]interface{}{"ok": true}
		if token != "" {
			out["undo_token"] = token
		}
		writeJSON(w, out)
	default:
		writeError(w, 405, "method not allowed")
	}
}

// POST /api/groups/{id}/externals  {kind:"sell"|"buy", items:[...]}
// 联单编辑器里「外部成交」两块的独立保存：只替换这一块，
// 不动标题、不动成员、也不动另一块。用户填完就能立刻落库，不必等整单保存。
func handleGroupExternalsSave(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	var in struct {
		Kind  string          `json:"kind"`
		Items []GroupExternal `json:"items"`
	}
	if !decodeJSONBody(w, r, &in, maxNormalJSONBody) {
		return
	}
	kind := "sell"
	if in.Kind == "buy" {
		kind = "buy"
	}
	var title, settledAt string
	if err := db.QueryRow("SELECT title, settled_at FROM link_groups WHERE id=? AND shop_id=?", id, shopID(r)).
		Scan(&title, &settledAt); err != nil {
		writeError(w, 404, "联单不存在")
		return
	}
	if settledAt != "" {
		writeSettledBlock(w, title, "修改")
		return
	}
	if err := saveGroupExternals(id, shopID(r), in.Items, kind); err != nil {
		writeError(w, 500, "保存失败: "+err.Error())
		return
	}
	var g LinkGroup
	db.QueryRow("SELECT id,title,note,settled_at,created_at,updated_at FROM link_groups WHERE id=? AND shop_id=?", id, shopID(r)).
		Scan(&g.ID, &g.Title, &g.Note, &g.SettledAt, &g.CreatedAt, &g.UpdatedAt)
	hydrateGroup(&g, shopID(r))
	writeJSON(w, g)
}

// POST /api/groups/{id}/items  {item_ids:[...]}：把记录追加进联单。
// 明细页的「加入联单」用它，幂等：已经在里面就跳过，不会覆盖别人刚改的成员。
func handleGroupItemsAdd(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	var in struct {
		ItemIDs []int64 `json:"item_ids"`
	}
	if !decodeJSONBody(w, r, &in, maxNormalJSONBody) {
		return
	}
	ids := uniqueIDs(in.ItemIDs)
	if len(ids) == 0 {
		writeError(w, 400, "请选择要加入的记录")
		return
	}
	var title, settledAt string
	if err := db.QueryRow("SELECT title, settled_at FROM link_groups WHERE id=? AND shop_id=?", id, shopID(r)).
		Scan(&title, &settledAt); err != nil {
		writeError(w, 404, "联单不存在")
		return
	}
	if settledAt != "" {
		writeSettledBlock(w, title, "加入记录")
		return
	}
	added := 0
	cleared := 0 // 原本单独完结、被转入本联单的记录数
	for _, iid := range ids {
		// 一条记录只能属于一张联单：已在别处的，明确告知而不是悄悄合并
		var otherID int64
		var otherTitle string
		if db.QueryRow(`SELECT g.id, g.title FROM group_items gi
            JOIN link_groups g ON g.id=gi.group_id
            WHERE gi.item_id=? AND g.shop_id=? AND g.id<>? LIMIT 1`,
			iid, shopID(r), id).Scan(&otherID, &otherTitle) == nil {
			writeError(w, 400, "「"+otherTitle+"」里已经有这条记录了，请先在那边移出再加入")
			return
		}
		var product, discarded, settled string
		if err := db.QueryRow("SELECT product, COALESCE(discarded_at,''), COALESCE(settled_at,'') FROM items WHERE id=? AND shop_id=?",
			iid, shopID(r)).Scan(&product, &discarded, &settled); err != nil {
			continue // 不存在或别家店铺：静默跳过，不泄漏
		}
		if discarded != "" {
			writeError(w, 400, "「"+product+"」已弃单，不能加入联单")
			return
		}
		// 已单独完结的记录也能加进联单：加进来之后完结状态改由联单决定，
		// 所以把单品完结时间清掉，避免「既单独完结、又在联单里」两套口径打架。
		if settled != "" {
			if _, err := db.Exec("UPDATE items SET settled_at='', updated_at=? WHERE id=? AND shop_id=?",
				nowStr(), iid, shopID(r)); err != nil {
				writeError(w, 500, "「"+product+"」转入联单失败")
				return
			}
			cleared++
		}
		res, err := db.Exec("INSERT OR IGNORE INTO group_items(group_id, item_id) VALUES(?,?)", id, iid)
		if err != nil {
			writeError(w, 500, "加入失败: "+err.Error())
			return
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
		}
	}
	if added > 0 {
		db.Exec("UPDATE link_groups SET updated_at=? WHERE id=? AND shop_id=?", nowStr(), id, shopID(r))
		mergeAfterMutation(shopID(r))
	}
	var g LinkGroup
	db.QueryRow("SELECT id,title,note,settled_at,created_at,updated_at FROM link_groups WHERE id=? AND shop_id=?", id, shopID(r)).
		Scan(&g.ID, &g.Title, &g.Note, &g.SettledAt, &g.CreatedAt, &g.UpdatedAt)
	hydrateGroup(&g, shopID(r))
	writeJSON(w, map[string]interface{}{"ok": true, "added": added, "cleared": cleared, "group": g})
}

// /api/groups/candidates：联单选记录的候选池。
// q=关键词（搜索商品/订单号/昵称）；similar=标题（按内存索引做相似度推荐）；
// side=buy|sell 过滤。返回轻量字段，前端多选秒开。
func handleGroupCandidates(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	similar := strings.TrimSpace(r.URL.Query().Get("similar"))
	side := r.URL.Query().Get("side")
	limit := 60

	// 已经进了任何联单的明细都不再是候选——不管那张联单有没有完结。
	// 理由：一条记录被两张联单同时引用，成本会被算两次，用户明确要求「候选里不允许出现」。
	// 编辑某张联单时，它自己的成员由前端补回候选列表（标「已选」），不受这里影响。
	// 先关闭派生集合查询，再打开主候选查询，避免高并发下连接池互锁。
	grouped := map[int64]bool{}
	if sr, e := db.Query(`SELECT DISTINCT gi.item_id FROM group_items gi
        JOIN link_groups g ON g.id=gi.group_id WHERE g.shop_id=?`, shopID(r)); e == nil {
		for sr.Next() {
			var iid int64
			if sr.Scan(&iid) == nil {
				grouped[iid] = true
			}
		}
		sr.Close()
	}
	rows, err := db.Query(`SELECT `+itemCols+` FROM items WHERE shop_id=? ORDER BY
        COALESCE(NULLIF(sell_time,''), NULLIF(buy_time,''), '') DESC, id DESC`, shopID(r))
	if err != nil {
		writeError(w, 500, "查询候选失败")
		return
	}
	defer rows.Close()
	type cand struct {
		Item   Item    `json:"item"`
		Score  float64 `json:"score"`
		Reason string  `json:"reason"`
	}
	out := []cand{}
	nq := normProduct(similar)
	qlow := strings.ToLower(q)
	for rows.Next() {
		it, e := scanItem(rows)
		if e != nil {
			continue
		}
		// 弃单、已经在联单里的不再当候选。
		// 单独完结的允许进候选：加进联单时会把「单品完结」交还给联单，不冲突。
		if it.DiscardedAt != "" || grouped[it.ID] {
			continue
		}
		role := "sell"
		if it.Kind == "purchase" {
			role = "buy"
		} else if it.Kind == "standalone" {
			role = "both"
		}
		if side == "buy" && role == "sell" {
			continue
		}
		if side == "sell" && role == "buy" {
			continue
		}
		score := 0.0
		reason := ""
		if q != "" {
			hay := strings.ToLower(strings.Join([]string{it.Product, it.BuyOrderNo, it.SellOrderNo,
				it.BuyCounterparty, it.SellCounterparty, it.Note}, " "))
			if strings.Contains(hay, qlow) {
				score = 100
				reason = "搜索命中"
			} else {
				continue
			}
		} else if similar != "" {
			np := itemProductNorm(it)
			if np == nq && nq != "" {
				score = 100
				reason = "同名"
			} else {
				score = similarity(nq, np) * 100
				if score < 55 {
					continue
				}
				reason = "相似推荐"
			}
		} else {
			score = 50
			reason = "最近录入"
		}
		out = append(out, cand{it, round2(score), reason})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	writeJSON(w, map[string]interface{}{"items": out})
}

var _ = sql.ErrNoRows

// /api/groups/by-item/{id}：返回包含该记录的所有联单（联动显示用）
func handleGroupsByItem(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/groups/by-item/"), 10, 64)
	if err != nil {
		writeError(w, 400, "无效 id")
		return
	}
	rows, err := db.Query(`SELECT g.id,g.title,g.note,g.settled_at,g.created_at,g.updated_at
        FROM link_groups g JOIN group_items gi ON gi.group_id=g.id
        WHERE gi.item_id=? AND g.shop_id=? ORDER BY g.id DESC`, id, shopID(r))
	if err != nil {
		writeError(w, 500, "查询失败")
		return
	}
	out := []LinkGroup{}
	for rows.Next() {
		var g LinkGroup
		if rows.Scan(&g.ID, &g.Title, &g.Note, &g.SettledAt, &g.CreatedAt, &g.UpdatedAt) == nil {
			out = append(out, g)
		}
	}
	rows.Close() // 先关闭外层查询，避免单连接池嵌套查询死锁
	for i := range out {
		_, ids, _ := loadGroupItems(out[i].ID, shopID(r))
		out[i].ItemIDs = ids
	}
	writeJSON(w, map[string]interface{}{"items": out})
}

// /api/groups/{id}/settle：完结 / 重开联单。
// 完结只锁定联单对账；全局在库成本始终按卖单实际带走的成本冲减。
// 入参 {settled:bool} 缺省则按当前状态取反。
func handleGroupSettle(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	var in struct {
		Settled bool `json:"settled"`
	}
	if !decodeOptionalJSONBody(w, r, &in, maxTinyJSONBody) {
		return
	}
	var cur string
	if err := db.QueryRow("SELECT settled_at FROM link_groups WHERE id=? AND shop_id=?", id, shopID(r)).Scan(&cur); err != nil {
		writeError(w, 404, "联单不存在")
		return
	}
	// 若未显式传 settled（false 且非意图），取反当前状态
	newSettled := in.Settled
	if !in.Settled && cur == "" {
		// 前端未传 true，且当前未完结 → 默认意图是「完结」
		newSettled = true
	}
	val := ""
	if newSettled {
		val = nowStr()
	}
	if _, err := db.Exec("UPDATE link_groups SET settled_at=?, updated_at=? WHERE id=? AND shop_id=?",
		val, nowStr(), id, shopID(r)); err != nil {
		writeError(w, 500, "更新失败")
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "settled": newSettled, "settled_at": val})
}

// ensureSaleGroup 为卖出单建立 / 复用联单，并把卖出单与其关联的买进单都挂进去。
// 修正点：若一个关联买进单已经属于某个联单，则把本次卖出单并入“该买进单所在的联单”，
// 而非另建新联单——避免同一个买单被拆进多个联单、成本被重复计入（用户反馈“联单重复”）。
// 多个候选联单（卖单原属 A、买单原属 B）会合并为同一个，删除多余的。
// autoGroupForStandalone 手动「两侧一起填」存成一条独立记录后，自动给它建一张联单，
// 标题取商品名。用户要求这一步不用再手动去联单页点一次。
// 标题重名时加序号，避免一堆同名联单分不清。
func autoGroupForStandalone(itemID int64, product string, sid int64) int64 {
	if itemID <= 0 {
		return 0
	}
	base := strings.TrimSpace(product)
	if base == "" {
		base = "未命名联单"
	}
	title := base
	for i := 2; i <= 99; i++ {
		var n int
		db.QueryRow("SELECT COUNT(*) FROM link_groups WHERE shop_id=? AND title=?", sid, title).Scan(&n)
		if n == 0 {
			break
		}
		title = fmt.Sprintf("%s (%d)", base, i)
	}
	ts := nowStr()
	tx, err := db.Begin()
	if err != nil {
		return 0
	}
	defer tx.Rollback()
	gid, err := createGroupTx(tx, sid, title, "", ts)
	if err != nil {
		return 0
	}
	if err := setGroupItemsTx(tx, gid, []int64{itemID}, sid); err != nil {
		return 0
	}
	if err := tx.Commit(); err != nil {
		return 0
	}
	return gid
}

func ensureSaleGroup(saleID int64, purchaseIDs []int64, title string, sid int64) (int64, error) {
	if saleID <= 0 || len(purchaseIDs) == 0 {
		return 0, nil
	}
	title = strings.TrimSpace(title)
	if title == "" {
		var p string
		if err := db.QueryRow("SELECT product FROM items WHERE id=? AND shop_id=?", saleID, sid).Scan(&p); err == nil {
			title = strings.TrimSpace(p)
		}
		if title == "" {
			title = "联动单"
		}
	}

	// 候选联单：包含本卖单，或包含任一关联买单
	candGroups, err := collectCandidateGroups(saleID, purchaseIDs, sid)
	if err != nil {
		return 0, err
	}

	var target int64
	if len(candGroups) > 0 {
		// 主联单优先选“包含关联买单”的那个（买本是锚点），否则取卖单所在的
		target = pickAnchorGroup(candGroups, purchaseIDs, sid)
		// 合并：收集所有候选联单成员 + 卖单 + 关联买单，去重后写入主联单
		all := append([]int64{saleID}, purchaseIDs...)
		for _, g := range candGroups {
			if ids, e := loadGroupItemIDs(g, sid); e == nil {
				all = append(all, ids...)
			} else {
				return 0, e
			}
		}
		all = uniqueIDs(all)
		tx, err := db.Begin()
		if err != nil {
			return 0, err
		}
		defer tx.Rollback()
		if err := mergeGroupsTx(tx, target, candGroups, all, sid, title); err != nil {
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
	} else {
		ts := nowStr()
		tx, err := db.Begin()
		if err != nil {
			return 0, err
		}
		defer tx.Rollback()
		target, err = createGroupTx(tx, sid, title, "", ts)
		if err != nil {
			return 0, err
		}
		ids := uniqueIDs(append([]int64{saleID}, purchaseIDs...))
		if err := setGroupItemsTx(tx, target, ids, sid); err != nil {
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
	}
	return target, nil
}

// collectCandidateGroups 返回包含给定卖单或任一关联买单的所有联单 id（去重、升序）
func collectCandidateGroups(saleID int64, purchaseIDs []int64, shopID int64) ([]int64, error) {
	set := map[int64]bool{}
	for _, id := range append([]int64{saleID}, purchaseIDs...) {
		rows, err := db.Query(`SELECT gi.group_id FROM group_items gi
            JOIN link_groups g ON g.id=gi.group_id WHERE gi.item_id=? AND g.shop_id=?`, id, shopID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var gid int64
			if rows.Scan(&gid) == nil {
				set[gid] = true
			}
		}
		rows.Close()
	}
	out := []int64{}
	for gid := range set {
		out = append(out, gid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// pickAnchorGroup 在候选联单中优先选“包含关联买单”的那个作为主联单，否则取第一个
func pickAnchorGroup(candGroups []int64, purchaseIDs []int64, shopID int64) int64 {
	pSet := map[int64]bool{}
	for _, p := range purchaseIDs {
		pSet[p] = true
	}
	for _, g := range candGroups {
		if ids, err := loadGroupItemIDs(g, shopID); err == nil {
			for _, id := range ids {
				if pSet[id] {
					return g
				}
			}
		}
	}
	return candGroups[0]
}

// loadGroupItemIDs 返回联单内的记录 id 列表
func loadGroupItemIDs(groupID int64, shopID int64) ([]int64, error) {
	rows, err := db.Query(`SELECT gi.item_id FROM group_items gi
        JOIN items i ON i.id=gi.item_id WHERE gi.group_id=? AND i.shop_id=?`, groupID, shopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// /api/groups/link-sale：卖出单保存后建立 / 更新与多个买进单的联动。
// 只选中 1 个买进单时仍沿用 parent_id 成本分摊（在前端处理），这里负责 N:M 分组。
func handleLinkSale(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	var in struct {
		SaleID      int64   `json:"sale_id"`
		PurchaseIDs []int64 `json:"purchase_ids"`
		Title       string  `json:"title"`
	}
	if !decodeJSONBody(w, r, &in, maxNormalJSONBody) {
		return
	}
	if in.SaleID <= 0 || len(in.PurchaseIDs) == 0 {
		writeJSON(w, map[string]interface{}{"ok": true, "skipped": true})
		return
	}
	gid, err := ensureSaleGroup(in.SaleID, in.PurchaseIDs, in.Title, shopID(r))
	if err != nil {
		writeError(w, 500, "关联失败: "+err.Error())
		return
	}
	mergeAfterMutation(shopID(r))
	writeJSON(w, map[string]interface{}{"ok": true, "group_id": gid})
}

// mergeAfterMutation 在联单发生变更后自动合并“共享同一条记录”的重复联单，
// 使“合并联单”全自动化：后续买卖单一旦关联到某联单，就会自动并入，无需手动点按钮。
func mergeAfterMutation(shopID int64) {
	if _, err := autoMergeDuplicateGroups(shopID); err != nil {
		// 自动合并是“尽力而为”的安全网，失败不影响主流程
	}
}

// POST /api/groups/auto-merge：把“共享同一条记录（尤其是同一买单）”的多个联单
// 自动合并为一个，消除“同一买单被拆进多个联单、成本重复计入”的重复联单。
func handleAutoMerge(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	merged, err := autoMergeDuplicateGroups(shopID(r))
	if err != nil {
		writeError(w, 500, "合并失败: "+err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "merged_groups": merged})
}

// autoMergeDuplicateGroups 用并查集找出“共享同一条记录”的联单集合，合并为同一联单。
// 主联单取集合中最小的 id，其余联单删除（成员并入主联单）。
func autoMergeDuplicateGroups(sid int64) (int, error) {
	// 只处理本店联单：跨店并把两家的联单合在一起，会让成本重复计入且无法拆回。
	rows, err := db.Query(`SELECT gi.group_id, gi.item_id FROM group_items gi
        JOIN link_groups g ON g.id=gi.group_id WHERE g.shop_id=?`, sid)
	if err != nil {
		return 0, err
	}
	type pair struct{ g, it int64 }
	var pairs []pair
	for rows.Next() {
		var g, it int64
		if rows.Scan(&g, &it) == nil {
			pairs = append(pairs, pair{g, it})
		}
	}
	rows.Close()

	parent := map[int64]int64{}
	var find func(int64) int64
	find = func(x int64) int64 {
		if p, ok := parent[x]; !ok || p == x {
			parent[x] = x
			return x
		}
		parent[x] = find(parent[x])
		return parent[x]
	}
	union := func(a, b int64) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	// 确保每个出现的联单都是并查集节点
	for _, p := range pairs {
		find(p.g)
	}
	// 同一记录所属的多个联单合并
	itemGroups := map[int64][]int64{}
	for _, p := range pairs {
		itemGroups[p.it] = append(itemGroups[p.it], p.g)
	}
	for _, gs := range itemGroups {
		for i := 1; i < len(gs); i++ {
			union(gs[0], gs[i])
		}
	}
	// 按根分组
	comp := map[int64][]int64{}
	for g := range parent {
		root := find(g)
		comp[root] = append(comp[root], g)
	}
	merged := 0
	for _, members := range comp {
		if len(members) <= 1 {
			continue
		}
		target := members[0]
		for _, m := range members {
			if m < target {
				target = m
			}
		}
		all := []int64{}
		for _, m := range members {
			if ids, e := loadGroupItemIDs(m, sid); e == nil {
				all = append(all, ids...)
			} else {
				return merged, e
			}
		}
		all = uniqueIDs(all)
		tx, err := db.Begin()
		if err != nil {
			return merged, err
		}
		if err := mergeGroupsTx(tx, target, members, all, sid, ""); err != nil {
			tx.Rollback()
			return merged, err
		}
		if err := tx.Commit(); err != nil {
			return merged, err
		}
		for _, m := range members {
			if m != target {
				merged++
			}
		}
	}
	return merged, nil
}

// sqlArgsIn 构造 `prefix IN (?,?,...)` 子句及其参数（SQLite 占位符）
func sqlArgsIn(prefix string, ids []interface{}) (string, []interface{}) {
	ph := make([]string, len(ids))
	for i := range ids {
		ph[i] = "?"
	}
	return prefix + " IN (" + strings.Join(ph, ",") + ")", ids
}

// settledGroupOf 返回该记录所属「已完结」联单的 id 与标题；0 表示未完结或不在联单里。
// 完结单是已经对过账的快照，直接改里面的单据会让联单统计和账面脱节，
// 所以编辑/删除前必须先回到「联单」页把联单重新开启。
func settledGroupOf(itemID, shopID int64) (int64, string) {
	var id int64
	var title string
	err := db.QueryRow(`SELECT g.id, g.title FROM group_items gi
        JOIN link_groups g ON g.id=gi.group_id
        WHERE gi.item_id=? AND g.shop_id=? AND g.settled_at<>'' LIMIT 1`,
		itemID, shopID).Scan(&id, &title)
	if err != nil {
		return 0, ""
	}
	return id, title
}

// settledBlockMsg 拼出统一的中文提示。
func settledBlockMsg(title, action string) string {
	if title == "" {
		title = "未命名联单"
	}
	return "「" + title + "」是已完结联单，" + action + "前请先在「联单」页把它重新开启"
}
