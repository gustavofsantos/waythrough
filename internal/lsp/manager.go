// Package lsp starts and tracks the language-server subprocesses Waythrough
// manages, and gates readiness on each server's own signal that it can
// answer a request, not merely that its process has started.
package lsp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/gustavofsantos/waythrough/internal/config"
)

// defaultProgressDebounce is a placeholder, not a measured value. The real
// constant is an open question pending measurement against real clojure-lsp
// and lua-language-server runs (see the initial-bundle issue's Q1).
const defaultProgressDebounce = 250 * time.Millisecond

const (
	defaultRestartLimit    = 3
	defaultRestartWindow   = 60 * time.Second
	defaultShutdownGrace   = 5 * time.Second
	defaultToolCallTimeout = 30 * time.Second
	maxCallHierarchyRoots  = 16
	maxCallHierarchyCalls  = 4096
	maxCallHierarchySites  = 16384
)

const maxConcurrentCallHierarchyRequests = 4

// Status is a language server's place in its own lifecycle.
type Status int

const (
	// StatusIdle means no supervisor attempt has begun for this configured
	// server yet.
	StatusIdle Status = iota
	// StatusStarting covers everything from process spawn through the
	// readiness gate: handshaking, and indexing when readiness is progress.
	StatusStarting
	// StatusReady means the server can answer a request.
	StatusReady
	// StatusFailed means the server crashed more times than the restart
	// limit allows within the restart window, and Waythrough has stopped
	// restarting it.
	StatusFailed
)

// Manager starts and tracks configured language-server subprocesses.
type Manager struct {
	root             string
	logger           *slog.Logger
	progressDebounce time.Duration
	restartLimit     int
	restartWindow    time.Duration
	shutdownGrace    time.Duration
	toolCallTimeout  time.Duration
	demandStart      bool

	mu          sync.Mutex
	lifetimeCtx context.Context
	procs       map[string]*serverProcess
}

// Location is a position in a file, 1-based to match how a coding agent
// reads line and column numbers.
type Location struct {
	File   string
	Line   int
	Column int
}

// Symbol identifies one callable declaration in a call hierarchy. Location
// is the selection the server says should be revealed, rather than the whole
// declaration range, so a caller can use it as the next hierarchy position.
type Symbol struct {
	Name     string
	Kind     string
	Detail   string
	Location Location
}

// Call is one direct edge from a hierarchy root. CallSites names every
// source position that contributes that edge.
type Call struct {
	Symbol    Symbol
	CallSites []Location
}

// CallHierarchy groups one prepared root with its direct calls. A position
// can prepare more than one root, and no root is selected silently.
type CallHierarchy struct {
	Symbol Symbol
	Calls  []Call
}

// Edit is a range replacement in a file, 1-based to match Location. Unlike
// Location it names both ends of the range a rename must replace, since a
// single point cannot say how much text to remove.
type Edit struct {
	File        string
	StartLine   int
	StartColumn int
	EndLine     int
	EndColumn   int
	NewText     string
}

// Signature is one callable form a symbol takes at a call site: the form
// itself, its doc comment, and the label of each parameter it accepts.
type Signature struct {
	Label         string
	Documentation string
	Parameters    []string
}

// SignatureHelp is what a language server knows about the call the cursor
// sits inside: every signature the call could match, and which one, and
// which of its parameters, the cursor is on.
//
// ActiveSignature indexes Signatures, and ActiveParameter indexes that
// signature's Parameters. Both are 0-based, unlike Location's line and
// column, because both are list indices rather than file positions. Neither
// means anything when Signatures is empty.
type SignatureHelp struct {
	Signatures      []Signature
	ActiveSignature int
	ActiveParameter int
}

// Diagnostic is one problem a language server reports in a file: where it
// is, how serious it is, and what it says. Its range is 1-based, to match
// Location and Edit.
//
// Severity, Source and Code are each empty when the server left them out.
// The spec urges a server to always set a severity, but does not require it.
type Diagnostic struct {
	StartLine   int
	StartColumn int
	EndLine     int
	EndColumn   int
	Severity    string
	Message     string
	Source      string
	Code        string
}

// Option configures a Manager.
type Option func(*Manager)

// WithProgressDebounce overrides how long a `progress`-mode server has,
// after the initialize/initialized handshake, to open its first
// WorkDoneProgress token before Waythrough treats it as having none to
// report and marks it ready.
func WithProgressDebounce(d time.Duration) Option {
	return func(m *Manager) { m.progressDebounce = d }
}

// WithRestartLimit overrides how many times a server may exit within
// window before Waythrough stops restarting it and marks it failed.
func WithRestartLimit(limit int, window time.Duration) Option {
	return func(m *Manager) {
		m.restartLimit = limit
		m.restartWindow = window
	}
}

// WithShutdownGrace overrides how long Shutdown waits for a server to exit
// on its own, after `shutdown` and `exit`, before killing its process.
func WithShutdownGrace(d time.Duration) Option {
	return func(m *Manager) { m.shutdownGrace = d }
}

// WithToolCallTimeout overrides how long a request such as Definition waits
// for its server to become ready before giving up.
func WithToolCallTimeout(d time.Duration) Option {
	return func(m *Manager) { m.toolCallTimeout = d }
}

// WithLogger routes this Manager's lifecycle records, and the stderr of
// every language server it starts, to logger. Both are written at debug
// level, so a logger with no debug handler — the default — leaves a
// server's stderr discarded exactly as it was before.
//
// Precondition: logger is not nil. A Manager always holds a logger it can
// call, so nothing here has to guard a log statement.
func WithLogger(logger *slog.Logger) Option {
	return func(m *Manager) {
		if logger == nil {
			panic("lsp: WithLogger needs a logger, got nil")
		}
		m.logger = logger
	}
}

// WithDemandStart makes Start record the manager lifetime without spawning
// every configured server. The first waiter for a server starts its one
// supervisor. This is intended for built-in configurations, where starting
// unrelated installed servers would make each of them index the same root.
func WithDemandStart() Option {
	return func(m *Manager) { m.demandStart = true }
}

// NewManager builds a Manager for entries. root is the project directory
// passed to each language server as its workspace root.
//
// Precondition: config.Validate returned no error for the config entries
// came from. procs is keyed by entry name, so two entries sharing a name
// would collapse into one process here, in silence, and the second would
// never start. config.Validate is the one enforcer of that uniqueness.
func NewManager(root string, entries []config.LanguageServer, opts ...Option) *Manager {
	m := &Manager{
		root:             root,
		logger:           slog.New(slog.DiscardHandler),
		progressDebounce: defaultProgressDebounce,
		restartLimit:     defaultRestartLimit,
		restartWindow:    defaultRestartWindow,
		shutdownGrace:    defaultShutdownGrace,
		toolCallTimeout:  defaultToolCallTimeout,
		procs:            make(map[string]*serverProcess, len(entries)),
	}
	for _, opt := range opts {
		opt(m)
	}
	for _, entry := range entries {
		m.procs[entry.Name] = newServerProcess(entry, m.logger)
	}
	return m
}

// newServerProcess builds the tracking state for one configured language
// server, before any attempt to spawn it.
//
// It exists so that no caller has to remember the logger: a serverProcess
// records its own lifecycle, so one built without a logger would nil-panic
// on the first transition it made rather than at the point of the mistake.
func newServerProcess(entry config.LanguageServer, logger *slog.Logger) *serverProcess {
	if logger == nil {
		panic("lsp: newServerProcess needs a logger, got nil")
	}

	return &serverProcess{
		entry:      entry,
		logger:     logger,
		readyCh:    make(chan struct{}),
		retiredCh:  make(chan struct{}),
		exitedCh:   make(chan struct{}),
		restartCh:  make(chan struct{}, 1),
		stoppingCh: make(chan struct{}),
	}
}

