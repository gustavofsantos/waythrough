package daemon_test

import (
	"bytes"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gustavofsantos/waythrough/internal/daemon"
)

// attachPaths names a key's files in a short private directory.
func attachPaths() daemon.Paths {
	socket := shortSocketPath()
	dir := filepath.Dir(socket)
	return daemon.Paths{
		Socket: socket,
		Lock:   filepath.Join(dir, "d.lock"),
		Spawn:  filepath.Join(dir, "d.spawn"),
		Log:    filepath.Join(dir, "d.log"),
	}
}

// fakeDaemon accepts connections at socket and does what behave says with
// each one, standing in for a daemon in a state a real one rarely holds.
func fakeDaemon(socket string, behave func(net.Conn)) {
	listener := listen(socket)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go behave(conn)
		}
	}()
}

// countingStart records how often Attach starts a daemon.
func countingStart(starts *atomic.Int32, exitWith error) daemon.StartFunc {
	return func(time.Time) (<-chan error, error) {
		starts.Add(1)
		exited := make(chan error, 1)
		if exitWith != nil {
			exited <- exitWith
		}
		return exited, nil
	}
}

var _ = Describe("Attach", func() {
	It("fails at once, without starting a daemon, when the daemon is busy", func() {
		paths := attachPaths()
		fakeDaemon(paths.Socket, func(conn net.Conn) {
			_, _ = conn.Write([]byte("busy 64 sessions\n"))
		})
		var starts atomic.Int32

		_, err := daemon.Attach(daemon.AttachOptions{
			Paths: paths, Key: testKey, Deadline: time.Now().Add(10 * time.Second),
			Start: countingStart(&starts, nil),
		})
		Expect(err).To(MatchError(daemon.ErrBusy))
		Expect(starts.Load()).To(BeZero())
	})

	It("names the daemon's log when the daemon it started exits early", func() {
		paths := attachPaths()
		var starts atomic.Int32

		_, err := daemon.Attach(daemon.AttachOptions{
			Paths: paths, Key: testKey, Deadline: time.Now().Add(10 * time.Second),
			Start: countingStart(&starts, errors.New("exit status 1")),
		})
		Expect(err).To(MatchError(daemon.ErrDaemonExited))
		Expect(err.Error()).To(ContainSubstring(paths.Log))
		Expect(starts.Load()).To(Equal(int32(1)), "a client starts at most one daemon")
	})

	It("reports a daemon that never greets as hung, naming its log", func() {
		paths := attachPaths()
		fakeDaemon(paths.Socket, func(net.Conn) {})
		var starts atomic.Int32

		_, err := daemon.Attach(daemon.AttachOptions{
			Paths: paths, Key: testKey, Deadline: time.Now().Add(30 * time.Second),
			Start: countingStart(&starts, nil),
		})
		Expect(err).To(MatchError(ContainSubstring("did not greet")))
		Expect(err.Error()).To(ContainSubstring(paths.Log))
		Expect(starts.Load()).To(BeZero())
	})
})

var _ = Describe("CappedWriter", func() {
	It("passes writes up to its limit, then marks the cut once and drops the rest", func() {
		var out bytes.Buffer
		writer := daemon.NewCappedWriter(&out, 10)

		for _, record := range []string{"12345", "67890", "over", "more"} {
			written, err := writer.Write([]byte(record))
			Expect(err).NotTo(HaveOccurred())
			Expect(written).To(Equal(len(record)))
		}
		Expect(strings.Count(out.String(), "limit")).To(Equal(1))
		Expect(out.String()).To(HavePrefix("1234567890\n"))
		Expect(out.String()).NotTo(ContainSubstring("more"))
	})
})
