package lsp

import (
	"context"
	"io"
	"strings"
	"testing"
)

type streamFixture struct {
	io.Reader
	io.Writer
}

func (streamFixture) Close() error { return nil }

func TestLSPTransportRejectsAnOversizedFrameBeforeItsBody(t *testing.T) {
	conn := streamFixture{
		Reader: strings.NewReader("Content-Length: 67108865\r\n\r\n"),
		Writer: io.Discard,
	}
	stream := newLSPStream(conn)

	_, _, err := stream.Read(context.Background())
	if err == nil || err.Error() != "LSP frame declares 67108865 bytes; maximum is 67108864" {
		t.Fatalf("oversized frame error = %v", err)
	}
}

func TestLSPTransportPassesConsecutiveFramesWithinTheBound(t *testing.T) {
	const first = `{"jsonrpc":"2.0","id":1,"result":null}`
	const second = `{"jsonrpc":"2.0","id":2,"result":[]}`
	wire := "Content-Length: 38\r\n\r\n" + first +
		"Content-Length: 36\r\n\r\n" + second
	conn := streamFixture{Reader: strings.NewReader(wire), Writer: io.Discard}
	stream := newLSPStream(conn)

	if _, _, err := stream.Read(context.Background()); err != nil {
		t.Fatalf("first frame: %v", err)
	}
	if _, _, err := stream.Read(context.Background()); err != nil {
		t.Fatalf("second frame: %v", err)
	}
}

func TestLSPTransportRejectsAnOversizedHeader(t *testing.T) {
	wire := strings.Repeat("X", maxLSPHeaderBytes+1) + "\n"
	conn := streamFixture{Reader: strings.NewReader(wire), Writer: io.Discard}
	stream := newLSPStream(conn)

	_, _, err := stream.Read(context.Background())
	if err == nil || !strings.Contains(err.Error(), "LSP header exceeds") {
		t.Fatalf("oversized header error = %v", err)
	}
}
