#!/usr/bin/env bash
set -euo pipefail

REPO="leonpham/golem"
BINARY="golem"

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
bold()  { printf '\033[1m%s\033[0m\n'  "$*"; }

die() { red "error: $*" >&2; exit 1; }

# ── OS / arch detection ───────────────────────────────────────────────────────

case "$(uname -s)" in
  Linux)  os=linux ;;
  Darwin) os=darwin ;;
  *)      die "Unsupported OS: $(uname -s). Build from source: https://github.com/$REPO" ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "Unsupported architecture: $(uname -m). Build from source: https://github.com/$REPO" ;;
esac

asset="${BINARY}_${os}_${arch}"

# ── Latest release tag ────────────────────────────────────────────────────────

bold "Fetching latest release …"

if command -v curl &>/dev/null; then
  fetch() { curl -fsSL "$1"; }
elif command -v wget &>/dev/null; then
  fetch() { wget -qO- "$1"; }
else
  die "curl or wget is required."
fi

tag=$(fetch "https://api.github.com/repos/$REPO/releases/latest" \
  | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')

[[ -n "$tag" ]] || die "Could not determine latest release tag. Check https://github.com/$REPO/releases"

url="https://github.com/$REPO/releases/download/$tag/$asset"

# ── Download ──────────────────────────────────────────────────────────────────

bold "Downloading golem $tag ($os/$arch) …"
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT

fetch "$url" > "$tmp" || die "Download failed. Check that $tag has a $os/$arch binary at:
  $url"

chmod +x "$tmp"

# ── Install location ──────────────────────────────────────────────────────────

if [[ -w /usr/local/bin ]]; then
  dest=/usr/local/bin/$BINARY
  mv "$tmp" "$dest"
elif command -v sudo &>/dev/null && sudo -n true 2>/dev/null; then
  dest=/usr/local/bin/$BINARY
  sudo mv "$tmp" "$dest"
else
  dest="$HOME/.local/bin/$BINARY"
  mkdir -p "$(dirname "$dest")"
  mv "$tmp" "$dest"
fi

# ── PATH check ────────────────────────────────────────────────────────────────

install_dir="$(dirname "$dest")"
if ! echo "$PATH" | tr ':' '\n' | grep -qx "$install_dir"; then
  cat >&2 <<EOF

$(red "warning: $install_dir is not on your PATH.")
Add it by appending this line to your shell profile (~/.bashrc, ~/.zshrc, etc.):

  export PATH="\$PATH:$install_dir"

Then restart your shell or run:  source ~/.bashrc
EOF
else
  green "golem $tag installed → $dest"
fi
