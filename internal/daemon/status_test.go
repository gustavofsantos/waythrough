package daemon_test

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gustavofsantos/waythrough/internal/daemon"
	"github.com/gustavofsantos/waythrough/internal/lsp"
)

// startDaemonWithStatus is startDaemon with a status socket, which it
// returns beside the daemon.
func startDaemonWithStatus(options daemon.Options) (runningDaemon, string) {
	statusSocket := shortSocketPath()
	statusListener, err := daemon.Listen(statusSocket)
	Expect(err).NotTo(HaveOccurred())
	options.StatusListener = statusListener
	return startDaemon(options), statusSocket
}

func readStatus(socket string) daemon.Report {
	report, err := daemon.ReadStatus(socket, time.Now().Add(5*time.Second))
	Expect(err).NotTo(HaveOccurred())
	return report
}

var _ = Describe("Status", func() {
	It("reports who the daemon is, its sessions, and its language servers", func() {
		servers := []lsp.InstanceStats{
			{Name: "gopls", Root: "/work", Status: "ready", Health: lsp.HealthDegraded},
		}
		running, statusSocket := startDaemonWithStatus(daemon.Options{
			StartupGrace:    time.Hour,
			Linger:          time.Hour,
			Root:            "/work",
			Version:         "v9.9.9",
			LanguageServers: func() []lsp.InstanceStats { return servers },
		})

		report := readStatus(statusSocket)
		Expect(report.Format).To(Equal(daemon.StatusFormat))
		Expect(report.Key).To(Equal(testKey))
		Expect(report.PID).To(Equal(os.Getpid()))
		Expect(report.Root).To(Equal("/work"))
		Expect(report.Version).To(Equal("v9.9.9"))
		Expect(report.StartedAt).To(BeTemporally("~", time.Now(), 10*time.Second))
		Expect(report.Sessions.State).To(Equal(daemon.StateStarting))
		Expect(report.Sessions.Max).To(Equal(daemon.MaxSessions))
		Expect(report.Sessions.DrainAt).
			To(BeTemporally("~", time.Now().Add(time.Hour), time.Minute))
		Expect(report.Runtime.Goroutines).To(BeNumerically(">", 0))
		Expect(report.Runtime.HeapBytes).To(BeNumerically(">", 0))
		Expect(report.LanguageServers).To(Equal(servers))
		Expect(report.Health).To(Equal(lsp.HealthDegraded),
			"a daemon is as healthy as its least healthy server")

		session := attach(running.socket)
		Expect(echo(session, "hello")).To(Equal("hello"))
		serving := readStatus(statusSocket).Sessions
		Expect(serving.State).To(Equal(daemon.StateServing))
		Expect(serving.Active).To(Equal(1))
		Expect(serving.AdmittedTotal).To(Equal(uint64(1)))
		Expect(serving.DrainAt.IsZero()).To(BeTrue(), "no timer runs while a session is connected")

		Expect(session.Close()).To(Succeed())
		Eventually(func() daemon.DaemonState { return readStatus(statusSocket).Sessions.State }).
			Should(Equal(daemon.StateLingering))
	})

	// A status read that counted as a session would stop and re-arm the
	// drain timer on every read, so watching an idle daemon would keep it
	// alive for as long as someone watched.
	It("never keeps an idle daemon alive, however often it is read", func() {
		running, statusSocket := startDaemonWithStatus(daemon.Options{
			StartupGrace: 300 * time.Millisecond, Linger: time.Hour,
		})

		started := time.Now()
		reads := 0
		for !isClosed(running.done) && time.Since(started) < 5*time.Second {
			if _, err := daemon.ReadStatus(statusSocket, time.Now().Add(time.Second)); err == nil {
				reads++
			}
			time.Sleep(20 * time.Millisecond)
		}
		Expect(running.done).To(BeClosed())
		Expect(time.Since(started)).To(BeNumerically("<", 2*time.Second))
		Expect(reads).To(BeNumerically(">", 1), "the reads must overlap the startup grace")
		Expect(statusSocket).NotTo(BeAnExistingFile(),
			"a stopped daemon must remove its status socket")
	})

	It("reports a socket file with no daemon behind it as not running", func() {
		statusSocket := shortSocketPath()
		listener, err := daemon.Listen(statusSocket)
		Expect(err).NotTo(HaveOccurred())
		// A killed daemon leaves its socket file; this keeps the file while
		// nobody listens.
		listener.SetUnlinkOnClose(false)
		Expect(listener.Close()).To(Succeed())

		_, err = daemon.ReadStatus(statusSocket, time.Now().Add(time.Second))
		Expect(err).To(MatchError(daemon.ErrNotRunning))
	})

	It("refuses a report larger than its limit", func() {
		statusSocket := shortSocketPath()
		fakeDaemon(statusSocket, func(conn net.Conn) {
			oversized := make([]byte, daemon.StatusReportBytesMax+1)
			_, _ = conn.Write(oversized)
		})

		_, err := daemon.ReadStatus(statusSocket, time.Now().Add(5*time.Second))
		Expect(err).To(MatchError(ContainSubstring("exceeds")))
	})

	It("accepts a report of exactly the limit, its newline included", func() {
		statusSocket := shortSocketPath()
		fakeDaemon(statusSocket, func(conn net.Conn) {
			report := []byte(`{"format": 1, "pid": 7}`)
			padding := bytes.Repeat([]byte(" "), daemon.StatusReportBytesMax-len(report)-1)
			_, _ = conn.Write(append(append(report, padding...), '\n'))
		})

		report, err := daemon.ReadStatus(statusSocket, time.Now().Add(5*time.Second))
		Expect(err).NotTo(HaveOccurred())
		Expect(report.PID).To(Equal(7))
	})

	It("refuses a report in a format it does not read", func() {
		statusSocket := shortSocketPath()
		fakeDaemon(statusSocket, func(conn net.Conn) {
			_, _ = conn.Write([]byte(`{"format": 99, "pid": 7}` + "\n"))
		})

		_, err := daemon.ReadStatus(statusSocket, time.Now().Add(5*time.Second))
		Expect(err).To(MatchError(ContainSubstring("status format 99")))
	})
})

