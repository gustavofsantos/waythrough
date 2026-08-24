package eval

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTheEvaluatorLoadsTheNamedDefinitionScenario(t *testing.T) {
	fixtureDirectory := t.TempDir()
	manifest := `{
  "scenarios": [
    {
      "name": "definition.cross_file",
      "tool": "get_definition",
      "file": "consumer.go",
      "line": 4,
      "column": 8,
      "symbol": "Target",
      "gold": [{"file": "provider.go", "line": 3, "column": 6}]
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(fixtureDirectory, "manifest.json"),
		[]byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	scenario, err := loadScenario(fixtureDirectory, "definition.cross_file")
	if err != nil {
		t.Fatal(err)
	}
	if scenario.Symbol != "Target" {
		t.Fatalf("symbol = %q, want Target", scenario.Symbol)
	}
	if !sameLocations(scenario.Gold, []Location{{
		File: "provider.go", Line: 3, Column: 6,
	}}) {
		t.Fatalf("gold locations = %#v", scenario.Gold)
	}
}
