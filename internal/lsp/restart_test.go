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
