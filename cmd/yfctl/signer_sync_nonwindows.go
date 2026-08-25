//go:build !windows

package main

import (
	"errors"
	"os"
)

const enforcesPOSIXSignerModes = true

func syncSignerDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
