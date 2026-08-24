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
	ScenarioCount int      `json:"scenario_count"`
	Results       []Result `json:"results"`
}

type Result struct {
	Method    string     `json:"method"`
	Correct   bool       `json:"correct"`
	Locations []Location `json:"locations,omitempty"`
	Error     string     `json:"error,omitempty"`
}

type toolLocations struct {
	Locations []Location `json:"locations"`
}

func Run(ctx context.Context, options Options) (Report, error) {
	fixtureDirectory, err := filepath.Abs(options.FixtureDirectory)
	if err != nil {
		return Report{}, fmt.Errorf("resolve fixture directory: %w", err)
	}
	scenario, err := loadScenario(fixtureDirectory, options.ScenarioName)
	if err != nil {
		return Report{}, err
	}

	waythroughResult := runWaythrough(ctx, fixtureDirectory, scenario)
	textOnlyResult := runTextOnly(ctx, fixtureDirectory, scenario)
	return Report{
		ScenarioCount: 1,
		Results:       []Result{waythroughResult, textOnlyResult},
	}, nil
}

func runWaythrough(
	ctx context.Context, fixtureDirectory string, scenario Scenario,
) (result Result) {
	result = Result{Method: "waythrough"}

	cfg := config.Default()
	manager := lsp.NewManager(fixtureDirectory, cfg.LanguageServers, lsp.WithDemandStart())
	if err := manager.Start(ctx); err != nil {
		result.Error = err.Error()
		return result
	}
	defer func() {
		if err := shutdownManager(manager); err != nil && result.Error == "" {
			result.Error = fmt.Sprintf("shutdown language servers: %v", err)
			result.Correct = false
		}
	}()

	server := editor.New(manager, cfg, slog.New(slog.DiscardHandler))
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		result.Error = fmt.Sprintf("connect MCP server: %v", err)
		return result
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "waythrough-eval", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		result.Error = fmt.Sprintf("connect MCP client: %v", err)
		return result
	}
	callResult, callErr := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "get_definition",
		Arguments: map[string]any{
			"file": scenario.File, "line": scenario.Line, "column": scenario.Column,
		},
	})
	closeErr := session.Close()
	if callErr != nil {
		result.Error = fmt.Sprintf("call get_definition: %v", callErr)
		return result
	}
	if closeErr != nil {
		result.Error = fmt.Sprintf("close MCP client: %v", closeErr)
		return result
	}

	locations, err := decodeToolLocations(callResult, fixtureDirectory)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Locations = locations
	result.Correct = sameLocations(locations, scenario.Gold)
	return result
}

func runTextOnly(ctx context.Context, fixtureDirectory string, scenario Scenario) Result {
	result := Result{Method: "text_only"}
	command := exec.CommandContext(ctx, "rg", "--line-number", "--column",
		"--fixed-strings", "--glob", "*.go", scenario.Symbol, ".")
	command.Dir = fixtureDirectory
	output, err := command.CombinedOutput()
	if err != nil && !isNoMatches(err) {
		result.Error = fmt.Sprintf("run rg: %v\n%s", err, output)
		return result
	}

	locations, err := parseRipgrepLocations(string(output))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Locations = locations
	result.Correct = sameLocations(locations, scenario.Gold)
	return result
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
