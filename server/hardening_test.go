package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSafeRequestHostRejectsInjection(t *testing.T) {
	valid := []string{
		"10.10.10.251:18083",
		"example.com",
		"sub.example.com:8080",
		"127.0.0.1",
		"[::1]:8080",
	}
	for _, host := range valid {
		if got := safeRequestHost(host); got != host {
			t.Errorf("safeRequestHost(%q)=%q，期望原样放行", host, got)
		}
	}
	invalid := []string{
		"",
		"example.com/path",
		"https://example.com",
		"example.com:abc",
		"evil.com@real.com",
		"example.com\ninjected",
		"bad host",
	}
	for _, host := range invalid {
		if got := safeRequestHost(host); got != "" {
			t.Errorf("safeRequestHost(%q)=%q，期望拒绝", host, got)
		}
	}
}

func TestRequestOriginFallsBackOnHostInjection(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "evil.com/path"
	origin := requestOrigin(r)
	if !strings.HasPrefix(origin, "http://localhost:") {
		t.Errorf("注入 Host 应回退到 localhost，实际 %q", origin)
	}
	r.Host = "10.10.10.251:18083"
	if got := requestOrigin(r); got != "http://10.10.10.251:18083" {
		t.Errorf("合法 Host 应原样使用，实际 %q", got)
	}
}

func TestVerifyPasswordRejectsHugeScryptN(t *testing.T) {
	// N=1<<20（约 1GB）：即使密码正确也必须拒绝，避免内存型 DoS。
	huge := "scrypt$1048576$8$1$salt$00"
	if verifyPassword("anything", huge, "") {
		t.Error("超大 scrypt N 必须被拒绝")
	}
	// 正常参数仍可校验（N 上限收紧不影响 1<<15 的常规哈希）。
	h, err := hashPassword("ok-pass", "")
	if err != nil || !verifyPassword("ok-pass", h, "") {
		t.Errorf("正常 scrypt 哈希应可校验: err=%v", err)
	}
}

func TestInterruptibleSleepReturnsOnShutdown(t *testing.T) {
	// 直接关闭全局 shutdownCh 模拟退出；测试进程结束前不恢复（该信号一次性）。
	select {
	case <-shutdownCh:
	default:
		close(shutdownCh)
	}
	done := make(chan bool, 1)
	go func() { done <- interruptibleSleep(time.Hour) }()
	select {
	case exited := <-done:
		if !exited {
			t.Error("退出信号下 interruptibleSleep 应返回 true")
		}
	case <-time.After(2 * time.Second):
		t.Error("interruptibleSleep 未在退出信号后立即返回")
	}
}
