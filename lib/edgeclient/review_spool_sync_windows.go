package edgeclient

// Windows 不允许通过普通目录句柄调用 FlushFileBuffers；文件内容已经在重命名前同步。
func syncReviewDirectory(string) error { return nil }
