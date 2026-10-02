package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/gustavofsantos/waythrough/internal/daemon"
	"github.com/gustavofsantos/waythrough/internal/lsp"
)

const (
	// statusDaemonsMax bounds how many daemons one status command reads.
	// One user runs a daemon per workspace and configuration, so a real
	// count is a handful.
	statusDaemonsMax = 64
	// statusReadTimeout bounds the wait for each daemon. A live daemon
	// answers at once, so a timeout means it is hung, which is itself the
	// answer. Daemons are read in parallel, so one hung daemon costs the
	// command this much, not this much per daemon.
	statusReadTimeout = 2 * time.Second
)

func newStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the running shared daemons, their sessions, and their language servers",
		Long: "Show every daemon that serve --shared started for this user: its workspace, " +
			"its sessions, its health, and for each language server its process, " +
			"startup time, memory, request latency, failures, and crashes.\n\n" +
			"Reading the status never counts as a session, so it never keeps a daemon alive.",
		Args: cobra.NoArgs,
	}
	jsonOutput := cmd.Flags().Bool("json", false, "print the reports as JSON")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runStatus(cmd.OutOrStdout(), *jsonOutput)
	}
	return cmd
}

// daemonStatus is what one status read found: a report, or why none came.
type daemonStatus struct {
	key    string
	paths  daemon.Paths
	report daemon.Report
	err    error
	// staleRemoved means no daemon held the key's lock, so its sockets
	// were left by one that was killed, and this read removed them.
	staleRemoved bool
}

func runStatus(stdout io.Writer, jsonOutput bool) error {
	runtimeDir, err := daemon.RuntimeDir()
	if err != nil {
		return err
	}
	keys, omitted, err := daemon.DaemonKeys(runtimeDir, statusDaemonsMax)
	if err != nil {
		return err
	}
	statuses, err := readStatuses(runtimeDir, keys)
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeStatusJSON(stdout, statuses, omitted)
	}
	return writeStatusText(stdout, statuses, omitted, time.Now())
}

// readStatuses reads every daemon at once, so the command waits for the
// slowest daemon rather than for the sum. len(keys) is at most
// statusDaemonsMax, which bounds the goroutines.
func readStatuses(runtimeDir string, keys []string) ([]daemonStatus, error) {
	statuses := make([]daemonStatus, len(keys))
	for index, key := range keys {
		paths, err := daemon.PathsFor(runtimeDir, key)
		if err != nil {
			return nil, err
		}
		statuses[index] = daemonStatus{key: key, paths: paths}
	}

	deadline := time.Now().Add(statusReadTimeout)
	var reads sync.WaitGroup
	for index := range statuses {
		reads.Add(1)
		go func() {
			defer reads.Done()
			status := &statuses[index]
			status.report, status.err = daemon.ReadStatus(status.paths.Status, deadline)
			if !errors.Is(status.err, daemon.ErrNotRunning) {
				return
			}
			// No daemon listens. Either one was killed and left its sockets,
			// or one is stopping its language servers after closing them;
			// only the second still holds the daemon lock.
			removed, err := daemon.RemoveStaleSockets(status.paths)
			if err != nil {
				status.err = err
				return
			}
			status.staleRemoved = removed
		}()
	}
	reads.Wait()
	return statuses, nil
}

// statusOutput is the JSON form of the status command. Daemons holds each
// report as the daemon sent it.
type statusOutput struct {
	Daemons     []daemon.Report     `json:"daemons"`
	Unreachable []unreachableDaemon `json:"unreachable"`
	// Omitted counts the daemons past statusDaemonsMax that were not read.
	Omitted int `json:"omitted"`
}

type unreachableDaemon struct {
	Key string `json:"key"`
	Log string `json:"log"`
	// Stale means the daemon was killed and left its sockets behind, and
	// this read removed them.
	Stale bool `json:"stale"`
	// Stopping means the daemon has closed its sockets and is stopping its
	// language servers.
	Stopping bool   `json:"stopping"`
	Error    string `json:"error"`
}

