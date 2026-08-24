//go:build !yufeng_dev

package main

import (
	"crypto/ed25519"
	"errors"
	"log"
	"net/url"
	"strings"
)

type edgeDevelopmentFlags struct {
	localDemo *bool
}

func registerEdgeDevelopmentFlags() edgeDevelopmentFlags {
	localDemo := false
	return edgeDevelopmentFlags{localDemo: &localDemo}
}

func validateLaunchMode(brainURL string, localDevelopment, devInsecure bool) error {
	if localDevelopment || strings.TrimSpace(brainURL) == "" {
		return errors.New("brain is required")
	}
	u, err := url.Parse(strings.TrimSpace(brainURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("brain must be an absolute http or https URL")
	}
	if !devInsecure && u.Scheme != "https" {
		return errors.New("production edge requires an https brain; use dev-insecure only for local development")
	}
	return nil
}

func launchEdgeDevelopmentMode(edgeDevelopmentFlags, string, ed25519.PublicKey) {
	log.Fatal("development mode is unavailable")
}
