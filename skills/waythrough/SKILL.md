---
name: waythrough
description: Navigate and change code through the Waythrough MCP tools, which answer from the project's language servers instead of from text search. Use when you need to find where a symbol is defined, every place it is used, who calls a function or what it calls, which argument a call takes, the edits a rename needs, or the errors in a file, in any language the user configured Waythrough for. Use it before grepping for a symbol's usages or reading files to trace one.
allowed-tools: mcp__waythrough__get_definition, mcp__waythrough__list_references, mcp__waythrough__signature_help, mcp__waythrough__get_call_hierarchy, mcp__waythrough__rename_symbol, mcp__waythrough__get_diagnostics, mcp__waythrough__restart_server, mcp__waythrough__get_status
---

# Waythrough

Waythrough runs the project's language servers and answers through MCP tools
(`mcp__waythrough__get_definition`, ...). They resolve symbols the way an IDE
does. Text search only matches strings. It cannot tell a definition from a
comment, a method from a field with the same name, or one package's `New`
from another's.

If the tools are not connected, tell the user and continue with text search.
Waythrough connects to Claude Code with this command:

```sh
claude mcp add --scope user waythrough -- waythrough serve --shared
```

`waythrough init` creates the configuration first, if the user has none.

## Pick the tool by the question

| Question | Tool | Extra arguments |
| --- | --- | --- |
| Where is this defined? | `get_definition` | |
| Where is this used? | `list_references` | |
| Who calls this, or what does it call? | `get_call_hierarchy` | `direction`: `incoming` or `outgoing` |
| Which argument goes here? | `signature_help` | |
| What must change to rename this? | `rename_symbol` | `new_name` |
| What is wrong in this file? | `get_diagnostics` | none: takes only `file` |
| Did a call fail or answer slowly? | `get_status` | none: takes no arguments |
| Do the answers no longer match the code? | `restart_server` | none: takes only `server` |

Every tool in the first five rows takes `file`, `line` and `column`.

Search text to find a name. Use these tools to learn what the name means.
For example, to find the callers of `parseHeader`, grep once to find its
definition, then call `get_call_hierarchy` on it. Do not grep for
`parseHeader(` and read every hit.

## Give a position

- Pass `file` as an absolute path. A relative path works only inside a git
  checkout.
- `line` and `column` are 1-based. Put them on the symbol itself, not on the
  start of its line. In `func (s *Server) Start(ctx context.Context)`, to
  ask about `Start`, give the column of the `S` in `Start`.
- To find a position, grep with line numbers for the name. Then count the
  characters before the name on that line and add 1. A tab counts as one
  character.
- A file type with no configured language server has no answers here. The
  error says so. Use text search for that file.

## Work with the answers

- **References.** `list_references` includes the declaration. Use it, not a
  text search, to decide whether a change is safe. It finds every use of the
  symbol and nothing that only shares its name.
- **Call hierarchy.** One call returns one level. To trace deeper, call it
  again on the callers you need, and stop when you have answered the
  question. Static hierarchies can miss calls through interfaces, function
  values and reflection, so check those with `list_references`.
- **Rename.** `rename_symbol` returns edits and writes nothing. Apply every
  edit it returns with your editing tools, then call `get_diagnostics` on the
  files you changed. Columns are a byte-offset approximation, so check edits
  on lines with non-ASCII characters before the symbol.
- **Diagnostics.** Only a server that offers pull diagnostics answers
  `get_diagnostics`. `gopls` does not, by default. When the call says the
  server does not support pull diagnostics, run the project's own build or
  checks instead.
- Answers come from the files on disk. Save your edits before you ask about
  them.

## When a call fails or is slow

- **"Still starting."** The server is still indexing. Wait a few seconds and
  call again once. If it still fails, call `get_status`.
- **Call `get_status`** when a call fails in a way you do not understand, or
  takes much longer than the others. It shows, for each server, whether it is
  `ready`, still `starting`, or `failed`, and gives a health word, its recent
  failures and latency, and its last error.
  - A server marked `failing` has given up after repeated crashes. Call
    `restart_server` once. If it fails again, tell the user and show them the
    last error.
  - A server marked `degraded` still answers. Read its last error before you
    decide anything.
  - A server marked `idle` has not started yet. A request for one of its
    files starts it.
- **Stale answers.** A definition that points at a line that no longer holds
  the symbol, or references to code you deleted, mean the index is out of
  date. This happens most after a branch switch or a large generated change.
  Call `restart_server` with the server's configured name, which
  `get_status` lists. A restart reaches every session that shares the
  server, so do it only when the answers are wrong.
- You can follow a definition into a dependency, such as a file in the Go
  module cache. The server you used last answers for a file outside every
  git checkout. A file in another project that no root marker claims fails,
  and the error says why.
