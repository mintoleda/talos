// Colors follow the prototype's CSS RGB design tokens.
#![allow(
    clippy::unreadable_literal,
    clippy::too_many_lines,
    clippy::unused_self
)]

// Share the frontend-neutral daemon bridge and projection, not a second agent runtime.
#[cfg(test)]
mod acceptance;
mod activity;
mod glass;
mod markdown;
mod model_picker;
mod model_preferences;
mod scrolling;
mod shimmer;
mod sidebar;
mod thinking_picker;
mod transcript;
mod workflow;
mod workspace_labels;
use glass::{GlassMode, GlassSettings, frosted};
use model_preferences::ModelPreferences;
use talos_ui_core::{
    backend::{Action, Backend},
    state::{self, EntryKind, PendingState, SessionStatus},
};

use std::{
    borrow::Cow,
    collections::{HashMap, HashSet},
    path::PathBuf,
    time::Duration,
};

use gpui::{
    Animation, AnimationExt as _, AssetSource, Bounds, Context, Entity, FontWeight, IntoElement,
    PathPromptOptions, Render, SharedString, Window, WindowBounds, WindowOptions, div, ease_in_out,
    hsla, prelude::*, px, rgb, size,
};
use gpui_base::input::{Input, InputEditorStyle, InputEvent, InputState, Textarea, TextareaState};
use workflow::PendingPrompt;

// Dark, depth-first tokens: the canvas is the quietest plane and controls lift
// through small luminance steps rather than heavy borders or decoration.
const INK: u32 = 0xEEF2EF;
const SECONDARY: u32 = 0xB4BDB7;
const MUTED: u32 = 0x89958D;
const PAPER: u32 = 0x080B09;
const BORDER: u32 = 0x29322C;
const ACCENT: u32 = 0xC9D5A8;
const ACCENT_WASH: u32 = 0x303A2A;
const ORANGE: u32 = 0xD69A6C;
const DANGER: u32 = 0xD98078;
const NEW: &str = "__new__";

// Keep the reference's layout in the app's original charcoal palette.
const COMPOSER_INK: u32 = INK;
const COMPOSER_MUTED: u32 = MUTED;

