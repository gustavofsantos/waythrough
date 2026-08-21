package cli

import (
	"context"
	"errors"
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

type configPathSource int

const (
	implicitConfigPath configPathSource = iota
	explicitConfigPath
)

type serveConfig struct {
	config.Config
	usesBuiltInDefaults bool
}

func newServeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Waythrough MCP server over stdio",
	}

	configPath := configFlag(cmd, "serve from")
	debug := debugFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		pathSource := implicitConfigPath
		if cmd.Flags().Changed("config") {
			pathSource = explicitConfigPath
		}
		return runServe(*configPath, pathSource, newLogger(cmd.ErrOrStderr(), *debug))
	}

	return cmd
}

func runServe(
	configPath string, pathSource configPathSource, logger *slog.Logger,
) error {
	loaded, err := loadServeConfig(configPath, pathSource)
	if err != nil {
		return err
	}
	if err := config.Validate(loaded.Config); err != nil {
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
	managerOptions := []lsp.Option{lsp.WithLogger(logger)}
	if loaded.usesBuiltInDefaults {
		managerOptions = append(managerOptions, lsp.WithDemandStart())
	}
	manager := lsp.NewManager(root, loaded.LanguageServers, managerOptions...)
	if err := manager.Start(context.Background()); err != nil {
		return err
	}
	logger.Debug("waythrough serving",
		slog.String("config", absConfigPath),
		slog.String("root", root),
		slog.Int("language_servers", len(loaded.LanguageServers)))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := editor.New(manager, loaded.Config, logger)
	runErr := server.Run(ctx, &mcp.StdioTransport{})

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)

	if runErr != nil {
		return fmt.Errorf("mcp session: %w", runErr)
	}
	return nil
}

// loadServeConfig uses built-ins only for the implicit waythrough.yaml path.
// An explicit --config path is a user assertion that the file must exist, so
// silently replacing a typo there with defaults would start the wrong servers.
func loadServeConfig(configPath string, pathSource configPathSource) (serveConfig, error) {
	cfg, err := config.Load(configPath)
	if err == nil {
		return serveConfig{Config: cfg}, nil
	}
	if pathSource == implicitConfigPath && errors.Is(err, os.ErrNotExist) {
		return serveConfig{Config: config.Default(), usesBuiltInDefaults: true}, nil
	}
	return serveConfig{}, err
}
