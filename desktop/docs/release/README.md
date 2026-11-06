# Linux release / installation

CI runs the backend gates with Rust 1.95.0 on Ubuntu 24.04 and a separate Linux packaging acceptance check. The packaging check builds checksum-verified fixture archives with stub executables, then exercises install, upgrade, uninstall, symlink refusal, and data retention in temporary homes. It validates the installer contract; it does not build or upload a release archive or verify compiled application binaries. The current workflow produces no downloadable release artifact.

## Install (per-user, Linux with systemd user manager)

These steps apply when a release bundle is available; the current CI workflow
does not produce one.

Download the artifact, verify the archive against its adjacent checksum (`sha256sum -c talos-desktop-linux-x86_64-*.tar.gz.sha256`), and extract it. From the extracted directory:

```sh
bash packaging/install-user.sh
systemctl --user daemon-reload
systemctl --user enable --now talosd.service
systemctl --user status talosd.service
~/.local/bin/talosdctl health
~/.local/bin/talos-desktop
```

Run as the desktop user, **never with sudo**. The installer replaces only `~/.local/bin/{talosd,talosdctl,talos-desktop}`, `~/.config/systemd/user/talosd.service`, and `~/.local/share/applications/talos-desktop.desktop`; it does not start/enable the service or touch daemon data. Re-running it upgrades those destinations. Before upgrading running binaries, stop the service with `systemctl --user stop talosd.service`, install the new files, then start it again. For manual operation without systemd, run `~/.local/bin/talosd` in a terminal, then use `~/.local/bin/talosdctl health` in another.

To uninstall, stop and disable the service, then run the bundle's uninstaller:

```sh
systemctl --user disable --now talosd.service
bash packaging/uninstall-user.sh
systemctl --user daemon-reload
```

The uninstaller removes only the five installed files listed above. It leaves `~/.talosd`, `~/.paseo`, and containing directories untouched. Both scripts refuse symlink destinations; the uninstaller checks all destinations before removing any file.

The **user** service runs as your account (`systemctl --user`, not `sudo systemctl`) and uses the daemon's default `$HOME/.talosd` for its private state, authentication token, instance guard, lock marker, and Unix socket. `talosdctl` defaults to the same home/socket; set `TALOSD_HOME` consistently for both processes if using a nondefault home (e.g., via a systemd user drop-in), and use `talosdctl --home PATH health` for a manual override. **Do not point it at `~/.paseo`**: that belongs to the legacy runtime and is not migrated by this installer. The systemd unit uses `%h/.local/bin/talosd`, sends SIGINT for graceful cleanup, and sets `KillMode=control-group` so systemd removes remaining service processes after a daemon crash.

On startup, talosd serializes recovery with a persistent private `talosd.guard` file lock. It removes a regular `talosd.lock` marker only when its recorded daemon PID is confirmed absent; unknown/importer markers and live or unverifiable PIDs fail closed. It removes `talosd.sock` only when it is a Unix socket and a connection is refused. Live, timed-out, inaccessible, symlink, and non-socket paths are preserved. This allows a normal process crash to restart without manual file deletion while avoiding guesses about active owners. The compiled-artifact acceptance exercises this restart path in a temporary home.

The desktop launcher resolves `talos-desktop` via the desktop session's `PATH`: ensure `~/.local/bin` is available there, or start the binary by absolute path. Fixture packaging acceptance uses stub binaries. A local compiled-artifact acceptance is also available after building Linux release binaries:

```sh
cargo build --release --locked -p talosd -p talosdctl -p talos-desktop --bins
python3 scripts/packaging-acceptance.py --compiled-artifact --verbose
```

It validates compiled install, replacement of prior binaries during upgrade, uninstall data retention, and daemon/CLI project, worktree, PTY hard-crash, and stale lock/socket restart behavior in temporary homes. It checks the packaged unit directives but does not launch the desktop UI, exercise a live systemd-user manager, or publish a release artifact. In the current acceptance environment, `systemctl --user is-system-running` could not run because `/bin/bash` reported `systemctl: command not found` (exit 127); no user service was started or changed. See [../STATUS.md](../STATUS.md) for evidence and remaining gates; this guide is not a claim that the current tree is release-ready.

## Build locally

```sh
cargo fmt --all -- --check
cargo clippy --workspace --all-targets --locked -- -D warnings
cargo test --workspace --locked
cargo build --release --locked -p talosd -p talosdctl -p talos-desktop
```

Requires Rust 1.95.0+ and Linux desktop development headers (see `.github/workflows/ci.yml`). Windows IPC is not supported yet; macOS and non-x86_64 Linux releases, signed packages, and distribution-specific installers are not produced by this pipeline. No cross-platform installation claim is made.
