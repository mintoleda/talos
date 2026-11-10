// Colors follow the prototype's CSS RGB design tokens.
#![allow(
    clippy::unreadable_literal,
    clippy::too_many_lines,
    clippy::unused_self
)]

// Share the proven IPC/replay implementation, not a second agent runtime.
// Both frontends provide native wake callbacks; no egui runtime is used here.
#[cfg(test)]
mod acceptance;
#[allow(dead_code)]
#[path = "../../talos-desktop/src/attachments.rs"]
mod attachments;
#[allow(dead_code, clippy::cast_precision_loss)]
#[path = "../../talos-desktop/src/backend.rs"]
mod backend;
mod glass;
#[allow(dead_code)]
#[path = "../../talos-desktop/src/state.rs"]
mod state;
mod workflow;
use glass::{GlassMode, GlassSettings, frosted};

use std::{
    borrow::Cow,
    collections::{HashMap, HashSet},
    path::PathBuf,
};

use backend::{Action, Backend};
use gpui::{
    AssetSource, Bounds, Context, Entity, FontWeight, IntoElement, PathPromptOptions, Render,
    ScrollHandle, SharedString, Window, WindowBounds, WindowOptions, div, hsla, prelude::*, px,
    rgb, size,
};
use gpui_base::input::{InputEditorStyle, InputEvent, Textarea, TextareaState};
use state::{EntryKind, PendingState};
use workflow::PendingPrompt;

// Dark, depth-first tokens: the canvas is the quietest plane and controls lift
// through small luminance steps rather than heavy borders or decoration.
const INK: u32 = 0xEEF2EF;
const SECONDARY: u32 = 0xB4BDB7;
const MUTED: u32 = 0x89958D;
const PAPER: u32 = 0x080B09;
const BORDER: u32 = 0x29322C;
const BLUE: u32 = 0x8EB5D2;
const BLUE_WASH: u32 = 0x1B2A33;
const ORANGE: u32 = 0xD69A6C;
const GREEN: u32 = 0x79B68C;
const DANGER: u32 = 0xD98078;
const NEW: &str = "__new__";

