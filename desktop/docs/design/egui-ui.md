# Paseo desktop UI — egui design specification

Status: first-pass architecture/design; **no implementation yet**. Scope: the Rust desktop client, not a redesign of the daemon, Talos, or the existing Electron application.

## Evidence boundary

- **Observed in shipped artifact (read-only inspection):** the running Paseo AppImage contains an Electron/Expo renderer. Its packaged `resources/app-dist/index.html` sets the dark shell background to `#181b1a`, fixes the root to the viewport, and gives keyboard focus a visible outline. The packaged English renderer bundle (`resources/app-dist/_expo/static/js/web/index-f1a1407fb8de7ac3fefc1cc576f0e46f.js`) includes a command center searching commands, files, workspaces, and agents; composer labels for file/image attachments, send, queue, interrupt, dictation/voice; and connection statuses online/connecting/offline/error/idle. These are **static UI strings, not proof of the precise visual layout or runtime behavior**. The requested placeholder below is a new design requirement, not the current bundle's desktop placeholder (which reads “Message the agent, tag @files, or use /commands and /skills”).
- **Design brief:** near-black, generous negative space, translucent charcoal surfaces, hairline boundaries, soft blue primary accent, warm-orange mark at the model selector, and a low, wide composer. Treat this as the visual target; avoid copying product branding or assuming a screenshot was inspected.
- **Proposed below:** concrete sizing, interactions, state boundaries, keyboard map, egui patterns, and acceptance criteria. Reconcile with actual protocol and accessibility support during implementation. The Talos integration notes in [`../../../docs/paseo-integration.md`](../../../docs/paseo-integration.md) describe the desktop as the UI/control plane and the Talos Go runtime (`talos acp`) as the agent loop beneath it.

## Product shape and information architecture

A calm desktop cockpit for several workspaces and agents. The current conversation is the focal surface; location, activity and capability controls remain reachable without consuming the reading column. Do not make tool execution or permission decisions look like ordinary assistant prose.

```text
┌ window chrome / workspace title · connection / account ──────────────────────────────┐
│ rail │ workspace + history sidebar │ conversation header      │ optional inspector  │
│      │ projects, sessions, search   │ conversation timeline    │ files / activity    │
│      │                              │                          │ / details           │
│      │ settings at bottom           │ low floating composer    │                    │
└───────────────────────────────────────────────────────────────────────────────────────┘
```

- **Rail:** 56 logical px, app/home, workspaces, activity/inbox, global search, settings/account. Active item gets a soft blue wash and a 2 px marker; icon and accessible label always available.
- **History/sidebar:** 260–300 px, user resizable (min 224, max 360); workspace picker and “New agent” primary quiet action at top, search/filters beneath, then recent sessions grouped by workspace and date. Rows show title, short subtitle, status dot, last activity; hover exposes overflow menu, keyboard focus exposes the same actions. Distinguish archived sessions; never hard-delete on a single click. Keep selection and scroll position per workspace. Loading, empty, offline and failed-to-load states are distinct.
- **Conversation:** max readable message width 780–840 px, horizontally centered within remaining space. Header shows agent name, workspace/path (truncated with tooltip), provider/model, lifecycle and controls (search conversation, split view, overflow). User messages may use restrained charcoal blocks; assistant content stays mostly on the dark canvas, not a wall of identical bubbles. Use clear turn separators and source attribution for concurrent agents.
- **Inspector (optional):** 280–360 px dock, closable, for selected tool details, changed files, terminal output, activity and contextual metadata. Its open state must not squeeze the reading column below 520 px; at narrower widths use a modal sheet instead. Inspector is not required to see or decide permissions.
- **Settings:** separate scrollable view with navigation list and content pane: Appearance/accessibility, Providers & models, Permissions, Notifications, Workspace/hosts, Keybindings, Data & diagnostics. Save per-field or explicit Apply/Cancel consistently; surface errors without losing edits; mark settings requiring restart.

