//go:build windows

package main

import "errors"

// Windows 上没做自更新：程序文件被占用时无法原地替换，
// 需要额外的 .bat 引导和延迟删除，收益不如直接下一份新包。
func spawnDetached(string) error {
	return errors.New("当前系统不支持一键更新，请手动下载替换")
}
