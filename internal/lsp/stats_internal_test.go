package lsp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNearestRank(t *testing.T) {
	samples := make([]time.Duration, 64)
	for index := range samples {
		samples[index] = time.Duration(index+1) * time.Millisecond
	}

	cases := []struct {
		name    string
		sorted  []time.Duration
		percent int
		want    time.Duration
	}{
		{"empty", nil, 95, 0},
		{"one sample is every percentile", samples[:1], 50, time.Millisecond},
		{"p50 of 64 is the 32nd", samples, 50, 32 * time.Millisecond},
		{"p95 of 64 rounds its rank up to the 61st", samples, 95, 61 * time.Millisecond},
		{"p100 is the max", samples, 100, 64 * time.Millisecond},
	}
	for _, test := range cases {
		if got := nearestRank(test.sorted, test.percent); got != test.want {
			t.Errorf("%s: nearestRank = %s, want %s", test.name, got, test.want)
		}
	}
}

// The ring must forget the oldest requests once full, so that a server
// which failed long ago and has since recovered reads healthy again.
func TestRecentRequestsForgetTheOldest(t *testing.T) {
	var counters instanceCounters
	for range recentRequestsMax {
		counters.recordRequest(time.Second, errors.New("broken"))
	}
	for range recentRequestsMax {
		counters.recordRequest(time.Millisecond, nil)
	}

	var stats InstanceStats
	counters.fill(&stats, time.Now(), time.Minute)
	requests := stats.Requests
	if requests.Total != 2*recentRequestsMax || requests.Failed != recentRequestsMax {
		t.Fatalf("lifetime totals = %d total, %d failed", requests.Total, requests.Failed)
	}
	if requests.RecentCount != recentRequestsMax || requests.RecentFailed != 0 {
		t.Fatalf("recent = %d of %d failed, want 0 of %d",
			requests.RecentFailed, requests.RecentCount, recentRequestsMax)
	}
	if requests.RecentDurationMillisecondsMax != 1 {
		t.Fatalf("recent max = %dms, want 1ms", requests.RecentDurationMillisecondsMax)
	}
}

func TestCanceledRequestsSayNothingAboutTheServer(t *testing.T) {
	var counters instanceCounters
	counters.recordRequest(time.Second, fmt.Errorf("definition: %w", context.Canceled))

	var stats InstanceStats
	counters.fill(&stats, time.Now(), time.Minute)
	requests := stats.Requests
	if requests.Total != 1 || requests.Canceled != 1 {
		t.Fatalf("total %d, canceled %d; want 1 and 1", requests.Total, requests.Canceled)
	}
	if requests.Failed != 0 || requests.RecentCount != 0 || requests.LastError != "" {
		t.Fatalf("a canceled request reached the failure figures: %+v", requests)
	}
}

func TestLastErrorIsBounded(t *testing.T) {
	var counters instanceCounters
	counters.recordRequest(time.Second, errors.New(strings.Repeat("é", lastErrorBytesMax)))

	var stats InstanceStats
	counters.fill(&stats, time.Now(), time.Minute)
	message := stats.Requests.LastError
	if !strings.HasSuffix(message, "…[truncated]") {
		t.Fatalf("a long error was not marked as cut: %q", message)
	}
	if len(strings.TrimSuffix(message, "…[truncated]")) > lastErrorBytesMax {
		t.Fatalf("kept %d bytes, limit is %d", len(message), lastErrorBytesMax)
	}
}

// A crash leaves the budget once the window slides past it, with no new
// event to say so, so the count must be taken at read time.
func TestCrashesLeaveTheWindowAsItSlides(t *testing.T) {
	var counters instanceCounters
	crashed := time.Now()
	counters.recordCrashes([]time.Time{crashed})

	var inside, after InstanceStats
	counters.fill(&inside, crashed.Add(30*time.Second), time.Minute)
	counters.fill(&after, crashed.Add(2*time.Minute), time.Minute)
	if inside.CrashesInWindow != 1 || after.CrashesInWindow != 0 {
		t.Fatalf("in window: %d inside, %d after; want 1 and 0",
			inside.CrashesInWindow, after.CrashesInWindow)
	}
	if after.CrashesTotal != 1 {
		t.Fatalf("crashes total = %d, want 1", after.CrashesTotal)
	}
}

func TestHealthOf(t *testing.T) {
	ready := InstanceStats{Status: "ready"}
	withRequests := func(count, failed int) InstanceStats {
		stats := ready
		stats.Requests.RecentCount = count
		stats.Requests.RecentFailed = failed
		return stats
	}
	crashed := ready
	crashed.CrashesInWindow = 1
	failed := InstanceStats{Status: "failed"}

	cases := []struct {
		name        string
		stats       InstanceStats
		startingFor time.Duration
		want        Health
	}{
		{"ready with no requests", ready, 0, HealthHealthy},
		{"spent its crash budget", failed, 0, HealthFailing},
		{"crashed within the window", crashed, 0, HealthDegraded},
		{"starting past the readiness timeout", ready, time.Minute, HealthDegraded},
		{"too few requests to judge", withRequests(3, 3), 0, HealthHealthy},
		{"exactly one in four failed", withRequests(8, 2), 0, HealthDegraded},
		{"fewer than one in four failed", withRequests(9, 2), 0, HealthHealthy},
	}
	for _, test := range cases {
		if got := healthOf(test.stats, test.startingFor, 30*time.Second); got != test.want {
			t.Errorf("%s: healthOf = %s, want %s", test.name, got, test.want)
		}
	}
}
