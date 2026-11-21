package main

import (
	"strings"
	"testing"
)

func TestParseProduct(t *testing.T) {
	cases := []struct {
		name, text, wantProduct string
		wantSide                string
		wantPrice               float64
	}{
		{
			name:        "卖出单-快给对方评价提示条",
			text:        "14:53 2 全 忆忆， 站 - 快给对方一个评价吧~ 请在26天18时23分内完成评价 虽号 网心云OEC-Turbo盒子，边缘计算 xy138.00 设备，网心云打米机型。 | 48小时发货 || 退货运费险 成交价 (已到达您的支付宝187****21) x131.91 软件服务费 -x2.11 预估到手 xy129.80 订单编号 5127565358550027332 | 复制 服务保障 退货运费险 48小时发货 保障结束 承诺按时发货，超时将赔付.… 人人 昌 上- 从 本 1 一一 全场锡息 Fe 及L有日付1元起 你有273件宝贝可卖，还能赚 Y34471.8 吃 更多 查看钱款 人 联系买家",
			wantProduct: "网心云OEC-Turbo盒子，边缘计算 设备，网心云打米机型", wantSide: "sell", wantPrice: 131.91,
		},
		{
			name:        "买进单-评价完成感谢使用亲鱼(OCR误识别)",
			text:        "15:00 交易成功 评价完成，感谢使用亲鱼~ (ED 技嘉迷你主机 GB-BSI3H-6100，i3-6100U处 ¥159.00 成交价（已到达卖家账户） ¥159.00 商品总价 ¥159.00 运费 ¥0.00 订单编号 3316534215293013185 复制 卖家昵称 隋朝孜孜不倦的海苔 下单时间 2026-11-04 19:29:55 成交时间 2026-11-11 19:08:54",
			wantProduct: "技嘉迷你主机 GB-BSI3H-6100，i3-6100U处", wantSide: "buy", wantPrice: 159,
		},
		{
			name:        "规范逐行-卖出单",
			text:        "交易成功\n网心云OEC-Turbo盒子，边缘计算  ¥138.00\n设备，网心云打米机型。\n成交价（已到达您的支付宝187****21）  ¥131.91\n软件服务费  -¥2.11\n预计到手  ¥129.80\n订单编号  5127565358550027332  复制",
			wantProduct: "网心云OEC-Turbo盒子，边缘计算 设备，网心云打米机型", wantSide: "sell", wantPrice: 131.91,
		},
		{
			name:        "买进单-混入标题字样且长标题不得截断",
			text:        "17:57 交易成功 评价完成，感谢使用闲鱼~ 标题 联想ThinkCentre M700 Tiny迷你小主机 ¥180.00 成交价（已到达卖家账户） ¥180.00 商品总价 ¥180.00 运费 ¥0.00 订单编号 3316534647236013185 复制 卖家昵称 智慧电脑科技有限公司理小铺 下单时间 2026-11-09 20:38:21 成交时间 2026-11-09 20:38:21",
			wantProduct: "联想ThinkCentre M700 Tiny迷你小主机", wantSide: "buy", wantPrice: 180,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseText(c.text)
			if got.Product != c.wantProduct {
				t.Errorf("product = %q, want %q", got.Product, c.wantProduct)
			}
			if got.Side != c.wantSide {
				t.Errorf("side = %q, want %q", got.Side, c.wantSide)
			}
			if got.Price != c.wantPrice {
				t.Errorf("price = %v, want %v", got.Price, c.wantPrice)
			}
			t.Logf("product=%q side=%s price=%v net=%v fee=%v order=%s", got.Product, got.Side, got.Price, got.NetPrice, got.Fee, got.OrderNo)
		})
	}
}

