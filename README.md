# Waythrough

Coding agents are great at reading code. Navigating an unfamiliar
codebase is harder: without language tooling, an agent has to search
for matching text and guess which definition or reference actually
matters.

Waythrough gives your agent a map. It runs your project's Language
Server Protocol (LSP) servers and exposes their code intelligence
through the Model Context Protocol (MCP). That means agents such as
Claude Code, Codex, and Antigravity can jump to definitions, find
references, inspect diagnostics, and safely plan renames—the same
kind of help you expect from a modern editor.

Instead of making your agent piece the codebase together one text
search at a time, let it ask the tools that already understand your
code.

## Status

This project is in early setup. It has six MCP tools. Tests run
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
- `restart_server` — restart one language server by name. The call
  returns only when the replacement can answer, so the next call
  does not reach a server that is still starting. Every other
  language server keeps running. Use it when a server's answers no
  longer match the code on disk. Waythrough cannot see that a
  server answers from a stale index, so your agent must decide.

## See what it is doing

`serve` is quiet by default: it says nothing on its own, because
stdout carries the MCP protocol frames and stderr belongs to whatever
your coding agent chooses to show you.

Pass `--debug` when you want to know whether Waythrough is earning
its place in your agent's tool list:

```json
{
  "mcpServers": {
    "waythrough": {
      "command": "waythrough",
      "args": [
        "serve", "--config", "/absolute/path/to/waythrough.yaml", "--debug"
      ]
    }
  }
}
```

Every record goes to stderr, never to stdout, and covers three
things:

- **Every MCP request.** Which tool your agent called, the arguments
  it sent, how long the answer took, and what came back. A tool that
  answers with nothing and a tool that answers with twelve locations
  are the two cases worth telling apart, so the answer itself is
  recorded, capped at 2 KB per record.
- **Every language server's lifecycle.** Starting, ready, exited,
  restarted, and gave up. This is usually why a tool call reports a
  server still starting.
- **Every language server's own stderr.** A server that will not
  start explains itself there and nowhere else. Waythrough discards
  that stream without `--debug`.

The records name file paths and carry tool results, which include
your source code for a rename. They go to your terminal and nowhere
else, but that is the reason `--debug` is a flag rather than the
default.

Coding agents differ in where they put an MCP server's stderr. Check
your agent's MCP logs for it.

## Learn more

- [CONTRIBUTING.md](CONTRIBUTING.md) — set up your tools, run the
  checks, and submit a change.
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — the file layout
  and how a request flows through the code.
- [LICENSE](LICENSE) — the license for Waythrough.