## Geometry and responsive behavior

All dimensions are egui logical points (DPI scaling handled by pixels-per-point), not device pixels. Recommended minimum window 720 × 520; support smaller windows by scrolling/collapsing rather than clipping safety controls.

| Usable width | Layout |
| --- | --- |
| ≥ 1440 | rail + history + centered timeline; optional inspector; composer capped at 900 px |
| 1040–1439 | rail + history + timeline; inspector as overlay; composer max 840 px |
| 720–1039 | rail + timeline, sidebar drawer via button/shortcut; header compacts labels; model selector shortens text |
| < 720 (best effort) | hide rail behind navigation menu; one primary pane; overflow secondary composer controls, retain attachment and send/stop |

Reserve 16–24 px edge gutter, 28–40 px reading-column interior space at wide widths and a 16 px composer-to-window bottom inset (plus safe-area/titlebar inset if needed). Timeline scroll viewport extends **behind neither the composer nor permission actions**: reserve measured composer height + 20 px breathing room. Resizable sidebar widths, drawer state, split ratio and active tab persist per window, not globally. At 200% UI scaling and a 720 px window, prefer one pane to clipped three-pane UI.

## Visual tokens (proposed starting values)

| Token | Value / intent |
| --- | --- |
| canvas | `#151819` / near-black; reconcile with packaged `#181b1a` shell after visual QA |
| raised surface | `#222729` with opaque fallback; at most ~92% opacity above canvas; no literal background blur in v1 |
| hover / selected | `#2B3338` / `#25394A` (blue-tinted selected wash) |
| hairline / separator | `#FFFFFF1C` / `#FFFFFF12`, 1 physical px after DPI snapping |
| text / secondary / muted | `#F0F2F2` / `#AFB8BD` / `#89959C` |
| primary blue | `#559EE8` resting; `#78B6F1` hover; `#367DC3` pressed; focus ring visible on dark surfaces |
| warm marker | `#DB9864`, only a tiny provider glyph/dot; not an error/status color |
| warning / danger / success | distinct amber / red / green *plus text and icon*, never color-only |
| radius | 10 px cards, 14 px menus, 20–22 px composer, circular send |
| spacing | 4 px base grid; 8/12/16 px compact controls; 24/32 px conversation rhythm |
| typography | system sans with bundled fallback; body 14–15 pt equivalent, message text 15, labels 12–13, line height ~1.45; monospace 12–13 for paths/code |

Use rgba layering conservatively: egui alpha blends against its own central panel, not a compositor-blurred background. Avoid fake glass, thick shadows, decorative gradients and saturated full-bleed panels. Contrast-test muted text, outlines, disabled buttons and code surfaces against actual composited colors; target WCAG AA text contrast where feasible, with a high-contrast option. Icons: consistent 16–18 px vectors and >= 36 × 36 px interactive hit boxes (>= 40 px in compact/touch-friendly mode). Ensure text remains legible at system scaling; do not encode state only in glow/animation.

## Composer: the primary visual anchor

A **wide, low pill/rounded-rectangle shell** centered under the conversation: 56–64 px minimum height at rest, width `min(available - 32, 900)`; 20–22 px corners, subtle 1 px border, charcoal fill one level above canvas. The input owns most width. Placeholder, exactly: **“Ask anything. @ files, $ skills, / commands”** in muted text. At narrow widths, use a shorter localized accessible hint visually while keeping full discoverability via tooltip/help; do not silently truncate typed content.

Inside shell, from left/top to right/bottom:

