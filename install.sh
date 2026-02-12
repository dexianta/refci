#!/usr/bin/env bash
set -euo pipefail

# Usage examples:
#   curl -fsSL https://raw.githubusercontent.com/dexianta/nci/main/install.sh | bash
#   NCI_REF=v0.1.0 curl -fsSL .../install.sh | bash
#   NCI_INSTALL_DIR="$HOME/bin" curl -fsSL .../install.sh | bash

NCI_REPO="${NCI_REPO:-https://github.com/dexianta/nci.git}"
NCI_REF="${NCI_REF:-main}"
NCI_INSTALL_DIR="${NCI_INSTALL_DIR:-}"

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "error: missing required command: $1" >&2
    exit 1
  fi
}

need_cmd git
need_cmd go
need_cmd mktemp

pick_install_dir() {
  if [[ -n "$NCI_INSTALL_DIR" ]]; then
    echo "$NCI_INSTALL_DIR"
    return
  fi

  if [[ -d /usr/local/bin && -w /usr/local/bin ]]; then
    echo "/usr/local/bin"
    return
  fi

  echo "$HOME/.local/bin"
}

INSTALL_DIR="$(pick_install_dir)"
mkdir -p "$INSTALL_DIR"

TMP_DIR="$(mktemp -d)"
cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

echo "==> Cloning nci ($NCI_REF)"
git clone --depth 1 --branch "$NCI_REF" "$NCI_REPO" "$TMP_DIR/src"

cd "$TMP_DIR/src"

echo "==> Building nci"
go build -o "$TMP_DIR/nci" ./cmd/cli

echo "==> Installing to $INSTALL_DIR/nci"
install -m 0755 "$TMP_DIR/nci" "$INSTALL_DIR/nci"

echo "==> Installed: $INSTALL_DIR/nci"
if ! command -v nci >/dev/null 2>&1; then
  case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *)
      echo "note: add this to your shell profile:" >&2
      echo "  export PATH=\"$INSTALL_DIR:\$PATH\"" >&2
      ;;
  esac
fi

echo "done"
