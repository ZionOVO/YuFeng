package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJarvisIdentityIsStableAndPrivate(t *testing.T) {
	dir := t.TempDir()
	first, err := loadOrCreateJarvisPublicKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateJarvisPublicKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Fatal("jarvis public key must be nonempty and stable")
	}
	for _, name := range []string{jarvisPrivateKeyFile, jarvisPublicKeyFile} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode=%o want 600", name, info.Mode().Perm())
		}
	}
}

func TestJarvisIdentityRequiresStateDirectory(t *testing.T) {
	if _, err := loadOrCreateJarvisPublicKey(""); err == nil {
		t.Fatal("empty state directory must fail closed")
	}
}