1. Multiline text edit, no visible input border; text wraps, grows from one to ~6 lines, then scrolls internally. Preserve draft per agent, cursor and selection when switching sessions. IME preedit must not trigger send or autocomplete.
2. Bottom control row aligned on a shared baseline: provider/model selector (small **warm-orange mark**, provider and model label, chevron); compact activity/signal control (status glyph + short label or meter, with status tooltip); paperclip attachment button; **circular blue send** button with upward arrow (36–40 px). A signal control is not a proxy for a connection failure: display offline/error text in a visible status area. The model dropdown lists provider > model, availability, current selection and any incompatible/unavailable reason, with search if long; a change applies to the next turn and never silently changes a running run.
3. Attachment chips appear in a wrap row *above* the controls when present: filename/type/size, remove action, preview where supported; rejected files get explicit cause. Drag-and-drop reveals a target overlay but must not trigger uploads on drag hover. Clipboard paste image and file picker converge on the same attachment validation path.

Idle: send disabled for empty/whitespace-only input with no valid attachment. Sending: show progress/queue state; replace send with stop where current protocol supports cancellation, retain draft if enqueue fails, prevent duplicate submission by stable client message ID. If queueing/steering exists, offer explicit labeled action via menu rather than silently altering Enter. When permissions are pending, composer can remain editable but the pending decision must stay visible above it. Activity control details belong in a popover (connection, agent state, token/runtime indicators if backed by data), not an unreadable animated waveform. Voice/dictation may be a future control if supported; do not pretend it exists in a Rust backend without integration.

## Timeline and tool/permission presentation

- Render streamed assistant text incrementally in stable blocks. Preserve anchors when new content arrives; follow tail only if within ~80 px of bottom and no selection/scroll intent is active. Show “New messages” control when scrolled away. Markdown is a presentation layer: headings, lists, links, fenced code, copy action and horizontal overflow; sanitize/confirm external links, never treat model-produced markup as UI commands.
- Each turn has role, time on hover/focus, lifecycle (streaming/complete/failed/interrupted), and optional usage once measured. Running indicators should be subtle and labeled. Partial streaming text must remain selectable; errors offer retry or copy details where supported.
- Tool card: compact summary line with type icon, tool name, target/path in monospace, duration/status; collapsible body with input/result tabs, scrollable/log-clamped output, copy, reveal-more and redaction for secrets. Distinct queued/running/success/failure/cancelled states. Avoid rebuilding megabytes of output each frame; retain a bounded preview and fetch full details on expansion. Paths and commands are data, not implicit clickable actions.
- Permissions: **high-salience inline decision card**, with requested action, origin agent/tool, scope/target, readable reason, risk/safety level when provided and exact choices (e.g. Allow once, Deny; persistent allow only if supported and scoped). Pin pending requests in an activity inbox and keep the active request visible even if the timeline autoscrolls. No default approval, no timeout interpreted as allow, no Enter-to-approve from general composer focus. Disable actions while submitting; on conflict/expiry re-fetch and announce the resolved state. Multiple pending requests are individually identified; never apply one decision to all without explicit API and confirmation.
- Agent side events (plan, compaction, handoff, subagent start/finish, connection loss) use quieter timeline blocks; display what the backend actually emits rather than inventing canonical events. Empty new chat uses an unobtrusive prompt and keyboard hint, not a large marketing hero.

## Navigation, keyboard and accessibility

Keyboard contract (proposed; configurable and visible in menus): `Ctrl/Cmd+K` command center; `Ctrl/Cmd+N` new agent; `Ctrl/Cmd+L` focus composer; `Ctrl/Cmd+B` toggle history; `Ctrl/Cmd+,` settings; `Esc` close topmost popover/drawer first, then clear command-center query; `Enter` send from composer, `Shift+Enter` newline (configurable alternate Ctrl/Cmd+Enter mode). During IME composition, Enter commits IME text only. Avoid platform-reserved shortcuts and let native text-edit shortcuts win. Arrow keys navigate menus/results, Tab moves focus, Space/Enter activates focused buttons. Destructive or permission shortcuts require explicit focus and label.

