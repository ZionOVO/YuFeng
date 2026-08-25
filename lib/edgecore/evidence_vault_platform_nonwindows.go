//go:build !windows

package edgecore

import (
	"errors"
	"os"
)

const enforcesPOSIXVaultPermissions = true

func secureEvidenceVaultDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("evidence vault directory permissions are too broad")
	}
	return nil
}

func validateEvidenceVaultSegment(info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("evidence vault segment permissions are too broad")
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
