//! Desktop application shell: panels, routing, keyboard map and dialogs.

mod management;
use management::Management;
use serde_json::Value;
use std::collections::HashMap;
use std::time::{Duration, Instant};
use talos_ipc::Command;

use eframe::egui::{
    self, Align, Color32, Layout, Margin, RichText, Sense, Stroke, TextWrapMode, Vec2,
};

use crate::backend::{Action, Backend};
use crate::state::{
    EntryKind, Layout as Breakpoint, PendingState, ProviderSummary, Session, SessionStatus,
    ViewModel, layout_for_width,
};
use crate::theme;
use crate::ui::composer::{self, ComposerAction, ComposerState};
use crate::ui::icons::{self, Icon};
use crate::ui::markdown::MarkdownCache;
use crate::ui::motion;
use crate::ui::timeline::{self, TimelineAction, TimelineState};
use crate::ui::widgets;

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum SettingsTab {
    Appearance,
    Providers,
    Permissions,
    Diagnostics,
    About,
    Workspaces,
    History,
    Terminal,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum PaletteAction {
    NewAgent,
    NewWorkspace,
    Settings(SettingsTab),
    ToggleSidebar,
    ToggleInspector,
    FocusComposer,
    Refresh,
    Shutdown,
    Select(usize),
}

struct PaletteItem {
    icon: Icon,
    label: String,
    hint: String,
    action: PaletteAction,
}

#[derive(Default)]
struct Palette {
    open: bool,
    query: String,
    index: usize,
    focus: bool,
}

struct Toast {
    text: String,
    tone: Color32,
    at: Instant,
    sticky: bool,
}

#[derive(Default)]
struct NewAgentForm {
    open: bool,
    title: String,
    workspace: Option<String>,
    provider: Option<String>,
}

#[derive(Default)]
struct NewWorkspaceForm {
    open: bool,
    name: String,
    path: String,
}

const NEW_CONVERSATION: &str = "__new__";

pub struct Desktop {
    backend: Backend,
    selected: Option<String>,
    drafts: HashMap<String, String>,
    stick: HashMap<String, bool>,
    caches: HashMap<String, MarkdownCache>,
    sidebar_open: bool,
    workspace_filter: Option<String>,
    settings: Option<SettingsTab>,
    palette: Palette,
    new_agent: NewAgentForm,
    new_workspace: NewWorkspaceForm,
    rename: Option<(String, String, String)>,
    inspector: bool,
    reduced_motion: bool,
    high_contrast: bool,
    zoom: f32,
    toast: Option<Toast>,
    composer_focus: bool,
    pending_prompt: Option<String>,
    pending_model: Option<(String, String)>,
    pending_clipboard: Option<String>,
    preview: bool,
    last_size: Vec2,
    manage: Management,
    images: HashMap<String, Vec<Value>>,
    attachment_dialog: Option<(String, String)>,
    confirmation: Option<(String, Action)>,
    session_actions: bool,
    show_archived: bool,
    pending_start_session: Option<String>,
}

impl Desktop {
    pub fn new(ctx: egui::Context, preview: bool) -> Self {
        theme::install(&ctx, false);
        ctx.set_zoom_factor(1.0);
        let mut backend = Backend::new(ctx, preview);
        if preview {
            backend.view = ViewModel::demo();
            backend.view.online = true;
            backend.view.status = "Preview · sample conversation".into();
        }
        Self {
            backend,
            selected: None,
            drafts: HashMap::new(),
            stick: HashMap::new(),
            caches: HashMap::new(),
            sidebar_open: true,
            workspace_filter: None,
            settings: None,
            palette: Palette::default(),
            new_agent: NewAgentForm::default(),
            new_workspace: NewWorkspaceForm::default(),
            rename: None,
            inspector: false,
            reduced_motion: false,
            high_contrast: false,
            zoom: 1.0,
            toast: None,
            composer_focus: false,
            pending_prompt: None,
            pending_model: None,
            pending_clipboard: None,
            preview,
            last_size: Vec2::ZERO,
            manage: Management::default(),
            images: HashMap::new(),
            attachment_dialog: None,
            confirmation: None,
            session_actions: false,
            show_archived: false,
            pending_start_session: None,
        }
    }

    fn view(&self) -> &ViewModel {
        &self.backend.view
    }

    fn session(&self) -> Option<&Session> {
        self.selected
            .as_deref()
            .and_then(|id| self.view().session(id))
    }

    fn session_id(&self) -> Option<String> {
        self.selected.clone()
    }

    /// Reuse the most recently active model for this provider when creating a
    /// new agent. Agent model choices are persisted by talosd, so this survives
    /// desktop restarts without inventing a second preference store.
    fn last_model(&self, provider: &str, excluding: Option<&str>) -> Option<String> {
        self.view()
            .sessions
            .iter()
            .filter(|session| {
                session.provider == provider
                    && !session.archived
                    && excluding != Some(session.id.as_str())
            })
            .filter_map(|session| {
                session
                    .model
                    .as_ref()
                    .filter(|model| !model.is_empty())
                    .map(|model| (session.time.as_str(), model.clone()))
            })
            .max_by_key(|(time, _)| *time)
            .map(|(_, model)| model)
    }

    fn notify(&mut self, text: impl Into<String>) {
        self.toast = Some(Toast {
            text: text.into(),
            tone: theme::BLUE,
            at: Instant::now(),
            sticky: false,
        });
    }

    fn warn(&mut self, text: impl Into<String>) {
        self.toast = Some(Toast {
            text: text.into(),
            tone: theme::AMBER,
            at: Instant::now(),
            sticky: false,
        });
    }

    fn take_backend_events(&mut self) {
        self.backend.drain();
        for (session, images) in std::mem::take(&mut self.backend.validated_images) {
            self.images
                .insert(session, images.as_array().cloned().unwrap_or_default());
            self.attachment_dialog = None;
        }
        for action in std::mem::take(&mut self.backend.completed) {
            match action {
                Action::CreateWorkspace { .. } => self.new_workspace.open = false,
                Action::CreateAgent { .. } => self.new_agent.open = false,
                Action::Rename { .. } => self.rename = None,
                Action::Rpc(Command::CreateProject { .. }) => {
                    self.manage.project_name.clear();
                    self.manage.project_root.clear();
                }
                Action::SetModel { session, .. }
                    if self.pending_start_session.as_ref() == Some(&session) =>
                {
                    if let Some(text) = self.pending_prompt.take() {
                        self.drafts.insert(session.clone(), text);
                        self.selected = Some(session);
                        self.send();
                    }
                    self.pending_start_session = None;
                }
                _ => {}
            }
        }
        if let Some((session, text)) = self.backend.failed_prompt.take() {
            let draft = self.drafts.entry(session).or_default();
            if draft.is_empty() {
                *draft = text;
            }
        }
        if let Some(id) = self.backend.created_session.take() {
            self.selected = Some(id.clone());
            self.composer_focus = true;
            if let Some(images) = self.images.remove(NEW_CONVERSATION) {
                self.images.insert(id.clone(), images);
            }
            let provider = self
                .view()
                .session(&id)
                .map(|session| session.provider.clone())
                .unwrap_or_else(|| "talos".into());
            let model = self
                .pending_model
                .take()
                .map(|(_, model)| model)
                .or_else(|| self.last_model(&provider, Some(&id)));
            let model_pending = model.is_some();
            if let Some(model) = model {
                self.backend.submit(Action::SetModel {
                    session: id.clone(),
                    model,
                });
            }
            if self.pending_prompt.is_some() {
                if model_pending {
                    self.pending_start_session = Some(id.clone());
                } else if let Some(text) = self.pending_prompt.take() {
                    self.drafts.insert(id.clone(), text);
                    self.drafts.remove(NEW_CONVERSATION);
                    self.send();
                }
            }
        }
        if let Some(id) = self.backend.created_workspace.take() {
            self.workspace_filter = Some(id);
        }
        if let Some(error) = self.view().error.clone() {
            self.backend.view.error = None;
            self.warn(error);
        }
        if let Some(notice) = self.view().notice.clone() {
            self.backend.view.notice = None;
            self.notify(notice);
        }
    }

    fn ensure_selection(&mut self) {
        let valid = self
            .selected
            .as_deref()
            .is_some_and(|id| self.view().session(id).is_some());
        if !valid {
            self.selected = self
                .view()
                .sessions
                .iter()
                .find(|session| session.status == SessionStatus::Attention)
                .or_else(|| self.view().sessions.first())
                .map(|session| session.id.clone());
        }
        let Some(id) = self.session_id() else {
            return;
        };
        if !self.preview
            && self.view().online
            && !self.backend.ready.contains(&id)
            && !self.backend.loading.contains(&id)
            && !self.backend.load_errors.contains_key(&id)
            && self.session().is_some_and(|s| !s.archived)
        {
            self.backend.submit(Action::Load(id));
        }
    }

    fn select(&mut self, id: &str) {
        if self.selected.as_deref() != Some(id) {
            self.selected = Some(id.to_owned());
            self.composer_focus = true;
        }
        self.settings = None;
        if layout_for_width(self.last_size.x) != Breakpoint::Wide {
            self.sidebar_open = false;
        }
    }

    fn send(&mut self) -> bool {
        let Some(id) = self.session_id() else {
            self.warn("Create an agent before sending a prompt.");
            return false;
        };
        let draft = self.drafts.get(&id).cloned().unwrap_or_default();
        let text = draft.trim_end_matches(['\n', ' ']).trim().to_owned();
        let images: Vec<String> = self
            .images
            .get(&id)
            .into_iter()
            .flatten()
            .filter_map(|i| i["path"].as_str().map(str::to_owned))
            .collect();
        if text.is_empty() && images.is_empty() {
            return false;
        }
        if self.preview {
            self.warn("Preview mode · start talosd to run prompts.");
            return false;
        }
        if !self.view().online {
            self.warn("Daemon is offline; the prompt was kept as a draft.");
            return false;
        }
        if self
            .backend
            .retry_prompt
            .as_ref()
            .is_some_and(|a| matches!(a, Action::Prompt {session, ..} if session == &id))
        {
            self.warn(
                "Resolve the previous submission using Retry or Keep draft before sending again.",
            );
            return false;
        }
        if !self.backend.ready.contains(&id) || self.view().running(&id) {
            return false;
        }
        if self.backend.send(&id, text, images) {
            if let Some(draft) = self.drafts.get_mut(&id) {
                draft.clear();
            }
            self.images.remove(&id);
            self.stick.insert(id.clone(), true);
            true
        } else {
            false
        }
    }

    fn create_agent(&mut self) {
        let workspace = self.workspace_filter.clone().or_else(|| {
            self.view()
                .workspaces
                .first()
                .map(|workspace| workspace.id.clone())
        });
        let Some(workspace) = workspace else {
            self.new_workspace.open = true;
            self.warn("Add a workspace first.");
            return;
        };
        self.new_agent.workspace = Some(workspace);
        self.new_agent.provider = Some("talos".into());
        self.new_agent.title.clear();
        self.new_agent.open = true;
    }

    // -- keyboard ------------------------------------------------------------

    fn shortcuts(&mut self, ctx: &egui::Context) {
        let command = ctx.input(|input| input.modifiers.command);
        if command && ctx.input(|input| input.key_pressed(egui::Key::K)) {
            self.palette.open = true;
            self.palette.focus = true;
            self.palette.query.clear();
            self.palette.index = 0;
        }
        if command && ctx.input(|input| input.key_pressed(egui::Key::N)) {
            self.create_agent();
        }
        if command && ctx.input(|input| input.key_pressed(egui::Key::B)) {
            self.sidebar_open = !self.sidebar_open;
        }
        if command && ctx.input(|input| input.key_pressed(egui::Key::L)) {
            self.composer_focus = true;
        }
        if command && ctx.input(|input| input.key_pressed(egui::Key::Comma)) {
            self.settings = Some(SettingsTab::Appearance);
        }
        if ctx.input(|input| input.key_pressed(egui::Key::Escape)) {
            if self.palette.open {
                self.palette.open = false;
            } else if self.new_agent.open {
                self.new_agent.open = false;
            } else if self.new_workspace.open {
                self.new_workspace.open = false;
            } else if self.rename.is_some() {
                self.rename = None;
            } else if self.settings.is_some() {
                self.settings = None;
            }
        }
    }

    // -- panels --------------------------------------------------------------

    fn rail(&mut self, root: &mut egui::Ui, breakpoint: Breakpoint) {
        if breakpoint == Breakpoint::Minimal {
            return;
        }
        egui::Panel::left("rail")
            .exact_size(theme::RAIL_WIDTH)
            .resizable(false)
            .frame(
                egui::Frame::new()
                    .fill(theme::RAIL)
                    .inner_margin(Margin::symmetric(0, 14)),
            )
            .show(root, |ui| {
                ui.vertical_centered(|ui| {
                    let (rect, response) =
                        ui.allocate_exact_size(Vec2::splat(34.0), Sense::click());
                    paint_logo(ui, rect);
                    response.on_hover_text("Talos Desktop");
                    ui.add_space(26.0);
                });
                let attention = self.view().attention_count();
                let items = [
                    (Icon::Conversations, self.sidebar_open, 0),
                    (Icon::Search, false, 0),
                    (Icon::Inbox, false, attention),
                ];
                for (icon, active, badge) in items {
                    if rail_button(ui, icon, active, badge) {
                        match icon {
                            Icon::Conversations => self.sidebar_open = !self.sidebar_open,
                            Icon::Search => {
                                self.palette.open = true;
                                self.palette.focus = true;
                                self.palette.query.clear();
                            }
                            Icon::Inbox => self.open_attention(),
                            _ => {}
                        }
                    }
                    ui.add_space(2.0);
                }
                egui::Panel::bottom("rail-bottom")
                    .exact_size(96.0)
                    .frame(egui::Frame::new().fill(Color32::TRANSPARENT))
                    .show(ui, |ui| {
                        ui.with_layout(Layout::bottom_up(Align::Center), |ui| {
                            ui.add_space(10.0);
                            let (rect, _) =
                                ui.allocate_exact_size(Vec2::splat(6.0), Sense::hover());
                            let online = self.view().online;
                            ui.painter().circle_filled(
                                rect.center(),
                                3.0,
                                if online { theme::GREEN } else { theme::AMBER },
                            );
                            ui.add_space(10.0);
                            if rail_button(ui, Icon::Settings, self.settings.is_some(), 0) {
                                self.settings = Some(SettingsTab::Appearance);
                            }
                            ui.add_space(4.0);
                        });
                    });
            });
    }

    fn open_attention(&mut self) {
        if let Some(session) = self
            .view()
            .sessions
            .iter()
            .find(|session| session.status == SessionStatus::Attention)
        {
            let id = session.id.clone();
            self.select(&id);
        } else {
            self.notify("Nothing needs your attention.");
        }
    }

    fn sidebar(&mut self, root: &mut egui::Ui, breakpoint: Breakpoint) {
        if breakpoint != Breakpoint::Wide || !self.sidebar_open {
            return;
        }
        let width = if breakpoint == Breakpoint::Compact {
            240.0
        } else {
            theme::SIDEBAR_DEFAULT
        };
        egui::Panel::left("history")
            .default_size(width)
            .size_range(theme::SIDEBAR_MIN..=theme::SIDEBAR_MAX)
            .resizable(breakpoint == Breakpoint::Wide)
            .frame(
                egui::Frame::new()
                    .fill(theme::SIDEBAR)
                    .inner_margin(Margin::symmetric(12, 12)),
            )
            .show(root, |ui| {
                self.sidebar_contents(ui);
            });
    }

    fn sidebar_contents(&mut self, ui: &mut egui::Ui) {
        ui.horizontal(|ui| {
            if ui.small_button("Manage workspaces").clicked() {
                self.settings = Some(SettingsTab::Workspaces);
                self.backend.submit(Action::Rpc(Command::ListProjects));
            }
            if ui.small_button("History").clicked() {
                self.settings = Some(SettingsTab::History);
            }
        });
        // Workspace picker.
        let current = self
            .workspace_filter
            .as_ref()
            .and_then(|id| {
                self.view()
                    .workspaces
                    .iter()
                    .find(|workspace| &workspace.id == id)
            })
            .map_or("All workspaces".to_owned(), |workspace| {
                workspace.name.clone()
            });
        let response = ui.add_sized(
            [ui.available_width(), 34.0],
            egui::Button::new(
                RichText::new(format!("      {current}"))
                    .font(theme::medium(12.5))
                    .color(theme::SECONDARY),
            )
            .fill(Color32::TRANSPARENT)
            .stroke(Stroke::new(1.0, theme::LINE))
            .corner_radius(9),
        );
        icons::paint(
            ui.painter(),
            egui::Rect::from_center_size(
                egui::pos2(response.rect.left() + 18.0, response.rect.center().y),
                Vec2::splat(14.0),
            ),
            Icon::Folder,
            theme::MUTED,
            1.5,
        );
        icons::paint(
            ui.painter(),
            egui::Rect::from_center_size(
                egui::pos2(response.rect.right() - 16.0, response.rect.center().y),
                Vec2::splat(12.0),
            ),
            Icon::ChevronDown,
            theme::FAINT,
            1.4,
        );
        egui::Popup::menu(&response)
            .id(egui::Id::unique("workspace-menu"))
            .show(|ui| {
                ui.set_min_width(230.0);
                if ui
                    .add(
                        egui::Button::new(
                            RichText::new("All workspaces")
                                .font(theme::ui(12.0))
                                .color(theme::TEXT),
                        )
                        .fill(if self.workspace_filter.is_none() {
                            theme::SELECTED
                        } else {
                            Color32::TRANSPARENT
                        })
                        .corner_radius(7),
                    )
                    .clicked()
                {
                    self.workspace_filter = None;
                    ui.close();
                }
                let workspaces = self.view().workspaces.clone();
                for workspace in workspaces.into_iter().filter(|w| !w.archived) {
                    let selected = self.workspace_filter.as_deref() == Some(workspace.id.as_str());
                    if ui
                        .add(
                            egui::Button::new(
                                RichText::new(&workspace.name).font(theme::ui(12.0)).color(
                                    if selected {
                                        theme::TEXT
                                    } else {
                                        theme::SECONDARY
                                    },
                                ),
                            )
                            .fill(if selected {
                                theme::SELECTED
                            } else {
                                Color32::TRANSPARENT
                            })
                            .corner_radius(7),
                        )
                        .on_hover_text(&workspace.path)
                        .clicked()
                    {
                        self.workspace_filter = Some(workspace.id.clone());
                        ui.close();
                    }
                }
                ui.separator();
                if ui
                    .add(
                        egui::Button::new(
                            RichText::new("  Add workspace…")
                                .font(theme::ui(12.0))
                                .color(theme::SECONDARY),
                        )
                        .corner_radius(7),
                    )
                    .clicked()
                {
                    self.new_workspace.open = true;
                    ui.close();
                }
            });
        ui.add_space(8.0);
        let new_agent = ui.add_sized(
            [ui.available_width(), 36.0],
            egui::Button::new(
                RichText::new("  New agent")
                    .font(theme::medium(13.0))
                    .color(theme::TEXT),
            )
            .fill(theme::SURFACE)
            .stroke(Stroke::new(1.0, theme::LINE))
            .corner_radius(10),
        );
        let icon_rect = egui::Rect::from_min_size(
            new_agent.rect.left_top() + Vec2::new(12.0, 10.0),
            Vec2::splat(16.0),
        );
        icons::paint(ui.painter(), icon_rect, Icon::Plus, theme::SECONDARY, 1.6);
        if new_agent.clicked() {
            self.create_agent();
        }
        ui.add_space(6.0);
        let search = ui.add_sized(
            [ui.available_width(), 30.0],
            egui::Button::new(
                RichText::new("  Search agents")
                    .font(theme::ui(12.0))
                    .color(theme::FAINT),
            )
            .fill(Color32::TRANSPARENT)
            .stroke(Stroke::new(1.0, theme::LINE))
            .corner_radius(9),
        );
        icons::paint(
            ui.painter(),
            egui::Rect::from_center_size(
                egui::pos2(search.rect.left() + 18.0, search.rect.center().y),
                Vec2::splat(14.0),
            ),
            Icon::Search,
            theme::FAINT,
            1.5,
        );
        if search.clicked() {
            self.palette.open = true;
            self.palette.focus = true;
            self.palette.query.clear();
        }
        search.on_hover_text("Search agents and commands · ⌘K");

        ui.checkbox(&mut self.show_archived, "Archived conversations");
        ui.add_space(12.0);
        widgets::divider(ui);
        ui.add_space(4.0);

        egui::ScrollArea::vertical()
            .id_salt("history-list")
            .auto_shrink([false, false])
            .show(ui, |ui| {
                let sessions: Vec<Session> = self
                    .view()
                    .sessions_for(self.workspace_filter.as_deref())
                    .into_iter()
                    .filter(|session| session.archived == self.show_archived)
                    .cloned()
                    .collect();
                let attention: Vec<&Session> = sessions
                    .iter()
                    .filter(|session| session.status == SessionStatus::Attention)
                    .collect();
                let active: Vec<&Session> = sessions
                    .iter()
                    .filter(|session| session.status == SessionStatus::Running)
                    .collect();
                let recent: Vec<&Session> = sessions
                    .iter()
                    .filter(|session| {
                        !matches!(
                            session.status,
                            SessionStatus::Attention | SessionStatus::Running
                        )
                    })
                    .collect();
                let selected = self.selected.clone();
                let mut chosen = None;
                for (label, group) in [
                    ("NEEDS YOU", attention),
                    ("ACTIVE", active),
                    ("RECENT", recent),
                ] {
                    if group.is_empty() {
                        continue;
                    }
                    widgets::section_label(ui, label);
                    for session in group {
                        if session_row(
                            ui,
                            session,
                            selected.as_deref() == Some(&session.id),
                            self.reduced_motion,
                        ) {
                            chosen = Some(session.id.clone());
                        }
                    }
                    ui.add_space(6.0);
                }
                if sessions.is_empty() {
                    ui.add_space(18.0);
                    ui.vertical_centered(|ui| {
                        ui.label(
                            RichText::new("No agents in this workspace yet.")
                                .font(theme::ui(12.0))
                                .color(theme::FAINT),
                        );
                        ui.add_space(6.0);
                        if ui
                            .add(
                                egui::Button::new(
                                    RichText::new("Start the first one")
                                        .font(theme::medium(12.0))
                                        .color(theme::BLUE),
                                )
                                .frame(false),
                            )
                            .clicked()
                        {
                            self.create_agent();
                        }
                    });
                }
                if let Some(id) = chosen {
                    self.select(&id);
                }
            });

        // Usage footer.
        ui.with_layout(Layout::bottom_up(Align::Min), |ui| {
            ui.add_space(6.0);
            let usage = self
                .session()
                .and_then(|session| self.view().usage.get(&session.id));
            let (fraction, label) = usage.map_or((0.0, "No usage yet".to_owned()), |usage| {
                let fraction = usage
                    .context_size
                    .filter(|size| *size > 0)
                    .zip(usage.context_used)
                    .map_or(0.0, |(size, used)| used as f32 / size as f32);
                let tokens = usage.input_tokens.unwrap_or(0) + usage.output_tokens.unwrap_or(0);
                (
                    fraction,
                    if tokens > 0 {
                        format!(
                            "{} tokens · {:.0}% context",
                            crate::backend::format_tokens(tokens),
                            fraction * 100.0
                        )
                    } else {
                        "Usage reported per run".to_owned()
                    },
                )
            });
            ui.add_space(2.0);
            ui.label(
                RichText::new(label)
                    .font(theme::ui(10.5))
                    .color(theme::FAINT),
            );
            let (rect, _) =
                ui.allocate_exact_size(Vec2::new(ui.available_width(), 4.0), Sense::hover());
            ui.painter().rect_filled(rect, 2.0, theme::LINE);
            if fraction > 0.0 {
                let filled = egui::Rect::from_min_size(
                    rect.min,
                    Vec2::new(rect.width() * fraction.clamp(0.02, 1.0), rect.height()),
                );
                let color = if fraction > 0.9 {
                    theme::RED
                } else if fraction > 0.7 {
                    theme::AMBER
                } else {
                    theme::BLUE
                };
                ui.painter().rect_filled(filled, 2.0, color);
            }
            widgets::divider(ui);
            ui.horizontal(|ui| {
                if ui
                    .add(
                        egui::Button::new(
                            RichText::new("Providers")
                                .font(theme::ui(11.0))
                                .color(theme::MUTED),
                        )
                        .frame(false),
                    )
                    .clicked()
                {
                    self.settings = Some(SettingsTab::Providers);
                }
                ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                    ui.label(
                        RichText::new(format!(
                            "{} online",
                            self.view()
                                .providers
                                .iter()
                                .filter(|provider| provider.available)
                                .count()
                        ))
                        .font(theme::ui(10.5))
                        .color(theme::FAINT),
                    );
                });
            });
        });
    }

    fn header(&mut self, root: &mut egui::Ui, breakpoint: Breakpoint) {
        egui::Panel::top("header")
            .exact_size(58.0)
            .frame(
                egui::Frame::new()
                    .fill(theme::CANVAS)
                    .inner_margin(Margin::symmetric(20, 8)),
            )
            .show(root, |ui| {
                ui.horizontal(|ui| {
                    if breakpoint != Breakpoint::Wide
                        && header_icon(ui, Icon::PanelLeft, "Toggle history · ⌘B")
                    {
                        self.sidebar_open = !self.sidebar_open;
                    }
                    let session = self.session().cloned();
                    ui.vertical(|ui| {
                        ui.spacing_mut().item_spacing.y = 1.0;
                        let title = session
                            .as_ref()
                            .map_or("New conversation", |session| session.title.as_str());
                        ui.label(
                            RichText::new(title)
                                .font(theme::semibold(15.0))
                                .color(theme::TEXT),
                        );
                        if let Some(session) = &session {
                            let workspace = self
                                .view()
                                .workspaces
                                .iter()
                                .find(|workspace| workspace.id == session.workspace)
                                .map_or(session.workspace.clone(), |workspace| {
                                    workspace.name.clone()
                                });
                            let provider = self
                                .view()
                                .provider(&session.provider)
                                .map_or(session.provider.clone(), |provider| provider.name.clone());
                            let detail = if session.cwd.is_empty() {
                                format!("{workspace}  ·  {provider}")
                            } else {
                                format!("{workspace}  ·  {}", compact_path(&session.cwd))
                            };
                            ui.add(
                                egui::Label::new(
                                    RichText::new(detail)
                                        .font(theme::ui(11.0))
                                        .color(theme::MUTED),
                                )
                                .truncate(),
                            );
                        } else {
                            ui.label(
                                RichText::new("Choose a workspace and start an agent")
                                    .font(theme::ui(11.0))
                                    .color(theme::MUTED),
                            );
                        }
                    });
                    ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                        if header_icon(ui, Icon::More, "Conversation actions") {
                            self.open_session_menu();
                        }
                        if header_icon(ui, Icon::PanelLeft, "Inspector") {
                            self.inspector = !self.inspector;
                        }
                        if header_icon(ui, Icon::Copy, "Copy conversation") {
                            self.copy_conversation();
                        }
                        if let Some(session) = &self.session().cloned() {
                            status_chip(ui, session.status, self.reduced_motion);
                        }
                    });
                });
            });
    }

    fn open_session_menu(&mut self) {
        self.session_actions = true;
    }

    fn copy_conversation(&mut self) {
        let Some(id) = self.session_id() else { return };
        let Some(entries) = self.view().entries.get(&id) else {
            return;
        };
        let mut text = String::new();
        for entry in entries {
            match &entry.kind {
                EntryKind::User { text: value } => {
                    text.push_str("You: ");
                    text.push_str(value);
                    text.push('\n');
                }
                EntryKind::Assistant { text: value, .. } => {
                    text.push_str(value);
                    text.push_str("\n\n");
                }
                EntryKind::Tool { name, target, .. } => {
                    text.push_str(&format!("[{name}] {target}\n"));
                }
                _ => {}
            }
        }
        if text.is_empty() {
            self.warn("Nothing to copy yet.");
        } else {
            ui_copy(self, text);
        }
    }

    /// With no session selected, the first prompt creates the agent and then
    /// sends itself once the daemon reports the new session.
    fn start_from_prompt(&mut self) -> bool {
        let Some(workspace) = self.workspace_filter.clone().or_else(|| {
            self.view()
                .workspaces
                .first()
                .map(|workspace| workspace.id.clone())
        }) else {
            self.warn("Add a workspace before starting an agent.");
            self.new_workspace.open = true;
            return false;
        };
        let draft = self
            .drafts
            .get(NEW_CONVERSATION)
            .cloned()
            .unwrap_or_default();
        let prompt = draft.trim().to_owned();
        let has_images = self
            .images
            .get(NEW_CONVERSATION)
            .is_some_and(|images| !images.is_empty());
        if prompt.is_empty() && !has_images {
            return false;
        }
        let title = truncate(
            prompt
                .lines()
                .next()
                .filter(|line| !line.trim().is_empty())
                .unwrap_or("Image request"),
            52,
        );
        let provider = self
            .pending_model
            .as_ref()
            .map(|(provider, _)| provider.clone());
        if self.pending_prompt.is_some() {
            return false;
        }
        if self.backend.submit(Action::CreateAgent {
            workspace,
            provider,
            title: Some(title),
        }) {
            self.pending_prompt = Some(prompt);

            true
        } else {
            false
        }
    }

    fn central(&mut self, root: &mut egui::Ui, breakpoint: Breakpoint) {
        let has_inspector =
            self.inspector && root.available_width() >= 850.0 && breakpoint == Breakpoint::Wide;
        if has_inspector {
            egui::Panel::right("inspector")
                .exact_size(300.0)
                .frame(
                    egui::Frame::new()
                        .fill(theme::SIDEBAR)
                        .inner_margin(Margin::symmetric(16, 14)),
                )
                .show(root, |ui| self.inspector_contents(ui));
        }
        egui::CentralPanel::default()
            .frame(
                egui::Frame::new()
                    .fill(theme::CANVAS)
                    .inner_margin(Margin::symmetric(24, 0)),
            )
            .show(root, |ui| {
                if let Some(tab) = self.settings {
                    self.settings_view(ui, tab);
                } else {
                    self.conversation(ui);
                }
            });
    }

    fn conversation(&mut self, ui: &mut egui::Ui) {
        let Some(id) = self.session_id() else {
            self.empty_state(ui);
            return;
        };
        let entries = self.view().entries.get(&id).cloned().unwrap_or_default();
        if entries.is_empty() {
            self.empty_state(ui);
            return;
        }
        let mut stick = self.stick.get(&id).copied().unwrap_or(true);
        let Some(mut cache) = self.caches.remove(&id) else {
            self.caches.insert(id.clone(), MarkdownCache::default());
            return;
        };
        let running = self.view().running(&id);
        let provider = self
            .session()
            .map(|session| session.provider.clone())
            .unwrap_or_else(|| "talos".into());
        let provider_name = self
            .view()
            .provider(&provider)
            .map_or(provider, |provider| provider.name.clone());
        let motion = !self.reduced_motion;
        let actions = {
            let mut state = TimelineState {
                session_id: &id,
                entries: &entries,
                running,
                motion,
                cache: &mut cache,
                stick: &mut stick,
                provider: &provider_name,
            };
            timeline::show(ui, &mut state)
        };
        self.caches.insert(id.clone(), cache);
        self.stick.insert(id, stick);
        for action in actions {
            match action {
                TimelineAction::Decide {
                    session,
                    id: permission,
                    allow,
                } => {
                    if self.backend.submit(Action::Decision {
                        id: permission.clone(),
                        session,
                        allow,
                    }) {
                        self.backend
                            .view
                            .pending
                            .insert(permission, PendingState::Submitting);
                    }
                }
            }
        }
    }

    fn empty_state(&mut self, ui: &mut egui::Ui) {
        ui.add_space((ui.available_height() * 0.24).clamp(12.0, 160.0));
        ui.vertical_centered(|ui| {
            ui.set_max_width((ui.available_width() - 24.0).clamp(280.0, 560.0));
            let (rect, _) = ui.allocate_exact_size(Vec2::splat(46.0), Sense::hover());
            paint_logo(ui, rect);
            ui.add_space(18.0);
            ui.label(
                RichText::new("What are we building?")
                    .font(theme::semibold(24.0))
                    .color(theme::TEXT),
            );
            ui.add_space(8.0);
            ui.add(
                egui::Label::new(
                    RichText::new(match self.session() {
                        Some(session) if !session.workspace.is_empty() => {
                            "Talos runs the coding loop; Talos Desktop keeps the workspace, history and decisions in one place."
                        }
                        _ => "Add a workspace to give the agent a place to work.",
                    })
                    .font(theme::ui(13.0))
                    .color(theme::SECONDARY),
                )
                .wrap(),
            );
            ui.add_space(26.0);
            let suggestions = [
                "Explain the architecture of this repo",
                "Review my uncommitted changes",
                "Find the riskiest code path",
            ];
            ui.vertical_centered(|ui| {
                ui.spacing_mut().item_spacing = Vec2::new(0.0, 8.0);
                for suggestion in suggestions {
                    if composer::suggestion_chip(ui, suggestion).clicked() {
                        let id = self
                            .session_id()
                            .unwrap_or_else(|| NEW_CONVERSATION.into());
                        self.drafts.insert(id, suggestion.to_owned());
                        self.composer_focus = true;
                    }
                }
            });
            if !self.view().online {
                ui.add_space(22.0);
                ui.label(
                    RichText::new(
                        "Connect to the local daemon to create agents. Your drafts stay here.",
                    )
                    .font(theme::ui(11.5))
                    .color(theme::AMBER),
                );
            }
        });
    }

    fn composer_panel(&mut self, root: &mut egui::Ui, breakpoint: Breakpoint) {
        if self.settings.is_some() {
            return;
        }
        let id = self
            .session_id()
            .unwrap_or_else(|| NEW_CONVERSATION.to_owned());
        let has_session = self.session_id().is_some();
        let mut draft = self.drafts.remove(&id).unwrap_or_default();
        let width = root.available_width();
        let attachments = self.images.get(&id).cloned().unwrap_or_default();
        let target = composer::height_with_attachments(
            &draft,
            width.min(theme::COMPOSER_MAX),
            attachments.len(),
        );
        let height = if self.reduced_motion {
            target
        } else {
            root.ctx()
                .animate_value_with_time(egui::Id::unique("composer-height"), target, 0.12)
        };
        let session = self.session().cloned();
        let provider_id = session
            .as_ref()
            .map(|session| session.provider.as_str())
            .or_else(|| {
                self.pending_model
                    .as_ref()
                    .map(|(provider, _)| provider.as_str())
            })
            .unwrap_or("talos")
            .to_owned();
        let remembered_model = (!has_session)
            .then(|| {
                self.pending_model
                    .as_ref()
                    .map(|(_, model)| model.clone())
                    .or_else(|| self.last_model("talos", None))
            })
            .flatten();
        let usage = self.view().usage.get(&id).cloned();
        let providers = self.view().providers.clone();
        let running = has_session && self.view().running(&id);
        let online = self.view().online
            && (self.preview
                || !has_session
                || (self.backend.ready.contains(&id)
                    && session.as_ref().is_some_and(|s| !s.archived)))
            && self.pending_prompt.is_none()
            && !self
                .backend
                .retry_prompt
                .as_ref()
                .is_some_and(|a| matches!(a, Action::Prompt {session, ..} if session == &id));
        let config_options = self
            .view()
            .config_options
            .get(&id)
            .cloned()
            .unwrap_or_default();
        let motion = !self.reduced_motion;
        if self.composer_focus {
            root.ctx().memory_mut(|memory| {
                memory.request_focus(composer::composer_id(&id));
            });
            self.composer_focus = false;
        }
        egui::Panel::bottom("composer")
            .exact_size(height + 18.0)
            .frame(
                egui::Frame::new()
                    .fill(theme::CANVAS)
                    .inner_margin(Margin::symmetric(0, 6)),
            )
            .show(root, |ui| {
                ui.set_max_width(ui.available_width());
                let mut state = ComposerState {
                    session_id: &id,
                    provider_id: &provider_id,
                    draft: &mut draft,
                    running,
                    online,
                    session: session.as_ref(),
                    remembered_model: remembered_model.as_deref(),
                    providers: &providers,
                    usage: usage.as_ref(),
                    motion,
                    attachments: &attachments,
                    config_options: &config_options,
                };
                let action = composer::show(ui, &mut state);
                let _ = breakpoint;
                match action {
                    Some(ComposerAction::Send) => {
                        self.drafts.insert(id.clone(), draft.clone());
                        let sent = if has_session {
                            self.send()
                        } else {
                            self.start_from_prompt()
                        };
                        if sent {
                            draft.clear();
                        }
                    }
                    Some(ComposerAction::Stop) => {
                        self.backend.submit(Action::Cancel(id.clone()));
                    }
                    Some(ComposerAction::SetModel { provider, model }) => {
                        if has_session {
                            if self.backend.submit(Action::SetModel {
                                session: id.clone(),
                                model: model.clone(),
                            }) {
                                self.notify(format!("Model set to {model} for the next turn."));
                            }
                        } else {
                            self.pending_model = Some((provider, model.clone()));
                            self.notify(format!(
                                "Model {model} selected for the new conversation."
                            ));
                        }
                    }
                    Some(ComposerAction::Attach) => {
                        self.attachment_dialog = Some((id.clone(), String::new()));
                    }
                    Some(ComposerAction::RemoveAttachment(index)) => {
                        if let Some(images) = self.images.get_mut(&id)
                            && index < images.len()
                        {
                            images.remove(index);
                        }
                        if self.images.get(&id).is_some_and(Vec::is_empty) {
                            self.images.remove(&id);
                        }
                    }
                    Some(ComposerAction::SetMode(mode)) => {
                        self.backend.submit(Action::Rpc(Command::SetMode {
                            session_id: id.clone(),
                            mode_id: mode,
                        }));
                    }
                    Some(ComposerAction::SetConfig { option, value }) => {
                        self.backend.submit(Action::SetConfig {
                            session: id.clone(),
                            option,
                            value,
                        });
                    }
                    None => {}
                }
            });
        self.drafts.insert(id, draft);
    }

    fn status_bar(&mut self, root: &mut egui::Ui) {
        egui::Panel::bottom("status")
            .exact_size(24.0)
            .frame(
                egui::Frame::new()
                    .fill(theme::CANVAS)
                    .inner_margin(Margin::symmetric(18, 2)),
            )
            .show(root, |ui| {
                ui.horizontal(|ui| {
                    let (rect, _) = ui.allocate_exact_size(Vec2::splat(6.0), Sense::hover());
                    let online = self.view().online;
                    ui.painter().circle_filled(
                        rect.center(),
                        3.0,
                        if online { theme::GREEN } else { theme::AMBER },
                    );
                    ui.add(
                        egui::Label::new(
                            RichText::new(self.view().status.clone())
                                .font(theme::ui(10.5))
                                .color(theme::FAINT),
                        )
                        .truncate(),
                    );
                    ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                        ui.label(
                            RichText::new("Enter to send  ·  Shift+Enter newline")
                                .font(theme::ui(10.0))
                                .color(theme::FAINT),
                        );
                        if let Some(session) = self.session() {
                            ui.label(
                                RichText::new(format!(
                                    "{} · {}",
                                    provider_label(self.view(), &session.provider),
                                    session.model_label()
                                ))
                                .font(theme::ui(10.5))
                                .color(theme::MUTED),
                            );
                        }
                    });
                });
            });
    }

    fn inspector_contents(&mut self, ui: &mut egui::Ui) {
        ui.horizontal(|ui| {
            ui.label(
                RichText::new("Activity")
                    .font(theme::semibold(13.0))
                    .color(theme::TEXT),
            );
            ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                if ui
                    .add(
                        egui::Button::new(
                            RichText::new("Close")
                                .font(theme::ui(10.5))
                                .color(theme::FAINT),
                        )
                        .frame(false),
                    )
                    .clicked()
                {
                    self.inspector = false;
                }
            });
        });
        ui.add_space(10.0);
        let Some(session) = self.session().cloned() else {
            ui.label(
                RichText::new("No session selected.")
                    .font(theme::ui(12.0))
                    .color(theme::MUTED),
            );
            return;
        };
        widgets::divider(ui);
        ui.add_space(6.0);
        for (label, value) in [
            (
                "Provider",
                provider_label(self.view(), &session.provider).to_owned(),
            ),
            ("Agent ID", session.agent_id.clone()),
            ("Model", session.model_label()),
            ("Workspace", session.workspace.clone()),
            ("Directory", compact_path(&session.cwd)),
        ] {
            ui.label(
                RichText::new(label.to_uppercase())
                    .font(theme::semibold(9.5))
                    .color(theme::FAINT),
            );
            ui.add(
                egui::Label::new(
                    RichText::new(value)
                        .font(theme::mono(11.0))
                        .color(theme::SECONDARY),
                )
                .wrap_mode(TextWrapMode::Wrap),
            );
            ui.add_space(8.0);
        }
        if let Some(usage) = self.view().usage.get(&session.id) {
            widgets::divider(ui);
            ui.add_space(6.0);
            ui.label(
                RichText::new("USAGE")
                    .font(theme::semibold(9.5))
                    .color(theme::FAINT),
            );
            let mut parts = Vec::new();
            if let Some(value) = usage.input_tokens {
                parts.push(format!("in {}", crate::backend::format_tokens(value)));
            }
            if let Some(value) = usage.output_tokens {
                parts.push(format!("out {}", crate::backend::format_tokens(value)));
            }
            if let (Some(used), Some(size)) = (usage.context_used, usage.context_size) {
                parts.push(format!("context {used}/{size}"));
            }
            ui.label(
                RichText::new(parts.join(" · "))
                    .font(theme::mono(10.5))
                    .color(theme::SECONDARY),
            );
        }
    }

    fn settings_view(&mut self, ui: &mut egui::Ui, tab: SettingsTab) {
        ui.add_space(18.0);
        ui.horizontal(|ui| {
            ui.label(
                RichText::new("Settings")
                    .font(theme::semibold(20.0))
                    .color(theme::TEXT),
            );
            ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                if ui
                    .add(
                        egui::Button::new(
                            RichText::new("Close")
                                .font(theme::ui(11.0))
                                .color(theme::FAINT),
                        )
                        .frame(false),
                    )
                    .clicked()
                {
                    self.settings = None;
                }
            });
        });
        ui.add_space(14.0);
        ui.horizontal_top(|ui| {
            ui.vertical(|ui| {
                ui.set_width(168.0);
                for (item, label) in [
                    (SettingsTab::Appearance, "Appearance"),
                    (SettingsTab::Providers, "Providers"),
                    (SettingsTab::Permissions, "Permissions"),
                    (SettingsTab::Diagnostics, "Diagnostics"),
                    (SettingsTab::About, "About"),
                    (SettingsTab::Workspaces, "Workspaces"),
                    (SettingsTab::History, "History search"),
                    (SettingsTab::Terminal, "Terminal"),
                ] {
                    let selected = tab == item;
                    if ui
                        .add_sized(
                            [160.0, 32.0],
                            egui::Button::new(
                                RichText::new(label)
                                    .font(theme::medium(12.5))
                                    .color(if selected {
                                        theme::TEXT
                                    } else {
                                        theme::SECONDARY
                                    }),
                            )
                            .fill(if selected {
                                theme::SURFACE
                            } else {
                                Color32::TRANSPARENT
                            })
                            .corner_radius(8),
                        )
                        .clicked()
                    {
                        self.settings = Some(item);
                    }
                }
            });
            ui.add_space(18.0);
            egui::ScrollArea::vertical().show(ui, |ui| {
                ui.set_max_width(560.0);
                match tab {
                    SettingsTab::Appearance => self.settings_appearance(ui),
                    SettingsTab::Providers => self.settings_providers(ui),
                    SettingsTab::Permissions => self.settings_permissions(ui),
                    SettingsTab::Diagnostics => self.settings_diagnostics(ui),
                    SettingsTab::About => self.settings_about(ui),
                    SettingsTab::Workspaces => self.workspace_settings(ui),
                    SettingsTab::History => self.history_settings(ui),
                    SettingsTab::Terminal => self.terminal_settings(ui),
                }
            });
        });
    }

    fn settings_appearance(&mut self, ui: &mut egui::Ui) {
        settings_heading(ui, "Appearance & motion");
        ui.checkbox(&mut self.reduced_motion, "Reduce motion");
        ui.label(
            RichText::new(
                "Disables transitions and the working pulse; state changes stay immediate.",
            )
            .font(theme::ui(11.0))
            .color(theme::MUTED),
        );
        ui.add_space(12.0);
        ui.checkbox(&mut self.high_contrast, "High contrast text and focus");
        ui.add_space(16.0);
        settings_heading(ui, "Interface scale");
        let mut zoom = self.zoom;
        if ui
            .add(egui::Slider::new(&mut zoom, 0.85..=2.0).text("Zoom"))
            .changed()
        {
            self.zoom = zoom;
            ui.ctx().set_zoom_factor(zoom);
        }
        ui.add_space(16.0);
        settings_heading(ui, "Keyboard");
        for (keys, action) in [
            ("⌘K", "Command center"),
            ("⌘N", "New agent"),
            ("⌘L", "Focus composer"),
            ("⌘B", "Toggle history"),
            ("⌘,", "Settings"),
            ("Esc", "Close the topmost layer"),
        ] {
            ui.horizontal(|ui| {
                widgets::kbd(ui, keys);
                ui.label(
                    RichText::new(action)
                        .font(theme::ui(11.5))
                        .color(theme::SECONDARY),
                );
            });
            ui.add_space(4.0);
        }
    }

    fn settings_providers(&mut self, ui: &mut egui::Ui) {
        settings_heading(ui, "Providers");
        ui.label(
            RichText::new(
                "Talos is the default backend and Claude Code is the only secondary provider. Existing sessions never change provider silently.",
            )
            .font(theme::ui(11.5))
            .color(theme::MUTED),
        );
        ui.add_space(12.0);
        let providers = self.view().providers.clone();
        for provider in providers {
            provider_card(ui, &provider);
            ui.add_space(8.0);
        }
        if self.view().providers.is_empty() {
            ui.label(
                RichText::new("No provider diagnostics available.")
                    .font(theme::ui(12.0))
                    .color(theme::MUTED),
            );
        }
        ui.add_space(12.0);
        ui.horizontal(|ui| {
            if ui
                .add(
                    egui::Button::new(
                        RichText::new("Refresh diagnostics")
                            .font(theme::medium(12.0))
                            .color(theme::TEXT),
                    )
                    .fill(theme::SURFACE)
                    .corner_radius(8),
                )
                .clicked()
            {
                self.backend
                    .submit(Action::Rpc(Command::ProviderDiagnostics {
                        provider_id: None,
                    }));
            }
            if ui
                .add(
                    egui::Button::new(
                        RichText::new("Copy config path")
                            .font(theme::medium(12.0))
                            .color(theme::SECONDARY),
                    )
                    .fill(theme::SURFACE)
                    .corner_radius(8),
                )
                .clicked()
            {
                ui_copy(
                    self,
                    self.backend.endpoint.replace("talosd.sock", "config.toml"),
                );
            }
        });
        ui.add_space(10.0);
        ui.label(
            RichText::new("Configure Talos credentials in ~/.talosd/config.toml under [providers.env]. Legacy ~/.paseo and ~/.talos are never modified.")
                .font(theme::ui(10.5))
                .color(theme::FAINT),
        );
    }

    fn settings_permissions(&mut self, ui: &mut egui::Ui) {
        settings_heading(ui, "Permissions");
        ui.label(
            RichText::new(
                "Every tool request fails closed. Pending decisions stay visible in their conversation and are never approved by timeout or restart.",
            )
            .font(theme::ui(11.5))
            .color(theme::MUTED),
        );
        ui.add_space(14.0);
        if self.view().pending.is_empty() {
            ui.label(
                RichText::new("No pending requests.")
                    .font(theme::ui(12.0))
                    .color(theme::GREEN),
            );
            return;
        }
        let sessions: Vec<(String, String)> = self
            .view()
            .sessions
            .iter()
            .filter(|session| session.status == SessionStatus::Attention)
            .map(|session| (session.id.clone(), session.title.clone()))
            .collect();
        for (id, title) in sessions {
            if ui
                .add(
                    egui::Button::new(
                        RichText::new(format!("⚠  {title}"))
                            .font(theme::medium(12.0))
                            .color(theme::AMBER),
                    )
                    .fill(theme::SURFACE)
                    .corner_radius(8),
                )
                .clicked()
            {
                self.select(&id);
            }
            ui.add_space(4.0);
        }
    }

    fn settings_diagnostics(&mut self, ui: &mut egui::Ui) {
        settings_heading(ui, "Diagnostics");
        for (label, value) in [
            ("Daemon endpoint", self.backend.endpoint.clone()),
            ("Status", self.view().status.clone()),
            ("Workspaces", self.view().workspaces.len().to_string()),
            ("Sessions", self.view().sessions.len().to_string()),
            ("Protocol", "v1 · newline JSON".to_owned()),
        ] {
            ui.label(
                RichText::new(label.to_uppercase())
                    .font(theme::semibold(9.5))
                    .color(theme::FAINT),
            );
            ui.add(
                egui::Label::new(
                    RichText::new(value)
                        .font(theme::mono(11.0))
                        .color(theme::SECONDARY),
                )
                .wrap_mode(TextWrapMode::Wrap),
            );
            ui.add_space(8.0);
        }
        ui.add_space(8.0);
        ui.horizontal(|ui| {
            if ui
                .add(
                    egui::Button::new(
                        RichText::new("Refresh snapshot")
                            .font(theme::medium(12.0))
                            .color(theme::TEXT),
                    )
                    .fill(theme::SURFACE)
                    .corner_radius(8),
                )
                .clicked()
            {
                self.backend.submit(Action::Refresh);
            }
            if ui
                .add(
                    egui::Button::new(
                        RichText::new("Copy log path")
                            .font(theme::medium(12.0))
                            .color(theme::SECONDARY),
                    )
                    .fill(theme::SURFACE)
                    .corner_radius(8),
                )
                .on_hover_text("talosd.log lives next to the daemon socket")
                .clicked()
            {
                ui_copy(
                    self,
                    self.backend.endpoint.replace("talosd.sock", "talosd.log"),
                );
            }
        });
        ui.add_space(14.0);
        ui.label(
            RichText::new("DANGER ZONE")
                .font(theme::semibold(9.5))
                .color(theme::RED),
        );
        ui.add_space(4.0);
        if ui
            .add(
                egui::Button::new(
                    RichText::new("Stop daemon")
                        .font(theme::medium(12.0))
                        .color(theme::RED),
                )
                .fill(theme::SURFACE)
                .stroke(Stroke::new(
                    1.0,
                    Color32::from_rgba_unmultiplied(224, 115, 106, 80),
                ))
                .corner_radius(8),
            )
            .on_hover_text("Interrupts active runs and cancels pending permissions.")
            .clicked()
        {
            self.confirmation = Some((
                "Stop the daemon and interrupt active runs?".into(),
                Action::Shutdown,
            ));
        }
    }

    fn settings_about(&mut self, ui: &mut egui::Ui) {
        settings_heading(ui, "Talos Desktop");
        ui.label(
            RichText::new(
                "A native cockpit for coding agents. Talos Desktop owns the workspace, durable timeline, permissions and history; Talos owns the default agent loop, with Claude Code as the only secondary provider.",
            )
            .font(theme::ui(12.0))
            .color(theme::SECONDARY),
        );
        ui.add_space(14.0);
        for (label, value) in [
            ("UI", "egui 0.37 · eframe · Inter"),
            ("Daemon", "talosd · tokio · SQLite"),
            ("Protocol", "ACP over stdio · versioned local IPC"),
            ("Privacy", "No legacy ~/.paseo data is read or written"),
        ] {
            ui.label(
                RichText::new(label.to_uppercase())
                    .font(theme::semibold(9.5))
                    .color(theme::FAINT),
            );
            ui.label(
                RichText::new(value)
                    .font(theme::ui(11.5))
                    .color(theme::SECONDARY),
            );
            ui.add_space(8.0);
        }
    }

    // -- overlays ------------------------------------------------------------

    fn overlays(&mut self, ctx: &egui::Context, breakpoint: Breakpoint) {
        self.palette_overlay(ctx);
        self.new_agent_dialog(ctx);
        self.new_workspace_dialog(ctx);
        self.rename_dialog(ctx);
        self.management_overlays(ctx);
        self.toast_overlay(ctx);
        if breakpoint != Breakpoint::Wide && self.sidebar_open && self.settings.is_none() {
            self.drawer(ctx);
        }
    }

    fn drawer(&mut self, ctx: &egui::Context) {
        egui::Window::new("History")
            .id(egui::Id::unique("history-drawer"))
            .fixed_pos(egui::pos2(12.0, 64.0))
            .default_width(280.0)
            .max_height(ctx.content_rect().height() - 140.0)
            .collapsible(false)
            .resizable(false)
            .frame(
                egui::Frame::new()
                    .fill(theme::SIDEBAR)
                    .stroke(Stroke::new(1.0, theme::LINE_STRONG))
                    .corner_radius(14)
                    .inner_margin(Margin::symmetric(10, 10)),
            )
            .show(ctx, |ui| self.sidebar_contents(ui));
    }

    fn palette_overlay(&mut self, ctx: &egui::Context) {
        if !self.palette.open {
            return;
        }
        let items = self.palette_items();
        let filtered: Vec<&PaletteItem> = items
            .iter()
            .filter(|item| {
                self.palette.query.is_empty()
                    || item
                        .label
                        .to_lowercase()
                        .contains(&self.palette.query.to_lowercase())
            })
            .collect();
        if !filtered.is_empty() {
            self.palette.index = self.palette.index.min(filtered.len() - 1);
        } else {
            self.palette.index = 0;
        }
        // Scrim.
        let screen = ctx.content_rect();
        ctx.layer_painter(egui::LayerId::new(
            egui::Order::Foreground,
            egui::Id::unique("palette-scrim"),
        ))
        .rect_filled(screen, 0.0, Color32::from_black_alpha(120));

        let width = screen.width().min(600.0) - 48.0;
        let mut close = false;
        let area = egui::Area::new(egui::Id::unique("palette"))
            .order(egui::Order::Tooltip)
            .fixed_pos(egui::pos2(
                screen.center().x - width * 0.5,
                screen.top() + 92.0,
            ))
            .show(ctx, |ui| {
                egui::Frame::new()
                    .fill(theme::SURFACE_HIGH)
                    .stroke(Stroke::new(1.0, theme::LINE_STRONG))
                    .corner_radius(16)
                    .inner_margin(Margin::symmetric(14, 12))
                    .show(ui, |ui| {
                        ui.set_width(width - 28.0);
                        ui.horizontal(|ui| {
                            icons::icon(ui, Icon::Search, 15.0, theme::MUTED);
                            let response = ui.add(
                                egui::TextEdit::singleline(&mut self.palette.query)
                                    .hint_text(
                                        RichText::new("Search agents, workspaces and commands")
                                            .font(theme::ui(13.5))
                                            .color(theme::MUTED),
                                    )
                                    .frame(egui::Frame::NONE)
                                    .font(theme::ui(13.5))
                                    .desired_width(f32::INFINITY),
                            );
                            if self.palette.focus {
                                response.request_focus();
                                self.palette.focus = false;
                            }
                            widgets::kbd(ui, "Esc");
                        });
                        ui.add_space(8.0);
                        widgets::divider(ui);
                        ui.add_space(2.0);
                        let filtered = filtered.clone();
                        if filtered.is_empty() {
                            ui.add_space(12.0);
                            ui.label(
                                RichText::new("No matches. Try a session title or “settings”.")
                                    .font(theme::ui(12.0))
                                    .color(theme::FAINT),
                            );
                            ui.add_space(12.0);
                        }
                        egui::ScrollArea::vertical()
                            .max_height(320.0)
                            .id_salt("palette-results")
                            .show(ui, |ui| {
                                for (index, item) in filtered.iter().enumerate() {
                                    let selected = index == self.palette.index;
                                    let response = ui.add_sized(
                                        [ui.available_width(), 38.0],
                                        egui::Button::new(
                                            RichText::new(format!("  {}", item.label))
                                                .font(theme::medium(12.5))
                                                .color(if selected {
                                                    theme::TEXT
                                                } else {
                                                    theme::SECONDARY
                                                }),
                                        )
                                        .fill(if selected {
                                            theme::SELECTED
                                        } else {
                                            Color32::TRANSPARENT
                                        })
                                        .corner_radius(8),
                                    );
                                    let icon_rect = egui::Rect::from_center_size(
                                        egui::pos2(
                                            response.rect.left() + 18.0,
                                            response.rect.center().y,
                                        ),
                                        Vec2::splat(15.0),
                                    );
                                    icons::paint(
                                        ui.painter(),
                                        icon_rect,
                                        item.icon,
                                        if selected { theme::BLUE } else { theme::MUTED },
                                        1.5,
                                    );
                                    if !item.hint.is_empty() {
                                        let galley = ui.painter().layout_no_wrap(
                                            item.hint.clone(),
                                            theme::ui(10.5),
                                            theme::FAINT,
                                        );
                                        ui.painter().galley(
                                            egui::pos2(
                                                response.rect.right() - galley.size().x - 12.0,
                                                response.rect.center().y - galley.size().y * 0.5,
                                            ),
                                            galley,
                                            theme::FAINT,
                                        );
                                    }
                                    if response.clicked() {
                                        self.palette.index = index;
                                        self.palette.open = false;
                                        self.run_palette_action(item.action);
                                        close = true;
                                    }
                                    if selected && response.hovered() {
                                        // Keep hover aligned with the keyboard cursor.
                                    }
                                }
                            });
                        ui.add_space(6.0);
                        ui.horizontal(|ui| {
                            for (keys, label) in
                                [("↑↓", "navigate"), ("↵", "run"), ("⌘N", "new agent")]
                            {
                                widgets::kbd(ui, keys);
                                ui.label(
                                    RichText::new(label)
                                        .font(theme::ui(10.0))
                                        .color(theme::FAINT),
                                );
                                ui.add_space(6.0);
                            }
                        });
                    });
            });
        if close {
            return;
        }
        if ctx.input(|input| input.pointer.any_click())
            && !area
                .response
                .rect
                .contains(ctx.input(|input| input.pointer.interact_pos().unwrap_or_default()))
        {
            self.palette.open = false;
        }
        let (up, down, enter) = ctx.input(|input| {
            (
                input.key_pressed(egui::Key::ArrowUp),
                input.key_pressed(egui::Key::ArrowDown),
                input.key_pressed(egui::Key::Enter),
            )
        });
        if down && !filtered.is_empty() {
            self.palette.index = (self.palette.index + 1) % filtered.len();
        }
        if up && !filtered.is_empty() {
            self.palette.index = self
                .palette
                .index
                .checked_sub(1)
                .unwrap_or(filtered.len() - 1);
        }
        if enter && !filtered.is_empty() {
            let action = filtered[self.palette.index].action;
            self.palette.open = false;
            self.run_palette_action(action);
        }
    }

    fn palette_items(&self) -> Vec<PaletteItem> {
        let mut items = vec![
            PaletteItem {
                icon: Icon::Plus,
                label: "New agent".into(),
                hint: "⌘N".into(),
                action: PaletteAction::NewAgent,
            },
            PaletteItem {
                icon: Icon::Folder,
                label: "New workspace".into(),
                hint: String::new(),
                action: PaletteAction::NewWorkspace,
            },
            PaletteItem {
                icon: Icon::Conversations,
                label: "Toggle history sidebar".into(),
                hint: "⌘B".into(),
                action: PaletteAction::ToggleSidebar,
            },
            PaletteItem {
                icon: Icon::PanelLeft,
                label: "Toggle inspector".into(),
                hint: String::new(),
                action: PaletteAction::ToggleInspector,
            },
            PaletteItem {
                icon: Icon::Compose,
                label: "Focus composer".into(),
                hint: "⌘L".into(),
                action: PaletteAction::FocusComposer,
            },
            PaletteItem {
                icon: Icon::Settings,
                label: "Settings".into(),
                hint: "⌘,".into(),
                action: PaletteAction::Settings(SettingsTab::Appearance),
            },
            PaletteItem {
                icon: Icon::Cpu,
                label: "Provider diagnostics".into(),
                hint: String::new(),
                action: PaletteAction::Settings(SettingsTab::Providers),
            },
            PaletteItem {
                icon: Icon::Refresh,
                label: "Refresh from daemon".into(),
                hint: String::new(),
                action: PaletteAction::Refresh,
            },
            PaletteItem {
                icon: Icon::Shield,
                label: "Permissions".into(),
                hint: String::new(),
                action: PaletteAction::Settings(SettingsTab::Permissions),
            },
            PaletteItem {
                icon: Icon::Stop,
                label: "Stop daemon".into(),
                hint: String::new(),
                action: PaletteAction::Shutdown,
            },
        ];
        // Sessions, newest first.
        for (index, session) in self.view().sessions.iter().enumerate() {
            items.push(PaletteItem {
                icon: Icon::Terminal,
                label: session.title.clone(),
                hint: session.time.clone(),
                action: PaletteAction::Select(index),
            });
        }
        items
    }

    fn run_palette_action(&mut self, action: PaletteAction) {
        match action {
            PaletteAction::NewAgent => self.create_agent(),
            PaletteAction::NewWorkspace => self.new_workspace.open = true,
            PaletteAction::Settings(tab) => self.settings = Some(tab),
            PaletteAction::ToggleSidebar => self.sidebar_open = !self.sidebar_open,
            PaletteAction::ToggleInspector => self.inspector = !self.inspector,
            PaletteAction::FocusComposer => self.composer_focus = true,
            PaletteAction::Refresh => {
                self.backend.submit(Action::Refresh);
                self.notify("Refreshing from daemon…");
            }
            PaletteAction::Shutdown => {
                self.backend.submit(Action::Shutdown);
                self.warn("Stop request sent.");
            }
            PaletteAction::Select(index) => {
                if let Some(session) = self.view().sessions.get(index) {
                    let id = session.id.clone();
                    self.select(&id);
                }
            }
        }
    }

    fn new_agent_dialog(&mut self, ctx: &egui::Context) {
        if !self.new_agent.open {
            return;
        }
        let mut open = true;
        egui::Window::new("New agent")
            .open(&mut open)
            .collapsible(false)
            .resizable(false)
            .default_width(380.0)
            .frame(
                egui::Frame::new()
                    .fill(theme::SURFACE_HIGH)
                    .stroke(Stroke::new(1.0, theme::LINE_STRONG))
                    .corner_radius(14)
                    .inner_margin(Margin::symmetric(18, 16)),
            )
            .show(ctx, |ui| {
                ui.label(
                    RichText::new("TITLE")
                        .font(theme::semibold(9.5))
                        .color(theme::FAINT),
                );
                ui.add(
                    egui::TextEdit::singleline(&mut self.new_agent.title)
                        .hint_text("What should this agent work on?")
                        .desired_width(f32::INFINITY),
                );
                ui.add_space(10.0);
                ui.label(
                    RichText::new("WORKSPACE")
                        .font(theme::semibold(9.5))
                        .color(theme::FAINT),
                );
                let workspace_name = self
                    .new_agent
                    .workspace
                    .as_ref()
                    .and_then(|id| {
                        self.view()
                            .workspaces
                            .iter()
                            .find(|workspace| &workspace.id == id)
                    })
                    .map_or("Select a workspace", |workspace| workspace.name.as_str())
                    .to_owned();
                egui::ComboBox::from_id_salt("agent-workspace")
                    .selected_text(workspace_name)
                    .width(330.0)
                    .show_ui(ui, |ui| {
                        let workspaces = self.view().workspaces.clone();
                        for workspace in workspaces {
                            if ui
                                .selectable_label(
                                    self.new_agent.workspace.as_deref()
                                        == Some(workspace.id.as_str()),
                                    &workspace.name,
                                )
                                .clicked()
                            {
                                self.new_agent.workspace = Some(workspace.id.clone());
                            }
                        }
                    });
                ui.add_space(10.0);
                ui.label(
                    RichText::new("PROVIDER")
                        .font(theme::semibold(9.5))
                        .color(theme::FAINT),
                );
                let providers = self.view().providers.clone();
                for provider in providers {
                    let selected = self.new_agent.provider.as_deref() == Some(provider.id.as_str());
                    let label = if provider.default {
                        format!("{}  ·  primary", provider.name)
                    } else {
                        format!("{}  ·  secondary", provider.name)
                    };
                    if ui
                        .add_enabled(
                            provider.available,
                            egui::Button::new(RichText::new(label).font(theme::ui(12.0)).color(
                                if selected {
                                    theme::TEXT
                                } else {
                                    theme::SECONDARY
                                },
                            ))
                            .fill(if selected {
                                theme::SELECTED
                            } else {
                                Color32::TRANSPARENT
                            })
                            .corner_radius(8),
                        )
                        .clicked()
                    {
                        self.new_agent.provider = Some(provider.id.clone());
                    }
                }
                ui.add_space(14.0);
                let ready = self.view().online
                    && self.backend.busy == 0
                    && self.new_agent.workspace.is_some()
                    && self
                        .new_agent
                        .provider
                        .as_deref()
                        .and_then(|id| self.view().provider(id))
                        .is_some_and(|p| p.available);
                if ui
                    .add_enabled(
                        ready,
                        egui::Button::new(
                            RichText::new("Create agent")
                                .font(theme::medium(12.5))
                                .color(Color32::from_rgb(13, 18, 22)),
                        )
                        .fill(theme::BLUE)
                        .corner_radius(9)
                        .min_size(Vec2::new(120.0, 32.0)),
                    )
                    .clicked()
                    && let Some(workspace) = self.new_agent.workspace.clone()
                {
                    let title = self.new_agent.title.trim().to_owned();
                    self.backend.submit(Action::CreateAgent {
                        workspace,
                        provider: self.new_agent.provider.clone(),
                        title: (!title.is_empty()).then_some(title),
                    });
                }
            });
        self.new_agent.open &= open;
    }

    fn new_workspace_dialog(&mut self, ctx: &egui::Context) {
        if !self.new_workspace.open {
            return;
        }
        let mut open = true;
        egui::Window::new("Add workspace")
            .open(&mut open)
            .collapsible(false)
            .resizable(false)
            .default_width(400.0)
            .show(ctx, |ui| {
                ui.label(
                    RichText::new("NAME")
                        .font(theme::semibold(9.5))
                        .color(theme::FAINT),
                );
                ui.add(
                    egui::TextEdit::singleline(&mut self.new_workspace.name)
                        .hint_text("my-project")
                        .desired_width(f32::INFINITY),
                );
                ui.add_space(8.0);
                ui.label(
                    RichText::new("ABSOLUTE PATH")
                        .font(theme::semibold(9.5))
                        .color(theme::FAINT),
                );
                ui.add(
                    egui::TextEdit::singleline(&mut self.new_workspace.path)
                        .hint_text("/home/you/repos/project")
                        .desired_width(f32::INFINITY),
                );
                ui.add_space(12.0);
                let ready = self.view().online
                    && self.backend.busy == 0
                    && !self.new_workspace.name.trim().is_empty()
                    && self.new_workspace.path.starts_with('/');
                if ui
                    .add_enabled(
                        ready,
                        egui::Button::new(
                            RichText::new("Add workspace")
                                .font(theme::medium(12.5))
                                .color(Color32::from_rgb(13, 18, 22)),
                        )
                        .fill(theme::BLUE)
                        .corner_radius(9),
                    )
                    .clicked()
                {
                    self.backend.submit(Action::CreateWorkspace {
                        name: self.new_workspace.name.trim().to_owned(),
                        path: self.new_workspace.path.trim().to_owned(),
                    });
                }
            });
        self.new_workspace.open &= open;
    }

    fn rename_dialog(&mut self, ctx: &egui::Context) {
        let Some((id, mut title, original)) = self.rename.take() else {
            return;
        };
        let mut open = true;
        let mut submit = false;
        egui::Window::new("Rename agent")
            .open(&mut open)
            .collapsible(false)
            .resizable(false)
            .default_width(360.0)
            .show(ctx, |ui| {
                ui.add(egui::TextEdit::singleline(&mut title).desired_width(f32::INFINITY));
                ui.add_space(10.0);
                if ui
                    .add_enabled(
                        !title.trim().is_empty() && title != original,
                        egui::Button::new("Rename")
                            .fill(theme::BLUE)
                            .corner_radius(8),
                    )
                    .clicked()
                {
                    submit = true;
                }
            });
        if submit {
            self.backend.submit(Action::Rename {
                session: id,
                title: title.trim().to_owned(),
            });
        } else if open {
            self.rename = Some((id, title, original));
        }
    }

    fn toast_overlay(&mut self, ctx: &egui::Context) {
        let Some(toast) = &self.toast else { return };
        let age = toast.at.elapsed();
        if !toast.sticky && age > Duration::from_secs(5) {
            self.toast = None;
            return;
        }
        let fade = if age < Duration::from_millis(160) {
            (age.as_secs_f32() / 0.16).clamp(0.0, 1.0)
        } else if !toast.sticky && age > Duration::from_secs(4) {
            (1.0 - (age.as_secs_f32() - 4.0)).clamp(0.0, 1.0)
        } else {
            1.0
        };
        let screen = ctx.content_rect();
        let width = screen.width().min(560.0) - 32.0;
        let mut dismiss = false;
        egui::Area::new(egui::Id::unique("toast"))
            .order(egui::Order::Tooltip)
            .fixed_pos(egui::pos2(
                screen.center().x - width * 0.5,
                screen.bottom() - 150.0 + (1.0 - fade) * 12.0,
            ))
            .show(ctx, |ui| {
                ui.multiply_opacity(fade);
                egui::Frame::new()
                    .fill(theme::SURFACE_HIGH)
                    .stroke(Stroke::new(1.0, theme::LINE_STRONG))
                    .corner_radius(12)
                    .inner_margin(Margin::symmetric(14, 10))
                    .show(ui, |ui| {
                        ui.set_width(width - 28.0);
                        ui.horizontal(|ui| {
                            icons::icon(ui, Icon::Sparkle, 13.0, toast.tone);
                            ui.add(
                                egui::Label::new(
                                    RichText::new(&toast.text)
                                        .font(theme::ui(12.0))
                                        .color(theme::TEXT),
                                )
                                .wrap_mode(TextWrapMode::Wrap),
                            );
                            ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                                if ui
                                    .add(
                                        egui::Button::new(
                                            RichText::new("Dismiss")
                                                .font(theme::ui(10.5))
                                                .color(theme::FAINT),
                                        )
                                        .frame(false),
                                    )
                                    .clicked()
                                {
                                    dismiss = true;
                                }
                            });
                        });
                    });
            });
        if dismiss {
            self.toast = None;
        }
        ctx.request_repaint_after(Duration::from_millis(120));
    }
}