struct Assets;
impl AssetSource for Assets {
    fn load(&self, path: &str) -> gpui::Result<Option<Cow<'static, [u8]>>> {
        let bytes: Option<&'static [u8]> = match path {
            "quiet-field.svg" => Some(include_bytes!("../assets/quiet-field.svg")),
            "icons/menu.svg" => Some(include_bytes!("../assets/icons/menu.svg")),
            "icons/plus.svg" => Some(include_bytes!("../assets/icons/plus.svg")),
            "icons/folder.svg" => Some(include_bytes!("../assets/icons/folder.svg")),
            "icons/settings.svg" => Some(include_bytes!("../assets/icons/settings.svg")),
            "icons/paperclip.svg" => Some(include_bytes!("../assets/icons/paperclip.svg")),
            "icons/arrow-up.svg" => Some(include_bytes!("../assets/icons/arrow-up.svg")),
            "icons/stop.svg" => Some(include_bytes!("../assets/icons/stop.svg")),
            "icons/chevron-down.svg" => Some(include_bytes!("../assets/icons/chevron-down.svg")),
            "icons/sparkles.svg" => Some(include_bytes!("../assets/icons/sparkles.svg")),
            "icons/openai.svg" => Some(include_bytes!("../assets/icons/openai.svg")),
            _ => None,
        };
        Ok(bytes.map(Cow::Borrowed))
    }
    fn list(&self, path: &str) -> gpui::Result<Vec<SharedString>> {
        const ASSETS: &[&str] = &[
            "quiet-field.svg",
            "icons/menu.svg",
            "icons/plus.svg",
            "icons/folder.svg",
            "icons/settings.svg",
            "icons/paperclip.svg",
            "icons/arrow-up.svg",
            "icons/stop.svg",
            "icons/chevron-down.svg",
            "icons/sparkles.svg",
            "icons/openai.svg",
        ];
        Ok(ASSETS
            .iter()
            .filter(|asset| asset.starts_with(path))
            .map(|asset| SharedString::from(*asset))
            .collect())
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Menu {
    Workspaces,
    Models,
    Diagnostics,
}

struct AppView {
    backend: Backend,
    selected: Option<String>,
    workspace: Option<String>,
    provider: Option<String>,
    sidebar_open: bool,
    editor: Entity<TextareaState>,
    _editor_events: gpui::Subscription,
    _backend_task: gpui::Task<()>,
    drafts: HashMap<String, String>,
    images: HashMap<String, Vec<String>>,
    configured_model: Option<String>,
    pending: Option<PendingPrompt>,
    sending: HashSet<String>,
    menu: Option<Menu>,
    notice: Option<String>,
    scroll: ScrollHandle,
}

impl AppView {
    fn new(window: &mut Window, cx: &mut Context<Self>) -> Self {
        let editor = cx.new(|cx| {
            TextareaState::new(window, cx)
                .placeholder("Ask...")
                .auto_grow(1, 7)
                .submit_on_enter(true)
        });
        editor.update(cx, |state, cx| {
            state.set_editor_style(InputEditorStyle {
                foreground: rgb(INK).into(),
                muted_foreground: rgb(MUTED).into(),
                background: hsla(0., 0., 0., 0.),
                border: rgb(BORDER).into(),
                selection: rgb(BLUE_WASH).into(),
                caret: rgb(BLUE).into(),
                ..Default::default()
            });
            state.focus(window, cx);
        });
        let editor_events =
            cx.subscribe_in(&editor, window, |this, _, event, window, cx| match event {
                InputEvent::PressEnter { shift: false, .. } => this.send_prompt(window, cx),
                InputEvent::Change => {
                    this.drafts.insert(
                        this.draft_key().into(),
                        this.editor.read(cx).value().to_string(),
                    );
                    cx.notify();
                }
                _ => {}
            });
        // Coalesce wakeups, but drain every queued batch before sleeping. No idle polling.
        let (wake, wakes) = async_channel::bounded(1);
        let backend = Backend::new(
            move || {
                let _ = wake.try_send(());
            },
            false,
        );
        let backend_task = cx.spawn_in(window, async move |this, window| {
            while wakes.recv().await.is_ok() {
                if this.update_in(window, AppView::drain_backend).is_err() {
                    break;
                }
            }
        });
        Self {
            backend,
            selected: None,
            workspace: None,
            provider: None,
            sidebar_open: false,
            editor,
            _editor_events: editor_events,
            _backend_task: backend_task,
            drafts: HashMap::new(),
            images: HashMap::new(),
            configured_model: configured_model(),
            pending: None,
            sending: HashSet::new(),
            menu: None,
            notice: None,
            scroll: ScrollHandle::new(),
        }
    }

    fn draft_key(&self) -> &str {
        self.selected.as_deref().unwrap_or(NEW)
    }
    fn session(&self) -> Option<&state::Session> {
        self.selected
            .as_deref()
            .and_then(|id| self.backend.view.session(id))
    }
    fn provider_id(&self) -> &str {
        self.session()
            .map(|s| s.provider.as_str())
            .or(self.provider.as_deref())
            .or_else(|| {
                self.backend
                    .view
                    .providers
                    .iter()
                    .find(|p| p.default)
                    .map(|p| p.id.as_str())
            })
            .unwrap_or("talos")
    }
    fn running(&self) -> bool {
        self.selected
            .as_deref()
            .is_some_and(|id| self.backend.view.running(id) || self.sending.contains(id))
    }
    fn set_editor(&mut self, text: String, window: &mut Window, cx: &mut Context<Self>) {
        self.editor.update(cx, |editor, cx| {
            editor.set_value(text, window, cx);
            editor.focus(window, cx);
        });
    }
    fn select(&mut self, id: Option<String>, window: &mut Window, cx: &mut Context<Self>) {
        if self.pending.is_some() {
            return;
        }
        self.drafts.insert(
            self.draft_key().into(),
            self.editor.read(cx).value().to_string(),
        );
        self.selected = id;
        self.menu = None;
        self.notice = None;
        if let Some(session) = self.session() {
            self.workspace = Some(session.workspace.clone());
        }
        let text = self
            .drafts
            .get(self.draft_key())
            .cloned()
            .unwrap_or_default();
        self.set_editor(text, window, cx);
        self.scroll = ScrollHandle::new();
        self.ensure_loaded();
        cx.notify();
    }
    fn ensure_loaded(&mut self) {
        if let Some(id) = self.selected.clone()
            && self.backend.view.online
            && !self.backend.ready.contains(&id)
            && !self.backend.loading.contains(&id)
            && !self.backend.load_errors.contains_key(&id)
        {
            self.backend.submit(Action::Load(id));
        }
    }
    fn drain_backend(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        // Backend::drain is bounded to 64. Repeated drains avoid stranding a coalesced wake.
        let follow_tail = self.scroll.max_offset().y + self.scroll.offset().y <= px(80.);
        for _ in 0..8 {
            self.backend.drain();
        }
        if follow_tail {
            self.scroll.scroll_to_bottom();
        }
        if self.workspace.is_none() {
            self.workspace = self
                .backend
                .view
                .workspaces
                .iter()
                .find(|w| !w.archived)
                .map(|w| w.id.clone());
        }
        for (session, value) in std::mem::take(&mut self.backend.validated_images) {
            self.images.insert(
                session,
                value
                    .as_array()
                    .into_iter()
                    .flatten()
                    .filter_map(|v| v["path"].as_str().map(str::to_owned))
                    .collect(),
            );
        }
        if let Some((session, text)) = self.backend.failed_prompt.take() {
            self.sending.remove(&session);
            let draft = self.drafts.entry(session.clone()).or_default();
            if draft.is_empty() {
                *draft = text;
            }
            if self.selected.as_ref() == Some(&session) {
                self.set_editor(self.drafts[&session].clone(), window, cx);
            }
        }
        if let Some(id) = self.backend.created_workspace.take() {
            self.workspace = Some(id);
            self.backend.submit(Action::Refresh);
        }
        if let Some(id) = self.backend.created_session.take()
            && let Some(pending) = self.pending.take()
        {
            self.selected = Some(id.clone());
            self.drafts.insert(id.clone(), pending.text.clone());
            self.images.insert(id.clone(), pending.images.clone());
            if self.backend.send(&id, pending.text, pending.images) {
                self.drafts.remove(NEW);
                self.images.remove(NEW);
                self.drafts.remove(&id);
                self.sending.insert(id);
                self.set_editor(String::new(), window, cx);
            }
        }
        for action in std::mem::take(&mut self.backend.completed) {
            if let Action::Prompt { session, .. } = action {
                self.sending.remove(&session);
                self.images.remove(&session);
            }
        }
        if std::mem::take(&mut self.backend.agent_creation_failed) {
            // Unrelated failures must not discard an in-flight session creation.
            self.pending = None;
        }
        if let Some(error) = self.backend.view.error.take() {
            self.notice = Some(error);
        }
        if let Some(notice) = self.backend.view.notice.take() {
            self.notice = Some(notice);
        }
        self.ensure_loaded();
        cx.notify();
    }
    fn send_prompt(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self.pending.is_some() || self.running() {
            return;
        }
        let text = self.editor.read(cx).value().trim().to_owned();
        if text == "/new" {
            self.select(None, window, cx);
            self.set_editor(String::new(), window, cx);
            return;
        }
        if text == "/workspace" {
            self.pick_workspace(window, cx);
            return;
        }
        if text.starts_with('/') {
            self.notice = Some("Available commands: /new, /workspace. File/skill autocomplete is not implemented yet.".into());
            cx.notify();
            return;
        }
        let images = self
            .images
            .get(self.draft_key())
            .cloned()
            .unwrap_or_default();
        if text.is_empty() && images.is_empty() {
            return;
        }
        if !self.backend.view.online {
            self.notice = Some("Daemon offline. Your draft is retained.".into());
            cx.notify();
            return;
        }
        if let Some(id) = self.selected.clone() {
            if self.backend.ready.contains(&id) {
                // An uncertain acceptance is retried only explicitly, with its original request key.
                if self.backend.retry_prompt.as_ref().is_some_and(|action| {
                    matches!(action,
                    Action::Prompt { session, text: previous, images: previous_images, .. }
                    if session == &id && previous == &text && previous_images == &images)
                }) {
                    let retry = self.backend.retry_prompt.clone().expect("checked above");
                    if self.backend.submit(retry) {
                        self.sending.insert(id.clone());
                        self.drafts.remove(&id);
                        self.set_editor(String::new(), window, cx);
                    }
                } else if self.backend.send(&id, text, images) {
                    self.sending.insert(id.clone());
                    self.drafts.remove(&id);
                    self.set_editor(String::new(), window, cx);
                }
            } else {
                self.notice =
                    Some("Wait for session loading to finish, or use Reconnect to retry.".into());
            }
        } else if let Some(workspace) = self.workspace.clone() {
            let pending = PendingPrompt {
                text: text.clone(),
                images,
            };
            if self.backend.submit(Action::CreateAgent {
                workspace,
                provider: self.provider.clone(),
                title: Some(workflow::prompt_title(&text)),
            }) {
                self.pending = Some(pending);
            }
        } else {
            self.notice = Some("Choose a workspace folder before sending.".into());
            self.menu = Some(Menu::Workspaces);
        }
        cx.notify();
    }
    fn pick_workspace(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self.pending.is_some() {
            return;
        }
        let result = cx.prompt_for_paths(PathPromptOptions {
            files: false,
            directories: true,
            multiple: false,
            prompt: Some("Choose workspace folder".into()),
        });
        cx.spawn_in(window, async move |this, window| match result.await {
            Ok(Ok(Some(paths))) => {
                if let Some(path) = paths.first() {
                    let path = path.clone();
                    let _ = this.update_in(window, |this, _, cx| {
                        let existing = this
                            .backend
                            .view
                            .workspaces
                            .iter()
                            .find(|w| w.path == path.to_string_lossy() && !w.archived)
                            .map(|w| w.id.clone());
                        if let Some(id) = existing {
                            this.workspace = Some(id);
                        } else {
                            this.backend.submit(Action::CreateWorkspace {
                                name: path
                                    .file_name()
                                    .unwrap_or_default()
                                    .to_string_lossy()
                                    .into(),
                                path: path.to_string_lossy().into(),
                            });
                        }
                        this.menu = None;
                        cx.notify();
                    });
                }
            }
            Ok(Ok(None)) => {}
            _ => {
                let _ = this.update_in(window, |this, _, cx| {
                    this.notice = Some("Could not open the folder picker.".into());
                    cx.notify();
                });
            }
        })
        .detach();
    }
    fn attach_images(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self.pending.is_some() || self.running() {
            return;
        }
        let provider = self.provider_id();
        if !self
            .backend
            .view
            .provider(provider)
            .is_some_and(|p| p.supports("images"))
        {
            self.notice = Some("This provider does not advertise image input.".into());
            cx.notify();
            return;
        }
        let key = self.draft_key().to_owned();
        let mut paths = self.images.get(&key).cloned().unwrap_or_default();
        let result = cx.prompt_for_paths(PathPromptOptions {
            files: true,
            directories: false,
            multiple: true,
            prompt: Some("Attach PNG, JPEG, GIF or WebP images".into()),
        });
        cx.spawn_in(window, async move |this, window| {
            if let Ok(Ok(Some(selected))) = result.await {
                paths.extend(
                    selected
                        .into_iter()
                        .map(|p| p.to_string_lossy().into_owned()),
                );
                let _ = this.update_in(window, |this, _, cx| {
                    this.backend.submit(Action::ValidateImages {
                        session: key,
                        paths,
                    });
                    cx.notify();
                });
            }
        })
        .detach();
    }
    fn toggle_menu(&mut self, menu: Menu, cx: &mut Context<Self>) {
        self.menu = if self.menu == Some(menu) {
            None
        } else {
            Some(menu)
        };
        cx.notify();
    }

    fn shell(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let width = f32::from(window.viewport_size().width);
        let glass = *cx.global::<GlassSettings>();
        let title = self
            .session()
            .map_or("New conversation", |s| s.title.as_str())
            .to_owned();
        let workspace = self
            .backend
            .view
            .workspaces
            .iter()
            .find(|w| Some(&w.id) == self.workspace.as_ref())
            .map_or("Choose workspace", |w| w.name.as_str())
            .to_owned();
        div()
            .id("talos-shell")
            .relative()
            .flex()
            .size_full()
            .overflow_hidden()
            .bg(glass.wash(0.58))
            .child(
                div()
                    .relative()
                    .flex()
                    .size_full()
                    .when(width >= 760. || self.sidebar_open, |d| {
                        d.child(frosted(0.0, 18.0, self.sidebar(cx)))
                    })
                    .child(
                        div()
                            .relative()
                            .flex_1()
                            .min_w_0()
                            .min_h_0()
                            .flex()
                            .flex_col()
                            .child(
                                div()
                                    .h(px(64.))
                                    .flex_shrink_0()
                                    .w_full()
                                    .flex()
                                    .items_center()
                                    .justify_between()
                                    .px(px(22.))
                                    .border_b_1()
                                    .border_color(hsla(0.0, 0.0, 1.0, 0.07))
                                    .bg(glass.wash(0.38))
                                    .child(
                                        div()
                                            .flex()
                                            .items_center()
                                            .gap(px(12.))
                                            .child(icon_button(
                                                "nav",
                                                "icons/menu.svg",
                                                "Toggle conversations",
                                                cx.listener(|this, _, _, cx| {
                                                    this.sidebar_open = !this.sidebar_open;
                                                    cx.notify();
                                                }),
                                            ))
                                            .child(
                                                div()
                                                    .flex()
                                                    .flex_col()
                                                    .gap(px(2.))
                                                    .child(label(
                                                        title,
                                                        13.,
                                                        INK,
                                                        FontWeight::MEDIUM,
                                                    ))
                                                    .child(label(
                                                        workspace,
                                                        10.5,
                                                        MUTED,
                                                        FontWeight::NORMAL,
                                                    )),
                                            ),
                                    ),
                            )
                            // Paint the reading surface before floating materials so their
                            // snapshots contain the conversation, not an empty backdrop.
                            .child(if self.selected.is_none() {
                                self.new_surface(cx).into_any_element()
                            } else {
                                self.conversation_surface(cx).into_any_element()
                            })
                            .when_some(self.menu, |d, menu| d.child(self.menu_surface(menu, cx)))
                            .when_some(
                                self.notice
                                    .clone()
                                    .or_else(|| self.backend.view.error.clone()),
                                |d, notice| {
                                    d.child(
                                        div()
                                            .absolute()
                                            .top(px(76.))
                                            .left_0()
                                            .right_0()
                                            .px(px(20.))
                                            .flex()
                                            .justify_center()
                                            .child(frosted(
                                                12.0,
                                                16.0,
                                                div()
                                                    .w_full()
                                                    .max_w(px(720.))
                                                    .flex()
                                                    .justify_between()
                                                    .items_center()
                                                    .px(px(14.))
                                                    .py(px(10.))
                                                    .rounded(px(12.))
                                                    .border_1()
                                                    .border_color(hsla(0.07, 0.42, 0.55, 0.2))
                                                    .bg(hsla(
                                                        0.06,
                                                        0.35,
                                                        0.11,
                                                        if glass.mode == GlassMode::Opaque {
                                                            1.0
                                                        } else {
                                                            0.78
                                                        },
                                                    ))
                                                    .shadow_lg()
                                                    .child(label(
                                                        notice,
                                                        12.,
                                                        SECONDARY,
                                                        FontWeight::MEDIUM,
                                                    ))
                                                    .child(button(
                                                        "dismiss",
                                                        "×",
                                                        cx.listener(|this, _, _, cx| {
                                                            this.notice = None;
                                                            this.backend.view.error = None;
                                                            cx.notify();
                                                        }),
                                                    )),
                                            )),
                                    )
                                },
                            ),
                    ),
            )
    }
    fn sidebar(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let mut list = div()
            .id("sessions")
            .flex_1()
            .min_h_0()
            .overflow_y_scroll()
            .flex()
            .flex_col()
            .gap(px(4.));
        for session in self
            .backend
            .view
            .sessions_for(self.workspace.as_deref())
            .into_iter()
            .filter(|s| !s.archived)
        {
            let id = session.id.clone();
            let selected = self.selected.as_ref() == Some(&id);
            list = list.child(
                div()
                    .id(SharedString::from(format!("session-{id}")))
                    .px(px(12.))
                    .py(px(10.))
                    .rounded(px(10.))
                    .border_1()
                    .border_color(if selected {
                        hsla(0.56, 0.34, 0.42, 0.28)
                    } else {
                        hsla(0.0, 0.0, 1.0, 0.0)
                    })
                    .bg(if selected {
                        hsla(0.56, 0.28, 0.18, 0.62)
                    } else {
                        hsla(0.0, 0.0, 0.0, 0.0)
                    })
                    .hover(|s| s.bg(hsla(0.0, 0.0, 1.0, 0.055)))
                    .cursor_pointer()
                    .on_click(cx.listener(move |this, _, window, cx| {
                        this.select(Some(id.clone()), window, cx);
                    }))
                    .child(label(session.title.clone(), 12.5, INK, FontWeight::MEDIUM))
                    .child(label(
                        format!("{} · {}", session.provider, session.status.label()),
                        10.5,
                        MUTED,
                        FontWeight::NORMAL,
                    )),
            );
        }
        if self.backend.view.sessions.is_empty() {
            list = list.child(label(
                if self.backend.view.online {
                    "No conversations yet"
                } else {
                    "Connecting to talosd…"
                },
                11.,
                MUTED,
                FontWeight::NORMAL,
            ));
        }
        div()
            .w(px(272.))
            .flex_shrink_0()
            .h_full()
            .flex()
            .flex_col()
            .p(px(16.))
            .gap(px(14.))
            .bg(cx.global::<GlassSettings>().wash(0.30))
            .border_r_1()
            .border_color(hsla(0.0, 0.0, 1.0, 0.07))
            .child(sidebar_action(
                "new-conversation",
                "icons/plus.svg",
                "New conversation",
                cx.listener(|this, _, window, cx| this.select(None, window, cx)),
            ))
            .child(sidebar_action(
                "workspace-picker",
                "icons/folder.svg",
                "Choose workspace",
                cx.listener(|this, _, _, cx| this.toggle_menu(Menu::Workspaces, cx)),
            ))
            .child(div().h(px(4.)))
            .child(label("RECENT", 9.5, MUTED, FontWeight::BOLD))
            .child(list)
            .child(sidebar_action(
                "appearance",
                "icons/settings.svg",
                "Appearance & diagnostics",
                cx.listener(|this, _, _, cx| this.toggle_menu(Menu::Diagnostics, cx)),
            ))
    }
    fn new_surface(&self, cx: &mut Context<Self>) -> impl IntoElement {
        div()
            .flex_1()
            .min_h_0()
            .w_full()
            .flex()
            .flex_col()
            .items_center()
            .justify_center()
            .px(px(28.))
            .pb(px(24.))
            .child(
                div()
                    .w_full()
                    .max_w(px(760.))
                    .flex()
                    .flex_col()
                    .items_center()
                    .gap(px(16.))
                    .child(label("Start a new session", 16., INK, FontWeight::MEDIUM))
                    .child(label(
                        "What would you like Talos to work on?",
                        12.,
                        MUTED,
                        FontWeight::NORMAL,
                    ))
                    .child(div().h(px(8.)))
                    .child(self.composer(cx)),
            )
    }
    fn conversation_surface(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let id = self.selected.as_deref().unwrap_or_default();
        let mut messages = div()
            .w_full()
            .max_w(px(790.))
            .flex()
            .flex_col()
            .gap(px(26.))
            .py(px(36.));
        if self.backend.loading.contains(id) {
            messages = messages.child(label(
                "Loading conversation…",
                13.,
                MUTED,
                FontWeight::NORMAL,
            ));
        }
        if let Some(error) = self.backend.load_errors.get(id) {
            messages = messages.child(label(error.clone(), 13., ORANGE, FontWeight::MEDIUM));
        }
        for entry in self.backend.view.entries.get(id).into_iter().flatten() {
            messages = messages.child(self.timeline_entry(entry, cx));
        }
        if self.running() {
            messages = messages.child(label("Talos is working…", 12., BLUE, FontWeight::MEDIUM));
        }
        div()
            .flex_1()
            .min_h_0()
            .w_full()
            .flex()
            .flex_col()
            .items_center()
            .px(px(28.))
            .child(
                div()
                    .id("conversation-scroll")
                    .track_scroll(&self.scroll)
                    .flex_1()
                    .min_h_0()
                    .w_full()
                    .overflow_y_scroll()
                    .flex()
                    .justify_center()
                    .child(messages),
            )
            .child(
                div()
                    .w_full()
                    .max_w(px(820.))
                    .pt(px(12.))
                    .pb(px(20.))
                    .child(self.composer(cx)),
            )
    }
    fn timeline_entry(&self, entry: &state::Entry, cx: &mut Context<Self>) -> gpui::AnyElement {
        match &entry.kind {
            EntryKind::User { text } => div()
                .w_full()
                .flex()
                .justify_end()
                .child(
                    div()
                        .max_w(px(560.))
                        .px(px(16.))
                        .py(px(13.))
                        .rounded(px(16.))
                        .border_1()
                        .border_color(hsla(0.0, 0.0, 1.0, 0.1))
                        .bg(hsla(0.36, 0.14, 0.12, 0.92))
                        .child(label(text.clone(), 14., INK, FontWeight::NORMAL)),
                )
                .into_any_element(),
            EntryKind::Assistant { text, streaming } => div()
                .flex()
                .flex_col()
                .gap(px(8.))
                .child(label(
                    if *streaming {
                        "●  Talos is responding"
                    } else {
                        "Talos"
                    },
                    11.,
                    if *streaming { GREEN } else { BLUE },
                    FontWeight::MEDIUM,
                ))
                .child(label(text.clone(), 14., INK, FontWeight::NORMAL))
                .into_any_element(),
            EntryKind::Thought { text, .. } => panel("Thinking", text, MUTED).into_any_element(),
            EntryKind::Tool {
                name,
                target,
                state,
                input,
                output,
                ..
            } => panel(
                &format!("{name} · {} · {target}", state.label()),
                &format!("{input}\n{output}"),
                SECONDARY,
            )
            .into_any_element(),
            EntryKind::Permission {
                id,
                action,
                target,
                risk,
                reason,
            } => {
                let mut card = panel(
                    &format!("Permission needed · {action} · {risk}"),
                    &format!("{target}\n{reason}"),
                    ORANGE,
                );
                if self.backend.view.pending.get(id) == Some(&PendingState::Submitting) {
                    card = card.child(label(
                        "Submitting decision…",
                        11.,
                        MUTED,
                        FontWeight::MEDIUM,
                    ));
                } else {
                    let mut choices = div().flex().justify_end().gap(px(10.));
                    for (allow, title) in [(false, "Deny"), (true, "Allow once")] {
                        let permission = id.clone();
                        let session = self.draft_key().to_owned();
                        choices = choices.child(button(
                            SharedString::from(format!("decision-{id}-{allow}")),
                            title,
                            cx.listener(move |this, _, _, cx| {
                                if this.backend.view.pending.get(&permission)
                                    != Some(&PendingState::Submitting)
                                    && this.backend.submit(Action::Decision {
                                        id: permission.clone(),
                                        session: session.clone(),
                                        allow,
                                    })
                                {
                                    this.backend
                                        .view
                                        .pending
                                        .insert(permission.clone(), PendingState::Submitting);
                                }
                                cx.notify();
                            }),
                        ));
                    }
                    card = card.child(choices);
                }
                card.into_any_element()
            }
            EntryKind::Notice { message, .. } => panel("Notice", message, MUTED).into_any_element(),
            EntryKind::Error { message, .. } => panel("Error", message, ORANGE).into_any_element(),
            EntryKind::Attachment { name, mime, size } => label(
                format!("{name} · {mime} · {size} bytes"),
                12.,
                MUTED,
                FontWeight::NORMAL,
            )
            .into_any_element(),
        }
    }
    fn composer(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let running = self.running();
        let session = self.session();
        let model_id = session
            .and_then(|session| session.model.as_deref())
            .or_else(|| {
                session
                    .is_none_or(|session| session.provider == "talos")
                    .then_some(self.configured_model.as_deref())
                    .flatten()
            });
        let model = session.map_or_else(
            || {
                self.configured_model
                    .clone()
                    .unwrap_or_else(|| "Default model".into())
            },
            |session| {
                self.backend
                    .view
                    .provider(&session.provider)
                    .and_then(|provider| {
                        provider
                            .models
                            .iter()
                            .find(|model| Some(model.id.as_str()) == model_id)
                    })
                    .map_or_else(
                        || {
                            session
                                .model
                                .clone()
                                .or_else(|| self.configured_model.clone())
                                .unwrap_or_else(|| "Default model".into())
                        },
                        |model| model.label.clone(),
                    )
            },
        );
        let (model_label, is_openai_model) = model_picker_label(&model, model_id);
        let mut chips = div().flex().flex_wrap().gap(px(6.));
        for (index, path) in self
            .images
            .get(self.draft_key())
            .into_iter()
            .flatten()
            .enumerate()
        {
            let name = std::path::Path::new(path)
                .file_name()
                .unwrap_or_default()
                .to_string_lossy();
            chips = chips.child(button(
                SharedString::from(format!("image-{index}")),
                format!("{name} ×"),
                cx.listener(move |this, _, _, cx| {
                    let key = this.draft_key().to_owned();
                    if let Some(images) = this.images.get_mut(&key)
                        && index < images.len()
                    {
                        images.remove(index);
                    }
                    cx.notify();
                }),
            ));
        }
        frosted(
            24.0,
            18.0,
            div()
                .w_full()
                .min_h(px(56.))
                .rounded(px(24.))
                .bg(cx.global::<GlassSettings>().wash(0.30))
                .border_1()
                .border_color(hsla(0.0, 0.0, 1.0, 0.12))
                .shadow_lg()
                .px(px(15.))
                .py(px(8.))
                .flex()
                .items_center()
                .gap(px(8.))
                .child(icon_button(
                    "attach",
                    "icons/paperclip.svg",
                    "Attach images",
                    cx.listener(|this, _, window, cx| {
                        this.attach_images(window, cx);
                    }),
                ))
                .child(
                    div()
                        .flex_1()
                        .min_w_0()
                        .flex()
                        .flex_col()
                        .text_color(rgb(INK))
                        .justify_center()
                        .font_family("Inter")
                        .text_size(px(14.))
                        .line_height(px(22.))
                        .child(
                            div()
                                .w_full()
                                .max_h(px(176.))
                                .flex()
                                .items_center()
                                .text_color(rgb(0xF4F6F5))
                                .child(Textarea::new(&self.editor)),
                        )
                        .child(chips),
                )
                .child(
                    div()
                        .id("models")
                        .px(px(9.))
                        .py(px(2.))
                        .rounded(px(8.))
                        .flex()
                        .items_center()
                        .gap(px(7.))
                        .hover(|s| s.bg(hsla(0.0, 0.0, 1.0, 0.07)))
                        .cursor_pointer()
                        .on_click(cx.listener(|this, _, _, cx| {
                            this.toggle_menu(Menu::Models, cx);
                        }))
                        .when(is_openai_model, |element| {
                            element.child(icon("icons/openai.svg", 24., SECONDARY))
                        })
                        .child(label(model_label, 14., SECONDARY, FontWeight::MEDIUM))
                        .child(icon("icons/chevron-down.svg", 12., MUTED)),
                )
                .child(composer_action_button(
                    "send",
                    if running {
                        "icons/stop.svg"
                    } else {
                        "icons/arrow-up.svg"
                    },
                    if running {
                        "Stop response"
                    } else if self.pending.is_some() {
                        "Creating conversation"
                    } else {
                        "Send message"
                    },
                    running,
                    self.pending.is_some(),
                    cx.listener(move |this, _, window, cx| {
                        if running {
                            if let Some(id) = this.selected.clone() {
                                this.backend.submit(Action::Cancel(id));
                                cx.notify();
                            }
                        } else {
                            this.send_prompt(window, cx);
                        }
                    }),
                )),
        )
    }
    fn menu_surface(&self, menu: Menu, cx: &mut Context<Self>) -> impl IntoElement {
        let mut content = div()
            .id("menu-content")
            .max_h(px(250.))
            .overflow_y_scroll()
            .flex()
            .flex_col()
            .gap(px(6.));
        match menu {
            Menu::Workspaces => {
                for workspace in self.backend.view.workspaces.iter().filter(|w| !w.archived) {
                    let id = workspace.id.clone();
                    content = content.child(button(
                        SharedString::from(format!("workspace-{id}")),
                        format!("{} · {}", workspace.name, workspace.path),
                        cx.listener(move |this, _, window, cx| {
                            this.select(None, window, cx);
                            this.workspace = Some(id.clone());
                            cx.notify();
                        }),
                    ));
                }
                content = content.child(button(
                    "add-workspace",
                    "+ Open folder…",
                    cx.listener(|this, _, window, cx| this.pick_workspace(window, cx)),
                ));
            }
            Menu::Models => {
                if let Some(session) = self.session() {
                    if let Some(provider) = self.backend.view.provider(&session.provider) {
                        for model in &provider.models {
                            let id = model.id.clone();
                            let session = session.id.clone();
                            content = content.child(button(
                                SharedString::from(format!("model-{id}")),
                                model.label.clone(),
                                cx.listener(move |this, _, _, cx| {
                                    if !this.running() && this.pending.is_none() {
                                        this.backend.submit(Action::SetModel {
                                            session: session.clone(),
                                            model: id.clone(),
                                        });
                                        this.menu = None;
                                        cx.notify();
                                    }
                                }),
                            ));
                        }
                        if provider.models.is_empty() {
                            content = content.child(label(
                                "No models advertised by this provider.",
                                12.,
                                MUTED,
                                FontWeight::NORMAL,
                            ));
                        }
                    }
                } else {
                    content = content.child(label(
                        "Choose provider. Model selection is available after the first message.",
                        12.,
                        MUTED,
                        FontWeight::NORMAL,
                    ));
                    for provider in self.backend.view.providers.iter().filter(|p| p.available) {
                        let id = provider.id.clone();
                        content = content.child(button(
                            SharedString::from(format!("provider-{id}")),
                            provider.name.clone(),
                            cx.listener(move |this, _, _, cx| {
                                this.provider = Some(id.clone());
                                this.menu = None;
                                cx.notify();
                            }),
                        ));
                    }
                }
            }
            Menu::Diagnostics => {
                content = content.child(label(format!("{}\nEndpoint: {}\n{}", self.backend.view.status, self.backend.endpoint,
                    self.backend.connection_error.as_deref().unwrap_or("No connection error")), 12., SECONDARY, FontWeight::NORMAL))
                    .child(label("GPUI experiment: plain-text timeline; no file/skill autocomplete or terminal UI yet.", 11., MUTED, FontWeight::NORMAL))
                    .child(label("MATERIAL", 10., MUTED, FontWeight::BOLD));
                for (mode, title) in [
                    (
                        GlassMode::Frosted,
                        "Frosted glass · soft blur & charcoal tint",
                    ),
                    (GlassMode::Opaque, "Opaque · maximum readability"),
                ] {
                    content = content.child(button(
                        title,
                        title,
                        cx.listener(move |_, _, window, cx| {
                            GlassSettings::set_mode(mode, window, cx);
                            cx.notify();
                        }),
                    ));
                }
            }
        }
        frosted(
            14.0,
            16.0,
            div()
                .absolute()
                .top(px(76.))
                .right(px(22.))
                .w(px(370.))
                .max_w(px(370.))
                .p(px(14.))
                .rounded(px(14.))
                .bg(cx.global::<GlassSettings>().wash(0.42))
                .border_1()
                .border_color(hsla(0.0, 0.0, 1.0, 0.12))
                .shadow_lg()
                .child(content),
        )
    }
}

impl Render for AppView {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        self.shell(window, cx)
    }
}
fn configured_model() -> Option<String> {
    let path = std::env::var_os("TALOS_HOME")
        .map(PathBuf::from)
        .or_else(|| directories::BaseDirs::new().map(|dirs| dirs.home_dir().join(".talos")))?
        .join("config.toml");
    let config = std::fs::read_to_string(path).ok()?;
    config_value(&config, "model")
}

