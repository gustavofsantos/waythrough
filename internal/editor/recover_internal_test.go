package editor

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A panicking handler must fail its own request and leave the process, and
// so every other session, running.
func TestRecoverPanicsFailsOnlyThePanickingRequest(t *testing.T) {
	var recorded bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&recorded, nil))
	handler := recoverPanics(logger)(func(
		context.Context, string, mcp.Request,
	) (mcp.Result, error) {
		panic("tool bug")
	})

	result, err := handler(context.Background(), "tools/call", nil)
	if result != nil {
		t.Fatalf("result = %v, want nil", result)
	}
	if err == nil || !strings.Contains(err.Error(), "tool bug") {
		t.Fatalf("error = %v, want one naming the panic", err)
	}
	if !strings.Contains(recorded.String(), "mcp handler panicked") {
		t.Fatalf("panic was not logged at the default level: %q", recorded.String())
	}
}

func TestRecoverPanicsPassesAnswersThrough(t *testing.T) {
	want := &mcp.CallToolResult{}
	handler := recoverPanics(slog.New(slog.DiscardHandler))(func(
		context.Context, string, mcp.Request,
	) (mcp.Result, error) {
		return want, nil
	})

	result, err := handler(context.Background(), "tools/call", nil)
	if err != nil || result != want {
		t.Fatalf("handler = (%v, %v), want the wrapped answer untouched", result, err)
	}
}
