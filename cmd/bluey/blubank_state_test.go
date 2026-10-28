package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSessionState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")
	b := &BluBank{cfg: Config{BluBankStatePath: path}}

	if got := b.loadSessionState(); got != nil {
		t.Fatalf("missing file: got %+v", got)
	}

	want := `{"s5":"token\"x","k9":"kid","k3":"pub"}`
	if err := os.WriteFile(path, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	got := b.loadSessionState()
	if got == nil || got.S5 != `token"x` || got.K9 != "kid" || got.K3 != "pub" {
		t.Fatalf("got %+v", got)
	}

	script := sessionScript(got)
	for _, part := range []string{"%S5", "%9", "%3", "kid", "pub"} {
		if !strings.Contains(script, part) {
			t.Fatalf("script missing %q: %s", part, script)
		}
	}
	if strings.Contains(script, `token\"x`) {
		t.Fatalf("stale auth token must not be restored: %s", script)
	}
}

func TestLoadSessionStateIgnoresEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")
	if err := os.WriteFile(path, []byte(`{"s5":"","k9":"","k3":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b := &BluBank{cfg: Config{BluBankStatePath: path}}
	if got := b.loadSessionState(); got != nil {
		t.Fatalf("empty state: got %+v", got)
	}
}
