package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/gustavofsantos/waythrough/internal/config"
	"github.com/gustavofsantos/waythrough/internal/daemon"
)

const (
	// defaultLinger keeps a warm index across an agent's MCP reconnect, and
	// across the gap between one session and the next.
	defaultLinger = 60 * time.Second
	// maxLinger bounds how long servers may outlive their last session.
	maxLinger = 24 * time.Hour
)

type sharedOptions struct {
	linger time.Duration
	debug  bool
}

// runSharedServe attaches this agent session to the workspace's daemon,
// starting one when none runs, and relays MCP between the agent's stdio and
// the daemon until either side closes.
func runSharedServe(
	stdin io.Reader, stdout io.Writer, logger *slog.Logger, options sharedOptions,
) error {
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve workspace root: %w", err)
	}
	runtimeDir, err := daemon.RuntimeDir()
	if err != nil {
		return err
	}

	deadline := time.Now().Add(daemon.AttachTimeout)
	session, err := attachToWorkspace(root, runtimeDir, deadline, logger, options)
	if err != nil {
		return err
	}

	// A signal ends this session alone. Closing the connection tells the
	// daemon this session left; the servers stay for the other sessions.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = session.Conn.Close()
	}()

	if err := daemon.Proxy(session, stdin, stdout); err != nil && ctx.Err() == nil {
		return fmt.Errorf("shared session: %w", err)
	}
	return nil
}

// attachToWorkspace attaches with the key the configuration names now. A
// daemon this session started exits at once when the configuration changed
// after it was read here, so the key is computed again, and the attach
// retried once, before that is reported as a failure.
func attachToWorkspace(
	root, runtimeDir string,
	deadline time.Time,
	logger *slog.Logger,
	options sharedOptions,
) (daemon.Session, error) {
	const keyAttemptsMax = 2
	var lastKey string
	for attempt := 1; ; attempt++ {
		key, err := currentWorkspaceKey(root)
		if err != nil {
			return daemon.Session{}, err
		}
		paths, err := daemon.PathsFor(runtimeDir, key)
		if err != nil {
			return daemon.Session{}, err
		}

		session, err := daemon.Attach(daemon.AttachOptions{
			Paths:    paths,
			Key:      key,
			Deadline: deadline,
			Start:    daemonStarter(root, key, paths, options),
		})
		if err == nil {
			logger.Debug("attached to shared daemon",
				slog.String("key", key), slog.String("log", paths.Log))
			return session, nil
		}
		retry := errors.Is(err, daemon.ErrDaemonExited) &&
			key != lastKey && attempt < keyAttemptsMax
		if !retry {
			return daemon.Session{}, fmt.Errorf("attach to shared daemon: %w", err)
		}
		lastKey = key
	}
}

// currentWorkspaceKey reads and validates the configuration, so that an
// invalid file fails here, on the agent's stderr, as it does without
// --shared, and returns the key for it.
func currentWorkspaceKey(root string) (string, error) {
	loaded, err := readUserConfig()
	if err != nil {
		return "", err
	}
	if err := config.Validate(loaded.config); err != nil {
		return "", err
	}
	return workspaceKey(root, loaded.data)
}

// daemonStarter starts `waythrough daemon` for one key, detached from this
// session: in a new session of its own, so the agent's Ctrl-C or hangup
// never reaches servers other sessions use, with stdin and stdout on
// /dev/null and stderr appended to the key's log.
//
// The log is opened for appending and never truncated here. A predecessor
// may still be draining and writing to it; the new daemon truncates it once
// it holds the lock.
func daemonStarter(root, key string, paths daemon.Paths, options sharedOptions) daemon.StartFunc {
	return func(deadline time.Time) (<-chan error, error) {
		executable, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locate waythrough executable: %w", err)
		}
		logFile, err := os.OpenFile(paths.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open daemon log: %w", err)
		}
		defer func() { _ = logFile.Close() }()

		args := []string{
			"daemon",
			"--key", key,
			"--root", root,
			"--attach-deadline", deadline.Format(time.RFC3339Nano),
			"--linger", options.linger.String(),
		}
		if options.debug {
			args = append(args, "--debug")
		}
		// The background context is deliberate: the daemon outlives this
		// session, so nothing this session owns may cancel it.
		command := exec.CommandContext(context.Background(), executable, args...)
		command.Dir = root
		command.Stderr = logFile
		command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := command.Start(); err != nil {
			return nil, fmt.Errorf("start daemon: %w", err)
		}

		// Waiting reaps the daemon if it exits while this session runs. If
		// this session exits first, the daemon is reparented and unaffected.
		exited := make(chan error, 1)
		go func() { exited <- command.Wait() }()
		return exited, nil
	}
}
