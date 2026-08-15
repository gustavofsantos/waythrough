# Waythrough

A coding agent without a language server guesses at your code. It reads
text and looks for matching names. It does not know a symbol's
definition, and it does not know every place that uses it.

Waythrough closes that gap. Waythrough is a Model Context Protocol
(MCP) server. It starts and manages Language Server Protocol (LSP)
servers. It offers their features to the agent as MCP tools.

Point a coding agent at Waythrough. Claude Code, Codex, and
Antigravity are coding agents. Waythrough gives the agent the same
"go to definition" and "find references" power as a modern editor.

## Status

This project is in early setup. It has five MCP tools. Tests run
them against a test language server, not against a real one yet.

## Install

Waythrough publishes binaries for Linux and macOS. This project is
also its own Homebrew tap. Run these commands to install it:

```sh
brew tap gustavofsantos/waythrough https://github.com/gustavofsantos/waythrough
brew install waythrough
```

## Quick start

1. Create a `waythrough.yaml` file in your project root. Each entry
   names a language server, the command that starts it, and the file
   extensions it handles:

   ```yaml
   language_servers:
     - name: gopls
       command: gopls
       args: []
       filetypes:
         .go: go
   ```

   Run `waythrough init` to write a starter config for Clojure
   instead. Edit it for your own language servers. Then run
   `waythrough validate` to check the file for errors.

2. Add Waythrough as an MCP server in your coding agent's config.
   Pass an absolute path to `--config`, so Waythrough finds
   `waythrough.yaml` no matter where the agent starts it from. The
   agent starts Waythrough over stdio:

   ```json
   {
     "mcpServers": {
       "waythrough": {
         "command": "waythrough",
         "args": ["serve", "--config", "/absolute/path/to/waythrough.yaml"]
       }
     }
   }
   ```

3. Ask your coding agent to find a definition or list references. The
   agent calls Waythrough's MCP tools. Waythrough forwards the
   request to the language server for that file type.

## Tools

Waythrough exposes these MCP tools to a connected coding agent:

- `get_definition` — find where a symbol's definition is, given a
  file position.
- `list_references` — find every place that uses a symbol, given a
  file position.
- `rename_symbol` — build the list of edits that rename a symbol,
  across every file it touches. It does not write the edits to disk.
  Your agent applies them.
- `signature_help` — list the signatures a call could match, given a
  file position inside that call. It also says which signature and
  which parameter the position is on.
- `get_diagnostics` — list the problems a language server finds in a
  file. The language server must advertise pull diagnostics at its
  handshake. One that does not fails the call with an error naming it,
  rather than reporting a clean file. `gopls` pushes its diagnostics
  by default and advertises no pull support, so it fails this call
  today.

## Learn more

- [CONTRIBUTING.md](CONTRIBUTING.md) — set up your tools, run the
  checks, and submit a change.
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — the file layout
  and how a request flows through the code.
- [LICENSE](LICENSE) — the license for Waythrough.
