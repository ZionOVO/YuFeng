package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type hostJournal struct{ dir string }

type journalState struct {
	Steps map[int]journalStep `json:"steps"`
}

type journalStep struct {
	Phase                  string            `json:"phase"`
	GuardDigest            string            `json:"guard_digest,omitempty"`
	ReceiptRef             string            `json:"receipt_ref,omitempty"`
	CompensationReceiptRef string            `json:"compensation_receipt_ref,omitempty"`
	Output                 map[string]any    `json:"output,omitempty"`
	Error                  string            `json:"error,omitempty"`
	Compensation           *fileCompensation `json:"compensation,omitempty"`
}

func openHostJournal(dir string) (*hostJournal, error) {
	if err := ensurePrivateDir(dir); err != nil {
		return nil, err
	}
	return &hostJournal{dir: dir}, nil
}

func (j *hostJournal) load(commandID string) (*journalState, error) {
	raw, err := os.ReadFile(j.path(commandID))
	if errors.Is(err, os.ErrNotExist) {
		return &journalState{Steps: make(map[int]journalStep)}, nil
	}
	if err != nil {
		return nil, err
	}
	var state journalState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("load command journal: %w", err)
	}
	if state.Steps == nil {
		state.Steps = make(map[int]journalStep)
	}
	return &state, nil
}

func (j *hostJournal) save(commandID string, state *journalState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return atomicWriteFile(j.path(commandID), raw, 0o600)
}

func (j *hostJournal) path(commandID string) string {
	return filepath.Join(j.dir, hexDigest(commandID)+".json")
}

func (s *journalState) Step(index int) journalStep {
	if s == nil || s.Steps == nil {
		return journalStep{}
	}
	return s.Steps[index]
}

func (s *journalState) SetStep(index int, step journalStep) {
	if s.Steps == nil {
		s.Steps = make(map[int]journalStep)
	}
	s.Steps[index] = step
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || (enforcesPOSIXHostPermissions && info.Mode().Perm()&0o077 != 0) {
		return errors.New("host state directories must not be accessible by group or others")
	}
	return nil
}

func atomicWriteFile(path string, raw []byte, mode os.FileMode) error {
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".yufeng-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return syncHostDirectory(filepath.Dir(path))
}
