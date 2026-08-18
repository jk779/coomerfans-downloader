#!/usr/bin/env bash
set -euo pipefail

REPO="jk779/coomerfans-downloader"
INSTALL_DIR="$HOME/.local/bin"
INSTALL_PATH="$INSTALL_DIR/coomerfans-downloader"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m | tr '[:upper:]' '[:lower:]')"

case "$ARCH" in
  x86_64|amd64)  ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

case "$OS/$ARCH" in
  darwin/arm64) ASSET="coomerfans-downloader-darwin-arm64" ;;
  linux/amd64)  ASSET="coomerfans-downloader-linux-amd64" ;;
  linux/arm64)  ASSET="coomerfans-downloader-linux-arm64" ;;
  *) echo "Unsupported platform: $OS/$ARCH"; exit 1 ;;
esac

URL="https://github.com/$REPO/releases/latest/download/$ASSET"

echo "Installing coomerfans-downloader..."
echo "  Platform: $OS/$ARCH"
echo "  Source:   $URL"
echo "  Target:   $INSTALL_PATH"
echo

mkdir -p "$INSTALL_DIR"

TMP="$(mktemp "$INSTALL_DIR/.coomerfans-downloader.XXXXXX")"
trap 'rm -f "$TMP"' EXIT

curl -fL "$URL" -o "$TMP"

chmod 0755 "$TMP"
mv -f "$TMP" "$INSTALL_PATH"

trap - EXIT

echo
echo "Installed:"
echo "  $INSTALL_PATH"

case ":${PATH:-}:" in
  *":$INSTALL_DIR:"*)
    echo
    echo "Done. ~/.local/bin is already in PATH."
    exit 0
    ;;
esac

echo
echo "~/.local/bin is not currently in PATH."

case "$(basename "${SHELL:-}")" in
  zsh)
    PROFILE="$HOME/.zprofile"
    ;;
  bash)
    if [[ "$OS" == "darwin" ]]; then
      PROFILE="$HOME/.bash_profile"
    else
      PROFILE="$HOME/.bashrc"
    fi
    ;;
  *)
    echo
    echo "Your shell is not modified automatically."
    echo "Add this to your shell profile if desired:"
    echo
    echo '  export PATH="$HOME/.local/bin:$PATH"'
    exit 0
    ;;
esac

PATH_LINE='export PATH="$HOME/.local/bin:$PATH"'

if [[ -f "$PROFILE" ]] && grep -Fqx "$PATH_LINE" "$PROFILE"; then
  echo
  echo "$PROFILE already contains the required PATH entry."
  echo "Open a new terminal session for it to take effect."
  exit 0
fi

echo
printf 'Add ~/.local/bin to PATH in %s? [y/N] ' "$PROFILE" > /dev/tty

ANSWER=""
IFS= read -r ANSWER < /dev/tty || true

case "$ANSWER" in
  y|Y|yes|YES|Yes)
    printf '\n%s\n' "$PATH_LINE" >> "$PROFILE"

    echo
    echo "Added PATH entry to:"
    echo "  $PROFILE"
    echo
    echo "Open a new terminal session for it to take effect."
    ;;
  *)
    echo
    echo "PATH was not modified."
    echo "You can still run the tool directly:"
    echo "  $INSTALL_PATH"
    ;;
esac