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