// Start records ctx as the lifetime of every process and, unless demand-start
// mode is enabled, starts one supervisor for every configured entry. It
// returns once the supervisors have been launched, not once every server is
// ready — use WaitReady for that. A server that exits unexpectedly is
// restarted; one that exits too often is not — see WithRestartLimit.
//
// ctx is the lifetime of every process this Manager owns, the ones a
// Restart spawns included: runServer is the only code that spawns, and it
// runs from here until Shutdown. No single tool call's context can
// therefore end a language server when that call returns.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.lifetimeCtx == nil {
		m.lifetimeCtx = ctx
	}
	lifetimeCtx := m.lifetimeCtx
	if m.demandStart {
		m.mu.Unlock()
		return nil
	}
	procs := make([]*serverProcess, 0, len(m.procs))
	for _, proc := range m.procs {
		procs = append(procs, proc)
	}
	m.mu.Unlock()

	for _, proc := range procs {
		m.startSupervisor(lifetimeCtx, proc)
	}
	return nil
}

// startSupervisor is the single spawn gate for one configured server. The
// process-level reservation makes concurrent first requests single-flight.
func (m *Manager) startSupervisor(ctx context.Context, proc *serverProcess) bool {
	if ctx == nil || !proc.beginSupervision() {
		return false
	}
	go m.runServer(ctx, proc)
	return true
}

// ensureSupervisor starts proc on the Manager lifetime captured by Start.
// Eager managers reach this with an existing supervisor, so the same call is
// safe on every request without branching on startup mode.
func (m *Manager) ensureSupervisor(proc *serverProcess) bool {
	m.mu.Lock()
	ctx := m.lifetimeCtx
	m.mu.Unlock()
	return m.startSupervisor(ctx, proc)
}

// runServer owns proc's process from Start until Shutdown or a done ctx:
// spawn, handshake, wait for exit, and either restart or give up. A server
// that spends its crash budget parks here waiting for a restart rather than
// returning, so exactly one supervisor owns proc at every moment. It is the
// only caller of proc.wait, so it is the only goroutine that calls
// exec.Cmd.Wait for this process, which Go allows only once per process.
func (m *Manager) runServer(ctx context.Context, proc *serverProcess) {
	defer proc.endSupervision()

	var exits []time.Time

	for {
		generation, spawning := proc.beginAttempt()
		if !spawning {
			return
		}

		m.logger.Debug("language server starting",
			slog.String("server", proc.entry.Name),
			slog.String("command", proc.entry.Command),
			slog.Any("args", proc.entry.Args),
			slog.Int("attempt", generation))

		// A start or handshake that fails is why a later tool call reports
		// the server still starting, so it is recorded here rather than
		// left for the caller of that tool to guess at.
		if err := proc.startProcess(ctx, generation); err != nil {
			m.logger.Warn("language server failed to start",
				slog.String("server", proc.entry.Name),
				slog.Int("attempt", generation),
				slog.String("error", truncateForLog(err.Error())))
		} else {
			if err := proc.handshake(ctx, m.root); err != nil {
				m.logger.Warn("language server handshake failed",
					slog.String("server", proc.entry.Name),
					slog.Int("attempt", generation),
					slog.String("error", truncateForLog(err.Error())))
			} else {
				go proc.negotiateReadiness(ctx, m.progressDebounce, generation)
			}
			proc.wait()
			m.logger.Debug("language server exited",
				slog.String("server", proc.entry.Name),
				slog.Int("attempt", generation))
		}

		if proc.shutdownRequested() {
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}

		// A stop this Manager asked for is not a crash, so it leaves the
		// budget holding exactly the crashes that came before it.
		if proc.takeRestartRequest() {
			continue
		}

		exits = withinWindow(append(exits, time.Now()), m.restartWindow)
		if len(exits) <= m.restartLimit {
			continue
		}

		proc.markFailed(generation)
		if !proc.awaitRestart(ctx) {
			return
		}
		// A restart of a server that already gave up is a fresh start, so
		// the crashes that spent the budget stop counting against it.
		exits = nil
	}
}

func withinWindow(times []time.Time, window time.Duration) []time.Time {
	cutoff := time.Now().Add(-window)
	kept := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}

// WaitReady blocks until the named server is ready, fails permanently, the
// context is done, or timeout elapses, whichever comes first.
func (m *Manager) WaitReady(ctx context.Context, name string, timeout time.Duration) error {
	proc, err := m.serverNamed(name)
	if err != nil {
		return err
	}
	m.ensureSupervisor(proc)

	// Every started server is on attempt 1 or later, so "after attempt 0"
	// accepts whichever attempt the server happens to be on.
	return m.waitReady(ctx, name, timeout, 0)
}

// Restart stops the named server and returns once its replacement has
// passed its own readiness gate. A demand-started server no request has used
// yet is started instead, because it has no process to replace. A server that
// already spent its crash budget takes a restart too: that is the state a
// restart is most needed in. Waythrough cannot see that a server answers
// from a stale index, so the caller is the only judge of when a restart is
// due.
//
// The replacement spawns on the lifetime context Start was given, never on
// ctx. ctx bounds only the wait: how long the stop and the new readiness
// gate may take before Restart gives up on them.
//
// One narrow case answers late. An attempt that has claimed its place but
// not yet published a process has nothing to stop, so a Restart that lands
// inside those few instructions leaves that attempt running and waits out
// toolCallTimeout before reporting the server still starting. The server
// itself is unharmed, and the request it queued is spent on the next exit
// rather than lost. Closing this would cost a retry loop around a window
// no larger than one spawn, which is not a trade this design makes.
func (m *Manager) Restart(ctx context.Context, name string) error {
	proc, err := m.serverNamed(name)
	if err != nil {
		return err
	}
	started := m.ensureSupervisor(proc)
	if started || proc.snapshot().status == StatusIdle {
		// A demand-started server has no process to replace. Starting it is the
		// only transition that can satisfy the caller's request for a ready one.
		// Concurrent callers coalesce while the first supervisor claims attempt 1.
		return m.waitReady(ctx, name, m.toolCallTimeout, 0)
	}

	// The attempt read here is the one this call retires. Everything below
	// names it, so a restart can neither stop the replacement it asked for
	// nor mistake the retired attempt's readiness for the new one's.
	retired := proc.snapshot().generation
	m.logger.Debug("restarting language server",
		slog.String("server", name),
		slog.Int("retired_attempt", retired))
	proc.requestRestart(retired)
	proc.stopAttempt(ctx, m.shutdownGrace, retired)
	return m.waitReady(ctx, name, m.toolCallTimeout, retired)
}

// waitReady blocks until the named server is ready on an attempt later than
// afterGeneration. It loops because a server can move between attempts
// while a caller waits: each wake re-reads which attempt the server is on
// and waits on that attempt's channels instead of on a channel this
// generation has abandoned. The deadline bounds the loop, and each attempt
// closes its channels exactly once, so no wake can repeat.
func (m *Manager) waitReady(
	ctx context.Context, name string, timeout time.Duration, afterGeneration int,
) error {
	proc, err := m.serverNamed(name)
	if err != nil {
		return err
	}

	deadline := time.Now().Add(timeout)

	for {
		state := proc.snapshot()

		// Until a later attempt begins, this server's status still answers
		// for the attempt the caller wants replaced, so the wait is for the
		// next attempt to start rather than for this one to settle.
		wake := state.retiredCh
		if state.generation > afterGeneration {
			switch state.status {
			case StatusReady:
				return nil
			case StatusFailed:
				return fmt.Errorf("language server %q failed to start", name)
			}
			wake = state.readyCh
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("language server %q is still starting", name)
		}

		timer := time.NewTimer(remaining)
		select {
		case <-wake:
			timer.Stop()
		case <-proc.stoppingCh:
			timer.Stop()
			return fmt.Errorf("language server %q is shutting down", name)
		case <-timer.C:
			return fmt.Errorf("language server %q is still starting", name)
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("waiting for language server %q: %w", name, ctx.Err())
		}
	}
}

