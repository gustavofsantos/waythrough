package editor

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// stackBytesMax bounds the stack trace one panic record carries.
const stackBytesMax = 8 << 10

// recoverPanics returns MCP receiving middleware that turns a panic in any
// handler into an error for that one request. The SDK recovers nothing, so
// without this a single faulty tool call would end the process, and with it
// every session a shared daemon serves and every language server it owns.
//
// A panic is a programmer mistake, not an operational failure, so it is
// still logged at error level with its stack, whatever the log level.
func recoverPanics(logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(
			ctx context.Context, method string, req mcp.Request,
		) (result mcp.Result, err error) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				stack := debug.Stack()
				if len(stack) > stackBytesMax {
					stack = stack[:stackBytesMax]
				}
				logger.ErrorContext(ctx, "mcp handler panicked",
					slog.String("method", method),
					slog.String("panic", fmt.Sprint(recovered)),
					slog.String("stack", string(stack)))
				result = nil
				err = fmt.Errorf("internal error handling %s: %v", method, recovered)
			}()
			return next(ctx, method, req)
		}
	}
}