// TestParseFields 重点验证：运费提取、买卖侧判定（含收货地址歧义）、订单号/时间/对方昵称
func TestParseFields(t *testing.T) {
	cases := []struct {
		name           string
		text           string
		wantSide       string
		wantProduct    string // 子串即可
		wantPrice      float64
		wantShipping   float64
		wantOrder      string // 子串即可
		wantDealPrefix string // 时间前缀，如 2026-10-21
		wantCounter    string // 子串即可
	}{
		{
			name:           "买进单-华硕PN41(本轮反馈图，运费0=包邮)",
			text:           "交易成功 评价完成，感谢使用闲鱼~ 华硕PN41，N6005迷你准系统 ¥220.00 商品总价¥220.00 运费¥0.00 订单编号3317146572100013185 复制 支付宝交易号2026102123001170521404955148 收货地址徐洪生18723957921新疆维吾尔自治区阿拉尔市幸福街道幸福北路鸿燕南国风光小区7号楼 卖家昵称咖喱给b 下单时间2026-10-21 17:55:04 付款时间2026-10-21 17:55:09 发货时间2026-10-22 15:23:43 成交时间2026-10-28 18:19:55",
			wantSide:       "buy",
			wantProduct:    "华硕PN41",
			wantPrice:      220,
			wantShipping:   0,
			wantOrder:      "3317146572100013185",
			wantDealPrefix: "2026-10-21", // 买进单应取「下单时间」
			wantCounter:    "咖喱给b",
		},
		{
			name:           "买进单-有运费(应识别买家承担)",
			text:           "交易成功 无线键鼠套装 ¥99.00 成交价 ¥99.00 商品总价 ¥99.00 运费 ¥12.00 订单编号 5511223344556677881 卖家昵称 小店主 下单时间2026-09-01 10:00:00 成交时间2026-09-02 18:00:00",
			wantSide:       "buy",
			wantProduct:    "无线键鼠套装",
			wantPrice:      99,
			wantShipping:   12,
			wantOrder:      "5511223344556677881",
			wantDealPrefix: "2026-09-01",
			wantCounter:    "小店主",
		},
		{
			name:           "卖出单-含收货地址(验证收货地址不再误判为买进)",
			text:           "交易成功 网心云OEC-Turbo盒子 成交价（已到达您的支付宝187****21） ¥131.91 软件服务费 -¥2.11 预计到手 ¥129.80 订单编号 5127565358550027332 收货地址 浙江省杭州市余杭区某某街道1号 买家昵称 张三 成交时间 2026-11-01 12:00:00",
			wantSide:       "sell",
			wantProduct:    "网心云OEC-Turbo盒子",
			wantPrice:      131.91,
			wantShipping:   0,
			wantOrder:      "5127565358550027332",
			wantDealPrefix: "2026-11-01",
			wantCounter:    "张三",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseText(c.text)
			if got.Side != c.wantSide {
				t.Errorf("side=%q want %q (buy=%d sell=%d)", got.Side, c.wantSide, got.SideBuyScore, got.SideSellScore)
			}
			if c.wantProduct != "" && !strings.Contains(got.Product, c.wantProduct) {
				t.Errorf("product=%q want contains %q", got.Product, c.wantProduct)
			}
			if got.Price != c.wantPrice {
				t.Errorf("price=%v want %v", got.Price, c.wantPrice)
			}
			if got.Shipping != c.wantShipping {
				t.Errorf("shipping=%v want %v", got.Shipping, c.wantShipping)
			}
			if c.wantOrder != "" && !strings.Contains(got.OrderNo, c.wantOrder) {
				t.Errorf("order_no=%q want contains %q", got.OrderNo, c.wantOrder)
			}
			if c.wantDealPrefix != "" && !strings.HasPrefix(got.DealTime, c.wantDealPrefix) {
				t.Errorf("deal_time=%q want prefix %q", got.DealTime, c.wantDealPrefix)
			}
			if c.wantCounter != "" && !strings.Contains(got.Counterparty, c.wantCounter) {
				t.Errorf("counterparty=%q want contains %q", got.Counterparty, c.wantCounter)
			}
			t.Logf("side=%s(b%d/s%d) product=%q price=%v ship=%v order=%q time=%q cp=%q",
				got.Side, got.SideBuyScore, got.SideSellScore, got.Product, got.Price, got.Shipping, got.OrderNo, got.DealTime, got.Counterparty)
		})
	}
}

