package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImplicitMissingConfigUsesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "waythrough.yaml")

	cfg, err := loadServeConfig(path, implicitConfigPath)
	if err != nil {
		t.Fatalf("load implicit config: %v", err)
	}
	if len(cfg.LanguageServers) != 5 {
		t.Fatalf("got %d default servers, want 5", len(cfg.LanguageServers))
	}
	if !cfg.usesBuiltInDefaults {
		t.Fatal("implicit missing config was not marked for demand startup")
	}
}

func TestRepositoryConfigReplacesDefaultsWhenItAppears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "waythrough.yaml")
	before, err := loadServeConfig(path, implicitConfigPath)
	if err != nil {
		t.Fatalf("load defaults before repository config exists: %v", err)
	}
	if len(before.LanguageServers) != 5 {
		t.Fatalf("got %d default servers before override, want 5",
			len(before.LanguageServers))
	}

	custom := []byte(`language_servers:
  - name: company-gopls
    command: company-gopls
    args: ["serve", "--company"]
    filetypes:
      .go: go
`)
	if err := os.WriteFile(path, custom, 0o600); err != nil {
		t.Fatalf("write custom config: %v", err)
	}

	cfg, err := loadServeConfig(path, implicitConfigPath)
	if err != nil {
		t.Fatalf("load repository config: %v", err)
	}
	if len(cfg.LanguageServers) != 1 {
		t.Fatalf("got %d servers, want only the repository entry", len(cfg.LanguageServers))
	}
	if cfg.usesBuiltInDefaults {
		t.Fatal("repository config was incorrectly marked as built-in defaults")
	}
	server := cfg.LanguageServers[0]
	if server.Name != "company-gopls" || server.Command != "company-gopls" {
		t.Fatalf("loaded server %#v, want repository override", server)
	}
}

func TestExplicitMissingConfigIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "company-waythrough.yaml")

	if _, err := loadServeConfig(path, explicitConfigPath); err == nil {
		t.Fatal("load explicit missing config succeeded, want an error")
	}
}

func TestInvalidRepositoryConfigIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "waythrough.yaml")
	if err := os.WriteFile(path, []byte("language_servers: [\n"), 0o600); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}

	if _, err := loadServeConfig(path, implicitConfigPath); err == nil {
		t.Fatal("load invalid repository config succeeded, want an error")
	}
}