struct Assets;
impl AssetSource for Assets {
    fn load(&self, path: &str) -> gpui::Result<Option<Cow<'static, [u8]>>> {
        let bytes: Option<&'static [u8]> = match path {
            "quiet-field.svg" => Some(include_bytes!("../assets/quiet-field.svg")),
            "icons/search.svg" => Some(include_bytes!("../assets/icons/search.svg")),
            "icons/sort.svg" => Some(include_bytes!("../assets/icons/sort.svg")),
            "icons/folder-plus.svg" => Some(include_bytes!("../assets/icons/folder-plus.svg")),
            "icons/branch.svg" => Some(include_bytes!("../assets/icons/branch.svg")),
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
            "icons/anthropic.svg" => Some(include_bytes!("../assets/icons/anthropic.svg")),
            "icons/claude-mark.svg" => Some(include_bytes!("../assets/icons/claude-mark.svg")),
            "icons/gemini.svg" => Some(include_bytes!("../assets/icons/gemini.svg")),
            "icons/deepseek.svg" => Some(include_bytes!("../assets/icons/deepseek.svg")),
            "icons/mistral.svg" => Some(include_bytes!("../assets/icons/mistral.svg")),
            _ => None,
        };
        Ok(bytes.map(Cow::Borrowed))
    }
    fn list(&self, path: &str) -> gpui::Result<Vec<SharedString>> {
        const ASSETS: &[&str] = &[
            "quiet-field.svg",
            "icons/search.svg",
            "icons/sort.svg",
            "icons/folder-plus.svg",
            "icons/branch.svg",
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
            "icons/anthropic.svg",
            "icons/claude-mark.svg",
            "icons/gemini.svg",
            "icons/deepseek.svg",
            "icons/mistral.svg",
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
    SessionSort,
    Models,
    Thinking,
    Diagnostics,
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum SidebarVisibility {
    Responsive,
    Open,
    Closed,
}

struct AppView {
    backend: Backend,
    selected: Option<String>,
    workspace: Option<String>,
    provider: Option<String>,
    sidebar_visibility: SidebarVisibility,
    sidebar_width: f32,
    editor: Entity<TextareaState>,
    _editor_events: gpui::Subscription,
    model_search: Entity<TextareaState>,
    _model_search_events: gpui::Subscription,
    session_search: Entity<InputState>,
    _session_search_events: gpui::Subscription,
    session_sort: sidebar::SessionSort,
    picker_provider: Option<String>,
    picker_group: Option<String>,
    model_preferences: ModelPreferences,
    picker_load_attempted: bool,
    viewport_height: f32,
    _backend_task: gpui::Task<()>,
    drafts: HashMap<String, String>,
    images: HashMap<String, Vec<String>>,
    configured_model: Option<String>,
    draft_model: Option<String>,
    draft_thinking: HashMap<String, String>,
    pending: Option<PendingPrompt>,
    pending_model_session: Option<String>,
    pending_thinking_session: Option<String>,
    sending: HashSet<String>,
    menu: Option<Menu>,
    notice: Option<String>,
    expanded_activities: HashSet<String>,
    expanded_activity_steps: HashSet<String>,
    transcript: transcript::Transcript,
}

impl AppView {
    fn new(window: &mut Window, cx: &mut Context<Self>) -> Self {
        let editor = cx.new(|cx| {
            TextareaState::new(window, cx)
                .placeholder("Do anything…")
                .auto_grow(1, 7)
                .submit_on_enter(true)
        });
        editor.update(cx, |state, cx| {
            state.set_editor_style(InputEditorStyle {
                foreground: rgb(COMPOSER_INK).into(),
                muted_foreground: rgb(COMPOSER_MUTED).into(),
                background: hsla(0., 0., 0., 0.),
                border: hsla(0., 0., 0., 0.),
                selection: rgb(ACCENT_WASH).into(),
                caret: rgb(ACCENT).into(),
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
        let model_search = cx.new(|cx| {
            TextareaState::new(window, cx)
                .placeholder("Search models…")
                .auto_grow(1, 1)
                .submit_on_enter(true)
        });
        model_search.update(cx, |state, _| {
            state.set_editor_style(InputEditorStyle {
                foreground: rgb(INK).into(),
                muted_foreground: rgb(MUTED).into(),
                background: hsla(0., 0., 0., 0.),
                border: hsla(0., 0., 0., 0.),
                selection: rgb(ACCENT_WASH).into(),
                caret: rgb(INK).into(),
                ..Default::default()
            });
        });
        let model_search_events = cx.subscribe_in(&model_search, window, |_, _, _, _, cx| {
            cx.notify();
        });
        let session_search = sidebar::search_input(window, cx);
        let session_search_events = cx.subscribe_in(
            &session_search,
            window,
            |_, _, event: &InputEvent, _, cx| {
                if matches!(event, InputEvent::Change) {
                    cx.notify();
                }
            },
        );
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
        let configured_model = configured_model();
        Self {
            backend,
            selected: None,
            workspace: None,
            provider: None,
            sidebar_visibility: SidebarVisibility::Responsive,
            sidebar_width: 300.,
            editor,
            _editor_events: editor_events,
            model_search,
            _model_search_events: model_search_events,
            session_search,
            _session_search_events: session_search_events,
            session_sort: sidebar::SessionSort::Recent,
            picker_provider: None,
            picker_group: None,
            model_preferences: ModelPreferences::load(),
            picker_load_attempted: false,
            viewport_height: 720.,
            _backend_task: backend_task,
            drafts: HashMap::new(),
            images: HashMap::new(),
            configured_model: configured_model.clone(),
            draft_model: configured_model,
            draft_thinking: HashMap::new(),
            pending: None,
            pending_model_session: None,
            pending_thinking_session: None,
            sending: HashSet::new(),
            menu: None,
            notice: None,
            expanded_activities: HashSet::new(),
            expanded_activity_steps: HashSet::new(),
            transcript: transcript::Transcript::default(),
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
        // Opening a conversation always starts with its work folded away.
        self.expanded_activities.clear();
        self.expanded_activity_steps.clear();
        self.selected = id;
        if let Some((provider, model)) = self
            .session()
            .map(|session| (session.provider.clone(), session.model.clone()))
        {
            self.provider = Some(provider);
            if let Some(model) = model {
                self.draft_model = Some(model);
            }
        }
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
        self.transcript = transcript::Transcript::default();
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
        self.transcript.dirty = true;
        // Backend::drain is bounded to 64. Repeated drains avoid stranding a coalesced wake.
        for _ in 0..8 {
            self.backend.drain();
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
        if let Some(id) = self.backend.created_session.take() {
            self.selected = Some(id.clone());
            if let Some(pending) = self.pending.clone() {
                self.drafts.remove(NEW);
                self.drafts.insert(id.clone(), pending.text);
                self.images.remove(NEW);
                if !pending.images.is_empty() {
                    self.images.insert(id.clone(), pending.images);
                }
                if let Some(model) = pending.model {
                    if self.backend.submit(Action::SetModel {
                        session: id.clone(),
                        model,
                    }) {
                        self.pending_model_session = Some(id);
                    } else {
                        self.pending = None;
                        let draft = self.drafts.get(&id).cloned().unwrap_or_default();
                        self.set_editor(draft, window, cx);
                    }
                } else {
                    self.apply_pending_thinking(&id, window, cx);
                }
            }
        }
        for action in std::mem::take(&mut self.backend.completed) {
            match action {
                Action::Prompt { session, .. } => {
                    self.sending.remove(&session);
                    self.images.remove(&session);
                    if let Some(model) = self
                        .backend
                        .view
                        .session(&session)
                        .and_then(|session| session.model.clone())
                    {
                        self.record_model_use(&model);
                    }
                }
                Action::SetModel { session, model } => {
                    self.record_model_use(&model);
                    if self.pending_model_session.as_deref() == Some(session.as_str()) {
                        self.apply_pending_thinking(&session, window, cx);
                    }
                }
                Action::Rpc(talos_ipc::Command::SetThinking { session_id, .. })
                    if self.pending_thinking_session.as_deref() == Some(session_id.as_str()) =>
                {
                    self.send_pending_prompt(&session_id, window, cx);
                }
                _ => {}
            }
        }
        if let Some(session) = self.pending_model_session.clone()
            && !self.backend.setting_models.contains(&session)
        {
            // The model update failed; keep the submitted prompt as an editable draft.
            self.pending_model_session = None;
            self.pending = None;
            if self.selected.as_deref() == Some(session.as_str()) {
                let draft = self.drafts.get(&session).cloned().unwrap_or_default();
                self.set_editor(draft, window, cx);
            }
        }
        if let Some(session) = self.pending_thinking_session.clone()
            && !self.backend.setting_thinking.contains(&session)
        {
            // A rejected level must never silently send with a different setting.
            self.pending_thinking_session = None;
            self.pending = None;
            if self.selected.as_deref() == Some(session.as_str()) {
                let draft = self.drafts.get(&session).cloned().unwrap_or_default();
                self.set_editor(draft, window, cx);
            }
        }
        if std::mem::take(&mut self.backend.agent_creation_failed) {
            self.pending = None;
        }
        if let Some(error) = self.backend.view.error.take() {
            self.notice = Some(error);
        }
        // Connection notices stay backend-owned so a successful connection
        // can clear them even when startup and connection arrive in separate wakes.
        self.remember_session_preferences();
        self.ensure_draft_models();
        self.ensure_picker_models(cx);
        self.ensure_loaded();
        cx.notify();
    }
    fn apply_pending_thinking(
        &mut self,
        session: &str,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.pending_model_session = None;
        if let Some(level) = self
            .pending
            .as_ref()
            .and_then(|pending| pending.thinking.clone())
        {
            if self
                .backend
                .submit(Action::Rpc(talos_ipc::Command::SetThinking {
                    session_id: session.into(),
                    thinking_option_id: level,
                }))
            {
                self.pending_thinking_session = Some(session.into());
            } else {
                self.pending = None;
            }
        } else {
            self.send_pending_prompt(session, window, cx);
        }
    }

    fn send_pending_prompt(&mut self, session: &str, window: &mut Window, cx: &mut Context<Self>) {
        self.pending_thinking_session = None;
        let Some(pending) = self.pending.clone() else {
            return;
        };
        if self.backend.send(session, pending.text, pending.images) {
            self.pending = None;
            self.pending_model_session = None;
            self.sending.insert(session.to_owned());
            self.drafts.remove(session);
            self.set_editor(String::new(), window, cx);
        } else {
            self.pending = None;
            self.pending_model_session = None;
            let draft = self.drafts.get(session).cloned().unwrap_or_default();
            self.set_editor(draft, window, cx);
        }
    }

    fn send_prompt(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self.transcript.dirty = true;
        if self.pending.is_some()
            || self.running()
            || self
                .selected
                .as_ref()
                .is_some_and(|id| self.backend.preferences_pending(id))
        {
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
        if self.selected.is_none() && self.draft_model.is_none() {
            let provider = self.provider_id().to_owned();
            let must_choose_model =
                self.backend.view.provider(&provider).is_some_and(|item| {
                    item.supports("model_selection") && !item.models.is_empty()
                });
            if must_choose_model {
                self.notice = Some("Choose a model before starting this conversation.".into());
                self.picker_provider = Some(provider);
                self.picker_load_attempted = false;
                self.menu = Some(Menu::Models);
                self.ensure_picker_models(cx);
                cx.notify();
                return;
            }
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
                model: self
                    .draft_model
                    .clone()
                    .or_else(|| self.configured_model.clone()),
                thinking: self
                    .thinking_control()
                    .and_then(|thinking| thinking.current),
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
                    let _ = this.update_in(window, |this, window, cx| {
                        if this.pending.is_some() {
                            return;
                        }
                        this.select(None, window, cx);
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
    fn record_model_use(&mut self, model_id: &str) {
        self.model_preferences
            .record_model_use(model_id, model_preferences::unix_now());
        if let Err(error) = self.model_preferences.save() {
            eprintln!("Could not save model picker preferences: {error}");
        }
    }

    fn reorder_model_providers(
        &mut self,
        available: &[String],
        source: &str,
        target: &str,
        cx: &mut Context<Self>,
    ) {
        if self
            .model_preferences
            .move_provider(available, source, target)
        {
            if let Err(error) = self.model_preferences.save() {
                eprintln!("Could not save model provider order: {error}");
            }
            cx.notify();
        }
    }

    fn remember_session_preferences(&mut self) {
        let Some(session) = self.session() else {
            return;
        };
        let session_id = session.id.clone();
        let provider = session.provider.clone();
        let model = session.model.clone();
        if !self.backend.setting_models.contains(&session_id) {
            self.provider = Some(provider);
            if let Some(model) = model.as_ref() {
                self.draft_model = Some(model.clone());
            }
        }
        if !self.backend.setting_thinking.contains(&session_id)
            && let (Some(model), Some(level)) = (
                model,
                self.backend
                    .view
                    .session_thinking(&session_id)
                    .and_then(|thinking| thinking.current),
            )
        {
            self.draft_thinking.insert(model, level);
        }
    }

    fn ensure_draft_models(&mut self) {
        if self.selected.is_some() || self.pending.is_some() || !self.backend.view.online {
            return;
        }
        let provider = self.provider_id().to_owned();
        if self
            .backend
            .view
            .provider(&provider)
            .is_some_and(|p| p.available && p.models.is_empty())
            && !self.backend.model_catalog_loading.contains(&provider)
            && !self.backend.model_catalog_errors.contains_key(&provider)
        {
            self.backend.ensure_model_catalog(&provider);
        }
    }

    fn ensure_picker_models(&mut self, cx: &mut Context<Self>) {
        if self.menu != Some(Menu::Models)
            || self.pending.is_some()
            || self.picker_load_attempted
            || !self.backend.view.online
        {
            return;
        }
        let provider = self
            .picker_provider
            .as_deref()
            .unwrap_or_else(|| self.provider_id())
            .to_owned();
        if let Some(summary) = self.backend.view.provider(&provider) {
            if !summary.available {
                return;
            }
            // Share discovery with the draft composer; reopening uses its cache.
            self.picker_load_attempted = self.backend.ensure_model_catalog(&provider);
        }
        cx.notify();
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
        let desktop_layout = width >= 760.;
        let sidebar_visible = match self.sidebar_visibility {
            SidebarVisibility::Responsive => desktop_layout,
            SidebarVisibility::Open => true,
            SidebarVisibility::Closed => false,
        };
        self.viewport_height = f32::from(window.viewport_size().height);
        let glass = *cx.global::<GlassSettings>();
        let title = self
            .session()
            .map_or("New session", |s| s.title.as_str())
            .to_owned();
        let workspaces: Vec<_> = self.backend.view.workspaces.iter().collect();
        let workspace_labels =
            workspace_labels::for_paths(workspaces.iter().map(|workspace| workspace.path.as_str()));
        let workspace = workspaces
            .iter()
            .zip(workspace_labels)
            .find_map(|(workspace, label)| {
                (Some(&workspace.id) == self.workspace.as_ref()).then_some(label)
            })
            .unwrap_or_else(|| "Choose a workspace".into());
        div()
            .id("talos-shell")
            .on_click(cx.listener(|this, _, _, cx| {
                if this.menu == Some(Menu::Thinking) {
                    this.menu = None;
                    cx.notify();
                }
            }))
            .on_key_down(cx.listener(|this, event: &gpui::KeyDownEvent, window, cx| {
                if matches!(this.menu, Some(Menu::Thinking | Menu::SessionSort | Menu::Diagnostics | Menu::Workspaces)) && event.keystroke.key == "escape" {
                    this.menu = None;
                    this.editor.update(cx, |editor, cx| editor.focus(window, cx));
                    cx.stop_propagation();
                    cx.notify();
                }
            }))
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
                    .when(sidebar_visible, |d| {
                        let d = d.child(frosted(
                            0.0,
                            18.0,
                            self.sidebar(self.sidebar_width, cx),
                        ));
                        if desktop_layout {
                            d.child(sidebar::resize_handle(cx))
                        } else {
                            d
                        }
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
                                                cx.listener(move |this, _, _, cx| {
                                                    this.sidebar_visibility = if sidebar_visible {
                                                        SidebarVisibility::Closed
                                                    } else {
                                                        SidebarVisibility::Open
                                                    };
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
                                                        14.,
                                                        INK,
                                                        FontWeight::MEDIUM,
                                                    ))
                                                    .child(label(
                                                        workspace,
                                                        11.5,
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
                            .when_some(self.menu.filter(|menu| !matches!(menu, Menu::Models | Menu::Thinking | Menu::SessionSort)), |d, menu| {
                                d.child(self.menu_surface(menu, cx))
                            })
                            .when_some(
                                self.notice
                                    .clone()
                                    .or_else(|| self.backend.view.error.clone())
                                    .or_else(|| self.backend.view.notice.clone()),
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
                                                            this.backend.view.notice = None;
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
    fn new_surface(&self, cx: &mut Context<Self>) -> impl IntoElement {
        div()
            .flex_1()
            .min_h_0()
            .w_full()
            .flex()
            .flex_col()
            .items_center()
            .px(px(28.))
            .child(
                scrolling::reading_viewport("new-session-scroll").child(
                    div()
                        .w_full()
                        .max_w(px(790.))
                        .flex()
                        .flex_col()
                        .gap(px(26.))
                        .py(px(36.))
                        .child(label(
                            "What are you working on?",
                            14.,
                            INK,
                            FontWeight::NORMAL,
                        )),
                ),
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
    fn conversation_surface(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let view = cx.entity();
        let viewport = self
            .transcript
            .viewport("conversation-scroll", move |index, _, cx| {
                view.update(cx, |this, cx| this.conversation_row(index, cx))
            });
        div()
            .flex_1()
            .min_h_0()
            .w_full()
            .flex()
            .flex_col()
            .items_center()
            .px(px(28.))
            .child(viewport)
            .child(
                div()
                    .w_full()
                    .max_w(px(820.))
                    .pt(px(12.))
                    .pb(px(20.))
                    .child(self.composer(cx)),
            )
    }
    fn conversation_row(&self, index: usize, cx: &mut Context<Self>) -> gpui::AnyElement {
        let content = match &self.transcript.rows[index] {
            transcript::Row::Message { entry, markdown } => {
                self.timeline_entry(entry, markdown.as_ref(), index, cx)
            }
            transcript::Row::Activity { id, steps, active } => {
                let group = talos_ui_core::activity::ActivityGroup {
                    id,
                    steps: steps.iter().collect(),
                    active: *active,
                };
                let key = format!("{}:{id}", self.draft_key());
                let expanded = self.expanded_activities.contains(&key);
                let toggle_key = key.clone();
                activity::row(
                    &group,
                    expanded,
                    &self.expanded_activity_steps,
                    &key,
                    cx.listener(move |this, _, _, cx| {
                        toggle_expanded(&mut this.expanded_activities, &toggle_key);
                        this.transcript.invalidate(index);
                        cx.notify();
                    }),
                    cx.listener(move |this, key: &str, _, cx| {
                        toggle_expanded(&mut this.expanded_activity_steps, key);
                        this.transcript.invalidate(index);
                        cx.notify();
                    }),
                )
                .into_any_element()
            }
            transcript::Row::Loading => {
                label("Loading conversation…", 13., MUTED, FontWeight::NORMAL).into_any_element()
            }
            transcript::Row::Error(error) => {
                label(error.clone(), 13., ORANGE, FontWeight::MEDIUM).into_any_element()
            }
            transcript::Row::Working => working_status().into_any_element(),
        };
        div()
            .w_full()
            .flex()
            .justify_center()
            .child(div().w_full().max_w(px(790.)).child(content))
            .into_any_element()
    }

    fn timeline_entry(
        &self,
        entry: &state::Entry,
        markdown: Option<&markdown::Document>,
        index: usize,
        cx: &mut Context<Self>,
    ) -> gpui::AnyElement {
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
            EntryKind::Assistant { .. } => markdown
                .expect("assistant row has parsed Markdown")
                .render(&entry.id)
                .into_any_element(),
            EntryKind::Thought { .. } | EntryKind::Tool { .. } => {
                activity::step(entry).into_any_element()
            }
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
                                    this.transcript.invalidate(index);
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
        let preparing = self.pending.is_some()
            || self
                .selected
                .as_ref()
                .is_some_and(|id| self.backend.preferences_pending(id));
        let session = self.session();
        let model_id = session
            .and_then(|session| session.model.as_deref())
            .or_else(|| {
                session
                    .is_none_or(|session| session.provider == "talos")
                    .then(|| {
                        self.draft_model
                            .as_deref()
                            .or(self.configured_model.as_deref())
                    })
                    .flatten()
            });
        let backend_id = session.map_or_else(|| self.provider_id(), |session| &session.provider);
        let model = model_id
            .and_then(|id| {
                self.backend
                    .view
                    .provider(backend_id)
                    .and_then(|provider| provider.models.iter().find(|model| model.id == id))
                    .map(|model| model.label.clone())
            })
            .or_else(|| model_id.map(str::to_owned))
            .unwrap_or_else(|| "Default model".into());
        let (model_label, _) = model_picker_label(&model, model_id);
        let (model_icon, model_color) = composer_model_mark(model_id, backend_id);
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
            chips = chips.child(composer_chip(
                SharedString::from(format!("image-{index}")),
                label(format!("{name} ×"), 11.5, COMPOSER_INK, FontWeight::MEDIUM),
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
        let has_content = !self.editor.read(cx).value().trim().is_empty()
            || self
                .images
                .get(self.draft_key())
                .is_some_and(|images| !images.is_empty());
        let send_disabled = preparing || (!running && !has_content);
        // Keep the model popover anchored to the composer as it grows with text.
        div()
            .relative()
            .w_full()
            .child(frosted(
                20.0,
                24.0,
                div()
                    .w_full()
                    .min_h(px(104.))
                    .rounded(px(20.))
                    .bg(cx.global::<GlassSettings>().wash(0.48))
                    .border_1()
                    .border_color(hsla(0.0, 0.0, 1.0, 0.14))
                    .px(px(16.))
                    .pt(px(10.))
                    .pb(px(10.))
                    .flex()
                    .flex_col()
                    .justify_between()
                    .gap(px(6.))
                    .child(
                        div()
                            .w_full()
                            .flex_1()
                            .min_h(px(40.))
                            .flex()
                            .gap(px(8.))
                            .child(
                                div()
                                    .flex_1()
                                    .min_w_0()
                                    .flex()
                                    .flex_col()
                                    .gap(px(6.))
                                    .text_color(rgb(COMPOSER_INK))
                                    .font_family("Inter")
                                    .text_size(px(15.))
                                    .line_height(px(24.))
                                    .child(
                                        div()
                                            .w_full()
                                            .max_h(px(176.))
                                            .flex()
                                            .items_start()
                                            // Align the prompt with the attachment affordance below.
                                            .pl(px(8.))
                                            .text_color(rgb(COMPOSER_INK))
                                            .child(Textarea::new(&self.editor)),
                                    )
                                    .child(chips),
                            ),
                    )
                    .child(
                        div()
                            .w_full()
                            .flex()
                            .items_center()
                            .justify_between()
                            .child(composer_chip(
                                "attach",
                                icon("icons/paperclip.svg", 21., COMPOSER_MUTED),
                                cx.listener(|this, _, window, cx| {
                                    this.attach_images(window, cx);
                                }),
                            ))
                            .child(
                                div()
                                    .flex()
                                    .items_center()
                                    .min_w_0()
                                    .gap(px(10.))
                                    .child(
                                        div()
                                            .id("models")
                                            .min_w_0()
                                            .px(px(8.))
                                            .py(px(7.))
                                            .rounded(px(10.))
                                            .flex()
                                            .items_center()
                                            .gap(px(7.))
                                            .hover(|style| style.bg(hsla(0.0, 0.0, 1.0, 0.07)))
                                            .cursor_pointer()
                                            .on_click(cx.listener(|this, _, window, cx| {
                                                let opening = this.menu != Some(Menu::Models);
                                                this.toggle_menu(Menu::Models, cx);
                                                if opening {
                                                    this.picker_provider =
                                                        Some(this.provider_id().to_owned());
                                                    this.picker_load_attempted = false;
                                                    this.picker_group = None;
                                                    this.ensure_picker_models(cx);
                                                    this.model_search.update(cx, |search, cx| {
                                                        search.set_value(String::new(), window, cx);
                                                        search.focus(window, cx);
                                                    });
                                                } else {
                                                    this.editor.update(cx, |editor, cx| {
                                                        editor.focus(window, cx);
                                                    });
                                                }
                                            }))
                                            .child(div().flex_shrink_0().child(icon(
                                                model_icon,
                                                20.,
                                                model_color,
                                            )))
                                            .child(
                                                div()
                                                    .min_w_0()
                                                    .overflow_hidden()
                                                    .whitespace_nowrap()
                                                    .text_ellipsis()
                                                    .child(label(
                                                        model_label,
                                                        13.,
                                                        COMPOSER_INK,
                                                        FontWeight::MEDIUM,
                                                    )),
                                            ),
                                    )
                                    .child(self.thinking_level_control(cx))
                                    .child(composer_action_button(
                                        "send",
                                        if running {
                                            "icons/stop.svg"
                                        } else {
                                            "icons/arrow-up.svg"
                                        },
                                        if running {
                                            "Stop response"
                                        } else if preparing {
                                            "Starting conversation"
                                        } else {
                                            "Send message"
                                        },
                                        running,
                                        send_disabled,
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
                            ),
                    ),
            ))
            .when(self.menu == Some(Menu::Models), |d| {
                d.child(self.model_picker_surface(cx))
            })
            .when(self.menu == Some(Menu::Thinking), |d| {
                d.child(self.thinking_picker_surface(cx))
            })
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
                let workspaces: Vec<_> = self.backend.view.workspaces.iter().collect();
                let workspace_labels = workspace_labels::for_paths(
                    workspaces.iter().map(|workspace| workspace.path.as_str()),
                );
                for (workspace, name) in workspaces
                    .into_iter()
                    .zip(workspace_labels)
                    .filter(|(workspace, _)| !workspace.archived)
                {
                    let id = workspace.id.clone();
                    content = content.child(button(
                        SharedString::from(format!("workspace-{id}")),
                        format!("{} · {}", name, workspace.path),
                        cx.listener(move |this, _, window, cx| {
                            if this.pending.is_some() {
                                return;
                            }
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
            Menu::Models | Menu::Thinking | Menu::SessionSort => {} // Anchored to their controls.
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
                .max_w_full()
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
        if self.transcript.dirty {
            let id = self.selected.as_deref().unwrap_or_default();
            let entries = self
                .backend
                .view
                .entries
                .get(id)
                .map(Vec::as_slice)
                .unwrap_or_default();
            let running = self.running();
            self.transcript.sync(
                entries,
                running,
                self.backend.loading.contains(id),
                self.backend.load_errors.get(id).map(String::as_str),
            );
        }
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
    let lower_label = model.to_ascii_lowercase();
    let lower_id = model_id.unwrap_or_default().to_ascii_lowercase();
    let is_openai = lower_id.contains("openai")
        || lower_label.starts_with("gpt")
        || lower_label.contains("openai codex/");
    if is_openai {
        // Prefer the routing ID over labels that append the provider and full
        // qualified ID (e.g. "Luna openai codex/gpt 5.6").
        let model = model_id
            .and_then(|id| id.rsplit('/').next())
            .filter(|id| !id.is_empty())
            .unwrap_or(model);
        let normalized = model.replace(['-', '_'], " ");
        let mut parts: Vec<_> = normalized.split_whitespace().collect();
        if parts
            .first()
            .is_some_and(|part| part.eq_ignore_ascii_case("gpt"))
        {
            parts.remove(0);
        }
        let label = if parts.is_empty() {
            model.to_owned()
        } else {
            let version = parts.remove(0);
            let suffix = parts
                .iter()
                .map(|part| capitalize_model_words(part))
                .collect::<Vec<_>>()
                .join(" ");
            if suffix.is_empty() {
                format!("GPT-{version}")
            } else {
                format!("GPT-{version} {suffix}")
            }
        };
        return (capitalize_model_words(&label), true);
    }

    let mut display = model.trim();
    if let Some(provider) = model_id.and_then(|id| id.split_once('/').map(|(p, _)| p)) {
        // Model labels frequently repeat their provider namespace (e.g.
        // "deepseek/deepseek-flash"); the provider rail already identifies it.
        if let Some(rest) = strip_model_prefix(display, provider) {
            display = rest;
        }
        let provider_words = provider.replace(['-', '_'], " ");
        if let Some(rest) = strip_model_prefix(display, &provider_words) {
            display = rest;
        }
        if let Some(rest) = strip_model_prefix(display, provider) {
            display = rest;
        }
    }
    let display = display.replace(['/', '-', '_'], " ");
    let display = if model_id
        .and_then(|id| id.split_once('/').map(|(provider, _)| provider))
        .is_some_and(|provider| provider.eq_ignore_ascii_case("deepseek"))
    {
        format!("Deepseek {display}")
    } else {
        display
    };
    (capitalize_model_words(&display), false)
}

fn strip_model_prefix<'a>(value: &'a str, prefix: &str) -> Option<&'a str> {
    let matched = value.get(..prefix.len())?;
    if !matched.eq_ignore_ascii_case(prefix) {
        return None;
    }
    let rest = value[prefix.len()..].trim_start_matches(|character: char| {
        character.is_whitespace() || matches!(character, '/' | '-' | '_' | ':')
    });
    (!rest.is_empty()).then_some(rest)
}

fn capitalize_model_words(value: &str) -> String {
    value
        .split_whitespace()
        .map(|word| {
            if word.eq_ignore_ascii_case("gpt") {
                return "GPT".to_owned();
            }
            if word.chars().any(char::is_uppercase) {
                return word.to_owned();
            }
            let mut characters = word.chars();
            characters.next().map_or_else(String::new, |first| {
                first.to_uppercase().collect::<String>() + characters.as_str()
            })
        })
        .collect::<Vec<_>>()
        .join(" ")
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
/// Share the picker's real provider marks; never brand an unknown model as Claude.
fn composer_model_mark(model_id: Option<&str>, backend: &str) -> (&'static str, u32) {
    if model_id.is_some_and(|id| model_picker_label(id, Some(id)).1) {
        return ("icons/openai.svg", COMPOSER_INK);
    }
    let provider = workflow::model_provider(model_id.unwrap_or_default(), backend);
    if provider.key == "anthropic" {
        ("icons/claude-mark.svg", 0xC57858)
    } else {
        (provider.icon, COMPOSER_INK)
    }
}

fn composer_chip(
    id: impl Into<gpui::ElementId>,
    content: impl IntoElement,
    on_click: impl Fn(&gpui::ClickEvent, &mut Window, &mut gpui::App) + 'static,
) -> impl IntoElement {
    div()
        .id(id)
        .min_w(px(36.))
        .h(px(36.))
        .px(px(8.))
        .flex()
        .items_center()
        .justify_center()
        .rounded(px(10.))
        .hover(|style| style.bg(hsla(0., 0., 1., 0.07)))
        .cursor_pointer()
        .on_click(on_click)
        .child(content)
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
        .flex_shrink_0()
        .flex()
        .items_center()
        .justify_center()
        .rounded_full()
        .bg(rgb(if destructive {
            DANGER
        } else if disabled {
            0x465047
        } else {
            ACCENT
        }))
        .opacity(if disabled && !destructive { 0.72 } else { 1.0 })
        .when(!disabled, |element| {
            element
                .hover(|style| style.opacity(0.85))
                .cursor_pointer()
                .on_click(on_click)
        })
        .child(icon(
            path,
            18.,
            if disabled && !destructive {
                SECONDARY
            } else {
                PAPER
            },
        ))
}
fn disclosure_card() -> gpui::Div {
    div()
        // Clipping removes a flex item's content-based minimum height. Without
        // this, a restored conversation can collapse tool/thought rows and
        // clip both the header and saved details. Keep the container unboxed.
        .flex_shrink_0()
        .w_full()
        .overflow_hidden()
}

fn formatted_tool_output(output: &str) -> String {
    let Ok(value) = serde_json::from_str::<serde_json::Value>(output) else {
        return output.replace("\\n", "\n").replace("\\t", "\t");
    };
    let content = value.get("content").unwrap_or(&value);
    if let Some(text) = content.as_str() {
        return text.replace("\\n", "\n").replace("\\t", "\t");
    }
    if let Some(blocks) = content.as_array() {
        let text = blocks
            .iter()
            .filter_map(|block| block.get("text").and_then(serde_json::Value::as_str))
            .collect::<Vec<_>>();
        if !text.is_empty() {
            return text.join("\n");
        }
    }
    serde_json::to_string_pretty(&value).unwrap_or_else(|_| output.to_owned())
}

fn tool_detail(title: &'static str, text: &str) -> gpui::Div {
    div()
        .flex()
        .flex_col()
        .gap(px(6.))
        .child(label(title, 10., MUTED, FontWeight::BOLD))
        .child(
            div()
                .w_full()
                .rounded(px(8.))
                .bg(hsla(0.0, 0.0, 0.0, 0.22))
                .px(px(10.))
                .py(px(9.))
                .font_family("JetBrains Mono")
                .text_size(px(11.5))
                .line_height(px(17.))
                .text_color(rgb(SECONDARY))
                .child(workflow::bounded_preview(text)),
        )
}

fn toggle_expanded(expanded: &mut HashSet<String>, key: &str) {
    if !expanded.remove(key) {
        expanded.insert(key.to_owned());
    }
}

fn working_status() -> impl IntoElement {
    div()
        .w_full()
        .flex()
        .items_center()
        .gap(px(9.))
        .px(px(8.))
        .py(px(4.))
        .child(working_indicator())
        .child(label("Working…", 12., ACCENT, FontWeight::MEDIUM))
}

fn working_indicator() -> gpui::AnyElement {
    div()
        .size(px(7.))
        .rounded_full()
        .bg(rgb(ACCENT))
        .with_animation(
            "working-indicator",
            Animation::new(Duration::from_millis(1_100))
                .repeat()
                .with_easing(ease_in_out),
            |dot, progress| dot.opacity(0.4 + 0.6 * (1.0 - (2.0 * progress - 1.0).abs())),
        )
        .into_any_element()
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
    use super::{
        COMPOSER_INK, composer_model_mark, config_value, formatted_tool_output, model_picker_label,
        toggle_expanded,
    };
    use std::collections::HashSet;

    #[test]
    fn turn_disclosures_start_collapsed_and_toggle_independently() {
        let mut thoughts = HashSet::new();
        let first = "session:run:thought-1";
        let second = "session:run:thought-2";
        assert!(!thoughts.contains(first));

        toggle_expanded(&mut thoughts, first);
        assert!(thoughts.contains(first));
        assert!(!thoughts.contains(second));

        toggle_expanded(&mut thoughts, second);
        assert!(thoughts.contains(first));
        assert!(thoughts.contains(second));
        toggle_expanded(&mut thoughts, first);
        assert!(!thoughts.contains(first));
        assert!(thoughts.contains(second));
        toggle_expanded(&mut thoughts, second);
        assert!(thoughts.is_empty());
    }

    #[test]
    fn tool_output_extracts_and_unescapes_content_for_readable_details() {
        assert_eq!(
            formatted_tool_output(r#"{"content":"first\\nsecond"}"#),
            "first\nsecond"
        );
        assert_eq!(
            formatted_tool_output(r#"{"content":[{"type":"text","text":"one"},{"text":"two"}]}"#),
            "one\ntwo"
        );
    }

    #[test]
    fn composer_marks_follow_the_selected_inference_provider() {
        assert_eq!(
            composer_model_mark(Some("anthropic/claude-opus"), "talos"),
            ("icons/claude-mark.svg", 0xC57858)
        );
        assert_eq!(
            composer_model_mark(Some("claude-sonnet"), "claude"),
            ("icons/claude-mark.svg", 0xC57858)
        );
        assert_eq!(
            composer_model_mark(Some("openai-codex/gpt-sol"), "talos"),
            ("icons/openai.svg", COMPOSER_INK)
        );
        assert_eq!(
            composer_model_mark(Some("deepseek/deepseek-v4-pro"), "talos"),
            ("icons/deepseek.svg", COMPOSER_INK)
        );
        assert_eq!(
            composer_model_mark(None, "talos"),
            ("icons/sparkles.svg", COMPOSER_INK)
        );
    }

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
    fn openai_models_show_brand_and_slug_name() {
        assert_eq!(
            model_picker_label("GPT 5.6 Luna", Some("openai/gpt-5.6-luna")),
            ("GPT-5.6 Luna".into(), true)
        );
        assert_eq!(
            model_picker_label("openai/gpt-5.6-luna", Some("openai/gpt-5.6-luna")),
            ("GPT-5.6 Luna".into(), true)
        );
    }

    #[test]
    fn picker_labels_hide_provider_namespaces_and_fix_slug_capitalization() {
        assert_eq!(
            model_picker_label(
                "Luna openai codex/gpt 5.6",
                Some("openai-codex/gpt-5.6-luna")
            ),
            ("GPT-5.6 Luna".into(), true)
        );
        assert_eq!(
            model_picker_label("deepseek/deepseek-flash", Some("deepseek/deepseek-flash")),
            ("Deepseek Flash".into(), false)
        );
        assert_eq!(
            model_picker_label("deepseek/deepseek-v4-pro", Some("deepseek/deepseek-v4-pro")),
            ("Deepseek V4 Pro".into(), false)
        );
    }

    #[test]
    fn configured_openai_model_name_is_capitalized() {
        assert_eq!(
            model_picker_label("gpt-6.1-sol", Some("gpt-6.1-sol")),
            ("GPT-6.1 Sol".into(), true)
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