// TestParsePersonalIdle 回归「个人闲置」类目标签污染商品名：
//   - 闲鱼在标题区会显示「个人闲置」类目标签，OCR 可能用逗号分隔，也可能紧贴商品名
//   - 无论哪种，商品名都不应残留「个人闲置」，且真实标题（UBNT…）必须保留
func TestParsePersonalIdle(t *testing.T) {
	cases := []struct {
		name, text  string
		wantContain string
	}{
		{
			name:        "个人闲置-逗号分隔(常见)",
			text:        "交易成功 个人闲置，UBNT优倍快，UACC，电源 ¥420.00 成交价 ¥420.00 商品总价 ¥420.00 运费 ¥0.00 订单编号 3317025864051013185 复制 卖家昵称 闲鱼小铺 下单时间 2026-11-05 14:30:00 成交时间 2026-11-06 10:00:00",
			wantContain: "UBNT优倍快",
		},
		{
			// 最坏情况：OCR 未切分逗号，类目标签与商品名连在一起
			name:        "个人闲置-紧贴无逗号(容错)",
			text:        "交易成功 个人闲置UBNT优倍快UACC电源 ¥420.00 成交价 ¥420.00 商品总价 ¥420.00 运费 ¥0.00 订单编号 3317025864051013185 复制 卖家昵称 闲鱼小铺 下单时间 2026-11-05 14:30:00 成交时间 2026-11-06 10:00:00",
			wantContain: "UBNT优倍快",
		},
		{
			// 手机长截图 OCR：商品卡片下方小图标被读成「目目」，不能混进商品名
			name:        "个人闲置-带目目噪声(手机截图)",
			text:        "22:55 赖 评价完成，感谢使用亲鱼~ 下 个人闲置 UBNT优倍快 UACC- x270.00 人 PoE++-10G 电源 目目 成交价 x420.00 订单编号 3317025864051013185 复制 卖家昵称 一羽酒先生 下单时间 2026-09-11 20:06:26 成交时间 2026-09-17 20:54:09",
			wantContain: "UBNT优倍快",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseText(c.text)
			if strings.Contains(got.Product, "个人闲置") {
				t.Errorf("product=%q 不应残留「个人闲置」", got.Product)
			}
			if strings.Contains(got.Product, "目目") {
				t.Errorf("product=%q 不应残留「目目」噪声", got.Product)
			}
			if !strings.Contains(got.Product, c.wantContain) {
				t.Errorf("product=%q 应保留 %q", got.Product, c.wantContain)
			}
			t.Logf("product=%q ship=%v order=%q cp=%q", got.Product, got.Shipping, got.OrderNo, got.Counterparty)
		})
	}
}

// TestParseCounterpartyNoise 回归 OCR 把「昵称」误读成「呢称」（同形字），
// 以及「卖家了呢称」这类多噪字，确保对方昵称仍能抓到（即使本身被 OCR 略扭曲）。
func TestParseCounterpartyNoise(t *testing.T) {
	// 真实 OCR：卖家了呢称 一羽酒先4（真名「一月酒先生」）；订单号/时间齐全
	text := `评价完成，感谢使用闲鱼~
个人闲置 UBNT优倍快 UACC-  ”x270.00
PoE++-10G 电源
成交价 (已到达卖家账户)                x420.00
订单编号               3317025864051013185 | 复制
支付宝交易号           2026091123001170521406573551 复制
收货地址 “”徐洪生 18723957921 新疆维吾尔自治区阿
拉尔市幸福路街道幸福北路鸿添南国风光
小区7号楼
卖家了呢称                               一羽酒先4
下单时间                         2026-09-11 20:06:26
付款时间                                   2026-09-11 20:07:02
发货时间                                2026-09-12 10:15:16
成交时间                         2026-09-17 20:54:09
联系卖家`
	got := parseText(text)
	if got.Counterparty == "" {
		t.Errorf("counterparty 应为非空（昵称误读仍要抓到），got=%q", got.Counterparty)
	}
	// 末尾杂数字 4 应被剥除
	if strings.HasSuffix(got.Counterparty, "4") {
		t.Errorf("counterparty=%q 末尾杂数字未剥除", got.Counterparty)
	}
	if got.OrderNo != "3317025864051013185" {
		t.Errorf("order_no=%q want 3317025864051013185", got.OrderNo)
	}
	if !strings.HasPrefix(got.DealTime, "2026-09-11") {
		t.Errorf("deal_time=%q want prefix 2026-09-11(买进取下单时间)", got.DealTime)
	}
	if got.Side != "buy" {
		t.Errorf("side=%q want buy", got.Side)
	}
	t.Logf("cp=%q order=%q time=%q side=%s product=%q", got.Counterparty, got.OrderNo, got.DealTime, got.Side, got.Product)
}