func writeStatusJSON(stdout io.Writer, statuses []daemonStatus, omitted int) error {
	output := statusOutput{
		Daemons:     []daemon.Report{},
		Unreachable: []unreachableDaemon{},
		Omitted:     omitted,
	}
	for _, status := range statuses {
		if status.err == nil {
			output.Daemons = append(output.Daemons, status.report)
			continue
		}
		notRunning := errors.Is(status.err, daemon.ErrNotRunning)
		output.Unreachable = append(output.Unreachable, unreachableDaemon{
			Key:      status.key,
			Log:      status.paths.Log,
			Stale:    notRunning && status.staleRemoved,
			Stopping: notRunning && !status.staleRemoved,
			Error:    status.err.Error(),
		})
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(output); err != nil {
		return fmt.Errorf("write status: %w", err)
	}
	return nil
}

func writeStatusText(stdout io.Writer, statuses []daemonStatus, omitted int, now time.Time) error {
	var text strings.Builder
	shown := 0
	for _, status := range statuses {
		if status.err != nil {
			continue
		}
		if shown > 0 {
			text.WriteString("\n")
		}
		writeDaemonText(&text, status, now)
		shown++
	}
	if len(statuses) == 0 {
		text.WriteString("No waythrough daemon is running for this user.\n" +
			"A daemon runs while a `waythrough serve --shared` session needs it.\n")
	}

	for _, status := range statuses {
		if status.err == nil {
			continue
		}
		if !errors.Is(status.err, daemon.ErrNotRunning) {
			fmt.Fprintf(&text, "\nunreachable daemon %s: %s\n  log: %s\n",
				status.key, printable(status.err.Error()), status.paths.Log)
			continue
		}
		if status.staleRemoved {
			fmt.Fprintf(&text, "\nremoved the sockets of daemon %s, which was killed "+
				"without removing them.\n  log: %s\n", status.key, status.paths.Log)
			continue
		}
		fmt.Fprintf(&text, "\ndaemon %s is stopping its language servers.\n  log: %s\n",
			status.key, status.paths.Log)
	}
	if omitted > 0 {
		fmt.Fprintf(&text, "\n%d more daemons not shown; the limit is %d.\n",
			omitted, statusDaemonsMax)
	}

	if _, err := io.WriteString(stdout, text.String()); err != nil {
		return fmt.Errorf("write status: %w", err)
	}
	return nil
}

func writeDaemonText(text *strings.Builder, status daemonStatus, now time.Time) {
	report := status.report
	sessions := report.Sessions
	fmt.Fprintf(text, "%s  [%s]\n", printable(report.Root), report.Health)
	fmt.Fprintf(text, "  daemon    pid %d, waythrough %s, up %s, %s\n",
		report.PID, report.Version, formatDuration(now.Sub(report.StartedAt)),
		describeState(sessions, now))
	fmt.Fprintf(text, "  sessions  %d active of %d, %d since start, %d refused\n",
		sessions.Active, sessions.Max, sessions.AdmittedTotal,
		sessions.RefusedBusy+sessions.RefusedPeer)
	fmt.Fprintf(text, "  runtime   %d goroutines, %s heap, %s total\n",
		report.Runtime.Goroutines, formatBytes(report.Runtime.HeapBytes),
		formatBytes(report.Runtime.TotalBytes))
	fmt.Fprintf(text, "  log       %s\n\n", status.paths.Log)

	table := tabwriter.NewWriter(text, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "  SERVER\tROOT\tSTATUS\tHEALTH\tPID\tUP\tSTARTUP\tRSS\tDOCS\t"+
		"REQUESTS\tFAILED\tRECENT FAILED\tP50\tP95\tMAX\tCRASHES")
	for _, server := range report.LanguageServers {
		writeServerRow(table, report.Root, server, now)
	}
	_ = table.Flush()

	for _, server := range report.LanguageServers {
		if server.Requests.LastError == "" {
			continue
		}
		fmt.Fprintf(text, "  last error from %s at %s, %s ago: %s\n",
			printable(server.Name), displayRoot(report.Root, server.Root),
			formatDuration(now.Sub(server.Requests.LastErrorAt)),
			printable(server.Requests.LastError))
	}
}

func writeServerRow(table io.Writer, daemonRoot string, server lsp.InstanceStats, now time.Time) {
	requests := server.Requests
	up := "-"
	if server.PID != 0 {
		up = formatDuration(now.Sub(server.AttemptStartedAt))
	}
	startup := "-"
	if server.StartupMilliseconds > 0 {
		startup = formatDuration(time.Duration(server.StartupMilliseconds) * time.Millisecond)
	}
	latency := func(milliseconds int64) string {
		if requests.RecentCount == 0 {
			return "-"
		}
		return formatDuration(time.Duration(milliseconds) * time.Millisecond)
	}
	const rowFormat = "  %s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t" +
		"%d\t%d\t%d/%d\t%s\t%s\t%s\t%d/%d\n"
	_, _ = fmt.Fprintf(table, rowFormat,
		printable(server.Name), displayRoot(daemonRoot, server.Root),
		server.Status, server.Health,
		formatPID(server.PID), up, startup, formatResident(server.ResidentBytes),
		server.OpenDocuments, requests.Total, requests.Failed,
		requests.RecentFailed, requests.RecentCount,
		latency(requests.RecentDurationMillisecondsP50),
		latency(requests.RecentDurationMillisecondsP95),
		latency(requests.RecentDurationMillisecondsMax),
		server.CrashesInWindow, server.CrashLimit)
}

func describeState(sessions daemon.SessionStats, now time.Time) string {
	state := string(sessions.State)
	if sessions.DrainAt.IsZero() {
		return state
	}
	return fmt.Sprintf("%s, drains in %s", state, formatDuration(sessions.DrainAt.Sub(now)))
}

// displayRoot shortens a server's root to a path relative to the daemon's,
// when it is inside it, since most servers run at the workspace root.
func displayRoot(daemonRoot, serverRoot string) string {
	if serverRoot == "" {
		return "-"
	}
	relative, err := filepath.Rel(daemonRoot, serverRoot)
	if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
		return printable(serverRoot)
	}
	return printable(relative)
}

