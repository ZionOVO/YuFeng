package main

const enforcesPOSIXSignerModes = false

// Windows 不允许通过普通目录句柄调用 FlushFileBuffers；证书包内容已经在重命名前同步。
func syncSignerDirectory(string) error { return nil }
