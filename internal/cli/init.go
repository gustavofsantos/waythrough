package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const defaultConfig = `language_servers:
  - name: clojure-lsp
    command: clojure-lsp
    args: []
    filetypes:
      .clj: clojure
      .cljc: clojure
      .cljs: clojurescript
`

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
		return runInit(configPath)
	}

	return cmd
}

func runInit(configPath string) error {
	if _, err := os.Stat(configPath); err == nil {
		return fmt.Errorf("config file already exists: %s", configPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check %s: %w", configPath, err)
	}

	if err := os.WriteFile(configPath, []byte(defaultConfig), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", configPath, err)
	}

	return nil
}