fn config_value(config: &str, wanted: &str) -> Option<String> {
    let mut in_root = true;
    for line in config.lines() {
        let line = line.trim();
        if line.starts_with('[') {
            in_root = false;
        }
        if !in_root || line.is_empty() || line.starts_with('#') {
            continue;
        }
        let Some((key, value)) = line.split_once('=') else {
            continue;
        };
        if key.trim() != wanted {
            continue;
        }
        let value = value.trim().trim_end_matches(',').trim();
        let value = toml::from_str::<toml::Value>(&format!("value = {value}"))
            .ok()?
            .get("value")?
            .as_str()?
            .to_owned();
        return (!value.is_empty()).then_some(value);
    }
    None
}

fn model_picker_label(model: &str, model_id: Option<&str>) -> (String, bool) {
    let is_openai = model_id.is_some_and(|id| id.to_ascii_lowercase().contains("openai"))
        || model.to_ascii_lowercase().starts_with("gpt");
    if !is_openai {
        return (model.to_owned(), false);
    }

    let model = model.strip_prefix("openai/").unwrap_or(model);
    let normalized = model.replace(['-', '_'], " ");
    let mut parts: Vec<_> = normalized.split_whitespace().collect();
    if parts
        .first()
        .is_some_and(|part| part.eq_ignore_ascii_case("gpt"))
    {
        parts.remove(0);
    }
    if parts.len() > 1
        && parts
            .last()
            .is_some_and(|part| part.chars().any(char::is_alphabetic))
    {
        let name = parts.pop().unwrap_or_default();
        parts.insert(0, name);
    }
    let label = if parts.is_empty() {
        model.to_owned()
    } else {
        parts.join(" ")
    };
    let mut characters = label.chars();
    let label = characters.next().map_or(label.clone(), |first| {
        first.to_uppercase().collect::<String>() + characters.as_str()
    });
    (label, true)
}