impl eframe::App for Desktop {
    fn ui(&mut self, root: &mut egui::Ui, _frame: &mut eframe::Frame) {
        let ctx = root.ctx().clone();
        if let Some(text) = self.pending_clipboard.take() {
            ctx.copy_text(text);
        }
        if !self.preview {
            self.take_backend_events();
        }
        // Compositor-driven resizes can land after the last paint; repaint once
        // more so the new surface size is fully presented.
        let size = ctx.content_rect().size();
        if (size - self.last_size).length() > 0.5 {
            self.last_size = size;
            ctx.request_repaint();
        }
        self.ensure_selection();
        theme::install(&ctx, self.high_contrast);
        self.shortcuts(&ctx);
        let breakpoint = layout_for_width(root.available_width());
        self.rail(root, breakpoint);
        self.sidebar(root, breakpoint);
        self.header(root, breakpoint);
        self.status_bar(root);
        self.composer_panel(root, breakpoint);
        self.permission_panel(root);
        self.recovery_panel(root);
        self.attachment_panel(root);
        self.handle_dropped_files(&ctx);
        self.central(root, breakpoint);
        self.overlays(&ctx, breakpoint);
    }
}

// ---------------------------------------------------------------------------
// Small components and helpers
// ---------------------------------------------------------------------------

