//go:build !yufeng_dev

package main

import (
	"flag"
	"testing"
)

func TestProductionBuildDoesNotRegisterDevelopmentTriage(t *testing.T) {
	before := flag.CommandLine
	flag.CommandLine = flag.NewFlagSet("production", flag.ContinueOnError)
	t.Cleanup(func() { flag.CommandLine = before })
	development := registerBrainDevelopmentFlags()
	if development.demoTriage == nil || *development.demoTriage {
		t.Fatal("production build must keep development triage disabled")
	}
	if flag.CommandLine.Lookup("demo-triage") != nil {
		t.Fatal("production build must not register the demo-triage flag")
	}
}
