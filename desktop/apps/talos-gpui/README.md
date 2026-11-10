# Talos GPUI experiment

A separate, functional GPUI frontend for evaluating the native desktop alternative.
The existing `talos-desktop` / egui app remains available; this is not a port back
to egui. Both frontends reuse the same daemon bridge, projection and attachment
validation. The bridge accepts a frontend-native wake callback, so GPUI does not
link egui/eframe or poll for updates while idle.

## Run

On Void Linux, install the GPUI native build dependencies first:

```sh
sudo xbps-install -S fontconfig-devel libxcb-devel libxkbcommon-devel
```

From `desktop/`:

```sh
RUST_FONTCONFIG_DLOPEN=1 cargo build -p talosd -p talos-gpui
RUST_FONTCONFIG_DLOPEN=1 cargo run -p talos-gpui
```

The fontconfig environment flag selects upstream's dynamic-loader path. Native
linker libraries must still be installed (or available through `LIBRARY_PATH`).
This frontend uses `gpui` and `gpui_platform` from
[`zeronsh/zui`](https://github.com/zeronsh/zui), and `gpui-base` from
[`zeronsh/gpui-component`](https://github.com/zeronsh/gpui-component), at pinned
revisions.

## Connected workflow

- Connects to `talosd` using its authenticated local IPC; starts the sibling daemon
  when needed, using the same configuration/discovery as the egui frontend.
- Uses `TALOSD_HOME`, `TALOSD_SOCKET`, `TALOSD_AUTH_TOKEN` and `TALOSD_BIN` overrides.
  Normal runs use `~/.talosd`; tests use temporary homes. Provider credentials and
  the `talos acp` executable must be configured separately for live model calls.
- Choose a workspace, or open a folder to register one. Send creates a real agent
  session and forwards the first prompt; recent rows are actual persisted sessions.
- Enter sends; Shift+Enter inserts a newline. Drafts are retained per conversation.
  Stop cancels an active run. Failed prompts restore their draft; explicitly
  resending an unchanged failed prompt reuses the original acceptance key.
- Streams assistant/thinking/tool events, replays saved history on selection and
  reconnect, and presents real Allow once / Deny permission decisions.
- Model and mode controls use advertised options for idle sessions. Provider
  selection for a new conversation uses available providers. Model selection
  becomes available after the first message creates the session.
- Image selection goes through shared validation; click an image chip to remove it.
- `/new` starts a fresh conversation; `/workspace` opens the folder picker.
  The gear opens connection diagnostics rather than a fake settings screen.

## Known evaluation gaps

This is not feature parity or a GPUI performance verdict. Timeline output is plain
text, tool previews are bounded to 8,000 characters, and file/skill autocomplete,
terminal UI, rich Markdown, accessibility/keyboard-navigation acceptance, and
persistent UI preferences are not implemented. No controls claim these features.
Live authenticated provider acceptance remains separate from fixture testing.

## Verify without touching real homes

From `desktop/`, with the native build dependencies available:

```sh
cargo fmt --all -- --check
RUST_FONTCONFIG_DLOPEN=1 cargo clippy --workspace --all-targets -- -D warnings
RUST_FONTCONFIG_DLOPEN=1 cargo test --workspace
RUST_FONTCONFIG_DLOPEN=1 cargo build -p talosd -p talosdctl -p talos-acp --bins
RUST_FONTCONFIG_DLOPEN=1 python3 scripts/gpui-smoke.py
```

The smoke test starts a real daemon and deterministic ACP fixture in isolated
homes, then exercises this frontend's bridge for creation, streaming, permission
deny, cancel, and history loading through a fresh connection. It is not a GUI
input test or evidence of live-model acceptance.

Latest local verification: formatting, strict workspace Clippy, build, fixture
bridge smoke, and serial workspace tests passed (269 tests, 2 opt-in tests ignored).
Parallel workspace runs intermittently failed in the existing daemon lock and
socket-concurrency tests; those failures remain open, not a passing parallel gate.
A separate X11 window check verified Enter-created sessions, streamed replies and
Shift+Enter multiline persistence against the fixture. Live model calls, Wayland
input and full keyboard/accessibility acceptance were not verified.
