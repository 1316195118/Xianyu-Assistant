package main

import "log"

// ============================================================
// 运费标签口径迁移
//
// 规则（2026-11 统一）：运费 > 0.1 元才算「有运费」，否则视为包邮。
//   买进侧：有运费 → 到付；没有 → 包邮
//   卖出侧：有运费 → 卖家承担；没有 → 买家承担
//
// 只在第一次启动时执行一次（settings.ship_tag_rule 记版本），
// 之后用户手动改过的标签不会被每次重启覆盖。
// 没有那一侧的记录（没买价/没订单号/没昵称）清空标签，避免给卖单挂上"包邮"这种噪音。
// ============================================================

const shipTagRuleVersion = "1"

// 判据与 Item.derive() 保持一致：任一侧有实质内容才算该侧存在
const (
	hasBuyCond  = `(buy_price > 0 OR buy_order_no <> '' OR buy_counterparty <> '')`
	hasSellCond = `(sell_net > 0 OR sell_price > 0 OR sell_order_no <> '' OR sell_counterparty <> '')`
	shipEpsSQL  = "0.1"
)

func remapShipTags() {
	if getSetting("ship_tag_rule") == shipTagRuleVersion {
		return
	}
	type step struct {
		sql  string
		args []interface{}
	}
	steps := []step{
		{"UPDATE items SET buy_ship_tag = CASE WHEN buy_shipping > " + shipEpsSQL + " THEN '到付' ELSE '包邮' END WHERE " + hasBuyCond, nil},
		{"UPDATE items SET buy_ship_tag = '' WHERE NOT " + hasBuyCond, nil},
		{"UPDATE items SET sell_ship_tag = CASE WHEN sell_shipping > " + shipEpsSQL + " THEN '卖家承担' ELSE '买家承担' END WHERE " + hasSellCond, nil},
		{"UPDATE items SET sell_ship_tag = '' WHERE NOT " + hasSellCond, nil},
	}
	tx, err := db.Begin()
	if err != nil {
		log.Printf("运费标签迁移跳过（开启事务失败）: %v", err)
		return
	}
	defer tx.Rollback()
	buyN, sellN := int64(0), int64(0)
	for i, s := range steps {
		res, e := tx.Exec(s.sql, s.args...)
		if e != nil {
			log.Printf("运费标签迁移失败（第 %d 步）: %v", i+1, e)
			return
		}
		n, _ := res.RowsAffected()
		if i%2 == 0 {
			buyN += n
		} else {
			sellN += n
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("运费标签迁移提交失败: %v", err)
		return
	}
	setSetting("ship_tag_rule", shipTagRuleVersion)
	log.Printf("运费标签已按金额重新判定：买进侧 %d 条、卖出侧 %d 条", buyN, sellN)
}
