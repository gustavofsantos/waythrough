package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gustavofsantos/waythrough/internal/status"
)

// The status socket answers each connection with one JSON status.Report and a
// newline, then closes it. It is a socket of its own, apart from the session
// socket, so that a status reader is never counted as a session: a session
// stops the linger timer and restarts it when it leaves, so a status probe
// on the session socket would keep an idle daemon alive for as long as
// someone watched it. A separate socket also answers while the session
// socket refuses new sessions at MaxSessions, which is when a person most
// needs to look.
const (
	// StatusReportBytesMax bounds one report, its newline included. The
	// largest real one, with every configured server at its root limit, is
	// a few tens of kilobytes.
	StatusReportBytesMax = 1 << 20
	// statusConnectionsMax bounds the status connections served at once. A
	// connection past it is closed unanswered, so a reader in a tight loop
	// costs the daemon a bounded amount of work.
	statusConnectionsMax = 4
	statusWriteTimeout   = 2 * time.Second
)

// ErrNotRunning reports a status socket with no daemon behind it: one that
// was killed and left its socket file, which the next daemon for the key
// removes.
var ErrNotRunning = errors.New("no daemon is listening")

// serveStatus answers status connections on listener until it closes, and
// returns a channel that closes once the accept loop and every connection
// it started have finished. A nil listener serves nothing.
func serveStatus(
	ctx context.Context, listener *net.UnixListener, running *Daemon,
) <-chan struct{} {
	done := make(chan struct{})
	if listener == nil {
		close(done)
		return done
	}

	slots := make(chan struct{}, statusConnectionsMax)
	var handlers sync.WaitGroup
	go func() {
		defer close(done)
		acceptConnections(ctx, listener, running.logger, func() bool { return false },
			func(conn *net.UnixConn) {
				select {
				case slots <- struct{}{}:
				default:
					_ = conn.Close()
					return
				}
				handlers.Add(1)
				go func() {
					defer handlers.Done()
					defer func() { <-slots }()
					answerStatus(conn, running)
				}()
			})
		// Each handler is bounded by statusWriteTimeout, so this wait is.
		handlers.Wait()
	}()
	return done
}

func answerStatus(conn *net.UnixConn, running *Daemon) {
	defer func() { _ = conn.Close() }()

	uid, err := peerUID(conn)
	if err != nil || uid != os.Geteuid() {
		running.sessions.refusePeer()
		running.logger.Warn("daemon refused a status peer",
			slog.Int("peer_uid", uid), slog.Any("error", err))
		return
	}

	data, err := json.Marshal(running.Report())
	if err != nil {
		running.logger.Warn("daemon status report failed", slog.String("error", err.Error()))
		return
	}
	data = append(data, '\n')
	if len(data) > StatusReportBytesMax {
		running.logger.Warn("daemon status report exceeds its limit",
			slog.Int("bytes", len(data)), slog.Int("limit_bytes", StatusReportBytesMax))
		return
	}
	if err := conn.SetWriteDeadline(time.Now().Add(statusWriteTimeout)); err != nil {
		return
	}
	if _, err := conn.Write(data); err != nil {
		running.logger.Debug("daemon status write failed", slog.String("error", err.Error()))
	}
}

// ReadStatus asks the daemon whose status socket is at path for its report.
// It gives up at deadline, and it refuses a socket held by another user, a
// report larger than StatusReportBytesMax, and a report in another format.
func ReadStatus(path string, deadline time.Time) (status.Report, error) {
	dialer := net.Dialer{Deadline: deadline}
	conn, err := dialer.Dial("unix", path)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
			return status.Report{}, fmt.Errorf("%w at %s", ErrNotRunning, path)
		}
		return status.Report{}, fmt.Errorf("dial daemon status: %w", err)
	}
	defer func() { _ = conn.Close() }()
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return status.Report{}, fmt.Errorf("dial %s returned %T", path, conn)
	}
	if err := checkDaemonPeer(unixConn); err != nil {
		return status.Report{}, err
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return status.Report{}, fmt.Errorf("set status deadline: %w", err)
	}

	data, err := readReportLine(conn)
	if err != nil {
		return status.Report{}, err
	}
	var report status.Report
	if err := json.Unmarshal(data, &report); err != nil {
		return status.Report{}, fmt.Errorf("decode daemon status: %w", err)
	}
	if err := checkDaemonReport(report); err != nil {
		return status.Report{}, err
	}
	return report, nil
}

