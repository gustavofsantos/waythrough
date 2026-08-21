package lsp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gustavofsantos/waythrough/internal/config"
	"github.com/gustavofsantos/waythrough/internal/lsp"
)

// syncEvent mirrors one JSON line fakelsp's -sync-log writes on every
// didOpen/didChange, letting a test see the content and version Waythrough
// actually sent, not merely that some document is open.
type syncEvent struct {
	Method  string `json:"method"`
	Version int32  `json:"version"`
	Text    string `json:"text"`
}

// fakeManager starts a manager whose one "fake" server runs with args, and
// returns it with the path of a main.fake file holding content. It is the
// fixture every spec that asks the fake a question needs, and it returns
// only once the server is ready to be asked.
func fakeManager(ctx context.Context, content string, args ...string) (*lsp.Manager, string) {
	root := GinkgoT().TempDir()
	file := filepath.Join(root, "main.fake")
	Expect(os.WriteFile(file, []byte(content), 0o644)).To(Succeed())

	manager := lsp.NewManager(root, []config.LanguageServer{fakeEntry(args...)})
	Expect(manager.Start(ctx)).To(Succeed())
	Expect(manager.WaitReady(ctx, "fake", time.Second)).To(Succeed())
	return manager, file
}

// fakeEntry configures the one "fake" language server every spec here
// names: fakelsp for the .fake extension, ready as soon as its handshake
// completes. Only the flags it runs with differ between specs.
func fakeEntry(args ...string) config.LanguageServer {
	return config.LanguageServer{
		Name:      "fake",
		Command:   fakelspPath,
		Args:      args,
		Readiness: config.ReadinessHandshake,
		Filetypes: map[string]string{".fake": "fake"},
	}
}

// indexingFakeEntry is fakeEntry under the readiness gate a server with
// background work uses: ready only once every progress token it opens has
// closed.
func indexingFakeEntry(args ...string) config.LanguageServer {
	entry := fakeEntry(args...)
	entry.Readiness = config.ReadinessProgress
	return entry
}

// logLines reads one of the append-only logs fakelsp writes, as one record
// per line with the blank lines dropped. Every such log is read this way,
// and only what a line holds differs between them.
func logLines(path string) []string {
	data, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())

	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func readSyncLog(path string) []syncEvent {
	var events []syncEvent
	for _, line := range logLines(path) {
		var e syncEvent
		Expect(json.Unmarshal([]byte(line), &e)).To(Succeed())
		events = append(events, e)
	}
	return events
}