fn rail_button(ui: &mut egui::Ui, icon: Icon, active: bool, badge: usize) -> bool {
    let width = theme::RAIL_WIDTH - 18.0;
    let (rect, response) = ui.allocate_exact_size(Vec2::new(width, 38.0), Sense::click());
    let hover = motion::animate_bool(
        ui.ctx(),
        egui::Id::unique(("rail-hover", icon as u8)),
        response.hovered(),
        true,
    );
    let active_amount = motion::animate_bool(
        ui.ctx(),
        egui::Id::unique(("rail-active", icon as u8)),
        active,
        true,
    );
    let fill = if active_amount > 0.02 {
        theme::mix(theme::HOVER, theme::SURFACE_HIGH, active_amount)
    } else {
        theme::mix(theme::RAIL, theme::HOVER, hover)
    };
    if hover > 0.01 || active_amount > 0.01 {
        ui.painter().rect_filled(rect.shrink(3.0), 10.0, fill);
        if active_amount > 0.01 {
            let marker = egui::Rect::from_min_size(
                egui::pos2(rect.left() + 1.0, rect.center().y - 8.0),
                Vec2::new(2.0, 16.0),
            );
            ui.painter().rect_filled(
                marker,
                1.0,
                Color32::from_rgba_unmultiplied(
                    theme::BLUE.r(),
                    theme::BLUE.g(),
                    theme::BLUE.b(),
                    (255.0 * active_amount) as u8,
                ),
            );
        }
    }
    let color = if active {
        theme::TEXT
    } else if hover > 0.5 {
        theme::SECONDARY
    } else {
        theme::MUTED
    };
    icons::paint(
        ui.painter(),
        egui::Rect::from_center_size(rect.center(), Vec2::splat(19.0)),
        icon,
        color,
        1.6,
    );
    if badge > 0 {
        let badge_rect = egui::Rect::from_center_size(
            egui::pos2(rect.right() - 9.0, rect.top() + 10.0),
            Vec2::splat(15.0),
        );
        ui.painter()
            .circle_filled(badge_rect.center(), 7.5, theme::AMBER);
        ui.painter().text(
            badge_rect.center(),
            egui::Align2::CENTER_CENTER,
            badge.to_string(),
            theme::semibold(9.0),
            Color32::from_rgb(20, 18, 14),
        );
    }
    if response.hovered() {
        ui.ctx().set_cursor_icon(egui::CursorIcon::PointingHand);
    }
    response.clicked()
}

