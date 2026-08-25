package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"yufeng/lib/edgeclient"
)

type persistedSession struct {
	UnitID   string `json:"unitId"`
	AssetID  string `json:"assetId"`
	Token    string `json:"token"`
	Refresh  string `json:"refresh"`
	Interval int64  `json:"heartbeatSeconds"`
}

func saveSession(path string, sess *edgeclient.Session) error {
	if path == "" || sess == nil {
		return nil
	}
	snap := sess.Snapshot()
	raw, err := json.Marshal(persistedSession{
		UnitID: snap.UnitID, AssetID: snap.AssetID, Token: snap.Token, Refresh: snap.Refresh,
		Interval: int64(snap.HeartbeatInterval / time.Second),
	})
	if err != nil {
		return err
	}
	return atomicWritePrivate(path, ".session-*", raw)
}

func atomicWritePrivate(path, pattern string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), pattern)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	abandon := func(cause error) error {
		closeErr := tmp.Close()
		removeErr := os.Remove(tmpName)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		return errors.Join(cause, closeErr, removeErr)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return abandon(err)
	}
	if _, err := tmp.Write(payload); err != nil {
		return abandon(err)
	}
	if err := tmp.Sync(); err != nil {
		return abandon(err)
	}
	if err := tmp.Close(); err != nil {
		removeErr := os.Remove(tmpName)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		return errors.Join(err, removeErr)
	}
	if err := os.Rename(tmpName, path); err != nil {
		removeErr := os.Remove(tmpName)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		return errors.Join(err, removeErr)
	}
	if err := syncPrivateParentDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync parent directory: %w", err)
	}
	return nil
}

func loadSession(path string) *edgeclient.Session {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var p persistedSession
	if json.Unmarshal(raw, &p) != nil || p.UnitID == "" {
		return nil
	}
	iv := time.Duration(p.Interval) * time.Second
	if iv <= 0 {
		iv = 30 * time.Second
	}
	return &edgeclient.Session{UnitID: p.UnitID, AssetID: p.AssetID, Token: p.Token, Refresh: p.Refresh, HeartbeatInterval: iv}
}
