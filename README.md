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
