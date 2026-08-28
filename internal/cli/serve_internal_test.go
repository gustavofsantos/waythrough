package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gustavofsantos/waythrough/internal/config"
)

func TestValidateUsesOnlyUserConfiguration(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(workspace)

	projectConfig := []byte(`language_servers:
  - name: project-gopls
    command: project-gopls
    filetypes:
      .go: go
`)
	if err := os.WriteFile(filepath.Join(workspace, "waythrough.yaml"),
		projectConfig, 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	var stderr bytes.Buffer
	code := Execute([]string{"validate"}, &bytes.Buffer{}, &stderr)
	if code == 0 {
		t.Fatal("validate accepted a project config without the user config")
	}
	wantPath := filepath.Join(home, ".waythrough.yaml")
	if !strings.Contains(stderr.String(), wantPath) {
		t.Fatalf("error %q does not name user config %s", stderr.String(), wantPath)
	}
	if !strings.Contains(stderr.String(), "waythrough init") {
		t.Fatalf("error %q does not explain how to create user config", stderr.String())
	}
}

func TestUserConfigurationLivesInTheUserHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "developer")
	t.Setenv("HOME", home)

	path, err := userConfigPath()
	if err != nil {
		t.Fatalf("resolve user config path: %v", err)
	}

	want := filepath.Join(home, ".waythrough.yaml")
	if path != want {
		t.Fatalf("got config path %s, want %s", path, want)
	}
}

func TestConfigPathFlagIsNotSupported(t *testing.T) {
	var stderr bytes.Buffer
	code := Execute([]string{"validate", "--config", "/tmp/other.yaml"},
		&bytes.Buffer{}, &stderr)
	if code == 0 {
		t.Fatal("validate accepted the removed --config flag")
	}
	if !strings.Contains(stderr.String(), "unknown flag: --config") {
		t.Fatalf("error %q does not reject --config", stderr.String())
	}
}

func TestInvalidUserConfigIsAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".waythrough.yaml")
	if err := os.WriteFile(path, []byte("language_servers: [\n"), 0o600); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}

	if _, _, err := loadUserConfig(); err == nil {
		t.Fatal("load invalid user config succeeded, want an error")
	}
}

func TestServeStartLogNamesUserConfiguration(t *testing.T) {
	var output bytes.Buffer
	cfg := config.Config{LanguageServers: config.Presets()}

	logServeStarted(newLogger(&output, true), cfg,
		"/home/developer/.waythrough.yaml", "/project")

	record := output.String()
	if !strings.Contains(record, "config_source=user") {
		t.Fatalf("startup record %q does not name user configuration", record)
	}
	if !strings.Contains(record, "config=/home/developer/.waythrough.yaml") {
		t.Fatalf("startup record %q does not name the loaded file", record)
	}
}
