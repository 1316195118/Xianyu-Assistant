package main

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// cachedRead 缓存重型只读接口的完整响应。多人同时打开概览、经营中心时，
// 同一店铺的数据只计算一次；任何写操作或后台同步都会清空缓存。
type cachedResponse struct {
	status  int
	header  http.Header
	body    []byte
	expires time.Time
}

type cacheFlight struct {
	done chan struct{}
}

type readResponseCache struct {
	mu      sync.Mutex
	entries map[string]cachedResponse
	flights map[string]*cacheFlight
	max     int
	epoch   uint64
}

var apiReadCache = &readResponseCache{
	entries: make(map[string]cachedResponse),
	flights: make(map[string]*cacheFlight),
	max:     256,
}

func (c *readResponseCache) get(key string, now time.Time) (cachedResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return cachedResponse{}, false
	}
	if !entry.expires.After(now) {
		delete(c.entries, key)
		return cachedResponse{}, false
	}
	entry.header = entry.header.Clone()
	return entry, true
}

func (c *readResponseCache) put(key string, entry cachedResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putLocked(key, entry)
}

func (c *readResponseCache) putLocked(key string, entry cachedResponse) {
	if len(c.entries) >= c.max {
		now := time.Now()
		for k, v := range c.entries {
			if !v.expires.After(now) {
				delete(c.entries, k)
			}
		}
		// 极端情况下仍满：丢掉最早过期的一项，保证内存有上限。
		if len(c.entries) >= c.max {
			var oldestKey string
			var oldest time.Time
			for k, v := range c.entries {
				if oldestKey == "" || v.expires.Before(oldest) {
					oldestKey, oldest = k, v.expires
				}
			}
			delete(c.entries, oldestKey)
		}
	}
	c.entries[key] = entry
}

func (c *readResponseCache) clearAll() {
	c.mu.Lock()
	c.entries = make(map[string]cachedResponse)
	c.epoch++
	c.mu.Unlock()
}

type responseCapture struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func newResponseCapture() *responseCapture {
	return &responseCapture{header: make(http.Header), status: http.StatusOK}
}

func (w *responseCapture) Header() http.Header {
	return w.header
}

func (w *responseCapture) WriteHeader(code int) {
	if w.status == http.StatusOK && w.body.Len() == 0 {
		w.status = code
	}
}

func (w *responseCapture) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func cachedGET(ttl time.Duration, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Cache-Control") == "no-cache" {
			next(w, r)
			return
		}
		key := strconv.FormatInt(shopID(r), 10) + "\n" + r.URL.RequestURI()
		now := time.Now()
		if hit, ok := apiReadCache.get(key, now); ok {
			copyHeaders(w.Header(), hit.header)
			w.Header().Set("X-Response-Cache", "HIT")
			w.WriteHeader(hit.status)
			_, _ = w.Write(hit.body)
			return
		}

		apiReadCache.mu.Lock()
		if flight, ok := apiReadCache.flights[key]; ok {
			apiReadCache.mu.Unlock()
			select {
			case <-flight.done:
			case <-r.Context().Done():
				return
			}
			if hit, ok := apiReadCache.get(key, time.Now()); ok {
				copyHeaders(w.Header(), hit.header)
				w.Header().Set("X-Response-Cache", "HIT")
				w.WriteHeader(hit.status)
				_, _ = w.Write(hit.body)
				return
			}
			next(w, r)
			return
		}
		flight := &cacheFlight{done: make(chan struct{})}
		apiReadCache.flights[key] = flight
		cacheEpoch := apiReadCache.epoch
		apiReadCache.mu.Unlock()

		// 若 next 内部 panic，缺少清理会让 done 永不关闭、flights 中的 key 永久残留，
		// 后续同 key 请求全部卡在等待 done，接口彻底挂起。这里在 panic 时兜底清理并
		// 原样重新抛出（交给 net/http 的 recover 处理当前连接）；正常路径不受影响，
		// 仍保持「先写缓存、再关闭 flight」的顺序，避免等待者醒来后缓存未命中而重复计算。
		capture := newResponseCapture()
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					apiReadCache.mu.Lock()
					delete(apiReadCache.flights, key)
					close(flight.done)
					apiReadCache.mu.Unlock()
					panic(recovered)
				}
			}()
			next(capture, r)
		}()
		copyHeaders(w.Header(), capture.header)
		w.Header().Set("X-Response-Cache", "MISS")
		w.WriteHeader(capture.status)
		_, _ = w.Write(capture.body.Bytes())

		body := append([]byte(nil), capture.body.Bytes()...)
		apiReadCache.mu.Lock()
		// 计算期间若发生写入，这一份响应可能已经过期，只返回给当前请求，
		// 绝不能再写回缓存，否则会把刚失效的旧统计重新保留 45 秒。
		if capture.status == http.StatusOK && len(body) > 0 && apiReadCache.epoch == cacheEpoch {
			header := capture.header.Clone()
			header.Del("Content-Length")
			apiReadCache.putLocked(key, cachedResponse{
				status: capture.status, header: header, body: body, expires: now.Add(ttl),
			})
		}
		// 正常路径同样必须释放 flight：与写缓存在同一把锁内完成，保证等待者
		// 醒来时缓存已就位，既能命中缓存，也不会永久卡在等待 done。
		delete(apiReadCache.flights, key)
		close(flight.done)
		apiReadCache.mu.Unlock()
	}
}

func copyHeaders(dst, src http.Header) {
	for k, values := range src {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range values {
			dst.Add(k, v)
		}
	}
}
