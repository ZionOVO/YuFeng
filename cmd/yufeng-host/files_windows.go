package main

import "os"

const enforcesPOSIXHostPermissions = false

// yufeng-host 的生产入口拒绝 Windows；开发测试中的文件提交仍在重命名前同步内容。
func syncHostDirectory(string) error { return nil }

func syncRootDirectory(*os.Root, string) error { return nil }
