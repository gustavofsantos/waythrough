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
	"runtime/metrics"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gustavofsantos/waythrough/internal/lsp"
)

// The status socket answers each connection with one JSON report and a
// newline, then closes it. It is a socket of its own, apart from the session
// socket, so that a status reader is never counted as a session: a session
// stops the linger timer and restarts it when it leaves, so a status probe
// on the session socket would keep an idle daemon alive for as long as
// someone watched it. A separate socket also answers while the session
// socket refuses new sessions at MaxSessions, which is when a person most
// needs to look.
const (
	// StatusFormat is the version of Report's JSON shape. A reader refuses
	// a report in any other format rather than misread it.
	StatusFormat = 1
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

// Report is one daemon's account of itself.
type Report struct {
	Format    int          `json:"format"`
	Key       string       `json:"key"`
	PID       int          `json:"pid"`
	Root      string       `json:"root"`
	Version   string       `json:"version"`
	StartedAt time.Time    `json:"started_at"`
	Health    lsp.Health   `json:"health"`
	Sessions  SessionStats `json:"sessions"`
	Runtime   RuntimeStats `json:"runtime"`
	// LanguageServers lists every configured server: see lsp.Manager.Stats.
	LanguageServers []lsp.InstanceStats `json:"language_servers"`
}

// RuntimeStats is the daemon process's own footprint. A goroutine count
// that climbs while the sessions do not is the first sign of a leak.
type RuntimeStats struct {
	Goroutines uint64 `json:"goroutines"`
	// HeapBytes is the memory live and not yet collected heap objects
	// occupy.
	HeapBytes uint64 `json:"heap_bytes"`
	// TotalBytes is all the memory the Go runtime has mapped.
	TotalBytes uint64 `json:"total_bytes"`
}

// serveStatus answers status connections on listener until it closes, and
// returns a channel that closes once the accept loop and every connection
// it started have finished. A nil listener serves nothing.
func serveStatus(
	ctx context.Context, listener *net.UnixListener, sessions *registry, options Options,
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
		acceptConnections(ctx, listener, options.Logger, func() bool { return false },
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
					answerStatus(conn, sessions, options)
				}()
			})
		// Each handler is bounded by statusWriteTimeout, so this wait is.
		handlers.Wait()
	}()
	return done
}

func answerStatus(conn *net.UnixConn, sessions *registry, options Options) {
	defer func() { _ = conn.Close() }()

	uid, err := peerUID(conn)
	if err != nil || uid != os.Geteuid() {
		sessions.refusePeer()
		options.Logger.Warn("daemon refused a status peer",
			slog.Int("peer_uid", uid), slog.Any("error", err))
		return
	}

	data, err := json.Marshal(buildReport(sessions, options))
	if err != nil {
		options.Logger.Warn("daemon status report failed", slog.String("error", err.Error()))
		return
	}
	data = append(data, '\n')
	if len(data) > StatusReportBytesMax {
		options.Logger.Warn("daemon status report exceeds its limit",
			slog.Int("bytes", len(data)), slog.Int("limit_bytes", StatusReportBytesMax))
		return
	}
	if err := conn.SetWriteDeadline(time.Now().Add(statusWriteTimeout)); err != nil {
		return
	}
	if _, err := conn.Write(data); err != nil {
		options.Logger.Debug("daemon status write failed", slog.String("error", err.Error()))
	}
}

func buildReport(sessions *registry, options Options) Report {
	report := Report{
		Format:          StatusFormat,
		Key:             options.Key,
		PID:             os.Getpid(),
		Root:            options.Root,
		Version:         options.Version,
		StartedAt:       sessions.startedAt,
		Sessions:        sessions.stats(),
		Runtime:         readRuntimeStats(),
		LanguageServers: []lsp.InstanceStats{},
	}
	if options.LanguageServers != nil {
		report.LanguageServers = options.LanguageServers()
	}
	report.Health = daemonHealth(report.Sessions, report.LanguageServers)
	return report
}

// daemonHealth is the worst of its servers' health. A daemon refusing
// sessions at its limit is degraded too, whatever its servers say.
func daemonHealth(sessions SessionStats, servers []lsp.InstanceStats) lsp.Health {
	degraded := sessions.Active >= sessions.Max
	for _, server := range servers {
		switch server.Health {
		case lsp.HealthFailing:
			return lsp.HealthFailing
		case lsp.HealthDegraded:
			degraded = true
		case lsp.HealthHealthy:
		}
	}
	if degraded {
		return lsp.HealthDegraded
	}
	return lsp.HealthHealthy
}

// readRuntimeStats reads runtime/metrics, which, unlike
// runtime.ReadMemStats, does not stop the world.
func readRuntimeStats() RuntimeStats {
	samples := []metrics.Sample{
		{Name: "/sched/goroutines:goroutines"},
		{Name: "/memory/classes/heap/objects:bytes"},
		{Name: "/memory/classes/total:bytes"},
	}
	metrics.Read(samples)
	return RuntimeStats{
		Goroutines: sampleUint64(samples[0]),
		HeapBytes:  sampleUint64(samples[1]),
		TotalBytes: sampleUint64(samples[2]),
	}
}

// sampleUint64 reads a metric this Go release may not offer, as zero.
func sampleUint64(sample metrics.Sample) uint64 {
	if sample.Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return sample.Value.Uint64()
}

// ReadStatus asks the daemon whose status socket is at path for its report.
// It gives up at deadline, and it refuses a socket held by another user, a
// report larger than StatusReportBytesMax, and a report in another format.
func ReadStatus(path string, deadline time.Time) (Report, error) {
	dialer := net.Dialer{Deadline: deadline}
	conn, err := dialer.Dial("unix", path)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
			return Report{}, fmt.Errorf("%w at %s", ErrNotRunning, path)
		}
		return Report{}, fmt.Errorf("dial daemon status: %w", err)
	}
	defer func() { _ = conn.Close() }()
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return Report{}, fmt.Errorf("dial %s returned %T", path, conn)
	}
	if err := checkDaemonPeer(unixConn); err != nil {
		return Report{}, err
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return Report{}, fmt.Errorf("set status deadline: %w", err)
	}

	// The newline ends the report, so the read does not depend on the
	// daemon closing the connection. The limit counts the newline, as the
	// daemon's does, and one byte past it is what tells a report at the
	// limit from one over it.
	reader := bufio.NewReader(io.LimitReader(conn, StatusReportBytesMax+1))
	data, err := reader.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return Report{}, fmt.Errorf("read daemon status: %w", err)
	}
	if len(data) > StatusReportBytesMax {
		return Report{}, fmt.Errorf("daemon status exceeds %d bytes", StatusReportBytesMax)
	}
	if len(data) == 0 {
		return Report{}, errors.New("daemon closed the status connection unanswered: " +
			"it is stopping, it is answering other status readers, " +
			"or its report failed, which its log records")
	}
	if err != nil {
		return Report{}, errors.New("daemon closed the status connection mid-report")
	}

	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return Report{}, fmt.Errorf("decode daemon status: %w", err)
	}
	if report.Format != StatusFormat {
		return Report{}, fmt.Errorf(
			"daemon pid %d reports status format %d; this waythrough reads format %d",
			report.PID, report.Format, StatusFormat)
	}
	return report, nil
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
