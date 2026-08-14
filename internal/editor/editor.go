// Package editor exposes editor-menu-style operations — "go to definition",
// "find references" — as MCP tools, backed by the language servers
// internal/lsp manages. It translates between the MCP tool's simple
// file/line/column shape and the LSP protocol details a coding agent should
// never have to know about.
package editor

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gustavofsantos/waythrough/internal/config"
	"github.com/gustavofsantos/waythrough/internal/lsp"
)

// New builds the MCP server exposing editor operations backed by manager's
// configured language servers, routed by each file's extension per cfg.
func New(manager *lsp.Manager, cfg config.Config) *mcp.Server {
	e := &editor{manager: manager, serverForExt: routeByExtension(cfg)}

	server := mcp.NewServer(&mcp.Implementation{Name: "waythrough", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_definition",
		Description: "Find where the symbol at a file position is defined.",
	}, e.getDefinition)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_references",
		Description: "List every place the symbol at a file position is used.",
	}, e.listReferences)

	return server
}

type editor struct {
	manager      *lsp.Manager
	serverForExt map[string]string
}

func routeByExtension(cfg config.Config) map[string]string {
	routes := make(map[string]string)
	for _, entry := range cfg.LanguageServers {
		for ext := range entry.Filetypes {
			routes[ext] = entry.Name
		}
	}
	return routes
}

type position struct {
	File   string `json:"file" jsonschema:"path to the file, absolute or relative to the project root"`
	Line   int    `json:"line" jsonschema:"1-based line number"`
	Column int    `json:"column" jsonschema:"1-based column number"`
}

type location struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

type locationsOutput struct {
	Locations []location `json:"locations"`
}

func (e *editor) getDefinition(ctx context.Context, _ *mcp.CallToolRequest, in position) (*mcp.CallToolResult, locationsOutput, error) {
	name, err := e.resolveTarget(in)
	if err != nil {
		return nil, locationsOutput{}, err
	}

	locations, err := e.manager.Definition(ctx, name, in.File, in.Line, in.Column)
	if err != nil {
		return nil, locationsOutput{}, err
	}
	return nil, toLocationsOutput(locations), nil
}

func (e *editor) listReferences(ctx context.Context, _ *mcp.CallToolRequest, in position) (*mcp.CallToolResult, locationsOutput, error) {
	name, err := e.resolveTarget(in)
	if err != nil {
		return nil, locationsOutput{}, err
	}

	locations, err := e.manager.References(ctx, name, in.File, in.Line, in.Column)
	if err != nil {
		return nil, locationsOutput{}, err
	}
	return nil, toLocationsOutput(locations), nil
}

// resolveTarget validates a 1-based position and finds which configured
// server handles in.File's extension — the checks every editor operation
// needs before it can ask a language server anything.
func (e *editor) resolveTarget(in position) (string, error) {
	if in.Line < 1 || in.Column < 1 {
		return "", fmt.Errorf("line and column are 1-based and must be at least 1, got line=%d column=%d", in.Line, in.Column)
	}

	ext := filepath.Ext(in.File)
	name, ok := e.serverForExt[ext]
	if !ok {
		return "", fmt.Errorf("no configured language server for file extension %q", ext)
	}
	return name, nil
}

func toLocationsOutput(locations []lsp.Location) locationsOutput {
	out := locationsOutput{Locations: make([]location, len(locations))}
	for i, loc := range locations {
		out.Locations[i] = location{File: loc.File, Line: loc.Line, Column: loc.Column}
	}
	return out
}
