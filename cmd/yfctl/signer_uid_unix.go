//go:build !windows

package main

import "os"

func signerProcessUID() int { return os.Geteuid() }