// TestParseCounterpartyDropped 回归：手机端 OCR 把昵称行整行漏掉时，
// 「卖家昵称」后直接跟「下单时间 …」，必须拒收「下单时间」作为昵称（返回空），
// 绝不能把时间标签填进对方昵称。
func TestParseCounterpartyDropped(t *testing.T) {
	// 模拟手机 OCR：昵称行被漏掉，卖家昵称后紧跟「下单时间」
	text := `交易成功
个人闲置 UBNT优倍快 UACC-  ”x270.00
PoE++-10G 电源
成交价 (已到达卖家账户)                x420.00
订单编号               3317025864051013185 | 复制
卖家昵称 下单时间 2026-09-11 20:06:26
付款时间                                   2026-09-11 20:07:02
成交时间                         2026-09-17 20:54:09`
	got := parseText(text)
	if got.Counterparty != "" {
		t.Errorf("counterparty=%q 不应把「下单时间」当昵称（手机 OCR 漏掉真昵称时应返回空）", got.Counterparty)
	}
	if got.OrderNo != "3317025864051013185" {
		t.Errorf("order_no=%q want 3317025864051013185", got.OrderNo)
	}
	if !strings.HasPrefix(got.DealTime, "2026-09-11") {
		t.Errorf("deal_time=%q want prefix 2026-09-11", got.DealTime)
	}
	t.Logf("cp=%q order=%q time=%q side=%s", got.Counterparty, got.OrderNo, got.DealTime, got.Side)
}

// TestParseSellFees 回归卖出订单的两项平台扣费：
// 「无忧卖类目软件服务费 -¥7.00」与「软件服务费 -¥11.20」必须分开，
// 且到手价 = 成交价 − 软件服务费 − 无忧卖服务费 = 700 − 11.20 − 7.00 = 681.80
func TestParseSellFees(t *testing.T) {
	text := `交易成功 评价完成，感谢使用闲鱼~
UniFi U7 lite WiFi7 优倍快 吸顶AP ¥750.00
成交价 (已到达您的支付宝187****21) ¥700.00
无忧卖类目软件服务费 -¥7.00
软件服务费 -¥11.20
预估到手 ¥681.80
订单编号 3317167812006002279 复制
支付宝交易号 2026103023001193891419532170 复制
买家昵称 calli_3
下单时间 2026-10-30 16:28:50
成交时间 2026-11-02 16:46:47`
	got := parseText(text)
	if got.Side != "sell" {
		t.Errorf("side=%q want sell", got.Side)
	}
	if got.Price != 700 {
		t.Errorf("price=%v want 700", got.Price)
	}
	if got.WuyouFee != 7 {
		t.Errorf("wuyou_fee=%v want 7", got.WuyouFee)
	}
	if got.Fee != 11.20 {
		t.Errorf("fee=%v want 11.20（不能把无忧卖的 7 当手续费）", got.Fee)
	}
	if got.NetPrice != 681.80 {
		t.Errorf("net_price=%v want 681.80", got.NetPrice)
	}
	t.Logf("side=%s price=%v fee=%v wuyou=%v net=%v product=%q", got.Side, got.Price, got.Fee, got.WuyouFee, got.NetPrice, got.Product)
}

// TestParseProductNoMarketingLabel 回归商品卡片上的黄色营销标签（爆款/预售等）
// 被 OCR 读成「碟前/爆赤」后混进商品名。
func TestParseProductNoMarketingLabel(t *testing.T) {
	text := `评价完成，感谢使用闲鱼~
碟赤 任天堂Alarmo闹钟 马里奥主 x220.00
题红色款，朋友赠送，带原包装
成交价（已到达卖家账户） x220.00
商品总价 x220.00
运费 x0.00
订单编号 3317143332049013185 复制
卖家昵称 王小花0526
下单时间 2026-10-19 00:21:12`
	got := parseText(text)
	for _, junk := range []string{"碟赤", "爆赤", "碟前", "预售"} {
		if strings.Contains(got.Product, junk) {
			t.Errorf("product=%q 不应包含营销标签噪声 %q", got.Product, junk)
		}
	}
	if !strings.Contains(got.Product, "Alarmo") {
		t.Errorf("product=%q 应保留真实标题", got.Product)
	}
	t.Logf("product=%q", got.Product)
}

