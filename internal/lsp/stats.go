package lsp

import (
	"context"
	"errors"
	"slices"
	"sort"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// recentRequestsMax is how many of an instance's latest requests its
	// recent figures describe. Lifetime totals hide a server that was fine
	// for an hour and has failed every call since; a short window shows it.
	// It is a fixed ring, so recording a request never allocates.
	recentRequestsMax = 64
	// lastErrorBytesMax bounds the one error message an instance keeps. A
	// language server chooses the length of its own error replies.
	lastErrorBytesMax = 256
	// healthSampleMin is how many recent requests the failure share needs
	// before it may call an instance degraded, so that one failed call out
	// of one does not.
	healthSampleMin = 4
)

// Health is a one-word verdict on a language-server instance, for a person
// deciding whether to look closer.
type Health string

const (
	// HealthHealthy means nothing below applies.
	HealthHealthy Health = "healthy"
	// HealthDegraded means the instance still answers but is getting worse:
	// it crashed within the restart window, it has been starting for longer
	// than a request waits for it, or at least one in four of its recent
	// requests failed.
	HealthDegraded Health = "degraded"
	// HealthFailing means the instance spent its crash budget and answers
	// nothing until something restarts it.
	HealthFailing Health = "failing"
)

// String names a status the way a person reading a status report expects.
func (s Status) String() string {
	switch s {
	case StatusIdle:
		return "idle"
	case StatusStarting:
		return "starting"
	case StatusReady:
		return "ready"
	case StatusFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// InstanceStats is a point-in-time account of one configured language
// server at one project root. Its JSON form is part of the daemon's status
// report, so a field's name and unit, once released, do not change.
type InstanceStats struct {
	Name string `json:"name"`
	// Root is empty for a configured server that no request has started.
	Root   string `json:"root,omitempty"`
	Status string `json:"status"`
	Health Health `json:"health"`
	// Attempt counts the processes this instance has spawned, restarts
	// included.
	Attempt int `json:"attempt"`
	// PID is zero when no process runs.
	PID int `json:"pid,omitempty"`
	// AttemptStartedAt is when the current process was spawned.
	AttemptStartedAt time.Time `json:"attempt_started_at,omitzero"`
	// StartupMilliseconds is how long the current process took to pass
	// its readiness gate, and zero until it has.
	StartupMilliseconds int64 `json:"startup_ms,omitempty"`
	// ResidentBytes is the process's resident memory, and zero where the
	// platform offers no cheap way to read it.
	ResidentBytes uint64 `json:"resident_bytes,omitempty"`
	// OpenDocuments counts the files synced to the current process.
	OpenDocuments int `json:"open_documents"`
	// CrashesInWindow counts the unrequested exits within the restart
	// window. Once it exceeds CrashLimit, the instance gives up.
	CrashesInWindow    int     `json:"crashes_in_window"`
	CrashLimit         int     `json:"crash_limit"`
	CrashWindowSeconds float64 `json:"crash_window_seconds"`
	CrashesTotal       uint64  `json:"crashes_total"`
	// RestartsRequested counts the exits a restart asked for.
	RestartsRequested uint64       `json:"restarts_requested"`
	Requests          RequestStats `json:"requests"`
}

// RequestStats describes the tool requests one instance has served. A
// request's duration runs from the moment it was routed to the instance,
// so it includes waiting for readiness and syncing the file, which is what
// the agent waited for.
type RequestStats struct {
	// Total counts every request, the canceled and refused ones included.
	Total  uint64 `json:"total"`
	Failed uint64 `json:"failed"`
	// Canceled counts the requests whose caller gave up first, and Refused
	// the ones Waythrough refused before asking the server, such as for a
	// file it cannot read or a capability the server lacks. Neither says
	// anything about the server's health, so neither is in Failed or in
	// the recent figures.
	Canceled uint64 `json:"canceled"`
	Refused  uint64 `json:"refused"`
	// The recent figures cover the latest RecentCount requests, at most
	// recentRequestsMax, canceled ones excluded.
	RecentCount                   int   `json:"recent_count"`
	RecentFailed                  int   `json:"recent_failed"`
	RecentDurationMillisecondsP50 int64 `json:"recent_duration_ms_p50"`
	RecentDurationMillisecondsP95 int64 `json:"recent_duration_ms_p95"`
	RecentDurationMillisecondsMax int64 `json:"recent_duration_ms_max"`
	// LastError is the latest failure's message, truncated.
	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at,omitzero"`
}

// instanceCounters is what one instance has done, across every attempt. It
// has its own lock so that recording a request never waits behind
// serverProcess.mu, which a lifecycle transition may hold.
type instanceCounters struct {
	mu sync.Mutex
	// crashTimes is runServer's restart window, copied. It holds at most
	// restartLimit+1 entries, because one more crash than that marks the
	// instance failed and a restart clears it.
	crashTimes        []time.Time
	crashesTotal      uint64
	restartsRequested uint64

	requestsTotal    uint64
	requestsFailed   uint64
	requestsCanceled uint64
	requestsRefused  uint64
	// recent is a ring: recentNext is the slot the next request takes, and
	// recentCount stops growing at recentRequestsMax.
	recent      [recentRequestsMax]requestSample
	recentNext  int
	recentCount int
	lastError   string
	lastErrorAt time.Time
}

type requestSample struct {
	duration time.Duration
	failed   bool
}

func (c *instanceCounters) recordCrashes(window []time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.crashesTotal++
	c.crashTimes = append(c.crashTimes[:0], window...)
}

// clearCrashes forgets the window after a restart revives a failed
// instance, as runServer does with its own copy.
func (c *instanceCounters) clearCrashes() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.crashTimes = c.crashTimes[:0]
}

func (c *instanceCounters) recordRequestedRestart() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.restartsRequested++
}

