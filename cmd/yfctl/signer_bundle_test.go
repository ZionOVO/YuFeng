package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteClientCABundleAtomicallyReplacesExistingPublicBundle(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "client-ca-bundle.crt")
	first := filepath.Join(directory, "first.crt")
	second := filepath.Join(directory, "second.crt")
	if err := os.WriteFile(target, []byte("old-bundle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first, []byte("first-certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second-certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeClientCABundle(target, first, second); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "first-certificate\nsecond-certificate\n" {
		t.Fatalf("client certificate authority bundle=%q", raw)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("client certificate authority bundle mode=%#o", info.Mode().Perm())
	}
	temporary, err := filepath.Glob(filepath.Join(directory, ".client-ca-bundle-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("temporary client certificate authority bundles remain: %v", temporary)
	}
}

func TestReplaceClientCABundleRenameFailurePreservesExistingBundle(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "client-ca-bundle.crt")
	if err := os.WriteFile(target, []byte("trusted-original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	renameFailure := errors.New("injected rename failure")
	err := replaceClientCABundle(target, []byte("uncommitted-replacement\n"), func(_, _ string) error {
		return renameFailure
	})
	if !errors.Is(err, renameFailure) {
		t.Fatalf("replace client certificate authority bundle error=%v", err)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "trusted-original\n" {
		t.Fatalf("rename failure changed trusted bundle: %q", raw)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("rename failure changed bundle mode=%#o", info.Mode().Perm())
	}
	temporary, err := filepath.Glob(filepath.Join(directory, ".client-ca-bundle-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("failed replacement left temporary bundles: %v", temporary)
	}
}
