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
	// eager names the servers the daemon this session starts prestarts. A
	// daemon that already runs keeps the startup it was given.
	eager []string
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
	if err := checkEagerNames(options.eager); err != nil {
		return err
	}
	runtimeDir, err := daemon.RuntimeDir()
	if err != nil {
		return err
	}

	deadline := time.Now().Add(daemon.AttachTimeout)
	startFor := func(key string, paths daemon.Paths) daemon.StartFunc {
		return daemonStarter(root, key, paths, options)
	}
	session, err := attachToWorkspace(root, runtimeDir, deadline, logger, startFor)
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

// attachToWorkspace attaches with the key the configuration names now.
// startFor builds the function that starts a daemon for one key.
//
// A daemon refuses to serve when the configuration changed after this
// session read it, and exits at once. So when a started daemon exits, the
// key is computed again: a different key gets one more attach, and an
// unchanged one fails, since its daemon would only exit the same way again.
func attachToWorkspace(
	root, runtimeDir string,
	deadline time.Time,
	logger *slog.Logger,
	startFor func(key string, paths daemon.Paths) daemon.StartFunc,
) (daemon.Session, error) {
	const keyAttemptsMax = 2
	key, err := currentWorkspaceKey(root)
	if err != nil {
		return daemon.Session{}, err
	}
	for attempt := 1; ; attempt++ {
		paths, err := daemon.PathsFor(runtimeDir, key)
		if err != nil {
			return daemon.Session{}, err
		}

		session, attachErr := daemon.Attach(daemon.AttachOptions{
			Paths:    paths,
			Key:      key,
			Deadline: deadline,
			Start:    startFor(key, paths),
		})
		if attachErr == nil {
			logger.Debug("attached to shared daemon",
				slog.String("key", key), slog.String("log", paths.Log))
			return session, nil
		}
		if !errors.Is(attachErr, daemon.ErrDaemonExited) || attempt == keyAttemptsMax {
			return daemon.Session{}, fmt.Errorf("attach to shared daemon: %w", attachErr)
		}

		currentKey, err := currentWorkspaceKey(root)
		if err != nil {
			return daemon.Session{}, err
		}
		if currentKey == key {
			return daemon.Session{}, fmt.Errorf("attach to shared daemon: %w", attachErr)
		}
		key = currentKey
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

// checkEagerNames fails a misspelled --eager name here, on the agent's
// stderr, rather than only in the log of a daemon that refused to start. The
// daemon checks again against the configuration it reads itself.
func checkEagerNames(eager []string) error {
	if len(eager) == 0 {
		return nil
	}
	loaded, err := readUserConfig()
	if err != nil {
		return err
	}
	return validateEager(loaded.config, eager)
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
		// One flag per name, which the daemon reads as a string array, so
		// each name arrives as given rather than through a join and a split.
		for _, name := range options.eager {
			args = append(args, "--eager="+name)
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
