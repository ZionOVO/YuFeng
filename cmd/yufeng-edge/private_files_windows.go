package main

import "os"

// Windows 文件访问由访问控制列表约束，Go 的 FileMode 不表达该权限模型。
func privateEvidenceKeyPermissions(os.FileInfo) bool { return true }

// Windows 不允许通过普通目录句柄调用 FlushFileBuffers；文件内容已经在重命名前同步。
func syncPrivateParentDirectory(string) error { return nil }
