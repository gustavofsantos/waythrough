package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/gustavofsantos/waythrough/internal/config"
)

const (
	initSelectionInputSizeMax = 4096
	initConfigFileMode        = 0o600
)

func newInitCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create the user waythrough config file",
	}

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		configPath, err := userConfigPath()
		if err != nil {
			return err
		}
		return runInit(configPath, cmd.InOrStdin(), cmd.OutOrStdout())
	}

	return cmd
}

func runInit(configPath string, input io.Reader, output io.Writer) error {
	if _, err := os.Stat(configPath); err == nil {
		return fmt.Errorf("config file already exists: %s", configPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check %s: %w", configPath, err)
	}

	presets := config.Presets()
	selectedIndexes, err := selectPresetIndexes(input, output, presets)
	if err != nil {
		return err
	}

	selected := make([]config.LanguageServer, 0, len(selectedIndexes))
	for _, index := range selectedIndexes {
		selected = append(selected, presets[index])
	}
	cfg := config.Config{LanguageServers: selected}
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("validate selected presets: %w", err)
	}

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("encode user config: %w", err)
	}
	if err := writeFileAtomically(configPath, string(data), initConfigFileMode); err != nil {
		return fmt.Errorf("write %s: %w", configPath, err)
	}

	return nil
}

func selectPresetIndexes(
	input io.Reader, output io.Writer, presets []config.LanguageServer,
) ([]int, error) {
	if _, err := io.WriteString(output, "Select language servers to configure:\n"); err != nil {
		return nil, fmt.Errorf("write init prompt: %w", err)
	}
	for index, preset := range presets {
		if _, err := fmt.Fprintf(output, "  %d) %s\n", index+1, preset.Name); err != nil {
			return nil, fmt.Errorf("write init prompt: %w", err)
		}
	}
	if _, err := io.WriteString(output,
		"Enter one or more numbers separated by commas, or all: "); err != nil {
		return nil, fmt.Errorf("write init prompt: %w", err)
	}

	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64), initSelectionInputSizeMax)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("read preset selection: %w", err)
		}
		return nil, errors.New("read preset selection: no selection")
	}

	selected, err := parsePresetSelection(scanner.Text(), len(presets))
	if err != nil {
		return nil, err
	}
	return selected, nil
}

func parsePresetSelection(input string, presetCount int) ([]int, error) {
	if presetCount == 0 {
		return nil, errors.New("no language-server presets are available")
	}
	selection := strings.TrimSpace(input)
	if selection == "" {
		return nil, errors.New("preset selection is empty")
	}
	if strings.EqualFold(selection, "all") {
		selected := make([]int, presetCount)
		for index := range selected {
			selected[index] = index
		}
		return selected, nil
	}

	parts := strings.Split(selection, ",")
	selected := make([]int, 0, len(parts))
	seen := make(map[int]struct{}, len(parts))
	for _, part := range parts {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || value < 1 || value > presetCount {
			return nil, fmt.Errorf(
				"invalid preset selection %q: choose numbers from 1 to %d",
				strings.TrimSpace(part), presetCount)
		}
		index := value - 1
		if _, exists := seen[index]; exists {
			return nil, fmt.Errorf("preset %d was selected more than once", value)
		}
		seen[index] = struct{}{}
		selected = append(selected, index)
	}
	return selected, nil
}
