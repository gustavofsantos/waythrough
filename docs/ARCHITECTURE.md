# Architecture

This document describes the file layout of Waythrough, and how a
request flows through the code. Read
[CONTRIBUTING.md](../CONTRIBUTING.md) first for how to set up your
tools and check a change.

## File layout

| Path | Content |
| --- | --- |
| `cmd/waythrough/` | The `main` package. It only calls `cli.Execute`. |
| `internal/cli/` | The `waythrough` command-line interface: `init`, `validate`, and `serve`. |
| `internal/config/` | The `waythrough.yaml` schema, plus its `Load` and `Validate` functions. |
| `internal/lsp/` | Process lifecycle for each configured language server, and the LSP client that talks to it. |
| `internal/lsp/fakelsp/` | A small language server built only for `internal/lsp` tests. |
| `internal/editor/` | The MCP server. It turns each MCP tool call into an LSP request, and the LSP response back into MCP output. |
| `scripts/` | `check.sh`, the check script, and `install-git-hooks.sh`, the hook installer. |
| `.github/workflows/` | The CI workflow and the release workflow. |
| `.tools/` | A gitignored directory. It holds the pinned `golangci-lint` binary. |
| `.goreleaser.yaml` | The GoReleaser config for the release build and the Homebrew tap. |
| `waythrough.yaml` | An example config file, used when you run Waythrough from this repository. |

## How a request flows

1. A coding agent starts `waythrough serve` over stdio, as an MCP
   server.
2. `internal/cli` loads and validates `waythrough.yaml`, through
   `internal/config`.
3. `internal/cli` starts an `internal/lsp` manager. The manager
   starts one subprocess per configured language server, and waits
   for each one to become ready.
4. `internal/cli` builds the MCP server from `internal/editor`. This
   step registers the `get_definition`, `list_references`,
   `rename_symbol`, `signature_help`, and `get_diagnostics` tools.
5. The MCP server serves tool calls until the agent's session ends.
   `internal/editor` routes each call, by the file extension in the
   call, to the language server that handles it. It sends the LSP
   request through the `internal/lsp` manager, and it turns the LSP
   response into the tool's output shape.
6. When `serve` exits, it shuts down every language server the
   manager started.

## Readiness

An LSP server's process can start before the server can answer a
request. `internal/lsp` gates on one of two readiness signals, set
per server in `waythrough.yaml`:

- `progress` — the default. The manager waits for every LSP
  `workDoneProgress` token the server opens to close.
- `handshake` — an opt-in for a server with no background indexing.
  The manager treats the server as ready as soon as the
  initialize and initialized handshake completes.

## Add a language server

Add an entry to `waythrough.yaml`. You need no code change. See the
`language_servers` schema in `internal/config/config.go`.

## Add an MCP tool

Add the tool to `internal/editor/editor.go`, and add the LSP call it
needs to `internal/lsp/manager.go`. Add tests at both levels: an
`internal/lsp` test against `fakelsp`, and an `internal/editor` test
for the tool's input and output shape.
