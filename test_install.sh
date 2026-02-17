#!/usr/bin/env bash
#
# Sandbox tests for install.sh using Docker.
# Each test runs in an isolated container to verify install.sh behavior.
#
# Usage:
#   bash test_install.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_SH="$SCRIPT_DIR/install.sh"

# Colors (disabled when not a terminal).
if [ -t 1 ]; then
    GREEN='\033[0;32m'
    RED='\033[0;31m'
    BOLD='\033[1m'
    NC='\033[0m'
else
    GREEN='' RED='' BOLD='' NC=''
fi

PASS=0
FAIL=0

# Shared harness loaded at the top of every test script.
# Loads all install.sh functions without running main,
# and stubs install_gs to create a fake binary so tests do not
# need the package published on the internet.
read -r -d '' HARNESS << 'HARNESS_EOF' || true
set -euo pipefail

# Load all functions from install.sh without running main.
eval "$(sed '/^main "\$@"$/d' /install.sh)"

# Stub install_gs: creates a fake gs binary in GOBIN.
install_gs() {
    local gobin
    gobin=$(get_gobin)
    mkdir -p "$gobin"
    printf '#!/bin/sh\necho "gs-test-stub"\n' > "$gobin/gs"
    chmod +x "$gobin/gs"
    info "git-spice installed (stub)."
}
HARNESS_EOF

# run_docker_test IMAGE NAME SCRIPT
# Prepends HARNESS to SCRIPT and runs the combined script inside IMAGE.
run_docker_test() {
    local image="$1"
    local name="$2"
    local script="$3"

    echo ""
    echo -e "${BOLD}--- $name ---${NC}"

    local tmp
    tmp=$(mktemp)
    printf '%s\n%s\n' "$HARNESS" "$script" > "$tmp"

    local status=0
    docker run --rm \
        -v "$INSTALL_SH:/install.sh:ro" \
        -v "$tmp:/test.sh:ro" \
        "$image" \
        bash /test.sh || status=$?

    rm -f "$tmp"

    if [ "$status" -eq 0 ]; then
        echo -e "${GREEN}PASS${NC}: $name"
        PASS=$((PASS + 1))
    else
        echo -e "${RED}FAIL${NC}: $name (exit code $status)"
        FAIL=$((FAIL + 1))
    fi
}

# ── Test 1: Fresh install ─────────────────────────────────────────────────────
# Plain Ubuntu with no Go — the script must download and install Go itself.
run_docker_test "ubuntu:24.04" \
    "fresh install: Go auto-installed when missing" \
'
# Install tools the script depends on.
apt-get update -qq
apt-get install -y -qq curl tar ca-certificates > /dev/null 2>&1

main

# Go binary must be installed at the expected path.
test -x /usr/local/go/bin/go \
    || { echo "ERROR: /usr/local/go/bin/go not found"; exit 1; }

# The current session PATH must include the Go toolchain (exported
# by install_go so the rest of the script can use go immediately).
echo "$PATH" | grep -q "/usr/local/go/bin" \
    || { echo "ERROR: /usr/local/go/bin not in session PATH"; exit 1; }

# The shell rc file must record the Go toolchain path for future sessions.
grep -q "/usr/local/go/bin" ~/.bashrc \
    || { echo "ERROR: /usr/local/go/bin not added to .bashrc"; exit 1; }

# The gs stub must be present in GOBIN.
gobin=$(get_gobin)
test -x "$gobin/gs" \
    || { echo "ERROR: gs not found at $gobin/gs"; exit 1; }

# GOBIN must also be recorded in the shell rc.
grep -q "$gobin" ~/.bashrc \
    || { echo "ERROR: GOBIN ($gobin) not in .bashrc"; exit 1; }

echo "All checks passed."
'

# ── Test 2: Go pre-installed ──────────────────────────────────────────────────
# Official Go image — install_go must not be called and
# /usr/local/go/bin must not be written to the shell rc.
run_docker_test "golang:1.23" \
    "go pre-installed: installation skipped" \
'
# Intercept install_go to detect any unwanted invocation.
INSTALL_GO_CALLED=false
install_go() { INSTALL_GO_CALLED=true; }

main

if [ "$INSTALL_GO_CALLED" = "true" ]; then
    echo "ERROR: install_go was called despite Go being already installed"
    exit 1
fi

# gs stub must still be installed.
gobin=$(get_gobin)
test -x "$gobin/gs" \
    || { echo "ERROR: gs not found at $gobin/gs"; exit 1; }

# configure_go_rc must NOT have run since Go was already present.
if grep -q "/usr/local/go/bin" ~/.bashrc 2>/dev/null; then
    echo "ERROR: /usr/local/go/bin was added to .bashrc unnecessarily"
    exit 1
fi

echo "All checks passed."
'

# ── Test 3: Idempotent rc update ──────────────────────────────────────────────
# Running the script twice must not produce duplicate PATH entries.
run_docker_test "golang:1.23" \
    "idempotent: rc not updated twice on re-run" \
'
main
main  # second run

gobin=$(get_gobin)
count=$(grep -c "$gobin" ~/.bashrc || true)
if [ "$count" -ne 1 ]; then
    echo "ERROR: expected 1 GOBIN entry in .bashrc, found $count"
    echo "--- .bashrc contents ---"
    cat ~/.bashrc
    exit 1
fi

echo "All checks passed."
'

# ── Summary ───────────────────────────────────────────────────────────────────
echo ""
echo -e "${BOLD}Results: ${GREEN}${PASS} passed${NC}${BOLD}, ${RED}${FAIL} failed${NC}"
[ "$FAIL" -eq 0 ]
