package main

// 多人实时同步的回归测试：事件总线、SSE 流、广播范围、编辑乐观锁。

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRealtimePublishAndSince(t *testing.T) {
	h := &rtHub{clients: make(map[*rtClient]struct{})}
	ev1 := h.publish("POST", "/api/records", "c1", "新增了一条记录", defaultShopID)
	ev2 := h.publish("PUT", "/api/records/1", "c2", "修改了一条记录", defaultShopID)
	if ev1.ID != 1 || ev2.ID != 2 || ev2.Rev != 2 {
		t.Fatalf("事件序号/版本异常: %+v %+v", ev1, ev2)
	}
	got := h.since(0, defaultShopID)
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("since(0) = %+v", got)
	}
	if tail := h.since(1, defaultShopID); len(tail) != 1 || tail[0].ID != 2 {
		t.Fatalf("since(1) = %+v", tail)
	}
	if none := h.since(2, defaultShopID); len(none) != 0 {
		t.Fatalf("since(2) 应为空，得到 %+v", none)
	}
	// 环形缓冲只保留最近 100 条
	for i := 0; i < 120; i++ {
		h.publish("POST", "/api/records", "", "x", defaultShopID)
	}
	if len(h.recent) != rtRecentMax {
		t.Fatalf("recent 长度 = %d, want %d", len(h.recent), rtRecentMax)
	}
	if h.recent[len(h.recent)-1].ID != 122 {
		t.Fatalf("最近一条 ID = %d, want 122", h.recent[len(h.recent)-1].ID)
	}
}

func TestSummarizeAndBroadcastScope(t *testing.T) {
	cases := map[string]string{
		"POST /api/records":           "新增了一条记录",
		"PUT /api/records/9":          "修改了一条记录",
		"DELETE /api/records/9":       "删除了一条记录",
		"POST /api/import":            "导入了数据",
		"POST /api/groups/auto-merge": "合并了重复联单",
		"POST /api/settings":          "更新了系统设置",
		"DELETE /api/backups/x.db":    "删除了备份",
		"POST /api/undo":              "从回收站恢复了数据",
	}
	for k, want := range cases {
		p := strings.SplitN(k, " ", 2)
		if got := summarizeChange(p[0], p[1]); got != want {
			t.Errorf("summarizeChange(%s) = %q, want %q", k, got, want)
		}
	}
	skip := []string{"POST /api/recognize", "POST /api/parse-text", "POST /api/match",
		"POST /api/settings/test", "POST /api/ocr-feedback", "POST /api/backup/verify"}
	for _, s := range skip {
		p := strings.SplitN(s, " ", 2)
		if shouldBroadcast(p[0], p[1]) {
			t.Errorf("%s 不应广播", s)
		}
	}
	for _, s := range []string{"GET /api/records", "POST /api/records", "DELETE /api/tags/1"} {
		p := strings.SplitN(s, " ", 2)
		if s == "GET /api/records" {
			if shouldBroadcast(p[0], p[1]) {
				t.Errorf("%s 不应广播", s)
			}
			continue
		}
		if !shouldBroadcast(p[0], p[1]) {
			t.Errorf("%s 应广播", s)
		}
	}
}

type sseBlock struct {
	text string
	err  error
}

func readSSEBlock(r *bufio.Reader) sseBlock {
	var b strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return sseBlock{b.String(), err}
		}
		if line == "\n" || line == "\r\n" {
			return sseBlock{b.String(), nil}
		}
		b.WriteString(line)
	}
}

// waitEvent 持续读流直到收到指定类型事件，途中跳过 presence/hello 等无关事件
//（连接建立时服务端会广播一次在线数变化，属正常行为）。
func waitEvent(t *testing.T, r *bufio.Reader, want string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		ch := make(chan sseBlock, 1)
		go func() { ch <- readSSEBlock(r) }()
		select {
		case b := <-ch:
			if b.err != nil {
				t.Fatalf("读取 %s 失败: %v（最后收到 %q）", want, b.err, b.text)
			}
			last = b.text
			if strings.Contains(b.text, "event: "+want) {
				return b.text
			}
		case <-time.After(time.Until(deadline)):
			t.Fatalf("等待 %s 超时（最后收到 %q）", want, last)
		}
	}
	t.Fatalf("等待 %s 超时（最后收到 %q）", want, last)
	return ""
}

