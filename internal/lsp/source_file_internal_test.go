package lsp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSourceFileRejectsOversizedFileBeforeReading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.fake")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	if err := file.Truncate(maxSyncedFileBytes + 1); err != nil {
		t.Fatalf("truncate file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	_, err = readSourceFile(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "source file has") {
		t.Fatalf("expected source-size error, got %v", err)
	}
}

func TestReadSourceFileRejectsNonRegularPathWithoutBlocking(t *testing.T) {
	path := t.TempDir()

	_, err := readSourceFile(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("expected regular-file error, got %v", err)
	}
}

func TestReadSourceFileHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := readSourceFile(ctx, filepath.Join(t.TempDir(), "missing.fake"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestReadSourceFileReturnsRegularFileContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.fake")
	if err := os.WriteFile(path, []byte("target()"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	content, err := readSourceFile(context.Background(), path)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if string(content) != "target()" {
		t.Fatalf("content = %q, want target()", content)
	}
}
