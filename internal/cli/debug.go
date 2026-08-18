package cli

import (
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
)

// debugFlag registers --debug on a command that runs the MCP server, and
// returns the value cobra will fill in.
func debugFlag(cmd *cobra.Command) *bool {
	return cmd.Flags().Bool("debug", false,
		"log every MCP request, language-server lifecycle change, and "+
			"language-server stderr line to stderr")
}

// newLogger builds the logger the rest of serve writes through: a debug
// logger on stderr when --debug is set, and one that records nothing
// otherwise.
//
// Its one hard rule is where the records may not go. serve speaks MCP over
// stdio, so stdout carries JSON-RPC frames and nothing else; a single log
// byte written there desynchronizes the framing and the coding agent on the
// other end loses the session. Sending logs to stdout is a programmer
// mistake rather than an operational failure, so it is refused here instead
// of handled.
func newLogger(stderr io.Writer, debug bool) *slog.Logger {
	if !debug {
		return slog.New(slog.DiscardHandler)
	}
	if stderr == os.Stdout {
		panic("cli: debug logs must not go to stdout, which carries the MCP stdio frames")
	}

	return slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
