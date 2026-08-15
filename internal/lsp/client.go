package lsp

import (
	"context"
	"encoding/json"

	"go.lsp.dev/protocol"
)

// client is Waythrough's implementation of the LSP client-side callbacks: the
// requests and notifications a language server sends to the editor. It only
// overrides what the readiness gate needs; every other method is a no-op
// inherited from protocol.UnimplementedClient.
type client struct {
	protocol.UnimplementedClient

	proc *serverProcess
}

// WorkDoneProgressCreate accepts a server's request to report progress on a
// new token. Waythrough never supplies its own workDoneToken in initialize,
// so this request is the only path a server has to start reporting, which
// keeps the readiness gate's token tracking complete.
func (c *client) WorkDoneProgressCreate(
	ctx context.Context,
	params *protocol.WorkDoneProgressCreateParams,
) error {
	c.proc.tokenCreated(tokenKey(params.Token))
	return nil
}

// Progress records when a token the server opened closes. The readiness
// gate does not need "begin" or "report": a token counts as open from the
// moment it is created until its "end" arrives.
func (c *client) Progress(ctx context.Context, params *protocol.ProgressParams) error {
	var value struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(params.Value), &value); err != nil {
		return nil
	}
	if value.Kind == "end" {
		c.proc.tokenClosed(tokenKey(params.Token))
	}
	return nil
}

func tokenKey(token protocol.ProgressToken) string {
	data, err := protocol.Marshal(token)
	if err != nil {
		return ""
	}
	return string(data)
}
