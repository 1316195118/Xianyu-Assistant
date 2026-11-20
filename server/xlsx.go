package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// ============================================================
// 极简 xlsx 写入器（只依赖标准库）
//
// 为什么不用现成的库：
//   - 前端 SheetJS 社区版只能写「值」不能写「样式」，做不出带表头配色、
//     冻结窗格、数字格式的表格；
//   - Go 侧引入 excelize 会给单文件二进制再加近 10MB，而这里只需要
//     「一张表 + 一套固定样式」。
// 手写 OOXML 只需拼 5 个小 XML，代价最小，样式完全可控。
// ============================================================

type xlCellKind int

const (
	xlText xlCellKind = iota
	xlMoney
	xlInt
	xlStatus
	xlTime
)

// xlCol 是一列的定义；CSV 与 Excel 共用同一份定义，保证两种导出的列序、
// 列名、空值处理完全一致，用户拿到哪个文件都对得上。
type xlCol struct {
	header string
	width  float64
	kind   xlCellKind
	// zeroBlank 为 true 时，数值 0 输出为空单元格（未发生的买/卖金额），
	// 满屏 0.00 既难读也容易被误当成真实发生额。
	zeroBlank bool
	value     func(Item) string
	number    func(Item) float64
}

func f2s(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

var itemColumns = []xlCol{
	{"ID", 6, xlInt, false, func(it Item) string { return strconv.FormatInt(it.ID, 10) }, func(it Item) float64 { return float64(it.ID) }},
	{"状态", 11, xlStatus, false, func(it Item) string { return statusLabel(it.Status) }, nil},
	{"商品名称", 30, xlText, false, func(it Item) string { return it.Product }, nil},
	{"买进价", 10, xlMoney, true, func(it Item) string { return f2s(it.BuyPrice) }, func(it Item) float64 { return it.BuyPrice }},
	{"买进运费", 10, xlMoney, true, func(it Item) string { return f2s(it.BuyShipping) }, func(it Item) float64 { return it.BuyShipping }},
	{"买运费标签", 11, xlText, false, func(it Item) string { return it.BuyShipTag }, nil},
	{"买订单号", 21, xlText, false, func(it Item) string { return it.BuyOrderNo }, nil},
	{"买进时间", 18, xlTime, false, func(it Item) string { return it.BuyTime }, nil},
	{"卖家昵称", 14, xlText, false, func(it Item) string { return it.BuyCounterparty }, nil},
	{"卖出成交价", 12, xlMoney, true, func(it Item) string { return f2s(it.SellPrice) }, func(it Item) float64 { return it.SellPrice }},
	{"软件服务费", 11, xlMoney, true, func(it Item) string { return f2s(it.SellFee) }, func(it Item) float64 { return it.SellFee }},
	{"无忧卖类目服务费", 16, xlMoney, true, func(it Item) string { return f2s(it.SellWuyouFee) }, func(it Item) float64 { return it.SellWuyouFee }},
	{"卖出到手", 11, xlMoney, true, func(it Item) string { return f2s(it.SellNet) }, func(it Item) float64 { return it.SellNet }},
	{"卖出运费", 10, xlMoney, true, func(it Item) string { return f2s(it.SellShipping) }, func(it Item) float64 { return it.SellShipping }},
	{"卖运费标签", 11, xlText, false, func(it Item) string { return it.SellShipTag }, nil},
	{"卖订单号", 21, xlText, false, func(it Item) string { return it.SellOrderNo }, nil},
	{"卖出时间", 18, xlTime, false, func(it Item) string { return it.SellTime }, nil},
	{"买家昵称", 14, xlText, false, func(it Item) string { return it.SellCounterparty }, nil},
	{"总成本", 11, xlMoney, false, func(it Item) string { return f2s(it.Cost) }, func(it Item) float64 { return it.Cost }},
	{"利润", 11, xlMoney, false, func(it Item) string { return f2s(it.Profit) }, func(it Item) float64 { return it.Profit }},
	{"备注", 26, xlText, false, func(it Item) string { return it.Note }, nil},
	{"创建时间", 20, xlTime, false, func(it Item) string { return it.CreatedAt }, nil},
}

func statusLabel(s string) string {
	switch s {
	case "sold":
		return "已成交"
	case "pending":
		return "待售"
	case "sell_only":
		return "待补买进"
	}
	return s
}

// ---------------- 样式表 ----------------

type xlFont struct {
	bold, italic bool
	color        string
	size         float64
}
type xlFill struct{ color string } // 空串表示无填充
type xlBorder struct {
	bottom, top, color string
	style              string
}
type xlXF struct {
	font, fill, border int
	numFmt             string
	halign             string
	wrap               bool
}

type xlStyleBook struct {
	fonts    []xlFont
	fills    []xlFill
	borders  []xlBorder
	xfs      []xlXF
	fontIx   map[string]int
	fillIx   map[string]int
	borderIx map[string]int
}

func newXLStyleBook() *xlStyleBook {
	s := &xlStyleBook{fontIx: map[string]int{}, fillIx: map[string]int{}, borderIx: map[string]int{}}
	// 0 号字体/填充/边框是 OOXML 的强制占位
	s.font(xlFont{size: 11, color: "FF1F2329"})
	s.fill(xlFill{})
	s.fill(xlFill{color: "FFEDEDED"})
	s.border(xlBorder{})
	return s
}

func (s *xlStyleBook) font(f xlFont) int {
	if f.size == 0 {
		f.size = 11
	}
	if f.color == "" {
		f.color = "FF1F2329"
	}
	k := fmt.Sprintf("%v|%v|%s|%g", f.bold, f.italic, f.color, f.size)
	if i, ok := s.fontIx[k]; ok {
		return i
	}
	s.fonts = append(s.fonts, f)
	s.fontIx[k] = len(s.fonts) - 1
	return len(s.fonts) - 1
}

func (s *xlStyleBook) fill(f xlFill) int {
	if i, ok := s.fillIx[f.color]; ok {
		return i
	}
	s.fills = append(s.fills, f)
	s.fillIx[f.color] = len(s.fills) - 1
	return len(s.fills) - 1
}

func (s *xlStyleBook) border(b xlBorder) int {
	k := b.bottom + "|" + b.top + "|" + b.color + "|" + b.style
	if i, ok := s.borderIx[k]; ok {
		return i
	}
	s.borders = append(s.borders, b)
	s.borderIx[k] = len(s.borders) - 1
	return len(s.borders) - 1
}

func (s *xlStyleBook) xf(f xlFont, fl xlFill, b xlBorder, numFmt, halign string) int {
	x := xlXF{font: s.font(f), fill: s.fill(fl), border: s.border(b), numFmt: numFmt, halign: halign}
	for i, o := range s.xfs {
		if o == x {
			return i
		}
	}
	s.xfs = append(s.xfs, x)
	return len(s.xfs) - 1
}

func (s *xlStyleBook) xml() string {
	var b strings.Builder
	b.WriteString(xml.Header)
	b.WriteString(`<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)

	// 自定义数字格式从 164 起（<164 是内置 id）
	numFmts := []string{}
	for _, x := range s.xfs {
		if x.numFmt != "" {
			found := false
			for _, n := range numFmts {
				if n == x.numFmt {
					found = true
					break
				}
			}
			if !found {
				numFmts = append(numFmts, x.numFmt)
			}
		}
	}
	if len(numFmts) > 0 {
		fmt.Fprintf(&b, `<numFmts count="%d">`, len(numFmts))
		for i, n := range numFmts {
			fmt.Fprintf(&b, `<numFmt numFmtId="%d" formatCode="%s"/>`, 164+i, xmlEscape(n))
		}
		b.WriteString(`</numFmts>`)
	}

	fmt.Fprintf(&b, `<fonts count="%d">`, len(s.fonts))
	for _, f := range s.fonts {
		b.WriteString(`<font>`)
		if f.bold {
			b.WriteString(`<b/>`)
		}
		if f.italic {
			b.WriteString(`<i/>`)
		}
		fmt.Fprintf(&b, `<sz val="%g"/><color rgb="%s"/><name val="Microsoft YaHei"/></font>`, f.size, f.color)
	}
	b.WriteString(`</fonts>`)

	fmt.Fprintf(&b, `<fills count="%d">`, len(s.fills))
	for i, f := range s.fills {
		if i == 0 {
			b.WriteString(`<fill><patternFill patternType="none"/></fill>`)
			continue
		}
		if f.color == "" {
			b.WriteString(`<fill><patternFill patternType="gray125"/></fill>`)
			continue
		}
		fmt.Fprintf(&b, `<fill><patternFill patternType="solid"><fgColor rgb="%s"/><bgColor indexed="64"/></patternFill></fill>`, f.color)
	}
	b.WriteString(`</fills>`)

	fmt.Fprintf(&b, `<borders count="%d">`, len(s.borders))
	for _, bd := range s.borders {
		b.WriteString(`<border><left/><right/><top`)
		if bd.top != "" {
			fmt.Fprintf(&b, ` style="%s"><color rgb="%s"/></top>`, bd.top, bd.color)
		} else {
			b.WriteString(`/>`)
		}
		b.WriteString(`<bottom`)
		if bd.bottom != "" {
			fmt.Fprintf(&b, ` style="%s"><color rgb="%s"/></bottom>`, bd.bottom, bd.color)
		} else {
			b.WriteString(`/>`)
		}
		b.WriteString(`<diagonal/></border>`)
	}
	b.WriteString(`</borders>`)

	b.WriteString(`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>`)
	fmt.Fprintf(&b, `<cellXfs count="%d">`, len(s.xfs))
	for _, x := range s.xfs {
		numFmtID := 0
		if x.numFmt != "" {
			for i, n := range numFmts {
				if n == x.numFmt {
					numFmtID = 164 + i
					break
				}
			}
		}
		fmt.Fprintf(&b, `<xf numFmtId="%d" fontId="%d" fillId="%d" borderId="%d" xfId="0"`, numFmtID, x.font, x.fill, x.border)
		if numFmtID != 0 {
			b.WriteString(` applyNumberFormat="1"`)
		}
		if x.font != 0 {
			b.WriteString(` applyFont="1"`)
		}
		if x.fill != 0 {
			b.WriteString(` applyFill="1"`)
		}
		if x.border != 0 {
			b.WriteString(` applyBorder="1"`)
		}
		if x.halign != "" || x.wrap {
			b.WriteString(` applyAlignment="1"`)
		}
		if x.halign != "" || x.wrap {
			fmt.Fprintf(&b, `><alignment horizontal="%s"%s vertical="center"/></xf>`, x.halign, map[bool]string{true: ` wrapText="1"`, false: ""}[x.wrap])
		} else {
			b.WriteString(`/>`)
		}
	}
	b.WriteString(`</cellXfs>`)
	// cellStyles 是 OOXML 的必填小节：缺了它 Excel 能开，但读取库会报
	// 「no default style」，也容易触发「文件需要修复」的提示。
	b.WriteString(`<cellStyles count="1"><cellStyle name="常规" xfId="0" builtinId="0"/></cellStyles>`)
	b.WriteString(`<dxfs count="0"/>`)
	b.WriteString(`<tableStyles count="0" defaultTableStyle="TableStyleMedium2" defaultPivotStyle="PivotStyleLight16"/>`)
	b.WriteString(`</styleSheet>`)
	return b.String()
}

// ---------------- 工作表 ----------------

type xlSheet struct {
	name       string
	rows       [][]xlCell
	cols       []float64
	freezeRows int
	autoFilter string
	landscape  bool
}

type xlCell struct {
	text    string
	number  float64
	isNum   bool
	formula string
	style   int
}

func cellText(s string, style int) xlCell { return xlCell{text: s, style: style} }
func cellNum(v float64, style int) xlCell { return xlCell{number: v, isNum: true, style: style} }
func cellSum(f string, style int) xlCell {
	return xlCell{formula: f, number: 0, isNum: true, style: style}
}

func colName(i int) string {
	s := ""
	for i >= 0 {
		s = string(rune('A'+i%26)) + s
		i = i/26 - 1
	}
	return s
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	out := b.String()
	// XML 不允许的控制字符会让 Excel 直接报文件损坏，逐个剔除
	if strings.ContainsAny(out, "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x0b\x0c\x0e\x0f") {
		var sb strings.Builder
		for _, r := range out {
			if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
				continue
			}
			sb.WriteRune(r)
		}
		return sb.String()
	}
	return out
}

func (s *xlSheet) xml(sb *xlStyleBook) string {
	var b strings.Builder
	b.WriteString(xml.Header)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	lastCol := colName(len(s.cols) - 1)
	fmt.Fprintf(&b, `<dimension ref="A1:%s%d"/>`, lastCol, len(s.rows))
	b.WriteString(`<sheetViews><sheetView showGridLines="0" tabSelected="1" workbookViewId="0">`)
	if s.freezeRows > 0 {
		fmt.Fprintf(&b, `<pane ySplit="%d" topLeftCell="A%d" activePane="bottomLeft" state="frozen"/>`, s.freezeRows, s.freezeRows+1)
		fmt.Fprintf(&b, `<selection pane="bottomLeft" activeCell="A%d" sqref="A%d"/>`, s.freezeRows+1, s.freezeRows+1)
	} else {
		b.WriteString(`<selection activeCell="A1" sqref="A1"/>`)
	}
	b.WriteString(`</sheetView></sheetViews>`)
	b.WriteString(`<sheetFormatPr defaultRowHeight="19" baseColWidth="10"/>`)

	if len(s.cols) > 0 {
		b.WriteString(`<cols>`)
		for i, w := range s.cols {
			fmt.Fprintf(&b, `<col min="%d" max="%d" width="%g" customWidth="1"/>`, i+1, i+1, w)
		}
		b.WriteString(`</cols>`)
	}

	b.WriteString(`<sheetData>`)
	for ri, row := range s.rows {
		ht := ""
		if ri == 0 {
			ht = ` ht="30" customHeight="1"`
		}
		fmt.Fprintf(&b, `<row r="%d"%s>`, ri+1, ht)
		for ci, c := range row {
			ref := colName(ci) + strconv.Itoa(ri+1)
			switch {
			case c.formula != "":
				fmt.Fprintf(&b, `<c r="%s" s="%d"><f>%s</f><v>%g</v></c>`, ref, c.style, xmlEscape(c.formula), c.number)
			case c.isNum:
				fmt.Fprintf(&b, `<c r="%s" s="%d"><v>%g</v></c>`, ref, c.style, c.number)
			case c.text != "":
				fmt.Fprintf(&b, `<c r="%s" s="%d" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, c.style, xmlEscape(c.text))
			default:
				fmt.Fprintf(&b, `<c r="%s" s="%d"/>`, ref, c.style)
			}
		}
		b.WriteString(`</row>`)
	}
	b.WriteString(`</sheetData>`)

	if s.autoFilter != "" {
		fmt.Fprintf(&b, `<autoFilter ref="%s"/>`, s.autoFilter)
	}
	b.WriteString(`<pageMargins left="0.4" right="0.4" top="0.5" bottom="0.5" header="0.3" footer="0.3"/>`)
	if s.landscape {
		b.WriteString(`<pageSetup orientation="landscape" paperSize="9" fitToWidth="1" fitToHeight="0"/>`)
	}
	b.WriteString(`</worksheet>`)
	return b.String()
}