fn header_icon(ui: &mut egui::Ui, icon: Icon, tip: &str) -> bool {
    let (rect, response) = ui.allocate_exact_size(Vec2::splat(30.0), Sense::click());
    let hover = motion::animate_bool(
        ui.ctx(),
        egui::Id::unique(("header-icon", icon as u8)),
        response.hovered(),
        true,
    );
    if hover > 0.01 {
        ui.painter().rect_filled(
            rect.shrink(2.0),
            7.0,
            Color32::from_rgba_unmultiplied(
                theme::HOVER.r(),
                theme::HOVER.g(),
                theme::HOVER.b(),
                (220.0 * hover) as u8,
            ),
        );
    }
    icons::paint(
        ui.painter(),
        egui::Rect::from_center_size(rect.center(), Vec2::splat(15.0)),
        icon,
        if hover > 0.5 {
            theme::TEXT
        } else {
            theme::MUTED
        },
        1.5,
    );
    if response.hovered() {
        ui.ctx().set_cursor_icon(egui::CursorIcon::PointingHand);
    }
    response.on_hover_text(tip).clicked()
}

fn status_chip(ui: &mut egui::Ui, status: SessionStatus, motion: bool) {
    let color = match status {
        SessionStatus::Running => theme::BLUE,
        SessionStatus::Attention => theme::AMBER,
        SessionStatus::Error => theme::RED,
        SessionStatus::Offline => theme::FAINT,
        SessionStatus::Idle => theme::GREEN,
    };
    widgets::status_dot(
        ui,
        color,
        status == SessionStatus::Running,
        motion,
        egui::Id::unique("header-status"),
    );
    ui.label(
        RichText::new(status.label())
            .font(theme::ui(11.0))
            .color(color),
    );
    ui.add_space(4.0);
}