func (c *instanceCounters) recordRequest(duration time.Duration, err error) {
	canceled := errors.Is(err, context.Canceled)
	var refusal refusedError
	wasRefused := !canceled && errors.As(err, &refusal)
	failed := err != nil && !canceled && !wasRefused
	// Built before the lock, so the hold stays a few plain stores.
	var message string
	if failed {
		message = truncateText(err.Error(), lastErrorBytesMax)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.requestsTotal++
	if canceled {
		c.requestsCanceled++
		return
	}
	if wasRefused {
		c.requestsRefused++
		return
	}

	c.recent[c.recentNext] = requestSample{duration: duration, failed: failed}
	c.recentNext = (c.recentNext + 1) % recentRequestsMax
	c.recentCount = min(c.recentCount+1, recentRequestsMax)
	if !failed {
		return
	}
	c.requestsFailed++
	c.lastError = message
	c.lastErrorAt = time.Now()
}

// fill copies the counters into stats. now and window decide which crashes
// still count against the budget, since the window slides while no crash
// happens.
func (c *instanceCounters) fill(stats *InstanceStats, now time.Time, window time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cutoff := now.Add(-window)
	for _, crashed := range c.crashTimes {
		if crashed.After(cutoff) {
			stats.CrashesInWindow++
		}
	}
	stats.CrashesTotal = c.crashesTotal
	stats.RestartsRequested = c.restartsRequested

	requests := &stats.Requests
	requests.Total = c.requestsTotal
	requests.Failed = c.requestsFailed
	requests.Canceled = c.requestsCanceled
	requests.Refused = c.requestsRefused
	requests.LastError = c.lastError
	requests.LastErrorAt = c.lastErrorAt
	requests.RecentCount = c.recentCount

	// The ring's order does not matter here: every figure below is over
	// the set of samples, and the slots past recentCount were never filled.
	durations := make([]time.Duration, 0, recentRequestsMax)
	for _, sample := range c.recent[:c.recentCount] {
		durations = append(durations, sample.duration)
		if sample.failed {
			requests.RecentFailed++
		}
	}
	slices.Sort(durations)
	requests.RecentDurationMillisecondsP50 = nearestRank(durations, 50).Milliseconds()
	requests.RecentDurationMillisecondsP95 = nearestRank(durations, 95).Milliseconds()
	if len(durations) > 0 {
		requests.RecentDurationMillisecondsMax = durations[len(durations)-1].Milliseconds()
	}
}

// nearestRank returns the percentile of sorted durations by the
// nearest-rank method: the smallest sample with at least percent of the
// samples at or below it. Its rank rounds up, so p95 of 64 samples is the
// 61st, and an empty set has zero.
func nearestRank(sorted []time.Duration, percent int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := (percent*len(sorted) + 99) / 100
	return sorted[max(rank, 1)-1]
}

// healthOf judges an instance from its own figures. startingFor is how
// long the current attempt has been starting, and is zero unless the
// status is starting.
func healthOf(stats InstanceStats, startingFor, readinessTimeout time.Duration) Health {
	if stats.Status == StatusFailed.String() {
		return HealthFailing
	}
	if stats.CrashesInWindow > 0 {
		return HealthDegraded
	}
	if startingFor > readinessTimeout {
		return HealthDegraded
	}
	recent := stats.Requests
	if recent.RecentCount >= healthSampleMin && 4*recent.RecentFailed >= recent.RecentCount {
		return HealthDegraded
	}
	return HealthHealthy
}

// Stats reports every configured server: each instance it runs, ordered by
// server name and then by root, and one idle entry with no root for a
// server no request has started. The work is bounded by the configured
// servers times maxWorkspaceRootsPerServer, and it holds Manager.mu only
// to list the instances, never across a read of an instance's own state.
func (m *Manager) Stats() []InstanceStats {
	type listed struct {
		name string
		proc *serverProcess
	}
	m.mu.Lock()
	names := make([]string, 0, len(m.servers))
	for name := range m.servers {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]listed, 0, len(names))
	for _, name := range names {
		server := m.servers[name]
		if len(server.instances) == 0 {
			entries = append(entries, listed{name: name})
			continue
		}
		for _, root := range sortedRoots(server.instances) {
			entries = append(entries, listed{name: name, proc: server.instances[root]})
		}
	}
	m.mu.Unlock()

	now := time.Now()
	stats := make([]InstanceStats, 0, len(entries))
	for _, entry := range entries {
		if entry.proc == nil {
			stats = append(stats, InstanceStats{
				Name:               entry.name,
				Status:             StatusIdle.String(),
				Health:             HealthHealthy,
				CrashLimit:         m.restartLimit,
				CrashWindowSeconds: m.restartWindow.Seconds(),
			})
			continue
		}
		stats = append(stats, m.instanceStats(entry.proc, now))
	}
	return stats
}

