// Package cli implements the waythrough command-line interface.
package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// Execute runs the waythrough CLI with args and returns the process exit
// code. It writes normal output to stdout and errors to stderr.
func Execute(args []string, stdout, stderr io.Writer) int {
	root := newRootCommand()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SilenceUsage = true
	root.SilenceErrors = true

	if err := root.Execute(); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// configFlag registers the --config flag shared by every subcommand that
// reads waythrough.yaml, and returns the value cobra will fill in.
func configFlag(cmd *cobra.Command, verb string) *string {
	usage := fmt.Sprintf("path to the config file to %s", verb)
	return cmd.Flags().String("config", "waythrough.yaml", usage)
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:     "waythrough",
		Short:   "Waythrough manages configured LSP servers for coding agents",
		Version: version,
	}

	root.AddCommand(newInitCommand())
	root.AddCommand(newValidateCommand())
	root.AddCommand(newServeCommand())

	return root
}
