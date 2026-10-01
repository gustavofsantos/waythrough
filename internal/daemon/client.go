package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

const (
	// AttachTimeout is the one deadline every attach step shares: waiting
	// for the spawn lock, for a predecessor's drain, and for a new daemon
	// to listen. A drain takes about 16 seconds at most.
	AttachTimeout = 30 * time.Second
	dialPollFirst = 10 * time.Millisecond
	dialPollMax   = 250 * time.Millisecond
)

// ErrDaemonExited reports that the daemon this client started exited before
// it greeted. Its log says why.
var ErrDaemonExited = errors.New("daemon exited before it accepted the session")

// StartFunc starts a daemon for the attaching key. It returns a channel that
// receives the daemon's exit once it exits, and is never closed otherwise.
type StartFunc func(deadline time.Time) (<-chan error, error)

// AttachOptions configure Attach.
type AttachOptions struct {
	Paths    Paths
	Key      string
	Deadline time.Time
	Start    StartFunc
}

// Session is an attached daemon session. MCP bytes are read through Reader,
// which may already hold bytes that followed the greeting, and written to
// Conn.
type Session struct {
	Conn   *net.UnixConn
	Reader *bufio.Reader
}

// Attach connects to the daemon for options.Key, starting one when none
// answers. Only one client at a time starts a daemon for a key: it holds the
// spawn lock, and it dials once more after taking the lock in case the
// previous holder started one.
//
// Each outcome has one response. A greeting attaches. No daemon, or one
// that closes before greeting because it is draining, is retried until the
// deadline, starting at most one daemon. A busy daemon, one that does not
// greet in time, and a daemon this client started that exits all fail at
// once, because waiting cannot change them.
func Attach(options AttachOptions) (Session, error) {
	session, err := attachOnce(options)
	if err == nil || !retryable(err) {
		return session, err
	}

	spawnLock, err := AcquireLock(options.Paths.Spawn, options.Deadline)
	if err != nil {
		return Session{}, fmt.Errorf("wait for another session to start the daemon: %w", err)
	}
	defer func() { _ = spawnLock.Release() }()

	var exited <-chan error
	wait := dialPollFirst
	for {
		session, err := attachOnce(options)
		if err == nil || !retryable(err) {
			return session, err
		}
		if exited == nil {
			exited, err = options.Start(options.Deadline)
			if err != nil {
				return Session{}, fmt.Errorf("start daemon: %w", err)
			}
		}

		remaining := time.Until(options.Deadline)
		if remaining <= 0 {
			return Session{}, fmt.Errorf(
				"no daemon accepted the session within %s; see %s",
				AttachTimeout, options.Paths.Log)
		}
		select {
		case exitErr := <-exited:
			return Session{}, fmt.Errorf("%w (%v); see %s",
				ErrDaemonExited, exitErr, options.Paths.Log)
		case <-time.After(min(wait, remaining)):
		}
		wait = min(2*wait, dialPollMax)
	}
}

// retryable reports whether err means no daemon has accepted yet: none is
// listening, or the one that is drains and closed without a greeting.
func retryable(err error) bool {
	var noDaemon *noDaemonError
	return errors.As(err, &noDaemon) || errors.Is(err, ErrNoGreeting)
}

type noDaemonError struct{ err error }

func (e *noDaemonError) Error() string { return "no daemon is listening: " + e.err.Error() }
func (e *noDaemonError) Unwrap() error { return e.err }

func attachOnce(options AttachOptions) (Session, error) {
	var dialer net.Dialer
	ctx, cancel := context.WithDeadline(context.Background(), options.Deadline)
	defer cancel()
	conn, err := dialer.DialContext(ctx, "unix", options.Paths.Socket)
	if err != nil {
		return Session{}, &noDaemonError{err: err}
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		return Session{}, fmt.Errorf("dial %s returned %T", options.Paths.Socket, conn)
	}

	if err := checkDaemonPeer(unixConn); err != nil {
		_ = conn.Close()
		return Session{}, err
	}
	reader := bufio.NewReader(unixConn)
	if err := ReadGreeting(unixConn, reader, options.Key); err != nil {
		_ = conn.Close()
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return Session{}, fmt.Errorf("daemon pid %d did not greet within %s; see %s: %w",
				RecordedPID(options.Paths.Lock), GreetingTimeout, options.Paths.Log, err)
		}
		return Session{}, err
	}
	return Session{Conn: unixConn, Reader: reader}, nil
}

// checkDaemonPeer refuses a socket held by another user. The runtime
// directory check guards the path; this guards the connection itself.
func checkDaemonPeer(conn *net.UnixConn) error {
	uid, err := peerUID(conn)
	if err != nil {
		return err
	}
	if uid != os.Geteuid() {
		return fmt.Errorf("daemon socket is held by uid %d, not %d", uid, os.Geteuid())
	}
	return nil
}

// Proxy copies the agent's MCP bytes to the daemon and the daemon's back,
// and returns when the daemon closes the session. When the agent closes
// stdin, the write side is shut, so the daemon ends the session and the
// copy back finishes with everything the daemon had still to say.
func Proxy(session Session, stdin io.Reader, stdout io.Writer) error {
	go func() {
		_, _ = io.Copy(session.Conn, stdin)
		_ = session.Conn.CloseWrite()
	}()
	if _, err := io.Copy(stdout, session.Reader); err != nil {
		return fmt.Errorf("copy daemon output: %w", err)
	}
	return nil
}
