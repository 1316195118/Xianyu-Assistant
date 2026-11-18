package main

// 由「乱字符暴力测试」发现的两类问题的回归测试：
//   - NUL / C0 / DEL / C1 控制字符不应写入数据库
//   - CSV 导出不得把 = + - @ 开头的文本原样输出（公式注入）

import (
	"encoding/csv"
	"strings"
	"testing"
)

func TestCleanControlChars(t *testing.T) {
	cases := []struct{ in, want string }{
		{"A\x00B\x01C\x7f", "ABC"},
		{"A\r\nB\tC\x0b\x0c", "A\r\nB\tC"},
		{"a\u0080b\u009fc", "abc"},
		{"正常的商品 😀👨‍👩‍👧‍👦", "正常的商品 😀👨‍👩‍👧‍👦"},
		{"a\u200bb\u202ec", "a\u200bb\u202ec"}, // 零宽 / RTL 是合法可见内容，不清理
		{"", ""},
	}
	for _, c := range cases {
		if got := cleanControlChars(c.in); got != c.want {
			t.Errorf("cleanControlChars(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCSVSafe(t *testing.T) {
	cases := []struct{ in, want string }{
		{"=1+1", "'=1+1"},
		{"@SUM(A1:A9)", "'@SUM(A1:A9)"},
		{"+cmd|'/C calc'!A0", "'+cmd|'/C calc'!A0"},
		{"-1+1", "'-1+1"},
		{"-100.00", "-100.00"},   // 合法负金额不转义
		{"1.00", "1.00"},         // 合法正数不转义
		{"-1e3", "-1e3"},         // 科学计数数值不转义
		{"正常商品", "正常商品"},
		{"", ""},
	}
	for _, c := range cases {
		if got := csvSafe(c.in); got != c.want {
			t.Errorf("csvSafe(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestWriteCSVRowEscapes(t *testing.T) {
	var buf strings.Builder
	cw := csv.NewWriter(&buf)
	writeCSVRow(cw, []string{"=1+1", "-12.50", "@SUM(1)", "普通"})
	cw.Flush()
	out := buf.String()
	if !strings.Contains(out, "'=1+1") || !strings.Contains(out, "'@SUM(1)") {
		t.Errorf("公式未转义: %q", out)
	}
	if !strings.Contains(out, "-12.50") || strings.Contains(out, "'-12.50") {
		t.Errorf("合法负数被误转义: %q", out)
	}
}

func TestCreateFiltersControlChars(t *testing.T) {
	h := newExtrasEnv(t)
	body := map[string]interface{}{
		"product": "A\x00B\x01C\x7f", "note": "x\u0080y",
		"buy_order_no": "O\x00R", "buy_counterparty": "卖家\u009f",
		"side": "", "buy_price": 1, "sell_price": 2, "sell_net": 2,
		"quantity": 1, "no_merge": true,
	}
	w := doExtrasReq(h, "POST", "/api/records", body)
	if w.Code != 200 {
		t.Fatalf("POST /api/records = %d %s", w.Code, w.Body.String())
	}
	m := decodeMap(t, w)
	item, _ := m["item"].(map[string]interface{})
	if item == nil {
		t.Fatalf("响应缺少 item: %s", w.Body.String())
	}
	for _, k := range []string{"product", "note", "buy_order_no", "buy_counterparty"} {
		v, _ := item[k].(string)
		if strings.ContainsRune(v, '\x00') || strings.ContainsRune(v, '\x7f') ||
			strings.Contains(v, "\u0080") || strings.Contains(v, "\u009f") {
			t.Errorf("字段 %s 仍含控制字符: %q", k, v)
		}
	}
	if item["product"] != "ABC" {
		t.Errorf("product 清理结果 = %q, want ABC", item["product"])
	}
}

func TestExportCSVEscapesFormula(t *testing.T) {
	h := newExtrasEnv(t)
	insertExtrasItem(t, &Item{Product: "=1+1+cmd", Note: "@SUM(1+1)",
		BuyPrice: 1, SellPrice: 2, SellNet: 2, Quantity: 1})
	for _, path := range []string{
		"/api/export?format=csv",
		"/api/export?format=csv&columns=product,note,buy_price",
		"/api/export?format=profit",
	} {
		w := doExtrasReq(h, "GET", path, nil)
		if w.Code != 200 {
			t.Errorf("GET %s = %d", path, w.Code)
			continue
		}
		body := w.Body.String()
		if strings.Contains(body, `"=1+1`) || strings.Contains(body, ",=1+1") {
			t.Errorf("%s 存在裸公式单元格: %s", path, body[:min(300, len(body))])
		}
		if !strings.Contains(body, "'=1+1") {
			t.Errorf("%s 公式未加安全前缀: %s", path, body[:min(300, len(body))])
		}
	}
}

func min(a, b int) int { if a < b { return a }; return b }
