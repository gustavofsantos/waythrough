// Package editor exposes editor-menu-style operations — "go to definition",
// "find references" — as MCP tools, backed by the language servers
// internal/lsp manages. It translates between the MCP tool's simple
// file/line/column shape and the LSP protocol details a coding agent should
// never have to know about.
package editor

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gustavofsantos/waythrough/internal/config"
	"github.com/gustavofsantos/waythrough/internal/lsp"
)

// New builds the MCP server exposing editor operations backed by manager's
// configured language servers, routed by each file's extension per cfg.
//
// logger records every request the server handles, at debug level. Pass
// slog.New(slog.DiscardHandler) to record nothing.
//
// Precondition: logger is not nil, so no call site here has to guard a log
// statement.
func New(manager *lsp.Manager, cfg config.Config, logger *slog.Logger) *mcp.Server {
	if logger == nil {
		panic("editor: New needs a logger, got nil")
	}

	e := &editor{manager: manager, serverForExt: routeByExtension(cfg)}

	server := mcp.NewServer(&mcp.Implementation{Name: "waythrough", Version: "0.1.0"}, nil)
	server.AddReceivingMiddleware(logMethodCalls(logger))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_definition",
		Description: "Find where the symbol at a file position is defined.",
	}, e.getDefinition)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_references",
		Description: "List every place the symbol at a file position is used.",
	}, e.listReferences)
	mcp.AddTool(server, &mcp.Tool{
		Name: "rename_symbol",
		Description: "Compute the edits that rename the symbol at a file position, " +
			"across every file it touches. Does not write the edits to disk — " +
			"apply them yourself. Columns are a byte-offset approximation and may " +
			"be off on lines with non-ASCII characters before the edit.",
	}, e.renameSymbol)
	mcp.AddTool(server, &mcp.Tool{
		Name: "signature_help",
		Description: "List the signatures the call at a file position could match, " +
			"and say which signature and which parameter that position is on. " +
			"Use it inside a call to learn what the next argument must be.",
	}, e.signatureHelp)
	mcp.AddTool(server, &mcp.Tool{
		Name: "get_call_hierarchy",
		Description: "List the direct incoming or outgoing calls of every symbol " +
			"at a file position. Direction must be incoming or outgoing.",
	}, e.getCallHierarchy)
	mcp.AddTool(server, &mcp.Tool{
		Name: "get_diagnostics",
		Description: "List the problems a language server finds in a file: where each " +
			"one is, how serious it is, and what it says. Use it instead of reading " +
			"compiler or linter output by hand.",
	}, e.getDiagnostics)
	mcp.AddTool(server, &mcp.Tool{
		Name: "restart_server",
		Description: "Restart one language server by name, and wait until its " +
			"replacement can answer. Use it when a server's answers no longer " +
			"match the code on disk. Every other language server keeps running.",
	}, e.restartServer)

	return server
}

type editor struct {
	manager      *lsp.Manager
	serverForExt map[string]string
}

// routeByExtension builds the extension-to-server routing table.
//
// Precondition: config.Validate(cfg) returned no error. It is the one
// enforcer of the rule this table depends on — at most one entry claims any
// one extension. Given a config that breaks it, the last entry to claim an
// extension wins here, in silence, and an agent gets answers from a server
// it never picked. cmd/waythrough reaches this only through serve, which
// validates first (internal/cli/serve.go), and a CLI test pins that.
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
	File   string `json:"file" jsonschema:"file path, absolute or relative to the project root"`
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

type renamePosition struct {
	position
	NewName string `json:"new_name" jsonschema:"the new name to give the symbol"`
}

// edit is a range replacement an agent applies verbatim, so its columns —
// unlike location's — are not merely a navigation hint: an off-by-N on a
// line with non-ASCII characters before the edit corrupts the file.
type edit struct {
	File        string `json:"file"`
	StartLine   int    `json:"start_line"`
	StartColumn int    `json:"start_column"`
	EndLine     int    `json:"end_line"`
	EndColumn   int    `json:"end_column"`
	NewText     string `json:"new_text"`
}

type editsOutput struct {
	Edits []edit `json:"edits"`
}

// document is the input of a tool that asks about a file as a whole, rather
// than about one position in it.
type document struct {
	File string `json:"file" jsonschema:"file path, absolute or relative to the project root"`
}

// restartTarget names a whole language server, the subject of a tool that
// acts on one rather than on a file or a position in a file.
type restartTarget struct {
	Name string `json:"server" jsonschema:"the configured name of the language server"`
}

