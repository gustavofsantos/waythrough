package lsp_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gustavofsantos/waythrough/internal/config"
	"github.com/gustavofsantos/waythrough/internal/lsp"
)

// recordedLogs is a buffer a spec may read while the goroutines a Manager
// owns are still writing to it. slog serializes writes through one handler,
// but a spec reading the buffer is a reader that handler knows nothing
// about, so the lock here covers that side too.
type recordedLogs struct {
	mu       sync.Mutex
	recorded bytes.Buffer
}

func (l *recordedLogs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// bytes.Buffer.Write documents that it always returns a nil error, and
	// panics rather than fail to grow, so there is no error here to pass on.
	written, _ := l.recorded.Write(p)
	return written, nil
}

func (l *recordedLogs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.recorded.String()
}

// debugLogger is the logger --debug builds, pointed at a buffer instead of
// at the process's stderr.
func debugLogger() (*slog.Logger, *recordedLogs) {
	logs := &recordedLogs{}
	handler := slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(handler), logs
}

var _ = Describe("what a manager records about its language servers", func() {
	var ctx context.Context

	BeforeEach(func() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		DeferCleanup(cancel)
	})

	When("a debug logger is configured", func() {
		It("records the server reaching ready, and what it said on its own stderr", func() {
			logger, logs := debugLogger()
			manager := lsp.NewManager(
				GinkgoT().TempDir(),
				[]config.LanguageServer{fakeEntry("-stderr-line=loaded 3 dependencies")},
				lsp.WithLogger(logger))
			DeferCleanup(func() { _ = manager.Shutdown(context.Background()) })

			Expect(manager.Start(ctx)).To(Succeed())
			Expect(manager.WaitReady(ctx, "fake", 5*time.Second)).To(Succeed())

			// Every assertion here waits rather than reads once. A record is
			// written after the transition it describes — deliberately, since
			// the ready record is emitted outside the lock that publishes
			// readiness — so WaitReady returning does not mean the records
			// have landed.
			//
			// Each one names the rendered attribute rather than the text
			// alone, because the server's arguments are logged too and this
			// spec would pass on those without the stderr stream being read
			// at all.
			Eventually(logs.String).Should(SatisfyAll(
				ContainSubstring(`line="loaded 3 dependencies"`),
				ContainSubstring("language server starting"),
				ContainSubstring("language server ready"),
				ContainSubstring("server=fake"),
			), "a language server's stderr is the one place it explains itself, "+
				"and --debug exists to stop that going to /dev/null")
		})

		It("records the tail a server left unterminated when it died", func() {
			logger, logs := debugLogger()
			manager := lsp.NewManager(
				GinkgoT().TempDir(),
				[]config.LanguageServer{fakeEntry(
					"-stderr-partial=fatal: no runtime found", "-crash")},
				lsp.WithLogger(logger),
				lsp.WithRestartLimit(0, time.Minute))
			DeferCleanup(func() { _ = manager.Shutdown(context.Background()) })

			Expect(manager.Start(ctx)).To(Succeed())
			Expect(manager.WaitReady(ctx, "fake", 5*time.Second)).NotTo(Succeed())

			Eventually(logs.String).Should(SatisfyAll(
				// The tail reaches a record only through the flush wait
				// performs once the process is gone. The server's arguments
				// carry the same text, so the record's own shape is what is
				// asserted rather than the text alone.
				ContainSubstring(`line="fatal: no runtime found"`),
				ContainSubstring("language server gave up after repeated exits"),
			))
		})
	})

	// This is the shape of every run without --debug: the logger is there,
	// but nothing is listening below warning level. What it pins is that
	// the debug gate covers the language server's stderr too, which is the
	// one part of this that would otherwise cost work on every byte a
	// server writes whether or not anyone reads it.
	When("the configured logger has no debug handler", func() {
		It("records neither lifecycle nor stderr, and answers as it always did", func() {
			logs := &recordedLogs{}
			handler := slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn})

			root := GinkgoT().TempDir()
			file := filepath.Join(root, "main.fake")
			Expect(os.WriteFile(file, []byte("package main\n"), 0o644)).To(Succeed())

			manager := lsp.NewManager(
				root,
				[]config.LanguageServer{fakeEntry(
					"-stderr-line=this line goes nowhere",
					"-definition-line=0", "-definition-column=8")},
				lsp.WithLogger(slog.New(handler)))
			DeferCleanup(func() { _ = manager.Shutdown(context.Background()) })

			Expect(manager.Start(ctx)).To(Succeed())
			Expect(manager.WaitReady(ctx, "fake", 5*time.Second)).To(Succeed())

			locations, err := manager.Definition(ctx, "fake", file, 1, 1)
			Expect(err).NotTo(HaveOccurred())
			Expect(locations).To(HaveLen(1))

			Expect(logs.String()).NotTo(ContainSubstring("language server stderr"))
			Expect(logs.String()).NotTo(ContainSubstring("language server ready"))
		})
	})
})
