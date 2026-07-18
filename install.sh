#!/bin/sh
set -eu

# yaah installer — one-liner:
#   curl -fsSL https://raw.githubusercontent.com/buchenberg/yaah/main/install.sh | sh

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BOLD='\033[1m'
NC='\033[0m' # no color

info()  { printf "  ${GREEN}==>${NC} %s\n" "$1"; }
warn()  { printf "  ${YELLOW}WARN${NC} %s\n" "$1"; }
err()   { printf "  ${RED}ERROR${NC} %s\n" "$1"; exit 1; }

REPO="buchenberg/yaah"

# --- platform detection ---
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$OS" in
    linux)  GOOS="linux" ;;
    darwin) GOOS="darwin" ;;
    *)      err "unsupported OS: $OS" ;;
esac

case "$ARCH" in
    x86_64|amd64) GOARCH="amd64" ;;
    aarch64|arm64) GOARCH="arm64" ;;
    *)            err "unsupported architecture: $ARCH" ;;
esac

BINARY="yaah-${GOOS}-${GOARCH}"
if [ "$GOOS" = "windows" ]; then
    BINARY="${BINARY}.exe"
fi

# --- choose install dir ---
if [ -w "/usr/local/bin" ]; then
    INSTALL_DIR="/usr/local/bin"
elif [ -w "$HOME/.local/bin" ] || [ -d "$HOME/.local/bin" ]; then
    INSTALL_DIR="$HOME/.local/bin"
    mkdir -p "$INSTALL_DIR"
else
    INSTALL_DIR="$HOME/.local/bin"
    mkdir -p "$INSTALL_DIR"
fi

# check if INSTALL_DIR is in PATH
case ":$PATH:" in
    *:"$INSTALL_DIR":*) ;;
    *)
        warn "$INSTALL_DIR is not in your PATH. Add it to your shell config:"
        warn "  export PATH=\"\$HOME/.local/bin:\$PATH\""
        ;;
esac

# --- download ---
DOWNLOAD_URL="https://github.com/${REPO}/releases/latest/download/${BINARY}"
info "downloading yaah for ${GOOS}/${GOARCH}..."
info "  $DOWNLOAD_URL"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$DOWNLOAD_URL" -o "$TMP/yaah" || err "download failed — is the release binary uploaded?"
elif command -v wget >/dev/null 2>&1; then
    wget -q "$DOWNLOAD_URL" -O "$TMP/yaah" || err "download failed — is the release binary uploaded?"
else
    err "neither curl nor wget found"
fi

chmod +x "$TMP/yaah"

# macOS: avoid Gatekeeper quarantine with ditto
if [ "$GOOS" = "darwin" ] && command -v ditto >/dev/null 2>&1; then
    ditto --norsrc "$TMP/yaah" "$INSTALL_DIR/yaah"
else
    mv "$TMP/yaah" "$INSTALL_DIR/yaah"
fi

info "installed yaah to ${INSTALL_DIR}/yaah"

# --- verify ---
if ! command -v yaah >/dev/null 2>&1; then
    warn "yaah not found in PATH yet. You may need to restart your shell or run:"
    warn "  export PATH=\"\$HOME/.local/bin:\$PATH\""
    YAAH_BIN="$INSTALL_DIR/yaah"
else
    YAAH_BIN="yaah"
fi

printf "\n"
"$YAAH_BIN" version 2>/dev/null || true

# --- scaffold config ---
if [ ! -f "$HOME/.yaah/config.yaml" ]; then
    info "scaffolding config at ~/.yaah/config.yaml..."
    "$YAAH_BIN" config edit </dev/null 2>/dev/null || true
fi

printf "\n"
info "yaah is installed. Next steps:"
printf "  ${BOLD}yaah doctor${NC}           # check your setup\n"
printf "  ${BOLD}yaah config edit${NC}       # add your API key\n"
printf "  ${BOLD}yaah${NC}                   # start the REPL\n"
printf "  ${BOLD}yaah \"your prompt\"${NC}   # one-shot\n"
printf "\n"