// Status reports the named server's current lifecycle status.
func (m *Manager) Status(name string) (Status, error) {
	proc, err := m.serverNamed(name)
	if err != nil {
		return 0, err
	}
	return proc.snapshot().status, nil
}

// Definition asks the named server for the definition at a 1-based
// line/column in file, waiting for the server to be ready first and
// syncing the file's current on-disk content to it before asking. file may
// be relative to Manager's root or absolute.
func (m *Manager) Definition(
	ctx context.Context, name, file string, line, column int,
) ([]Location, error) {
	proc, path, err := m.prepare(ctx, name, file)
	if err != nil {
		return nil, err
	}

	result, err := proc.currentServer().Definition(ctx, &protocol.DefinitionParams{
		TextDocumentPositionParams: textDocumentPosition(path, line, column),
	})
	if err != nil {
		return nil, fmt.Errorf("definition: %w", err)
	}
	return locationsFromDefinitionResult(path, result)
}

// References asks the named server for every reference to the symbol at a
// 1-based line/column in file, waiting for the server to be ready first and
// syncing the file's current on-disk content to it before asking. file may
// be relative to Manager's root or absolute.
func (m *Manager) References(
	ctx context.Context, name, file string, line, column int,
) ([]Location, error) {
	proc, path, err := m.prepare(ctx, name, file)
	if err != nil {
		return nil, err
	}

	protocolLocations, err := proc.currentServer().References(ctx, &protocol.ReferenceParams{
		TextDocumentPositionParams: textDocumentPosition(path, line, column),
		Context:                    protocol.ReferenceContext{IncludeDeclaration: true},
	})
	if err != nil {
		return nil, fmt.Errorf("references: %w", err)
	}

	locations := make([]Location, len(protocolLocations))
	for i, loc := range protocolLocations {
		locations[i] = locationFromProtocol(loc)
	}
	return locations, nil
}

// Rename asks the named server for the workspace edit that renames the
// symbol at a 1-based line/column in file to newName, waiting for the
// server to be ready first and syncing the file's current on-disk content
// to it before asking. file may be relative to Manager's root or absolute.
// Rename does not write the edit to disk; the caller applies it.
func (m *Manager) Rename(
	ctx context.Context, name, file string, line, column int, newName string,
) ([]Edit, error) {
	proc, path, err := m.prepare(ctx, name, file)
	if err != nil {
		return nil, err
	}

	result, err := proc.currentServer().Rename(ctx, &protocol.RenameParams{
		TextDocumentPositionParams: textDocumentPosition(path, line, column),
		NewName:                    newName,
	})
	if err != nil {
		return nil, fmt.Errorf("rename: %w", err)
	}
	return editsFromWorkspaceEdit(result)
}

// SignatureHelp asks the named server which signatures the call at a 1-based
// line/column in file could match, waiting for the server to be ready first
// and syncing the file's current on-disk content to it before asking. file
// may be relative to Manager's root or absolute.
func (m *Manager) SignatureHelp(
	ctx context.Context, name, file string, line, column int,
) (SignatureHelp, error) {
	proc, path, err := m.prepare(ctx, name, file)
	if err != nil {
		return SignatureHelp{}, err
	}

	result, err := proc.currentServer().SignatureHelp(ctx, &protocol.SignatureHelpParams{
		TextDocumentPositionParams: textDocumentPosition(path, line, column),
	})
	if err != nil {
		return SignatureHelp{}, fmt.Errorf("signatureHelp: %w", err)
	}
	return signatureHelpFromProtocol(result)
}

// CallHierarchy asks the named server for the direct calls in direction from
// every symbol prepared at a 1-based position. The prepared item is sent back
// verbatim because its Data field belongs to the language server.
func (m *Manager) CallHierarchy(
	ctx context.Context, name, file string, line, column int, direction string,
) ([]CallHierarchy, error) {
	if direction != "incoming" && direction != "outgoing" {
		return nil, fmt.Errorf(
			"call hierarchy direction must be incoming or outgoing, got %q", direction)
	}

	ctx, cancel := context.WithTimeout(ctx, m.toolCallTimeout)
	defer cancel()

	proc, err := m.serverNamed(name)
	if err != nil {
		return nil, err
	}
	if err := m.WaitReady(ctx, name, m.toolCallTimeout); err != nil {
		return nil, err
	}
	attempt, err := proc.callHierarchyAttempt(name)
	if err != nil {
		return nil, err
	}
	path := m.resolvePath(file)
	if err := proc.syncFileOnAttempt(ctx, path, attempt); err != nil {
		return nil, fmt.Errorf("sync call hierarchy file: %w", err)
	}

	items, err := attempt.server.PrepareCallHierarchy(ctx, &protocol.CallHierarchyPrepareParams{
		TextDocumentPositionParams: textDocumentPosition(path, line, column),
	})
	if err != nil {
		return nil, fmt.Errorf("prepare call hierarchy: %w", err)
	}
	if err := proc.validateCallHierarchyAttempt(name, attempt); err != nil {
		return nil, err
	}
	if len(items) > maxCallHierarchyRoots {
		return nil, fmt.Errorf(
			"prepare call hierarchy returned %d roots; maximum is %d",
			len(items), maxCallHierarchyRoots)
	}
	sort.Slice(items, func(left, right int) bool {
		leftSymbol := callHierarchySymbol(items[left])
		rightSymbol := callHierarchySymbol(items[right])
		return symbolLess(leftSymbol, rightSymbol)
	})

	hierarchies, err := directedCallHierarchies(
		ctx, proc, name, attempt, items, direction)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("call hierarchy: %w", err)
	}
	sort.Slice(hierarchies, func(left, right int) bool {
		return callHierarchyLess(hierarchies[left], hierarchies[right])
	})
	if err := proc.validateCallHierarchyAttempt(name, attempt); err != nil {
		return nil, err
	}
	return hierarchies, nil
}

type directedCallResponse struct {
	incoming []protocol.CallHierarchyIncomingCall
	outgoing []protocol.CallHierarchyOutgoingCall
}

func directedCallHierarchies(
	ctx context.Context,
	proc *serverProcess,
	name string,
	attempt serverAttempt,
	items []protocol.CallHierarchyItem,
	direction string,
) ([]CallHierarchy, error) {
	hierarchies := make([]CallHierarchy, len(items))
	budget := callHierarchyBudget{}
	for start := 0; start < len(items); start += maxConcurrentCallHierarchyRequests {
		end := min(start+maxConcurrentCallHierarchyRequests, len(items))
		responses, err := requestDirectedCallBatch(
			ctx, attempt.server, items[start:end], direction)
		if err != nil {
			return nil, err
		}
		if err := proc.validateCallHierarchyAttempt(name, attempt); err != nil {
			return nil, err
		}
		for offset, response := range responses {
			index := start + offset
			item := items[index]
			if direction == "incoming" {
				if err := budget.acceptIncoming(item.Name, response.incoming); err != nil {
					return nil, err
				}
				hierarchies[index] = incomingCallHierarchy(item, response.incoming)
				continue
			}
			if err := budget.acceptOutgoing(item.Name, response.outgoing); err != nil {
				return nil, err
			}
			hierarchies[index] = outgoingCallHierarchy(item, response.outgoing)
		}
	}
	return hierarchies, nil
}

