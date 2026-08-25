//go:build !windows

package main

import (
	"errors"
	"os"
)

func privateEvidenceKeyPermissions(info os.FileInfo) bool {
	return info.Mode().Perm()&0o077 == 0
}

func syncPrivateParentDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
