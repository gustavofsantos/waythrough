package daemon_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gustavofsantos/waythrough/internal/daemon"
)

const testKey = "0123456789abcdef0123456789abcdef"

type echoInput struct {
	Text string `json:"text"`
}

type echoOutput struct {
	Text string `json:"text"`
}

// echoServer is the smallest MCP server a session can call, so these specs
// read the daemon's lifecycle rather than any tool's behavior.
func echoServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "echo", Version: "0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo"}, func(
		_ context.Context, _ *mcp.CallToolRequest, in echoInput,
	) (*mcp.CallToolResult, echoOutput, error) {
		return nil, echoOutput(in), nil
	})
	return server
}

// shortSocketPath keeps the socket within sun_path. Ginkgo's per-spec
// temporary directories carry the spec name and can exceed it.
func shortSocketPath() string {
	dir, err := os.MkdirTemp("", "wtd")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

type runningDaemon struct {
	socket string
	cancel context.CancelFunc
	done   chan struct{}
}

func startDaemon(options daemon.Options) runningDaemon {
	socket := shortSocketPath()
	listener, err := daemon.Listen(socket)
	Expect(err).NotTo(HaveOccurred())

	options.Key = testKey
	options.Logger = slog.New(slog.DiscardHandler)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		daemon.Serve(ctx, listener, echoServer(), options)
	}()
	DeferCleanup(func() {
		cancel()
		Eventually(done, 10*time.Second).Should(BeClosed())
	})
	return runningDaemon{socket: socket, cancel: cancel, done: done}
}

// bufferedReader lets the MCP client read through the buffered reader that
// consumed the greeting. Its Close does nothing: the transport closes its
// writer too, which is the same connection, and that close is the one that
// counts.
type bufferedReader struct {
	io.Reader
}

func (bufferedReader) Close() error { return nil }

func listen(socket string) net.Listener {
	var config net.ListenConfig
	listener, err := config.Listen(context.Background(), "unix", socket)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(listener.Close)
	return listener
}

func dial(socket string) (net.Conn, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(context.Background(), "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", socket, err)
	}
	return conn, nil
}

// attach dials the daemon, reads its greeting, and opens an MCP session.
func attach(socket string) *mcp.ClientSession {
	conn, err := dial(socket)
	Expect(err).NotTo(HaveOccurred())
	reader := bufio.NewReader(conn)
	Expect(daemon.ReadGreeting(conn, reader, testKey)).To(Succeed())

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(),
		&mcp.IOTransport{Reader: bufferedReader{reader}, Writer: conn}, nil)
	Expect(err).NotTo(HaveOccurred())
	return session
}

func echo(session *mcp.ClientSession, text string) string {
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "echo", Arguments: map[string]any{"text": text},
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(result.IsError).To(BeFalse())
	return result.StructuredContent.(map[string]any)["text"].(string)
}