func requestDirectedCallBatch(
	ctx context.Context,
	server protocol.Server,
	items []protocol.CallHierarchyItem,
	direction string,
) ([]directedCallResponse, error) {
	if len(items) > maxConcurrentCallHierarchyRequests {
		return nil, fmt.Errorf(
			"directed call batch has %d roots; maximum is %d",
			len(items), maxConcurrentCallHierarchyRequests)
	}
	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	responses := make([]directedCallResponse, len(items))
	firstError := make(chan error, 1)
	var wait sync.WaitGroup
	for index := range items {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			response, err := requestDirectedCall(batchCtx, server, items[index], direction)
			if err != nil {
				select {
				case firstError <- err:
					cancel()
				default:
				}
				return
			}
			responses[index] = response
		}()
	}
	wait.Wait()
	select {
	case err := <-firstError:
		return nil, err
	default:
		return responses, nil
	}
}

func requestDirectedCall(
	ctx context.Context,
	server protocol.Server,
	item protocol.CallHierarchyItem,
	direction string,
) (directedCallResponse, error) {
	if err := ctx.Err(); err != nil {
		return directedCallResponse{}, fmt.Errorf("call hierarchy: %w", err)
	}
	if direction == "incoming" {
		params := &protocol.CallHierarchyIncomingCallsParams{Item: item}
		calls, err := server.IncomingCalls(ctx, params)
		if err != nil {
			return directedCallResponse{}, fmt.Errorf("incoming calls for %q: %w", item.Name, err)
		}
		if err := ctx.Err(); err != nil {
			return directedCallResponse{}, fmt.Errorf("call hierarchy: %w", err)
		}
		return directedCallResponse{incoming: calls}, nil
	}

	calls, err := server.OutgoingCalls(ctx, &protocol.CallHierarchyOutgoingCallsParams{Item: item})
	if err != nil {
		return directedCallResponse{}, fmt.Errorf("outgoing calls for %q: %w", item.Name, err)
	}
	if err := ctx.Err(); err != nil {
		return directedCallResponse{}, fmt.Errorf("call hierarchy: %w", err)
	}
	return directedCallResponse{outgoing: calls}, nil
}

// Diagnostics asks the named server what is wrong in file, waiting for the
// server to be ready first and syncing the file's current on-disk content to
// it before asking. file may be relative to Manager's root or absolute. A
// diagnostic belongs to the file as a whole, so this takes no position.
//
// Only a server that advertised pull diagnostics at its handshake is asked.
// One that reports diagnostics by pushing them instead fails here, since
// Waythrough keeps no record of what a server pushed.
func (m *Manager) Diagnostics(ctx context.Context, name, file string) ([]Diagnostic, error) {
	proc, path, err := m.prepare(ctx, name, file)
	if err != nil {
		return nil, err
	}
	if err := proc.requirePullDiagnostics(name); err != nil {
		return nil, err
	}

	result, err := proc.currentServer().Diagnostic(ctx, &protocol.DocumentDiagnosticParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri.File(path)},
	})
	if err != nil {
		return nil, fmt.Errorf("diagnostic: %w", err)
	}
	return diagnosticsFromReport(path, result)
}

// prepare resolves file, waits for name's server to be ready, and syncs
// file's current on-disk content to it — the setup every LSP position
// request needs before it can ask the server anything.
func (m *Manager) prepare(ctx context.Context, name, file string) (*serverProcess, string, error) {
	proc, err := m.serverNamed(name)
	if err != nil {
		return nil, "", err
	}
	if err := m.WaitReady(ctx, name, m.toolCallTimeout); err != nil {
		return nil, "", err
	}

	// A ready server has completed a handshake, so it holds a connection.
	// Checking that here keeps a readiness signal that escaped its own
	// attempt from being read as an invitation to talk to nothing.
	if proc.currentServer() == nil {
		return nil, "", fmt.Errorf("language server %q reported ready with no connection", name)
	}

	path := m.resolvePath(file)
	if err := proc.syncFile(ctx, path); err != nil {
		return nil, "", fmt.Errorf("sync %s: %w", path, err)
	}
	return proc, path, nil
}

func textDocumentPosition(path string, line, column int) protocol.TextDocumentPositionParams {
	return protocol.TextDocumentPositionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri.File(path)},
		Position:     protocol.Position{Line: uint32(line - 1), Character: uint32(column - 1)},
	}
}

func (m *Manager) resolvePath(file string) string {
	if filepath.IsAbs(file) {
		return file
	}
	return filepath.Join(m.root, file)
}

func locationsFromDefinitionResult(
	requestedPath string, result protocol.DefinitionResult,
) ([]Location, error) {
	switch v := result.(type) {
	case nil:
		return nil, nil
	case *protocol.Location:
		if v == nil {
			return nil, nil
		}
		return []Location{locationFromProtocol(*v)}, nil
	case protocol.LocationSlice:
		locations := make([]Location, len(v))
		for i, loc := range v {
			locations[i] = locationFromProtocol(loc)
		}
		return locations, nil
	default:
		return nil, fmt.Errorf(
			"definition for %s: unsupported result shape %T (linkSupport was not advertised)",
			requestedPath, result)
	}
}

func incomingCallHierarchy(
	root protocol.CallHierarchyItem, incoming []protocol.CallHierarchyIncomingCall,
) CallHierarchy {
	hierarchy := CallHierarchy{
		Symbol: callHierarchySymbol(root),
		Calls:  make([]Call, len(incoming)),
	}
	for index, call := range incoming {
		hierarchy.Calls[index] = Call{
			Symbol:    callHierarchySymbol(call.From),
			CallSites: locationsFromRanges(call.From.URI, call.FromRanges),
		}
	}
	sortCalls(hierarchy.Calls)
	return hierarchy
}

type callHierarchyBudget struct {
	calls int
	sites int
}

func (b *callHierarchyBudget) acceptIncoming(
	root string, calls []protocol.CallHierarchyIncomingCall,
) error {
	sites := 0
	for _, call := range calls {
		sites += len(call.FromRanges)
	}
	return b.accept(root, "incoming", len(calls), sites)
}

func (b *callHierarchyBudget) acceptOutgoing(
	root string, calls []protocol.CallHierarchyOutgoingCall,
) error {
	sites := 0
	for _, call := range calls {
		sites += len(call.FromRanges)
	}
	return b.accept(root, "outgoing", len(calls), sites)
}

func (b *callHierarchyBudget) accept(
	root, direction string, calls, sites int,
) error {
	attemptedCalls := b.calls + calls
	if attemptedCalls > maxCallHierarchyCalls {
		return fmt.Errorf(
			"%s calls for root %q would raise total to %d; maximum is %d",
			direction, root, attemptedCalls, maxCallHierarchyCalls)
	}
	attemptedSites := b.sites + sites
	if attemptedSites > maxCallHierarchySites {
		return fmt.Errorf(
			"%s call sites for root %q would raise total to %d; maximum is %d",
			direction, root, attemptedSites, maxCallHierarchySites)
	}
	b.calls = attemptedCalls
	b.sites = attemptedSites
	return nil
}

func outgoingCallHierarchy(
	root protocol.CallHierarchyItem, outgoing []protocol.CallHierarchyOutgoingCall,
) CallHierarchy {
	hierarchy := CallHierarchy{
		Symbol: callHierarchySymbol(root),
		Calls:  make([]Call, len(outgoing)),
	}
	for index, call := range outgoing {
		hierarchy.Calls[index] = Call{
			Symbol:    callHierarchySymbol(call.To),
			CallSites: locationsFromRanges(root.URI, call.FromRanges),
		}
	}
	sortCalls(hierarchy.Calls)
	return hierarchy
}

