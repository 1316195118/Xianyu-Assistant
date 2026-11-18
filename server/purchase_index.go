package main

import "sync"

// 买进单内存索引：匹配关联时不再全表扫描 + 逐条算编辑距离 + N+1 查询子单数。
// 启动时构建一次；每次新增/编辑/删除/导入后同步重建（数据量小，单条索引 SQL < 数 ms），
// 做到「录入即建索引、打开即秒出」。
type purchaseEntry struct {
	ID              int64   `json:"id"`
	ShopID          int64   `json:"shop_id"`
	Product         string  `json:"product"`
	Norm            string  `json:"norm"`
	BuyPrice        float64 `json:"buy_price"`
	BuyShipping     float64 `json:"buy_shipping"`
	BuyOrderNo      string  `json:"buy_order_no"`
	BuyTime         string  `json:"buy_time"`
	BuyCounterparty string  `json:"buy_counterparty"`
	Quantity        int     `json:"quantity"`
	ChildCount      int     `json:"child_count"`
}

var purchaseIndex = struct {
	sync.RWMutex
	ready       bool
	entries     []purchaseEntry
	byNorm      map[int64]map[string][]int // shopID -> norm -> entries 下标
	childCounts map[int64]int
}{}

// initPurchaseIndex 服务启动时调用
func initPurchaseIndex() { refreshPurchaseIndex() }

// refreshPurchaseIndex 一次 SQL 取全部买进单 + 一次 GROUP BY 取子单数，构建内存索引
func refreshPurchaseIndex() {
	// 一次取全部店铺的买进单再按 shop_id 分桶：索引是进程级全局结构，
	// 分店查询必须命中各自的桶，否则 A 店的卖出单会匹配到 B 店的同名买进。
	rows, err := db.Query(`SELECT id, shop_id, product, COALESCE(NULLIF(product_norm,''),''),
        buy_price, buy_shipping, buy_order_no, buy_time, buy_counterparty, quantity
        FROM items WHERE ` + purchaseCond + ` ORDER BY id DESC`)
	if err != nil {
		return // 保留旧索引，不影响其它功能
	}
	entries := []purchaseEntry{}
	for rows.Next() {
		var e purchaseEntry
		if rows.Scan(&e.ID, &e.ShopID, &e.Product, &e.Norm, &e.BuyPrice, &e.BuyShipping,
			&e.BuyOrderNo, &e.BuyTime, &e.BuyCounterparty, &e.Quantity) != nil {
			continue
		}
		if e.Norm == "" {
			e.Norm = normProduct(e.Product)
		}
		entries = append(entries, e)
	}
	rows.Close()

	childCounts := map[int64]int{}
	if cr, err := db.Query("SELECT parent_id, COUNT(*) FROM items WHERE parent_id>0 GROUP BY parent_id"); err == nil {
		for cr.Next() {
			var id int64
			var n int
			if cr.Scan(&id, &n) == nil {
				childCounts[id] = n
			}
		}
		cr.Close()
	}
	byNorm := map[int64]map[string][]int{}
	for i := range entries {
		entries[i].ChildCount = childCounts[entries[i].ID]
		m := byNorm[entries[i].ShopID]
		if m == nil {
			m = map[string][]int{}
			byNorm[entries[i].ShopID] = m
		}
		m[entries[i].Norm] = append(m[entries[i].Norm], i)
	}

	purchaseIndex.Lock()
	purchaseIndex.entries = entries
	purchaseIndex.byNorm = byNorm
	purchaseIndex.childCounts = childCounts
	purchaseIndex.ready = true
	purchaseIndex.Unlock()
}

// listPurchases 返回买进单快照（供前端预加载，选择器秒开）
func listPurchases(shopID int64) []purchaseEntry {
	purchaseIndex.RLock()
	ready := purchaseIndex.ready
	purchaseIndex.RUnlock()
	if !ready {
		refreshPurchaseIndex()
	}
	purchaseIndex.RLock()
	defer purchaseIndex.RUnlock()
	out := []purchaseEntry{}
	for _, e := range purchaseIndex.entries {
		if e.ShopID == shopID {
			out = append(out, e)
		}
	}
	return out
}

// indexedChildCount 从内存索引取子单数，避免 N+1 查询
func indexedChildCount(id int64) int {
	purchaseIndex.RLock()
	defer purchaseIndex.RUnlock()
	return purchaseIndex.childCounts[id]
}

// matchPurchasesIndexed：精确匹配走 byNorm 哈希 O(1)；相似匹配只在内存里算编辑距离，
// 并按长度差先剪枝，避免长标题无谓的 O(n*m) 计算。
func matchPurchasesIndexed(product string, shopID int64, limit int) (exact []purchaseEntry, similar []purchaseEntry) {
	purchaseIndex.RLock()
	defer purchaseIndex.RUnlock()
	nq := normProduct(product)
	if nq == "" || len(purchaseIndex.entries) == 0 {
		return nil, nil
	}
	if idxs := purchaseIndex.byNorm[shopID][nq]; len(idxs) > 0 {
		for _, i := range idxs {
			exact = append(exact, purchaseIndex.entries[i])
		}
	}
	lq := len([]rune(nq))
	for i := range purchaseIndex.entries {
		e := &purchaseIndex.entries[i]
		if e.ShopID != shopID {
			continue
		}
		if e.Norm == nq {
			continue // 已在 exact
		}
		le := len([]rune(e.Norm))
		// 长度差过大不可能达到 0.6 相似度（1 - dist/max >= 0.6 要求 |la-lb| <= 0.4*max），先剪枝
		maxLen := lq
		if le > maxLen {
			maxLen = le
		}
		diff := lq - le
		if diff < 0 {
			diff = -diff
		}
		if float64(diff) > float64(maxLen)*0.4 {
			continue
		}
		if similarity(nq, e.Norm) >= 0.6 {
			similar = append(similar, *e)
			if len(similar) >= limit {
				break
			}
		}
	}
	if len(exact) > limit {
		exact = exact[:limit]
	}
	return exact, similar
}
