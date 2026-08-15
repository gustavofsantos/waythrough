#!/usr/bin/env bash
# Validation gate for waythrough: formatting, vet, lint, build, module
# tidiness, and tests. This is the single source of truth run by both the
# local pre-commit hook (scripts/install-git-hooks.sh) and CI
# (.github/workflows/ci.yml) so the two never disagree.
set -uo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || {
	echo "not a git repository" >&2
	exit 1
}
cd "$REPO_ROOT"

# Keep this in sync with .github/workflows/ci.yml.
GOLANGCI_LINT_VERSION="v2.6.2"
TOOLS_BIN="$REPO_ROOT/.tools/bin"
GOLANGCI_LINT="$TOOLS_BIN/golangci-lint"

if [ -t 1 ]; then
	RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[0;33m'; BOLD=$'\033[1m'; NC=$'\033[0m'
else
	RED=""; GREEN=""; YELLOW=""; BOLD=""; NC=""
fi

failures=()

pass() { printf '%s✓%s %s\n' "$GREEN" "$NC" "$1"; }
fail() { printf '%s✗%s %s\n' "$RED" "$NC" "$1"; failures+=("$1"); }

# Installs a pinned golangci-lint into a repo-local, gitignored bin dir
# rather than requiring it on $PATH, so local runs and CI use the exact
# same binary and never drift.
ensure_golangci_lint() {
	if [ -x "$GOLANGCI_LINT" ] && "$GOLANGCI_LINT" version 2>/dev/null | grep -q "${GOLANGCI_LINT_VERSION#v}"; then
		return 0
	fi
	echo "${YELLOW}Installing golangci-lint ${GOLANGCI_LINT_VERSION} into ${TOOLS_BIN}...${NC}" >&2
	mkdir -p "$TOOLS_BIN"
	if ! GOBIN="$TOOLS_BIN" go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION}"; then
		return 1
	fi
}

echo "${BOLD}Running validation checks in ${REPO_ROOT}${NC}"
echo

# 1. Formatting
unformatted="$(gofmt -l . 2>&1)"
if [ -n "$unformatted" ]; then
	echo "$unformatted"
	fail "gofmt (run: gofmt -w .)"
else
	pass "gofmt"
fi

# 2. Vet
if vet_out="$(go vet ./... 2>&1)"; then
	pass "go vet"
else
	echo "$vet_out"
	fail "go vet"
fi

# 3. Lint
if ensure_golangci_lint; then
	if lint_out="$("$GOLANGCI_LINT" run ./... 2>&1)"; then
		pass "golangci-lint"
	else
		echo "$lint_out"
		fail "golangci-lint"
	fi
else
	fail "golangci-lint (could not install ${GOLANGCI_LINT_VERSION})"
fi

# 4. Build
if build_out="$(go build ./... 2>&1)"; then
	pass "go build"
else
	echo "$build_out"
	fail "go build"
fi

# 5. Module tidiness
if tidy_out="$(go mod tidy -diff 2>&1)"; then
	pass "go mod tidy"
else
	echo "$tidy_out"
	fail "go mod tidy (run: go mod tidy)"
fi

# 6. Tests
if test_out="$(go test ./... -race -count=1 2>&1)"; then
	echo "$test_out"
	pass "go test"
else
	echo "$test_out"
	fail "go test"
fi

echo
if [ "${#failures[@]}" -eq 0 ]; then
	echo "${GREEN}${BOLD}All checks passed.${NC}"
	exit 0
fi

echo "${RED}${BOLD}${#failures[@]} check(s) failed:${NC}"
for f in "${failures[@]}"; do
	echo "  - $f"
done
exit 1
