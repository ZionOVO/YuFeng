//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func replaceAgentdFile(temporaryPath, path string) error {
	from, err := windows.UTF16PtrFromString(temporaryPath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return fmt.Errorf("replace worker state: %w", err)
	}
	return nil
}

func syncAgentdDirectory(string) error {
	// Windows 没有 Unix 目录 fsync；新建状态文件在关闭前已经 FlushFileBuffers，
	// 原子替换则由 replaceAgentdFile 的 MOVEFILE_WRITE_THROUGH 提供持久化屏障。
	return nil
}
