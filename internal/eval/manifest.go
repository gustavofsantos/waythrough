package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const manifestSizeMax = 1 << 20

type Scenario struct {
	Name   string     `json:"name"`
	Tool   string     `json:"tool"`
	File   string     `json:"file"`
	Line   int        `json:"line"`
	Column int        `json:"column"`
	Symbol string     `json:"symbol"`
	Gold   []Location `json:"gold"`
}

type manifest struct {
	Scenarios []Scenario `json:"scenarios"`
}

func loadScenario(fixtureDirectory, scenarioName string) (Scenario, error) {
	if fixtureDirectory == "" {
		return Scenario{}, errors.New("fixture directory must not be empty")
	}
	if scenarioName == "" {
		return Scenario{}, errors.New("scenario name must not be empty")
	}

	manifestPath := filepath.Join(fixtureDirectory, "manifest.json")
	document, err := readManifest(manifestPath)
	if err != nil {
		return Scenario{}, err
	}
	return findScenario(document, scenarioName)
}

func readManifest(manifestPath string) (manifest, error) {
	info, err := os.Stat(manifestPath)
	if err != nil {
		return manifest{}, fmt.Errorf("stat %s: %w", manifestPath, err)
	}
	if !info.Mode().IsRegular() {
		return manifest{}, fmt.Errorf("%s is not a regular file", manifestPath)
	}
	if info.Size() > manifestSizeMax {
		return manifest{}, fmt.Errorf("%s is larger than %d bytes", manifestPath, manifestSizeMax)
	}

	file, err := os.Open(manifestPath)
	if err != nil {
		return manifest{}, fmt.Errorf("open %s: %w", manifestPath, err)
	}
	data, err := io.ReadAll(io.LimitReader(file, manifestSizeMax+1))
	closeErr := file.Close()
	if err != nil {
		return manifest{}, fmt.Errorf("read %s: %w", manifestPath, err)
	}
	if closeErr != nil {
		return manifest{}, fmt.Errorf("close %s: %w", manifestPath, closeErr)
	}
	if len(data) > manifestSizeMax {
		return manifest{}, fmt.Errorf("%s is larger than %d bytes", manifestPath, manifestSizeMax)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document manifest
	if err := decoder.Decode(&document); err != nil {
		return manifest{}, fmt.Errorf("decode %s: %w", manifestPath, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return manifest{}, fmt.Errorf("decode %s: multiple JSON documents", manifestPath)
		}
		return manifest{}, fmt.Errorf("decode %s after document: %w", manifestPath, err)
	}
	return document, nil
}

func findScenario(document manifest, scenarioName string) (Scenario, error) {
	seen := make(map[string]struct{}, len(document.Scenarios))
	for _, scenario := range document.Scenarios {
		if err := validateScenario(scenario); err != nil {
			return Scenario{}, err
		}
		if _, exists := seen[scenario.Name]; exists {
			return Scenario{}, fmt.Errorf("manifest has duplicate scenario %q", scenario.Name)
		}
		seen[scenario.Name] = struct{}{}
		if scenario.Name == scenarioName {
			return scenario, nil
		}
	}

	return Scenario{}, fmt.Errorf("manifest has no scenario %q", scenarioName)
}

func validateScenario(scenario Scenario) error {
	if scenario.Name == "" {
		return errors.New("scenario name must not be empty")
	}
	if scenario.Tool != "get_definition" {
		return fmt.Errorf("scenario %q has unsupported tool %q", scenario.Name, scenario.Tool)
	}
	if !isFixtureRelativePath(scenario.File) {
		return fmt.Errorf("scenario %q file must be a relative path", scenario.Name)
	}
	if scenario.Line < 1 || scenario.Column < 1 {
		return fmt.Errorf("scenario %q position must be 1-based", scenario.Name)
	}
	if scenario.Symbol == "" {
		return fmt.Errorf("scenario %q symbol must not be empty", scenario.Name)
	}
	if len(scenario.Gold) == 0 {
		return fmt.Errorf("scenario %q must have at least one gold location", scenario.Name)
	}
	seenLocations := make(map[Location]struct{}, len(scenario.Gold))
	for _, location := range scenario.Gold {
		if !isFixtureRelativePath(location.File) || location.Line < 1 || location.Column < 1 {
			return fmt.Errorf("scenario %q has an invalid gold location", scenario.Name)
		}
		if _, exists := seenLocations[location]; exists {
			return fmt.Errorf("scenario %q gold locations contain duplicates", scenario.Name)
		}
		seenLocations[location] = struct{}{}
	}
	return nil
}

func isFixtureRelativePath(path string) bool {
	if path == "" || filepath.IsAbs(path) {
		return false
	}
	cleanPath := filepath.Clean(path)
	return cleanPath != "." && cleanPath != ".." &&
		!strings.HasPrefix(cleanPath, ".."+string(filepath.Separator))
}
