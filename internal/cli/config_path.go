package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gustavofsantos/waythrough/internal/config"
)

const userConfigFilename = ".waythrough.yaml"

func userConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config home: %w", err)
	}
	if home == "" {
		return "", errors.New("resolve user config home: empty home directory")
	}
	return filepath.Join(home, userConfigFilename), nil
}

func loadUserConfig() (config.Config, string, error) {
	loaded, err := readUserConfig()
	return loaded.config, loaded.path, err
}

// userConfig is the user configuration together with the exact bytes it
// was parsed from, which a shared daemon's key hashes.
type userConfig struct {
	config config.Config
	path   string
	data   []byte
}

func readUserConfig() (userConfig, error) {
	path, err := userConfigPath()
	if err != nil {
		return userConfig{}, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return userConfig{path: path}, fmt.Errorf(
				"user configuration %s is missing; run waythrough init: %w", path, err)
		}
		return userConfig{path: path}, fmt.Errorf("read %s: %w", path, err)
	}
	cfg, err := config.Parse(data, path)
	if err != nil {
		return userConfig{path: path}, err
	}
	return userConfig{config: cfg, path: path, data: data}, nil
}
