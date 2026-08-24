//go:build !yufeng_dev

package main

func runDevelopmentCommand(string, []string) (bool, error) { return false, nil }
func developmentUsage() string                             { return "" }
