package editor

import (
	"context"
	_ "embed"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gustavofsantos/waythrough/internal/status"
)

// The get_status tool answers twice over: with the report as structured
// content, which the agent reads, and with a pointer to a page, which a host
// that supports the MCP Apps extension renders beside the result for the
// person. A host without the extension ignores the pointer, so the tool
// answers every client the same way.
const (
	statusAppURI = "ui://waythrough/status"
	// mcpAppMIMEType is the MIME type the MCP Apps extension gives an HTML
	// page a host may render.
	mcpAppMIMEType = "text/html;profile=mcp-app"
)

//go:embed status_app.html
var statusAppPage string

type statusInput struct{}

func (e *editor) getStatus(
	context.Context, *mcp.CallToolRequest, statusInput,
) (*mcp.CallToolResult, status.Report, error) {
	return nil, e.report(), nil
}

// addStatusTool registers get_status and the page it points to.
func addStatusTool(server *mcp.Server, e *editor) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "get_status",
		Description: "Report the health of the language servers behind these tools: " +
			"which run, for which project root, whether each is ready, still starting, " +
			"degraded, or failing, its recent latency and failures, and its last error. " +
			"Use it when a tool call fails or is slow, to decide between waiting and " +
			"restart_server. With --shared, it also reports the sessions sharing them.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
		Meta: mcp.Meta{
			"ui": map[string]any{"resourceUri": statusAppURI},
			// Hosts built against the extension's draft read this flat key.
			"ui/resourceUri": statusAppURI,
		},
	}, e.getStatus)

	server.AddResource(&mcp.Resource{
		URI:         statusAppURI,
		Name:        "waythrough-status",
		Title:       "Waythrough status",
		Description: "A page that shows the get_status report",
		MIMEType:    mcpAppMIMEType,
	}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI:      statusAppURI,
			MIMEType: mcpAppMIMEType,
			Text:     statusAppPage,
			Meta:     mcp.Meta{"ui": map[string]any{"prefersBorder": true}},
		}}}, nil
	})
}
