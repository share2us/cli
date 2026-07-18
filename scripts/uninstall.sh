#!/bin/sh
# Share2Us CLI uninstaller. Reverses install.sh:
#   curl -fsSL https://share2.us/uninstall.sh | sh
# Removes the binary + `s2u` symlink, the PATH line install.sh added, and (by
# default) your config + credentials. Keep config with SHARE2US_KEEP_CONFIG=1.
set -eu

install_dir="${SHARE2US_INSTALL_DIR:-$HOME/.local/bin}"
binary_name="share2us"
alias_name="s2u"

log() { printf '%s\n' "$*"; }

removed_any=0

# 1. binary + the s2u symlink
for f in "$install_dir/$alias_name" "$install_dir/$binary_name"; do
  if [ -e "$f" ] || [ -L "$f" ]; then
    rm -f "$f" && { log "Removed $f"; removed_any=1; }
  fi
done

# 2. the PATH line install.sh appended to shell rc files
for rc in "$HOME/.bashrc" "$HOME/.zshrc" "$HOME/.profile"; do
  [ -f "$rc" ] || continue
  if grep -Fq "Share2Us CLI (added by install.sh)" "$rc" 2>/dev/null; then
    tmp="$(mktemp)"
    grep -Fv "Share2Us CLI (added by install.sh)" "$rc" \
      | grep -Fv "export PATH=\"${install_dir}:\$PATH\"" > "$tmp" || true
    cat "$tmp" > "$rc"
    rm -f "$tmp"
    log "Cleaned the Share2Us PATH line from $rc"
  fi
done

# 3. config + credentials (config.json, credentials.json under share2us/)
if [ "${SHARE2US_KEEP_CONFIG:-0}" = "1" ]; then
  log "Keeping config + credentials (SHARE2US_KEEP_CONFIG=1)."
else
  for d in \
    "${XDG_CONFIG_HOME:-$HOME/.config}/share2us" \
    "$HOME/.config/share2us" \
    "$HOME/Library/Application Support/share2us"; do
    if [ -d "$d" ]; then
      rm -rf "$d" && { log "Removed $d"; removed_any=1; }
    fi
  done
fi

log ""
if [ "$removed_any" -eq 1 ]; then
  log "Share2Us CLI uninstalled."
else
  log "Nothing to remove — the Share2Us CLI was not found in $install_dir."
  log "If you installed it elsewhere, set SHARE2US_INSTALL_DIR to that directory and re-run."
fi
