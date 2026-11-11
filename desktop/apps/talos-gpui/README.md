# Talos GPUI desktop

The native desktop frontend for Talos. It reuses the frontend-neutral daemon
bridge, projection and attachment validation in `talos-ui-core`; the bridge
accepts a native wake callback and does not poll for updates while idle.

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
  when needed, using the shared configuration and discovery.
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
  The gear opens appearance controls and connection diagnostics.

## Frosted appearance

Frosted glass is the default: soft backdrop blur, a quiet charcoal tint and
hairline borders. No refraction, pointer-reactive highlights, or idle animation.
The sidebar, composer and floating menus use the existing pinned renderer;
no local GPUI fork, shader patch, or additional dependency is required.

The window and root wash stay translucent so Mango's compositor blur can show
through. GPUI's own blur only samples previously painted **app content**.
Floating menus paint after the conversation, with blur and sharp controls kept
inside one scene layer. Opaque mode disables both transparent window appearance
and the app's backdrop-blur passes.

Use **Appearance & diagnostics** at the bottom of the sidebar to switch modes
for the current run, or choose the initial mode:

```sh
TALOS_GLASS=frosted RUST_FONTCONFIG_DLOPEN=1 cargo run -p talos-gpui
TALOS_GLASS=opaque RUST_FONTCONFIG_DLOPEN=1 cargo run -p talos-gpui
```

Preferences are session-only. Unknown values (including the retired `full`
setting) fall back to frosted. No compositor settings or real user data are
changed. Without compositor blur, desktop translucency may appear sharp;
choose opaque mode if that affects readability.

For a deterministic, **daemon-free** visual comparison over app-rendered content:

```sh
RUST_FONTCONFIG_DLOPEN=1 cargo run -p talos-gpui -- --glass-demo
```

The lab includes two overlapping rounded panels and frosted/opaque toggles.
It does not create agents, contact providers, or read credentials.

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

Latest frosted-material verification: formatting, strict workspace Clippy, build,
fixture bridge smoke, and serial workspace tests passed (276 tests, 2 opt-in tests
ignored; the bridge smoke separately passed one opt-in test). Wayland screenshots
were inspected for the material lab and an isolated offline app at 175% scale.
This is visual evidence, not a performance benchmark or full input acceptance.
Parallel workspace runs intermittently failed in the existing daemon lock and
socket-concurrency tests; those failures remain open, not a passing parallel gate.
A separate X11 window check verified Enter-created sessions, streamed replies and
Shift+Enter multiline persistence against the fixture. Live model calls, Wayland
input and full keyboard/accessibility acceptance were not verified.
