# Project instructions

- `desktop/` is the Rust workspace inside the Talos monorepo: the native `egui` desktop app (`talos-desktop`), its local daemon (`talosd`), CLI (`talosdctl`) and supporting crates. The Go agent runtime lives at the repository root (`cmd/`, `internal/`).
- The desktop reaches Talos only through `talos acp` (ACP over stdio). Do not reimplement Go runtime behavior in Rust; runtime fixes belong in `internal/`. Changes spanning both sides must keep `docs/architecture/talos-acp.md` and `../docs/paseo-integration.md` in sync.
- Do not modify the installed runtime under `~/.paseo` or the real `~/.talos` home; tests and smoke runs use isolated temporary homes.
- Talos/ACP is the primary backend; Claude Code is the only secondary provider.
- Keep provider-neutral domain types and capability-driven UI behavior.
- Use idiomatic Rust, typed errors, cancellation-safe async code, bounded channels, and no `unsafe` code.
- The desktop frontend must use `egui`/`eframe`; preserve the visual and motion direction in `docs/design/egui-ui.md`.
- Run `cargo fmt`, `cargo clippy --workspace --all-targets -- -D warnings`, and `cargo test --workspace` (from `desktop/`) before declaring a workstream complete. Run `go build ./... && go test ./...` from the repository root when touching Go code.
- Agents working concurrently must stay within their assigned crate/files and avoid broad formatting of unrelated files.
