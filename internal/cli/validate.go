package cli

import (
	"github.com/spf13/cobra"

	"github.com/gustavofsantos/waythrough/internal/config"
)

func newValidateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate a waythrough.yaml config file",
	}

	configPath := configFlag(cmd, "validate")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runValidate(*configPath)
	}

	return cmd
}

func runValidate(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	return config.Validate(cfg)
}
