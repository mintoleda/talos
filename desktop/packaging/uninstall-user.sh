#!/usr/bin/env bash
# Remove only the files installed by the per-user Linux release bundle.
set -euo pipefail

if [[ $# -ne 0 ]]; then
  printf 'Usage: %s\nRemoves the Talos desktop user binaries and integration files.\n' "$0" >&2
  exit 2
fi
if (( EUID == 0 )); then
  echo 'Run as the target desktop user, not as root or with sudo' >&2
  exit 1
fi
if [[ -z ${HOME:-} || $HOME != /* || $HOME == / || $HOME == *$'\n'* ]]; then
  echo 'HOME must be an absolute user home without newlines' >&2
  exit 1
fi

bin="$HOME/.local/bin"
service="$HOME/.config/systemd/user/talosd.service"
desktop="$HOME/.local/share/applications/talos-gpui.desktop"
targets=(
  "$bin/talosd"
  "$bin/talosdctl"
  "$bin/talos-gpui"
  "$service"
  "$desktop"
)

# Check all path components before removing anything. This prevents following a
# redirected parent directory and avoids a partially completed uninstall.
for path in "$HOME/.local" "$bin" "$HOME/.local/share" \
  "$(dirname -- "$desktop")" "$HOME/.config" "$HOME/.config/systemd" \
  "$(dirname -- "$service")" "${targets[@]}"; do
  if [[ -L "$path" ]]; then
    printf 'Refusing symlink destination: %s\n' "$path" >&2
    exit 1
  fi
done

for path in "${targets[@]}"; do
  if [[ -e "$path" && ! -f "$path" ]]; then
    printf 'Refusing non-file destination: %s\n' "$path" >&2
    exit 1
  fi
done

removed=0
for path in "${targets[@]}"; do
  if [[ -f "$path" ]]; then
    rm -- "$path"
    printf 'Removed %s\n' "$path"
    removed=$((removed + 1))
  fi
done

printf 'Removed %d installed file(s). Daemon data in %s/.talosd was left untouched.\n' "$removed" "$HOME"
