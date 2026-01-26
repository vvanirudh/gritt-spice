#!/bin/bash
#
# Install script for git-spice (gs).
# This script installs gs from source and configures PATH in the
# appropriate shell rc file (.zshrc for zsh, .bashrc for bash).
#
# Usage:
#   ./install.sh
#
# Requirements:
#   - Go 1.21 or later
#

set -euo pipefail

# Colors for output (disabled if not a terminal).
if [ -t 1 ]; then
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[0;33m'
    NC='\033[0m' # No Color
else
    RED=''
    GREEN=''
    YELLOW=''
    NC=''
fi

info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

error() {
    echo -e "${RED}[ERROR]${NC} $1" >&2
}

die() {
    error "$1"
    exit 1
}

# Check if Go is installed.
check_go() {
    if ! command -v go &> /dev/null; then
        die "Go is not installed. Please install Go 1.21 or later from https://go.dev/dl"
    fi

    # Verify Go version is at least 1.21.
    local go_version
    go_version=$(go version | grep -oE 'go[0-9]+\.[0-9]+' | sed 's/go//')
    local major minor
    major=$(echo "$go_version" | cut -d. -f1)
    minor=$(echo "$go_version" | cut -d. -f2)

    if [ "$major" -lt 1 ] || { [ "$major" -eq 1 ] && [ "$minor" -lt 21 ]; }; then
        die "Go version 1.21 or later is required. Found: go$go_version"
    fi

    info "Found Go version: go$go_version"
}

# Determine GOBIN directory.
# Priority:
#   1. $GOBIN if set
#   2. $GOPATH/bin if GOPATH is set
#   3. $HOME/go/bin (Go default)
get_gobin() {
    if [ -n "${GOBIN:-}" ]; then
        echo "$GOBIN"
    elif [ -n "${GOPATH:-}" ]; then
        echo "$GOPATH/bin"
    else
        echo "$HOME/go/bin"
    fi
}

# Install gs using go install.
install_gs() {
    info "Installing git-spice..."
    go install -v -tags="" go.abhg.dev/gs
    info "git-spice installed successfully."
}

# Detect the user's login shell.
# When running under sudo, $SHELL reflects root's shell, not the user's.
# This function tries multiple methods to find the real user's shell.
get_user_shell() {
    local real_user shell_path

    # Determine the real user (handles sudo).
    real_user="${SUDO_USER:-$USER}"

    # Try to get login shell from passwd database.
    # Works on Linux (getent) and macOS (dscl).
    if command -v getent &> /dev/null; then
        shell_path=$(getent passwd "$real_user" | cut -d: -f7)
    elif command -v dscl &> /dev/null; then
        shell_path=$(dscl . -read "/Users/$real_user" UserShell 2>/dev/null \
            | awk '{print $2}')
    fi

    # Fall back to $SHELL if lookup failed.
    echo "${shell_path:-${SHELL:-/bin/bash}}"
}

# Detect the user's shell and return the appropriate rc file.
get_shell_rc() {
    local shell_path shell_name real_user home_dir

    shell_path=$(get_user_shell)
    shell_name=$(basename "$shell_path")

    # Determine the real user's home directory (handles sudo).
    real_user="${SUDO_USER:-$USER}"
    if [ -n "${SUDO_USER:-}" ]; then
        # Get home directory for the real user, not root.
        if command -v getent &> /dev/null; then
            home_dir=$(getent passwd "$real_user" | cut -d: -f6)
        elif command -v dscl &> /dev/null; then
            home_dir=$(dscl . -read "/Users/$real_user" NFSHomeDirectory \
                2>/dev/null | awk '{print $2}')
        fi
        home_dir="${home_dir:-$HOME}"
    else
        home_dir="$HOME"
    fi

    case "$shell_name" in
        zsh)
            echo "$home_dir/.zshrc"
            ;;
        bash)
            echo "$home_dir/.bashrc"
            ;;
        *)
            # Default to .bashrc for unknown shells.
            warn "Unknown shell '$shell_name', defaulting to .bashrc"
            echo "$home_dir/.bashrc"
            ;;
    esac
}

# Add GOBIN to PATH in the shell rc file if not already present.
configure_shell_rc() {
    local gobin="$1"
    local rc_file
    rc_file=$(get_shell_rc)
    local rc_name
    rc_name=$(basename "$rc_file")
    local export_line="export PATH=\"$gobin:\$PATH\""

    # Check if rc file exists.
    if [ ! -f "$rc_file" ]; then
        warn "$rc_name not found. Creating it."
        touch "$rc_file"
    fi

    # Check if GOBIN is already in PATH via rc file.
    if grep -qF "$gobin" "$rc_file" 2>/dev/null; then
        info "GOBIN ($gobin) is already configured in $rc_name"
        return
    fi

    # Append export line to rc file.
    {
        echo ""
        echo "# Added by git-spice install script"
        echo "$export_line"
    } >> "$rc_file"

    info "Added GOBIN to PATH in $rc_name"
    warn "Run 'source ~/$rc_name' or start a new shell to update your PATH."
}

# Check if gs is accessible in PATH.
verify_installation() {
    local gobin="$1"

    if [ -x "$gobin/gs" ]; then
        info "gs binary installed at: $gobin/gs"
    else
        warn "gs binary not found at expected location: $gobin/gs"
    fi
}

main() {
    info "Starting git-spice installation..."

    check_go

    local gobin
    gobin=$(get_gobin)
    info "GOBIN directory: $gobin"

    install_gs
    configure_shell_rc "$gobin"
    verify_installation "$gobin"

    echo ""
    info "Installation complete!"
    info "Run 'gs --help' to get started."
}

main "$@"
