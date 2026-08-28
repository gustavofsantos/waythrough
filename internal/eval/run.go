package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gustavofsantos/waythrough/internal/config"
	"github.com/gustavofsantos/waythrough/internal/editor"
	"github.com/gustavofsantos/waythrough/internal/lsp"
)

type Options struct {
	FixtureDirectory string
	ScenarioName     string
}

type Report struct {
	ScenarioCount int       `json:"scenario_count"`
	Results       []Result  `json:"results"`
	Summary       []Summary `json:"summary"`
}

type Result struct {
	Scenario       string     `json:"scenario"`
	Method         string     `json:"method"`
	TruePositives  int        `json:"true_positives"`
	FalsePositives int        `json:"false_positives"`
	FalseNegatives int        `json:"false_negatives"`
	Precision      float64    `json:"precision"`
	Recall         float64    `json:"recall"`
	F1             float64    `json:"f1"`
	ExactMatch     bool       `json:"exact_match"`
	OutputBytes    int        `json:"output_bytes"`
	Locations      []Location `json:"locations,omitempty"`
	Error          string     `json:"error,omitempty"`
}

type Summary struct {
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

type toolLocations struct {
	Locations []Location `json:"locations"`
}

func Run(ctx context.Context, options Options) (Report, error) {
	fixtureDirectory, err := filepath.Abs(options.FixtureDirectory)
	if err != nil {
		return Report{}, fmt.Errorf("resolve fixture directory: %w", err)
	}
	scenarios, err := loadScenarios(fixtureDirectory, options.ScenarioName)
	if err != nil {
		return Report{}, err
	}

	waythroughResults := runWaythrough(ctx, fixtureDirectory, scenarios)
	textOnlyResults := runTextOnly(ctx, fixtureDirectory, scenarios)
	results := append(waythroughResults, textOnlyResults...)
	return Report{
		ScenarioCount: len(scenarios),
		Results:       results,
		Summary:       summarizeResults(results),
	}, nil
}

func runWaythrough(
	ctx context.Context, fixtureDirectory string, scenarios []Scenario,
) []Result {
	runner, err := newWaythroughRunner(ctx, fixtureDirectory)
	if err != nil {
		results := make([]Result, 0, len(scenarios))
		for _, scenario := range scenarios {
			results = append(results, failedResult(scenario, "waythrough", err, 0))
		}
		return results
	}

	results := make([]Result, 0, len(scenarios))
	for _, scenario := range scenarios {
		results = append(results, runner.run(ctx, fixtureDirectory, scenario))
	}
	if err := runner.close(); err != nil {
		for index := range results {
			if results[index].Error == "" {
				results[index].Error = fmt.Sprintf("close Waythrough runner: %v", err)
			}
		}
	}
	return results
}

type waythroughRunner struct {
	manager *lsp.Manager
	session *mcp.ClientSession
}

func newWaythroughRunner(ctx context.Context, fixtureDirectory string) (*waythroughRunner, error) {
	cfg := config.Config{LanguageServers: config.Presets()}
	manager := lsp.NewManager(fixtureDirectory, cfg.LanguageServers, lsp.WithDemandStart())
	if err := manager.Start(ctx); err != nil {
		return nil, err
	}
	runner := &waythroughRunner{manager: manager}
	server := editor.New(manager, cfg, slog.New(slog.DiscardHandler))
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		return nil, errors.Join(err, shutdownManager(manager))
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "waythrough-eval", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		return nil, errors.Join(err, shutdownManager(manager))
	}
	runner.session = session
	return runner, nil
}

func (runner *waythroughRunner) run(
	ctx context.Context, fixtureDirectory string, scenario Scenario,
) Result {
	callResult, err := runner.session.CallTool(ctx, &mcp.CallToolParams{
		Name: "get_definition",
		Arguments: map[string]any{
			"file": scenario.File, "line": scenario.Line, "column": scenario.Column,
		},
	})
	outputBytes := toolOutputBytes(callResult)
	if err != nil {
		return failedResult(
			scenario, "waythrough", fmt.Errorf("call get_definition: %w", err), outputBytes)
	}

	locations, err := decodeToolLocations(callResult, fixtureDirectory)
	if err != nil {
		return failedResult(scenario, "waythrough", err, outputBytes)
	}
	return locationResult(scenario, "waythrough", locations, outputBytes)
}

func (runner *waythroughRunner) close() error {
	var closeErrors []error
	if runner.session != nil {
		if err := runner.session.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close MCP client: %w", err))
		}
	}
	if runner.manager != nil {
		if err := shutdownManager(runner.manager); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("shutdown language servers: %w", err))
		}
	}
	return errors.Join(closeErrors...)
}

func toolOutputBytes(result *mcp.CallToolResult) int {
	if result == nil {
		return 0
	}
	total := 0
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if ok {
			total += len(text.Text)
		}
	}
	return total
}

