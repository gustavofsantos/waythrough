package lsp_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gustavofsantos/waythrough/internal/config"
	"github.com/gustavofsantos/waythrough/internal/lsp"
)

// readInstanceLog reads the pids fakelsp's -instance-log recorded, one per
// process start, in start order. It is how a spec tells one process from
// the next across a restart, which no other reading of a language server
// makes visible.
func readInstanceLog(path string) []int {
	var pids []int
	for _, line := range logLines(path) {
		pid, err := strconv.Atoi(line)
		Expect(err).NotTo(HaveOccurred(), "each instance-log line holds one pid")
		pids = append(pids, pid)
	}
	return pids
}

// processAlive reports whether pid names a process this test can still
// signal. Signal 0 delivers nothing: it only asks the kernel whether the
// process is there, which is the whole question a spec about a stopped
// subprocess needs answered.
func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

// killProcess ends a language-server process behind the manager's back, so
// a spec can stage the one thing it cannot ask for: an exit the manager
// never requested. The manager reaps the process, so this does not.
func killProcess(pid int) {
	process, err := os.FindProcess(pid)
	Expect(err).NotTo(HaveOccurred())
	Expect(process.Kill()).To(Succeed())
}

var _ = Describe("Restart", func() {
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

	When("a language server is running", func() {
		It("ends that process and answers the next request from a new one", func() {
			instanceLog := filepath.Join(GinkgoT().TempDir(), "instances.log")
			manager, file := fakeManager(ctx, "hello world", "-instance-log="+instanceLog)

			started := readInstanceLog(instanceLog)
			Expect(started).To(HaveLen(1))
			retired := started[0]

			Expect(manager.Restart(ctx, "fake")).To(Succeed())

			started = readInstanceLog(instanceLog)
			Expect(started).To(HaveLen(2), "the restart should have started a second process")
			Expect(started[1]).NotTo(Equal(retired), "the replacement must be a different process")

			Eventually(processAlive).WithArguments(retired).Should(BeFalse(),
				"the process the restart replaced should be gone")

			// The manager reports ready only once a server can answer, so a
			// request that follows the restart must reach the replacement
			// and be answered by it, with no wait of the caller's own.
			_, err := manager.Definition(ctx, "fake", file, 1, 1)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	When("a language server has given up after crashing too often", func() {
		It("takes a restart and serves again, since that is the state a restart is for", func() {
			root := GinkgoT().TempDir()
			file := filepath.Join(root, "main.fake")
			Expect(os.WriteFile(file, []byte("hello world"), 0o644)).To(Succeed())

			// The fake crashes on its first run only, so the restart faces a
			// server that can work and had simply stopped being tried.
			marker := filepath.Join(GinkgoT().TempDir(), "crashed-once")
			entry := config.LanguageServer{
				Name:      "fake",
				Command:   fakelspPath,
				Args:      []string{"-crash-marker=" + marker},
				Readiness: config.ReadinessHandshake,
				Filetypes: map[string]string{".fake": "fake"},
			}
			manager := lsp.NewManager(root, []config.LanguageServer{entry},
				lsp.WithRestartLimit(0, time.Minute))
			Expect(manager.Start(ctx)).To(Succeed())

			Expect(manager.WaitReady(ctx, "fake", 5*time.Second)).NotTo(Succeed())
			Expect(manager.Status("fake")).To(Equal(lsp.StatusFailed))

			Expect(manager.Restart(ctx, "fake")).To(Succeed())
			Expect(manager.Status("fake")).To(Equal(lsp.StatusReady))

			_, err := manager.Definition(ctx, "fake", file, 1, 1)
			Expect(err).NotTo(HaveOccurred(),
				"a server that took a restart must answer, not merely report ready")
		})
	})

	When("an agent restarts a server again and again", func() {
		It("serves again after each one, however many the agent asks for", func() {
			entry := config.LanguageServer{
				Name:      "fake",
				Command:   fakelspPath,
				Readiness: config.ReadinessHandshake,
				Filetypes: map[string]string{".fake": "fake"},
			}
			manager := lsp.NewManager(GinkgoT().TempDir(), []config.LanguageServer{entry})
			Expect(manager.Start(ctx)).To(Succeed())
			Expect(manager.WaitReady(ctx, "fake", 5*time.Second)).To(Succeed())

			for restart := 1; restart <= 3; restart++ {
				Expect(manager.Restart(ctx, "fake")).To(Succeed(), "restart %d", restart)
				Expect(manager.Status("fake")).To(Equal(lsp.StatusReady))
			}
		})
	})

	When("a server crashes on its own after an agent restarted it", func() {
		It("gives that crash the whole budget, because the restart spent none of it", func() {
			instanceLog := filepath.Join(GinkgoT().TempDir(), "instances.log")

			// A budget of one exit is the smallest that can tell the two
			// readings apart: one crash must be survivable, and a restart
			// that had spent the budget would leave no room for it.
			entry := config.LanguageServer{
				Name:      "fake",
				Command:   fakelspPath,
				Args:      []string{"-instance-log=" + instanceLog},
				Readiness: config.ReadinessHandshake,
				Filetypes: map[string]string{".fake": "fake"},
			}
			manager := lsp.NewManager(GinkgoT().TempDir(), []config.LanguageServer{entry},
				lsp.WithRestartLimit(1, time.Minute))
			Expect(manager.Start(ctx)).To(Succeed())
			Expect(manager.WaitReady(ctx, "fake", 5*time.Second)).To(Succeed())

			Expect(manager.Restart(ctx, "fake")).To(Succeed())
			started := readInstanceLog(instanceLog)
			Expect(started).To(HaveLen(2))

			// Killing the replacement is an exit nobody asked for, which is
			// the only kind the budget counts.
			killProcess(started[1])

			Eventually(func() int { return len(readInstanceLog(instanceLog)) },
				5*time.Second).Should(Equal(3),
				"the budget should still have had room for one crash")
			Expect(manager.WaitReady(ctx, "fake", 5*time.Second)).To(Succeed())
			Expect(manager.Status("fake")).To(Equal(lsp.StatusReady))
		})
	})

	When("a shutdown lands while a restart is stopping the old process", func() {
		// This spec runs the two against each other and reads the outcome.
		// It cannot place a shutdown inside the few instructions in which a
		// spawn is neither refused nor visible: the specs in attempt_test.go
		// pin that window, one order each. What this one does add is a bound
		// on both calls, since a shutdown now waits for the goroutine that
		// owns the process, and a restart now waits for a readiness gate.
		It("ends both calls with no language-server process left running", func() {
			instanceLog := filepath.Join(GinkgoT().TempDir(), "instances.log")

			// The fake never acts on exit, so the restart's stop lasts the
			// whole grace, and the shutdown below lands inside it.
			entry := config.LanguageServer{
				Name:      "fake",
				Command:   fakelspPath,
				Args:      []string{"-ignore-exit", "-instance-log=" + instanceLog},
				Readiness: config.ReadinessHandshake,
				Filetypes: map[string]string{".fake": "fake"},
			}
			manager := lsp.NewManager(GinkgoT().TempDir(), []config.LanguageServer{entry},
				lsp.WithShutdownGrace(300*time.Millisecond))
			Expect(manager.Start(ctx)).To(Succeed())
			Expect(manager.WaitReady(ctx, "fake", 5*time.Second)).To(Succeed())

			restarted := make(chan struct{})
			go func() {
				defer GinkgoRecover()
				defer close(restarted)
				// Either answer is honest: the restart won and served a
				// replacement, or the shutdown won and said so. Neither may
				// leave a process running.
				_ = manager.Restart(ctx, "fake")
			}()

			stopped := make(chan struct{})
			go func() {
				defer GinkgoRecover()
				defer close(stopped)
				Expect(manager.Shutdown(ctx)).To(Succeed())
			}()

			Eventually(stopped, 10*time.Second).Should(BeClosed(),
				"a shutdown that waits for the supervisor must not wait for a restart forever")
			Eventually(restarted, 10*time.Second).Should(BeClosed(),
				"a restart must stop waiting for a readiness gate the shutdown has closed")

			for _, pid := range readInstanceLog(instanceLog) {
				Eventually(processAlive, 5*time.Second).WithArguments(pid).Should(BeFalse(),
					"process %d outlived the shutdown that reported every server stopped", pid)
			}
		})
	})

	When("the named server is not configured", func() {
		It("names the servers that are, so the next call can name one of them", func() {
			entry := config.LanguageServer{
				Name:      "fake",
				Command:   fakelspPath,
				Readiness: config.ReadinessHandshake,
				Filetypes: map[string]string{".fake": "fake"},
			}
			manager := lsp.NewManager(GinkgoT().TempDir(), []config.LanguageServer{entry})
			Expect(manager.Start(ctx)).To(Succeed())

			err := manager.Restart(ctx, "gopls")
			Expect(err).To(MatchError(ContainSubstring(`"gopls"`)))
			Expect(err).To(MatchError(ContainSubstring("fake")))
		})
	})

	When("a language server reports progress for the work its restart repeats", func() {
		It("returns only after the replacement passes its own readiness gate", func() {
			const indexing = 300 * time.Millisecond

			entry := config.LanguageServer{
				Name:      "fake",
				Command:   fakelspPath,
				Args:      []string{"-progress", "-progress-delay=" + indexing.String()},
				Filetypes: map[string]string{".fake": "fake"},
			}
			manager := lsp.NewManager(GinkgoT().TempDir(), []config.LanguageServer{entry},
				lsp.WithProgressDebounce(20*time.Millisecond))
			Expect(manager.Start(ctx)).To(Succeed())
			Expect(manager.WaitReady(ctx, "fake", 5*time.Second)).To(Succeed())

			start := time.Now()
			Expect(manager.Restart(ctx, "fake")).To(Succeed())

			// The replacement indexes for as long as the first process did,
			// so a restart that returned sooner than that reported a server
			// ready while it was still working.
			Expect(time.Since(start)).To(BeNumerically(">=", indexing))
		})
	})
})