func locationsFromRanges(docURI uri.URI, ranges []protocol.Range) []Location {
	locations := make([]Location, len(ranges))
	for index, sourceRange := range ranges {
		locations[index] = locationFromRange(docURI, sourceRange)
	}
	sort.Slice(locations, func(left, right int) bool {
		return locationLess(locations[left], locations[right])
	})
	return locations
}

func sortCalls(calls []Call) {
	sort.Slice(calls, func(left, right int) bool {
		return callLess(calls[left], calls[right])
	})
}

func symbolLess(left, right Symbol) bool {
	if left.Location != right.Location {
		return locationLess(left.Location, right.Location)
	}
	if left.Name != right.Name {
		return left.Name < right.Name
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	return left.Detail < right.Detail
}

func locationLess(left, right Location) bool {
	if left.File != right.File {
		return left.File < right.File
	}
	if left.Line != right.Line {
		return left.Line < right.Line
	}
	return left.Column < right.Column
}

func callLess(left, right Call) bool {
	if symbolLess(left.Symbol, right.Symbol) {
		return true
	}
	if symbolLess(right.Symbol, left.Symbol) {
		return false
	}
	for index := 0; index < min(len(left.CallSites), len(right.CallSites)); index++ {
		if left.CallSites[index] != right.CallSites[index] {
			return locationLess(left.CallSites[index], right.CallSites[index])
		}
	}
	return len(left.CallSites) < len(right.CallSites)
}

func callHierarchyLess(left, right CallHierarchy) bool {
	if symbolLess(left.Symbol, right.Symbol) {
		return true
	}
	if symbolLess(right.Symbol, left.Symbol) {
		return false
	}
	for index := 0; index < min(len(left.Calls), len(right.Calls)); index++ {
		if callLess(left.Calls[index], right.Calls[index]) {
			return true
		}
		if callLess(right.Calls[index], left.Calls[index]) {
			return false
		}
	}
	return len(left.Calls) < len(right.Calls)
}

func callHierarchySymbol(item protocol.CallHierarchyItem) Symbol {
	detail := ""
	if item.Detail != nil {
		detail = *item.Detail
	}
	return Symbol{
		Name:     item.Name,
		Kind:     symbolKindName(item.Kind),
		Detail:   detail,
		Location: locationFromRange(item.URI, item.SelectionRange),
	}
}

// symbolKindNames follows LSP's contiguous SymbolKind values, 1 through 26.
// Slot zero and values beyond the table map to unknown.
var symbolKindNames = [...]string{
	"unknown", "file", "module", "namespace", "package", "class", "method",
	"property", "field", "constructor", "enum", "interface", "function", "variable",
	"constant", "string", "number", "boolean", "array", "object", "key", "null",
	"enum_member", "struct", "event", "operator", "type_parameter",
}

func symbolKindName(kind protocol.SymbolKind) string {
	if kind >= protocol.SymbolKind(len(symbolKindNames)) {
		return symbolKindNames[0]
	}
	return symbolKindNames[kind]
}

// signatureHelpFromProtocol converts a signature help result into the shape
// a coding agent reads. A server with nothing to say about the call site
// answers null, which is an empty result rather than a failure.
func signatureHelpFromProtocol(result *protocol.SignatureHelp) (SignatureHelp, error) {
	if result == nil || len(result.Signatures) == 0 {
		return SignatureHelp{}, nil
	}

	active := activeSignatureIndex(result)
	help := SignatureHelp{
		Signatures:      make([]Signature, len(result.Signatures)),
		ActiveSignature: active,
		ActiveParameter: activeParameterIndex(result, active),
	}
	for i, signature := range result.Signatures {
		labels, err := parameterLabels(signature)
		if err != nil {
			return SignatureHelp{}, err
		}
		help.Signatures[i] = Signature{
			Label:         signature.Label,
			Documentation: textFromTooltip(signature.Documentation),
			Parameters:    labels,
		}
	}
	return help, nil
}

// activeSignatureIndex reads which signature the server means. The spec
// defaults an omitted index, and one past the end of the server's own list,
// to the first signature.
//
// Precondition: result holds at least one signature, so the index it returns
// is always a valid one for callers to index Signatures with.
func activeSignatureIndex(result *protocol.SignatureHelp) int {
	if result.ActiveSignature == nil {
		return 0
	}
	index := int(*result.ActiveSignature)
	if index < 0 || index >= len(result.Signatures) {
		return 0
	}
	return index
}

// activeParameterIndex reads which parameter of the active signature the
// cursor is on. Since LSP 3.16 the active signature carries its own
// activeParameter, which takes precedence over the whole result's. Either
// one defaults to the first parameter when it is absent, and when it names a
// parameter the active signature does not hold.
//
// A server may also send an explicit null, meaning no parameter is active at
// all. That answer is only legal for a client that advertises
// noActiveParameterSupport, which Waythrough does not, so it reads as absent.
func activeParameterIndex(result *protocol.SignatureHelp, activeSignature int) int {
	signature := result.Signatures[activeSignature]

	index, ok := signature.ActiveParameter.Get()
	if !ok {
		index, ok = result.ActiveParameter.Get()
	}
	if !ok || int(index) >= len(signature.Parameters) {
		return 0
	}
	return int(index)
}

// parameterLabels reads each parameter's label. handshake() advertises no
// textDocument.signatureHelp capabilities, so a spec-compliant server must
// answer with a label string, not with a pair of offsets into the signature
// label.
func parameterLabels(signature protocol.SignatureInformation) ([]string, error) {
	labels := make([]string, len(signature.Parameters))
	for i, parameter := range signature.Parameters {
		label, ok := parameter.Label.(protocol.String)
		if !ok {
			return nil, fmt.Errorf(
				"signatureHelp for %q: unsupported parameter label shape %T "+
					"(labelOffsetSupport was not advertised)",
				signature.Label, parameter.Label)
		}
		labels[i] = string(label)
	}
	return labels, nil
}

// diagnosticsFromReport converts a pull diagnostic report into Diagnostics.
// It handles only the full report. An unchanged report says "the same as the
// report you already hold", which a server may only send in answer to a
// previousResultId, and Diagnostics never sends one.
func diagnosticsFromReport(
	requestedPath string, report protocol.DocumentDiagnosticReport,
) ([]Diagnostic, error) {
	full, ok := report.(*protocol.RelatedFullDocumentDiagnosticReport)
	if !ok {
		return nil, fmt.Errorf(
			"diagnostic for %s: unsupported report shape %T (no previousResultId was sent)",
			requestedPath, report)
	}

	// full.RelatedDocuments holds diagnostics for other files this one
	// affects. A caller asked about one file, so it gets one file's answer.
	diagnostics := make([]Diagnostic, len(full.Items))
	for i, item := range full.Items {
		source, _ := item.Source.Get()
		diagnostics[i] = Diagnostic{
			StartLine:   int(item.Range.Start.Line) + 1,
			StartColumn: int(item.Range.Start.Character) + 1,
			EndLine:     int(item.Range.End.Line) + 1,
			EndColumn:   int(item.Range.End.Character) + 1,
			Severity:    severityName(item.Severity),
			Message:     textFromTooltip(item.Message),
			Source:      source,
			Code:        codeText(item.Code),
		}
	}
	return diagnostics, nil
}

// severityName gives a severity the name a reader knows it by, rather than
// the number the protocol sends. A severity the server left out has no name.
func severityName(severity protocol.DiagnosticSeverity) string {
	switch severity {
	case protocol.DiagnosticSeverityError:
		return "error"
	case protocol.DiagnosticSeverityWarning:
		return "warning"
	case protocol.DiagnosticSeverityInformation:
		return "information"
	case protocol.DiagnosticSeverityHint:
		return "hint"
	default:
		return ""
	}
}

// codeText flattens a diagnostic's code, which the protocol allows as either
// a number or a string, into the text a coding agent reads. An absent code
// flattens to the empty string.
func codeText(code protocol.ProgressToken) string {
	switch v := code.(type) {
	case protocol.String:
		return string(v)
	case protocol.Integer:
		return strconv.Itoa(int(v))
	default:
		return ""
	}
}

// textFromTooltip flattens a field the protocol allows as either a plain
// string or MarkupContent — a signature's documentation, a diagnostic's
// message — into the text a coding agent reads. An absent field flattens to
// the empty string.
func textFromTooltip(tooltip protocol.InlayHintTooltip) string {
	switch v := tooltip.(type) {
	case protocol.String:
		return string(v)
	case *protocol.MarkupContent:
		if v == nil {
			return ""
		}
		return v.Value
	default:
		return ""
	}
}

// editsFromWorkspaceEdit converts a rename result into Edits, sorted by
// file then position so callers see a deterministic order regardless of
// Go's randomized map iteration over WorkspaceEdit.Changes. It handles only
// Changes: handshake() advertises no workspace.workspaceEdit capabilities,
// so a spec-compliant server must answer with Changes, not DocumentChanges.
func editsFromWorkspaceEdit(result *protocol.WorkspaceEdit) ([]Edit, error) {
	if result == nil {
		return nil, nil
	}
	if len(result.DocumentChanges) > 0 {
		return nil, errors.New(
			"rename: unsupported result shape: documentChanges " +
				"(workspace.workspaceEdit.documentChanges was not advertised)")
	}

	var edits []Edit
	for docURI, textEdits := range result.Changes {
		path := filePathFromURI(docURI)
		for _, te := range textEdits {
			edits = append(edits, Edit{
				File:        path,
				StartLine:   int(te.Range.Start.Line) + 1,
				StartColumn: int(te.Range.Start.Character) + 1,
				EndLine:     int(te.Range.End.Line) + 1,
				EndColumn:   int(te.Range.End.Character) + 1,
				NewText:     te.NewText,
			})
		}
	}
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].File != edits[j].File {
			return edits[i].File < edits[j].File
		}
		if edits[i].StartLine != edits[j].StartLine {
			return edits[i].StartLine < edits[j].StartLine
		}
		return edits[i].StartColumn < edits[j].StartColumn
	})
	return edits, nil
}

