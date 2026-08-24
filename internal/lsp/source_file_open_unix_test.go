//go:build aix || android || darwin || dragonfly || freebsd || hurd || illumos || ios || linux || netbsd || openbsd || solaris

package lsp

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestOpenSourceFileDoesNotBlockOnFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("create fifo: %v", err)
	}

	result := make(chan *os.File, 1)
	errors := make(chan error, 1)
	go func() {
		file, err := openSourceFile(path)
		if err != nil {
			errors <- err
			return
		}
		result <- file
	}()

	select {
	case err := <-errors:
		t.Fatalf("open fifo without blocking: %v", err)
	case file := <-result:
		defer func() { _ = file.Close() }()
		info, err := file.Stat()
		if err != nil {
			t.Fatalf("stat fifo: %v", err)
		}
		if err := validateSourceFile(info); err == nil {
			t.Fatal("fifo passed regular-file validation")
		}
	case <-time.After(time.Second):
		t.Fatal("opening a fifo blocked past the operation bound")
	}
}
