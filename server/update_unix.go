//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// spawnDetached 让更新脚本脱离当前进程：新会话、无控制终端。
// 这样即使当前进程被 script 里的 systemctl restart 杀掉，脚本也能跑完。
func spawnDetached(script string) error {
	cmd := exec.Command("/bin/sh", script)
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devnull.Close()
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}
