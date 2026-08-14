// Package lsp starts and tracks the language-server subprocesses Waythrough
// manages, and gates readiness on each server's own signal that it can
// answer a request, not merely that its process has started.
package lsp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"go.lsp.dev/jsonrpc2"
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
)

// Status is a language server's place in its own lifecycle.
type Status int

const (
	// StatusStarting covers everything from process spawn through the
	// readiness gate: handshaking, and indexing when readiness is progress.
	StatusStarting Status = iota
	// StatusReady means the server can answer a request.
	StatusReady
	// StatusFailed means the server crashed more times than the restart
	// limit allows within the restart window, and Waythrough has stopped
	// restarting it.
	StatusFailed
)

// Manager starts a subprocess for each configured language server and
// tracks when each one is ready to answer a request.
type Manager struct {
	root             string
	progressDebounce time.Duration
	restartLimit     int
	restartWindow    time.Duration
	shutdownGrace    time.Duration
	toolCallTimeout  time.Duration

	mu    sync.Mutex
	procs map[string]*serverProcess
}

// Location is a position in a file, 1-based to match how a coding agent
// reads line and column numbers.
type Location struct {
	File   string
	Line   int
	Column int
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
func WithRestartLimit(max int, window time.Duration) Option {
	return func(m *Manager) {
		m.restartLimit = max
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

// NewManager builds a Manager for entries. root is the project directory
// passed to each language server as its workspace root.
func NewManager(root string, entries []config.LanguageServer, opts ...Option) *Manager {
	m := &Manager{
		root:             root,
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
		m.procs[entry.Name] = &serverProcess{
			entry:    entry,
			readyCh:  make(chan struct{}),
			exitedCh: make(chan struct{}),
		}
	}
	return m
}

// Start spawns a subprocess for every configured entry and begins the
// initialize handshake and readiness tracking for each. It returns once
// every subprocess has been launched, not once every server is ready — use
// WaitReady for that. A server that exits unexpectedly is restarted; one
// that exits too often is not — see WithRestartLimit.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, proc := range m.procs {
		proc := proc
		go m.runServer(ctx, proc)
	}
	return nil
}

// runServer owns proc's process for its whole lifetime: spawn, handshake,
// wait for exit, and either restart or give up, until Shutdown is called or
// ctx is done. It is the only caller of proc.wait, so it is the only
// goroutine that calls exec.Cmd.Wait for this process, which Go allows only
// once per process.
func (m *Manager) runServer(ctx context.Context, proc *serverProcess) {
	var exits []time.Time

	for attempt := 0; ; attempt++ {
		// NewManager already gave proc its first readyCh/exitedCh, so a
		// WaitReady call that snapshots them before this goroutine is even
		// scheduled still observes the pair this attempt actually uses.
		// Only a restart (attempt > 0) needs a fresh pair.
		if attempt > 0 {
			proc.beginAttempt()
		}

		if err := proc.startProcess(ctx); err == nil {
			if err := proc.handshake(ctx, m.root); err == nil {
				go proc.negotiateReadiness(ctx, m.progressDebounce)
			}
			proc.wait()
		}

		if proc.shutdownRequested() {
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}

		exits = append(exits, time.Now())
		exits = withinWindow(exits, m.restartWindow)

		if len(exits) > m.restartLimit {
			proc.markFailed()
			return
		}
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
// context is done, or timeout elapses, whichever comes first. A restart
// between two attempts closes the old attempt's channel to wake WaitReady
// rather than leaving it waiting on a channel no one will ever close again,
// so WaitReady loops: each wake re-checks status and, if the server merely
// moved on to a new attempt, waits on that attempt's channel instead.
func (m *Manager) WaitReady(ctx context.Context, name string, timeout time.Duration) error {
	proc, err := m.serverNamed(name)
	if err != nil {
		return err
	}

	deadline := time.Now().Add(timeout)

	for {
		ch, status := proc.snapshot()
		switch status {
		case StatusReady:
			return nil
		case StatusFailed:
			return fmt.Errorf("language server %q failed to start", name)
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("language server %q is still starting", name)
		}

		timer := time.NewTimer(remaining)
		select {
		case <-ch:
			timer.Stop()
		case <-timer.C:
			return fmt.Errorf("language server %q is still starting", name)
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

// Status reports the named server's current lifecycle status.
func (m *Manager) Status(name string) (Status, error) {
	proc, err := m.serverNamed(name)
	if err != nil {
		return 0, err
	}
	_, status := proc.snapshot()
	return status, nil
}

// Definition asks the named server for the definition at a 1-based
// line/column in file, waiting for the server to be ready first and
// syncing the file's current on-disk content to it before asking. file may
// be relative to Manager's root or absolute.
func (m *Manager) Definition(ctx context.Context, name, file string, line, column int) ([]Location, error) {
	proc, err := m.serverNamed(name)
	if err != nil {
		return nil, err
	}

	if err := m.WaitReady(ctx, name, m.toolCallTimeout); err != nil {
		return nil, err
	}

	path := m.resolvePath(file)
	if err := proc.syncFile(ctx, path); err != nil {
		return nil, fmt.Errorf("sync %s: %w", path, err)
	}

	result, err := proc.currentServer().Definition(ctx, &protocol.DefinitionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri.File(path)},
			Position:     protocol.Position{Line: uint32(line - 1), Character: uint32(column - 1)},
		},
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
func (m *Manager) References(ctx context.Context, name, file string, line, column int) ([]Location, error) {
	proc, err := m.serverNamed(name)
	if err != nil {
		return nil, err
	}

	if err := m.WaitReady(ctx, name, m.toolCallTimeout); err != nil {
		return nil, err
	}

	path := m.resolvePath(file)
	if err := proc.syncFile(ctx, path); err != nil {
		return nil, fmt.Errorf("sync %s: %w", path, err)
	}

	protocolLocations, err := proc.currentServer().References(ctx, &protocol.ReferenceParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri.File(path)},
			Position:     protocol.Position{Line: uint32(line - 1), Character: uint32(column - 1)},
		},
		Context: protocol.ReferenceContext{IncludeDeclaration: true},
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

func (m *Manager) resolvePath(file string) string {
	if filepath.IsAbs(file) {
		return file
	}
	return filepath.Join(m.root, file)
}

func locationsFromDefinitionResult(requestedPath string, result protocol.DefinitionResult) ([]Location, error) {
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
		return nil, fmt.Errorf("definition for %s: unsupported result shape %T (linkSupport was not advertised)", requestedPath, result)
	}
}

func locationFromProtocol(loc protocol.Location) Location {
	platform := uri.PlatformPOSIX
	if runtime.GOOS == "windows" {
		platform = uri.PlatformWindows
	}
	return Location{
		File:   uri.FsPathFor(loc.URI, platform, false),
		Line:   int(loc.Range.Start.Line) + 1,
		Column: int(loc.Range.Start.Character) + 1,
	}
}

func (m *Manager) serverNamed(name string) (*serverProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	proc, ok := m.procs[name]
	if !ok {
		return nil, fmt.Errorf("no configured language server named %q", name)
	}
	return proc, nil
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
type serverProcess struct {
	entry config.LanguageServer

	mu           sync.Mutex
	cmd          *exec.Cmd
	server       protocol.Server
	status       Status
	readyCh      chan struct{}
	exitedCh     chan struct{}
	active       map[string]bool
	everSawToken bool
	shuttingDown bool
	openFiles    map[string]openFile
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

// syncFile tells the language server about path's current on-disk content:
// didOpen the first time, didChange whenever the content has moved on since
// the last sync. A tool call always reads disk fresh, so a coding agent's
// unsaved edits are invisible to Waythrough by design.
func (p *serverProcess) syncFile(ctx context.Context, path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(content)

	ext := filepath.Ext(path)
	languageID, ok := p.entry.Filetypes[ext]
	if !ok {
		return fmt.Errorf("no languageId configured for extension %q", ext)
	}

	p.mu.Lock()
	server := p.server
	prev, wasOpen := p.openFiles[path]
	p.mu.Unlock()

	docURI := uri.File(path)

	if !wasOpen {
		err = server.DidOpen(ctx, &protocol.DidOpenTextDocumentParams{
			TextDocument: protocol.TextDocumentItem{
				URI:        docURI,
				LanguageID: protocol.LanguageKind(languageID),
				Version:    1,
				Text:       text,
			},
		})
		prev = openFile{content: text, version: 1}
	} else if prev.content != text {
		prev.version++
		prev.content = text
		err = server.DidChange(ctx, &protocol.DidChangeTextDocumentParams{
			TextDocument: protocol.VersionedTextDocumentIdentifier{
				TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: docURI},
				Version:                prev.version,
			},
			ContentChanges: []protocol.TextDocumentContentChangeEvent{
				&protocol.TextDocumentContentChangeWholeDocument{Text: text},
			},
		})
	}
	if err != nil {
		return err
	}

	p.mu.Lock()
	if p.openFiles == nil {
		p.openFiles = make(map[string]openFile)
	}
	p.openFiles[path] = prev
	p.mu.Unlock()

	return nil
}

// beginAttempt resets per-attempt state at the start of every spawn,
// including restarts, so a server that failed once can become ready again.
// beginAttempt starts a new attempt generation. It closes the outgoing
// readyCh (if the attempt it belonged to never reached Ready or Failed, as
// happens when a server crashes before either) so any WaitReady call still
// waiting on it wakes up and moves on to this attempt's channel, instead of
// hanging on a channel this generation has abandoned.
func (p *serverProcess) beginAttempt() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.status == StatusStarting {
		close(p.readyCh)
	}
	p.readyCh = make(chan struct{})
	p.exitedCh = make(chan struct{})
	p.active = nil
	p.everSawToken = false
	p.openFiles = nil
	p.status = StatusStarting
}

func (p *serverProcess) startProcess(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, p.entry.Command, p.entry.Args...)
	cmd.Stderr = io.Discard

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	stream := jsonrpc2.NewStream(pipeRWC{ReadCloser: stdout, WriteCloser: stdin})
	_, _, server := protocol.NewClient(ctx, &client{proc: p}, stream)

	p.mu.Lock()
	p.cmd = cmd
	p.server = server
	p.mu.Unlock()

	return nil
}

func (p *serverProcess) handshake(ctx context.Context, root string) error {
	p.mu.Lock()
	server := p.server
	p.mu.Unlock()

	rootURI := uri.File(root)
	workDoneProgress := true
	if _, err := server.Initialize(ctx, &protocol.InitializeParams{
		RootURI: &rootURI,
		Capabilities: protocol.ClientCapabilities{
			Window: &protocol.WindowClientCapabilities{WorkDoneProgress: &workDoneProgress},
		},
	}); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}

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
func (p *serverProcess) negotiateReadiness(ctx context.Context, debounce time.Duration) {
	if p.entry.Readiness == config.ReadinessHandshake {
		p.markReady()
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
			p.markReady()
		}
	case <-ctx.Done():
	}
}

func (p *serverProcess) tokenCreated(key string) {
	p.mu.Lock()
	if p.active == nil {
		p.active = make(map[string]bool)
	}
	p.active[key] = true
	p.everSawToken = true
	p.mu.Unlock()
}

func (p *serverProcess) tokenClosed(key string) {
	p.mu.Lock()
	delete(p.active, key)
	empty := len(p.active) == 0
	p.mu.Unlock()

	if empty {
		p.markReady()
	}
}

func (p *serverProcess) markReady() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.status != StatusStarting {
		return
	}
	p.status = StatusReady
	close(p.readyCh)
}

func (p *serverProcess) markFailed() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.status == StatusFailed {
		return
	}
	wasStarting := p.status == StatusStarting
	p.status = StatusFailed
	if wasStarting {
		close(p.readyCh)
	}
}

func (p *serverProcess) snapshot() (chan struct{}, Status) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.readyCh, p.status
}

// wait blocks until the current process attempt exits. Only runServer's
// loop calls this: exec.Cmd.Wait may only be called once per process.
func (p *serverProcess) wait() {
	p.mu.Lock()
	cmd := p.cmd
	exited := p.exitedCh
	p.mu.Unlock()
	if cmd == nil {
		return
	}
	cmd.Wait()
	close(exited)
}

func (p *serverProcess) shutdownRequested() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.shuttingDown
}

func (p *serverProcess) shutdown(ctx context.Context, killGrace time.Duration) {
	p.mu.Lock()
	p.shuttingDown = true
	cmd := p.cmd
	server := p.server
	exited := p.exitedCh
	p.mu.Unlock()

	if cmd == nil {
		return
	}

	if server != nil {
		_ = server.Shutdown(ctx)
		_ = server.Exit(ctx)
	}

	select {
	case <-exited:
		return
	case <-time.After(killGrace):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-exited
	}
}
