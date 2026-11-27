package main

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// 后台循环（备份、闲鱼同步/保活）在收到 SIGINT/SIGTERM 后应尽快停手，
// 避免 systemd/PVE 重启时长时间 sleep 卡住退出、或退出瞬间正在写库。
// shutdownCh 关闭即代表进程正在退出；interruptibleSleep 用它替代 time.Sleep。
var (
	shutdownOnce sync.Once
	shutdownCh   = make(chan struct{})
)

// initShutdownSignal 注册 SIGINT/SIGTERM，关闭 shutdownCh（幂等，可重复调用）。
func initShutdownSignal() {
	shutdownOnce.Do(func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-ch
			signal.Stop(ch)
			close(shutdownCh)
		}()
	})
}

// shutdownRequested 报告进程是否正在退出。
func shutdownRequested() bool {
	select {
	case <-shutdownCh:
		return true
	default:
		return false
	}
}

// interruptibleSleep 等待 d，但退出信号到达时立即返回 true；正常睡满返回 false。
// 用于把后台长循环的时间.Sleep 变成可中断等待。
func interruptibleSleep(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-shutdownCh:
		return true
	case <-timer.C:
		return false
	}
}