fn label(
    text: impl Into<SharedString>,
    size: f32,
    color: u32,
    weight: FontWeight,
) -> impl IntoElement {
    div()
        .font_family("Inter")
        .font_weight(weight)
        .text_size(px(size))
        .text_color(rgb(color))
        .child(text.into())
}
fn icon(path: &'static str, size: f32, color: u32) -> impl IntoElement {
    gpui::svg().path(path).size(px(size)).text_color(rgb(color))
}
fn button(
    id: impl Into<gpui::ElementId>,
    text: impl Into<SharedString>,
    on_click: impl Fn(&gpui::ClickEvent, &mut Window, &mut gpui::App) + 'static,
) -> impl IntoElement {
    div()
        .id(id)
        .px(px(9.))
        .py(px(7.))
        .rounded(px(8.))
        .hover(|s| s.bg(hsla(0.0, 0.0, 1.0, 0.07)))
        .cursor_pointer()
        .on_click(on_click)
        .child(label(text, 11.5, SECONDARY, FontWeight::MEDIUM))
}
fn icon_button(
    id: impl Into<gpui::ElementId>,
    path: &'static str,
    _tooltip: &'static str,
    on_click: impl Fn(&gpui::ClickEvent, &mut Window, &mut gpui::App) + 'static,
) -> impl IntoElement {
    div()
        .id(id)
        .size(px(36.))
        .flex()
        .items_center()
        .justify_center()
        .rounded(px(10.))
        .text_color(rgb(MUTED))
        .hover(|s| s.bg(hsla(0.0, 0.0, 1.0, 0.07)).text_color(rgb(INK)))
        .cursor_pointer()
        .on_click(on_click)
        .child(icon(path, 17., MUTED))
}
fn sidebar_action(
    id: impl Into<gpui::ElementId>,
    path: &'static str,
    title: &'static str,
    on_click: impl Fn(&gpui::ClickEvent, &mut Window, &mut gpui::App) + 'static,
) -> impl IntoElement {
    div()
        .id(id)
        .h(px(38.))
        .w_full()
        .px(px(10.))
        .flex()
        .items_center()
        .gap(px(10.))
        .rounded(px(10.))
        .hover(|s| s.bg(hsla(0.0, 0.0, 1.0, 0.06)))
        .cursor_pointer()
        .on_click(on_click)
        .child(icon(path, 16., MUTED))
        .child(label(title, 11.5, SECONDARY, FontWeight::MEDIUM))
}
fn composer_action_button(
    id: impl Into<gpui::ElementId>,
    path: &'static str,
    _tooltip: &'static str,
    destructive: bool,
    disabled: bool,
    on_click: impl Fn(&gpui::ClickEvent, &mut Window, &mut gpui::App) + 'static,
) -> impl IntoElement {
    div()
        .id(id)
        .size(px(36.))
        .flex()
        .items_center()
        .justify_center()
        .rounded_full()
        .bg(rgb(if destructive { DANGER } else { BLUE }))
        .opacity(if disabled { 0.55 } else { 1.0 })
        .hover(|s| s.opacity(0.88))
        .cursor_pointer()
        .on_click(on_click)
        .child(icon(path, 17., PAPER))
}
fn panel(title: &str, text: &str, color: u32) -> gpui::Div {
    div()
        .w_full()
        .rounded(px(13.))
        .bg(hsla(0.36, 0.14, 0.1, 0.74))
        .border_1()
        .border_color(hsla(0.0, 0.0, 1.0, 0.09))
        .p(px(14.))
        .flex()
        .flex_col()
        .gap(px(8.))
        .child(label(title.to_owned(), 11.5, color, FontWeight::MEDIUM))
        .child(
            div()
                .font_family("JetBrains Mono")
                .text_size(px(12.))
                .line_height(px(19.))
                .text_color(rgb(SECONDARY))
                .child(workflow::bounded_preview(text)),
        )
}
fn main() {
    gpui_platform::application().with_assets(Assets).run(|cx| {
        gpui_base::init(cx);
        let glass = GlassSettings::from_env();
        cx.set_global(glass);
        let demo = std::env::args().any(|argument| argument == "--glass-demo");
        let fonts: Vec<Cow<'static, [u8]>> = [
            include_bytes!("../../../assets/fonts/Inter-Regular.ttf").as_slice(),
            include_bytes!("../../../assets/fonts/Inter-Medium.ttf").as_slice(),
            include_bytes!("../../../assets/fonts/Inter-SemiBold.ttf").as_slice(),
            include_bytes!("../../../assets/fonts/Inter-Bold.ttf").as_slice(),
            include_bytes!("../../../assets/fonts/JetBrainsMono-Regular.ttf").as_slice(),
        ]
        .into_iter()
        .map(Cow::Borrowed)
        .collect();
        if let Err(error) = cx.text_system().add_fonts(fonts) {
            eprintln!("Could not register Talos UI fonts: {error}");
        }
        let bounds = Bounds::centered(None, size(px(1280.), px(800.)), cx);
        let options = WindowOptions {
            window_bounds: Some(WindowBounds::Windowed(bounds)),
            window_min_size: Some(size(px(720.), px(560.))),
            titlebar: Some(gpui::TitlebarOptions {
                title: Some(
                    if demo {
                        "Talos · Frosted Material Lab"
                    } else {
                        "Talos"
                    }
                    .into(),
                ),
                appears_transparent: true,
                ..Default::default()
            }),
            window_background: if glass.mode == GlassMode::Opaque {
                gpui::WindowBackgroundAppearance::Opaque
            } else {
                gpui::WindowBackgroundAppearance::Transparent
            },
            app_id: Some("talos".into()),
            ..Default::default()
        };
        if demo {
            cx.open_window(options, |_, cx| cx.new(|_| glass::GlassDemo))
                .expect("open Talos material lab");
        } else {
            cx.open_window(options, |window, cx| cx.new(|cx| AppView::new(window, cx)))
                .expect("open Talos GPUI window");
        }
    });
}