// restartOutput reports the state the named server reached, so an agent
// reads that the replacement can answer rather than assuming it.
type restartOutput struct {
	Server string `json:"server"`
	Status string `json:"status"`
}

type diagnostic struct {
	StartLine   int    `json:"start_line"`
	StartColumn int    `json:"start_column"`
	EndLine     int    `json:"end_line"`
	EndColumn   int    `json:"end_column"`
	Severity    string `json:"severity,omitempty"`
	Message     string `json:"message"`
	Source      string `json:"source,omitempty"`
	Code        string `json:"code,omitempty"`
}

type diagnosticsOutput struct {
	Diagnostics []diagnostic `json:"diagnostics"`
}

type signature struct {
	Label         string   `json:"label"`
	Documentation string   `json:"documentation,omitempty"`
	Parameters    []string `json:"parameters"`
}

// signatureHelpOutput names its two indices as indices, not as positions:
// each one counts from 0, unlike the 1-based line and column every other
// tool here speaks in. ActiveSignature indexes Signatures, ActiveParameter
// indexes that signature's Parameters, and neither means anything when
// Signatures is empty.
type signatureHelpOutput struct {
	Signatures      []signature `json:"signatures"`
	ActiveSignature int         `json:"active_signature" jsonschema:"0-based signature index"`
	ActiveParameter int         `json:"active_parameter" jsonschema:"0-based parameter index"`
}

type callHierarchyInput struct {
	position
	Direction string `json:"direction" jsonschema:"call direction: incoming or outgoing"`
}

type callHierarchySymbol struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

type hierarchyCall struct {
	Symbol    callHierarchySymbol `json:"symbol"`
	CallSites []location          `json:"call_sites"`
}

type callHierarchyRoot struct {
	Symbol callHierarchySymbol `json:"symbol"`
	Calls  []hierarchyCall     `json:"calls"`
}

type callHierarchyOutput struct {
	Roots []callHierarchyRoot `json:"roots"`
}

