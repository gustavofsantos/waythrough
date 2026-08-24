# Instructions

## Priorities

1. Safety and correctness.
2. Performance and predictable resource use.
3. Developer experience.

Choose the simplest design that improves all three. Do the necessary design thinking before implementation. Do not knowingly introduce technical debt, unbounded work, avoidable latency spikes, or silently ignored failure paths.

## Go navigation

When working with `*.go` files, use the Waythrough tools for semantic navigation:

- **Where is this symbol defined?** Use `get_definition`.
- **Which code uses this symbol?** Use `list_references`.
- **Which edits does a rename need?** Use `rename_symbol`.

A text search finds names, not meaning. Use text search and the Waythrough tools for the questions each answers best.

## Change checklist

Before completing a non-trivial change, use the `critical-systems-design` skill. For changes on correctness-critical, resource-sensitive, or hot paths, also ask the appropriate specialist reviewer subagent for a focused review.

<!-- waythrough:start -->
## Code navigation: use Waythrough MCP, not grep

Waythrough runs this project's language servers. Its MCP tools answer from
the symbol graph, the way an IDE does. Text search matches strings; these
resolve symbols. Reach for them first, and never trace a symbol by reading
files.

These take `file`, `line`, `column` — 1-based, on the symbol itself:

- Go to definition → `get_definition`
- Find all references → `list_references`
- Which argument goes here → `signature_help`
- Rename across the project → `rename_symbol`, plus `new_name`
  (returns edits; you apply them)

These do not:

- Errors and warnings in a file → `get_diagnostics` (`file`)
- Answers stopped matching the code → `restart_server` (`server`)

Search for a name when you need one; resolve it with these. A file type
with no configured language server has no answers here.
<!-- waythrough:end -->
