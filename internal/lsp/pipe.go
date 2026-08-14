package lsp

import "io"

// pipeRWC adapts a subprocess's separate stdin and stdout pipes into the
// single io.ReadWriteCloser the LSP transport expects.
type pipeRWC struct {
	io.ReadCloser
	io.WriteCloser
}

func (p pipeRWC) Close() error {
	writeErr := p.WriteCloser.Close()
	readErr := p.ReadCloser.Close()
	if writeErr != nil {
		return writeErr
	}
	return readErr
}
