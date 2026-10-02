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
	"github.com/gustavofsantos/waythrough/internal/status"
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

	debug := debugFlag(cmd)
	shared := cmd.Flags().Bool("shared", false,
		"share language servers with every other --shared session in this "+
			"workspace, through a daemon started on demand")
	linger := cmd.Flags().Duration("linger", defaultLinger,
		"with --shared, how long the daemon this session starts keeps its "+
			"language servers after the last session leaves")
	eager := cmd.Flags().StringSlice("eager", nil,
		"names of configured language servers to start at once for the "+
			"workspace root, rather than on the first tool call that needs them; "+
			"with --shared, applies to the daemon this session starts")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		logger := newLogger(cmd.ErrOrStderr(), *debug)
		if !*shared {
			return runServe(logger, *eager)
		}
		if *linger < 0 || *linger > maxLinger {
			return fmt.Errorf("--linger must be between 0 and %s, got %s", maxLinger, *linger)
		}
		return runSharedServe(cmd.InOrStdin(), cmd.OutOrStdout(), logger, sharedOptions{
			linger: *linger,
			debug:  *debug,
			eager:  *eager,
		})
	}

	return cmd
}

func runServe(logger *slog.Logger, eager []string) error {
	cfg, configPath, err := loadUserConfig()
	if err != nil {
		return err
	}
	if err := config.Validate(cfg); err != nil {
		return err
	}
	if err := validateEager(cfg, eager); err != nil {
		return err
	}

	absConfigPath, err := filepath.Abs(configPath)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", configPath, err)
	}
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve workspace root: %w", err)
	}

	manager, err := startManager(root, cfg, logger, eager)
	if err != nil {
		return err
	}
	logServeStarted(logger, cfg, absConfigPath, root)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	source := status.Source{
		Root:            root,
		Version:         version,
		StartedAt:       time.Now(),
		LanguageServers: manager.Stats,
	}
	server := editor.New(manager, cfg, logger, source.Report)
	runErr := server.Run(ctx, &mcp.StdioTransport{})

	_ = shutdownManager(manager)

	if runErr != nil {
		return fmt.Errorf("mcp session: %w", runErr)
	}
	return nil
}

// validateEager checks that --eager names only configured servers, so a
// misspelled name fails at startup rather than leaving that server cold in
// silence.
func validateEager(cfg config.Config, eager []string) error {
	configured := make(map[string]bool, len(cfg.LanguageServers))
	for _, entry := range cfg.LanguageServers {
		configured[entry.Name] = true
	}
	for _, name := range eager {
		if !configured[name] {
			return fmt.Errorf("--eager names %q, which is not a configured language server", name)
		}
	}
	return nil
}

// startManager starts the manager for root in demand-start mode, then
// prestarts the servers eager names. On error, nothing it started runs.
//
// The language-server subprocesses live for as long as the caller runs, not
// for as long as any one MCP session does: only shutdownManager ends them,
// never the signal-aware context the MCP transport listens on.
func startManager(
	root string, cfg config.Config, logger *slog.Logger, eager []string,
) (*lsp.Manager, error) {
	manager := lsp.NewManager(root, cfg.LanguageServers,
		lsp.WithLogger(logger), lsp.WithDemandStart())
	if err := manager.Start(context.Background()); err != nil {
		return nil, err
	}
	if err := prestartEager(manager, logger, eager); err != nil {
		_ = shutdownManager(manager)
		return nil, err
	}
	return manager, nil
}

// prestartEager starts each server --eager names for the workspace root,
// without waiting for any to become ready: the MCP session must answer its
// initialize at once, and a slow server indexes while the agent gets going.
// A server that fails to start is reported as any other, through its status.
// The number of servers is bounded by the configuration validateEager
// checked the names against.
//
// A server whose root markers find no project from the root is skipped
// rather than refused, so one --eager list in a user-wide agent
// configuration still serves a workspace without that language; the server
// starts on demand there, as it would without the flag.
func prestartEager(manager *lsp.Manager, logger *slog.Logger, eager []string) error {
	for _, name := range eager {
		started, err := manager.Prestart(context.Background(), name)
		if err != nil {
			return fmt.Errorf("start %s eagerly: %w", name, err)
		}
		if !started {
			logger.Debug("eager start skipped: no root marker matches the workspace root",
				slog.String("language_server", name))
		}
	}
	return nil
}

// shutdownManager stops every language server manager started, waiting
// shutdownGrace at most.
func shutdownManager(manager *lsp.Manager) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	return manager.Shutdown(shutdownCtx)
}

func logServeStarted(
	logger *slog.Logger, cfg config.Config, absConfigPath, root string,
) {
	logAttributes := []any{
		slog.String("root", root),
		slog.Int("language_servers", len(cfg.LanguageServers)),
		slog.String("config", absConfigPath),
		slog.String("config_source", "user"),
	}
	logger.Debug("waythrough serving", logAttributes...)
}
