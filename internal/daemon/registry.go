package daemon

import (
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/gustavofsantos/waythrough/internal/status"
)

type admission int

const (
	admissionAccepted admission = iota
	admissionBusy
	admissionDraining
)

// registry counts the live sessions and owns the one timer that drains the
// daemon. Every field changes only under mu, and the drain decision reads
// the count and the timer generation in the same hold that sets draining,
// so no session can be admitted by a daemon that has decided to drain.
type registry struct {
	listener *net.UnixListener
	logger   *slog.Logger
	linger   time.Duration
	drained  chan struct{}

	mu       sync.Mutex
	sessions map[*net.UnixConn]struct{}
	draining bool
	// The counters below only rise. They are for the status report, and no
	// decision reads them.
	admittedTotal uint64
	refusedBusy   uint64
	refusedPeer   uint64
	// drainAt is when the armed timer fires, and zero when none is armed.
	drainAt time.Time
	// timerGeneration rises on every arm and stop. A timer whose callback
	// carries an older generation was superseded and does nothing.
	timerGeneration uint64
	timer           *time.Timer
}

func newRegistry(listener *net.UnixListener, options Options) *registry {
	return &registry{
		listener: listener,
		logger:   options.Logger,
		linger:   options.Linger,
		drained:  make(chan struct{}),
		sessions: make(map[*net.UnixConn]struct{}, MaxSessions),
	}
}

func (r *registry) admit(conn *net.UnixConn) admission {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.draining {
		return admissionDraining
	}
	if len(r.sessions) >= MaxSessions {
		r.refusedBusy++
		return admissionBusy
	}
	r.sessions[conn] = struct{}{}
	r.admittedTotal++
	r.stopTimerLocked()
	r.logger.Debug("daemon session started", slog.Int("sessions", len(r.sessions)))
	return admissionAccepted
}

func (r *registry) release(conn *net.UnixConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, conn)
	r.logger.Debug("daemon session ended", slog.Int("sessions", len(r.sessions)))
	if len(r.sessions) == 0 && !r.draining {
		r.armTimerLocked(r.linger)
	}
}

func (r *registry) armTimer(duration time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.armTimerLocked(duration)
}

func (r *registry) armTimerLocked(duration time.Duration) {
	r.stopTimerLocked()
	generation := r.timerGeneration
	r.drainAt = time.Now().Add(duration)
	r.timer = time.AfterFunc(duration, func() { r.expire(generation) })
}

func (r *registry) stopTimerLocked() {
	r.timerGeneration++
	r.drainAt = time.Time{}
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
}

// expire drains the daemon if the timer that fired is still the current one
// and no session has arrived since it was armed.
func (r *registry) expire(generation uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if generation != r.timerGeneration || len(r.sessions) > 0 || r.draining {
		return
	}
	r.beginDrainLocked()
}

// forceDrain drains whatever sessions remain. Closing a session's
// connection ends its MCP session, whose handler then releases it.
func (r *registry) forceDrain() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.draining {
		r.beginDrainLocked()
	}
	for conn := range r.sessions {
		_ = conn.Close()
	}
}

// beginDrainLocked closes the listener in the same hold that sets draining.
// Closing removes the socket file, so from here a new client finds no
// daemon and starts the next one, which waits for this one's lock.
func (r *registry) beginDrainLocked() {
	r.draining = true
	r.stopTimerLocked()
	_ = r.listener.Close()
	close(r.drained)
}

func (r *registry) isDraining() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.draining
}

// refusePeer counts a connection from another user, which never reaches
// admit.
func (r *registry) refusePeer() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refusedPeer++
}

// stats reads the session figures in one hold of the lock, so they agree
// with one another.
func (r *registry) stats() status.SessionStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	stats := status.SessionStats{
		Active:        len(r.sessions),
		Max:           MaxSessions,
		AdmittedTotal: r.admittedTotal,
		RefusedBusy:   r.refusedBusy,
		RefusedPeer:   r.refusedPeer,
		DrainAt:       r.drainAt,
	}
	switch {
	case r.draining:
		stats.State = status.StateDraining
	case len(r.sessions) > 0:
		stats.State = status.StateServing
	case r.admittedTotal == 0:
		stats.State = status.StateStarting
	default:
		stats.State = status.StateLingering
	}
	return stats
}
