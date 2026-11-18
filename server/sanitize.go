package main

// 输入卫生与导出安全：由「乱字符暴力测试」暴露的两类问题收敛到这里。
//  1. C0/DEL/C1 控制字符（含 NUL）不应写进 SQLite TEXT，否则导出、日志、
//     终端查看都可能出现不可见字符或截断；\t \n \r 属正常文本予以保留。
//  2. CSV 公式注入：Excel/WPS 打开 CSV 时会把以 = + - @ 开头的单元格当公式执行，
//     导出前统一前置单引号；纯数字（含负数）不转义，避免破坏金额列。

import (
	"encoding/csv"
	"strconv"
	"strings"
)

// cleanControlChars 去掉 C0（保留 \t\n\r）、DEL 与 C1 控制字符。
// 零宽字符、RTL 覆盖符、组合字符、emoji 等都是合法可见内容，不做过滤。
func cleanControlChars(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	changed := false
	for _, r := range s {
		if (r < 0x20 && r != '\t' && r != '\n' && r != '\r') || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			changed = true
			continue
		}
		b.WriteRune(r)
	}
	if !changed {
		return s
	}
	return b.String()
}

// 字段长度上限：超长文本会撑大数据库、拖慢列表渲染，也会让导出文件异常。
// 取值远大于正常使用（商品名一般 20-60 字），只拦明显异常的输入。
const (
	maxLenProduct      = 200
	maxLenNote         = 2000
	maxLenOrderNo      = 100
	maxLenCounterparty = 100
	maxLenTag          = 50
)

func clampRunes(s string, max int) string {
	if max <= 0 || s == "" {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// normalizeTime 把时间统一成 "YYYY-MM-DD HH:MM:SS"。
// 列表排序、日期区间筛选、趋势聚合都按字符串比较，格式不统一会直接算错。
func normalizeTime(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.Replace(s, "T", " ", 1)
	if len(s) == 10 { // 只有日期
		return s + " 00:00:00"
	}
	if len(s) == 16 { // 缺秒
		return s + ":00"
	}
	if len(s) > 19 { // 去掉毫秒/时区后缀
		s = s[:19]
	}
	return s
}

// sanitizeItemInput 对记录输入的全部字符串字段做控制字符清理。
func sanitizeItemInput(in *ItemInput) {
	in.Product = clampRunes(cleanControlChars(in.Product), maxLenProduct)
	in.BuyShipTag = cleanControlChars(in.BuyShipTag)
	in.BuyOrderNo = cleanControlChars(in.BuyOrderNo)
	in.BuyTime = cleanControlChars(in.BuyTime)
	in.BuyCounterparty = cleanControlChars(in.BuyCounterparty)
	in.SellShipTag = cleanControlChars(in.SellShipTag)
	in.SellOrderNo = cleanControlChars(in.SellOrderNo)
	in.SellTime = cleanControlChars(in.SellTime)
	in.SellCounterparty = cleanControlChars(in.SellCounterparty)
	in.Note = clampRunes(cleanControlChars(in.Note), maxLenNote)
	in.BuyOrderNo = clampRunes(in.BuyOrderNo, maxLenOrderNo)
	in.SellOrderNo = clampRunes(in.SellOrderNo, maxLenOrderNo)
	in.BuyCounterparty = clampRunes(in.BuyCounterparty, maxLenCounterparty)
	in.SellCounterparty = clampRunes(in.SellCounterparty, maxLenCounterparty)
	in.BuyShipTag = clampRunes(in.BuyShipTag, maxLenTag)
	in.SellShipTag = clampRunes(in.SellShipTag, maxLenTag)
	in.BuyTime = normalizeTime(in.BuyTime)
	in.SellTime = normalizeTime(in.SellTime)
}

// sanitizeItem 供 /api/import 使用：导入的 JSON 绕过 ItemInput，需单独清理。
func sanitizeItem(it *Item) {
	it.Product = cleanControlChars(it.Product)
	it.BuyShipTag = cleanControlChars(it.BuyShipTag)
	it.BuyOrderNo = cleanControlChars(it.BuyOrderNo)
	it.BuyTime = cleanControlChars(it.BuyTime)
	it.BuyCounterparty = cleanControlChars(it.BuyCounterparty)
	it.SellShipTag = cleanControlChars(it.SellShipTag)
	it.SellOrderNo = cleanControlChars(it.SellOrderNo)
	it.SellTime = cleanControlChars(it.SellTime)
	it.SellCounterparty = cleanControlChars(it.SellCounterparty)
	it.Product = clampRunes(it.Product, maxLenProduct)
	it.Note = clampRunes(it.Note, maxLenNote)
	it.BuyOrderNo = clampRunes(it.BuyOrderNo, maxLenOrderNo)
	it.SellOrderNo = clampRunes(it.SellOrderNo, maxLenOrderNo)
	it.BuyCounterparty = clampRunes(it.BuyCounterparty, maxLenCounterparty)
	it.SellCounterparty = clampRunes(it.SellCounterparty, maxLenCounterparty)
	it.BuyShipTag = clampRunes(it.BuyShipTag, maxLenTag)
	it.SellShipTag = clampRunes(it.SellShipTag, maxLenTag)
	it.BuyTime = normalizeTime(it.BuyTime)
	it.SellTime = normalizeTime(it.SellTime)
	it.CreatedAt = cleanControlChars(it.CreatedAt)
	it.UpdatedAt = cleanControlChars(it.UpdatedAt)
}

// csvSafe 给可能被表格软件解释为公式的单元格加前缀单引号。
func csvSafe(s string) string {
	if s == "" {
		return s
	}
	c := s[0]
	if c != '=' && c != '+' && c != '-' && c != '@' && c != '\t' && c != '\r' {
		return s
	}
	// 纯数字（含负号、科学计数）是合法金额，不转义
	if _, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return s
	}
	return "'" + s
}

// writeCSVRow 统一走安全转义后再写行，所有 CSV 导出都应使用它。
func writeCSVRow(cw *csv.Writer, row []string) {
	out := make([]string, len(row))
	for i, v := range row {
		out[i] = csvSafe(v)
	}
	cw.Write(out)
}