// ---------------- 打包 ----------------

func buildXLSX(sheet *xlSheet, styles *xlStyleBook) ([]byte, error) {
	return buildXLSXMulti([]*xlSheet{sheet}, styles)
}

// buildXLSXMulti 生成多 sheet 工作簿：明细 + 月度汇总等。
// 单 sheet 的旧调用走 buildXLSX，行为不变。
func buildXLSXMulti(sheets []*xlSheet, styles *xlStyleBook) ([]byte, error) {
	if len(sheets) == 0 {
		return nil, fmt.Errorf("no sheets")
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	// Content_Types：每个 sheet 一个 Override
	var ct strings.Builder
	ct.WriteString(xml.Header)
	ct.WriteString(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`)
	for i := range sheets {
		ct.WriteString(`<Override PartName="/xl/worksheets/sheet` + fmt.Sprint(i+1) + `.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`)
	}
	ct.WriteString(`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` +
		`</Types>`)

	// workbook.xml：列出全部 sheet
	var wb strings.Builder
	wb.WriteString(xml.Header)
	wb.WriteString(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
		`<workbookPr/><sheets>`)
	for i, sh := range sheets {
		wb.WriteString(`<sheet name="` + xmlEscape(sh.name) + `" sheetId="` + fmt.Sprint(i+1) + `" r:id="rId` + fmt.Sprint(i+1) + `"/>`)
	}
	wb.WriteString(`</sheets><calcPr calcId="171027" fullCalcOnLoad="1"/></workbook>`)

	// workbook rels：sheet 关系 + styles 关系
	var rels strings.Builder
	rels.WriteString(xml.Header)
	rels.WriteString(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for i := range sheets {
		rels.WriteString(`<Relationship Id="rId` + fmt.Sprint(i+1) + `" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet` + fmt.Sprint(i+1) + `.xml"/>`)
	}
	rels.WriteString(`<Relationship Id="rId` + fmt.Sprint(len(sheets)+1) + `" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`)
	rels.WriteString(`</Relationships>`)

	files := []struct{ name, body string }{
		{"[Content_Types].xml", ct.String()},
		{"_rels/.rels", xml.Header + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
			`</Relationships>`},
		{"xl/workbook.xml", wb.String()},
		{"xl/_rels/workbook.xml.rels", rels.String()},
		{"xl/styles.xml", styles.xml()},
	}
	for i, sh := range sheets {
		files = append(files, struct{ name, body string }{"xl/worksheets/sheet" + fmt.Sprint(i+1) + ".xml", sh.xml(styles)})
	}
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---------------- 交易明细工作簿 ----------------

// buildItemsXLSX 生成与 CSV 同列序的明细表，带表头配色、冻结首行、
// 筛选、数字格式、状态配色与合计行。
func buildItemsXLSX(items []Item, sheetName string) ([]byte, error) {
	s := newXLStyleBook()
	sheet := buildItemsSheet(s, items, sheetName)
	return buildXLSX(sheet, s)
}

// buildItemsSheet 只生成明细 sheet（样式注册进传入的 style book），
// 供多 sheet 工作簿与单 sheet 导出共用，避免两套样式索引冲突。
func buildItemsSheet(s *xlStyleBook, items []Item, sheetName string) *xlSheet {

	headerFill := xlFill{color: "FFF7D060"}
	headFont := xlFont{bold: true, color: "FF4A3600", size: 11}
	headBorder := xlBorder{bottom: "medium", color: "FFDDA600", style: "medium"}
	headStyle := s.xf(headFont, headerFill, headBorder, "", "center")

	bodyBorder := xlBorder{bottom: "thin", color: "FFF0F2F5", style: "thin"}
	zebraFill := xlFill{color: "FFFBFAF6"}

	textL := s.xf(xlFont{}, xlFill{}, bodyBorder, "", "left")
	textLZ := s.xf(xlFont{}, zebraFill, bodyBorder, "", "left")
	textC := s.xf(xlFont{}, xlFill{}, bodyBorder, "", "center")
	textCZ := s.xf(xlFont{}, zebraFill, bodyBorder, "", "center")
	moneyR := s.xf(xlFont{}, xlFill{}, bodyBorder, "#,##0.00", "right")
	moneyRZ := s.xf(xlFont{}, zebraFill, bodyBorder, "#,##0.00", "right")
	profitPos := s.xf(xlFont{bold: true, color: "FFE0492E"}, xlFill{}, bodyBorder, "#,##0.00", "right")
	profitPosZ := s.xf(xlFont{bold: true, color: "FFE0492E"}, zebraFill, bodyBorder, "#,##0.00", "right")
	profitNeg := s.xf(xlFont{bold: true, color: "FF1A9C6B"}, xlFill{}, bodyBorder, "#,##0.00", "right")
	profitNegZ := s.xf(xlFont{bold: true, color: "FF1A9C6B"}, zebraFill, bodyBorder, "#,##0.00", "right")
	statusSold := s.xf(xlFont{color: "FF1A9C6B"}, xlFill{}, bodyBorder, "", "center")
	statusSoldZ := s.xf(xlFont{color: "FF1A9C6B"}, zebraFill, bodyBorder, "", "center")
	statusPending := s.xf(xlFont{color: "FFB07A00"}, xlFill{}, bodyBorder, "", "center")
	statusPendingZ := s.xf(xlFont{color: "FFB07A00"}, zebraFill, bodyBorder, "", "center")
	statusOnly := s.xf(xlFont{color: "FF2F6BFF"}, xlFill{}, bodyBorder, "", "center")
	statusOnlyZ := s.xf(xlFont{color: "FF2F6BFF"}, zebraFill, bodyBorder, "", "center")

	totalFill := xlFill{color: "FFF1F3F8"}
	totalTop := xlBorder{top: "medium", color: "FFC9CEDA", style: "medium"}
	totalLabel := s.xf(xlFont{bold: true}, totalFill, totalTop, "", "center")
	totalMoney := s.xf(xlFont{bold: true}, totalFill, totalTop, "#,##0.00", "right")
	totalBlank := s.xf(xlFont{bold: true}, totalFill, totalTop, "", "left")

	rows := make([][]xlCell, 0, len(items)+2)
	head := make([]xlCell, len(itemColumns))
	widths := make([]float64, len(itemColumns))
	for i, c := range itemColumns {
		head[i] = cellText(c.header, headStyle)
		widths[i] = c.width
	}
	rows = append(rows, head)

	moneyCols := []int{}
	for ri, it := range items {
		z := ri%2 == 1
		row := make([]xlCell, len(itemColumns))
		for ci, c := range itemColumns {
			switch c.kind {
			case xlInt:
				st := textCZ
				if !z {
					st = textC
				}
				row[ci] = cellNum(c.number(it), st)
			case xlMoney:
				v := c.number(it)
				if c.zeroBlank && v == 0 {
					st := moneyR
					if z {
						st = moneyRZ
					}
					row[ci] = cellText("", st)
					continue
				}
				st := moneyR
				if z {
					st = moneyRZ
				}
				if c.header == "利润" {
					if v >= 0 {
						st = profitPos
						if z {
							st = profitPosZ
						}
					} else {
						st = profitNeg
						if z {
							st = profitNegZ
						}
					}
				}
				row[ci] = cellNum(v, st)
			case xlStatus:
				st := statusSold
				if z {
					st = statusSoldZ
				}
				switch it.Status {
				case "pending":
					st = statusPending
					if z {
						st = statusPendingZ
					}
				case "sell_only":
					st = statusOnly
					if z {
						st = statusOnlyZ
					}
				}
				row[ci] = cellText(c.value(it), st)
			case xlTime:
				st := textC
				if z {
					st = textCZ
				}
				row[ci] = cellText(c.value(it), st)
			default:
				st := textL
				if z {
					st = textLZ
				}
				row[ci] = cellText(c.value(it), st)
			}
		}
		rows = append(rows, row)
	}
	for ci, c := range itemColumns {
		if c.kind == xlMoney {
			moneyCols = append(moneyCols, ci)
		}
	}

	// 合计行：金额列用 SUM 公式，Excel/WPS 打开即算好，改数据也会自动重算
	last := len(rows)
	total := make([]xlCell, len(itemColumns))
	for i := range total {
		total[i] = cellText("", totalBlank)
	}
	total[0] = cellText("合计", totalLabel)
	total[1] = cellText(fmt.Sprintf("%d 条", len(items)), totalLabel)
	for _, ci := range moneyCols {
		cl := colName(ci)
		total[ci] = cellSum(fmt.Sprintf("SUM(%s2:%s%d)", cl, cl, last), totalMoney)
	}
	rows = append(rows, total)

	sheet := &xlSheet{
		name:       sheetName,
		rows:       rows,
		cols:       widths,
		freezeRows: 1,
		autoFilter: fmt.Sprintf("A1:%s%d", colName(len(itemColumns)-1), last),
		landscape:  true,
	}
	return sheet
}

// setDownloadName 同时给出 ASCII 兜底名与 UTF-8 真名，
// 中文名只在支持 RFC 5987 的浏览器上生效，避免下载成乱码文件。
func setDownloadName(w http.ResponseWriter, ascii, zh string) {
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+ascii+`"; filename*=UTF-8''`+rfc5987Escape(zh))
}

func rfc5987Escape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// buildProductProfitXLSX 商品利润排行导出：一行一个商品，带合计行。
func buildProductProfitXLSX(rows []productProfitRow) ([]byte, error) {
	st := newXLStyleBook()
	headStyle := st.xf(xlFont{bold: true, color: "FF4A3600", size: 11},
		xlFill{color: "FFF7D060"}, xlBorder{bottom: "medium", color: "FFDDA600", style: "medium"}, "", "center")
	body := xlBorder{bottom: "thin", color: "FFF0F2F5", style: "thin"}
	zebra := xlFill{color: "FFFBFAF6"}
	textL := st.xf(xlFont{}, xlFill{}, body, "", "left")
	textLZ := st.xf(xlFont{}, zebra, body, "", "left")
	money := st.xf(xlFont{}, xlFill{}, body, "#,##0.00", "right")
	moneyZ := st.xf(xlFont{}, zebra, body, "#,##0.00", "right")
	numC := st.xf(xlFont{}, xlFill{}, body, "0", "center")
	numCZ := st.xf(xlFont{}, zebra, body, "0", "center")
	pos := st.xf(xlFont{bold: true, color: "FFE0492E"}, xlFill{}, body, "#,##0.00", "right")
	posZ := st.xf(xlFont{bold: true, color: "FFE0492E"}, zebra, body, "#,##0.00", "right")
	neg := st.xf(xlFont{bold: true, color: "FF1A9C6B"}, xlFill{}, body, "#,##0.00", "right")
	negZ := st.xf(xlFont{bold: true, color: "FF1A9C6B"}, zebra, body, "#,##0.00", "right")
	pct := st.xf(xlFont{}, xlFill{}, body, "0.0", "right")
	pctZ := st.xf(xlFont{}, zebra, body, "0.0", "right")

	totalFill := xlFill{color: "FFF1F3F8"}
	totalTop := xlBorder{top: "medium", color: "FFC9CED0", style: "medium"}
	totalL := st.xf(xlFont{bold: true}, totalFill, totalTop, "", "center")
	totalN := st.xf(xlFont{bold: true}, totalFill, totalTop, "#,##0.00", "right")
	totalI := st.xf(xlFont{bold: true}, totalFill, totalTop, "0", "right")

	headers := []string{"商品", "成交单数", "销售额", "已售成本", "利润", "利润率(%)", "在库成本", "未挂成本单数"}
	widths := []float64{42, 10, 14, 14, 14, 12, 14, 14}
	head := make([]xlCell, len(headers))
	for i, h := range headers {
		head[i] = cellText(h, headStyle)
	}
	out := [][]xlCell{head}
	var sumSales, sumCost, sumProfit, sumStock float64
	var sumSold, sumUnlinked int
	for i, r := range rows {
		z := i%2 == 1
		pick := func(a, b int) int {
			if z {
				return b
			}
			return a
		}
		profitStyle := pos
		if z {
			profitStyle = posZ
		}
		if r.Profit < 0 {
			profitStyle = neg
			if z {
				profitStyle = negZ
			}
		}
		out = append(out, []xlCell{
			cellText(r.Product, pick(textL, textLZ)),
			cellNum(float64(r.Sold), pick(numC, numCZ)),
			cellNum(r.Sales, pick(money, moneyZ)),
			cellNum(r.Cost, pick(money, moneyZ)),
			cellNum(r.Profit, profitStyle),
			cellNum(r.Margin, pick(pct, pctZ)),
			cellNum(r.Stock, pick(money, moneyZ)),
			cellNum(float64(r.Unlinked), pick(numC, numCZ)),
		})
		sumSales += r.Sales
		sumCost += r.Cost
		sumProfit += r.Profit
		sumStock += r.Stock
		sumSold += r.Sold
		sumUnlinked += r.Unlinked
	}
	out = append(out, []xlCell{
		cellText("合计", totalL),
		cellNum(float64(sumSold), totalI),
		cellNum(sumSales, totalN),
		cellNum(sumCost, totalN),
		cellNum(sumProfit, totalN),
		cellText("", totalL),
		cellNum(sumStock, totalN),
		cellNum(float64(sumUnlinked), totalI),
	})
	sheet := &xlSheet{
		name: "商品利润", rows: out, cols: widths,
		freezeRows: 1, landscape: true,
		autoFilter: "A1:H" + strconv.Itoa(len(rows)+1),
	}
	return buildXLSX(sheet, st)
}

// buildMonthlySummarySheet 月度汇总 sheet：按成交月份聚合销售额 / 已售成本 / 净利润 / 单数。
// 口径与首页一致：只统计卖单（成交价计），成本 = 卖单带走的单件成本 + 卖运费 + 服务费。
// 联单分摊的单件成本这里用「有效单件成本」：自己填的优先，否则取联单按售价占比分摊的值。
// 为保持 Excel 导出自包含，分摊逻辑在本函数内复刻一份（与 loadGroupAlloc 同口径）。
func buildMonthlySummarySheet(st *xlStyleBook, sid int64, items []Item) *xlSheet {
	headStyle := st.xf(xlFont{bold: true, color: "FF4A3600", size: 11},
		xlFill{color: "FFF7D060"}, xlBorder{bottom: "medium", color: "FFDDA600", style: "medium"}, "", "center")
	body := xlBorder{bottom: "thin", color: "FFF0F2F5", style: "thin"}
	zebra := xlFill{color: "FFFBFAF6"}
	money := st.xf(xlFont{}, xlFill{}, body, "#,##0.00", "right")
	moneyZ := st.xf(xlFont{}, zebra, body, "#,##0.00", "right")
	numC := st.xf(xlFont{}, xlFill{}, body, "0", "center")
	numCZ := st.xf(xlFont{}, zebra, body, "0", "center")
	pos := st.xf(xlFont{bold: true, color: "FFE0492E"}, xlFill{}, body, "#,##0.00", "right")
	posZ := st.xf(xlFont{bold: true, color: "FFE0492E"}, zebra, body, "#,##0.00", "right")
	neg := st.xf(xlFont{bold: true, color: "FF1A9C6B"}, xlFill{}, body, "#,##0.00", "right")
	negZ := st.xf(xlFont{bold: true, color: "FF1A9C6B"}, zebra, body, "#,##0.00", "right")
	textL := st.xf(xlFont{}, xlFill{}, body, "", "left")
	textLZ := st.xf(xlFont{}, zebra, body, "", "left")

	totalFill := xlFill{color: "FFF1F3F8"}
	totalTop := xlBorder{top: "medium", color: "FFC9CED0", style: "medium"}
	totalL := st.xf(xlFont{bold: true}, totalFill, totalTop, "", "left")
	totalN := st.xf(xlFont{bold: true}, totalFill, totalTop, "#,##0.00", "right")
	totalI := st.xf(xlFont{bold: true}, totalFill, totalTop, "0", "center")

	// 1) 联单分摊：直接复用 loadGroupAlloc（同包函数，全店计算后按 id 取值）。
	// 它是内存派生、不写库，与首页/排行的口径完全一致。
	alloc := loadGroupAlloc(sid) // 只算当前店铺的联单分摊，与首页/排行口径一致
	// 2) 聚合
	type monthAgg struct {
		sales, cost, profit float64
		count               int
	}
	byMonth := map[string]*monthAgg{}
	var order []string
	for _, it := range items {
		if it.Kind != "sale" || it.DiscardedAt != "" {
			continue
		}
		tm := it.SellTime
		if tm == "" {
			tm = it.BuyTime
		}
		m := ""
		if len(tm) >= 7 {
			m = tm[:7]
		}
		if m == "" {
			m = "未知月份"
		}
		qty := it.Quantity
		if qty < 1 {
			qty = 1
		}
		unit := it.UnitCost
		if unit <= 0 {
			unit = alloc[it.ID]
		}
		goods := round2(unit * float64(qty))
		fee := it.SellFee + it.SellWuyouFee
		cost := round2(goods + it.SellShipping + fee)
		profit := round2(it.SellPrice - cost)
		a := byMonth[m]
		if a == nil {
			a = &monthAgg{}
			byMonth[m] = a
			order = append(order, m)
		}
		a.sales = round2(a.sales + it.SellPrice)
		a.cost = round2(a.cost + cost)
		a.profit = round2(a.profit + profit)
		a.count++
	}
	sort.Strings(order)
	headers := []string{"成交月份", "单数", "销售额", "已售成本", "净利润"}
	widths := []float64{14, 9, 15, 15, 15}
	head := make([]xlCell, len(headers))
	for i, h := range headers {
		head[i] = cellText(h, headStyle)
	}
	rows := [][]xlCell{head}
	var tSales, tCost, tProfit float64
	var tCount int
	for i, m := range order {
		z := i%2 == 1
		pick := func(a, b int) int {
			if z {
				return b
			}
			return a
		}
		ag := byMonth[m]
		ps := pos
		if z {
			ps = posZ
		}
		if ag.profit < 0 {
			ps = neg
			if z {
				ps = negZ
			}
		}
		rows = append(rows, []xlCell{
			cellText(m, pick(textL, textLZ)),
			cellNum(float64(ag.count), pick(numC, numCZ)),
			cellNum(ag.sales, pick(money, moneyZ)),
			cellNum(ag.cost, pick(money, moneyZ)),
			cellNum(ag.profit, ps),
		})
		tSales += ag.sales
		tCost += ag.cost
		tProfit += ag.profit
		tCount += ag.count
	}
	last := len(rows)
	rows = append(rows, []xlCell{
		cellText("合计", totalL),
		cellNum(float64(tCount), totalI),
		cellNum(round2(tSales), totalN),
		cellNum(round2(tCost), totalN),
		cellNum(round2(tProfit), totalN),
	})
	return &xlSheet{
		name:       "月度汇总",
		rows:       rows,
		cols:       widths,
		freezeRows: 1,
		autoFilter: fmt.Sprintf("A1:E%d", last),
	}
}
