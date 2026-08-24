//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
)

func replaceAgentdFile(temporaryPath, path string) error {
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncAgentdDirectory(path)
}

func syncAgentdDirectory(path string) error {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
