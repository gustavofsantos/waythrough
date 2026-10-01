package lsp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"go.lsp.dev/protocol"

	"github.com/gustavofsantos/waythrough/internal/config"
)

type blockingDidOpenServer struct {
	protocol.Server
	started chan<- struct{}
	release <-chan struct{}
}

func (s *blockingDidOpenServer) DidOpen(
	context.Context, *protocol.DidOpenTextDocumentParams,
) error {
	if s.started != nil {
		close(s.started)
	}
	if s.release != nil {
		<-s.release
	}
	return nil
}

func TestSyncFileDoesNotCommitAcrossRestart(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.fake")
	if err := os.WriteFile(path, []byte("target()"), 0o644); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	first := &blockingDidOpenServer{started: started, release: release}
	second := &blockingDidOpenServer{}
	proc := &serverProcess{
		entry:      config.LanguageServer{Filetypes: map[string]string{".fake": "fake"}},
		generation: 1,
		server:     first,
		status:     StatusReady,
		openFiles:  make(map[string]openFile),
		syncSlot:   make(chan struct{}, 1),
	}

	result := make(chan error, 1)
	go func() { result <- proc.syncFile(context.Background(), path) }()
	<-started

	proc.mu.Lock()
	proc.generation = 2
	proc.server = second
	proc.openFiles = make(map[string]openFile)
	proc.syncSlot = make(chan struct{}, 1)
	proc.mu.Unlock()
	close(release)

	if err := <-result; err == nil {
		t.Fatal("syncFile succeeded after its language-server attempt was replaced")
	}
	proc.mu.Lock()
	_, poisoned := proc.openFiles[path]
	proc.mu.Unlock()
	if poisoned {
		t.Fatal("retired attempt populated the replacement attempt's open-file state")
	}
}

// recordingSyncServer records every document notification in arrival order.
// Its first didOpen can block until release closes, which holds a sync in
// flight while a second sync of the same file starts.
type recordingSyncServer struct {
	protocol.Server
	firstOpenStarted chan<- struct{}
	release          <-chan struct{}

	mu            sync.Mutex
	notifications []string
}

func (s *recordingSyncServer) record(notification string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifications = append(s.notifications, notification)
	return len(s.notifications)
}

func (s *recordingSyncServer) DidOpen(
	_ context.Context, params *protocol.DidOpenTextDocumentParams,
) error {
	document := params.TextDocument
	count := s.record(fmt.Sprintf("didOpen v%d %s", document.Version, document.Text))
	if count == 1 && s.firstOpenStarted != nil {
		close(s.firstOpenStarted)
		<-s.release
	}
	return nil
}

func (s *recordingSyncServer) DidChange(
	_ context.Context, params *protocol.DidChangeTextDocumentParams,
) error {
	whole, ok := params.ContentChanges[0].(*protocol.TextDocumentContentChangeWholeDocument)
	if !ok {
		return fmt.Errorf("unexpected change shape %T", params.ContentChanges[0])
	}
	s.record(fmt.Sprintf("didChange v%d %s", params.TextDocument.Version, whole.Text))
	return nil
}

func readyProcessFor(server protocol.Server) *serverProcess {
	return &serverProcess{
		entry:      config.LanguageServer{Filetypes: map[string]string{".fake": "fake"}},
		generation: 1,
		server:     server,
		status:     StatusReady,
		syncSlot:   make(chan struct{}, 1),
	}
}

// Two tool calls, possibly from two agent sessions, sync one file at once.
// The second must wait for the first: otherwise both see the file as not
// yet open and send didOpen twice, or send one version number with two
// texts, and the server can keep text older than what is on disk.
func TestSyncFileSerializesConcurrentSyncsOfOneFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.fake")
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}

	firstOpenStarted := make(chan struct{})
	release := make(chan struct{})
	server := &recordingSyncServer{firstOpenStarted: firstOpenStarted, release: release}
	proc := readyProcessFor(server)

	firstResult := make(chan error, 1)
	go func() { firstResult <- proc.syncFile(context.Background(), path) }()
	<-firstOpenStarted

	// The file moves on while the first sync is still in flight, so the
	// second sync must read it only after the first one has finished.
	if err := os.WriteFile(path, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	secondResult := make(chan error, 1)
	go func() { secondResult <- proc.syncFile(context.Background(), path) }()

	// Give an unserialized second sync time to reach the server before the
	// first one is released. A correct one is still waiting here.
	time.Sleep(50 * time.Millisecond)
	close(release)

	for _, result := range []chan error{firstResult, secondResult} {
		if err := <-result; err != nil {
			t.Fatalf("syncFile: %v", err)
		}
	}

	want := []string{"didOpen v1 first", "didChange v2 second"}
	server.mu.Lock()
	got := append([]string(nil), server.notifications...)
	server.mu.Unlock()
	if !slices.Equal(got, want) {
		t.Fatalf("notifications = %q, want %q", got, want)
	}

	proc.mu.Lock()
	state := proc.openFiles[path]
	proc.mu.Unlock()
	if state != (openFile{content: "second", version: 2}) {
		t.Fatalf("open-file state = %+v, want the newest text at version 2", state)
	}
}

// A server can stop reading its stdin, and a notification write ignores its
// context. A sync queued behind that write must still return when its own
// caller gives up, rather than hang every later tool call.
func TestSyncFileWaitHonorsContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.fake")
	if err := os.WriteFile(path, []byte("text"), 0o644); err != nil {
		t.Fatal(err)
	}

	firstOpenStarted := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	proc := readyProcessFor(
		&recordingSyncServer{firstOpenStarted: firstOpenStarted, release: release})

	go func() { _ = proc.syncFile(context.Background(), path) }()
	<-firstOpenStarted

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- proc.syncFile(ctx, path) }()

	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("syncFile error = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("syncFile queued behind a hung server ignored its context")
	}
}
