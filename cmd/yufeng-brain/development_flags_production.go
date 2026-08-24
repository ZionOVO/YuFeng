//go:build !yufeng_dev

package main

type brainDevelopmentFlags struct{ demoTriage *bool }

func registerBrainDevelopmentFlags() brainDevelopmentFlags {
	value := false
	return brainDevelopmentFlags{demoTriage: &value}
}