func (e *editor) getDefinition(
	ctx context.Context, _ *mcp.CallToolRequest, in position,
) (*mcp.CallToolResult, locationsOutput, error) {
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

func (e *editor) listReferences(
	ctx context.Context, _ *mcp.CallToolRequest, in position,
) (*mcp.CallToolResult, locationsOutput, error) {
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

func (e *editor) renameSymbol(
	ctx context.Context, _ *mcp.CallToolRequest, in renamePosition,
) (*mcp.CallToolResult, editsOutput, error) {
	if in.NewName == "" {
		return nil, editsOutput{}, fmt.Errorf("new_name must not be empty")
	}

	name, err := e.resolveTarget(in.position)
	if err != nil {
		return nil, editsOutput{}, err
	}

	edits, err := e.manager.Rename(ctx, name, in.File, in.Line, in.Column, in.NewName)
	if err != nil {
		return nil, editsOutput{}, err
	}
	return nil, toEditsOutput(edits), nil
}

func (e *editor) signatureHelp(
	ctx context.Context, _ *mcp.CallToolRequest, in position,
) (*mcp.CallToolResult, signatureHelpOutput, error) {
	name, err := e.resolveTarget(in)
	if err != nil {
		return nil, signatureHelpOutput{}, err
	}

	help, err := e.manager.SignatureHelp(ctx, name, in.File, in.Line, in.Column)
	if err != nil {
		return nil, signatureHelpOutput{}, err
	}
	return nil, toSignatureHelpOutput(help), nil
}

func (e *editor) getCallHierarchy(
	ctx context.Context, _ *mcp.CallToolRequest, in callHierarchyInput,
) (*mcp.CallToolResult, callHierarchyOutput, error) {
	if in.Direction != "incoming" && in.Direction != "outgoing" {
		return nil, callHierarchyOutput{}, fmt.Errorf(
			"direction must be incoming or outgoing, got %q", in.Direction)
	}

	name, err := e.resolveTarget(in.position)
	if err != nil {
		return nil, callHierarchyOutput{}, err
	}

	hierarchies, err := e.manager.CallHierarchy(
		ctx, name, in.File, in.Line, in.Column, in.Direction)
	if err != nil {
		return nil, callHierarchyOutput{}, err
	}
	return nil, toCallHierarchyOutput(hierarchies), nil
}

func (e *editor) getDiagnostics(
	ctx context.Context, _ *mcp.CallToolRequest, in document,
) (*mcp.CallToolResult, diagnosticsOutput, error) {
	name, err := e.serverForFile(in.File)
	if err != nil {
		return nil, diagnosticsOutput{}, err
	}

	diagnostics, err := e.manager.Diagnostics(ctx, name, in.File)
	if err != nil {
		return nil, diagnosticsOutput{}, err
	}
	return nil, toDiagnosticsOutput(diagnostics), nil
}

// restartServer replaces one language server's process. It returns only
// once the replacement has passed its own readiness gate, so the next tool
// call reaches a server that can answer rather than one still starting.
func (e *editor) restartServer(
	ctx context.Context, _ *mcp.CallToolRequest, in restartTarget,
) (*mcp.CallToolResult, restartOutput, error) {
	if in.Name == "" {
		return nil, restartOutput{}, fmt.Errorf("server must name a configured language server")
	}

	if err := e.manager.Restart(ctx, in.Name); err != nil {
		return nil, restartOutput{}, err
	}
	return nil, restartOutput{Server: in.Name, Status: "ready"}, nil
}

// resolveTarget validates a 1-based position and finds which configured
// server handles in.File's extension — the checks every editor operation on
// a position needs before it can ask a language server anything.
func (e *editor) resolveTarget(in position) (string, error) {
	if in.Line < 1 || in.Column < 1 {
		return "", fmt.Errorf(
			"line and column are 1-based and must be at least 1, got line=%d column=%d",
			in.Line, in.Column)
	}
	return e.serverForFile(in.File)
}

// serverForFile finds which configured server handles file's extension. It
// is the whole check for a tool that asks about a file rather than about a
// position in it, since such a tool has no line or column to validate.
func (e *editor) serverForFile(file string) (string, error) {
	ext := filepath.Ext(file)
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

func toDiagnosticsOutput(diagnostics []lsp.Diagnostic) diagnosticsOutput {
	out := diagnosticsOutput{Diagnostics: make([]diagnostic, len(diagnostics))}
	for i, d := range diagnostics {
		out.Diagnostics[i] = diagnostic{
			StartLine:   d.StartLine,
			StartColumn: d.StartColumn,
			EndLine:     d.EndLine,
			EndColumn:   d.EndColumn,
			Severity:    d.Severity,
			Message:     d.Message,
			Source:      d.Source,
			Code:        d.Code,
		}
	}
	return out
}

func toSignatureHelpOutput(help lsp.SignatureHelp) signatureHelpOutput {
	out := signatureHelpOutput{
		Signatures:      make([]signature, len(help.Signatures)),
		ActiveSignature: help.ActiveSignature,
		ActiveParameter: help.ActiveParameter,
	}
	for i, s := range help.Signatures {
		out.Signatures[i] = signature{
			Label:         s.Label,
			Documentation: s.Documentation,
			Parameters:    s.Parameters,
		}
	}
	return out
}

func toCallHierarchyOutput(hierarchies []lsp.CallHierarchy) callHierarchyOutput {
	out := callHierarchyOutput{Roots: make([]callHierarchyRoot, len(hierarchies))}
	for rootIndex, hierarchy := range hierarchies {
		calls := make([]hierarchyCall, len(hierarchy.Calls))
		for callIndex, callResult := range hierarchy.Calls {
			sites := make([]location, len(callResult.CallSites))
			for siteIndex, site := range callResult.CallSites {
				sites[siteIndex] = location{File: site.File, Line: site.Line, Column: site.Column}
			}
			calls[callIndex] = hierarchyCall{
				Symbol:    toCallHierarchySymbol(callResult.Symbol),
				CallSites: sites,
			}
		}
		out.Roots[rootIndex] = callHierarchyRoot{
			Symbol: toCallHierarchySymbol(hierarchy.Symbol),
			Calls:  calls,
		}
	}
	return out
}

func toCallHierarchySymbol(symbol lsp.Symbol) callHierarchySymbol {
	return callHierarchySymbol{
		Name: symbol.Name, Kind: symbol.Kind, Detail: symbol.Detail,
		File: symbol.Location.File, Line: symbol.Location.Line, Column: symbol.Location.Column,
	}
}

func toEditsOutput(edits []lsp.Edit) editsOutput {
	out := editsOutput{Edits: make([]edit, len(edits))}
	for i, e := range edits {
		out.Edits[i] = edit{
			File:        e.File,
			StartLine:   e.StartLine,
			StartColumn: e.StartColumn,
			EndLine:     e.EndLine,
			EndColumn:   e.EndColumn,
			NewText:     e.NewText,
		}
	}
	return out
}