Define deterministic focus order: rail → sidebar → header → timeline interactive items → composer controls/input → inspector; dialogs trap focus and restore prior widget on dismissal. Visible 2 px keyboard focus ring, distinct from selected state; do not remove focus rings for mouse users if keyboard modality cannot be reliably detected. Each icon-only button needs name, tooltip and shortcut; communicate status and progress in text, not only color. Screen-reader support is an **implementation risk** in immediate-mode egui: validate `egui`/`eframe` accessibility backend and OS integration on Linux/Windows/macOS; if semantic roles, announcements, text selection or dialog focus are inadequate, add platform accessibility integration or declare the gap rather than claiming parity. Test keyboard-only journeys and high-contrast/reduced-motion settings.

## Motion and interaction tokens

Hover fill 90–120 ms, popover opacity/translate 120–160 ms, drawer 180–220 ms ease-out, new message highlight 250 ms then static; no indefinite decorative pulse. Streaming cursor/status may update at content cadence. Reduce motion disables transitions and smooth scroll, keeps state changes immediate. Animate only while a transition is active (`ctx.request_repaint_after`), not at 60 fps while idle; avoid animated geometry that shifts reading position.

## Concrete egui architecture (implementation guidance, not code)

- `eframe` native window with `egui` UI; use `TopBottomPanel` for chrome and measured composer, `SidePanel`/resizable sidebar for history, `CentralPanel` for timeline, overlay `Area`/`Window` for responsive drawers and permission-safe modals. Establish font families, `Style`, `Visuals`, spacing, rounding, strokes and text styles in one theme module. Use deterministic widget IDs scoped by workspace/agent/message/attachment; never by visible list index.
- Keep an immutable-ish backend-derived `ViewModel` (workspaces, sessions, messages, tool events, permissions, connection) separate from ephemeral `UiState` (focus, drafts, scroll anchors, open popovers, resize widths, optimistic pending actions). Network/daemon async tasks emit typed events over a channel; drain a bounded batch on UI frame, request repaint on incoming events, and never block the paint loop for filesystem/network/tool output. Generation/sequence IDs prevent late events from a previous agent/session painting into the new one. Model permission decisions as explicit pending→submitting→resolved/error state machines.
- egui does immediate-mode layout, not CSS: allocate input and control row with responsive `ui.available_width()`, measure composer height, clamp timeline width, and position overlays to the response rect rather than absolute screen constants. Use `ScrollArea` with stable IDs and virtualization/viewport culling for long history/timelines where feasible. Test with thousands of messages and large code/tool output; keep scroll anchoring stable across streamed text and expansion.
- Use actual `TextEdit::multiline` with stable ID for drafts, `Response` focus/hover state, explicit key handling gated on input/IME state, `Popup`/menu APIs matching the pinned egui version, and `Painter`/icon assets for the upward arrow and signal glyph. Check exact egui API and accessibility integration against locked crate versions before implementation; names here describe patterns, not a compilation claim.
- Store presentation preferences in app config; do not store credentials in UI state or tool-card text caches. Render backend-controlled text escaped/parsed; enforce attachment size/type validation in client *and* server. File paths opened from UI require user initiation and OS association safeguards.

## Verification gates for implementation

1. Screenshot review at 1600×900, 1280×800, 900×650, minimum 720×520, 100% and 200% scale; compare spacing, typography, visual hierarchy and composer silhouette against the brief, not branding.
2. Keyboard-only walkthrough: create/select session, command search, change model, attach, multiline prompt, send, stop, permission deny/allow once, settings, return focus. IME test and reduced-motion/high-contrast check.
3. State tests: offline/reconnect, session switch during stream, stale async event, rejected attachment, duplicate send, two simultaneous permissions, expired decision, large tool output, long history, scrolled-up streaming and pending inbox visibility.
4. Performance: idle no continuous repaint; responsive scrolling with large history/output; no blocking UI on I/O; stable scroll position and draft retention. Validate actual semantics/screen reader output per target OS before claiming accessible release.

**Out of scope for first pass:** source implementation, altering existing Paseo/Talos data, importing Electron assets wholesale, invented protocol capabilities, and copying the existing brand mark.
