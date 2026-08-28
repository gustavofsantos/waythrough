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
	path, err := userConfigPath()
	if err != nil {
		return config.Config{}, "", err
	}

	cfg, err := config.Load(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return config.Config{}, path, fmt.Errorf(
				"user configuration %s is missing; run waythrough init: %w", path, err)
		}
		return config.Config{}, path, err
	}
	return cfg, path, nil
}