func TestEventsStreamReceivesChange(t *testing.T) {
	handler := newExtrasEnv(t)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("连接 SSE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("SSE 状态码 = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}
	reader := bufio.NewReader(resp.Body)

	hello := waitEvent(t, reader, "hello")
	if !strings.Contains(hello, "event: hello") || !strings.Contains(hello, `"online"`) {
		t.Fatalf("hello 事件异常: %q", hello)
	}

	// 触发一次写入，SSE 应推送 change
	body := map[string]interface{}{"product": "实时同步测试", "side": "",
		"buy_price": 1, "sell_price": 2, "sell_net": 2, "quantity": 1, "no_merge": true}
	if w := doExtrasReq(handler, "POST", "/api/records", body); w.Code != 200 {
		t.Fatalf("写入失败: %d %s", w.Code, w.Body.String())
	}
	blk := waitEvent(t, reader, "change")
	if !strings.Contains(blk, "event: change") {
		t.Fatalf("未收到 change 事件: %q", blk)
	}
	if !strings.Contains(blk, "新增了一条记录") || !strings.Contains(blk, "id: ") {
		t.Fatalf("change 事件内容异常: %q", blk)
	}

	// 查询型 POST 不应触发广播
	before := realtimeHub.currentRev()
	if w := doExtrasReq(handler, "POST", "/api/match",
		map[string]interface{}{"product": "实时同步测试", "side": "sell"}); w.Code != 200 {
		t.Logf("match 返回 %d（不阻断）", w.Code)
	}
	time.Sleep(120 * time.Millisecond)
	if after := realtimeHub.currentRev(); after != before {
		t.Fatalf("查询型 POST 触发了广播: rev %d -> %d", before, after)
	}
}

