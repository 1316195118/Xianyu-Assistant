package main

import (
	"testing"
	"time"
)

func TestBridgeOnlineWindow(t *testing.T) {
	now := time.Now()
	if !gfBridgeOnline(now.Add(-2 * time.Minute).Format("2006-01-02 15:04:05")) {
		t.Fatal("两分钟前的心跳应视为在线")
	}
	if gfBridgeOnline(now.Add(-5 * time.Minute).Format("2006-01-02 15:04:05")) {
		t.Fatal("五分钟前的心跳应视为离线")
	}
	if gfBridgeOnline("") {
		t.Fatal("没有心跳记录时不应视为在线")
	}
}

func TestSyncRetryStopsTightLoopAfterThreeFailures(t *testing.T) {
	newExtrasEnv(t)
	const shop int64 = 82
	for i := 0; i < 3; i++ {
		gfSyncRetryOrFail(shop, "Cookie 已失效")
	}
	if gfGet(gfKeyDead, shop) != "1" {
		t.Fatal("连续失败三次后应标记失效")
	}
	raw := gfGet(gfKeyRetryAt, shop)
	next, err := time.ParseInLocation("2006-01-02 15:04:05", raw, time.Local)
	if err != nil {
		t.Fatalf("失败后应保留低频检查时间: %q", raw)
	}
	delay := time.Until(next)
	if delay < 5*time.Hour+55*time.Minute || delay > 6*time.Hour+5*time.Minute {
		t.Fatalf("第三次失败后应改为约 6 小时后检查，实际 %v", delay)
	}
}
