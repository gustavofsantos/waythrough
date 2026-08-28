package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gustavofsantos/waythrough/internal/config"
)

func TestInitAcceptsSeveralNumberedPresets(t *testing.T) {
	selected, err := parsePresetSelection("2,5", 5)
	if err != nil {
		t.Fatalf("parse preset selection: %v", err)
	}

	want := []int{1, 4}
	if !reflect.DeepEqual(selected, want) {
		t.Fatalf("got selected indexes %v, want %v", selected, want)
	}
}

func TestInitPromptListsEveryPreset(t *testing.T) {
	var output bytes.Buffer
	if _, err := selectPresetIndexes(
		strings.NewReader("all\n"), &output, config.Presets()); err != nil {
		t.Fatalf("select presets: %v", err)
	}

	for index, preset := range config.Presets() {
		want := fmt.Sprintf("%d) %s", index+1, preset.Name)
		if !strings.Contains(output.String(), want) {
			t.Fatalf("prompt %q does not contain %q", output.String(), want)
		}
	}
}

func TestInitRejectsAnOutOfRangePreset(t *testing.T) {
	if _, err := parsePresetSelection("6", 5); err == nil {
		t.Fatal("out-of-range preset selection succeeded, want an error")
	}
}

func TestInitRejectsAnEmptyPresetSelection(t *testing.T) {
	if _, err := parsePresetSelection("  ", 5); err == nil {
		t.Fatal("empty preset selection succeeded, want an error")
	}
}

func TestInitLeavesNoFileAfterAnEmptySelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".waythrough.yaml")
	if err := runInit(path, strings.NewReader("\n"), &bytes.Buffer{}); err == nil {
		t.Fatal("empty selection succeeded, want an error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("config file stat error %v, want file not to exist", err)
	}
}

func TestInitWritesTheUserConfigWithPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".waythrough.yaml")
	if err := runInit(path, strings.NewReader("1\n"), &bytes.Buffer{}); err != nil {
		t.Fatalf("run init: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat generated config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("generated config permissions %o, want 600", got)
	}
}
