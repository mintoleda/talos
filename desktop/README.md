# Talos desktop (Rust)

The native Rust desktop for Talos: a polished `egui` interface and a provider-neutral agent control plane. It lives in the Talos monorepo under `desktop/`; the Go agent runtime is at the repository root.

## Direction

- **Talos is the primary backend**, launched as `talos acp` and driven over Agent Client Protocol (ACP).
- **Claude Code is the only secondary provider**, connected through the official Claude Agent ACP adapter.
- The desktop app, daemon, CLI, storage, provider supervision, and UI are implemented in Rust. The agent loop, tools, transcripts and safety policy stay in Go.
- This app is intended to replace the Electron app in `../app/`; both coexist until the desktop reaches parity.
- Existing `~/.paseo` data is treated as read-only until an explicit migration is run.

See [`docs/PLAN.md`](docs/PLAN.md) for the backend-first, UI-last delivery sequence,
[`docs/STATUS.md`](docs/STATUS.md) for implementation evidence, open tasks and
verification blockers, the architecture reports under [`docs/architecture`](docs/architecture),
and [`../docs/paseo-integration.md`](../docs/paseo-integration.md) for the Go/Rust boundary.

## Development

From the repository root:

```bash
make talos            # build ./talos (the ACP backend)
make desktop          # build talos, then run the desktop UI
make desktop-check
make desktop-test
make desktop-accept   # process acceptance against the ACP fixture
```

Or from `desktop/`:

```bash
cargo check --workspace
cargo test --workspace
cargo run -p talos-desktop          # desktop UI
cargo run -p talosd         # local daemon
cargo run -p talosdctl -- --help
```

The first run uses a separate Rust-owned data directory (`~/.talosd`) and does not overwrite the installed Paseo state. On first start, `talosd` moves an existing pre-rename `~/.paseo-rs` into `~/.talosd` (stop any old `paseod` first). The Talos provider runs `talos acp` from `PATH` by default; set `command` under `[[providers]]` in `~/.talosd/config.toml` to an absolute path (for example the repo-root `talos` binary) to use a local build. See [`apps/talosdctl/README.md`](apps/talosdctl/README.md).

## Backend process acceptance

```bash
cargo build --locked -p talosd -p talosdctl -p paseo-acp --bins
python3 scripts/talosd-smoke.py --verbose --timeout 300
```

This runs real daemon/CLI processes against a deterministic ACP fixture in
isolated temporary homes, without a desktop or paid model calls. See
[`scripts/README.md`](scripts/README.md) for prerequisites and scenarios, and
[`docs/STATUS.md`](docs/STATUS.md) for current results and remaining live-provider
acceptance. CI ([`../.github/workflows/ci.yml`](../.github/workflows/ci.yml)) runs
this separately from the full-workspace gates.
