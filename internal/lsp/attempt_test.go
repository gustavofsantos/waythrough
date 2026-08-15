package lsp

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gustavofsantos/waythrough/internal/config"
)

// newAttemptTracker builds the readiness state NewManager hands a server
// that has not spawned yet. No subprocess takes part in these specs: which
// attempt a readiness signal belongs to is decided in this state alone, and
// the hazard it guards against — a graceful stop that leaves the retired
// process able to speak while its replacement starts — cannot be staged
// through a real subprocess, because a message written before a process
// exits is always read before that exit is seen.
func newAttemptTracker(readiness config.Readiness) *serverProcess {
	return &serverProcess{
		entry:      config.LanguageServer{Name: "fake", Readiness: readiness},
		readyCh:    make(chan struct{}),
		retiredCh:  make(chan struct{}),
		exitedCh:   make(chan struct{}),
		restartCh:  make(chan struct{}, 1),
		stoppingCh: make(chan struct{}),
	}
}

// retiredAndCurrent runs two attempts and names them, leaving the server on
// the second one — the state a restart produces, and the only state in
// which a signal can name an attempt that no longer speaks for the server.
func retiredAndCurrent(proc *serverProcess) (int, int) {
	retired, spawning := proc.beginAttempt()
	Expect(spawning).To(BeTrue())

	current, spawning := proc.beginAttempt()
	Expect(spawning).To(BeTrue())

	Expect(proc.snapshot().status).To(Equal(StatusStarting))
	return retired, current
}

var _ = Describe("a readiness signal from an attempt that was replaced", func() {
	It("cannot report the replacement ready before the replacement says so", func() {
		proc := newAttemptTracker(config.ReadinessHandshake)
		retired, current := retiredAndCurrent(proc)

		proc.markReady(retired)
		Expect(proc.snapshot().status).To(Equal(StatusStarting),
			"the replacement has not passed its own readiness gate yet")

		proc.markReady(current)
		Expect(proc.snapshot().status).To(Equal(StatusReady))
	})

	It("cannot report the replacement failed before the replacement has tried", func() {
		proc := newAttemptTracker(config.ReadinessHandshake)
		retired, current := retiredAndCurrent(proc)

		proc.markFailed(retired)
		Expect(proc.snapshot().status).To(Equal(StatusStarting))

		proc.markFailed(current)
		Expect(proc.snapshot().status).To(Equal(StatusFailed))
	})

	It("cannot close the last token of work the replacement never started", func() {
		proc := newAttemptTracker(config.ReadinessProgress)
		retired, current := retiredAndCurrent(proc)

		proc.tokenCreated(current, "indexing")
		proc.tokenClosed(retired, "indexing")
		Expect(proc.snapshot().status).To(Equal(StatusStarting),
			"the replacement is still indexing under its own token")

		proc.tokenClosed(current, "indexing")
		Expect(proc.snapshot().status).To(Equal(StatusReady))
	})

	It("cannot leave the replacement waiting on work it never took up", func() {
		proc := newAttemptTracker(config.ReadinessProgress)
		retired, current := retiredAndCurrent(proc)

		proc.tokenCreated(retired, "indexing")

		// A server that opens no token of its own becomes ready once the
		// debounce window closes. A token counted from the retired attempt
		// would hold this attempt starting until an end that never comes.
		proc.negotiateReadiness(context.Background(), time.Millisecond, current)
		Expect(proc.snapshot().status).To(Equal(StatusReady))
	})
})