func filePathFromURI(docURI uri.URI) string {
	platform := uri.PlatformPOSIX
	if runtime.GOOS == "windows" {
		platform = uri.PlatformWindows
	}
	return uri.FsPathFor(docURI, platform, false)
}

func locationFromProtocol(loc protocol.Location) Location {
	return locationFromRange(loc.URI, loc.Range)
}

func locationFromRange(docURI uri.URI, sourceRange protocol.Range) Location {
	return Location{
		File:   filePathFromURI(docURI),
		Line:   int(sourceRange.Start.Line) + 1,
		Column: int(sourceRange.Start.Character) + 1,
	}
}

// serverNamed finds the configured server called name. A name that matches
// none of them comes back with the names that would have matched: a coding
// agent picks a server by name, so the error it reads has to leave it able
// to make the call work, rather than guess again.
func (m *Manager) serverNamed(name string) (*serverProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	proc, ok := m.procs[name]
	if ok {
		return proc, nil
	}
	if len(m.procs) == 0 {
		return nil, fmt.Errorf("no language server named %q: none is configured", name)
	}

	configured := make([]string, 0, len(m.procs))
	for configuredName := range m.procs {
		configured = append(configured, configuredName)
	}
	sort.Strings(configured)
	return nil, fmt.Errorf("no language server named %q; configured servers: %s",
		name, strings.Join(configured, ", "))
}

// Shutdown sends `shutdown` and `exit` to every running server and waits
// for its process to exit, killing it after shutdownGrace if it does not.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	procs := make([]*serverProcess, 0, len(m.procs))
	for _, proc := range m.procs {
		procs = append(procs, proc)
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, proc := range procs {
		proc := proc
		wg.Add(1)
		go func() {
			defer wg.Done()
			proc.shutdown(ctx, m.shutdownGrace)
		}()
	}
	wg.Wait()
	return nil
}

// serverProcess is one managed language-server subprocess, across however
// many spawn attempts it takes: its current OS process, its LSP session,
// and its readiness state.
//
// restartCh and stoppingCh outlive every attempt and are never reassigned,
// so any goroutine may read them without the lock. Every other field here
// belongs to one attempt, and beginAttempt replaces it.
type serverProcess struct {
	entry      config.LanguageServer
	logger     *slog.Logger
	restartCh  chan struct{}
	stoppingCh chan struct{}

	mu sync.Mutex
	// generation counts the attempts this server has made. The process of a
	// retired attempt can still speak while its replacement starts, so
	// every readiness signal carries the generation that produced it and
	// the signals from an older one are dropped.
	generation int
	cmd        *exec.Cmd
	// stderrLog is nil unless debug logging asked for this server's stderr.
	// It belongs to the attempt that spawned it, and only wait may flush it.
	stderrLog    *serverLog
	server       protocol.Server
	capabilities protocol.ServerCapabilities
	status       Status
	readyCh      chan struct{}
	retiredCh    chan struct{}
	exitedCh     chan struct{}
	active       map[string]bool
	everSawToken bool
	retiring     bool
	shuttingDown bool
	supervised   chan struct{}
	openFiles    map[string]openFile
}

// serverAttempt identifies one concrete LSP connection. A generation alone
// cannot prove identity while a replacement is published, and a connection
// alone cannot say whether its retirement has begun.
type serverAttempt struct {
	generation int
	server     protocol.Server
}

// attemptState is what a waiter needs to know about a server at one
// instant: which attempt it is on, how that attempt is doing, and the two
// channels that wake the waiter when either changes.
type attemptState struct {
	generation int
	status     Status
	// readyCh closes when this attempt reaches Ready or Failed, and when it
	// is retired while still starting.
	readyCh chan struct{}
	// retiredCh closes when a later attempt begins, which is the only wake
	// a caller gets when the attempt it is waiting past has already settled.
	retiredCh chan struct{}
}

// openFile is what the server was last told about a document Waythrough
// has synced to it, so syncFile can tell didOpen from didChange and bump
// the version LSP requires on every change.
type openFile struct {
	content string
	version int32
}

func (p *serverProcess) currentServer() protocol.Server {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.server
}

// requirePullDiagnostics reports why this server cannot be asked for a
// file's diagnostics, and nothing when it can.
//
// It reads the status in the same lock hold as the capability, because a
// restart clears what the retired attempt advertised: a server between two
// attempts has said nothing yet about what it supports. Reading that
// silence as a refusal would tell a coding agent this server can never
// answer for diagnostics, and an agent told that stops asking.
func (p *serverProcess) requirePullDiagnostics(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.status != StatusReady {
		return fmt.Errorf("language server %q restarted while this call was in flight", name)
	}
	if p.capabilities.DiagnosticProvider == nil {
		return fmt.Errorf("language server %q does not support pull diagnostics", name)
	}
	return nil
}

// callHierarchyAttempt captures the connection in the same lock hold that
// validates its status and capability. Every later hierarchy step validates
// this token again, so retirement cannot turn a stale response into success.
func (p *serverProcess) callHierarchyAttempt(name string) (serverAttempt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.status != StatusReady || p.retiring || p.server == nil {
		return serverAttempt{}, fmt.Errorf(
			"language server %q restarted while this call was in flight", name)
	}

	supported := false
	switch provider := p.capabilities.CallHierarchyProvider.(type) {
	case protocol.Boolean:
		supported = bool(provider)
	case *protocol.CallHierarchyOptions:
		supported = provider != nil
	case *protocol.CallHierarchyRegistrationOptions:
		supported = provider != nil
	}
	if !supported {
		return serverAttempt{}, fmt.Errorf(
			"language server %q does not support call hierarchy", name)
	}
	return serverAttempt{generation: p.generation, server: p.server}, nil
}

