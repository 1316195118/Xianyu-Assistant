package main

import (
	"database/sql"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var db *sql.DB

func initDB() error {
	path := os.Getenv("DB_PATH")
	if path == "" {
		path = "xianyu.db"
	}
	// busy_timeout 用 DSN 设置，保证连接池里每条连接都生效（Exec 只作用于单条连接）。
	// 不用 WAL：部署脚本直接复制 .db 做备份，WAL 下未 checkpoint 的数据会漏。
	//
	// 另外三项是纯读性能参数，不改变数据落盘方式，不碰备份语义：
	//   cache_size  —— 页缓存 16MB（默认 2MB），列表/统计反复扫同一批页时省掉系统调用
	//   mmap_size   —— 64MB 内存映射，读走内存映射而不是 read()，大表扫描明显更快
	//   temp_store  —— 排序/分组/临时表放内存，趋势聚合与联单统计不必落临时文件
	dsn := path
	if !strings.Contains(dsn, "?") {
		dsn += "?_pragma=busy_timeout(5000)" +
			"&_pragma=cache_size(-16000)" +
			"&_pragma=mmap_size(67108864)" +
			"&_pragma=temp_store(memory)"
	}
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	// 少量并发连接：单连接时任何「rows 迭代中又发起查询」都会把整个服务拖死。
	d.SetMaxOpenConns(4)
	d.SetMaxIdleConns(4)
	db = d
	if err := createSchema(); err != nil {
		return err
	}
	return migrateLegacy()
}

func createSchema() error {
	q := `
-- 店铺（一个闲鱼账号 = 一个店铺）。数据按 shop_id 逻辑隔离：
-- 明细 / 联单 / 撤销栈 归属店铺，密码、识别引擎、运费标签等配置全局共享。
CREATE TABLE IF NOT EXISTS shops (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL DEFAULT '',
    color       TEXT NOT NULL DEFAULT '#f5b400',
    sort        INTEGER NOT NULL DEFAULT 1,
    note        TEXT NOT NULL DEFAULT '',
    archived_at TEXT NOT NULL DEFAULT '',   -- 非空=已归档，归档后不在切换器出现，数据保留
    password_hash TEXT NOT NULL DEFAULT '', -- 该店铺独立密码（scrypt）；空=无密码可直接进入
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);
-- 一件货 / 一个买进单 一条记录
-- parent_id=0：买进单（整批货）或独立完整记录
-- parent_id>0：卖出单，挂到对应买进单，一个买进单可挂 N 个卖出单
CREATE TABLE IF NOT EXISTS items (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    shop_id           INTEGER NOT NULL DEFAULT 1, -- 所属店铺，所有查询必须带此条件
    product           TEXT NOT NULL DEFAULT '',   -- 商品名（自动配对键）
    product_norm      TEXT NOT NULL DEFAULT '',   -- 归一化商品名（去空格/标点/小写），建索引加速匹配
    parent_id         INTEGER NOT NULL DEFAULT 0, -- 所属买进单 id，0=买进单/独立单
    quantity          INTEGER NOT NULL DEFAULT 1, -- 买进单包含的商品件数
    unit_cost         REAL NOT NULL DEFAULT 0,    -- 卖出单的单件成本（自动带出，可改）

    buy_price         REAL NOT NULL DEFAULT 0,    -- 买进价：买进单为整批总价
    buy_shipping      REAL NOT NULL DEFAULT 0,    -- 买进运费
    buy_ship_tag      TEXT NOT NULL DEFAULT '',   -- 买进运费标签
    buy_order_no      TEXT NOT NULL DEFAULT '',
    buy_time          TEXT NOT NULL DEFAULT '',
    buy_counterparty  TEXT NOT NULL DEFAULT '',   -- 卖家昵称

    sell_price        REAL NOT NULL DEFAULT 0,    -- 卖出成交价
    sell_fee          REAL NOT NULL DEFAULT 0,    -- 软件服务费（手续费）
    sell_wuyou_fee    REAL NOT NULL DEFAULT 0,    -- 无忧卖类目软件服务费
    sell_net          REAL NOT NULL DEFAULT 0,    -- 卖出到手价（已扣上述两项服务费）
    sell_shipping     REAL NOT NULL DEFAULT 0,    -- 卖出运费
    sell_ship_tag     TEXT NOT NULL DEFAULT '',   -- 卖出运费标签
    sell_order_no     TEXT NOT NULL DEFAULT '',
    sell_time         TEXT NOT NULL DEFAULT '',
    sell_counterparty TEXT NOT NULL DEFAULT '',   -- 买家昵称

    note              TEXT NOT NULL DEFAULT '',
    source            TEXT NOT NULL DEFAULT '',   -- 货物来源：自采 / 回收 / 寄卖 / 闲鱼（空=未标注）
    cost_free         INTEGER NOT NULL DEFAULT 0, -- 1=用户确认这单没有货品成本（代卖/赠送），不再提示挂成本
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_items_product ON items(product);
CREATE INDEX IF NOT EXISTS idx_items_sell_time ON items(sell_time);
CREATE INDEX IF NOT EXISTS idx_items_buy_time ON items(buy_time);

-- 运费标签（可自定义新增）
CREATE TABLE IF NOT EXISTS ship_tags (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL
);

-- 应用设置（识别 API 等），key-value
CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);
`
	if _, err := db.Exec(q); err != nil {
		return err
	}
	// 旧库升级：幂等补列（SQLite 不支持 IF NOT EXISTS 的 ADD COLUMN）。
	// items 的相关列在此补；link_groups 的补列在 createGroupTables() 之后处理。
	for _, col := range []struct{ name, ddl string }{
		{"shop_id", "ALTER TABLE items ADD COLUMN shop_id INTEGER NOT NULL DEFAULT 1"},
		{"parent_id", "ALTER TABLE items ADD COLUMN parent_id INTEGER NOT NULL DEFAULT 0"},
		{"quantity", "ALTER TABLE items ADD COLUMN quantity INTEGER NOT NULL DEFAULT 1"},
		{"unit_cost", "ALTER TABLE items ADD COLUMN unit_cost REAL NOT NULL DEFAULT 0"},
		{"sell_wuyou_fee", "ALTER TABLE items ADD COLUMN sell_wuyou_fee REAL NOT NULL DEFAULT 0"},
		{"product_norm", "ALTER TABLE items ADD COLUMN product_norm TEXT NOT NULL DEFAULT ''"},
		// 附件列放到主 schema 里补：itemCols 现在会选它，
		// 若等 extras 初始化才建，任何更早的查询都会因列不存在而失败。
		{"attachment", "ALTER TABLE items ADD COLUMN attachment TEXT NOT NULL DEFAULT ''"},
		// 单商品完结：非空=这条单据单独完结（不组成联单），不再计入在库、也不出现在联单候选里。
		{"settled_at", "ALTER TABLE items ADD COLUMN settled_at TEXT NOT NULL DEFAULT ''"},
		// 弃单：非空=这单作废。记录保留可查，但不占在库、不计成本、不计利润率。
		{"discarded_at", "ALTER TABLE items ADD COLUMN discarded_at TEXT NOT NULL DEFAULT ''"},
		{"source", "ALTER TABLE items ADD COLUMN source TEXT NOT NULL DEFAULT ''"},
		{"owner", "ALTER TABLE items ADD COLUMN owner TEXT NOT NULL DEFAULT ''"},
		// 无成本标记：代卖 / 寄卖 / 赠送这类本来就没有货品成本的卖单，
		// 用户主动标记后不再进「未挂成本」，利润按销售额全额计。
		{"cost_free", "ALTER TABLE items ADD COLUMN cost_free INTEGER NOT NULL DEFAULT 0"},
		// 发货时间：闲鱼卖单列表里的 consignTime，精确到秒。
		// 支付宝的「闲鱼寄件-寄件费」不带订单号，但付款时间与发货时间只差 2~5 秒，
		// 存下它才能把快递费准确对到具体订单上（而不是靠成交时间猜）。
		{"consign_time", "ALTER TABLE items ADD COLUMN consign_time TEXT NOT NULL DEFAULT ''"},
	} {
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('items') WHERE name=?", col.name).Scan(&n)
		if n == 0 {
			if _, err := db.Exec(col.ddl); err != nil {
				return err
			}
		}
	}
	// 历史数据补标来源：闲鱼同步建的记录 note 一定以「闲鱼同步」开头。
	// 必须放在补列之后（老库第一次启动时 source 列还不存在）；
	// 只在 source 为空时补，不覆盖用户手填的来源，重复执行无副作用。
	db.Exec(`UPDATE items SET source='闲鱼'
        WHERE COALESCE(source,'')='' AND note LIKE '闲鱼同步%'`)

	db.Exec("CREATE INDEX IF NOT EXISTS idx_items_parent ON items(parent_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_items_norm ON items(product_norm)")
	// 店铺维度复合索引：单列索引在「先按 shop_id 过滤、再按时间排序」时会退化成扫描。
	db.Exec("CREATE INDEX IF NOT EXISTS idx_items_shop_sell ON items(shop_id, sell_time)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_items_shop_buy ON items(shop_id, buy_time)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_items_shop_norm ON items(shop_id, product_norm)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_items_shop_parent ON items(shop_id, parent_id)")
	// 同步导入前要按「店铺 + 订单号」查重、去重修复也要扫这两列，
	// 没索引时是整表扫，几千条以后每次同步都会明显变慢。
	db.Exec("CREATE INDEX IF NOT EXISTS idx_items_shop_sellno ON items(shop_id, sell_order_no)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_items_shop_buyno ON items(shop_id, buy_order_no)")
	// 交易时间表达式索引：明细默认按「卖单时间，没有就用买单时间」倒序排，
	// 这个 COALESCE 表达式用不上普通列索引，实测会产生 TEMP B-TREE 全量排序。
	// 数据量上千后每次翻页都要重排一遍，建表达式索引后可直接走索引扫描。
	db.Exec("CREATE INDEX IF NOT EXISTS idx_items_shop_deal ON items(shop_id, " + dealTimeExpr + ")")
	// shops.password_hash 幂等补列（旧库升级）
	var pn int
	db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('shops') WHERE name='password_hash'").Scan(&pn)
	if pn == 0 {
		if _, err := db.Exec("ALTER TABLE shops ADD COLUMN password_hash TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	// 默认店铺必须最先存在：存量数据按 DEFAULT 1 归入，保证升级后统计口径不变。
	if err := ensureDefaultShop(); err != nil {
		return err
	}
	// 旧数据回填归一化商品名（只处理空值，幂等；避免每次启动重复扫描）
	if err := backfillProductNorm(); err != nil {
		return err
	}
	if err := createBillCacheTable(); err != nil {
		return err
	}
	if err := createGroupTables(); err != nil {
		return err
	}
	// link_groups 表已确保存在，再补 settled_at 列（旧库升级用，幂等）
	if err := backfillGroupColumns(); err != nil {
		return err
	}
	// 一次性数据修复：卖单到手价口径统一
	if err := repairSellNetOnce(); err != nil {
		return err
	}
	// 一次性数据修复：清掉同步重复插入 / 账单补建误收的非交易记录
	if err := repairSyncDupOnce(); err != nil {
		return err
	}
	// 预置常用运费标签
	for _, t := range []string{"包邮", "卖家承担", "买家承担", "到付"} {
		if _, err := db.Exec("INSERT OR IGNORE INTO ship_tags(name, created_at) VALUES(?,?)", t, nowStr()); err != nil {
			return err
		}
	}
	return nil
}

// repairSellNetOnce 统一卖单到手价口径并只跑一次：
//
//	到手价 = 成交价 − 软件服务费 − 无忧卖服务费 − 卖运费
//
// 历史上有三处写法不一：闲鱼同步只减了服务费、支付宝导入把运费也算进去了、
// 还有一批已完结联单里的记录停留在「未扣费」的旧值（如 RB5009 的 1199.00）。
// 明细页的「到手价」一直按「成交价 − 运费 − 两项服务费」实时算，
// 所以把库里的值对齐到同一口径，统计与列表才不会各说一套。
func repairSellNetOnce() error {
	if getSetting("fix_sell_net_v1") != "" {
		return nil
	}
	if _, err := db.Exec(`UPDATE items SET
        sell_net = round(sell_price - sell_fee - sell_wuyou_fee - sell_shipping, 2)
        WHERE sell_price > 0`); err != nil {
		return err
	}
	return setSetting("fix_sell_net_v1", nowStr())
}

// repairSyncDupOnce 修掉两类脏数据，只跑一次：
//
//  1. 账单补建把「退货运费赔付 / 闲鱼转账 / 红包」也当卖单建了出来 —— 这些不是成交，删掉；
//  2. 连点两次导入（或自动同步撞上手动导入）导致同一订单号插了两条 —— 保留 id 最小的一条。
//
// 两步都先清 group_items，再删 items，避免联单里留下悬空成员。
func repairSyncDupOnce() error {
	if getSetting("fix_sync_dup_v1") != "" {
		return nil
	}
	drop := func(where string, args ...interface{}) error {
		rows, err := db.Query("SELECT id FROM items WHERE "+where, args...)
		if err != nil {
			return err
		}
		ids := []int64{}
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, id := range ids {
			db.Exec("DELETE FROM group_items WHERE item_id=?", id)
			db.Exec("DELETE FROM items WHERE id=?", id)
		}
		return nil
	}
	// 1) 误收的赔付 / 转账 / 红包（只动账单补建出来的那些）
	if err := drop(`note LIKE '闲鱼账单补建%' AND (product LIKE '%赔付%' OR product LIKE '%转账%' OR product LIKE '%红包%')`); err != nil {
		return err
	}
	// 2) 同一店铺同一订单号的多条，保留最早那条
	if err := drop(`sell_order_no <> '' AND id NOT IN (
        SELECT MIN(id) FROM items WHERE sell_order_no <> '' GROUP BY shop_id, sell_order_no)`); err != nil {
		return err
	}
	if err := drop(`buy_order_no <> '' AND id NOT IN (
        SELECT MIN(id) FROM items WHERE buy_order_no <> '' GROUP BY shop_id, buy_order_no)`); err != nil {
		return err
	}
	return setSetting("fix_sync_dup_v1", nowStr())
}

// migrateLegacy：把旧版 records 表（买/卖两条流水）按商品名合并迁移到 items。
// 旧表重命名为 records_legacy 备份保留，不删除任何数据。
func migrateLegacy() error {
	var cnt int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='records'").Scan(&cnt); err != nil {
		return err
	}
	if cnt == 0 {
		return nil
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM records").Scan(&n); err != nil {
		return nil // 结构不同则跳过
	}
	if n > 0 {
		rows, err := db.Query(`SELECT type, product, counterparty, order_no, deal_time,
            listed_price, price, fee, net_price, cost, note, created_at FROM records ORDER BY id`)
		if err == nil {
			type legacy struct {
				typ, product, cp, orderNo, dealTime, note, createdAt string
				listed, price, fee, net, cost                        float64
			}
			var all []legacy
			for rows.Next() {
				var l legacy
				rows.Scan(&l.typ, &l.product, &l.cp, &l.orderNo, &l.dealTime,
					&l.listed, &l.price, &l.fee, &l.net, &l.cost, &l.note, &l.createdAt)
				all = append(all, l)
			}
			rows.Close()

			// 按商品名分组：同名的第一条 buy 与第一条 sell 配成一条记录
			type pair struct{ buy, sell *legacy }
			pairs := map[string]*pair{}
			var order []string
			for i := range all {
				l := &all[i]
				key := l.product
				if _, ok := pairs[key]; !ok {
					pairs[key] = &pair{}
					order = append(order, key)
				}
				p := pairs[key]
				if l.typ == "buy" && p.buy == nil {
					p.buy = l
				} else if l.typ == "sell" && p.sell == nil {
					p.sell = l
				}
			}
			for _, key := range order {
				p := pairs[key]
				var buyPrice, buyShip, sellPrice, sellFee, sellNet, sellShip float64
				var buyTag, buyNo, buyTime, buyCP, sellTag, sellNo, sellTime, sellCP, note string
				if p.buy != nil {
					buyPrice = p.buy.net // 成本单实付
					buyNo, buyTime, buyCP, note = p.buy.orderNo, p.buy.dealTime, p.buy.cp, p.buy.note
				}
				if p.sell != nil {
					sellPrice, sellFee, sellNet = p.sell.price, p.sell.fee, p.sell.net
					if p.sell.cost > 0 && buyPrice == 0 {
						buyPrice = p.sell.cost // 旧版卖出单里手填的成本
					}
					sellNo, sellTime, sellCP = p.sell.orderNo, p.sell.dealTime, p.sell.cp
					if note == "" {
						note = p.sell.note
					}
				}
				ts := nowStr()
				if p.buy != nil && p.buy.createdAt != "" {
					ts = p.buy.createdAt
				}
				db.Exec(`INSERT INTO items
                    (product, buy_price,buy_shipping,buy_ship_tag,buy_order_no,buy_time,buy_counterparty,
                     sell_price,sell_fee,sell_net,sell_shipping,sell_ship_tag,sell_order_no,sell_time,sell_counterparty,
                     note, created_at, updated_at)
                    VALUES (?,?,?,?,?,?,?, ?,?,?,?,?,?,?,?, ?,?,?)`,
					key, buyPrice, buyShip, buyTag, buyNo, buyTime, buyCP,
					sellPrice, sellFee, sellNet, sellShip, sellTag, sellNo, sellTime, sellCP,
					note, ts, nowStr())
			}
		}
	}
	_, err := db.Exec("ALTER TABLE records RENAME TO records_legacy")
	return err
}

func nowStr() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

// backfillProductNorm：给历史记录补归一化商品名，只处理空值，幂等。
// 在事务里批量更新，避免逐条提交。
func backfillProductNorm() error {
	rows, err := db.Query("SELECT id, product FROM items WHERE product_norm='' OR product_norm IS NULL")
	if err != nil {
		return err
	}
	type pair struct {
		id int64
		p  string
	}
	var list []pair
	for rows.Next() {
		var x pair
		if rows.Scan(&x.id, &x.p) == nil {
			list = append(list, x)
		}
	}
	rows.Close()
	if len(list) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare("UPDATE items SET product_norm=? WHERE id=?")
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, x := range list {
		if _, err := stmt.Exec(normProduct(x.p), x.id); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// backfillGroupColumns：link_groups 建表后，幂等补列（旧库升级用）。
func backfillGroupColumns() error {
	var n int
	db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('link_groups') WHERE name='settled_at'").Scan(&n)
	if n == 0 {
		if _, err := db.Exec("ALTER TABLE link_groups ADD COLUMN settled_at TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	// 多店铺：联单归属店铺。存量联单全部归默认店，与 items 保持一致。
	var m int
	db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('link_groups') WHERE name='shop_id'").Scan(&m)
	if m == 0 {
		if _, err := db.Exec("ALTER TABLE link_groups ADD COLUMN shop_id INTEGER NOT NULL DEFAULT 1"); err != nil {
			return err
		}
	}
	db.Exec("CREATE INDEX IF NOT EXISTS idx_groups_shop ON link_groups(shop_id)")
	return nil
}

// createBillCacheTable 账单事实表。
//
// 闲鱼的账单接口代价高：一次只能查 31 天，且区间结束日必须比今天早 3 天，
// 为了拿到一单的手续费往往要来回查好几个月。这里把每次查到的结果按订单号落地，
// 之后同步只去查「还没出账」的那些月份，既快又不会因为一次查询失败就把已有费用丢掉。
// dealTimeExpr 明细列表的「交易时间」表达式：卖单时间优先，没有则用买单时间。
// 索引与 ORDER BY 必须用完全相同的字符串，差一个空格索引就不生效，
// 所以集中在这里定义，两处都引用它。
const dealTimeExpr = "COALESCE(NULLIF(sell_time,''), NULLIF(buy_time,''), '')"

type gfBillRow struct {
	OrderNo string
	Actual  float64
	Auction float64
	Tech    float64
	Wuyou   float64
	Billed  bool
	PostFee float64 // 商品邮费（POST_FEE）：预留给「闲鱼同步时带入买运费」。默认 0，开关关闭时不会写入。
}

func createBillCacheTable() error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS gf_bills (
        order_no   TEXT PRIMARY KEY,
        actual     REAL NOT NULL DEFAULT 0,
        auction    REAL NOT NULL DEFAULT 0,
        tech       REAL NOT NULL DEFAULT 0,
        wuyou      REAL NOT NULL DEFAULT 0,
        billed     INTEGER NOT NULL DEFAULT 0,
        post_fee   REAL NOT NULL DEFAULT 0,
        fetched_at TEXT NOT NULL DEFAULT ''
    )`)
	if err != nil {
		return err
	}
	// 旧库升级：gf_bills 是后加的表，老库可能没有 post_fee 列。幂等补列。
	var pf int
	db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('gf_bills') WHERE name='post_fee'").Scan(&pf)
	if pf == 0 {
		if _, err := db.Exec("ALTER TABLE gf_bills ADD COLUMN post_fee REAL NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	return nil
}

// loadBillCache 批量取缓存（分片，避免 IN 列表过长）
func loadBillCache(orderNos []string) map[string]*gfBillRow {
	out := map[string]*gfBillRow{}
	const chunk = 200
	for i := 0; i < len(orderNos); i += chunk {
		end := i + chunk
		if end > len(orderNos) {
			end = len(orderNos)
		}
		part := orderNos[i:end]
		ph := strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")
		args := make([]interface{}, 0, len(part))
		for _, s := range part {
			args = append(args, s)
		}
		rows, err := db.Query("SELECT order_no, actual, auction, tech, wuyou, billed, post_fee FROM gf_bills WHERE order_no IN ("+ph+")", args...)
		if err != nil {
			return out
		}
		for rows.Next() {
			var r gfBillRow
			var billed int
			if rows.Scan(&r.OrderNo, &r.Actual, &r.Auction, &r.Tech, &r.Wuyou, &billed, &r.PostFee) == nil {
				r.Billed = billed == 1
				out[r.OrderNo] = &r
			}
		}
		rows.Close()
	}
	return out
}

// savePostFee 只更新某单的商品邮费缓存，不碰已缓存的成交额/服务费。
// 供「闲鱼同步带入买运费」预留链路使用（默认关闭），避免整行覆盖丢掉账单费用。
func savePostFee(orderNo string, fee float64) {
	db.Exec(`INSERT INTO gf_bills(order_no,post_fee,fetched_at) VALUES(?,?,?)
        ON CONFLICT(order_no) DO UPDATE SET post_fee=excluded.post_fee, fetched_at=excluded.fetched_at`,
		orderNo, fee, nowStr())
}

// saveBillFact 落一条账单事实（同单号覆盖，取最后一次查到的结果）
func saveBillFact(r gfBillRow) {
	billed := 0
	if r.Billed {
		billed = 1
	}
	db.Exec(`INSERT INTO gf_bills(order_no,actual,auction,tech,wuyou,billed,post_fee,fetched_at)
        VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(order_no) DO UPDATE SET
        actual=excluded.actual, auction=excluded.auction, tech=excluded.tech,
        wuyou=excluded.wuyou, billed=excluded.billed, post_fee=excluded.post_fee,
        fetched_at=excluded.fetched_at`,
		r.OrderNo, r.Actual, r.Auction, r.Tech, r.Wuyou, billed, r.PostFee, nowStr())
}
