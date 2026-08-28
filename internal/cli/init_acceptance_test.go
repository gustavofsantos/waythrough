package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestInitWritesExactlyTheSelectedPresets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cmd := newInitCommand()
	cmd.SetIn(strings.NewReader("2,5\n"))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("run init: %v", err)
	}

	cfg, _, err := loadUserConfig()
	if err != nil {
		t.Fatalf("load generated user config: %v", err)
	}
	if len(cfg.LanguageServers) != 2 {
		t.Fatalf("got %d language servers, want 2", len(cfg.LanguageServers))
	}
	if got := cfg.LanguageServers[0].Name; got != "gopls" {
		t.Fatalf("first selected server is %q, want gopls", got)
	}
	if got := cfg.LanguageServers[1].Name; got != "pyright-langserver" {
		t.Fatalf("second selected server is %q, want pyright-langserver", got)
	}
}
