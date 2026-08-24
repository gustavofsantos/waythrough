package lsp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
	}

	result := make(chan error, 1)
	go func() { result <- proc.syncFile(context.Background(), path) }()
	<-started

	proc.mu.Lock()
	proc.generation = 2
	proc.server = second
	proc.openFiles = make(map[string]openFile)
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
