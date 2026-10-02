package cli

import (
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		duration time.Duration
		want     string
	}{
		{-time.Second, "0ms"},
		{42 * time.Millisecond, "42ms"},
		{4200 * time.Millisecond, "4.2s"},
		{12*time.Minute + 3*time.Second, "12m03s"},
		{time.Hour + 2*time.Minute + 59*time.Second, "1h02m"},
		{3*24*time.Hour + 4*time.Hour, "3d04h"},
	}
	for _, test := range cases {
		if got := formatDuration(test.duration); got != test.want {
			t.Errorf("formatDuration(%s) = %q, want %q", test.duration, got, test.want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		size uint64
		want string
	}{
		{512, "512 B"},
		{1536, "1.5 KiB"},
		{812 << 20, "812.0 MiB"},
		{3 << 30, "3.0 GiB"},
	}
	for _, test := range cases {
		if got := formatBytes(test.size); got != test.want {
			t.Errorf("formatBytes(%d) = %q, want %q", test.size, got, test.want)
		}
	}
}

func TestPrintableKeepsTerminalControlOut(t *testing.T) {
	got := printable("bad\x1b[2Jthing\n  on\ttwo lines")
	if got != "bad?[2Jthing on two lines" {
		t.Fatalf("printable = %q", got)
	}
}

func TestDisplayRoot(t *testing.T) {
	cases := []struct {
		serverRoot string
		want       string
	}{
		{"", "-"},
		{"/work", "."},
		{"/work/services/api", "services/api"},
		{"/elsewhere", "/elsewhere"},
		{"/workshop", "/workshop"},
	}
	for _, test := range cases {
		if got := displayRoot("/work", test.serverRoot); got != test.want {
			t.Errorf("displayRoot(/work, %q) = %q, want %q", test.serverRoot, got, test.want)
		}
	}
}
