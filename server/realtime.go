package main

// 多人实时同步：服务端唯一数据源 + SSE 变更广播。
//
// 设计取舍：
//   - 用 SSE（Server-Sent Events）而非 WebSocket：业务是「服务端通知客户端去重新拉数据」，
//     单向足够；纯标准库、零新依赖、浏览器 EventSource 自带断线重连。
//   - 事件只带「发生了什么」的摘要，不携带完整数据，客户端收到后走既有 API 重拉，
//     避免双写路径与数据不一致。
//   - 递增 revision + 最近事件缓存，配合 Last-Event-ID 让断线重连能补发遗漏。
//   - 广播统一挂在 authWrap 上（见 auth.go），业务 handler 无需逐个改。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rtEvent struct {
	ID      int64  `json:"id"`
	ShopID  int64  `json:"shop_id,omitempty"` // 变更所属店铺
	Kind    string `json:"kind"`              // change | presence | hello
	Method  string `json:"method,omitempty"`
	Path    string `json:"path,omitempty"`
	Actor   string `json:"actor,omitempty"`
	Summary string `json:"summary,omitempty"`
	Online  int    `json:"online"`
	Rev     int64  `json:"rev"`
	Time    string `json:"time"`
	// Progress 仅 kind=progress 用：0-100 的同步阶段进度
	Progress int `json:"progress,omitempty"`
}

type rtClient struct {
	ch     chan rtEvent
	role   string
	shopID int64 // 连接建立时所在的店铺；变更只推给同店客户端
}

type rtHub struct {
	mu      sync.RWMutex
	clients map[*rtClient]struct{}
	rev     int64
	seq     int64
	recent  []rtEvent // 仅保存 change 事件，供 Last-Event-ID 补发
}

const rtRecentMax = 100
const rtBufferSize = 32

var realtimeHub = &rtHub{clients: make(map[*rtClient]struct{})}

func (h *rtHub) online() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// onlineInShop 返回某个店铺的在线客户端数。
func (h *rtHub) onlineInShop(shopID int64) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for c := range h.clients {
		if c.shopID == shopID {
			n++
		}
	}
	return n
}

func (h *rtHub) subscribe(c *rtClient) func() {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	h.broadcastPresence()

	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.clients, c)
			h.mu.Unlock()
			h.broadcastPresence()
		})
	}
}

// broadcastPresence 按店铺分别统计在线数：A 店的人不应该看到 B 店的人数，
// 否则「3 人在线」会让人误以为有同事在同一份数据上操作。
func (h *rtHub) broadcastPresence() {
	h.mu.RLock()
	counts := map[int64]int{}
	for c := range h.clients {
		counts[c.shopID]++
	}
	now := time.Now().Format("15:04:05")
	for c := range h.clients {
		pushEvent(c, rtEvent{Kind: "presence", ShopID: c.shopID, Online: counts[c.shopID],
			Rev: h.rev, Time: now})
	}
	h.mu.RUnlock()
}

func (h *rtHub) currentRev() int64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.rev
}

// pushEvent 非阻塞投递；客户端过慢时丢最旧一条，保证总能收到最新状态。
// 调用方持有 h.mu 的读锁或写锁。
func pushEvent(c *rtClient, ev rtEvent) {
	select {
	case c.ch <- ev:
	default:
		select {
		case <-c.ch:
		default:
		}
		select {
		case c.ch <- ev:
		default:
		}
	}
}

// publish 记录一次数据变更，只广播给同一店铺的在线客户端。
// 别的店铺的变更与本店无关，推过去只会造成无意义的自动刷新和错误提示。
func (h *rtHub) publish(method, path, actor, summary string, shopID int64) rtEvent {
	// 后台同步不一定经过 HTTP 写中间件；所有业务变更都在这里统一清一次读缓存。
	apiReadCache.clearAll()
	h.mu.Lock()
	h.rev++
	h.seq++
	online := 0
	for c := range h.clients {
		if c.shopID == shopID {
			online++
		}
	}
	ev := rtEvent{
		ID: h.seq, ShopID: shopID, Kind: "change", Method: method, Path: path,
		Actor: actor, Summary: summary, Online: online,
		Rev: h.rev, Time: time.Now().Format("15:04:05"),
	}
	h.recent = append(h.recent, ev)
	if len(h.recent) > rtRecentMax {
		h.recent = append([]rtEvent(nil), h.recent[len(h.recent)-rtRecentMax:]...)
	}
	for c := range h.clients {
		if c.shopID == shopID {
			pushEvent(c, ev)
		}
	}
	h.mu.Unlock()
	return ev
}

// publishProgress 广播同步阶段进度（不参与 rev 变更计数、不进补发队列）。
func (h *rtHub) publishProgress(shopID int64, pct int, text string) {
	h.mu.Lock()
	h.seq++
	ev := rtEvent{
		ID: h.seq, ShopID: shopID, Kind: "progress",
		Path: "/api/goofish/progress", Summary: text, Progress: pct,
		Time: time.Now().Format("15:04:05"),
	}
	for c := range h.clients {
		if c.shopID == shopID {
			pushEvent(c, ev)
		}
	}
	h.mu.Unlock()
}

