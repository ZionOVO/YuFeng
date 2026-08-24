package main

import (
	"errors"
	"net/http"
	"strings"

	"yufeng/agents/modelgateway"
)

type providerConfig struct {
	DevInsecure bool
	ModelURL    string
	ModelKey    string
	BrainURL    string
	Token       func() string
	Client      *http.Client
}

// selectProvider 生产只返回持久 Generate；-model-url 仅 -dev-insecure 可用。
func selectProvider(cfg providerConfig) (modelgateway.Provider, error) {
	if strings.TrimSpace(cfg.ModelURL) != "" && !cfg.DevInsecure {
		return nil, errors.New("production jarvis forbids -model-url")
	}
	if cfg.DevInsecure {
		if cfg.ModelURL != "" {
			return modelgateway.NewHTTPProvider(cfg.ModelURL, cfg.ModelKey), nil
		}
	}
	return modelgateway.NewGenerateProvider(cfg.BrainURL, cfg.Token, cfg.Client), nil
}