// readReportLine reads the one line a daemon answers with. The newline ends
// the report, so the read does not depend on the daemon closing the
// connection. The limit counts the newline, as the daemon's does, and one
// byte past it is what tells a report at the limit from one over it.
func readReportLine(conn net.Conn) ([]byte, error) {
	reader := bufio.NewReader(io.LimitReader(conn, StatusReportBytesMax+1))
	data, err := reader.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read daemon status: %w", err)
	}
	if len(data) > StatusReportBytesMax {
		return nil, fmt.Errorf("daemon status exceeds %d bytes", StatusReportBytesMax)
	}
	if len(data) == 0 {
		return nil, errors.New("daemon closed the status connection unanswered: " +
			"it is stopping, it is answering other status readers, " +
			"or its report failed, which its log records")
	}
	if err != nil {
		return nil, errors.New("daemon closed the status connection mid-report")
	}
	return data, nil
}

// checkDaemonReport refuses a report this reader cannot trust to mean what
// it says: another format, or a daemon's report with no sessions.
func checkDaemonReport(report status.Report) error {
	if report.Format != status.Format {
		return fmt.Errorf(
			"daemon pid %d reports status format %d; this waythrough reads format %d",
			report.PID, report.Format, status.Format)
	}
	if report.Mode != status.ModeShared || report.Sessions == nil {
		return fmt.Errorf(
			"daemon pid %d sent a report with no sessions, in mode %q", report.PID, report.Mode)
	}
	return nil
}

// DaemonKeys lists the keys of the daemons in runtimeDir that have a status
// socket, in key order, at most limit of them. omitted counts the rest. A
// key here may belong to a daemon that was killed: ReadStatus tells.
func DaemonKeys(runtimeDir string, limit int) (keys []string, omitted int, err error) {
	if limit <= 0 {
		panic(fmt.Sprintf("daemon: DaemonKeys needs a positive limit, got %d", limit))
	}
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		return nil, 0, fmt.Errorf("list daemon runtime directory: %w", err)
	}
	for _, entry := range entries {
		key, isStatus := strings.CutSuffix(entry.Name(), statusSuffix)
		if !isStatus || entry.Type()&os.ModeSocket == 0 || !validKey(key) {
			continue
		}
		if len(keys) == limit {
			omitted++
			continue
		}
		keys = append(keys, key)
	}
	return keys, omitted, nil
}

// validKey reports whether name has the shape Key produces, so a stray
// file in the runtime directory is never taken for a daemon.
func validKey(name string) bool {
	if len(name) != 2*keyLengthBytes {
		return false
	}
	for _, character := range name {
		isDigit := character >= '0' && character <= '9'
		isHexLetter := character >= 'a' && character <= 'f'
		if !isDigit && !isHexLetter {
			return false
		}
	}
	return true
}

// RemoveStaleSockets removes the sockets of a key whose daemon was killed
// without removing them. It reports false, and removes nothing, while any
// process holds the key's daemon lock: a live daemon, or a starting one.
//
// Holding that lock is what makes the removal safe, as it is for Listen. It
// is taken without waiting and held only for the removal, so a daemon that
// starts meanwhile waits a moment for it, then listens anew.
func RemoveStaleSockets(paths Paths) (bool, error) {
	lock, err := AcquireLock(paths.Lock, time.Now())
	if errors.Is(err, ErrLockTimeout) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = lock.Release() }()

	for _, socket := range []string{paths.Status, paths.Socket} {
		if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("remove stale socket %s: %w", socket, err)
		}
	}
	return true, nil
}
