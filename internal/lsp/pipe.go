package lsp

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"

	"go.lsp.dev/jsonrpc2"
)

const (
	maxLSPFrameBytes  = 64 << 20
	maxLSPHeaderBytes = 8 << 10
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

// boundedLSPConnection validates each Content-Length before jsonrpc2 allocates
// its frame buffer. It then replays the validated header and streams the body
// unchanged, leaving message decoding and outbound framing to jsonrpc2.
type boundedLSPConnection struct {
	io.ReadWriteCloser

	reader        *bufio.Reader
	header        []byte
	headerOffset  int
	bodyBytesLeft int
}

func newLSPStream(conn io.ReadWriteCloser) jsonrpc2.Stream {
	bounded := &boundedLSPConnection{
		ReadWriteCloser: conn,
		reader:          bufio.NewReader(conn),
		header:          make([]byte, 0, 256),
	}
	return jsonrpc2.NewStream(bounded)
}

func (c *boundedLSPConnection) Read(destination []byte) (int, error) {
	if len(destination) == 0 {
		return 0, nil
	}
	if c.headerOffset == len(c.header) && c.bodyBytesLeft == 0 {
		if err := c.readHeader(); err != nil {
			return 0, err
		}
	}
	if c.headerOffset < len(c.header) {
		count := copy(destination, c.header[c.headerOffset:])
		c.headerOffset += count
		return count, nil
	}

	if len(destination) > c.bodyBytesLeft {
		destination = destination[:c.bodyBytesLeft]
	}
	count, err := c.reader.Read(destination)
	c.bodyBytesLeft -= count
	return count, err
}

func (c *boundedLSPConnection) readHeader() error {
	c.header = c.header[:0]
	c.headerOffset = 0
	contentLength := -1

	for {
		line, err := c.readHeaderLine()
		if err != nil {
			return err
		}
		if bytes.Equal(line, []byte("\n")) || bytes.Equal(line, []byte("\r\n")) {
			break
		}

		field, value, found := bytes.Cut(bytes.TrimRight(line, "\r\n"), []byte(":"))
		if !found {
			return jsonrpc2.ErrInvalidHeader
		}
		if !strings.EqualFold(string(field), "content-length") {
			continue
		}

		declared, err := strconv.ParseUint(string(bytes.TrimSpace(value)), 10, 64)
		if err != nil || declared == 0 {
			return jsonrpc2.ErrInvalidHeader
		}
		if declared > maxLSPFrameBytes {
			return fmt.Errorf(
				"LSP frame declares %d bytes; maximum is %d", declared, maxLSPFrameBytes)
		}
		contentLength = int(declared)
	}
	if contentLength < 0 {
		return jsonrpc2.ErrInvalidHeader
	}
	c.bodyBytesLeft = contentLength
	return nil
}

func (c *boundedLSPConnection) readHeaderLine() ([]byte, error) {
	start := len(c.header)
	for {
		fragment, err := c.reader.ReadSlice('\n')
		if len(c.header)+len(fragment) > maxLSPHeaderBytes {
			return nil, fmt.Errorf(
				"LSP header exceeds %d bytes; maximum is %d",
				len(c.header)+len(fragment), maxLSPHeaderBytes)
		}
		c.header = append(c.header, fragment...)

		switch err {
		case nil:
			return c.header[start:], nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			if len(c.header) == 0 {
				return nil, io.EOF
			}
			return nil, io.ErrUnexpectedEOF
		default:
			return nil, err
		}
	}
}
