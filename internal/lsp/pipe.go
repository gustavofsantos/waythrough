package lsp

import (
	"fmt"
	"io"
)

// pipeRWC adapts a subprocess's separate stdin and stdout pipes into the
// single io.ReadWriteCloser the LSP transport expects.
type pipeRWC struct {
	io.ReadCloser
	io.WriteCloser
}

// Close closes both pipes, always, and reports the first failure. Each side
// names itself: the two closes fail for different reasons, and an
// unqualified pipe error cannot say which half of the transport broke.
func (p pipeRWC) Close() error {
	writeErr := p.WriteCloser.Close()
	readErr := p.ReadCloser.Close()
	if writeErr != nil {
		return fmt.Errorf("close stdin pipe: %w", writeErr)
	}
	if readErr != nil {
		return fmt.Errorf("close stdout pipe: %w", readErr)
	}
	return nil
}