// TestParseProductRealScreenshots 两张真实手机截图的 OCR 文本：
// 标题中空格/换行分隔的连续词不得被「，」打散，营销标签噪声不得混入。
func TestParseProductRealScreenshots(t *testing.T) {
	cases := []struct {
		name, text, wantContain string
		noJunk                  []string
	}{
		{
			name: "UBNT-u7promax-空格分隔不得打散",
			text: `本次交易体验满意吗 请在4天18时57分内完成评价 特别好
UBNT 优倍快 unifi u7promax金属 ¥110.00
支架一套
成交价（已到达卖家账户） ¥110.00
商品总价 ¥110.00
运费 ¥0.00
订单编号 3316506099017013185 复制
卖家昵称 休斯顿勤快的蚕豆
下单时间 2026-10-16 10:54:30`,
			wantContain: "UBNT 优倍快",
			noJunk:      []string{"特别好", "本次交易体验"},
		},
		{
			name: "任天堂Alarmo-营销标签噪声",
			text: `评价完成，感谢使用闲鱼~
碟赤 任天堂Alarmo闹钟 马里奥主 ¥220.00
题红色款，朋友赠送，带原包装
成交价（已到达卖家账户） ¥220.00
商品总价 ¥220.00
运费 ¥0.00
订单编号 3317143332049013185 复制
卖家昵称 王小花0526
下单时间 2026-10-19 00:21:12`,
			wantContain: "Alarmo",
			noJunk:      []string{"碟赤", "碟前", "爆赤"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseText(c.text)
			if !strings.Contains(got.Product, c.wantContain) {
				t.Errorf("product=%q 应包含 %q", got.Product, c.wantContain)
			}
			for _, j := range c.noJunk {
				if strings.Contains(got.Product, j) {
					t.Errorf("product=%q 不应含 %q", got.Product, j)
				}
			}
			t.Logf("product=%q side=%s price=%v", got.Product, got.Side, got.Price)
		})
	}
}

// TestParseSellStatusBarTitle 回归卖出单标题：状态栏时间、「已发货」状态条不得混入，
// 纯字母数字型号 RB5009UG+S+IN 不得被中文占比规则误杀。
func TestParseSellStatusBarTitle(t *testing.T) {
	text := `01:31画                    HE 5
《 已发货，待买家确认收货          。。。。
MikroTik                    x1199.00
RB5009UG+S+IN 有线路由器 2
WE
成交价 (在支付宝担保账户中)         x1199.00
商品总价                          x1199.00
运费                              x0.00
订单编号 3316585406014013983 复制
买家昵称 raid
下单时间 2026-11-10 07:12:58`
	got := parseText(text)
	for _, junk := range []string{"01:31", "已发货", "确认收货", "WE"} {
		if strings.Contains(got.Product, junk) {
			t.Errorf("product=%q 不应含状态栏/状态条噪声 %q", got.Product, junk)
		}
	}
	if !strings.Contains(got.Product, "RB5009UG") {
		t.Errorf("product=%q 应保留型号 RB5009UG+S+IN", got.Product)
	}
	if !strings.Contains(got.Product, "MikroTik") {
		t.Errorf("product=%q 应保留品牌 MikroTik", got.Product)
	}
	t.Logf("product=%q side=%s price=%v", got.Product, got.Side, got.Price)
}

// TestParseTimeFromTxn 回归：手机端长截图 OCR 把「下单时间」右侧浅灰数值整片漏掉，
// 只留下标签；此时必须用支付宝交易号前 8 位（yyyyMMdd）还原出下单日期。
func TestParseTimeFromTxn(t *testing.T) {
	text := `交易成功 评价完成，感谢使用闲鱼~
UBNT优倍快 UACC-PoE++-10G 电源 x420.00
成交价 (已到达卖家账户) x420.00
订单编号 3317025864051013185 复制
支付宝交易号 2026091123001170521406573551 复制
卖家昵称 一羽酒先生
下单时间
付款时间
发货时间
成交时间
联系卖家`
	got := parseText(text)
	want := "2026-09-11 00:00:00"
	if got.DealTime != want {
		t.Errorf("deal_time=%q want %q（支付宝交易号推断下单日期）", got.DealTime, want)
	}
	if got.OrderNo != "3317025864051013185" {
		t.Errorf("order_no=%q want 3317025864051013185", got.OrderNo)
	}
	if got.Side != "buy" {
		t.Errorf("side=%q want buy", got.Side)
	}
	t.Logf("deal_time=%q order=%q side=%s cp=%q", got.DealTime, got.OrderNo, got.Side, got.Counterparty)
}

