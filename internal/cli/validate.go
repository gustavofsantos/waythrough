package cli

import (
	"github.com/spf13/cobra"

	"github.com/gustavofsantos/waythrough/internal/config"
)

func newValidateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate the user waythrough config file",
	}

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runValidate()
	}

	return cmd
}

func runValidate() error {
	cfg, _, err := loadUserConfig()
	if err != nil {
		return err
	}

	return config.Validate(cfg)
}