#[cfg(test)]
mod composer_tests {
    use super::{config_value, model_picker_label};

    #[test]
    fn configured_model_is_read_from_root_even_with_a_trailing_comma() {
        assert_eq!(
            config_value(
                "provider = \"openai-codex\",\nmodel = \"gpt-6.1-sol\",\n[server]\nmodel = \"ignored\"",
                "model"
            ),
            Some("gpt-6.1-sol".into())
        );
    }

    #[test]
    fn openai_models_show_brand_and_compact_name() {
        assert_eq!(
            model_picker_label("GPT 5.6 Luna", Some("openai/gpt-5.6-luna")),
            ("Luna 5.6".into(), true)
        );
        assert_eq!(
            model_picker_label("openai/gpt-5.6-luna", Some("openai/gpt-5.6-luna")),
            ("Luna 5.6".into(), true)
        );
    }

    #[test]
    fn configured_openai_model_name_is_capitalized() {
        assert_eq!(
            model_picker_label("gpt-6.1-sol", Some("gpt-6.1-sol")),
            ("Sol 6.1".into(), true)
        );
    }

    #[test]
    fn other_models_keep_their_label_without_openai_branding() {
        assert_eq!(
            model_picker_label("Claude Sonnet", Some("claude-sonnet")),
            ("Claude Sonnet".into(), false)
        );
    }
}
