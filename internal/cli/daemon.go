package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/gustavofsantos/waythrough/internal/config"
	"github.com/gustavofsantos/waythrough/internal/daemon"
	"github.com/gustavofsantos/waythrough/internal/editor"
	"github.com/gustavofsantos/waythrough/internal/lsp"
)

// daemonOptions are what `serve --shared` passes to the daemon it starts.
type daemonOptions struct {
	key            string
	root           string
	attachDeadline time.Time
	linger         time.Duration
	debug          bool
}

// newDaemonCommand is the hidden command `serve --shared` starts. Users
// never run it: its arguments are a contract between two builds of the
// same binary, which the workspace key keeps identical.
func newDaemonCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "daemon",
		Short:  "Serve one workspace's shared language servers (started by serve --shared)",
		Hidden: true,
		Args:   cobra.NoArgs,
	}

	var options daemonOptions
	var attachDeadline string
	cmd.Flags().StringVar(&options.key, "key", "", "workspace key")
	cmd.Flags().StringVar(&options.root, "root", "", "workspace root")
	cmd.Flags().StringVar(&attachDeadline, "attach-deadline", "",
		"RFC 3339 time by which the starting session gives up attaching")
	cmd.Flags().DurationVar(&options.linger, "linger", defaultLinger,
		"how long to keep the language servers after the last session leaves")
	cmd.Flags().BoolVar(&options.debug, "debug", false, "log at debug level")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		deadline, err := time.Parse(time.RFC3339Nano, attachDeadline)
		if err != nil {
			return fmt.Errorf("--attach-deadline: %w", err)
		}
		options.attachDeadline = deadline
		if options.key == "" || options.root == "" {
			return errors.New("daemon needs --key and --root")
		}
		return runDaemon(cmd.ErrOrStderr(), options)
	}
	return cmd
}

// runDaemon holds the key's daemon lock for its whole life, serves sessions
// until none remain, and stops the language servers it started.
func runDaemon(stderr io.Writer, options daemonOptions) error {
	logger := newLogger(daemon.NewCappedWriter(stderr, daemon.LogBytesMax), options.debug)

	runtimeDir, err := daemon.RuntimeDir()
	if err != nil {
		return err
	}
	paths, err := daemon.PathsFor(runtimeDir, options.key)
	if err != nil {
		return err
	}

	// Waits out a predecessor that is still draining. Holding this lock is
	// what makes removing a stale socket and truncating the log safe.
	lock, err := daemon.AcquireLock(paths.Lock, options.attachDeadline)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	if err := lock.RecordPID(); err != nil {
		return err
	}
	if err := os.Truncate(paths.Log, 0); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("truncate daemon log: %w", err)
	}

	cfg, err := daemonConfig(options)
	if err != nil {
		return err
	}
	listener, err := daemon.Listen(paths.Socket)
	if err != nil {
		return err
	}

	manager := lsp.NewManager(options.root, cfg.LanguageServers,
		lsp.WithLogger(logger), lsp.WithDemandStart())
	if err := manager.Start(context.Background()); err != nil {
		_ = listener.Close()
		return err
	}

	// setsid already took this process out of the agent's session, so a
	// hangup there never reaches it. Ignoring SIGHUP covers being started
	// any other way.
	signal.Ignore(syscall.SIGHUP)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Debug("waythrough daemon serving")
	daemon.Serve(ctx, listener, editor.New(manager, cfg, logger), daemon.Options{
		Key:          options.key,
		StartupGrace: time.Until(options.attachDeadline),
		Linger:       options.linger,
		Logger:       logger,
	})

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
	logger.Debug("waythrough daemon stopped")
	return nil
}

// daemonConfig reads the configuration again and checks it still has the
// key the daemon was started for. The file may have changed after the
// starting session read it, and a daemon must never serve a configuration
// other than the one its key names.
func daemonConfig(options daemonOptions) (config.Config, error) {
	loaded, err := readUserConfig()
	if err != nil {
		return config.Config{}, err
	}
	if err := config.Validate(loaded.config); err != nil {
		return config.Config{}, err
	}
	key, err := workspaceKey(options.root, loaded.data)
	if err != nil {
		return config.Config{}, err
	}
	if key != options.key {
		return config.Config{}, fmt.Errorf(
			"workspace key changed from %s to %s since the session started this daemon",
			options.key, key)
	}
	return loaded.config, nil
}

// workspaceKey is the key every session and daemon for root computes the
// same way. See daemon.KeyInputs for why each input is in it.
func workspaceKey(root string, configData []byte) (string, error) {
	identity, err := daemon.BinaryIdentity(version)
	if err != nil {
		return "", err
	}
	return daemon.Key(daemon.KeyInputs{
		BinaryIdentity: identity,
		Root:           root,
		Config:         configData,
		Path:           os.Getenv("PATH"),
	}), nil
}
