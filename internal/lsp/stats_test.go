package lsp_test

import (
	"context"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gustavofsantos/waythrough/internal/config"
	"github.com/gustavofsantos/waythrough/internal/lsp"
)

// onlyStats returns the one entry a single-server, single-root manager
// reports.
func onlyStats(manager *lsp.Manager) lsp.InstanceStats {
	stats := manager.Stats()
	Expect(stats).To(HaveLen(1))
	return stats[0]
}

var _ = Describe("Manager.Stats", func() {
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

	It("reports a configured server no request has started as idle, with no root", func() {
		manager := lsp.NewManager(GinkgoT().TempDir(),
			[]config.LanguageServer{fakeEntry()}, lsp.WithDemandStart())
		Expect(manager.Start(ctx)).To(Succeed())

		stats := onlyStats(manager)
		Expect(stats.Name).To(Equal("fake"))
		Expect(stats.Root).To(BeEmpty())
		Expect(stats.Status).To(Equal("idle"))
		Expect(stats.Health).To(Equal(lsp.HealthHealthy))
		Expect(stats.PID).To(BeZero())
	})

	It("reports the process, its startup, and each request it served", func() {
		manager, file := fakeManager(ctx, "hello world", "-definition-line=0")
		_, err := manager.Definition(ctx, "fake", file, 1, 1)
		Expect(err).NotTo(HaveOccurred())

		stats := onlyStats(manager)
		Expect(stats.Root).To(Equal(filepath.Dir(file)))
		Expect(stats.Status).To(Equal("ready"))
		Expect(stats.Health).To(Equal(lsp.HealthHealthy))
		Expect(stats.Attempt).To(Equal(1))
		Expect(stats.PID).To(BeNumerically(">", 0))
		Expect(processAlive(stats.PID)).To(BeTrue())
		Expect(stats.AttemptStartedAt).To(BeTemporally("~", time.Now(), 10*time.Second))
		Expect(stats.OpenDocuments).To(Equal(1))
		Expect(stats.Requests.Total).To(Equal(uint64(1)))
		Expect(stats.Requests.Failed).To(BeZero())
		Expect(stats.Requests.RecentCount).To(Equal(1))
		Expect(stats.Requests.LastError).To(BeEmpty())
	})

	It("counts a failed request and keeps its message", func() {
		// The fake does not advertise pull diagnostics, so the request fails
		// after it has reached the instance.
		manager, file := fakeManager(ctx, "greet()")
		_, err := manager.Diagnostics(ctx, "fake", file)
		Expect(err).To(HaveOccurred())

		requests := onlyStats(manager).Requests
		Expect(requests.Total).To(Equal(uint64(1)))
		Expect(requests.Failed).To(Equal(uint64(1)))
		Expect(requests.RecentFailed).To(Equal(1))
		Expect(requests.LastError).To(Equal(err.Error()))
		Expect(requests.LastErrorAt).To(BeTemporally("~", time.Now(), 10*time.Second))
	})

	It("does not count a request that failed before it reached any server", func() {
		manager, _ := fakeManager(ctx, "hello world")
		// The root is no git checkout, so a relative path names no file and
		// fails before any instance is chosen.
		_, err := manager.Definition(ctx, "fake", "main.fake", 1, 1)
		Expect(err).To(MatchError(ContainSubstring("pass an absolute path")))

		Expect(onlyStats(manager).Requests.Total).To(BeZero())
	})

	It("reports a crash within the restart window as degraded", func() {
		manager, file := fakeManager(ctx, "hello world", "-definition-line=0")
		_, err := manager.Definition(ctx, "fake", file, 1, 1)
		Expect(err).NotTo(HaveOccurred())
		crashed := onlyStats(manager).PID

		killProcess(crashed)

		Eventually(func(g Gomega) {
			stats := onlyStats(manager)
			g.Expect(stats.Attempt).To(Equal(2))
			g.Expect(stats.Status).To(Equal("ready"))
			g.Expect(stats.PID).NotTo(Equal(crashed))
			g.Expect(stats.CrashesInWindow).To(Equal(1))
			g.Expect(stats.CrashesTotal).To(Equal(uint64(1)))
			g.Expect(stats.Health).To(Equal(lsp.HealthDegraded))
		}, 10*time.Second).Should(Succeed())
	})

	It("counts a requested restart apart from crashes", func() {
		manager, _ := fakeManager(ctx, "hello world")
		Expect(manager.Restart(ctx, "fake")).To(Succeed())

		stats := onlyStats(manager)
		Expect(stats.RestartsRequested).To(Equal(uint64(1)))
		Expect(stats.CrashesTotal).To(BeZero())
		Expect(stats.Health).To(Equal(lsp.HealthHealthy))
	})

	It("reports a server that spent its crash budget as failing", func() {
		manager := lsp.NewManager(GinkgoT().TempDir(),
			[]config.LanguageServer{fakeEntry("-crash")},
			lsp.WithRestartLimit(1, time.Minute))
		Expect(manager.Start(ctx)).To(Succeed())

		Eventually(func(g Gomega) {
			stats := onlyStats(manager)
			g.Expect(stats.Status).To(Equal("failed"))
			g.Expect(stats.Health).To(Equal(lsp.HealthFailing))
			g.Expect(stats.CrashesInWindow).To(Equal(2))
			g.Expect(stats.CrashLimit).To(Equal(1))
			g.Expect(stats.PID).To(BeZero(), "a failed server has no process")
		}, 10*time.Second).Should(Succeed())
	})
})