// printable keeps text to one line of the report, and replaces every
// character a terminal would act on rather than show. An error message
// comes from a language server, and a path from the filesystem, so either
// could carry an escape sequence.
func printable(text string) string {
	oneLine := strings.Join(strings.Fields(text), " ")
	return strings.Map(func(character rune) rune {
		if unicode.IsPrint(character) {
			return character
		}
		return '?'
	}, oneLine)
}

func formatPID(pid int) string {
	if pid == 0 {
		return "-"
	}
	return strconv.Itoa(pid)
}

// formatResident shows zero as unknown: a running process always has some
// memory resident, so zero means the platform could not say.
func formatResident(residentBytes uint64) string {
	if residentBytes == 0 {
		return "-"
	}
	return formatBytes(residentBytes)
}

// formatDuration rounds a duration to the two largest units a person
// reads at a glance. A negative duration, from clock skew, reads as zero.
func formatDuration(duration time.Duration) string {
	duration = max(duration, 0)
	switch {
	case duration < time.Second:
		return fmt.Sprintf("%dms", duration.Milliseconds())
	case duration < time.Minute:
		return fmt.Sprintf("%.1fs", duration.Seconds())
	case duration < time.Hour:
		minutes := duration / time.Minute
		return fmt.Sprintf("%dm%02ds", minutes, (duration-minutes*time.Minute)/time.Second)
	case duration < 24*time.Hour:
		hours := duration / time.Hour
		return fmt.Sprintf("%dh%02dm", hours, (duration-hours*time.Hour)/time.Minute)
	default:
		days := duration / (24 * time.Hour)
		return fmt.Sprintf("%dd%02dh", days, (duration-days*24*time.Hour)/time.Hour)
	}
}

// formatBytes shows a size in binary units with one decimal.
func formatBytes(size uint64) string {
	const kibibyte = 1 << 10
	switch {
	case size < kibibyte:
		return fmt.Sprintf("%d B", size)
	case size < kibibyte<<10:
		return fmt.Sprintf("%.1f KiB", float64(size)/kibibyte)
	case size < kibibyte<<20:
		return fmt.Sprintf("%.1f MiB", float64(size)/(kibibyte<<10))
	default:
		return fmt.Sprintf("%.1f GiB", float64(size)/(kibibyte<<20))
	}
}
