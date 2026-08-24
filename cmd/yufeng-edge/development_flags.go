//go:build yufeng_dev

package main

import "flag"
import (
	"errors"
	"net/url"
	"strings"
)

type edgeDevelopmentFlags struct {
	localDemo *bool
	upstream  *string
	artifacts *string
	telemetry *string
	mode      *string
	assetID   *string
	addr      *string
	posture   *string
}

func registerEdgeDevelopmentFlags() edgeDevelopmentFlags {
	return edgeDevelopmentFlags{
		localDemo: flag.Bool("local-demo", false, "启用本地开发演示"),
		upstream:  flag.String("upstream", "", "本地开发演示上游"),
		artifacts: flag.String("artifacts", "", "本地开发演示制品目录"),
		telemetry: flag.String("telemetry", "", "本地开发演示遥测文件"),
		mode:      flag.String("mode", "enforce", "本地开发演示发布模式"),
		assetID:   flag.String("asset", "", "本地开发演示资产"),
		addr:      flag.String("addr", ":18080", "本地开发演示业务监听地址"),
		posture:   flag.String("posture", "reverse_proxy", "本地开发演示入口姿态"),
	}
}

func validateLaunchMode(brainURL string, localDemo, devInsecure bool) error {
	connected := strings.TrimSpace(brainURL) != ""
	if connected == localDemo {
		return errors.New("exactly one of brain or local development mode is required")
	}
	if localDemo {
		return nil
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
