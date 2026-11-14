#!/bin/sh
# claude-hub installer: an unofficial profile launcher for Claude Code.
#
#   curl -fsSL https://raw.githubusercontent.com/wonsong82/claude-hub/main/install.sh | sh
#
# Downloads the release archive for this OS and architecture plus checksums.txt over HTTPS,
# verifies the SHA-256, and installs the single file claude-hub into
# ${CLAUDE_HUB_INSTALL_DIR:-$HOME/.local/bin} by rename, then installs the `claude` shim
# (claude-hub install-shim: a link plus a marked block at the end of each shell's rc files).
# No sudo.
#
#   CLAUDE_HUB_VERSION=vX.Y.Z   install that version instead of the latest
#   CLAUDE_HUB_INSTALL_DIR=DIR  install there
#   CLAUDE_HUB_RELEASE_BASE=URL release base URL (default: the GitHub releases of the repo)
#
# The whole body is a function called on the last line, so a truncated download runs nothing.

main() {
  set -eu
  repo_base="${CLAUDE_HUB_RELEASE_BASE:-https://github.com/wonsong82/claude-hub/releases}"
  dir="${CLAUDE_HUB_INSTALL_DIR:-$HOME/.local/bin}"
  version="${CLAUDE_HUB_VERSION:-}"

  say() { printf 'claude-hub install: %s\n' "$*" >&2; }
  die() { say "$*"; exit 1; }
  need() { command -v "$1" >/dev/null 2>&1 || die "needs $1"; }

  need uname
  need tar
  need mktemp
  if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL --proto '=https,http' -o "$2" "$1"; }
    latest() { curl -fsSI -o /dev/null -w '%{redirect_url}' "$repo_base/latest"; }
  elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -q -O "$2" "$1"; }
    latest() { wget -S --max-redirect=0 -O /dev/null "$repo_base/latest" 2>&1 | sed -n 's/^ *[Ll]ocation: *//p' | tr -d '\r' | tail -1; }
  else
    die "needs curl or wget"
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    sha() { sha256sum "$1" | cut -d' ' -f1; }
  elif command -v shasum >/dev/null 2>&1; then
    sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
  else
    die "needs sha256sum or shasum"
  fi

  case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) die "unsupported OS $(uname -s) (macOS and Linux only; on Windows use WSL)" ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) die "unsupported architecture $(uname -m)" ;;
  esac

  if [ -z "$version" ]; then
    loc="$(latest || true)"
    version="${loc##*/tag/}"
    [ -n "$loc" ] && [ "$version" != "$loc" ] || die "cannot find the latest release"
  fi
  case "$version" in v*) ;; *) version="v$version" ;; esac
  archive="claude-hub_${version#v}_${os}_${arch}.tar.gz"

  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT INT TERM
  say "downloading $archive ($version)"
  fetch "$repo_base/download/$version/checksums.txt" "$tmp/checksums.txt" || die "cannot download checksums.txt"
  fetch "$repo_base/download/$version/$archive" "$tmp/$archive" || die "cannot download $archive"

  want="$(awk -v f="$archive" '$2 == f || $2 == "*"f { print $1 }' "$tmp/checksums.txt")"
  [ -n "$want" ] || die "$archive is not in checksums.txt"
  got="$(sha "$tmp/$archive")"
  [ "$want" = "$got" ] || die "checksum mismatch for $archive; nothing was installed"

  mkdir -p "$tmp/x"
  tar -xzf "$tmp/$archive" -C "$tmp/x" claude-hub || die "the archive has no claude-hub"
  mkdir -p "$dir"
  cp "$tmp/x/claude-hub" "$dir/.claude-hub.new.$$"
  chmod 755 "$dir/.claude-hub.new.$$"
  mv -f "$dir/.claude-hub.new.$$" "$dir/claude-hub"
  say "installed $dir/claude-hub ($version)"

  # The shim is not optional: it is how `claude` reaches claude-hub.
  say "installing the claude shim"
  "$dir/claude-hub" install-shim </dev/null || die "the claude shim could not be installed; run \"$dir/claude-hub install-shim\" to see why"

  case ":$PATH:" in
    *":$dir:"*) ;;
    *) say "$dir is not on PATH; add this line to your shell profile:"
       printf '  export PATH="%s:$PATH"\n' "$dir" >&2 ;;
  esac
  say "next: claude-hub init"
}

main "$@"
