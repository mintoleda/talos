# Repository instructions

- The Go agent runtime lives in `cmd/` and `internal/`; the native GPUI desktop workspace is in `desktop/`. Follow `desktop/AGENTS.md` for desktop-specific architecture, safety, and verification requirements.
- Do not modify installed runtime state under `~/.paseo` or the real `~/.talos` home. Use isolated temporary homes for tests and smoke runs.
- After making implementation changes, rebuild the desktop app from `desktop/` with:
  ```sh
  RUST_FONTCONFIG_DLOPEN=1 cargo build -p talosd -p talos-gpui
  ```
  Then inspect the per-user development launcher and desktop entry, if present, and ensure they launch `target/debug/talos-gpui` with the matching `target/debug/talosd` from this worktree. Update stale paths/configuration as needed and validate the `.desktop` file with `desktop-file-validate` when available. Keep the development entry consistent with `desktop/packaging/desktop/talos-gpui.desktop` while preserving any required development-launcher environment setup.
- After changing Go code, also run `go build ./... && go test ./...` from the repository root. Follow the desktop workspace verification commands in `desktop/AGENTS.md` when applicable.
