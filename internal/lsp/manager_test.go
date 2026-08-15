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

func readSyncLog(path string) []syncEvent {
	data, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())

	var events []syncEvent
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
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

	When("a language server reports workDoneProgress for its startup work", func() {
		It("blocks WaitReady until the progress token closes, then returns", func() {
			entry := config.LanguageServer{
				Name:      "fake",
				Command:   fakelspPath,
				Args:      []string{"-progress", "-progress-delay=100ms"},
				Filetypes: map[string]string{".fake": "fake"},
			}

			manager := lsp.NewManager(GinkgoT().TempDir(), []config.LanguageServer{entry},
				lsp.WithProgressDebounce(20*time.Millisecond))

			Expect(manager.Start(ctx)).To(Succeed())

			err := manager.WaitReady(ctx, "fake", 20*time.Millisecond)
			Expect(err).To(HaveOccurred(), "the server is still indexing and should not be ready yet")

			err = manager.WaitReady(ctx, "fake", 2*time.Second)
			Expect(err).NotTo(HaveOccurred(), "the progress token closed, so the server should now be ready")
		})
	})

	When("a language server's readiness is handshake", func() {
		It("is ready as soon as the handshake completes, without waiting on progress", func() {
			entry := config.LanguageServer{
				Name:      "fake",
				Command:   fakelspPath,
				Readiness: config.ReadinessHandshake,
				Filetypes: map[string]string{".fake": "fake"},
			}

			manager := lsp.NewManager(GinkgoT().TempDir(), []config.LanguageServer{entry})
			Expect(manager.Start(ctx)).To(Succeed())

			Expect(manager.WaitReady(ctx, "fake", time.Second)).To(Succeed())
		})
	})

	When("a language server's readiness is progress but it opens no token", func() {
		It("becomes ready once the debounce window elapses", func() {
			entry := config.LanguageServer{
				Name:      "fake",
				Command:   fakelspPath, // no -progress flag: this server never reports
				Filetypes: map[string]string{".fake": "fake"},
			}

			manager := lsp.NewManager(GinkgoT().TempDir(), []config.LanguageServer{entry},
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
			entry := config.LanguageServer{
				Name:      "fake",
				Command:   fakelspPath,
				Args:      []string{"-crash-marker=" + marker},
				Readiness: config.ReadinessHandshake,
				Filetypes: map[string]string{".fake": "fake"},
			}

			manager := lsp.NewManager(GinkgoT().TempDir(), []config.LanguageServer{entry},
				lsp.WithRestartLimit(3, time.Minute))
			Expect(manager.Start(ctx)).To(Succeed())

			Expect(manager.WaitReady(ctx, "fake", 2*time.Second)).To(Succeed())
			_, err := os.Stat(marker)
			Expect(err).NotTo(HaveOccurred(), "expected the first attempt to have crashed and left its marker")
		})
	})

	When("a language server exits more times than the restart limit allows within the window", func() {
		It("stops restarting it and reports it failed", func() {
			entry := config.LanguageServer{
				Name:      "fake",
				Command:   fakelspPath,
				Args:      []string{"-crash"},
				Filetypes: map[string]string{".fake": "fake"},
			}

			manager := lsp.NewManager(GinkgoT().TempDir(), []config.LanguageServer{entry},
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
		It("sends shutdown and exit to a running server and waits for it to exit on its own", func() {
			entry := config.LanguageServer{
				Name:      "fake",
				Command:   fakelspPath,
				Readiness: config.ReadinessHandshake,
				Filetypes: map[string]string{".fake": "fake"},
			}

			manager := lsp.NewManager(GinkgoT().TempDir(), []config.LanguageServer{entry},
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
			entry := config.LanguageServer{
				Name:      "fake",
				Command:   fakelspPath,
				Args:      []string{"-ignore-exit"},
				Readiness: config.ReadinessHandshake,
				Filetypes: map[string]string{".fake": "fake"},
			}

			manager := lsp.NewManager(GinkgoT().TempDir(), []config.LanguageServer{entry},
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
			entry := config.LanguageServer{
				Name:      "fake",
				Command:   filepath.Join(GinkgoT().TempDir(), "no-such-binary"),
				Filetypes: map[string]string{".fake": "fake"},
			}

			manager := lsp.NewManager(GinkgoT().TempDir(), []config.LanguageServer{entry},
				lsp.WithRestartLimit(0, time.Minute))
			Expect(manager.Start(ctx)).To(Succeed())

			err := manager.WaitReady(ctx, "fake", 2*time.Second)
			Expect(err).To(HaveOccurred(), "the command does not exist, so the server should fail to start")

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
			root := GinkgoT().TempDir()
			file = filepath.Join(root, "main.fake")
			Expect(os.WriteFile(file, []byte("hello world"), 0o644)).To(Succeed())

			syncLog = filepath.Join(GinkgoT().TempDir(), "sync.log")
			entry := config.LanguageServer{
				Name:      "fake",
				Command:   fakelspPath,
				Args:      []string{"-sync-log=" + syncLog},
				Readiness: config.ReadinessHandshake,
				Filetypes: map[string]string{".fake": "fake"},
			}

			manager = lsp.NewManager(root, []config.LanguageServer{entry})
			Expect(manager.Start(ctx)).To(Succeed())
			Expect(manager.WaitReady(ctx, "fake", time.Second)).To(Succeed())
		})

		It("sends the file's actual content via didOpen, at version 1, on the first sync", func() {
			_, err := manager.Definition(ctx, "fake", file, 1, 1)
			Expect(err).NotTo(HaveOccurred())

			Expect(readSyncLog(syncLog)).To(Equal([]syncEvent{
				{Method: "textDocument/didOpen", Version: 1, Text: "hello world"},
			}))
		})

		It("sends the new content via didChange, with the version bumped, when the file has changed since the last sync", func() {
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
