# Talos GPUI desktop

The native desktop frontend for Talos. It reuses the frontend-neutral daemon
bridge, projection and attachment validation in `talos-ui-core`; the bridge
accepts a native wake callback. Timeline updates are streamed; a lightweight
inventory snapshot refresh every five seconds synchronizes sessions deleted
through the standalone Talos TUI, including while the app is idle.

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
  when needed, using the shared configuration and discovery. The command socket
  drains duplicate broadcast events even while idle, preventing slow-reader
  disconnects after streaming replies; only the replay socket projects history.
  Connection notices
  (including “Starting local daemon…”) clear once the initial snapshot connects.
- Uses `TALOSD_HOME`, `TALOSD_SOCKET`, `TALOSD_AUTH_TOKEN` and `TALOSD_BIN` overrides.
  Normal runs use `~/.talosd`; tests use temporary homes. Provider credentials and
  the `talos acp` executable must be configured separately for live model calls.
- Choose a workspace, or open a folder to register one. Send creates a real agent
  session if needed and forwards the first prompt; recent rows are actual persisted sessions.
- Enter sends; Shift+Enter inserts a newline. Drafts are retained per conversation.
  Scrolling all the way to the conversation's bottom enables automatic following
  of streamed output. Any upward scroll, even a fraction of a pixel, pauses it;
  only reaching the bottom again resumes it (there is no near-bottom threshold).
  Sending while reading earlier messages does not force a jump. Composer resizing
  and expanding activity details preserve the same follow/pause choice.
  Stop cancels an active run. Failed prompts restore their draft; explicitly
  resending an unchanged failed prompt reuses the original acceptance key.
- Streams assistant output directly, without speaker labels or a completed-work bar.
  The conversation uses a variable-height virtual list: scroll frames build only
  visible rows plus a small overdraw, not the full history. Timeline projection
  and Markdown parsing are cached between backend changes; streamed replies and
  activity disclosures invalidate only the affected row heights.
  Assistant replies render native Markdown paragraphs, headings, nested/numbered
  lists, emphasis, inline/fenced code, quotes, task lists, rules, and clickable
  HTTP(S)/mailto links, including incomplete streamed content. User prompts and
  tool details remain literal text. HTML is inert and images show alt text without
  fetching remote resources.
  Each assistant turn has one compact, unboxed activity row, collapsed by default:
  the left shows the current or most recently completed action (`Thinking` → `Thought`, `Reading file`
  → `Read file`, `Editing file` → `Edited file`); the right counts thought/tool steps.
  Tool updates change the existing step, not the count. While a turn is active,
  completion labels remain until the next activity. Once the response finishes,
  the row summarizes the whole turn, for example `Thought 2 times, read 2 files,
  and used 9 other tools`. File counts are distinct successful targets; failed or
  interrupted calls are listed separately. Unfolding shows a compact list of steps,
  not their full contents. Each list row independently reveals its thinking or
  tool input/output when clicked. Messages and permission decisions remain outside
  the list. Opening a conversation resets both disclosure levels to collapsed.
  Summary and step text use the muted token at rest and transition to the normal
  foreground token on hover with a brief, text-only shimmer. Hover never paints a
  background or border. The effect settles without idle animation and respects
  reduced motion; its colors are supplied by the existing semantic palette.
  No timer or artificial delay is used for activity state. Talos supplies an
  explicit thought-completion boundary; providers without
  one finish thinking on the next tool/text event or run end. A compact `Working…`
  status is used only before a turn has activity. History is replayed on reconnect.
- Model controls use advertised options for idle sessions. A new conversation
  discovers Talos models and thinking options without creating a session. The unsent
  prompt, images, and selected routing ID remain a local draft. Send creates the
  conversation, applies the chosen model, waits for acknowledgement, then submits
  the prompt. A model-change failure keeps the prompt editable. Drafts are currently
  in-memory and are not crash-persistent.
  The picker opens above the composer and follows multiline growth. Its search bar
  filters names and model IDs within the selected inference provider. The icon-only
  rail groups the catalogue by model-ID provider (Codex, DeepSeek, etc.). OpenAI
  and Codex IDs share one Codex group using the OpenAI icon. Groups are
  not ACP agent backends: Talos is never shown as a model provider. Hover icons
  for provider names. Unknown providers use a generic icon rather than a guessed
  brand. Drag provider icons to persist a custom rail order. Switching groups
  preserves the session and full routing IDs; models within each group are sorted
  by global frecency (successful selections and accepted prompts, keyed by model ID),
  with untouched models retaining catalogue order. Picker order and usage history are
  stored in `$XDG_CONFIG_HOME/talos-gpui/model-preferences.json` (or `~/.config/...`).
  Escape closes the picker and restores composer focus. Discovery is shared by the
  composer and picker and cached per provider, including
  empty results. Inventory refreshes retain discovered models and merge session-only
  options rather than resetting the catalogue. A background refresh never replaces
  an available model list with a loading message.
- The text beside the model (for example, `High`) is the current thinking level.
  Click it for the provider's advertised choices; unsupported models show no
  thinking control. New-conversation choices stay local and are remembered per
  model. The first prompt waits for both model and thinking acknowledgements;
  a rejected preference leaves the prompt editable. Running or preparing sessions
  cannot change levels. Escape dismisses the thinking picker.
- Image selection goes through shared validation; click an image chip to remove it.
- The compact sidebar searches non-archived sessions across all workspaces by
  title, workspace path, or branch. Its top toolbar has icon-only New session,
  New workspace (folder picker), and Sort controls. Sessions are always grouped
  under workspace folder headings, ordered A–Z, with all workspaces visible.
  Sorting supports recently updated, oldest updated, and title within each group;
  dates sort by real timestamps, not rounded time labels. Search retains only
  matching sessions and their workspace headings. Workspace paths are available
  on heading hover; folders with the same basename remain separate groups.
  Each row shows the title and time, and a branch line only when available.
  On desktop, drag the sidebar's right edge to resize it and use the header menu
  button to collapse or restore it. Branch discovery is read-only and bounded on
  the bridge worker; detached HEADs and non-Git folders have no branch line.
  Settings is an icon at bottom right.
- `/new` starts a fresh conversation; `/workspace` opens the folder picker.
  Settings opens appearance controls and connection diagnostics.

## Frosted appearance

Frosted glass is the default: soft backdrop blur, a quiet charcoal tint and
hairline borders. No refraction, pointer-reactive highlights, or idle animation.
The sidebar, composer and floating menus use the existing pinned renderer;
no local GPUI fork, shader patch, or additional dependency is required.
The composer keeps the original charcoal frosted palette with broad rounded
corners, a top-aligned `Do anything…` prompt, an attachment control at the lower-left,
and the selected model's provider mark and thinking level beside the circular
accent-colored send control.
An empty or preparing draft dims Send; an active response retains Stop.
Opaque mode keeps the same composer contrast without translucency.

The window and root wash stay translucent so Mango's compositor blur can show
through. GPUI's own blur only samples previously painted **app content**.
Floating menus paint after the conversation, with blur and sharp controls kept
inside one scene layer. Opaque mode disables both transparent window appearance
and the app's backdrop-blur passes.

Use **Settings** at the bottom of the sidebar to switch appearance modes
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

This is not feature parity or a GPUI performance verdict. Tool previews are bounded
to 8,000 characters, and file/skill autocomplete, terminal UI, Markdown tables,
syntax highlighting, accessibility/keyboard-navigation acceptance, and
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