fn session_row(ui: &mut egui::Ui, session: &Session, selected: bool, reduced: bool) -> bool {
    let width = ui.available_width();
    let (rect, response) = ui.allocate_exact_size(Vec2::new(width, 52.0), Sense::click());
    let id = egui::Id::unique(("session-row", &session.id));
    let hover = motion::animate_bool(ui.ctx(), id, response.hovered(), !reduced);
    let active = motion::animate_bool(ui.ctx(), id.with("active"), selected, !reduced);
    let fill = if active > 0.01 {
        theme::mix(theme::SIDEBAR, theme::SELECTED, active)
    } else {
        theme::mix(theme::SIDEBAR, theme::HOVER, hover * 0.8)
    };
    if hover > 0.01 || active > 0.01 {
        ui.painter()
            .rect_filled(rect.shrink2(Vec2::new(4.0, 2.0)), 9.0, fill);
    }
    if active > 0.01 {
        let marker = egui::Rect::from_min_size(
            egui::pos2(rect.left() + 4.0, rect.center().y - 11.0),
            Vec2::new(2.0, 22.0),
        );
        ui.painter().rect_filled(
            marker,
            1.0,
            Color32::from_rgba_unmultiplied(
                theme::BLUE.r(),
                theme::BLUE.g(),
                theme::BLUE.b(),
                (255.0 * active) as u8,
            ),
        );
    }
    let avatar = egui::Rect::from_min_size(
        egui::pos2(rect.left() + 14.0, rect.center().y - 11.0),
        Vec2::splat(22.0),
    );
    let hue = hue_for(&session.id);
    ui.painter()
        .rect_filled(avatar, 7.0, Color32::from_rgb(hue.0, hue.1, hue.2));
    let letter = session
        .title
        .chars()
        .next()
        .unwrap_or('A')
        .to_uppercase()
        .to_string();
    ui.painter().text(
        avatar.center(),
        egui::Align2::CENTER_CENTER,
        letter,
        theme::semibold(11.0),
        Color32::from_rgb(22, 26, 25),
    );
    let text_left = avatar.right() + 10.0;
    let title = truncate(&session.title, 30);
    ui.painter().text(
        egui::pos2(text_left, rect.top() + 11.0),
        egui::Align2::LEFT_TOP,
        title,
        theme::medium(12.5),
        if selected {
            theme::TEXT
        } else {
            theme::SECONDARY
        },
    );
    let workspace = truncate(&session.detail, 24);
    let subtitle = if session.time.is_empty() {
        workspace
    } else {
        format!("{workspace}  ·  {}", session.time)
    };
    ui.painter().text(
        egui::pos2(text_left, rect.top() + 29.0),
        egui::Align2::LEFT_TOP,
        subtitle,
        theme::ui(10.5),
        theme::FAINT,
    );
    let (status_color, _) = match session.status {
        SessionStatus::Running => (theme::BLUE, "running"),
        SessionStatus::Attention => (theme::AMBER, "attention"),
        SessionStatus::Error => (theme::RED, "error"),
        SessionStatus::Offline => (theme::FAINT, "offline"),
        SessionStatus::Idle => (theme::GREEN, "idle"),
    };
    let dot_center = egui::pos2(rect.right() - 14.0, rect.center().y);
    if session.status == SessionStatus::Attention {
        ui.painter()
            .circle_stroke(dot_center, 5.0, Stroke::new(1.0, theme::AMBER));
    }
    ui.painter().circle_filled(dot_center, 3.0, status_color);
    if response.hovered() {
        ui.ctx().set_cursor_icon(egui::CursorIcon::PointingHand);
    }
    let clicked = response.clicked();
    response.on_hover_text(format!(
        "{}\n{}\n{}",
        session.title,
        session.workspace,
        match session.status {
            SessionStatus::Attention => "Waiting for your decision",
            SessionStatus::Running => "Running",
            SessionStatus::Error => "Last run failed",
            SessionStatus::Offline => "Offline",
            SessionStatus::Idle => "Idle",
        }
    ));
    clicked
}

