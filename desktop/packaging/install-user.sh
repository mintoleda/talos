#!/usr/bin/env bash
# Install an extracted Linux release bundle for the current user only.
set -euo pipefail

if [[ $# -ne 0 ]]; then
  printf 'Usage: %s\nRun from an extracted release bundle; installs to ~/.local and ~/.config.\n' "$0" >&2
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

bundle=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
for binary in talosd talosdctl talos-desktop; do
  if [[ ! -f "$bundle/$binary" || ! -x "$bundle/$binary" ]]; then
    printf 'Missing executable: %s\n' "$bundle/$binary" >&2
    exit 1
  fi
done

bin="$HOME/.local/bin"
service="$HOME/.config/systemd/user/talosd.service"
desktop="$HOME/.local/share/applications/talos-desktop.desktop"
# Never write through a symlink to an unexpected location.
for path in "$HOME/.local" "$bin" "$HOME/.local/share" "$(dirname -- "$desktop")" \
  "$HOME/.config" "$HOME/.config/systemd" "$(dirname -- "$service")" \
  "$bin/talosd" "$bin/talosdctl" "$bin/talos-desktop" "$service" "$desktop"; do
  if [[ -L "$path" ]]; then
    printf 'Refusing symlink destination: %s\n' "$path" >&2
    exit 1
  fi
done

install -d -m 755 -- "$bin" "$(dirname -- "$desktop")" "$(dirname -- "$service")"
for binary in talosd talosdctl talos-desktop; do
  install -m 755 -- "$bundle/$binary" "$bin/$binary"
done
install -m 644 -- "$bundle/packaging/systemd/talosd.service" "$service"
install -m 644 -- "$bundle/packaging/desktop/talos-desktop.desktop" "$desktop"

printf 'Installed in %s. Add it to your desktop session PATH for the launcher.\n' "$bin"
printf 'Run: systemctl --user daemon-reload && systemctl --user enable --now talosd.service\n'
printf 'Check: %s/talosdctl health; launch desktop: %s/talos-desktop\n' "$bin" "$bin"
