---
paths:
    - "**/*.go"
---

## Navigate Go code with the Waythrough tools

A text search finds names. It does not find meaning. Two functions can share a name. A comment can hold a name. A local variable can hide a package symbol.

A text search cannot tell these apart. A language server can. Text search and the Waythrough tools answer different questions. Each question below belongs to one tool only:

- **Where is this symbol defined?** Use `get_definition`.
- **Which code uses this symbol?** Use `list_references`.
- **Which edits does a rename need?** Use `rename_symbol`.
