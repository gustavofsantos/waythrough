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

## Install

Waythrough publishes binaries for Linux and macOS. It uses a Homebrew
tap. Run these commands to install it:

```
brew tap gustavofsantos/tap
brew install waythrough
```

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

## Release

A tag push in the form `vX.Y.Z` starts the release. GitHub Actions
then runs GoReleaser.

GoReleaser builds a binary for Linux and a binary for macOS. It builds
each one for amd64 and for arm64. It publishes a GitHub release with
these binaries.

GoReleaser also pushes a new formula to the
[gustavofsantos/homebrew-tap](https://github.com/gustavofsantos/homebrew-tap)
repository. This step makes `brew install waythrough` install the new
version.

The release workflow needs a `HOMEBREW_TAP_GITHUB_TOKEN` secret. This
token must have write access to the `homebrew-tap` repository. The
default `GITHUB_TOKEN` cannot push to a different repository.

Run this command to preview a build without a tag push:

```
goreleaser release --snapshot --clean --skip=publish
```
