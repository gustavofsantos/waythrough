package editor

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolArgumentsBytesMax and toolResultBytesMax bound what one record
// carries of a tool call's two variable-length parts. A rename across a
// large package answers with every edit it found, so the result in
// particular has no size a language server is obliged to respect.
const (
	toolArgumentsBytesMax = 1024
	toolResultBytesMax    = 2048
)

// logMethodCalls returns MCP receiving middleware that records every
// request this server handles: what the coding agent asked for, what came
// back, and how long the answer took. Those three together are the evidence
// for whether Waythrough is earning its place in an agent's tool list,
// which is the question --debug exists to answer.
//
// It reports what the server did and changes none of it: the result and
// error below are the wrapped handler's own, returned untouched.
//
// Every record is debug level, so a logger with no debug handler — the
// default, and what serve builds without --debug — costs one branch per
// received message and nothing else.
func logMethodCalls(logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if !logger.Enabled(ctx, slog.LevelDebug) {
				return next(ctx, method, req)
			}

			call := describeCall(method, req)
			logger.DebugContext(ctx, "mcp request received", call.attributes()...)

			started := time.Now()
			result, err := next(ctx, method, req)
			elapsed := time.Since(started)

			// The answered record repeats the tool and arguments rather than
			// pointing back at the received one. An MCP session may have
			// several calls in flight, and the SDK hands middleware no
			// request id, so what the agent asked for is the only thing that
			// tells two concurrent answers apart.
			answered := call.attributes()
			answered = append(answered, slog.Int64("duration_ms", elapsed.Milliseconds()))
			answered = append(answered, describeAnswer(result, err)...)
			logger.DebugContext(ctx, "mcp request answered", answered...)

			return result, err
		}
	}
}

// call is what a log record says about one request: the MCP method, and —
// for tools/call alone — which tool and the arguments it was given.
type call struct {
	method    string
	tool      string
	arguments string
}

// attributes renders call for slog. It allocates a fresh slice on every
// use, so a caller may append to the result without disturbing another
// record built from the same call.
func (c call) attributes() []any {
	attributes := make([]any, 0, 4)
	attributes = append(attributes, slog.String("method", c.method))
	if c.tool != "" {
		attributes = append(attributes, slog.String("tool", c.tool))
	}
	if c.arguments != "" {
		attributes = append(attributes, slog.String("arguments", c.arguments))
	}
	return attributes
}

// describeCall reads what a request says about itself. A server receives
// tools/call arguments still encoded, which is what this wants: the bytes
// the agent sent, with no schema of ours in between. Every other method
// carries nothing worth a record beyond its name.
func describeCall(method string, req mcp.Request) call {
	described := call{method: method}

	// A typed nil satisfies mcp.Params, so matching the type is not enough
	// to know there is anything to read.
	switch params := req.GetParams().(type) {
	case *mcp.CallToolParamsRaw:
		if params == nil {
			return described
		}
		described.tool = params.Name
		described.arguments = truncateForLog(string(params.Arguments), toolArgumentsBytesMax)
	case *mcp.CallToolParams:
		if params == nil {
			return described
		}
		described.tool = params.Name
	}
	return described
}

// describeAnswer says how a request ended, separating the three outcomes an
// agent has to tell apart: the server answered, the tool refused the call,
// or the request never reached a tool.
//
// A tool's own answer is recorded, not merely counted, because "did this
// call come back with anything useful" is exactly what a reader of these
// logs is asking. It is the agent's own project data on the agent's own
// stderr, and truncateForLog bounds how much of it one record carries.
func describeAnswer(result mcp.Result, err error) []any {
	if err != nil {
		return []any{
			slog.String("outcome", "failed"),
			slog.String("error", truncateForLog(err.Error(), toolResultBytesMax)),
		}
	}

	toolResult, isToolResult := result.(*mcp.CallToolResult)
	if !isToolResult || toolResult == nil {
		return []any{slog.String("outcome", "ok")}
	}

	outcome := "ok"
	if toolResult.IsError {
		outcome = "tool_error"
	}
	return []any{
		slog.String("outcome", outcome),
		slog.String("result", truncateForLog(contentText(toolResult), toolResultBytesMax)),
	}
}

// contentText joins the text a tool answered with, reading no more than one
// byte past the record cap. Waythrough's tools all answer with structured
// output, which the SDK renders into one text block, and that block is as
// large as the language server's answer — so copying it whole only to
// truncate it afterwards would be work spent on bytes no record can carry.
//
// The one byte past the cap is what lets truncateForLog tell a whole answer
// from a cut one, so a cut is marked rather than silent.
func contentText(result *mcp.CallToolResult) string {
	const readBytesMax = toolResultBytesMax + 1

	var joined strings.Builder
	for _, content := range result.Content {
		text, isText := content.(*mcp.TextContent)
		if !isText {
			continue
		}

		separator := ""
		if joined.Len() > 0 {
			separator = " "
		}
		room := readBytesMax - joined.Len() - len(separator)
		if room <= 0 {
			break
		}

		joined.WriteString(separator)
		if len(text.Text) > room {
			joined.WriteString(text.Text[:room])
			break
		}
		joined.WriteString(text.Text)
	}
	return joined.String()
}

// truncateForLog caps text at limitBytes, cutting on a rune boundary so a
// truncated record stays valid UTF-8, and says that it cut rather than
// leaving a reader to mistake the cut for the whole answer.
func truncateForLog(text string, limitBytes int) string {
	if len(text) <= limitBytes {
		return text
	}

	cut := limitBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…[truncated]"
}
