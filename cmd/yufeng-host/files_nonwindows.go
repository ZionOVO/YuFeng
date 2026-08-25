//go:build !windows

package main

import (
	"errors"
	"os"
)

const enforcesPOSIXHostPermissions = true

func syncHostDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func syncRootDirectory(root *os.Root, dir string) error {
	directory, err := root.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