fn provider_card(ui: &mut egui::Ui, provider: &ProviderSummary) {
    egui::Frame::new()
        .fill(theme::SURFACE)
        .stroke(Stroke::new(1.0, theme::LINE))
        .corner_radius(10)
        .inner_margin(Margin::symmetric(14, 12))
        .show(ui, |ui| {
            ui.set_width(ui.available_width());
            ui.horizontal(|ui| {
                let (rect, _) = ui.allocate_exact_size(Vec2::splat(8.0), Sense::hover());
                ui.painter().circle_filled(
                    rect.center(),
                    3.5,
                    if provider.available {
                        theme::GREEN
                    } else {
                        theme::FAINT
                    },
                );
                ui.label(
                    RichText::new(&provider.name)
                        .font(theme::semibold(12.5))
                        .color(theme::TEXT),
                );
                if provider.default {
                    widgets::pill(ui, "primary", theme::BLUE);
                }
                ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                    ui.label(
                        RichText::new(if provider.available {
                            "available"
                        } else {
                            "unavailable"
                        })
                        .font(theme::ui(10.5))
                        .color(theme::FAINT),
                    );
                });
            });
            if let Some(message) = &provider.message {
                ui.add_space(4.0);
                ui.add(
                    egui::Label::new(
                        RichText::new(message)
                            .font(theme::ui(10.5))
                            .color(theme::MUTED),
                    )
                    .wrap_mode(TextWrapMode::Wrap),
                );
            }
            ui.add_space(4.0);
            ui.label(
                RichText::new(format!(
                    "{} models · {} modes",
                    provider.models.len(),
                    provider.modes.len()
                ))
                .font(theme::ui(10.5))
                .color(theme::FAINT),
            );
        });
}

