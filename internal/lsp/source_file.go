package lsp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

const maxSyncedFileBytes = 16 << 20

func readSourceFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read source file: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat source file: %w", err)
	}
	if err := validateSourceFile(info); err != nil {
		return nil, err
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open source file: %w", err)
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat open source file: %w", err)
	}
	if err := validateSourceFile(openedInfo); err != nil {
		_ = file.Close()
		return nil, err
	}

	content, readErr := io.ReadAll(io.LimitReader(
		contextReader{ctx: ctx, reader: file}, maxSyncedFileBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read source file: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close source file: %w", closeErr)
	}
	if len(content) > maxSyncedFileBytes {
		return nil, fmt.Errorf(
			"source file has more than %d bytes; maximum is %d",
			maxSyncedFileBytes, maxSyncedFileBytes)
	}
	return content, nil
}

func validateSourceFile(info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source path must be a regular file, got %s", info.Mode().Type())
	}
	if info.Size() > maxSyncedFileBytes {
		return fmt.Errorf(
			"source file has %d bytes; maximum is %d",
			info.Size(), maxSyncedFileBytes)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(destination []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, fmt.Errorf("read source file: %w", err)
	}
	count, err := r.reader.Read(destination)
	if errors.Is(err, io.EOF) {
		return count, io.EOF
	}
	if err != nil {
		return count, fmt.Errorf("read source file: %w", err)
	}
	return count, nil
}