func runTextOnly(
	ctx context.Context, fixtureDirectory string, scenarios []Scenario,
) []Result {
	results := make([]Result, 0, len(scenarios))
	for _, scenario := range scenarios {
		results = append(results, runTextOnlyOnce(ctx, fixtureDirectory, scenario))
	}
	return results
}

func runTextOnlyOnce(
	ctx context.Context, fixtureDirectory string, scenario Scenario,
) Result {
	command := exec.CommandContext(ctx, "rg", "--line-number", "--column",
		"--fixed-strings", "--glob", "*.go", scenario.Symbol, ".")
	command.Dir = fixtureDirectory
	output, err := command.CombinedOutput()
	if err != nil && !isNoMatches(err) {
		return failedResult(
			scenario, "text_only", fmt.Errorf("run rg: %w\n%s", err, output), len(output))
	}

	locations, err := parseRipgrepLocations(string(output))
	if err != nil {
		return failedResult(scenario, "text_only", err, len(output))
	}
	return locationResult(scenario, "text_only", locations, len(output))
}

func locationResult(
	scenario Scenario, method string, locations []Location, outputBytes int,
) Result {
	metrics := scoreLocations(locations, scenario.Gold)
	return Result{
		Scenario:       scenario.Name,
		Method:         method,
		TruePositives:  metrics.TruePositives,
		FalsePositives: metrics.FalsePositives,
		FalseNegatives: metrics.FalseNegatives,
		Precision:      metrics.Precision,
		Recall:         metrics.Recall,
		F1:             metrics.F1,
		ExactMatch:     metrics.ExactMatch,
		OutputBytes:    outputBytes,
		Locations:      locations,
	}
}

func failedResult(scenario Scenario, method string, runErr error, outputBytes int) Result {
	result := locationResult(scenario, method, nil, outputBytes)
	result.Error = runErr.Error()
	return result
}

func summarizeResults(results []Result) []Summary {
	indices := make(map[string]int)
	summaries := make([]Summary, 0, 2)
	for _, result := range results {
		index, ok := indices[result.Method]
		if !ok {
			index = len(summaries)
			indices[result.Method] = index
			summaries = append(summaries, Summary{Method: result.Method})
		}
		summary := &summaries[index]
		summary.ScenarioCount++
		summary.TruePositives += result.TruePositives
		summary.FalsePositives += result.FalsePositives
		summary.FalseNegatives += result.FalseNegatives
		summary.OutputBytes += result.OutputBytes
		if result.ExactMatch {
			summary.ExactMatchRate++
		}
	}

	for index := range summaries {
		summary := &summaries[index]
		summary.Precision, summary.Recall, summary.F1 = qualityRates(
			summary.TruePositives, summary.FalsePositives, summary.FalseNegatives)
		summary.ExactMatchRate /= float64(summary.ScenarioCount)
	}
	return summaries
}

func isNoMatches(err error) bool {
	var exitError *exec.ExitError
	return errors.As(err, &exitError) && exitError.ExitCode() == 1
}

func parseRipgrepLocations(output string) ([]Location, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}

	locations := make([]Location, 0, len(lines))
	for _, line := range lines {
		parts := strings.SplitN(line, ":", 4)
		if len(parts) < 3 {
			return nil, fmt.Errorf("parse rg result %q", line)
		}
		lineNumber, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("parse rg line in %q: %w", line, err)
		}
		column, err := strconv.Atoi(parts[2])
		if err != nil {
			return nil, fmt.Errorf("parse rg column in %q: %w", line, err)
		}
		locations = append(locations, Location{
			File: filepath.ToSlash(filepath.Clean(parts[0])), Line: lineNumber, Column: column,
		})
	}
	return locations, nil
}

func decodeToolLocations(result *mcp.CallToolResult, fixtureDirectory string) ([]Location, error) {
	if result.IsError {
		return nil, fmt.Errorf("get_definition returned an error: %s", toolErrorText(result))
	}
	if len(result.Content) != 1 {
		return nil, fmt.Errorf(
			"get_definition returned %d content blocks, want 1", len(result.Content))
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		return nil, errors.New("get_definition returned non-text content")
	}
	var output toolLocations
	if err := json.Unmarshal([]byte(text.Text), &output); err != nil {
		return nil, fmt.Errorf("decode get_definition output: %w", err)
	}
	for index := range output.Locations {
		path := output.Locations[index].File
		if !filepath.IsAbs(path) {
			path = filepath.Join(fixtureDirectory, path)
		}
		path, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve definition file: %w", err)
		}
		relative, err := filepath.Rel(fixtureDirectory, path)
		if err != nil {
			return nil, fmt.Errorf("relativize definition file: %w", err)
		}
		output.Locations[index].File = filepath.ToSlash(relative)
	}
	return output.Locations, nil
}

func toolErrorText(result *mcp.CallToolResult) string {
	if len(result.Content) == 0 {
		return "no error details"
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		return "non-text error details"
	}
	return text.Text
}

func shutdownManager(manager *lsp.Manager) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return manager.Shutdown(ctx)
}