fn settings_heading(ui: &mut egui::Ui, text: &str) {
    ui.label(
        RichText::new(text)
            .font(theme::semibold(13.5))
            .color(theme::TEXT),
    );
    ui.add_space(8.0);
}

fn provider_label(view: &ViewModel, id: &str) -> String {
    view.provider(id)
        .map_or_else(|| id.to_owned(), |provider| provider.name.clone())
}

fn paint_logo(ui: &egui::Ui, rect: egui::Rect) {
    let painter = ui.painter();
    let center = rect.center();
    painter.circle_filled(center, rect.width() * 0.5, theme::SURFACE);
    painter.add(egui::Shape::convex_polygon(
        vec![
            center + Vec2::new(0.0, -rect.width() * 0.24),
            center + Vec2::new(rect.width() * 0.24, 0.0),
            center + Vec2::new(0.0, rect.width() * 0.24),
            center + Vec2::new(-rect.width() * 0.24, 0.0),
        ],
        theme::BLUE,
        Stroke::NONE,
    ));
    painter.circle_filled(
        center + Vec2::new(rect.width() * 0.24, -rect.width() * 0.24),
        rect.width() * 0.075,
        theme::ORANGE,
    );
}

fn compact_path(path: &str) -> String {
    if path.is_empty() {
        return String::new();
    }
    if let Some(home) = directories::BaseDirs::new().map(|dirs| dirs.home_dir().to_path_buf())
        && let Ok(rest) = std::path::Path::new(path).strip_prefix(&home)
    {
        return format!("~/{}", rest.display());
    }
    path.to_owned()
}

