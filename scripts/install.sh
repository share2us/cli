#!/bin/sh
set -eu

repo="${SHARE2US_INSTALL_REPO:-share2us/cli}"
version="${SHARE2US_VERSION:-latest}"
base_url="${SHARE2US_INSTALL_BASE_URL:-https://share2.us}"
install_dir="${SHARE2US_INSTALL_DIR:-$HOME/.local/bin}"
binary_name="share2us"
alias_name="s2u"

log() {
  printf '%s\n' "$*"
}

fail() {
  printf 'share2us install: %s\n' "$*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

detect_os() {
  case "$(uname -s)" in
    Linux) printf linux ;;
    Darwin) printf darwin ;;
    *) fail "unsupported OS: $(uname -s)" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) printf amd64 ;;
    aarch64|arm64) printf arm64 ;;
    *) fail "unsupported architecture: $(uname -m)" ;;
  esac
}

download_to() {
  download_url="$1"
  download_dest="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$download_url" -o "$download_dest"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$download_dest" "$download_url"
  else
    fail "missing curl or wget"
  fi
}

verify_crc() {
  crc_file="$1"
  crc_sidecar="$2"
  set -- $(cksum "$crc_file")
  actual_crc="${1:-}"
  actual_size="${2:-}"
  set -- $(sed -n '1p' "$crc_sidecar")
  expected_crc="${1:-}"
  expected_size="${2:-}"
  if [ -z "$expected_crc" ] || [ -z "$expected_size" ]; then
    fail "CRC sidecar is invalid for ${crc_file##*/}"
  fi
  if [ "$actual_crc" != "$expected_crc" ] || [ "$actual_size" != "$expected_size" ]; then
    fail "CRC check failed for ${crc_file##*/}"
  fi
}

download_verified() {
  verified_dest="$1"
  verified_sidecar="$2"
  shift 2
  for verified_url in "$@"; do
    log "Trying $verified_url"
    if download_to "$verified_url" "$verified_dest" && download_to "$verified_url.crc32" "$verified_sidecar"; then
      verify_crc "$verified_dest" "$verified_sidecar"
      log "CRC check passed for ${verified_dest##*/}"
      return 0
    fi
  done
  return 1
}

# s2u is a real symlink to the installed binary (works in scripts + non-interactive
# shells), not just a shell alias.
install_links() {
  ln -sfn "$install_dir/$binary_name" "$install_dir/$alias_name"
}

# ensure_path makes install_dir usable by adding it to the user's PATH. Only the
# shell alias was added before, which never fixed "command not found" when
# ~/.local/bin isn't already on PATH (the common case on fresh WSL/Ubuntu).
ensure_path() {
  path_needs_reload=0
  case ":${PATH}:" in
    *":${install_dir}:"*) return 0 ;; # already on PATH — nothing to do
  esac
  path_needs_reload=1
  line="export PATH=\"${install_dir}:\$PATH\""
  wrote=0
  for rc in "$HOME/.bashrc" "$HOME/.zshrc" "$HOME/.profile"; do
    [ -f "$rc" ] || continue
    wrote=1
    if ! grep -Fq "$install_dir" "$rc" 2>/dev/null; then
      printf '\n# Share2Us CLI (added by install.sh)\n%s\n' "$line" >> "$rc"
    fi
  done
  # No shell rc yet? Create ~/.profile so login shells pick it up.
  if [ "$wrote" -eq 0 ]; then
    printf '# Share2Us CLI (added by install.sh)\n%s\n' "$line" >> "$HOME/.profile"
  fi
}

need uname
need mktemp
need tar
need grep
need chmod
need mkdir
need cp
need ln
need find
need head
need cksum
need sed

os="$(detect_os)"
arch="$(detect_arch)"
archive="share2us_${os}_${arch}.tar.gz"

if [ "$version" = "latest" ]; then
  hosted_url="${base_url%/}/downloads/$archive"
  github_url="https://github.com/$repo/releases/latest/download/$archive"
else
  hosted_url="${base_url%/}/downloads/$version/$archive"
  github_url="https://github.com/$repo/releases/download/$version/$archive"
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT INT TERM

log "Downloading Share2Us CLI for $os/$arch..."
# GitHub Releases is the source of truth (built by CI for every platform); the
# hosted mirror on share2.us/downloads is a fallback.
download_verified "$tmpdir/$archive" "$tmpdir/$archive.crc32" "$github_url" "$hosted_url" || fail "could not download and verify $archive"

tar -xzf "$tmpdir/$archive" -C "$tmpdir"
if [ -f "$tmpdir/$binary_name" ]; then
  bin="$tmpdir/$binary_name"
else
  bin="$(find "$tmpdir" -type f -name "$binary_name" | head -n 1 || true)"
fi
[ -n "$bin" ] || fail "archive did not contain $binary_name"

mkdir -p "$install_dir"
chmod 0755 "$bin"
cp "$bin" "$install_dir/$binary_name"
install_links
ensure_path

log ""
log "Installed the Share2Us CLI to $install_dir:"
log "  $alias_name     <- short command (use this)"
log "  $binary_name    (same tool, long name)"
if [ "${path_needs_reload:-0}" = "1" ]; then
  log ""
  log "Added $install_dir to your PATH for new shells. To use it in THIS shell now:"
  log "  export PATH=\"$install_dir:\$PATH\""
  log "  (or just open a new terminal)"
fi
log ""
log "Get started:  $alias_name login"
