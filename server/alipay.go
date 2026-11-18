package main

// ============================================================
// 支付宝账单导入
//
// 目的：闲鱼的「支出账单」接口按自然月出账、且有 3 天延迟，
// 当月的单往往拿不到手续费；但支付宝的交易明细是实时入账的，
// 其中 `分账-软件服务费(订单号)扣款`、`分账-基础软件服务费(订单号)扣款`、
// `分账-闲鱼无忧卖类目软件服务费(订单号)扣款` 三条记录直接带着闲鱼订单号，
// 金额就是实际扣款，比任何估算都准。
//
// 运费的情况不同：`闲鱼寄件-寄件费` / `运费` 这类记录里**没有订单号**，
// 只有揽件时间。所以运费用「时间就近」推测，界面上标成「推测」，可逐条取消。
// ============================================================

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------- 极简 xlsx 读取（只读第一个工作表的文本值） ----------

type xlsxSheet struct {
	rows [][]string
}

func readXLSX(r io.ReaderAt, size int64) (*xlsxSheet, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("不是有效的 xlsx 文件")
	}
	var shared []string
	var sheetXML []byte
	for _, f := range zr.File {
		name := strings.ToLower(f.Name)
		if name == "xl/sharedstrings.xml" {
			b, e := readZipFile(f)
			if e != nil {
				return nil, e
			}
			shared = parseSharedStrings(b)
			continue
		}
		// 第一个工作表（严格按 sheet1.xml，支付宝导出只会有一张表）
		if name == "xl/worksheets/sheet1.xml" {
			b, e := readZipFile(f)
			if e != nil {
				return nil, e
			}
			sheetXML = b
		}
	}
	if len(sheetXML) == 0 {
		return nil, fmt.Errorf("xlsx 里没有找到工作表")
	}
	return &xlsxSheet{rows: parseSheetRows(sheetXML, shared)}, nil
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, 32<<20))
}

func parseSharedStrings(b []byte) []string {
	var out []string
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	var cur strings.Builder
	inSI, inT := false, false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				cur.Reset()
			case "t":
				if inSI {
					inT = true
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				inSI = false
				out = append(out, cur.String())
			case "t":
				inT = false
			}
		case xml.CharData:
			if inT {
				cur.Write(t)
			}
		}
	}
	return out
}

func colIndexOf(ref string) int {
	n := 0
	for _, c := range ref {
		if c >= 'A' && c <= 'Z' {
			n = n*26 + int(c-'A'+1)
		} else if c >= 'a' && c <= 'z' {
			n = n*26 + int(c-'a'+1)
		} else {
			break
		}
	}
	if n <= 0 {
		return 0
	}
	return n - 1
}

func parseSheetRows(b []byte, shared []string) [][]string {
	var rows [][]string
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	var cur []string
	var col int
	var val strings.Builder
	inRow, inCell, inV, inIS := false, false, false, false
	cellType := ""
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				inRow = true
				cur = nil
			case "c":
				inCell = true
				cellType = ""
				col = -1
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "r":
						col = colIndexOf(a.Value)
					case "t":
						cellType = a.Value
					}
				}
				val.Reset()
			case "v":
				if inCell {
					inV = true
				}
			case "is":
				if inCell {
					inIS = true
				}
			case "t":
				if inIS {
					inV = true
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "row":
				inRow = false
				rows = append(rows, cur)
			case "c":
				if inCell && col >= 0 {
					for len(cur) <= col {
						cur = append(cur, "")
					}
					s := val.String()
					if cellType == "s" {
						if i, e := strconv.Atoi(strings.TrimSpace(s)); e == nil && i >= 0 && i < len(shared) {
							s = shared[i]
						}
					}
					cur[col] = strings.TrimSpace(s)
				}
				inCell = false
			case "v":
				inV = false
			case "is":
				inIS = false
			}
		case xml.CharData:
			if inV {
				val.Write(t)
			}
		}
	}
	_ = inRow
	return rows
}

// ---------- 支付宝明细解析 ----------

type aliTxn struct {
	Time   string
	Cat    string
	Peer   string
	Desc   string
	Dir    string // 收入 / 支出 / 不计收支
	Amount float64
	Status string
}

