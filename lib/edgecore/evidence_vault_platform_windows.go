package edgecore

import "os"

const enforcesPOSIXVaultPermissions = false

func secureEvidenceVaultDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return os.ErrInvalid
	}
	return nil
}

// Windows 文件访问由继承的访问控制列表约束，Go 的 FileMode 不表达该权限模型。
func validateEvidenceVaultSegment(os.FileInfo) error { return nil }

// Windows 不允许通过普通目录句柄调用 FlushFileBuffers；分段内容已经在提交前同步。
func syncDirectory(string) error { return nil }
