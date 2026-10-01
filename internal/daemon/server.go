package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// MaxSessions bounds the sessions one daemon serves at once, and so the
	// goroutines and descriptors it holds for them.
	MaxSessions = 64
	// sessionCloseGrace bounds how long a forced drain waits for session
	// handlers after it closes their connections.
	sessionCloseGrace = 5 * time.Second
	acceptRetryFirst  = 10 * time.Millisecond
	acceptRetryMax    = time.Second
)

// Options configure Serve.
type Options struct {
	// Key is the workspace key every greeting names.
	Key string
	// StartupGrace is how long the daemon waits for its first session. It
	// lasts until the spawning client's attach deadline. Another client can
	// still attach and leave first, and a short Linger can then drain the
	// daemon before the spawning client dials; Attach starts another daemon
	// when that happens.
	StartupGrace time.Duration
	// Linger is how long the daemon keeps its servers after the last
	// session ends. Zero drains at once.
	Linger time.Duration
	// Logger receives the daemon's lifecycle records.
	Logger *slog.Logger
}

// Listen removes a stale socket file and listens at path.
//
// Precondition: the caller holds this key's daemon lock. Only that proves
// no live daemon listens at path, so only then is removing it safe.
func Listen(path string) (*net.UnixListener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale socket %s: %w", path, err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	// The runtime directory already shuts out other users. This mode is the
	// second wall, for a directory whose mode later changes.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("restrict socket %s: %w", path, err)
	}
	return listener, nil
}

// Serve runs one MCP session of server for each connection listener
// accepts, until the daemon drains: when no session has been connected for
// the startup grace or the linger, or when ctx ends. It closes listener,
// which removes the socket file, and returns once every session handler has
// returned or sessionCloseGrace has passed.
//
// The caller then stops the language servers. Serve never does, so that the
// one owner of the servers is the one that started them.
func Serve(ctx context.Context, listener *net.UnixListener, server *mcp.Server, options Options) {
	if options.Logger == nil {
		panic("daemon: Serve needs a logger, got nil")
	}
	sessions := newRegistry(listener, options)
	sessions.armTimer(options.StartupGrace)

	var handlers sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		acceptSessions(ctx, listener, sessions, func(conn *net.UnixConn) {
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				serveSession(ctx, conn, server, sessions, options)
			}()
		})
	}()

	select {
	case <-sessions.drained:
		options.Logger.Debug("daemon draining: no sessions")
	case <-ctx.Done():
		options.Logger.Debug("daemon draining: stopped")
		sessions.forceDrain()
	}
	<-acceptDone
	waitWithGrace(&handlers, sessionCloseGrace, options.Logger)
}

// acceptSessions accepts until the listener closes. A failed accept that is
// not the close, such as running out of descriptors, is retried with a
// bounded backoff rather than ending the daemon its sessions rely on.
func acceptSessions(
	ctx context.Context,
	listener *net.UnixListener,
	sessions *registry,
	handle func(*net.UnixConn),
) {
	retry := acceptRetryFirst
	for {
		conn, err := listener.AcceptUnix()
		if err == nil {
			retry = acceptRetryFirst
			handle(conn)
			continue
		}
		if sessions.isDraining() || errors.Is(err, net.ErrClosed) {
			return
		}
		sessions.logger.Warn("daemon accept failed", slog.String("error", err.Error()))
		select {
		case <-time.After(retry):
		case <-ctx.Done():
			return
		}
		retry = min(2*retry, acceptRetryMax)
	}
}

// serveSession admits one connection and serves MCP on it until the client
// leaves. The decrement is deferred right after the admission, before any
// step that can fail, so every admitted session is uncounted exactly once.
func serveSession(
	ctx context.Context,
	conn *net.UnixConn,
	server *mcp.Server,
	sessions *registry,
	options Options,
) {
	defer func() { _ = conn.Close() }()

	uid, err := peerUID(conn)
	if err != nil || uid != os.Geteuid() {
		options.Logger.Warn("daemon refused a peer",
			slog.Int("peer_uid", uid), slog.Any("error", err))
		return
	}

	switch sessions.admit(conn) {
	case admissionDraining:
		return
	case admissionBusy:
		_ = writeLine(conn, busyPrefix+fmt.Sprintf("%d sessions\n", MaxSessions))
		return
	case admissionAccepted:
	}
	defer sessions.release(conn)

	if err := writeLine(conn, greetingLine(options.Key)); err != nil {
		options.Logger.Debug("daemon greeting failed", slog.String("error", err.Error()))
		return
	}
	reader := &endSignalingReader{conn: conn, ended: make(chan struct{})}
	session, err := server.Connect(ctx, &mcp.IOTransport{Reader: reader, Writer: conn}, nil)
	if err != nil {
		options.Logger.Warn("daemon session failed to connect", slog.String("error", err.Error()))
		return
	}
	awaitDeparture(session, reader.ended, options.Logger)
}

// awaitDeparture returns when the session ends, or sessionCloseGrace after
// its client stopped sending, whichever is first.
//
// A session's Wait returns only once every call it is handling has. A call
// stuck writing to a language server that stopped reading ignores its
// context, so its Wait would never return, and its count would keep the
// daemon and every server alive with no client left. Once the client has
// left and the grace has passed, the session is uncounted anyway. The stuck
// call ends when the drain stops the server it is writing to.
func awaitDeparture(session *mcp.ServerSession, clientEnded <-chan struct{}, logger *slog.Logger) {
	sessionEnded := make(chan struct{})
	go func() {
		_ = session.Wait()
		close(sessionEnded)
	}()

	select {
	case <-sessionEnded:
		return
	case <-clientEnded:
	}
	select {
	case <-sessionEnded:
	case <-time.After(sessionCloseGrace):
		logger.Warn("daemon session left with a call still running",
			slog.Duration("grace", sessionCloseGrace))
	}
}

// endSignalingReader closes ended when the first read from conn fails,
// which is how the daemon learns its client left: EOF when the client
// closed, or an error when the connection broke.
type endSignalingReader struct {
	conn  *net.UnixConn
	ended chan struct{}
	once  sync.Once
}

func (r *endSignalingReader) Read(buffer []byte) (int, error) {
	count, err := r.conn.Read(buffer)
	if err != nil {
		r.once.Do(func() { close(r.ended) })
		return count, fmt.Errorf("read session: %w", err)
	}
	return count, nil
}

func (r *endSignalingReader) Close() error {
	if err := r.conn.Close(); err != nil {
		return fmt.Errorf("close session: %w", err)
	}
	return nil
}

func waitWithGrace(group *sync.WaitGroup, grace time.Duration, logger *slog.Logger) {
	done := make(chan struct{})
	go func() {
		group.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(grace):
		logger.Warn("daemon session handlers outlived the close grace",
			slog.Duration("grace", grace))
	}
}