// parseAlipayDetail 从表格里找出表头并解析成交易列表。
// 支付宝导出的前 20 多行是导出信息与提示，表头以「交易时间」开头，需要按内容定位。
func parseAlipayDetail(sh *xlsxSheet) ([]aliTxn, error) {
	headerRow := -1
	idx := map[string]int{}
	for i, row := range sh.rows {
		if len(row) < 6 {
			continue
		}
		if strings.TrimSpace(row[0]) == "交易时间" && strings.Contains(strings.Join(row, "|"), "商品说明") {
			headerRow = i
			for j, c := range row {
				idx[strings.TrimSpace(c)] = j
			}
			break
		}
	}
	if headerRow < 0 {
		return nil, fmt.Errorf("没找到表头（应为「交易时间 / 交易分类 / 交易对方 / 商品说明 / 收/支 / 金额…」），请确认导出的就是支付宝交易明细")
	}
	need := []string{"交易时间", "商品说明", "金额"}
	for _, k := range need {
		if _, ok := idx[k]; !ok {
			return nil, fmt.Errorf("表头缺少「%s」列", k)
		}
	}
	get := func(row []string, key string) string {
		i, ok := idx[key]
		if !ok || i < 0 || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}
	out := make([]aliTxn, 0, len(sh.rows)-headerRow)
	for _, row := range sh.rows[headerRow+1:] {
		if len(row) == 0 {
			continue
		}
		desc := get(row, "商品说明")
		if desc == "" {
			continue
		}
		t := aliTxn{
			Time:   aliTimeCell(get(row, "交易时间")),
			Cat:    get(row, "交易分类"),
			Peer:   get(row, "交易对方"),
			Desc:   desc,
			Dir:    get(row, "收/支"),
			Status: get(row, "交易状态"),
		}
		amt := strings.ReplaceAll(get(row, "金额"), ",", "")
		t.Amount, _ = strconv.ParseFloat(strings.TrimSpace(amt), 64)
		out = append(out, t)
	}
	return out, nil
}

// ---------- 汇总：按订单号抽手续费，按时间抽运费 ----------

type aliFeeAgg struct {
	OrderNo string
	Tech    float64 // 软件服务费
	Basic   float64 // 基础软件服务费
	Wuyou   float64 // 无忧卖类目软件服务费
	Other   float64 // 其它分账（闲鱼币智能软件服务费等）
	Refund  float64 // 退费（收入方向）
	Time    time.Time // 最早一笔扣费时间（比成交晚 1~2 分钟，用来兜底匹配）
}

type aliShip struct {
	Time   time.Time
	Amount float64
	Desc   string
}

// aliTimeCell 支付宝导出的「交易时间」在 xlsx 里是 Excel 日期序列号（如 46340.6700347222），
// 极简读取器只拿到原始值，这里统一还原成 "2006-01-02 15:04:05"。
// 不还原的话时间全是 0，运费用「就近时间」匹配会一笔都对不上。
func aliTimeCell(v string) string {
	v = strings.TrimSpace(v)
	if f, err := strconv.ParseFloat(v, 64); err == nil && f > 20000 && f < 80000 {
		sec := int64(math.Round(f * 86400))
		t := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC).Add(time.Duration(sec) * time.Second)
		return t.Format("2006-01-02 15:04:05")
	}
	return v
}

func aliParseTime(s string) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006/01/02 15:04:05"} {
		if t, e := time.ParseInLocation(layout, strings.TrimSpace(s), time.Local); e == nil {
			return t
		}
	}
	return time.Time{}
}

// orderNoInDesc 从「分账-软件服务费(3316585406014013983)扣款」里取出 19 位订单号
func orderNoInDesc(desc string) string {
	open := strings.Index(desc, "(")
	closeIdx := strings.Index(desc, ")")
	if open < 0 || closeIdx <= open {
		return ""
	}
	s := desc[open+1 : closeIdx]
	if len(s) < 12 {
		return ""
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return s
}

func collectFees(txns []aliTxn) map[string]*aliFeeAgg {
	out := map[string]*aliFeeAgg{}
	for _, t := range txns {
		if !strings.HasPrefix(t.Desc, "分账-") {
			continue
		}
		if strings.Contains(t.Status, "关闭") {
			continue
		}
		no := orderNoInDesc(t.Desc)
		if no == "" {
			continue
		}
		a := out[no]
		if a == nil {
			a = &aliFeeAgg{OrderNo: no}
			out[no] = a
		}
		if tm := aliParseTime(t.Time); !tm.IsZero() && (a.Time.IsZero() || tm.Before(a.Time)) {
			a.Time = tm
		}
		sign := 1.0
		if t.Dir == "收入" {
			sign = -1 // 退费冲抵
		}
		amt := math.Abs(t.Amount) * sign
		switch {
		case strings.Contains(t.Desc, "无忧卖"):
			a.Wuyou += amt
		case strings.Contains(t.Desc, "基础软件服务费"):
			a.Basic += amt
		case strings.Contains(t.Desc, "软件服务费"):
			a.Tech += amt
		default:
			a.Other += amt
		}
	}
	return out
}

func collectShipping(txns []aliTxn) []aliShip {
	out := []aliShip{}
	for _, t := range txns {
		if t.Dir != "支出" || strings.Contains(t.Status, "关闭") {
			continue
		}
		isShip := strings.Contains(t.Desc, "寄件费") ||
			(strings.Contains(t.Desc, "运费") && !strings.Contains(t.Desc, "运费险")) ||
			strings.Contains(t.Peer, "物流") || strings.Contains(t.Peer, "速运") || strings.Contains(t.Peer, "快递")
		if !isShip {
			continue
		}
		tm := aliParseTime(t.Time)
		if tm.IsZero() || t.Amount <= 0 {
			continue
		}
		out = append(out, aliShip{Time: tm, Amount: t.Amount, Desc: t.Desc})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}