func (p *serverProcess) validateCallHierarchyAttempt(
	name string, attempt serverAttempt,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.attemptIsCurrent(attempt) {
		return fmt.Errorf("language server %q restarted while this call was in flight", name)
	}
	return nil
}

// attemptIsCurrent is called only while p.mu is held.
func (p *serverProcess) attemptIsCurrent(attempt serverAttempt) bool {
	return p.status == StatusReady && !p.retiring &&
		p.generation == attempt.generation && p.server == attempt.server
}

// syncFile tells the language server about path's current on-disk content:
// didOpen the first time, didChange whenever the content has moved on since
// the last sync. A tool call always reads disk fresh, so a coding agent's
// unsaved edits are invisible to Waythrough by design.
func (p *serverProcess) syncFile(ctx context.Context, path string) error {
	p.mu.Lock()
	if p.status != StatusReady || p.retiring || p.server == nil {
		p.mu.Unlock()
		return fmt.Errorf("language server restarted while syncing %s", path)
	}
	attempt := serverAttempt{generation: p.generation, server: p.server}
	p.mu.Unlock()
	return p.syncFileOnAttempt(ctx, path, attempt)
}

func (p *serverProcess) syncFileOnAttempt(
	ctx context.Context, path string, attempt serverAttempt,
) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	text := string(content)

	ext := filepath.Ext(path)
	languageID, ok := p.entry.Filetypes[ext]
	if !ok {
		return fmt.Errorf("no languageId configured for extension %q", ext)
	}

	p.mu.Lock()
	if !p.attemptIsCurrent(attempt) {
		p.mu.Unlock()
		return fmt.Errorf("language server restarted while syncing %s", path)
	}
	prev, wasOpen := p.openFiles[path]
	p.mu.Unlock()

	docURI := uri.File(path)

	// Each notification names itself in its error. The two share a return
	// path but not a cause, so one shared wrap would label a failed didOpen
	// as a didChange, and vice versa.
	if !wasOpen {
		err = attempt.server.DidOpen(ctx, &protocol.DidOpenTextDocumentParams{
			TextDocument: protocol.TextDocumentItem{
				URI:        docURI,
				LanguageID: protocol.LanguageKind(languageID),
				Version:    1,
				Text:       text,
			},
		})
		if err != nil {
			return fmt.Errorf("didOpen: %w", err)
		}
		prev = openFile{content: text, version: 1}
	} else if prev.content != text {
		prev.version++
		prev.content = text
		err = attempt.server.DidChange(ctx, &protocol.DidChangeTextDocumentParams{
			TextDocument: protocol.VersionedTextDocumentIdentifier{
				TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: docURI},
				Version:                prev.version,
			},
			ContentChanges: []protocol.TextDocumentContentChangeEvent{
				&protocol.TextDocumentContentChangeWholeDocument{Text: text},
			},
		})
		if err != nil {
			return fmt.Errorf("didChange: %w", err)
		}
	}

	p.mu.Lock()
	if !p.attemptIsCurrent(attempt) {
		p.mu.Unlock()
		return fmt.Errorf("language server restarted while syncing %s", path)
	}
	if p.openFiles == nil {
		p.openFiles = make(map[string]openFile)
	}
	p.openFiles[path] = prev
	p.mu.Unlock()

	return nil
}

// beginSupervision reserves this server for one supervisor goroutine. It
// reports false when a supervisor already owns it, so no two goroutines can
// ever call exec.Cmd.Wait on the same process.
func (p *serverProcess) beginSupervision() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.supervised != nil || p.shuttingDown {
		return false
	}
	p.supervised = make(chan struct{})
	return true
}

// endSupervision reports that the supervisor goroutine has returned, which
// is what a shutdown waits for before it calls this server stopped.
func (p *serverProcess) endSupervision() {
	p.mu.Lock()
	supervised := p.supervised
	p.mu.Unlock()

	// runServer runs only once beginSupervision has reserved this server,
	// so the channel a shutdown may be waiting on always exists here.
	close(supervised)
}

// requestRestart asks the supervisor for a fresh attempt that does not
// spend the crash budget. The channel holds one request: a second request
// that arrives before the supervisor takes the first asks for the same
// thing, so dropping it costs nothing. The same lock hold marks the current
// attempt retiring, so no in-flight operation can accept a later response in
// the gap between queuing the restart and sending shutdown.
func (p *serverProcess) requestRestart(generation int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case p.restartCh <- struct{}{}:
	default:
	}
	if generation == p.generation && p.cmd != nil {
		p.retiring = true
	}
}

// takeRestartRequest reports whether a restart was asked for, and consumes
// the request when it was.
func (p *serverProcess) takeRestartRequest() bool {
	select {
	case <-p.restartCh:
		return true
	default:
		return false
	}
}

// awaitRestart parks the supervisor of a server that spent its crash
// budget. It reports true when a restart was asked for, and false when the
// server is shutting down or ctx ended, in which case the supervisor
// returns and this server is over.
func (p *serverProcess) awaitRestart(ctx context.Context) bool {
	select {
	case <-p.restartCh:
		return true
	case <-p.stoppingCh:
		return false
	case <-ctx.Done():
		return false
	}
}

// beginAttempt starts a new attempt and clears the state the previous one
// left behind, so a server that failed once can become ready again, and so
// the replacement is told about a document from scratch rather than sent a
// change on top of a version only the retired session ever held.
//
// It reports false once a shutdown has begun. That read and the decision to
// spawn share this one lock hold, and shutdown sets the flag in the same
// hold that reads the process to stop, so no attempt can slip between the
// two and leave a subprocess nobody stops.
//
// It closes the outgoing readyCh, when the attempt it belonged to never
// reached Ready or Failed, and always closes the outgoing retiredCh, so
// every waiter wakes and moves on to this attempt rather than waiting on a
// channel this server has abandoned.
func (p *serverProcess) beginAttempt() (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.shuttingDown {
		return 0, false
	}

	if p.status == StatusStarting {
		close(p.readyCh)
	}
	close(p.retiredCh)

	p.generation++
	p.cmd = nil
	p.stderrLog = nil
	p.readyCh = make(chan struct{})
	p.retiredCh = make(chan struct{})
	p.exitedCh = make(chan struct{})
	p.active = nil
	p.everSawToken = false
	p.retiring = false
	p.openFiles = nil
	p.capabilities = protocol.ServerCapabilities{}
	p.status = StatusStarting

	return p.generation, true
}

// startProcess spawns this attempt's process and publishes it as the
// process this server currently owns. Until it does, p.cmd is nil, so a
// spawn that fails cannot leave a caller waiting on the exit of a process
// from an attempt that is already over.
//
// A shutdown that begins while the spawn is in flight looked for a process
// this attempt had not published yet, so this attempt ends its own child
// rather than let it outlive the manager. runServer skips proc.wait on an
// error, which leaves the Wait below as the one Wait this process gets, and
// Go allows exactly one.
func (p *serverProcess) startProcess(ctx context.Context, generation int) error {
	cmd := exec.CommandContext(ctx, p.entry.Command, p.entry.Args...)

	// A language server explains a bad start on its own stderr, and that is
	// the one account of it Waythrough can offer. Capturing it costs a
	// record per line, so only a debug logger opts in; every other run
	// discards the stream as before.
	var stderrLog *serverLog
	cmd.Stderr = io.Discard
	if p.logger.Enabled(ctx, slog.LevelDebug) {
		stderrLog = newServerLog(p.logger, p.entry.Name)
		cmd.Stderr = stderrLog
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe for %s: %w", p.entry.Command, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe for %s: %w", p.entry.Command, err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", p.entry.Command, err)
	}

	stream := newLSPStream(pipeRWC{ReadCloser: stdout, WriteCloser: stdin})
	_, _, server := protocol.NewClient(ctx, &client{proc: p, generation: generation}, stream)

	p.mu.Lock()
	stopping := p.shuttingDown
	if !stopping {
		p.cmd = cmd
		p.stderrLog = stderrLog
		p.server = server
	}
	p.mu.Unlock()

	if !stopping {
		return nil
	}

	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	// This attempt never published its process, so wait will not run for it
	// and this is the only place its stderr can be flushed.
	if stderrLog != nil {
		stderrLog.flush()
	}
	return fmt.Errorf("start %s: %w", p.entry.Command, errShutdownBegan)
}

