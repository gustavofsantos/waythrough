// Package status defines Waythrough's account of itself: which language
// servers run, how healthy each one is, and, for a shared daemon, the
// sessions it serves. One Report shape answers both the agent, through the
// get_status MCP tool, and a person, through `waythrough status`, so the two
// can never disagree about what a field means.
package status

import (
	"os"
	"runtime/metrics"
	"time"

	"github.com/gustavofsantos/waythrough/internal/lsp"
)

// Format is the version of Report's JSON shape. A reader refuses a report
// in any other format rather than misread it.
const Format = 1

// Mode says which kind of process wrote a report.
type Mode string

const (
	// ModeShared is a daemon that `serve --shared` sessions attach to.
	ModeShared Mode = "shared"
	// ModeStandalone is a plain `serve`, which owns its servers alone.
	ModeStandalone Mode = "standalone"
)

// Report is one Waythrough process's account of itself.
type Report struct {
	Format int  `json:"format"`
	Mode   Mode `json:"mode"`
	// Key is the shared daemon's workspace key, and empty for standalone.
	Key       string     `json:"key,omitempty"`
	PID       int        `json:"pid"`
	Root      string     `json:"root"`
	Version   string     `json:"version"`
	StartedAt time.Time  `json:"started_at"`
	Health    lsp.Health `json:"health"`
	// Sessions is nil for a standalone serve, which serves exactly one.
	Sessions *SessionStats `json:"sessions,omitempty"`
	Runtime  RuntimeStats  `json:"runtime"`
	// LanguageServers lists every configured server: see lsp.Manager.Stats.
	LanguageServers []lsp.InstanceStats `json:"language_servers"`
}

// DaemonState is where a shared daemon is in its own lifecycle.
type DaemonState string

const (
	// StateStarting means no session has arrived yet; the startup grace
	// drains the daemon if none does.
	StateStarting DaemonState = "starting"
	// StateServing means at least one session is connected.
	StateServing DaemonState = "serving"
	// StateLingering means the last session left, and the linger drains the
	// daemon unless another arrives.
	StateLingering DaemonState = "lingering"
	// StateDraining means the daemon admits no session and is stopping.
	StateDraining DaemonState = "draining"
)

// SessionStats is the session side of a shared daemon's report.
type SessionStats struct {
	State  DaemonState `json:"state"`
	Active int         `json:"active"`
	Max    int         `json:"max"`
	// AdmittedTotal counts every session since the daemon started.
	AdmittedTotal uint64 `json:"admitted_total"`
	// RefusedBusy counts the sessions refused at the session limit.
	RefusedBusy uint64 `json:"refused_busy"`
	// RefusedPeer counts the connections refused as another user's, on
	// the session socket and the status socket alike.
	RefusedPeer uint64 `json:"refused_peer"`
	// DrainAt is when the daemon drains if no session arrives first, and is
	// zero while a session is connected or once draining has begun.
	DrainAt time.Time `json:"drain_at,omitzero"`
}

// RuntimeStats is the Waythrough process's own footprint. A goroutine
// count that climbs while the sessions do not is the first sign of a leak.
type RuntimeStats struct {
	Goroutines uint64 `json:"goroutines"`
	// HeapBytes is the memory live and not yet collected heap objects
	// occupy.
	HeapBytes uint64 `json:"heap_bytes"`
	// TotalBytes is all the memory the Go runtime has mapped.
	TotalBytes uint64 `json:"total_bytes"`
}

// Source is what a Report is built from. Each field is fixed for the life
// of the process, so a Source is built once and its Report method called
// for every reader.
type Source struct {
	Root      string
	Version   string
	StartedAt time.Time
	// LanguageServers reports the servers. It must not be nil.
	LanguageServers func() []lsp.InstanceStats
	// Key and Sessions describe a shared daemon. A nil Sessions marks a
	// standalone serve.
	Key      string
	Sessions func() SessionStats
}

// Report reads every figure now. Its cost is bounded by the configured
// servers: see lsp.Manager.Stats.
func (s Source) Report() Report {
	if s.LanguageServers == nil {
		panic("status: Source needs LanguageServers, got nil")
	}
	report := Report{
		Format:          Format,
		Mode:            ModeStandalone,
		Key:             s.Key,
		PID:             os.Getpid(),
		Root:            s.Root,
		Version:         s.Version,
		StartedAt:       s.StartedAt,
		Runtime:         readRuntimeStats(),
		LanguageServers: s.LanguageServers(),
	}
	if report.LanguageServers == nil {
		report.LanguageServers = []lsp.InstanceStats{}
	}
	if s.Sessions != nil {
		sessions := s.Sessions()
		report.Mode = ModeShared
		report.Sessions = &sessions
	}
	report.Health = overallHealth(report.Sessions, report.LanguageServers)
	return report
}

// overallHealth is the worst of the servers' health. A daemon refusing
// sessions at its limit is degraded too, whatever its servers say.
func overallHealth(sessions *SessionStats, servers []lsp.InstanceStats) lsp.Health {
	degraded := sessions != nil && sessions.Active >= sessions.Max
	for _, server := range servers {
		switch server.Health {
		case lsp.HealthFailing:
			return lsp.HealthFailing
		case lsp.HealthDegraded:
			degraded = true
		case lsp.HealthHealthy:
		}
	}
	if degraded {
		return lsp.HealthDegraded
	}
	return lsp.HealthHealthy
}

// readRuntimeStats reads runtime/metrics, which, unlike
// runtime.ReadMemStats, does not stop the world.
func readRuntimeStats() RuntimeStats {
	samples := []metrics.Sample{
		{Name: "/sched/goroutines:goroutines"},
		{Name: "/memory/classes/heap/objects:bytes"},
		{Name: "/memory/classes/total:bytes"},
	}
	metrics.Read(samples)
	return RuntimeStats{
		Goroutines: sampleUint64(samples[0]),
		HeapBytes:  sampleUint64(samples[1]),
		TotalBytes: sampleUint64(samples[2]),
	}
}

// sampleUint64 reads a metric this Go release may not offer, as zero.
func sampleUint64(sample metrics.Sample) uint64 {
	if sample.Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return sample.Value.Uint64()
}
