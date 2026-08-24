//go:build eval

package evals_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type semanticEvaluationReport struct {
	ScenarioCount int                      `json:"scenario_count"`
	Results       []semanticEvaluationCase `json:"results"`
}

type semanticEvaluationCase struct {
	Method  string `json:"method"`
	Correct bool   `json:"correct"`
}

type efficiencyEvaluationReport struct {
	Measurements []efficiencyMeasurement `json:"measurements"`
}

type efficiencyMeasurement struct {
	Method      string `json:"method"`
	Phase       string `json:"phase"`
	ElapsedMS   int64  `json:"elapsed_ms"`
	OutputBytes int    `json:"output_bytes"`
}

func TestDefinitionEvaluationReportsBothPaths(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Fatalf("gopls is required for the eval acceptance test: %v", err)
	}

	repositoryRoot := repositoryRoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	command := exec.CommandContext(ctx, "go", "run", "./cmd/waythrough-eval",
		"--fixture", "evals/fixtures/go-semantic",
		"--scenario", "definition.cross_file",
		"--format", "json")
	command.Dir = repositoryRoot
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("definition evaluation failed: %v\n%s", err, output)
	}

	var report semanticEvaluationReport
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("evaluation output is not JSON: %v\n%s", err, output)
	}
	if report.ScenarioCount != 1 {
		t.Fatalf("scenario_count = %d, want 1", report.ScenarioCount)
	}

	methods := make(map[string]bool, len(report.Results))
	for _, result := range report.Results {
		methods[result.Method] = result.Correct
	}
	if !methods["waythrough"] {
		t.Fatalf("Waythrough did not report a correct definition: %#v", methods)
	}
	if _, ok := methods["text_only"]; !ok {
		t.Fatalf("text-only comparison is missing: %#v", methods)
	}
}

func TestDefinitionEvaluationReportsColdAndWarmMeasurements(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Fatalf("gopls is required for the eval acceptance test: %v", err)
	}

	repositoryRoot := repositoryRoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	command := exec.CommandContext(ctx, "go", "run", "./cmd/waythrough-eval",
		"--fixture", "evals/fixtures/go-semantic",
		"--scenario", "definition.cross_file",
		"--repeat", "2",
		"--format", "json")
	command.Dir = repositoryRoot
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("efficiency evaluation failed: %v\n%s", err, output)
	}

	var report efficiencyEvaluationReport
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("evaluation output is not JSON: %v\n%s", err, output)
	}
	seen := make(map[string]bool, len(report.Measurements))
	for _, measurement := range report.Measurements {
		if measurement.ElapsedMS < 0 {
			t.Fatalf("negative elapsed time: %#v", measurement)
		}
		if measurement.OutputBytes <= 0 {
			t.Fatalf("missing output size: %#v", measurement)
		}
		seen[measurement.Method+":"+measurement.Phase] = true
	}
	for _, key := range []string{
		"waythrough:cold", "waythrough:warm", "text_only:cold", "text_only:warm",
	} {
		if !seen[key] {
			t.Fatalf("missing measurement %q in %#v", key, seen)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	return filepath.Dir(workingDirectory)
}