// since 返回 id 之后、本店铺发生的变更，用于断线重连补发。
func (h *rtHub) since(id int64, shopID int64) []rtEvent {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := []rtEvent{}
	for _, ev := range h.recent {
		if ev.ID > id && ev.ShopID == shopID {
			out = append(out, ev)
		}
	}
	return out
}

// summarizeChange 把写操作翻译成人话，供其它客户端的轻量提示使用。
func summarizeChange(method, path string) string {
	switch {
	case path == "/api/records" && method == "POST":
		return "新增了一条记录"
	case path == "/api/records/batch-delete":
		return "批量删除了记录"
	case path == "/api/records/batch-update":
		return "批量修改了记录"
	case strings.HasPrefix(path, "/api/records/"):
		if strings.HasSuffix(path, "/attachment") {
			return "更新了记录附件"
		}
		if method == "DELETE" {
			return "删除了一条记录"
		}
		return "修改了一条记录"
	case path == "/api/import":
		return "导入了数据"
	case path == "/api/undo":
		return "从回收站恢复了数据"
	case path == "/api/undo/ops":
		return "清理了回收站"
	case path == "/api/groups/auto-merge":
		return "合并了重复联单"
	case path == "/api/groups/link-sale":
		return "关联了买卖单"
	case path == "/api/groups" && method == "POST":
		return "新建了联单"
	case strings.HasPrefix(path, "/api/groups/"):
		return "更新了联单"
	case path == "/api/tags":
		return "新增了运费标签"
	case strings.HasPrefix(path, "/api/tags/"):
		return "删除了运费标签"
	case path == "/api/settings":
		return "更新了系统设置"
	case path == "/api/backups":
		return "创建了备份"
	case strings.HasPrefix(path, "/api/backups/"):
		return "删除了备份"
	default:
		return "更新了数据"
	}
}

// noBroadcastWrites：这些 POST 是查询/识别/校验，不改动业务数据，不广播。
var noBroadcastWrites = map[string]bool{
	"/api/recognize":         true,
	"/api/parse-text":        true,
	"/api/match":             true,
	"/api/purchases":         true,
	"/api/settings/test":     true,
	"/api/ocr-feedback":      true,
	"/api/backup/verify":     true,
	"/api/groups/candidates": true,
}

func shouldBroadcast(method, path string) bool {
	if !isWriteMethod(method) {
		return false
	}
	if noBroadcastWrites[path] {
		return false
	}
	// 附件上传不改变列表展示，广播只会让其他人白白重拉一次；
	// 需要知道附件变化的场景（正在编辑同一条）由编辑弹窗自己刷新。
	if strings.HasSuffix(path, "/attachment") {
		return false
	}
	// 仅广播业务数据写，健康检查/统计等查询型 POST 一律忽略
	return strings.HasPrefix(path, "/api/records") ||
		strings.HasPrefix(path, "/api/groups") ||
		strings.HasPrefix(path, "/api/tags") ||
		strings.HasPrefix(path, "/api/settings") ||
		strings.HasPrefix(path, "/api/backups") ||
		path == "/api/import" || path == "/api/undo" || path == "/api/undo/ops"
}

func writeSSE(w io.Writer, f http.Flusher, ev rtEvent) error {
	b, _ := json.Marshal(ev)
	if ev.Kind == "change" && ev.ID > 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", ev.ID); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Kind, b); err != nil {
		return err
	}
	f.Flush()
	return nil
}

// handleEvents：GET /api/events，SSE 长连接。
func handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "method not allowed")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "当前服务器不支持流式响应")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)

	c := &rtClient{ch: make(chan rtEvent, rtBufferSize), role: sessionRole(r), shopID: shopID(r)}
	unsub := realtimeHub.subscribe(c)
	defer unsub()

	if err := writeSSE(w, flusher, rtEvent{
		Kind: "hello", ShopID: c.shopID, Online: realtimeHub.onlineInShop(c.shopID),
		Rev: realtimeHub.currentRev(), Time: time.Now().Format("15:04:05"),
	}); err != nil {
		return
	}

	// 断线重连：浏览器自动带 Last-Event-ID，补发期间的变更
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		if last, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && last > 0 {
			for _, ev := range realtimeHub.since(last, c.shopID) {
				if err := writeSSE(w, flusher, ev); err != nil {
					return
				}
			}
		}
	}

	flusher.Flush()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-c.ch:
			// 写不出去说明客户端已经断了：立刻退出，别把「在线」名额一直占着
			if err := writeSSE(w, flusher, ev); err != nil {
				return
			}
		case <-ticker.C:
			// 心跳同时兼作断线探测：写失败就结束这次连接，在线人数才不会虚高
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// handlePresence：GET /api/presence，给运维/调试看的在线状态。
func handlePresence(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "method not allowed")
		return
	}
	writeJSON(w, map[string]interface{}{
		"online":   realtimeHub.online(),
		"revision": realtimeHub.currentRev(),
	})
}