var _ = Describe("Manager", func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
	})

	AfterEach(func() {
		cancel()
	})

	When("the named server is not configured", func() {
		It("returns an error naming it", func() {
			manager := lsp.NewManager(GinkgoT().TempDir(), nil)

			_, err := manager.Status("missing")
			Expect(err).To(MatchError(ContainSubstring(`"missing"`)))
		})
	})

	When("servers are configured for demand startup", func() {
		It("starts only the first requested server, once across concurrent waiters", func() {
			requestedLog := filepath.Join(GinkgoT().TempDir(), "requested.log")
			unusedLog := filepath.Join(GinkgoT().TempDir(), "unused.log")

			requested := fakeEntry("-instance-log=" + requestedLog)
			unused := fakeEntry("-instance-log=" + unusedLog)
			unused.Name = "unused"
			unused.Filetypes = map[string]string{".unused": "unused"}

			manager := lsp.NewManager(GinkgoT().TempDir(),
				[]config.LanguageServer{requested, unused}, lsp.WithDemandStart())
			Expect(manager.Start(ctx)).To(Succeed())
			Expect(manager.Status("fake")).To(Equal(lsp.StatusIdle))
			Expect(manager.Status("unused")).To(Equal(lsp.StatusIdle))

			Consistently(func() bool {
				_, requestedErr := os.Stat(requestedLog)
				_, unusedErr := os.Stat(unusedLog)
				return os.IsNotExist(requestedErr) && os.IsNotExist(unusedErr)
			}, 100*time.Millisecond).Should(BeTrue(),
				"Start should not launch any demand-started server")

			const waiters = 8
			results := make(chan error, waiters)
			for range waiters {
				go func() {
					results <- manager.WaitReady(ctx, "fake", 2*time.Second)
				}()
			}
			for range waiters {
				Expect(<-results).To(Succeed())
			}

			Expect(logLines(requestedLog)).To(HaveLen(1),
				"concurrent first requests must share one supervisor")
			Expect(manager.Status("fake")).To(Equal(lsp.StatusReady))
			Expect(manager.Status("unused")).To(Equal(lsp.StatusIdle))
			_, err := os.Stat(unusedLog)
			Expect(os.IsNotExist(err)).To(BeTrue(),
				"requesting one server must not index the root with another")
		})
	})

	When("a language server reports workDoneProgress for its startup work", func() {
		It("blocks WaitReady until the progress token closes, then returns", func() {
			entry := indexingFakeEntry("-progress", "-progress-delay=100ms")

			manager := lsp.NewManager(GinkgoT().TempDir(), []config.LanguageServer{entry},
				lsp.WithProgressDebounce(20*time.Millisecond))

			Expect(manager.Start(ctx)).To(Succeed())

			err := manager.WaitReady(ctx, "fake", 20*time.Millisecond)
			Expect(err).To(HaveOccurred(),
				"the server is still indexing and should not be ready yet")

			err = manager.WaitReady(ctx, "fake", 2*time.Second)
			Expect(err).NotTo(HaveOccurred(),
				"the progress token closed, so the server should now be ready")
		})
	})

	When("a language server's readiness is handshake", func() {
		It("is ready as soon as the handshake completes, without waiting on progress", func() {
			manager := lsp.NewManager(GinkgoT().TempDir(),
				[]config.LanguageServer{fakeEntry()})
			Expect(manager.Start(ctx)).To(Succeed())

			Expect(manager.WaitReady(ctx, "fake", time.Second)).To(Succeed())
		})
	})

	When("a language server's readiness is progress but it opens no token", func() {
		It("becomes ready once the debounce window elapses", func() {
			// No -progress flag: this server never reports any progress.
			manager := lsp.NewManager(GinkgoT().TempDir(),
				[]config.LanguageServer{indexingFakeEntry()},
				lsp.WithProgressDebounce(50*time.Millisecond))
			Expect(manager.Start(ctx)).To(Succeed())

			err := manager.WaitReady(ctx, "fake", 10*time.Millisecond)
			Expect(err).To(HaveOccurred(), "the debounce window has not elapsed yet")

			Expect(manager.WaitReady(ctx, "fake", time.Second)).To(Succeed())
		})
	})

	When("a language-server subprocess exits and Waythrough did not request its shutdown", func() {
		It("starts a new subprocess, which becomes ready on its own", func() {
			marker := filepath.Join(GinkgoT().TempDir(), "crashed-once")
			entry := fakeEntry("-crash-marker=" + marker)

			manager := lsp.NewManager(GinkgoT().TempDir(), []config.LanguageServer{entry},
				lsp.WithRestartLimit(3, time.Minute))
			Expect(manager.Start(ctx)).To(Succeed())

			Expect(manager.WaitReady(ctx, "fake", 2*time.Second)).To(Succeed())
			_, err := os.Stat(marker)
			Expect(err).NotTo(HaveOccurred(),
				"expected the first attempt to have crashed and left its marker")
		})
	})

	When("a language server exits more times than the restart limit allows", func() {
		It("stops restarting it and reports it failed", func() {
			manager := lsp.NewManager(GinkgoT().TempDir(),
				[]config.LanguageServer{fakeEntry("-crash")},
				lsp.WithRestartLimit(3, time.Minute))
			Expect(manager.Start(ctx)).To(Succeed())

			err := manager.WaitReady(ctx, "fake", 5*time.Second)
			Expect(err).To(HaveOccurred())

			status, err := manager.Status("fake")
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(lsp.StatusFailed))
		})
	})

	Describe("Shutdown", func() {
		It("sends shutdown and exit, then waits for the server to exit on its own", func() {
			manager := lsp.NewManager(GinkgoT().TempDir(),
				[]config.LanguageServer{fakeEntry()},
				lsp.WithShutdownGrace(2*time.Second))
			Expect(manager.Start(ctx)).To(Succeed())
			Expect(manager.WaitReady(ctx, "fake", time.Second)).To(Succeed())

			done := make(chan struct{})
			go func() {
				defer close(done)
				Expect(manager.Shutdown(ctx)).To(Succeed())
			}()

			Eventually(done, 500*time.Millisecond).Should(BeClosed(),
				"a server that answers exit should not need the kill fallback")
		})

		It("kills a server that never acts on exit, instead of hanging forever", func() {
			manager := lsp.NewManager(GinkgoT().TempDir(),
				[]config.LanguageServer{fakeEntry("-ignore-exit")},
				lsp.WithShutdownGrace(100*time.Millisecond))
			Expect(manager.Start(ctx)).To(Succeed())
			Expect(manager.WaitReady(ctx, "fake", time.Second)).To(Succeed())

			done := make(chan struct{})
			go func() {
				defer close(done)
				Expect(manager.Shutdown(ctx)).To(Succeed())
			}()

			Eventually(done, time.Second).Should(BeClosed(),
				"Shutdown should kill a server that ignores exit rather than hang")
		})

		It("does not panic when shutting down a server that never spawned a process", func() {
			entry := fakeEntry()
			entry.Command = filepath.Join(GinkgoT().TempDir(), "no-such-binary")

			manager := lsp.NewManager(GinkgoT().TempDir(), []config.LanguageServer{entry},
				lsp.WithRestartLimit(0, time.Minute))
			Expect(manager.Start(ctx)).To(Succeed())

			err := manager.WaitReady(ctx, "fake", 2*time.Second)
			Expect(err).To(HaveOccurred(),
				"the command does not exist, so the server should fail to start")

			status, err := manager.Status("fake")
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(lsp.StatusFailed))

			Expect(manager.Shutdown(ctx)).To(Succeed())
		})
	})

	Describe("syncing a file's content", func() {
		var (
			file    string
			syncLog string
			manager *lsp.Manager
		)

		BeforeEach(func() {
			syncLog = filepath.Join(GinkgoT().TempDir(), "sync.log")
			manager, file = fakeManager(ctx, "hello world", "-sync-log="+syncLog)
		})

		It("sends the file's actual content via didOpen, at version 1, on the first sync", func() {
			_, err := manager.Definition(ctx, "fake", file, 1, 1)
			Expect(err).NotTo(HaveOccurred())

			Expect(readSyncLog(syncLog)).To(Equal([]syncEvent{
				{Method: "textDocument/didOpen", Version: 1, Text: "hello world"},
			}))
		})

		It("sends the changed content via didChange, with the version bumped", func() {
			_, err := manager.Definition(ctx, "fake", file, 1, 1)
			Expect(err).NotTo(HaveOccurred())

			Expect(os.WriteFile(file, []byte("hello galaxy"), 0o644)).To(Succeed())

			_, err = manager.Definition(ctx, "fake", file, 1, 1)
			Expect(err).NotTo(HaveOccurred())

			Expect(readSyncLog(syncLog)).To(Equal([]syncEvent{
				{Method: "textDocument/didOpen", Version: 1, Text: "hello world"},
				{Method: "textDocument/didChange", Version: 2, Text: "hello galaxy"},
			}))
		})

		It("sends nothing further while the file's content matches what was last synced", func() {
			_, err := manager.Definition(ctx, "fake", file, 1, 1)
			Expect(err).NotTo(HaveOccurred())

			_, err = manager.Definition(ctx, "fake", file, 1, 1)
			Expect(err).NotTo(HaveOccurred())

			Expect(readSyncLog(syncLog)).To(HaveLen(1),
				"a second sync with unchanged content should not resend didOpen or didChange")
		})
	})
})