// errShutdownBegan reports a spawn abandoned because the manager is
// stopping. It is not a fault of the language server, and no restart
// follows it: the supervisor returns instead.
var errShutdownBegan = errors.New("shutdown began before the language server could start")

func (p *serverProcess) handshake(ctx context.Context, root string) error {
	p.mu.Lock()
	server := p.server
	p.mu.Unlock()

	rootURI := uri.File(root)
	workDoneProgress := true
	result, err := server.Initialize(ctx, &protocol.InitializeParams{
		RootURI: &rootURI,
		Capabilities: protocol.ClientCapabilities{
			Window: &protocol.WindowClientCapabilities{WorkDoneProgress: &workDoneProgress},
			TextDocument: &protocol.TextDocumentClientCapabilities{
				CallHierarchy: &protocol.CallHierarchyClientCapabilities{},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}

	// This response is the only time the server states what it can do, so
	// keep it: a request for a feature the server never advertised has to
	// fail here rather than travel to a server that will not answer it.
	p.mu.Lock()
	p.capabilities = result.Capabilities
	p.mu.Unlock()

	if err := server.Initialized(ctx, &protocol.InitializedParams{}); err != nil {
		return fmt.Errorf("initialized: %w", err)
	}
	return nil
}

// negotiateReadiness decides, after the handshake, how this server becomes
// ready. handshake mode is ready immediately. progress mode waits for
// debounce to learn whether the server opens a WorkDoneProgress token at
// all; token open/close tracking in tokenCreated/tokenClosed handles
// marking it ready once real progress reporting is seen.
func (p *serverProcess) negotiateReadiness(
	ctx context.Context, debounce time.Duration, generation int,
) {
	if p.entry.Readiness == config.ReadinessHandshake {
		p.markReady(generation)
		return
	}

	timer := time.NewTimer(debounce)
	defer timer.Stop()

	select {
	case <-timer.C:
		p.mu.Lock()
		saw := p.everSawToken
		p.mu.Unlock()
		if !saw {
			p.markReady(generation)
		}
	case <-ctx.Done():
	}
}

func (p *serverProcess) tokenCreated(generation int, key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if generation != p.generation {
		return
	}
	if p.active == nil {
		p.active = make(map[string]bool)
	}
	p.active[key] = true
	p.everSawToken = true
}

func (p *serverProcess) tokenClosed(generation int, key string) {
	p.mu.Lock()
	stale := generation != p.generation
	if !stale {
		delete(p.active, key)
	}
	empty := len(p.active) == 0
	p.mu.Unlock()

	if stale {
		return
	}
	if empty {
		p.markReady(generation)
	}
}

// markReady, markFailed, tokenCreated and tokenClosed each take the attempt
// that is speaking, and do nothing for an attempt this server has already
// replaced. A graceful stop leaves the retired process able to speak while
// its replacement starts, so without that check a signal from the process a
// restart just ended could report the replacement ready before the
// replacement had indexed anything.
func (p *serverProcess) markReady(generation int) {
	p.mu.Lock()
	stale := generation != p.generation || p.status != StatusStarting
	if !stale {
		p.status = StatusReady
		close(p.readyCh)
	}
	p.mu.Unlock()

	// Logging outside the lock hold keeps a debug handler's write to stderr
	// off the path every waiter and every tool call takes through p.mu.
	if stale {
		return
	}
	p.logger.Debug("language server ready",
		slog.String("server", p.entry.Name),
		slog.Int("attempt", generation))
}

func (p *serverProcess) markFailed(generation int) {
	p.mu.Lock()
	stale := generation != p.generation || p.status == StatusFailed
	if !stale {
		wasStarting := p.status == StatusStarting
		p.status = StatusFailed
		if wasStarting {
			close(p.readyCh)
		}
	}
	p.mu.Unlock()

	if stale {
		return
	}
	// A server that has spent its crash budget answers nothing until
	// something restarts it, so this is a warning rather than a trace.
	p.logger.Warn("language server gave up after repeated exits",
		slog.String("server", p.entry.Name),
		slog.Int("attempt", generation))
}

func (p *serverProcess) snapshot() attemptState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return attemptState{
		generation: p.generation,
		status:     p.status,
		readyCh:    p.readyCh,
		retiredCh:  p.retiredCh,
	}
}

// wait blocks until the current process attempt exits. Only runServer's
// loop calls this: exec.Cmd.Wait may only be called once per process.
func (p *serverProcess) wait() {
	p.mu.Lock()
	cmd := p.cmd
	stderrLog := p.stderrLog
	exited := p.exitedCh
	p.mu.Unlock()
	if cmd == nil {
		return
	}
	_ = cmd.Wait()

	// Wait returns only once the goroutine copying this process's stderr
	// has finished, so this flush is the sole remaining writer and the
	// last partial line the server wrote is safe to read here.
	if stderrLog != nil {
		stderrLog.flush()
	}
	close(exited)
}

func (p *serverProcess) shutdownRequested() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.shuttingDown
}

// shutdown ends this server for good: no later attempt may spawn, the
// attempt running now is stopped, and shutdown returns only once the
// supervisor goroutine has returned. That last wait is what makes the
// promise whole, since a spawn already in flight ends its own child and the
// supervisor returns after it.
func (p *serverProcess) shutdown(ctx context.Context, killGrace time.Duration) {
	p.mu.Lock()
	firstCall := !p.shuttingDown
	p.shuttingDown = true
	generation := p.generation
	supervised := p.supervised
	if firstCall {
		close(p.stoppingCh)
	}
	p.mu.Unlock()

	p.stopAttempt(ctx, killGrace, generation)

	if supervised == nil {
		return
	}
	<-supervised
}

// stopAttempt ends the process of one attempt: `shutdown` and `exit` first,
// then a kill once killGrace has elapsed. It does nothing for an attempt
// this server has already replaced, so a restart cannot stop the very
// replacement it asked for, and nothing for an attempt that never spawned a
// process, so a failed spawn cannot leave this waiting on an exit that will
// never come.
func (p *serverProcess) stopAttempt(
	ctx context.Context, killGrace time.Duration, generation int,
) {
	p.mu.Lock()
	if generation != p.generation {
		p.mu.Unlock()
		return
	}
	cmd := p.cmd
	if cmd == nil {
		p.mu.Unlock()
		return
	}
	p.retiring = true
	server := p.server
	exited := p.exitedCh
	p.mu.Unlock()

	select {
	case <-exited:
		return
	default:
	}

	if server != nil {
		_ = server.Shutdown(ctx)
		_ = server.Exit(ctx)
	}

	select {
	case <-exited:
	case <-time.After(killGrace):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		// The kill closes the process's pipes, so whatever request the
		// supervisor still has in flight fails and it reaches proc.wait,
		// which is the one writer that closes this channel.
		<-exited
	}
}
