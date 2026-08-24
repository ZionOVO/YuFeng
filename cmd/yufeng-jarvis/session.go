package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type persistedRefresh struct {
	AgentID string `json:"agent_id"`
	Refresh string `json:"refresh"`
}

func refreshFile(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "refresh")
}

func loadRefresh(path, agentID string) string {
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var p persistedRefresh
	if json.Unmarshal(raw, &p) != nil || p.AgentID != agentID || strings.TrimSpace(p.Refresh) == "" {
		return ""
	}
	return p.Refresh
}

func saveRefresh(path, agentID, token string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(persistedRefresh{AgentID: agentID, Refresh: token})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
