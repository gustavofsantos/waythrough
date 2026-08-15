#!/usr/bin/env bash
# Installs scripts/check.sh as a git pre-commit hook and a pre-push hook.
# Safe to re-run.
#
# .git/hooks isn't tracked by git, so every clone needs to run this once:
#   ./scripts/install-git-hooks.sh
#
# GitButler-aware: when GitButler manages .git/hooks/pre-commit (it wraps
# it to block direct commits on gitbutler/workspace and calls
# pre-commit-user first if present), this installs to pre-commit-user
# instead of clobbering GitButler's wrapper. See
# https://docs.gitbutler.com for details on that mechanism.
set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
HOOKS_DIR="$(git rev-parse --git-path hooks)"
MARKER="# WAYTHROUGH_MANAGED_HOOK"

install_hook() {
	local hook_name="$1"
	local target="$HOOKS_DIR/$hook_name"

	if [ -f "$target" ] && grep -q "GITBUTLER_MANAGED_HOOK_V1" "$target"; then
		target="$HOOKS_DIR/${hook_name}-user"
	fi

	if [ -f "$target" ] && ! grep -q "$MARKER" "$target"; then
		local backup="${target}.bak"
		echo "Existing hook at $target was not installed by this script; backing it up to $backup"
		mv "$target" "$backup"
	fi

	cat >"$target" <<EOF
#!/bin/sh
$MARKER
# Installed by scripts/install-git-hooks.sh. Edit scripts/check.sh to
# change what this runs, then re-run the installer if this file itself
# needs to change.
exec "$REPO_ROOT/scripts/check.sh"
EOF
	chmod +x "$target"

	echo "Installed $hook_name hook at $target"
}

install_hook pre-commit
install_hook pre-push