func (m *Manager) instanceStats(proc *serverProcess, now time.Time) InstanceStats {
	stats := InstanceStats{
		Name:               proc.entry.Name,
		Root:               proc.root,
		CrashLimit:         m.restartLimit,
		CrashWindowSeconds: m.restartWindow.Seconds(),
	}

	proc.mu.Lock()
	status := proc.status
	stats.Attempt = proc.generation
	stats.AttemptStartedAt = proc.attemptStartedAt
	// cmd stays set after its process exits, until the next attempt
	// begins, and a failed server begins none. Only an attempt whose exit
	// has not been reaped still names a process.
	if proc.cmd != nil && proc.cmd.Process != nil && !isClosed(proc.exitedCh) {
		stats.PID = proc.cmd.Process.Pid
	}
	if !proc.readyAt.IsZero() {
		stats.StartupMilliseconds = proc.readyAt.Sub(proc.attemptStartedAt).Milliseconds()
	}
	stats.OpenDocuments = len(proc.openFiles)
	proc.mu.Unlock()

	stats.Status = status.String()
	proc.counters.fill(&stats, now, m.restartWindow)
	// Read outside every lock: it is a file read, and the process may exit
	// in the meantime, which only leaves the figure at zero.
	stats.ResidentBytes = residentBytes(stats.PID)

	var startingFor time.Duration
	if status == StatusStarting {
		startingFor = now.Sub(stats.AttemptStartedAt)
	}
	stats.Health = healthOf(stats, startingFor, m.readinessTimeout)
	return stats
}

func isClosed(channel <-chan struct{}) bool {
	select {
	case <-channel:
		return true
	default:
		return false
	}
}

// refusedError marks a request Waythrough refused before asking the
// server, for a reason of the caller's own: a file it cannot read, or a
// capability the server never offered. Such a refusal repeats on every
// call that makes it, so counting it as a failure would call a working
// server degraded. It keeps the message it wraps unchanged.
type refusedError struct{ err error }

func (e refusedError) Error() string { return e.err.Error() }
func (e refusedError) Unwrap() error { return e.err }

func refused(err error) error { return refusedError{err: err} }

// recordRequest counts one tool request routed to proc. A nil proc means
// the request failed before any instance was chosen, such as for a path
// outside the workspace, and that says nothing about any server.
func (p *serverProcess) recordRequest(started time.Time, err error) {
	if p == nil {
		return
	}
	p.counters.recordRequest(time.Since(started), err)
}

// truncateText caps text at limitBytes, cutting on a rune boundary so the
// text stays valid UTF-8, and says that it cut rather than leaving a reader
// to mistake the cut for the whole message.
func truncateText(text string, limitBytes int) string {
	if len(text) <= limitBytes {
		return text
	}
	cut := limitBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…[truncated]"
}
