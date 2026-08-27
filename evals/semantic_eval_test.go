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
	Method     string `json:"method"`
	ExactMatch bool   `json:"exact_match"`
}

type qualityEvaluationReport struct {
	ScenarioCount int                        `json:"scenario_count"`
	Results       []qualityEvaluationResult  `json:"results"`
	Summary       []qualityEvaluationSummary `json:"summary"`
}

type qualityEvaluationResult struct {
	Scenario       string  `json:"scenario"`
	Method         string  `json:"method"`
	TruePositives  int     `json:"true_positives"`
	FalsePositives int     `json:"false_positives"`
	FalseNegatives int     `json:"false_negatives"`
	Precision      float64 `json:"precision"`
	Recall         float64 `json:"recall"`
	F1             float64 `json:"f1"`
	ExactMatch     bool    `json:"exact_match"`
	OutputBytes    int     `json:"output_bytes"`
}

type qualityEvaluationSummary struct {
	Method         string  `json:"method"`
	ScenarioCount  int     `json:"scenario_count"`
	TruePositives  int     `json:"true_positives"`
	FalsePositives int     `json:"false_positives"`
	FalseNegatives int     `json:"false_negatives"`
	Precision      float64 `json:"precision"`
	Recall         float64 `json:"recall"`
	F1             float64 `json:"f1"`
	ExactMatchRate float64 `json:"exact_match_rate"`
	OutputBytes    int     `json:"output_bytes"`
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
		methods[result.Method] = result.ExactMatch
	}
	if !methods["waythrough"] {
		t.Fatalf("Waythrough did not report a correct definition: %#v", methods)
	}
	if _, ok := methods["text_only"]; !ok {
		t.Fatalf("text-only comparison is missing: %#v", methods)
	}
}

func TestTheSuiteReportsPerCaseAndAggregateQualityMetrics(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Fatalf("gopls is required for the eval acceptance test: %v", err)
	}

	repositoryRoot := repositoryRoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	command := exec.CommandContext(ctx, "go", "run", "./cmd/waythrough-eval",
		"--fixture", "evals/fixtures/go-semantic",
		"--scenario", "all",
		"--format", "json")
	command.Dir = repositoryRoot
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("quality evaluation failed: %v\n%s", err, output)
	}

	var report qualityEvaluationReport
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("evaluation output is not JSON: %v\n%s", err, output)
	}
	if report.ScenarioCount != 1 {
		t.Fatalf("scenario_count = %d, want 1", report.ScenarioCount)
	}
	if len(report.Results) != 2 {
		t.Fatalf("result count = %d, want 2", len(report.Results))
	}
	results := make(map[string]qualityEvaluationResult, len(report.Results))
	for _, result := range report.Results {
		results[result.Method] = result
	}
	waythrough := results["waythrough"]
	if waythrough.Scenario != "definition.cross_file" || !waythrough.ExactMatch {
		t.Fatalf("Waythrough quality result = %#v, want exact definition match", waythrough)
	}
	if waythrough.TruePositives != 1 || waythrough.FalsePositives != 0 ||
		waythrough.FalseNegatives != 0 || waythrough.Precision != 1 ||
		waythrough.Recall != 1 || waythrough.F1 != 1 || waythrough.OutputBytes <= 0 {
		t.Fatalf("Waythrough metrics = %#v, want one exact match", waythrough)
	}
	textOnly := results["text_only"]
	if textOnly.Scenario != "definition.cross_file" || textOnly.ExactMatch {
		t.Fatalf("text-only quality result = %#v, want a non-exact result", textOnly)
	}
	if textOnly.TruePositives != 1 || textOnly.FalsePositives != 2 ||
		textOnly.FalseNegatives != 0 || textOnly.Precision != 1.0/3.0 ||
		textOnly.Recall != 1 || textOnly.OutputBytes <= 0 {
		t.Fatalf("text-only metrics = %#v, want one match and two false positives", textOnly)
	}
	if len(report.Summary) != 2 {
		t.Fatalf("summary count = %d, want 2", len(report.Summary))
	}
	summaries := make(map[string]qualityEvaluationSummary, len(report.Summary))
	for _, summary := range report.Summary {
		summaries[summary.Method] = summary
	}
	waythroughSummary := summaries["waythrough"]
	if waythroughSummary.ScenarioCount != 1 || waythroughSummary.TruePositives != 1 ||
		waythroughSummary.FalsePositives != 0 || waythroughSummary.FalseNegatives != 0 ||
		waythroughSummary.Precision != 1 || waythroughSummary.Recall != 1 ||
		waythroughSummary.F1 != 1 || waythroughSummary.ExactMatchRate != 1 {
		t.Fatalf("Waythrough summary = %#v, want one exact match", waythroughSummary)
	}
	textOnlySummary := summaries["text_only"]
	if textOnlySummary.ScenarioCount != 1 || textOnlySummary.TruePositives != 1 ||
		textOnlySummary.FalsePositives != 2 || textOnlySummary.FalseNegatives != 0 ||
		textOnlySummary.Precision != 1.0/3.0 || textOnlySummary.Recall != 1 ||
		textOnlySummary.F1 != 0.5 || textOnlySummary.ExactMatchRate != 0 {
		t.Fatalf("text-only summary = %#v, want one match and two false positives", textOnlySummary)
	}
}

func TestTheNavigationSuiteSeparatesSemanticAnswersFromTextNoise(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Fatalf("gopls is required for the eval acceptance test: %v", err)
	}

	repositoryRoot := repositoryRoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	command := exec.CommandContext(ctx, "go", "run", "./cmd/waythrough-eval",
		"--fixture", "evals/fixtures/go-navigation",
		"--scenario", "all",
		"--format", "json")
	command.Dir = repositoryRoot
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("navigation evaluation failed: %v\n%s", err, output)
	}

	var report qualityEvaluationReport
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("evaluation output is not JSON: %v\n%s", err, output)
	}
	wantScenarios := []string{
		"definition.cross_file_noise",
		"definition.shadowed_local",
		"references.cross_file_noise",
	}
	if report.ScenarioCount != len(wantScenarios) || len(report.Results) != len(wantScenarios)*2 {
		t.Fatalf("scenario_count/results = %d/%d, want %d/%d",
			report.ScenarioCount, len(report.Results), len(wantScenarios), len(wantScenarios)*2)
	}

	results := make(map[string]qualityEvaluationResult, len(report.Results))
	for _, result := range report.Results {
		results[result.Scenario+":"+result.Method] = result
	}
	for _, scenario := range wantScenarios {
		semantic := results[scenario+":waythrough"]
		if !semantic.ExactMatch || semantic.FalsePositives != 0 || semantic.FalseNegatives != 0 {
			t.Fatalf("Waythrough result for %q = %#v, want an exact semantic answer", scenario, semantic)
		}
		textOnly := results[scenario+":text_only"]
		if textOnly.ExactMatch || textOnly.FalsePositives == 0 {
			t.Fatalf("text-only result for %q = %#v, want false positives", scenario, textOnly)
		}
	}

	summaries := make(map[string]qualityEvaluationSummary, len(report.Summary))
	for _, summary := range report.Summary {
		summaries[summary.Method] = summary
	}
	if summaries["waythrough"].ExactMatchRate != 1 {
		t.Fatalf("Waythrough exact-match rate = %v, want 1", summaries["waythrough"].ExactMatchRate)
	}
	if summaries["text_only"].ExactMatchRate >= 1 {
		t.Fatalf("text-only exact-match rate = %v, want less than 1", summaries["text_only"].ExactMatchRate)
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
