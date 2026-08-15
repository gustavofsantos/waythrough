# Waythrough

Waythrough is a Model Context Protocol (MCP) server. It connects coding
agents to Language Server Protocol (LSP) servers. Claude Code, Codex, and
Antigravity are coding agents.

Waythrough starts each LSP server. It manages the lifecycle of each LSP
server. Its own lifecycle controls the LSP server lifecycle. It starts a new
LSP server when the old one stops. It gives coding agents the LSP features
of a modern editor.

## Status

This project is in early setup. The server has no features yet.

## Build

```
go build ./...
```

## Validation

The script `scripts/check.sh` runs checks on the code:

- format
- vet
- lint
- build
- Go module files
- tests

GitHub Actions runs this script for every pull request.

Run this command one time, after you clone the repository:

```
./scripts/install-git-hooks.sh
```

This command installs the same script as a pre-commit hook. Git does not
track the `.git/hooks` directory, so each clone needs this step.

Note: the pre-commit hook checks your full working tree. It does not check
only the staged files. Uncommitted changes to tracked files can change the
result.