var _ = Describe("RemoveStaleSockets", func() {
	var paths daemon.Paths

	BeforeEach(func() {
		var err error
		paths, err = daemon.PathsFor(filepath.Dir(shortSocketPath()), testKey)
		Expect(err).NotTo(HaveOccurred())
		for _, socket := range []string{paths.Socket, paths.Status} {
			listener, err := daemon.Listen(socket)
			Expect(err).NotTo(HaveOccurred())
			listener.SetUnlinkOnClose(false)
			Expect(listener.Close()).To(Succeed())
		}
	})

	It("removes the sockets a killed daemon left", func() {
		Expect(daemon.RemoveStaleSockets(paths)).To(BeTrue())
		Expect(paths.Socket).NotTo(BeAnExistingFile())
		Expect(paths.Status).NotTo(BeAnExistingFile())
	})

	// A daemon that has closed its sockets still holds its lock while it
	// stops its servers, and a starting one holds it before it listens.
	It("removes nothing while a daemon holds the key's lock", func() {
		lock, err := daemon.AcquireLock(paths.Lock, time.Now().Add(time.Second))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(lock.Release)

		Expect(daemon.RemoveStaleSockets(paths)).To(BeFalse())
		Expect(paths.Socket).To(BeAnExistingFile())
		Expect(paths.Status).To(BeAnExistingFile())
	})
})

var _ = Describe("DaemonKeys", func() {
	It("lists the keys with a status socket, in order, up to the limit", func() {
		dir := filepath.Dir(shortSocketPath())
		keys := []string{
			"00000000000000000000000000000001",
			"00000000000000000000000000000002",
			"00000000000000000000000000000003",
		}
		for _, key := range keys {
			listener, err := daemon.Listen(filepath.Join(dir, key+".status"))
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(listener.Close)
		}
		// Neither of these is a daemon's status socket.
		Expect(os.WriteFile(filepath.Join(dir, keys[0]+".log"), nil, 0o600)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "stray.status"), nil, 0o600)).To(Succeed())

		listed, omitted, err := daemon.DaemonKeys(dir, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(Equal(keys[:2]))
		Expect(omitted).To(Equal(1))
	})
})

func isClosed(channel <-chan struct{}) bool {
	select {
	case <-channel:
		return true
	default:
		return false
	}
}
