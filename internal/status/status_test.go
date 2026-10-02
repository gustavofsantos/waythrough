package status

import (
	"os"
	"testing"

	"github.com/gustavofsantos/waythrough/internal/lsp"
)

func servers(health ...lsp.Health) func() []lsp.InstanceStats {
	return func() []lsp.InstanceStats {
		stats := make([]lsp.InstanceStats, 0, len(health))
		for _, each := range health {
			stats = append(stats, lsp.InstanceStats{Name: "server", Health: each})
		}
		return stats
	}
}

func TestStandaloneReportHasNoSessions(t *testing.T) {
	report := Source{Root: "/work", LanguageServers: servers()}.Report()
	if report.Mode != ModeStandalone || report.Sessions != nil {
		t.Fatalf("mode %q, sessions %v; want standalone and none", report.Mode, report.Sessions)
	}
	if report.PID != os.Getpid() || report.Format != Format {
		t.Fatalf("pid %d, format %d", report.PID, report.Format)
	}
	if report.LanguageServers == nil {
		t.Fatal("no servers must encode as [], so a reader never meets null")
	}
	if report.Runtime.Goroutines == 0 {
		t.Fatal("runtime figures were not read")
	}
}

func TestSharedReportCarriesItsSessions(t *testing.T) {
	report := Source{
		Key:             "key",
		LanguageServers: servers(),
		Sessions:        func() SessionStats { return SessionStats{Active: 2, Max: 64} },
	}.Report()
	if report.Mode != ModeShared || report.Sessions == nil || report.Sessions.Active != 2 {
		t.Fatalf("mode %q, sessions %+v", report.Mode, report.Sessions)
	}
}

func TestHealthIsTheWorstOfItsParts(t *testing.T) {
	full := func() SessionStats { return SessionStats{Active: 64, Max: 64} }
	cases := []struct {
		name     string
		servers  []lsp.Health
		sessions func() SessionStats
		want     lsp.Health
	}{
		{"no servers", nil, nil, lsp.HealthHealthy},
		{"all healthy",
			[]lsp.Health{lsp.HealthHealthy, lsp.HealthHealthy}, nil, lsp.HealthHealthy},
		{"one degraded",
			[]lsp.Health{lsp.HealthHealthy, lsp.HealthDegraded}, nil, lsp.HealthDegraded},
		{"failing beats degraded",
			[]lsp.Health{lsp.HealthDegraded, lsp.HealthFailing}, nil, lsp.HealthFailing},
		{"a full daemon is degraded", []lsp.Health{lsp.HealthHealthy}, full, lsp.HealthDegraded},
	}
	for _, test := range cases {
		source := Source{LanguageServers: servers(test.servers...), Sessions: test.sessions}
		report := source.Report()
		if report.Health != test.want {
			t.Errorf("%s: health %s, want %s", test.name, report.Health, test.want)
		}
	}
}
