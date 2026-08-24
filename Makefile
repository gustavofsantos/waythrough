# Makefile for waythrough. Its job is dogfooding: build the code in this
# working tree and put it where your shell finds it.
#
# One rule shapes every target here: after `make install`, the `waythrough`
# your shell runs is the code you just compiled. A target either upholds
# that rule or tells you exactly what broke it, and exits non-zero.
#
# `scripts/check.sh` stays the single source of truth for validation. The
# targets below call it. They never reimplement a check it already runs.

BINARY      := waythrough
MODULE      := github.com/gustavofsantos/waythrough
MAIN_PKG    := ./cmd/$(BINARY)
VERSION_PKG := $(MODULE)/internal/cli

# `type -a` and the `-ef` file test below are bash builtins. Every recipe
# here stays bash 3.2 compatible, because that is what macOS ships.
SHELL := /bin/bash
ifeq ($(wildcard $(SHELL)),)
$(error $(SHELL) not found. The recipes in this file need bash)
endif

# A missing toolchain would leave `go env GOPATH` empty, which would make
# INSTALL_DIR `/bin` -- a directory this file deletes a file from. Refuse to
# parse rather than compute a destination out of an empty string.
ifeq ($(shell command -v go 2>/dev/null),)
$(error Go toolchain not found on PATH. Install Go at the version in go.mod)
endif

# `git describe` carries the distance from the last tag, the commit, and a
# `-dirty` suffix for uncommitted changes. That is the point of stamping it:
# two different working trees never produce the same version string, so a
# stale binary shows up in `waythrough --version` instead of hiding behind a
# constant. A release build overrides this with the tag, through GoReleaser.
GIT_VERSION := $(shell git describe --tags --always --dirty 2>/dev/null)
VERSION     ?= $(if $(GIT_VERSION),$(GIT_VERSION),dev)

# The release build adds `-s -w` to shrink the binary. A dogfooding build
# keeps the symbol table, so a debugger and a profiler still work on the
# binary you are actually running.
LDFLAGS := -X $(VERSION_PKG).version=$(VERSION)

# `go install` writes to GOBIN when it is set, and to the first GOPATH
# entry's bin otherwise. Setting GOBIN for one command keeps `go install` as
# the backend even when you want the binary somewhere else:
#   make install INSTALL_DIR=$HOME/.local/bin
GOPATH_BIN  := $(shell go env GOPATH | cut -d: -f1)/bin
GOBIN_ENV   := $(shell go env GOBIN)
INSTALL_DIR ?= $(if $(GOBIN_ENV),$(GOBIN_ENV),$(GOPATH_BIN))
INSTALLED   := $(INSTALL_DIR)/$(BINARY)

# Every target is phony on purpose. Go's build cache decides what to
# recompile, and it hashes content instead of comparing timestamps. A file
# target here would let make skip a rebuild that Go would have done, which
# is the stale-binary bug this file exists to prevent.
.PHONY: help build install verify uninstall doctor clean test check fmt hooks version

.DEFAULT_GOAL := help

help: ## List the targets in this file
	@printf 'Usage: make <target> [INSTALL_DIR=<dir>] [VERSION=<string>]\n\n'
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'
	@printf '\nversion      %s\ninstall dir  %s\n' '$(VERSION)' '$(INSTALL_DIR)'

build: ## Compile ./waythrough in the repo root (gitignored)
	go build -ldflags '$(LDFLAGS)' -o '$(BINARY)' $(MAIN_PKG)
	@printf 'built ./%s %s\n' '$(BINARY)' '$(VERSION)'

