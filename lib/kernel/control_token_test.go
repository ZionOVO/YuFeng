package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDataplaneControlToken(t *testing.T) {
	dir := t.TempDir()
	write := func(name, value string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	valid := strings.Repeat("a", 43) + "="
	got, err := LoadDataplaneControlToken(write("valid", valid))
	if err != nil || got != valid {
		t.Fatalf("token=%q err=%v", got, err)
	}
	for name, path := range map[string]string{
		"missing path": "",
		"missing file": filepath.Join(dir, "missing"),
		"too short":    write("short", "short"),
		"whitespace":   write("space", valid+"\n"),
		"too long":     write("long", strings.Repeat("a", dataplaneControlTokenMaxBytes+1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadDataplaneControlToken(path); err == nil {
				t.Fatal("invalid control token file must fail")
			}
		})
	}
}
