package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/gustavofsantos/waythrough/internal/config"
	"github.com/gustavofsantos/waythrough/internal/editor"
	"github.com/gustavofsantos/waythrough/internal/lsp"
)

// shutdownGrace bounds how long serve waits, after the MCP session ends,
// for the configured language servers to exit on their own before it kills
// them and returns.
const shutdownGrace = 10 * time.Second

func newServeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Waythrough MCP server over stdio",
	}

	configPath := configFlag(cmd, "serve from")
	debug := debugFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runServe(*configPath, newLogger(cmd.ErrOrStderr(), *debug))
	}

	return cmd
}

func runServe(configPath string, logger *slog.Logger) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if err := config.Validate(cfg); err != nil {
		return err
	}

	absConfigPath, err := filepath.Abs(configPath)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", configPath, err)
	}
	root := filepath.Dir(absConfigPath)

	// The language-server subprocesses live for as long as serve runs, not
	// for as long as any one MCP session does: only Shutdown below ends
	// them, never the signal-aware context the MCP transport listens on.
	manager := lsp.NewManager(root, cfg.LanguageServers, lsp.WithLogger(logger))
	if err := manager.Start(context.Background()); err != nil {
		return err
	}
	logger.Debug("waythrough serving",
		slog.String("config", absConfigPath),
		slog.String("root", root),
		slog.Int("language_servers", len(cfg.LanguageServers)))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := editor.New(manager, cfg, logger)
	runErr := server.Run(ctx, &mcp.StdioTransport{})

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)

	if runErr != nil {
		return fmt.Errorf("mcp session: %w", runErr)
	}
	return nil
}
