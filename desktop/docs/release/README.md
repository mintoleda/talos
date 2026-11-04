# Linux release / installation

CI builds and tests the locked workspace with Rust 1.95.0 on Ubuntu 24.04. Each run uploads a `talos-desktop-linux-x86_64` artifact containing a tarball and a `.sha256` file. This is a Linux x86_64 **build artifact**, not a signed package or an auto-published GitHub release. The tarball includes `talosd`, `talosdctl`, the `talos-desktop` egui app, packaging assets, and this guide.

## Install (per-user, Linux with systemd user manager)

Download the artifact, verify the archive against its adjacent checksum (`sha256sum -c talos-desktop-linux-x86_64-*.tar.gz.sha256`), and extract it. From the extracted directory:

```sh
bash packaging/install-user.sh
systemctl --user daemon-reload
systemctl --user enable --now talosd.service
systemctl --user status talosd.service
~/.local/bin/talosdctl health
~/.local/bin/talos-desktop
```

Run as the desktop user, **never with sudo**. The installer replaces only `~/.local/bin/{talosd,talosdctl,talos-desktop}`, `~/.config/systemd/user/talosd.service`, and `~/.local/share/applications/talos-desktop.desktop`; it does not start/enable the service or touch daemon data. Re-running it is safe for those destinations; stop `talosd.service` before upgrading running binaries, then restart it after installation. For manual operation without systemd, run `~/.local/bin/talosd` in a terminal, then use `~/.local/bin/talosdctl health` in another.

The **user** service runs as your account (`systemctl --user`, not `sudo systemctl`) and uses the daemon's default `$HOME/.talosd` for its private state, authentication token, instance lock, and Unix socket. `talosdctl` defaults to the same home/socket; set `TALOSD_HOME` consistently for both processes if using a nondefault home (e.g., via a systemd user drop-in), and use `talosdctl --home PATH health` for a manual override. **Do not point it at `~/.paseo`**: that belongs to the legacy runtime and is not migrated by this installer. The systemd unit uses `%h/.local/bin/talosd` and sends SIGINT for graceful socket/lock cleanup. If a crash leaves a stale lock or socket, verify no daemon is running before removing those files from `.talosd`; automatic stale-file deletion is intentionally disabled.

The desktop launcher resolves `talos-desktop` via the desktop session's `PATH`: ensure `~/.local/bin` is available there, or start the binary by absolute path. The current working tree contains desktop daemon connection/autostart code, but live desktop acceptance has not been established; use `talosdctl` for backend/CLI validation. Talos/ACP is the primary provider and Claude Code the only secondary backend. Desktop integration and visual QA are deferred until the final local-MVP phase. See [../STATUS.md](../STATUS.md) for current evidence and failing workspace gates; this installation guide is not a claim that the current tree is release-ready.

## Build locally

```sh
cargo fmt --all -- --check
cargo clippy --workspace --all-targets --locked -- -D warnings
cargo test --workspace --locked
cargo build --release --locked -p talosd -p talosdctl -p talos-desktop
```

Requires Rust 1.95.0+ and Linux desktop development headers (see `.github/workflows/ci.yml`). Windows IPC is not supported yet; macOS and non-x86_64 Linux releases, signed packages, and distribution-specific installers are not produced by this pipeline. No cross-platform installation claim is made.
