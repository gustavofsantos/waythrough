# Contributing to Waythrough

Thank you for your interest in Waythrough. This project is in early
setup, so expect the tool set and the config schema to change. This
guide covers how to set up your tools, check a change, and submit it.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the file layout
and how a request flows through the code.

## Set up your tools

You need Go, at the version in [go.mod](go.mod), and git. Clone the
repository, then run this command once:

```sh
./scripts/install-git-hooks.sh
```

This command installs `scripts/check.sh` as a pre-commit hook and a
pre-push hook. Git does not track the `.git/hooks` directory, so each
clone needs this step. The pre-push hook runs on every push,
including a release tag push, so a broken release never reaches
GitHub Actions.

Both hooks check your full working tree. They do not check only the
staged or the pushed commits. An uncommitted change to a tracked file
can change the result.

If GitButler manages your `pre-commit` hook, the installer detects
this and installs to `pre-commit-user` instead. It does not overwrite
GitButler's hook.

## Build

```sh
go build ./...
```

## Check your change

`scripts/check.sh` is the single source of truth for validation. The
pre-commit hook, the pre-push hook, and GitHub Actions all run this
same script. A passing local run and a passing CI run never disagree.
Run it yourself at any time:

```sh
./scripts/check.sh
```

The script runs these checks, in order:

1. `gofmt` — formatting
2. `go vet` — static analysis
3. `golangci-lint` — lint, at the pinned version in the script
4. `go build` — compile every package
5. `go mod tidy -diff` — module file tidiness
6. `go test -race` — the test suite

## Two rules the linter cannot check

`.golangci.yml` enables the rules from TigerBeetle's
[TIGER_STYLE.md](https://github.com/tigerbeetle/tigerbeetle/blob/main/docs/TIGER_STYLE.md)
that this repo accepts and that a Go linter can check. Where
TigerStyle gives a number, the config uses it, such as the 100-column
line limit. Where TigerStyle gives none, the config uses a threshold
chosen for this repo. This repo rejects four other TigerStyle rules
outright, because each one fights `gofmt` or Go's own model.

Two more rules have no Go tool at all, and a checker built for either
would measure the wrong thing. A reviewer checks these two by reading
the change.

**State what a function assumes.** TigerStyle asks for at least two
assertions per function: one on what comes in, one on what goes out.
Go has no `assert` that a release build compiles away. In Go the same
rule becomes an explicit guard that returns an error. It can also
become a comment that names an invariant a caller cannot break. A
checker that counted `if err != nil` blocks would reward padding, not
thought.

Copy the shape of `editor.resolveTarget`: it names the 1-based rule,
and its error text says which value broke it.

**Bound every loop.** A loop with no lexical bound must say, in a
comment on the loop, what ends it. A syntactic checker cannot tell a
bounded loop from an unbounded one here. `lsp.runServer` uses
`for attempt := 0; ; attempt++`, which reads as unbounded. It stops on
the restart limit, on shutdown, and on a done context. Such a checker
would flag the one loop the design needs, and would pass a loop whose
bound is a variable that never decreases.

The issue file
`~/engineering/issues/2026-08-15-adopt-tigerstyle-static-checks.md`
records both rules, together with the four TigerStyle rules this repo
rejects.

## Write and run tests

Waythrough tests with [Ginkgo](https://onsi.github.io/ginkgo/) and
[Gomega](https://onsi.github.io/gomega/). Each package that has tests
has a suite file, for example `internal/cli/cli_suite_test.go`, plus
one file per behavior, for example `internal/cli/cli_test.go`.

`internal/lsp` tests against `internal/lsp/fakelsp`, a small
language server built for tests. It speaks real LSP framing over
stdio. A test against it exercises the same transport a real
language server uses, with no install of gopls or clojure-lsp.

Run the full test suite with `go test ./...`, or as part of
`./scripts/check.sh`.

## Submit a change

1. Create a branch from `main`.
2. Make your change, and add or update tests for it.
3. Run `./scripts/check.sh` and confirm every check passes.
4. Open a pull request. GitHub Actions runs `scripts/check.sh` again
   on every pull request.

Keep a pull request focused on one change. Explain the reason for the
change in the pull request description, not only what changed.

## Release process

A maintainer starts a release with a tag push in the form `vX.Y.Z`.
GitHub Actions then runs [GoReleaser](https://goreleaser.com), which:

- Builds a `waythrough` binary for Linux and macOS, each for amd64
  and arm64.
- Publishes a GitHub release with these binaries.
- Pushes an updated `Formula/waythrough.rb` file to `main`, so
  `brew install waythrough` installs the new version.

Each release asset carries the operating system and the architecture
in its name. mise reads the release through its `github` backend, and
that backend finds an asset by those two tokens. A change to
`archives.name_template` in `.goreleaser.yaml` can therefore break
`mise use -g github:gustavofsantos/waythrough`, even though it leaves
the Homebrew tap working.

GoReleaser runs `scripts/check.sh` as a hook before it builds
anything, so a broken release never publishes. Preview a release
build, without a tag push and without a publish step, with this
command:

```sh
goreleaser release --snapshot --clean --skip=publish
```
