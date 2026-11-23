package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
)

const (
	maxTinyJSONBody     = 8 << 10
	maxBackupJSONBody   = 16 << 10
	maxSettingsJSONBody = 32 << 10
	maxSmallJSONBody    = 64 << 10
	maxNormalJSONBody   = 1 << 20
	maxImportJSONBody   = 64 << 20
)

// decodeJSONBody 统一限制请求体大小、拒绝尾部多余 JSON，并把超限稳定映射为 413。
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	if r.ContentLength > limit {
		// 已声明长度且只超出上界一点时，先把这块有界请求读完再回 413。
		// 否则客户端可能还在发送，提前关闭连接会把正常的 413 响应变成 connection reset。
		if r.ContentLength <= limit+(2<<20) {
			_, _ = io.Copy(io.Discard, r.Body)
		} else {
			w.Header().Set("Connection", "close")
		}
		writeError(w, http.StatusRequestEntityTooLarge, "请求体过大")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "请求体过大")
		} else {
			writeError(w, http.StatusBadRequest, "请求体解析失败")
		}
		return false
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			writeError(w, http.StatusBadRequest, "请求体只能包含一个 JSON 对象")
		} else {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge, "请求体过大")
			} else {
				writeError(w, http.StatusBadRequest, "请求体解析失败")
			}
		}
		return false
	}
	return true
}

// decodeOptionalJSONBody 允许 POST 不带请求体，仍会限制最大尺寸。
func decodeOptionalJSONBody(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	if r.ContentLength > limit {
		if r.ContentLength <= limit+(2<<20) {
			_, _ = io.Copy(io.Discard, r.Body)
		} else {
			w.Header().Set("Connection", "close")
		}
		writeError(w, http.StatusRequestEntityTooLarge, "请求体过大")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); errors.Is(err, io.EOF) {
		return true
	} else if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "请求体过大")
		} else {
			writeError(w, http.StatusBadRequest, "请求体解析失败")
		}
		return false
	}
	return true
}

var (
	trustedProxyOnce sync.Once
	trustedProxyNets []*net.IPNet
)

// trustedProxyNetsFromEnv 默认不信任任何转发头。只有显式配置
// TRUSTED_PROXY_CIDRS（逗号分隔）后，来自这些网段的 X-Forwarded-For 才生效。
func trustedProxyCIDRs() []*net.IPNet {
	trustedProxyOnce.Do(func() {
		for _, raw := range strings.Split(os.Getenv("TRUSTED_PROXY_CIDRS"), ",") {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			if ip := net.ParseIP(raw); ip != nil {
				bits := 128
				if ip.To4() != nil {
					bits = 32
					ip = ip.To4()
				}
				trustedProxyNets = append(trustedProxyNets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
				continue
			}
			if _, network, err := net.ParseCIDR(raw); err == nil {
				trustedProxyNets = append(trustedProxyNets, network)
			}
		}
	})
	return trustedProxyNets
}

func resetTrustedProxyCacheForTest() {
	trustedProxyNets = nil
	trustedProxyOnce = sync.Once{}
}

func isTrustedProxy(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, network := range trustedProxyCIDRs() {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func requestFromTrustedProxy(r *http.Request) bool {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return isTrustedProxy(net.ParseIP(strings.Trim(host, "[]")))
}

// drainRequestBody 在 handler 提前返回时消费尚未读取的小请求体，避免客户端仍在发送时
// 服务端直接关闭连接，导致浏览器拿到 connection reset 而不是稳定的 4xx 响应。
// 最多排空 2MB；更大的异常请求直接交回 net/http 关闭连接。
func drainRequestBody(r *http.Request, limit int64) {
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return
	}
	if r.ContentLength > limit {
		_ = r.Body.Close()
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, limit))
}
