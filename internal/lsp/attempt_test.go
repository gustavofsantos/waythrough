package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
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

// survivalMarker is how long a spawned process waits before it leaves its
// trace. A process that is stopped never reaches the trace, so its absence
// is the reading that says the spawn left nothing running.
const survivalMarker = 300 * time.Millisecond

// tracingServer is a command that leaves a file behind, but only if it is
// still running when survivalMarker elapses. A spec cannot read the pid of
// a process that may be stopped before it even runs, so it asks the process
// to report its own survival instead.
func tracingServer(markerPath string) config.LanguageServer {
	return config.LanguageServer{
		Name:    "tracing",
		Command: "sh",
		Args: []string{
			"-c",
			"sleep " + strconv.FormatFloat(survivalMarker.Seconds(), 'f', -1, 64) +
				"; touch " + markerPath,
		},
	}
}

func markerExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// A shutdown reads the process to stop in the same lock hold that raises
// its flag, and an attempt raises its own claim to spawn in one lock hold
// too. Whichever hold comes first, the pair below covers both orders: a
// shutdown that arrives first stops any later attempt from starting, and
// one that arrives second meets an attempt that ends its own process.
// Neither order can leave a subprocess running after the shutdown returns.
var _ = Describe("an attempt that races the shutdown of its server", func() {
	It("starts no further attempt once the shutdown has begun", func() {
		proc := newAttemptTracker(config.ReadinessHandshake)

		_, spawning := proc.beginAttempt()
		Expect(spawning).To(BeTrue())

		proc.shutdown(context.Background(), time.Second)

		_, spawning = proc.beginAttempt()
		Expect(spawning).To(BeFalse(),
			"a shutdown has already looked for the last process it must stop")
	})

	It("ends the process it spawned when the shutdown looked before it published", func() {
		marker := filepath.Join(GinkgoT().TempDir(), "survived")
		proc := newAttemptTracker(config.ReadinessHandshake)
		proc.entry = tracingServer(marker)

		generation, spawning := proc.beginAttempt()
		Expect(spawning).To(BeTrue())

		// The shutdown runs while this attempt is between its claim to spawn
		// and the moment it publishes a process, so it finds nothing to stop.
		proc.shutdown(context.Background(), time.Second)

		err := proc.startProcess(context.Background(), generation)
		Expect(err).To(MatchError(errShutdownBegan))
		Expect(proc.currentServer()).To(BeNil(),
			"a connection published here would name a process nobody owns")

		Consistently(markerExists).WithArguments(marker).
			WithTimeout(3*survivalMarker).Should(BeFalse(),
			"the spawn no one could see must have ended the process it started")
	})
})

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