// TestParseTimePrecisePreferred 有完整时间戳时不得被交易号推断覆盖
func TestParseTimePrecisePreferred(t *testing.T) {
	text := `交易成功 UBNT电源 x420.00
订单编号 3317025864051013185 复制
支付宝交易号 2026091123001170521406573551 复制
卖家昵称 一羽酒先生
下单时间 2026-09-11 20:06:26
成交时间 2026-09-17 20:54:09`
	got := parseText(text)
	if got.DealTime != "2026-09-11 20:06:26" {
		t.Errorf("deal_time=%q want 精确的 2026-09-11 20:06:26", got.DealTime)
	}
	t.Logf("deal_time=%q", got.DealTime)
}

// TestParseRealOCR 用来自手机端 tesseract 的真实 OCR 文本回归，覆盖：
//  1. 支付优惠/−¥0.50 不能漏进运费（华硕运费的 0.00 之前、MikroTik 的 0.50 是支付优惠）
//  2. 对方昵称容错：OCR 误读「卖家了昵称」也要抓到昵称
//  3. 商品名去垃圾：「分享一下这次交易体验吧 / 特别好」等分享提示条不能进标题
func TestParseRealOCR(t *testing.T) {
	pn41 := `18:54                           人
0         -
 评价完成，感谢使用闲鱼~
    华硕PN41，N6005巡你准系统     x220.00
EC 站 加
| 攻sUR
和
| 嫩昌 3 明
人
网Eee
成交价 〈已到达卖家账户)                  Y220.00
商品总价                                    xY220.00
运费                                          xY0.00
订单编号                                       3317146572100013185 | 复制
交易快照                               发生交易争议时，可作为判断依据
支付宝交易号               2026102123001170521404955148 | 复制
收货地址 ”徐洪生 18723957921 新疆维寿尔自治区阿 复制
拉尔市幸福路街道幸福北路鸿添南国风光
卖家了昵称                               咖咀给给b
下单时间                                                 2026-10-21 17:55:04
付款时间                          2026-10-2117:55:09
发货时间                          2026-10-22 15:23:43
成交时间                                              2026-10-28 18:19:55
< 虽严 三| | =   人    疼  上  一:
联系卖家
`
	mikrotik := `22:24                            HH 5而
站                       -诈
 分享一下这次交易体验吧                     特别好
1     MikroTik RB5009UG+S+IN路由“>x1080.00
器 全新原装
成交价 〈已到达卖家账户)                  x1079.50
商品总价                          x1080.00
运费                               x0.00
支付优惠                           _x0.50
订单编号          3316830241024013185 |; 复制
交易快照         发生交易争议时，可作为判断依据
支付宝交易号   2026090323001170521447931573 | 复制
收货地址 “”徐洪生 18723957921 新疆维吾尔自治区阿 | 复制
拉尔市幸福路街道幸福北路鸿添南国风光
小区7号楼
卖家了昵称                          和白立洗发液
下单时间                              2026-09-03 11:38:58
付款时间                              2026-09-03 11:39:15
发货时间                               2026-09-03 12:45:48
成交时间                               2026-09-08 23:48:31
2EEEEEEEe
吃，            更多      查看钱款  2
联系卖家
`
	huawei := `22:53                          :多 全包
《                                           @@@
站                           -
轿 评价完成，感谢使用闲鱼~
向        华为AirEngine 9700S-S无线接入 x688.00
         控制器，企业级
成交价 (已到达卖家账户)                 xY688.00
订单编号                                             3316880893003013185 复制
交易快照              发生交易争议时，可作为判断依据
支付宝交易号          2026092123001170521426502437 | 复制
收货地址 “”徐洪生 18723957921 新疆维吾尔自治区阿 | 复制
拉尔市幸福路街道幸福北路鸿添南国风光
卖家昵称                                      三国圆圆的蛋黄
下单时间                                                 2026-09-21 14:56:27
付款时间                  2026-09-21 14:56:36
发货时间                  2026-09-21 19:59:13
成交时间                         2026-09-26 19:19:16
联系卖家
`
	cases := []struct {
		name, text string
		wantShip   float64
		wantOrder  string
		wantCP     string // 子串
		noJunk     []string
	}{
		{name: "华硕PN41-真实OCR", text: pn41, wantShip: 0, wantOrder: "3317146572100013185", wantCP: "给", noJunk: []string{"闲"}},
		{name: "MikroTik-真实OCR(支付优惠非运费)", text: mikrotik, wantShip: 0, wantOrder: "3316830241024013185", wantCP: "白立洗发液", noJunk: []string{"分享一下", "特别好"}},
		{name: "华为AirEngine-真实OCR", text: huawei, wantShip: 0, wantOrder: "3316880893003013185", wantCP: "蛋黄", noJunk: []string{"闲"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseText(c.text)
			if got.Shipping != c.wantShip {
				t.Errorf("shipping=%v want %v（支付优惠/−¥ 不能漏进运费）", got.Shipping, c.wantShip)
			}
			if !strings.Contains(got.OrderNo, c.wantOrder) {
				t.Errorf("order_no=%q want contains %q", got.OrderNo, c.wantOrder)
			}
			if !strings.Contains(got.Counterparty, c.wantCP) {
				t.Errorf("counterparty=%q want contains %q（卖家了昵称容错）", got.Counterparty, c.wantCP)
			}
			for _, j := range c.noJunk {
				if strings.Contains(got.Product, j) {
					t.Errorf("product=%q 不应包含垃圾 %q", got.Product, j)
				}
			}
			t.Logf("side=%s product=%q ship=%v order=%q cp=%q time=%q", got.Side, got.Product, got.Shipping, got.OrderNo, got.Counterparty, got.DealTime)
		})
	}
}