install: ## Build and install waythrough into INSTALL_DIR, then verify it
	@case '$(INSTALL_DIR)' in \
		/*) ;; \
		*) printf 'INSTALL_DIR must be an absolute path, got: %s\n' '$(INSTALL_DIR)' >&2; exit 1 ;; \
	esac
	@mkdir -p '$(INSTALL_DIR)'
	@# Delete first, so a failed compile or an unwritable destination cannot
	@# leave the previous binary sitting there looking like a fresh install.
	@rm -f '$(INSTALLED)'
	GOBIN='$(INSTALL_DIR)' go install -ldflags '$(LDFLAGS)' $(MAIN_PKG)
	@test -x '$(INSTALLED)' || { printf 'no binary at %s after go install\n' '$(INSTALLED)' >&2; exit 1; }
	@$(MAKE) --no-print-directory verify

verify: ## Check the installed binary matches this tree and wins on PATH
	@test -x '$(INSTALLED)' || { printf 'nothing installed at %s. Run: make install\n' '$(INSTALLED)' >&2; exit 1; }
	@reported="$$('$(INSTALLED)' --version 2>&1)"; \
	if [[ "$$reported" != *'$(VERSION)'* ]]; then \
		printf 'stale binary: %s reports "%s", expected version %s\n' '$(INSTALLED)' "$$reported" '$(VERSION)' >&2; \
		exit 1; \
	fi; \
	printf 'installed %s (%s)\n' '$(INSTALLED)' "$$reported"
	@resolved="$$(command -v '$(BINARY)' 2>/dev/null || true)"; \
	if [[ -z "$$resolved" ]]; then \
		printf '\nwarning: %s is not on your PATH, so the shell cannot find %s.\n' '$(INSTALL_DIR)' '$(BINARY)' >&2; \
		printf '  add to your shell profile:  export PATH="%s:$$PATH"\n' '$(INSTALL_DIR)' >&2; \
		exit 0; \
	fi; \
	if [[ ! "$$resolved" -ef '$(INSTALLED)' ]]; then \
		printf '\nerror: the install succeeded, but another %s wins on PATH:\n' '$(BINARY)' >&2; \
		printf '  your shell runs  %s\n  you just built   %s\n\n' "$$resolved" '$(INSTALLED)' >&2; \
		type -a '$(BINARY)' >&2; \
		printf '\nThat is what makes an install look stuck on an old version.\n' >&2; \
		printf 'Remove the other copy (brew uninstall waythrough, mise uninstall\n' >&2; \
		printf 'waythrough, or rm), or put %s earlier in PATH.\n' '$(INSTALL_DIR)' >&2; \
		exit 1; \
	fi; \
	printf 'PATH resolves %s to the binary just installed.\n' '$(BINARY)'; \
	printf 'Restart any coding agent holding an MCP session to pick it up.\n'

uninstall: ## Remove the binary from INSTALL_DIR, and report other copies
	@rm -f '$(INSTALLED)'
	@printf 'removed %s\n' '$(INSTALLED)'
	@remaining="$$(command -v '$(BINARY)' 2>/dev/null || true)"; \
	if [[ -n "$$remaining" ]]; then \
		printf '\nanother %s is still on your PATH:\n' '$(BINARY)' >&2; \
		type -a '$(BINARY)' >&2; \
	fi

doctor: ## Show every waythrough on your PATH, and where an install lands
	@printf 'repo version   %s\n' '$(VERSION)'
	@printf 'install dir    %s\n' '$(INSTALL_DIR)'
	@printf 'go             %s (%s)\n' "$$(go version)" "$$(command -v go)"
	@gobin="$$(go env GOBIN)"; printf 'GOBIN          %s\n' "$${gobin:-(unset)}"
	@printf 'GOPATH/bin     %s\n' '$(GOPATH_BIN)'
	@if [[ -x '$(INSTALLED)' ]]; then \
		printf 'installed      %s\n' "$$('$(INSTALLED)' --version 2>&1)"; \
	else \
		printf 'installed      none at %s\n' '$(INSTALLED)'; \
	fi
	@printf '\ncopies on PATH:\n'
	@type -a '$(BINARY)' 2>/dev/null || printf '  none\n'

clean: ## Delete build output: ./waythrough and dist/
	@rm -f '$(BINARY)'
	@rm -rf dist
	@printf 'removed ./%s and dist/\n' '$(BINARY)'

test: ## Run the test suite with the race detector
	go test ./... -race -count=1

check: ## Run the full validation gate (scripts/check.sh)
	./scripts/check.sh

fmt: ## Format every Go file in place
	gofmt -w .

hooks: ## Install the pre-commit and pre-push hooks
	./scripts/install-git-hooks.sh

version: ## Print the version string this tree stamps into a build
	@printf '%s\n' '$(VERSION)'