func TestEventsLastEventIDReplay(t *testing.T) {
	handler := newExtrasEnv(t)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// 记下基线：realtimeHub 是全局单例，测试间 revision 会累积
	base := realtimeHub.currentRev()
	for i := 0; i < 2; i++ {
		doExtrasReq(handler, "POST", "/api/records", map[string]interface{}{
			"product": "重放测试", "side": "", "buy_price": 1,
			"sell_price": 2, "sell_net": 2, "quantity": 1, "no_merge": true})
	}
	if realtimeHub.currentRev() != base+2 {
		t.Fatalf("两次写入应让 revision +2，实际 %d -> %d", base, realtimeHub.currentRev())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events", nil)
	req.Header.Set("Last-Event-ID", strconv.FormatInt(base, 10))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("连接 SSE: %v", err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	_ = waitEvent(t, reader, "hello")
	for i := 0; i < 2; i++ {
		blk := waitEvent(t, reader, "change")
		if !strings.Contains(blk, "event: change") {
			t.Fatalf("第 %d 条补发不是 change: %q", i+1, blk)
		}
	}
}

func TestOptimisticLockConflict(t *testing.T) {
	handler := newExtrasEnv(t)
	w := doExtrasReq(handler, "POST", "/api/records", map[string]interface{}{
		"product": "并发编辑测试", "side": "", "buy_price": 1,
		"sell_price": 2, "sell_net": 2, "quantity": 1, "no_merge": true})
	if w.Code != 200 {
		t.Fatalf("创建失败: %d %s", w.Code, w.Body.String())
	}
	item, _ := decodeMap(t, w)["item"].(map[string]interface{})
	id := int64(item["id"].(float64))

	// 把 updated_at 固定成已知旧值，避免同秒写入导致误判
	if _, err := db.Exec("UPDATE items SET updated_at='2020-01-01 00:00:00' WHERE id=?", id); err != nil {
		t.Fatalf("改时间: %v", err)
	}
	newBody := func(expected string) map[string]interface{} {
		b := map[string]interface{}{
			"product": "并发编辑测试", "side": "", "buy_price": 1,
			"sell_price": 2, "sell_net": 2, "quantity": 1,
		}
		if expected != "" {
			b["expected_updated_at"] = expected
		}
		return b
	}
	path := "/api/records/" + strconv.FormatInt(id, 10)

	// 过期的 expected -> 409，并回传 current 供前端展示最新值
	cw := doExtrasReq(handler, "PUT", path, newBody("2019-01-01 00:00:00"))
	if cw.Code != 409 {
		t.Fatalf("过期 expected 应 409，实际 %d %s", cw.Code, cw.Body.String())
	}
	cm := decodeMap(t, cw)
	if cm["code"] != "conflict" {
		t.Errorf("409 缺少 code=conflict: %v", cm)
	}
	if _, ok := cm["current"].(map[string]interface{}); !ok {
		t.Errorf("409 应带 current: %v", cm)
	}

	// 正确的 expected -> 200，且响应 updated_at 已刷新
	gw := doExtrasReq(handler, "PUT", path, newBody("2020-01-01 00:00:00"))
	if gw.Code != 200 {
		t.Fatalf("正确 expected 应 200，实际 %d %s", gw.Code, gw.Body.String())
	}
	got, _ := decodeMap(t, gw)["updated_at"].(string)
	if got == "" || got == "2020-01-01 00:00:00" {
		t.Errorf("响应 updated_at 未刷新: %q", got)
	}

	// 不带 expected（旧客户端/脚本）仍可写入，保持向后兼容
	ow := doExtrasReq(handler, "PUT", path, newBody(""))
	if ow.Code != 200 {
		t.Fatalf("无 expected 应向后兼容 200，实际 %d %s", ow.Code, ow.Body.String())
	}
}

// ---------- 契约：实时事件按店铺隔离 ----------

func TestRealtimeShopScope(t *testing.T) {
	h := &rtHub{clients: make(map[*rtClient]struct{})}
	c1 := &rtClient{ch: make(chan rtEvent, 8), shopID: 1}
	c2 := &rtClient{ch: make(chan rtEvent, 8), shopID: 2}
	h.clients[c1] = struct{}{}
	h.clients[c2] = struct{}{}

	ev := h.publish("POST", "/api/records", "actor", "A 店新增", 1)
	if ev.ShopID != 1 {
		t.Fatalf("事件店铺 = %d, want 1", ev.ShopID)
	}
	select {
	case got := <-c1.ch:
		if got.ShopID != 1 {
			t.Errorf("同店客户端收到别店事件: %+v", got)
		}
	default:
		t.Error("同店客户端没收到变更")
	}
	select {
	case got := <-c2.ch:
		t.Errorf("跨店客户端收到了变更: %+v", got)
	default:
	}

	// 断线补发同样只能补本店的
	if got := h.since(0, 2); len(got) != 0 {
		t.Errorf("店铺 2 补发到 %d 条（应 0）", len(got))
	}
	if got := h.since(0, 1); len(got) != 1 {
		t.Errorf("店铺 1 补发 %d 条（应 1）", len(got))
	}

	// 在线数按店统计
	if n := h.onlineInShop(1); n != 1 {
		t.Errorf("店铺 1 在线 = %d, want 1", n)
	}
	if n := h.onlineInShop(2); n != 1 {
		t.Errorf("店铺 2 在线 = %d, want 1", n)
	}
	if n := h.onlineInShop(3); n != 0 {
		t.Errorf("店铺 3 在线 = %d, want 0", n)
	}

	// presence 广播：每个客户端看到的是自己店铺的人数
	h.broadcastPresence()
	for _, c := range []*rtClient{c1, c2} {
		select {
		case p := <-c.ch:
			if p.Kind != "presence" || p.Online != 1 {
				t.Errorf("presence 异常: %+v", p)
			}
			if p.ShopID != c.shopID {
				t.Errorf("presence 店铺错位: %+v", p)
			}
		default:
			t.Error("未收到 presence")
		}
	}
}