var _ = Describe("Serve", func() {
	It("serves several sessions at once, and drains after the last one leaves", func() {
		running := startDaemon(daemon.Options{
			StartupGrace: 10 * time.Second, Linger: 100 * time.Millisecond,
		})

		first := attach(running.socket)
		second := attach(running.socket)
		Expect(echo(first, "one")).To(Equal("one"))
		Expect(echo(second, "two")).To(Equal("two"))

		Expect(first.Close()).To(Succeed())
		Consistently(running.done, 300*time.Millisecond).ShouldNot(BeClosed(),
			"one session is still connected")
		Expect(echo(second, "still here")).To(Equal("still here"))

		Expect(second.Close()).To(Succeed())
		Eventually(running.done, 5*time.Second).Should(BeClosed())
		Expect(running.socket).NotTo(BeAnExistingFile(),
			"a drained daemon must remove its socket so the next client starts a new one")
	})

	It("drains when no session arrives within the startup grace", func() {
		running := startDaemon(daemon.Options{
			StartupGrace: 100 * time.Millisecond, Linger: time.Hour,
		})
		Eventually(running.done, 5*time.Second).Should(BeClosed())
	})

	It("lets the spawning client attach even when the linger is zero", func() {
		running := startDaemon(daemon.Options{StartupGrace: 10 * time.Second, Linger: 0})
		Consistently(running.done, 200*time.Millisecond).ShouldNot(BeClosed())

		session := attach(running.socket)
		Expect(echo(session, "first")).To(Equal("first"))
		Expect(session.Close()).To(Succeed())
		Eventually(running.done, 5*time.Second).Should(BeClosed())
	})

	It("keeps serving when a session arrives within the linger", func() {
		running := startDaemon(daemon.Options{
			StartupGrace: 10 * time.Second, Linger: 300 * time.Millisecond,
		})
		Expect(attach(running.socket).Close()).To(Succeed())

		returning := attach(running.socket)
		// Outlast the linger armed by the first departure. The arrival
		// superseded that timer, so the daemon must still be serving.
		Consistently(running.done, 600*time.Millisecond).ShouldNot(BeClosed())
		Expect(echo(returning, "back")).To(Equal("back"))
		Expect(returning.Close()).To(Succeed())
		Eventually(running.done, 5*time.Second).Should(BeClosed())
	})

	It("uncounts a client that leaves before reading its greeting", func() {
		running := startDaemon(daemon.Options{
			StartupGrace: 10 * time.Second, Linger: 50 * time.Millisecond,
		})
		conn, err := dial(running.socket)
		Expect(err).NotTo(HaveOccurred())
		Expect(conn.Close()).To(Succeed())

		Eventually(running.done, 5*time.Second).Should(BeClosed(),
			"a leaked session count would keep the daemon alive forever")
	})

	It("refuses sessions beyond its maximum with a busy line", func() {
		running := startDaemon(daemon.Options{StartupGrace: 10 * time.Second, Linger: time.Hour})
		for range daemon.MaxSessions {
			conn, err := dial(running.socket)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(conn.Close)
			Expect(daemon.ReadGreeting(conn, bufio.NewReader(conn), testKey)).To(Succeed())
		}

		conn, err := dial(running.socket)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(conn.Close)
		err = daemon.ReadGreeting(conn, bufio.NewReader(conn), testKey)
		Expect(err).To(MatchError(daemon.ErrBusy))
	})

	It("closes every session and returns when its context ends", func() {
		running := startDaemon(daemon.Options{StartupGrace: 10 * time.Second, Linger: time.Hour})
		session := attach(running.socket)

		ended := make(chan struct{})
		go func() {
			_ = session.Wait()
			close(ended)
		}()

		running.cancel()
		Eventually(running.done, 10*time.Second).Should(BeClosed())
		Eventually(ended, 5*time.Second).Should(BeClosed(),
			"the client must see its session end, not hang on a dead daemon")
	})
})

var _ = Describe("ReadGreeting", func() {
	// exchange plays a daemon over a real Unix socket, as production uses:
	// it says daemonSays, then closes its end, which a draining daemon does
	// without saying anything at all.
	exchange := func(daemonSays string) error {
		listener := listen(shortSocketPath())
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_, _ = io.WriteString(conn, daemonSays)
			_ = conn.Close()
		}()

		conn, err := dial(listener.Addr().String())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(conn.Close)
		return daemon.ReadGreeting(conn, bufio.NewReader(conn), testKey)
	}

	It("accepts the greeting for its own key", func() {
		Expect(exchange("waythrough-daemon 1 " + testKey + "\n")).To(Succeed())
	})

	It("reports a draining daemon's silent close as no greeting", func() {
		Expect(exchange("")).To(MatchError(daemon.ErrNoGreeting))
	})

	It("rejects a greeting for another key or protocol", func() {
		Expect(exchange("waythrough-daemon 2 " + testKey + "\n")).To(
			MatchError(ContainSubstring("unexpected daemon greeting")))
	})

	It("bounds a line that never ends", func() {
		Expect(exchange(string(make([]byte, 200)))).To(MatchError(ContainSubstring("exceeds")))
	})
})

var _ = Describe("AcquireLock", func() {
	It("excludes a second holder until the first releases", func() {
		path := filepath.Join(GinkgoT().TempDir(), "d.lock")
		first, err := daemon.AcquireLock(path, time.Now().Add(time.Second))
		Expect(err).NotTo(HaveOccurred())
		Expect(first.RecordPID()).To(Succeed())
		Expect(daemon.RecordedPID(path)).To(Equal(os.Getpid()))

		_, err = daemon.AcquireLock(path, time.Now().Add(100*time.Millisecond))
		Expect(err).To(MatchError(daemon.ErrLockTimeout))

		Expect(first.Release()).To(Succeed())
		second, err := daemon.AcquireLock(path, time.Now().Add(time.Second))
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Release()).To(Succeed())
	})
})