// 服务费漏识别兜底 & 到手价校正：成交价 > 到手价 时反推服务费；
// 手续费已识别但到手价缺失/误填成成交价时，重算到手价。
func TestDeriveFeeIfMissing(t *testing.T) {
	// 1) 视觉/本地识别漏了手续费行，但成交价与到手价都在 → 反推 fee = 1199 - 1167.8 = 31.2
	r := parseText("成交价 ¥1199.00 预估到手 ¥1167.80")
	if r.Fee != 31.2 {
		t.Errorf("漏识服务费反推失败: fee=%v want 31.2", r.Fee)
	}
	if r.NetPrice != 1167.8 {
		t.Errorf("net=%v want 1167.8", r.NetPrice)
	}
	// 2) 手续费已正确识别，到手价也正确 → 都不应被覆盖
	r2 := parseText("成交价 ¥700.00 软件服务费 -¥11.20 无忧卖类目软件服务费 -¥7.00 预估到手 ¥681.80")
	if r2.Fee != 11.2 || r2.WuyouFee != 7 || r2.NetPrice != 681.8 {
		t.Errorf("已识别服务费/到手价被覆盖: fee=%v wuyou=%v net=%v", r2.Fee, r2.WuyouFee, r2.NetPrice)
	}
	// 3) 手续费已识别但到手价被误填成成交价 → 重算到手价 = 1199 - 19.1 - 11.99 = 1167.91
	r5 := parseText("成交价 ¥1199.00 软件服务费 -¥19.10 无忧卖类目软件服务费 -¥11.99 预估到手 ¥1199.00")
	if r5.NetPrice != 1167.91 {
		t.Errorf("到手价未校正: net=%v want 1167.91", r5.NetPrice)
	}
	if r5.Fee != 19.1 || r5.WuyouFee != 11.99 {
		t.Errorf("手续费被覆盖: fee=%v wuyou=%v", r5.Fee, r5.WuyouFee)
	}
	// 4) 手续费已识别但到手价缺失 → 重算到手价
	r6 := parseText("成交价 ¥1199.00 软件服务费 -¥19.10 无忧卖类目软件服务费 -¥11.99")
	if r6.NetPrice != 1167.91 {
		t.Errorf("到手价缺失未重算: net=%v want 1167.91", r6.NetPrice)
	}
	// 5) 买进侧不处理
	r3 := parseText("卖家昵称 张三 成交价 ¥500.00 商品总价 ¥500.00")
	if r3.Side != "buy" || r3.Fee != 0 {
		t.Errorf("买进侧不应处理: side=%v fee=%v", r3.Side, r3.Fee)
	}
	// 6) 成交价==到手价（无服务费）→ 不反推，保持 0
	r4 := parseText("成交价 ¥1199.00 预估到手 ¥1199.00")
	if r4.Fee != 0 {
		t.Errorf("无服务费不应反推: fee=%v", r4.Fee)
	}
	// 7) 成交价与到手价只差卖运费 → 运费不能反推成服务费
	r7 := parseText("成交价 ¥700.00 运费 ¥4.00 预估到手 ¥696.00")
	if r7.Shipping != 4 || r7.Fee != 0 || r7.NetPrice != 696 {
		t.Errorf("运费被误判为服务费: shipping=%v fee=%v net=%v", r7.Shipping, r7.Fee, r7.NetPrice)
	}
}