fn truncate(text: &str, limit: usize) -> String {
    let count = text.chars().count();
    if count <= limit {
        return text.to_owned();
    }
    let mut short: String = text.chars().take(limit.saturating_sub(1)).collect();
    short.push('…');
    short
}

fn hue_for(id: &str) -> (u8, u8, u8) {
    let mut hash: u32 = 216_613;
    for byte in id.bytes() {
        hash = hash.wrapping_mul(31).wrapping_add(u32::from(byte));
    }
    let palette = [
        (126, 158, 210),
        (150, 178, 140),
        (206, 166, 118),
        (178, 154, 204),
        (120, 176, 176),
        (206, 150, 150),
    ];
    palette[(hash as usize) % palette.len()]
}

fn ui_copy(desktop: &mut Desktop, text: String) {
    desktop.notify(format!("Copied: {}", truncate(&text, 60)));
    // Clipboard access needs the context; the next frame performs the copy.
    desktop.pending_clipboard = Some(text);
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn truncation_keeps_byte_boundaries() {
        assert_eq!(truncate("hello", 10), "hello");
        assert_eq!(truncate("hello world", 6), "hello…");
    }

    #[test]
    fn hue_is_deterministic_and_in_palette() {
        assert_eq!(hue_for("abc"), hue_for("abc"));
        let (r, g, b) = hue_for("abc");
        assert!(r > 0 && g > 0 && b > 0);
    }

    #[test]
    fn preview_shell_renders_headlessly_at_responsive_sizes() {
        for (width, height) in [
            (1600.0, 900.0),
            (1280.0, 800.0),
            (900.0, 650.0),
            (720.0, 520.0),
        ] {
            let context = egui::Context::default();
            let mut desktop = Desktop::new(context.clone(), true);
            desktop.selected = Some("demo-1".into());
            desktop
                .backend
                .view
                .pending
                .insert("perm-1".into(), PendingState::Idle);
            let mut frame = eframe::Frame::_new_kittest();
            let mut input = egui::RawInput::default();
            input.screen_rect = Some(egui::Rect::from_min_size(
                egui::Pos2::ZERO,
                egui::vec2(width, height),
            ));

            let output = context.run_ui(input, |ui| {
                eframe::App::ui(&mut desktop, ui, &mut frame);
            });
            assert!(
                !output.shapes.is_empty(),
                "no UI shapes at {width}×{height}"
            );
            output.drop_without_applying_deltas();
        }
    }
}
