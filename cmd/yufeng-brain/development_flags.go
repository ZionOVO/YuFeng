//go:build yufeng_dev

package main

import "flag"

type brainDevelopmentFlags struct{ demoTriage *bool }

func registerBrainDevelopmentFlags() brainDevelopmentFlags {
	value := flag.Bool("demo-triage", false, "启用开发演示分诊")
	return brainDevelopmentFlags{demoTriage: value}
}
