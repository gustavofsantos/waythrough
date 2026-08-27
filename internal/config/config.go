// Package config defines the waythrough.yaml schema: the language servers
// Waythrough starts and how to know when each one is ready.
package config

import (
	"bytes"
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"
)

// Readiness names how Waythrough decides a language server can answer a
// request, not merely that its process has started.
type Readiness string

const (
	// ReadinessProgress waits for every LSP workDoneProgress token the
	// server opens to close before treating the server as ready.
	ReadinessProgress Readiness = "progress"
	// ReadinessHandshake treats the server as ready as soon as the
	// initialize/initialized handshake completes. An explicit opt-in for
	// servers that do no background indexing.
	ReadinessHandshake Readiness = "handshake"
)

// RootMarkers is an ordered list of project-root priorities. Every inner
// group has equal priority; groups that appear earlier take precedence even
// when a later group's marker is closer to the requested file.
type RootMarkers [][]string

// UnmarshalYAML accepts Neovim's compact shape: a scalar is one priority
// group, while a nested sequence gives several markers equal priority.
func (markers *RootMarkers) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("root_markers must be a sequence")
	}

	groups := make(RootMarkers, 0, len(value.Content))
	for index, item := range value.Content {
		switch item.Kind {
		case yaml.ScalarNode:
			if item.Tag != "!!str" {
				return fmt.Errorf("root_markers item %d must be a string", index)
			}
			groups = append(groups, []string{item.Value})
		case yaml.SequenceNode:
			group := make([]string, len(item.Content))
			for markerIndex, marker := range item.Content {
				if marker.Kind != yaml.ScalarNode || marker.Tag != "!!str" {
					return fmt.Errorf(
						"root_markers group %d marker %d must be a string",
						index, markerIndex)
				}
				group[markerIndex] = marker.Value
			}
			groups = append(groups, group)
		default:
			return fmt.Errorf("root_markers item %d must be a string or sequence", index)
		}
	}

	*markers = groups
	return nil
}

// LanguageServer is one entry in waythrough.yaml: how to start a language
// server and which files it handles.
type LanguageServer struct {
	Name                  string            `yaml:"name"`
	Command               string            `yaml:"command"`
	Args                  []string          `yaml:"args,omitempty"`
	Readiness             Readiness         `yaml:"readiness,omitempty"`
	RootMarkers           RootMarkers       `yaml:"root_markers,omitempty"`
	Filetypes             map[string]string `yaml:"filetypes"`
	Env                   map[string]string `yaml:"env,omitempty"`
	InitializationOptions map[string]any    `yaml:"initialization_options,omitempty"`
}

// Config is the parsed content of waythrough.yaml.
type Config struct {
	LanguageServers []LanguageServer `yaml:"language_servers"`
}

// Load reads and parses the waythrough.yaml file at path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}

	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}

	return cfg, nil
}
