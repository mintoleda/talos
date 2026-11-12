//! Bounded, asynchronous local-daemon bridge. Reconnect replays durable history
//! from per-session delivery cursors; RPC timeouts always retire the connection.

#[cfg(test)]
use crate::state::{Entry, ToolState};
use crate::state::{
    PendingState, ProviderSummary, Session, SessionStatus, Usage, ViewModel, Workspace,
    parse_session,
};
/// Frontend-neutral wake handle. The GPUI shell supplies its native notifier.
#[derive(Clone)]
struct UiWake(std::sync::Arc<dyn Fn() + Send + Sync>);

impl UiWake {
    fn request_repaint(&self) {
        (self.0)();
    }
}
use serde_json::Value;
use std::{
    collections::{BTreeMap, HashSet},
    os::unix::fs::FileTypeExt,
    path::{Path, PathBuf},
    process::{Command as Process, Stdio},
    time::Duration,
};
use talos_ipc::{Command, EventNotification, ServerFrame, WireEvent};
use tokio::sync::mpsc;
const BATCH: u32 = 64;

#[derive(Clone, Debug, PartialEq)]
pub enum Action {
    Load(String),
    LoadTimeline {
        session: String,
        after: u64,
    },
    Prompt {
        session: String,
        text: String,
        images: Vec<String>,
        key: String,
    },
    Cancel(String),
    Decision {
        id: String,
        session: String,
        allow: bool,
    },
    CreateAgent {
        workspace: String,
        provider: Option<String>,
        title: Option<String>,
    },
    CreateWorkspace {
        name: String,
        path: String,
    },
    SetModel {
        session: String,
        model: String,
    },
    SetConfig {
        session: String,
        option: String,
        value: String,
    },
    Rename {
        session: String,
        title: String,
    },
    Archive(String),
    Rpc(Command),
    ValidateImages {
        session: String,
        paths: Vec<String>,
    },
    Refresh,
    Shutdown,
}
impl Action {
    fn command(&self) -> Option<Command> {
        Some(match self {
            Self::ValidateImages { .. } => return None,
            Self::Load(session) => Command::LoadSession {
                session_id: session.clone(),
            },
            Self::LoadTimeline { session, after } => Command::ReplaySession {
                session_id: session.clone(),
                after: *after,
                limit: Some(128),
            },
            Self::Prompt {
                session,
                text,
                images,
                key,
            } if !images.is_empty() => Command::PromptWithImages {
                session_id: session.clone(),
                text: text.clone(),
                image_paths: images.clone(),
                request_key: Some(key.clone()),
            },
            Self::Prompt {
                session, text, key, ..
            } => Command::Prompt {
                session_id: session.clone(),
                text: text.clone(),
                request_key: Some(key.clone()),
            },
            Self::Cancel(session) => Command::Cancel {
                session_id: session.clone(),
            },
            Self::Decision { id, allow, .. } => Command::PermissionDecision {
                permission_id: id.clone(),
                decision: if *allow {
                    talos_ipc::PermissionDecision::Allow
                } else {
                    talos_ipc::PermissionDecision::Deny
                },
            },
            Self::CreateAgent {
                workspace,
                provider,
                title,
            } => Command::CreateAgent {
                workspace_id: workspace.clone(),
                provider_id: provider.clone(),
                title: title.clone(),
            },
            Self::CreateWorkspace { name, path } => Command::CreateWorkspace {
                name: name.clone(),
                path: path.clone(),
            },
            Self::SetModel { session, model } => Command::SetModel {
                session_id: session.clone(),
                model_id: model.clone(),
            },
            Self::SetConfig {
                session,
                option,
                value,
            } => Command::SetConfigOption {
                session_id: session.clone(),
                option_id: option.clone(),
                value: value.clone(),
            },
            Self::Rename { session, title } => Command::RenameSession {
                session_id: session.clone(),
                title: title.clone(),
            },
            Self::Archive(session) => Command::ArchiveSession {
                session_id: session.clone(),
            },
            Self::Rpc(command) => command.clone(),
            Self::Refresh => Command::Snapshot,
            Self::Shutdown => Command::Shutdown,
        })
    }
}
enum Update {
    Connected(Value),
    Snapshot(Value),
    Result {
        action: Action,
        result: Result<Value, String>,
    },
    Events(Vec<EventNotification>),
    Offline(String),
    Notice(String),
}

pub struct Backend {
    pub view: ViewModel,
    sender: mpsc::Sender<Action>,
    receiver: mpsc::Receiver<Update>,
    next_key: u64,
    key_namespace: String,
    pub validated_images: Vec<(String, Value)>,
    pub failed_prompt: Option<(String, String)>,
    pub retry_prompt: Option<Action>,
    pub endpoint: String,
    pub created_session: Option<String>,
    pub agent_creation_failed: bool,
    pub created_workspace: Option<String>,
    pub generation: u64,
    pub loading: HashSet<String>,
    pub ready: HashSet<String>,
    /// Sessions whose model change has not yet been acknowledged.
    pub setting_models: HashSet<String>,
    pub load_errors: std::collections::HashMap<String, String>,
    pub model_catalog_loading: HashSet<String>,
    pub model_catalog_errors: std::collections::HashMap<String, String>,
    pub busy: usize,
    pub completed: Vec<Action>,
    pub connection_error: Option<String>,
}
impl Backend {
    pub fn new(wake: impl Fn() + Send + Sync + 'static, preview: bool) -> Self {
        let ctx = UiWake(std::sync::Arc::new(wake));
        let (sender, actions) = mpsc::channel(BATCH as usize);
        let (updates, receiver) = mpsc::channel(512);
        if !preview {
            let failure = updates.clone();
            let paint = ctx.clone();
            if let Err(error) = std::thread::Builder::new()
                .name("talos-ui-backend".into())
                .spawn(move || {
                    match tokio::runtime::Builder::new_current_thread()
                        .enable_all()
                        .build()
                    {
                        Ok(runtime) => runtime.block_on(worker(ctx, actions, updates)),
                        Err(error) => {
                            let _ = failure.blocking_send(Update::Offline(format!(
                                "Backend runtime: {error}"
                            )));
                            paint.request_repaint();
                        }
                    }
                })
            {
                return Self::disconnected(
                    sender,
                    receiver,
                    format!("Cannot start backend: {error}"),
                );
            }
        }
        Self::disconnected(sender, receiver, "Connecting to local daemon…".into())
    }
    fn disconnected(
        sender: mpsc::Sender<Action>,
        receiver: mpsc::Receiver<Update>,
        status: String,
    ) -> Self {
        let mut view = ViewModel::default();
        view.status = status;
        Self {
            view,
            sender,
            receiver,
            next_key: 0,
            key_namespace: format!(
                "{}-{}",
                std::process::id(),
                std::time::SystemTime::now()
                    .duration_since(std::time::UNIX_EPOCH)
                    .unwrap_or_default()
                    .as_nanos()
            ),
            validated_images: Vec::new(),
            failed_prompt: None,
            retry_prompt: None,
            endpoint: socket_path().display().to_string(),
            created_session: None,
            agent_creation_failed: false,
            created_workspace: None,
            generation: 0,
            loading: HashSet::new(),
            ready: HashSet::new(),
            setting_models: HashSet::new(),
            load_errors: std::collections::HashMap::new(),
            model_catalog_loading: HashSet::new(),
            model_catalog_errors: std::collections::HashMap::new(),
            busy: 0,
            completed: Vec::new(),
            connection_error: None,
        }
    }
    #[allow(clippy::needless_pass_by_value)]
    pub fn submit(&mut self, action: Action) -> bool {
        if !self.view.online && !matches!(action, Action::Refresh | Action::ValidateImages { .. }) {
            self.view.error = Some(
                "Daemon offline. Reconnect before making changes; drafts are retained.".into(),
            );
            return false;
        }
        if matches!(&action, Action::Prompt { session, .. } if self.setting_models.contains(session))
        {
            self.view.error = Some("Wait for the model change to finish before sending.".into());
            return false;
        }
        if self.sender.try_send(action.clone()).is_err() {
            self.view.error =
                Some("Backend queue unavailable or full. Your edits are retained.".into());
            return false;
        }
        match &action {
            Action::CreateAgent { .. } => self.agent_creation_failed = false,
            Action::SetModel { session, .. } => {
                self.setting_models.insert(session.clone());
            }
            Action::Load(session) => {
                self.loading.insert(session.clone());
                self.ready.remove(session);
                self.load_errors.remove(session);
            }
            Action::LoadTimeline { session, .. } => {
                self.loading.insert(session.clone());
            }
            Action::Rpc(Command::ListModels { provider_id }) => {
                self.model_catalog_loading.insert(provider_id.clone());
                self.model_catalog_errors.remove(provider_id);
            }
            _ => {}
        }
        self.busy += 1;
        true
    }
    #[allow(clippy::needless_pass_by_value)]
    pub fn send(&mut self, session: &str, text: String, images: Vec<String>) -> bool {
        self.next_key += 1;
        let action = Action::Prompt {
            session: session.into(),
            text: text.clone(),
            images,
            key: format!("desktop-{}-{}", self.key_namespace, self.next_key),
        };
        if self.submit(action) {
            self.view.echo_user_prompt(session, &text);
            true
        } else {
            false
        }
    }
    pub fn drain(&mut self) {
        for _ in 0..BATCH {
            let Ok(update) = self.receiver.try_recv() else {
                break;
            };
            self.apply(update);
        }
    }
    fn apply(&mut self, update: Update) {
        match update {
            Update::Connected(snapshot) => {
                self.generation += 1;
                self.ready.clear();
                self.loading.clear();
                self.load_errors.clear();
                self.connection_error = None;
                apply_snapshot(&mut self.view, &snapshot, true);
            }
            Update::Snapshot(snapshot) => apply_snapshot(&mut self.view, &snapshot, true),
            Update::Offline(reason) => {
                self.view.online = false;
                self.view.status = "Disconnected · reconnecting".into();
                self.connection_error = Some(reason);
                self.ready.clear();
                for session in &mut self.view.sessions {
                    if session.status != SessionStatus::Attention {
                        session.status = SessionStatus::Offline;
                    }
                }
            }
            Update::Notice(message) => self.view.notice = Some(message),
            Update::Events(events) => {
                for event in &events {
                    self.view.apply_event(event);
                }
            }
            Update::Result { action, result } => {
                self.busy = self.busy.saturating_sub(1);
                match result {
                    Ok(value) => {
                        self.completed.push(action.clone());
                        self.apply_success(action, value);
                    }
                    Err(error) => self.apply_failure(action, error),
                }
            }
        }
    }
    #[allow(clippy::too_many_lines)]
    fn apply_success(&mut self, action: Action, value: Value) {
        match action {
            Action::ValidateImages { session, .. } => self.validated_images.push((session, value)),
            Action::Load(session) => {
                self.loading.remove(&session);
                self.load_errors.remove(&session);
                self.submit(Action::LoadTimeline { session, after: 0 });
            }
            Action::LoadTimeline { session, after } => {
                let events = value["events"].as_array();
                for event in events.into_iter().flatten().filter_map(notification) {
                    self.view.apply_event(&event);
                }
                let next = value["next_cursor"]
                    .as_u64()
                    .or_else(|| {
                        events
                            .and_then(|events| events.last())
                            .and_then(|event| event["sequence"].as_u64())
                    })
                    .unwrap_or(after);
                let has_more = events.is_some_and(|events| events.len() == 128);
                if has_more && next > after {
                    self.submit(Action::LoadTimeline {
                        session,
                        after: next,
                    });
                } else {
                    self.loading.remove(&session);
                    self.load_errors.remove(&session);
                    self.ready.insert(session.clone());
                    self.submit(Action::Rpc(Command::UsageSummary {
                        session_id: Some(session),
                    }));
                }
            }
            Action::Prompt { .. } => {
                // Acceptance can arrive after a terminal event on the event connection.
                // send() marks optimistic activity; durable RunState events own lifecycle.
                self.retry_prompt = None;
            }
            Action::Cancel(_) => {} // Busy until the durable terminal event arrives.
            Action::Decision { id, session, .. } => {
                self.view.pending.remove(&id);
                if let Some(entries) = self.view.entries.get_mut(&session) {
                    entries.retain(|entry| !matches!(&entry.kind, crate::state::EntryKind::Permission { id: known, .. } if known == &id));
                }
                self.view.refresh_attention(&session);
            }
            Action::CreateAgent { workspace, .. } => {
                if let Some(mut session) = value.get("session").and_then(parse_session) {
                    session.title = value["agent"]["title"]
                        .as_str()
                        .unwrap_or("New agent")
                        .into();
                    session.provider = value["agent"]["provider"]
                        .as_str()
                        .unwrap_or("talos")
                        .into();
                    session.workspace.clone_from(&workspace);
                    session.cwd = self
                        .view
                        .workspaces
                        .iter()
                        .find(|item| item.id == workspace)
                        .map(|item| item.path.clone())
                        .unwrap_or_default();
                    self.ready.insert(session.id.clone());
                    self.created_session = Some(session.id.clone());
                    self.view.sessions.insert(0, session);
                }
            }
            Action::CreateWorkspace { .. } => {
                self.created_workspace = value["id"].as_str().map(str::to_owned);
            }
            Action::SetModel { session, model } => {
                self.setting_models.remove(&session);
                if let Some(item) = self.view.session_mut(&session) {
                    item.model = Some(model);
                }
            }
            Action::SetConfig {
                session,
                option,
                value,
            } => {
                if let Some(options) = self
                    .view
                    .config_options
                    .get_mut(&session)
                    .and_then(Value::as_array_mut)
                    && let Some(config) = options.iter_mut().find(|item| item["id"] == option)
                {
                    config["currentValue"] = Value::String(value);
                }
            }
            Action::Rename { session, title } => {
                if let Some(item) = self.view.session_mut(&session) {
                    item.title = title;
                }
            }
            Action::Archive(session) => {
                if let Some(item) = self.view.session_mut(&session) {
                    item.archived = true;
                }
            }
            Action::Rpc(command) => match command {
                Command::ListProjects => {
                    self.view.projects = value.as_array().cloned().unwrap_or_default();
                }
                Command::ListModels { provider_id } => {
                    self.model_catalog_loading.remove(&provider_id);
                    self.model_catalog_errors.remove(&provider_id);
                    if let Some(provider) = self
                        .view
                        .providers
                        .iter_mut()
                        .find(|provider| provider.id == provider_id)
                    {
                        provider.models =
                            parse_model_summaries(value.as_array().map(Vec::as_slice));
                    }
                }
                Command::SearchTimeline { .. } => self.view.search = value,
                Command::ListTerminals { .. } => self.view.terminals = value,
                Command::CaptureTerminal { .. } => self.view.terminal_capture = value,
                Command::CreateTerminal { workspace_id, .. }
                | Command::KillTerminal { workspace_id, .. } => {
                    self.submit(Action::Rpc(Command::ListTerminals { workspace_id }));
                }
                Command::TerminalInput {
                    workspace_id,
                    terminal_id,
                    ..
                } => {
                    self.submit(Action::Rpc(Command::CaptureTerminal {
                        workspace_id,
                        terminal_id,
                        limit: Some(65_536),
                    }));
                }
                Command::CreateProject { .. }
                | Command::RenameProject { .. }
                | Command::DeleteProject { .. } => {
                    self.submit(Action::Rpc(Command::ListProjects));
                }
                Command::SetWorkspaceProject { .. }
                | Command::CreateManagedWorktree { .. }
                | Command::DeleteManagedWorktree { .. }
                | Command::RenameWorkspace { .. }
                | Command::ArchiveWorkspace { .. }
                | Command::RestoreWorkspace { .. } => {
                    self.submit(Action::Refresh);
                }
                Command::ProviderDiagnostics { .. } => self.view.diagnostics = value,
                Command::UsageSummary { session_id } => {
                    if let Some(session) = session_id {
                        self.view.usage.insert(session, parse_usage(&value));
                    } else {
                        self.view.usage_summary = value;
                    }
                }
                Command::SetMode {
                    session_id,
                    mode_id,
                } => {
                    if let Some(item) = self.view.session_mut(&session_id) {
                        item.mode = Some(mode_id);
                    }
                }
                _ => {}
            },
            Action::Refresh => apply_snapshot(&mut self.view, &value, true),
            Action::Shutdown => {
                self.view.online = false;
                self.view.status = "Daemon stopped · Reconnect to start".into();
            }
        }
    }
    #[allow(clippy::needless_pass_by_value)]
    fn apply_failure(&mut self, action: Action, error: String) {
        match &action {
            Action::CreateAgent { .. } => self.agent_creation_failed = true,
            Action::SetModel { session, .. } => {
                self.setting_models.remove(session);
            }
            Action::Load(session) | Action::LoadTimeline { session, .. } => {
                self.loading.remove(session);
                self.ready.remove(session);
                self.load_errors.insert(session.clone(), error.clone());
            }
            Action::Rpc(Command::ListModels { provider_id }) => {
                self.model_catalog_loading.remove(provider_id);
                self.model_catalog_errors
                    .insert(provider_id.clone(), error.clone());
            }
            Action::Prompt { session, text, .. } => {
                self.view.run.insert(session.clone(), false);
                self.failed_prompt = Some((session.clone(), text.clone()));
                self.retry_prompt = Some(action.clone());
                if let Some(entries) = self.view.entries.get_mut(session) {
                    entries.retain(|entry| !entry.id.starts_with("local-"));
                }
            }
            Action::Decision { id, .. } => {
                self.view.pending.insert(id.clone(), PendingState::Failed);
                self.submit(Action::Refresh);
            }
            _ => {}
        }
        self.view.error = Some(error);
    }
}

fn apply_snapshot(view: &mut ViewModel, snapshot: &Value, _preserve_entries: bool) {
    view.online = true;
    view.status = snapshot.get("home").and_then(Value::as_str).map_or_else(
        || "Connected · talosd".into(),
        |home| format!("Connected · {}", compact_path(home)),
    );
    view.workspaces = snapshot["workspaces"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(|item| {
            Some(Workspace {
                id: item["id"].as_str()?.into(),
                name: item["name"].as_str()?.into(),
                path: item["path"].as_str().unwrap_or_default().into(),
                project: item["project_id"].as_str().map(str::to_owned),
                archived: item["lifecycle"] == "Archived",
                managed: item["kind"] == "Worktree"
                    && item["metadata"]["managedWorktree"]["owner"] == "talosd",
            })
        })
        .collect();
    view.providers = parse_providers(snapshot.get("providers"));
    view.sessions = parse_sessions(
        snapshot.get("sessions"),
        snapshot.get("agents"),
        &view.workspaces,
    );
    view.usage_summary = snapshot["usage"].clone();
    let pending: HashSet<String> = snapshot["pending_permissions"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(|p| p["id"].as_str().map(str::to_owned))
        .collect();
    view.pending.retain(|id, _| pending.contains(id));
    for entries in view.entries.values_mut() {
        entries.retain(|entry| !matches!(&entry.kind, crate::state::EntryKind::Permission { id, .. } if !pending.contains(id)));
    }
    for request in snapshot["pending_permissions"]
        .as_array()
        .into_iter()
        .flatten()
    {
        if let (Some(session), Some(id)) = (request["session_id"].as_str(), request["id"].as_str())
        {
            view.pending.entry(id.into()).or_default();
            view.apply_pending(request);
            view.refresh_attention(session);
        }
    }
    for session in &view.sessions {
        // Snapshots and event updates travel on separate connections. A snapshot
        // may predate an already-applied terminal event, so it is only a fallback
        // for sessions without replay/live/optimistic lifecycle state.
        view.run.entry(session.id.clone()).or_insert_with(|| {
            matches!(
                session.status,
                SessionStatus::Running | SessionStatus::Attention
            )
        });
    }
}
fn parse_providers(value: Option<&Value>) -> Vec<ProviderSummary> {
    value
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(|item| {
            let id = item.get("id")?.as_str()?.to_owned();
            let name = item
                .get("display_name")
                .and_then(Value::as_str)
                .unwrap_or(&id)
                .to_owned();
            // Snapshot model_options uses ProviderModel { id, label, ... },
            // while the legacy `models` field contains ModelRef { provider,
            // model_id, ... }. Accept both wire shapes; otherwise Talos' ACP
            // catalogue appears empty even though session/new advertised it.
            let detailed_models = item
                .get("model_options")
                .and_then(Value::as_array)
                .filter(|models| !models.is_empty());
            let raw_models =
                detailed_models.or_else(|| item.get("models").and_then(Value::as_array));
            let models = parse_model_summaries(raw_models.map(Vec::as_slice));
            let mode_options = item
                .get("modes")
                .and_then(Value::as_array)
                .into_iter()
                .flatten()
                .filter_map(|mode| {
                    Some((
                        mode.get("id")?.as_str()?.to_owned(),
                        mode.get("label")
                            .and_then(Value::as_str)
                            .unwrap_or_default()
                            .to_owned(),
                    ))
                })
                .collect();
            Some(ProviderSummary {
                default: id == "talos",
                available: item
                    .get("available")
                    .and_then(Value::as_bool)
                    .unwrap_or(true),
                message: item
                    .get("message")
                    .and_then(Value::as_str)
                    .map(str::to_owned),
                id,
                name,
                models,
                modes: mode_options,
                capabilities: item["capabilities"].clone(),
            })
        })
        .collect()
}

fn parse_model_summaries(models: Option<&[Value]>) -> Vec<crate::state::ModelSummary> {
    models
        .into_iter()
        .flatten()
        .filter_map(|model| {
            let id = model
                .get("id")
                .or_else(|| model.get("model_id"))?
                .as_str()?
                .to_owned();
            Some(crate::state::ModelSummary {
                label: model
                    .get("label")
                    .or_else(|| model.get("name"))
                    .and_then(Value::as_str)
                    .unwrap_or(&id)
                    .to_owned(),
                id,
                default: model
                    .get("is_default")
                    .and_then(Value::as_bool)
                    .unwrap_or(false),
            })
        })
        .collect()
}

fn parse_sessions(
    sessions: Option<&Value>,

    agents: Option<&Value>,
    workspaces: &[Workspace],
) -> Vec<Session> {
    sessions
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(|session| {
            let mut parsed = parse_session(session)?;
            let agent = agents
                .and_then(Value::as_array)
                .into_iter()
                .flatten()
                .find(|agent| agent.get("id") == session.get("agent_id"));
            if let Some(agent) = agent {
                agent
                    .get("title")
                    .and_then(Value::as_str)
                    .unwrap_or("Agent")
                    .clone_into(&mut parsed.title);
                agent
                    .get("provider")
                    .and_then(Value::as_str)
                    .unwrap_or("talos")
                    .clone_into(&mut parsed.provider);
                agent
                    .get("workspace_id")
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .clone_into(&mut parsed.workspace);
                parsed.detail = parsed.provider.clone();
                parsed.model = agent
                    .get("model")
                    .and_then(|model| {
                        model
                            .get("model_id")
                            .and_then(Value::as_str)
                            .or_else(|| model.as_str())
                    })
                    .map(str::to_owned);
                parsed.mode = agent.get("mode").and_then(Value::as_str).map(str::to_owned);
                parsed.cwd = workspaces
                    .iter()
                    .find(|workspace| workspace.id == parsed.workspace)
                    .map(|workspace| workspace.path.clone())
                    .unwrap_or_default();
                parsed.status = match agent["lifecycle"].as_str() {
                    Some("Busy" | "Stopping") => SessionStatus::Running,
                    Some("AttentionRequired") => SessionStatus::Attention,
                    Some("Failed") => SessionStatus::Error,
                    _ => SessionStatus::Idle,
                };
                parsed.archived = parsed.lifecycle == "Archived"
                    || agent
                        .get("lifecycle")
                        .and_then(Value::as_str)
                        .is_some_and(|lifecycle| lifecycle == "Archived");
            }
            Some(parsed)
        })
        .collect()
}

fn parse_usage(value: &Value) -> Usage {
    Usage {
        input_tokens: value.get("input_tokens").and_then(Value::as_u64),
        output_tokens: value.get("output_tokens").and_then(Value::as_u64),
        cached_read_tokens: value.get("cached_read_tokens").and_then(Value::as_u64),
        context_used: value.get("context_used").and_then(Value::as_u64),
        context_size: value.get("context_size").and_then(Value::as_u64),
        cost_micros: value.get("cost_micros").and_then(Value::as_u64),
    }
}

fn compact_path(path: &str) -> String {
    if let Some(home) = directories::BaseDirs::new().map(|dirs| dirs.home_dir().to_path_buf())
        && let Ok(rest) = Path::new(path).strip_prefix(&home)
    {
        return format!("~/{}", rest.display());
    }
    path.to_owned()
}

/// Convert a `SessionSnapshot` payload into timeline entries.
#[cfg(test)]
fn entries_from_snapshot(value: &Value) -> Vec<Entry> {
    let session_id = value
        .get("session")
        .and_then(|session| session.get("id"))
        .and_then(Value::as_str)
        .or_else(|| value.get("session_id").and_then(Value::as_str))
        .unwrap_or_default()
        .to_owned();
    let mut view = ViewModel::default();
    for event in value
        .get("events")
        .or_else(|| value.get("timeline"))
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
    {
        let cursor = event.get("sequence").and_then(Value::as_u64).unwrap_or(0);
        let event_id = event
            .get("id")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned();
        let kind = event.get("kind").cloned().unwrap_or(Value::Null);
        let payload = serde_json::json!({
            "id": event_id,
            "session_id": event.get("session_id").cloned().unwrap_or(Value::String(session_id.clone())),
            "run_id": event.get("run_id").cloned().unwrap_or(Value::Null),
            "sequence": cursor,
            "timestamp": event.get("timestamp").cloned().unwrap_or(Value::Null),
            "kind": kind,
        });
        view.apply_event(&EventNotification {
            version: talos_ipc::PROTOCOL_VERSION,
            cursor,
            event_id: event
                .get("id")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
            event: WireEvent {
                kind: "timeline".into(),
                payload,
            },
        });
    }
    view.entries.remove(&session_id).unwrap_or_default()
}

async fn publish(
    ctx: &UiWake,
    updates: &mpsc::Sender<Update>,
    update: Update,
) -> Result<(), String> {
    updates
        .send(update)
        .await
        .map_err(|_| "Desktop closed".to_owned())?;
    ctx.request_repaint();
    Ok(())
}

type LocalClient = talos_ipc::Client<tokio::net::UnixStream>;
async fn rpc(client: &mut LocalClient, command: Command) -> Result<Value, String> {
    tokio::time::timeout(Duration::from_secs(45), client.call(command))
        .await
        .map_err(|_| {
            "Request timed out; outcome may be unknown. Reconnecting before further requests."
                .to_owned()
        })?
        .map_err(|error| error.to_string())
}
async fn connect() -> Result<LocalClient, String> {
    let mut client = tokio::time::timeout(
        Duration::from_secs(5),
        talos_ipc::unix::connect(&socket_path(), auth_token()),
    )
    .await
    .map_err(|_| "Daemon connection timed out".to_owned())?
    .map_err(|e| e.to_string())?;
    tokio::time::timeout(Duration::from_secs(5), client.negotiate())
        .await
        .map_err(|_| "Daemon handshake timed out".to_owned())?
        .map_err(|e| e.to_string())?;
    Ok(client)
}
async fn worker(ctx: UiWake, mut actions: mpsc::Receiver<Action>, updates: mpsc::Sender<Update>) {
    let mut cursors = BTreeMap::new();
    loop {
        ensure_daemon(&updates, &ctx).await;
        let result = async {
            let mut commands = connect().await?;
            let mut stream = connect().await?;
            tokio::select! {
                result = command_loop(&ctx, &mut commands, &mut actions, &updates) => result,
                result = event_loop(&ctx, &mut stream, &updates, &mut cursors) => result.map(|()| false),
            }
        }.await;
        match result {
            Ok(true) => {
                // An explicit stop must stay stopped. Refresh is the explicit
                // start/reconnect action; closing the window just drops sender.
                loop {
                    match actions.recv().await {
                        None => return,
                        Some(Action::Refresh) => {
                            let _ = publish(
                                &ctx,
                                &updates,
                                Update::Result {
                                    action: Action::Refresh,
                                    result: Err("Starting daemon…".into()),
                                },
                            )
                            .await;
                            break;
                        }
                        Some(action) => {
                            let _ = publish(
                                &ctx,
                                &updates,
                                Update::Result {
                                    action,
                                    result: Err("Daemon is stopped".into()),
                                },
                            )
                            .await;
                        }
                    }
                }
            }
            Ok(false) => return,
            Err(error) => {
                if publish(&ctx, &updates, Update::Offline(error))
                    .await
                    .is_err()
                {
                    return;
                }
                tokio::select! {
                    () = tokio::time::sleep(Duration::from_millis(1200)) => {}
                    action = actions.recv() => match action {
                        None => return,
                        Some(action) => { let _ = publish(&ctx, &updates, Update::Result { action, result: Err("Daemon unavailable. Edits were retained.".into()) }).await; }
                    }
                }
            }
        }
    }
}
async fn command_loop(
    ctx: &UiWake,
    client: &mut LocalClient,
    actions: &mut mpsc::Receiver<Action>,
    updates: &mpsc::Sender<Update>,
) -> Result<bool, String> {
    while let Some(action) = actions.recv().await {
        let Some(command) = action.command() else {
            if let Action::ValidateImages { paths, .. } = &action {
                let paths = paths.clone();
                let result =
                    tokio::task::spawn_blocking(move || crate::attachments::validate(&paths))
                        .await
                        .map_err(|e| e.to_string())
                        .and_then(|r| r);
                publish(ctx, updates, Update::Result { action, result }).await?;
            }
            continue;
        };
        let result = rpc(client, command.clone()).await;
        let connection_failure = result
            .as_ref()
            .err()
            .filter(|error| !error.starts_with("remote error"))
            .cloned();
        let success = result.is_ok();
        publish(
            ctx,
            updates,
            Update::Result {
                action: action.clone(),
                result,
            },
        )
        .await?;
        if let Some(error) = connection_failure {
            return Err(error);
        }
        if action == Action::Shutdown && success {
            return Ok(true);
        }
        if success
            && !matches!(
                command,
                Command::Snapshot
                    | Command::CaptureTerminal { .. }
                    | Command::TerminalInput { .. }
                    | Command::ResizeTerminal { .. }
                    | Command::ListTerminals { .. }
                    | Command::SearchTimeline { .. }
                    | Command::ListProjects
                    | Command::UsageSummary { .. }
                    | Command::ProviderDiagnostics { .. }
            )
        {
            let snapshot = rpc(client, Command::Snapshot).await?;
            publish(ctx, updates, Update::Snapshot(snapshot)).await?;
        }
    }
    Ok(false)
}
async fn replay(
    ctx: &UiWake,
    client: &mut LocalClient,
    updates: &mpsc::Sender<Update>,
    cursors: &mut BTreeMap<String, u64>,
) -> Result<(), String> {
    loop {
        let page = rpc(
            client,
            Command::ReplaySessions {
                after: cursors.clone(),
                limit: Some(128),
            },
        )
        .await?;
        let events: Vec<_> = page["events"]
            .as_array()
            .into_iter()
            .flatten()
            .filter_map(notification)
            .collect();
        let mut next = cursors.clone();
        let mut delivered = Vec::new();
        for event in events {
            let Some(session) = event.event.payload["session_id"].as_str() else {
                continue;
            };
            let cursor = next.entry(session.into()).or_default();
            if event.cursor > *cursor {
                *cursor = event.cursor;
                delivered.push(event);
            }
        }
        if delivered.is_empty() && page["has_more"] == true {
            return Err("Replay did not advance; refusing to skip history".into());
        }
        publish(ctx, updates, Update::Events(delivered)).await?;
        *cursors = next;
        if page["has_more"] != true {
            return Ok(());
        }
    }
}
fn notification(event: &Value) -> Option<EventNotification> {
    Some(EventNotification {
        version: talos_ipc::PROTOCOL_VERSION,
        cursor: event["sequence"].as_u64()?,
        event_id: event["id"].as_str()?.into(),
        event: WireEvent {
            kind: "timeline".into(),
            payload: event.clone(),
        },
    })
}
async fn event_loop(
    ctx: &UiWake,
    client: &mut LocalClient,
    updates: &mpsc::Sender<Update>,
    cursors: &mut BTreeMap<String, u64>,
) -> Result<(), String> {
    replay(ctx, client, updates, cursors).await?;
    let snapshot = rpc(client, Command::Snapshot).await?;
    publish(ctx, updates, Update::Connected(snapshot)).await?;
    loop {
        let frame = client.next_frame().await.map_err(|e| e.to_string())?;
        if let ServerFrame::Event(event) = frame {
            if event.event.kind == "replay_required" {
                replay(ctx, client, updates, cursors).await?;
                let snapshot = rpc(client, Command::Snapshot).await?;
                publish(ctx, updates, Update::Snapshot(snapshot)).await?;
                continue;
            }
            let Some(session) = event.event.payload["session_id"].as_str() else {
                continue;
            };
            let known = cursors.get(session).copied().unwrap_or_default();
            if event.cursor <= known {
                continue;
            }
            if event.cursor != known + 1 {
                replay(ctx, client, updates, cursors).await?;
                continue;
            }
            let session = session.to_owned();
            let cursor = event.cursor;
            let permission = event.event.payload["kind"]
                .get("PermissionResolved")
                .is_some();
            publish(ctx, updates, Update::Events(vec![event])).await?;
            cursors.insert(session, cursor);
            if permission {
                let snapshot = rpc(client, Command::Snapshot).await?;
                publish(ctx, updates, Update::Snapshot(snapshot)).await?;
            }
        }
    }
}

fn home_dir() -> PathBuf {
    if let Some(home) = std::env::var_os("TALOSD_HOME") {
        return PathBuf::from(home);
    }
    directories::BaseDirs::new().map_or_else(
        || PathBuf::from(".talosd"),
        |dirs| dirs.home_dir().join(".talosd"),
    )
}

fn socket_path() -> PathBuf {
    std::env::var_os("TALOSD_SOCKET").map_or_else(|| home_dir().join("talosd.sock"), PathBuf::from)
}

fn auth_token() -> Option<String> {
    if let Ok(token) = std::env::var("TALOSD_AUTH_TOKEN")
        && !token.is_empty()
    {
        return Some(token);
    }
    std::fs::read_to_string(home_dir().join("auth.token"))
        .ok()
        .map(|token| token.trim().to_owned())
        .filter(|token| !token.is_empty())
}

fn daemon_binary() -> Option<PathBuf> {
    if let Some(path) = std::env::var_os("TALOSD_BIN") {
        return Some(PathBuf::from(path));
    }
    if let Ok(exe) = std::env::current_exe()
        && let Some(parent) = exe.parent()
    {
        let candidate = parent.join(if cfg!(windows) {
            "talosd.exe"
        } else {
            "talosd"
        });
        if candidate.is_file() {
            return Some(candidate);
        }
    }
    let path = std::env::var_os("PATH")?;
    std::env::split_paths(&path)
        .map(|dir| {
            dir.join(if cfg!(windows) {
                "talosd.exe"
            } else {
                "talosd"
            })
        })
        .find(|candidate| candidate.is_file())
}

async fn ensure_daemon(updates: &mpsc::Sender<Update>, ctx: &UiWake) {
    let socket = socket_path();
    if tokio::net::UnixStream::connect(&socket).await.is_ok() {
        return;
    }
    // A leftover Unix socket path is not proof that the daemon is alive. Remove
    // stale socket nodes so the daemon can bind its endpoint on startup.
    if let Ok(metadata) = std::fs::symlink_metadata(&socket)
        && metadata.file_type().is_socket()
    {
        let _ = std::fs::remove_file(&socket);
    }
    let Some(binary) = daemon_binary() else {
        let _ = updates
            .send(Update::Notice(
                "talosd was not found; start it manually or set TALOSD_BIN.".into(),
            ))
            .await;
        return;
    };
    let home = home_dir();
    let _ = std::fs::create_dir_all(&home);
    let log = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(home.join("talosd.log"))
        .ok();
    let mut command = Process::new(binary);
    command
        .arg("--home")
        .arg(&home)
        .env("TALOSD_HOME", &home)
        .stdin(Stdio::null());
    if let Some(log) = log {
        if let Ok(clone) = log.try_clone() {
            command.stdout(Stdio::from(clone));
        }
        command.stderr(Stdio::from(log));
    } else {
        command.stdout(Stdio::null()).stderr(Stdio::null());
    }
    if let Err(error) = command.spawn() {
        let _ = updates
            .send(Update::Notice(format!("Could not start talosd: {error}")))
            .await;
        return;
    }
    let _ = updates
        .send(Update::Notice("Starting local daemon…".into()))
        .await;
    ctx.request_repaint();
    for _ in 0..40 {
        tokio::time::sleep(Duration::from_millis(100)).await;
        if socket.exists() {
            return;
        }
    }
}

/// Group timeline entries for rendering: adjacent completed tools collapse into
/// one activity block to keep the reading column calm.
#[must_use]
#[cfg(test)]
pub fn group_tools(entries: &[Entry]) -> Vec<Vec<&Entry>> {
    let mut groups: Vec<Vec<&Entry>> = Vec::new();
    for entry in entries {
        let is_quiet_tool = matches!(
            &entry.kind,
            crate::state::EntryKind::Tool {
                state: ToolState::Completed,
                ..
            }
        );
        if is_quiet_tool
            && let Some(last) = groups.last_mut()
            && matches!(
                last.first().map(|entry| &entry.kind),
                Some(crate::state::EntryKind::Tool {
                    state: ToolState::Completed,
                    ..
                })
            )
        {
            last.push(entry);
            continue;
        }
        groups.push(vec![entry]);
    }
    groups
}

/// Formats a token count compactly (`1.2k`, `18.4k`, `1.1M`).
#[must_use]
#[allow(clippy::cast_precision_loss)]
pub fn format_tokens(tokens: u64) -> String {
    if tokens >= 1_000_000 {
        format!("{:.1}M", tokens as f64 / 1_000_000.0)
    } else if tokens >= 1_000 {
        format!("{:.1}k", tokens as f64 / 1_000.0)
    } else {
        tokens.to_string()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::state::EntryKind;

    fn isolated_backend() -> (Backend, mpsc::Receiver<Action>) {
        let (sender, actions) = mpsc::channel(16);
        let (_updates, receiver) = mpsc::channel(16);
        let mut backend = Backend::disconnected(sender, receiver, "Test".into());
        backend.view.online = true;
        (backend, actions)
    }

    #[test]
    fn model_change_blocks_first_prompt_until_acknowledged() {
        let (mut backend, mut actions) = isolated_backend();
        let change = Action::SetModel {
            session: "s1".into(),
            model: "vendor/chosen".into(),
        };
        assert!(backend.submit(change.clone()));
        assert!(matches!(
            actions.try_recv().unwrap(),
            Action::SetModel { .. }
        ));
        assert!(backend.setting_models.contains("s1"));
        assert!(!backend.send("s1", "first message".into(), vec![]));
        assert!(actions.try_recv().is_err());
        assert!(!backend.view.entries.contains_key("s1"));
        backend.apply(Update::Result {
            action: change,
            result: Ok(serde_json::json!({})),
        });
        assert!(!backend.setting_models.contains("s1"));
        assert!(backend.send("s1", "first message".into(), vec![]));
        assert!(matches!(actions.try_recv().unwrap(), Action::Prompt { .. }));
    }

    #[test]
    fn rejected_model_change_preserves_previous_model_and_allows_retry() {
        let (mut backend, mut actions) = isolated_backend();
        backend.view = ViewModel::demo();
        backend.view.online = true;
        let previous = backend.view.session("demo-1").unwrap().model.clone();
        let change = Action::SetModel {
            session: "demo-1".into(),
            model: "invalid".into(),
        };
        assert!(backend.submit(change.clone()));
        actions.try_recv().unwrap();
        backend.apply(Update::Result {
            action: change,
            result: Err("Model unavailable".into()),
        });
        assert!(!backend.setting_models.contains("demo-1"));
        assert_eq!(backend.view.session("demo-1").unwrap().model, previous);
        assert_eq!(backend.view.error.as_deref(), Some("Model unavailable"));
        assert!(backend.submit(Action::SetModel {
            session: "demo-1".into(),
            model: "valid".into()
        }));
    }

    #[test]
    fn preparing_session_never_submits_a_prompt() {
        let (mut backend, mut actions) = isolated_backend();
        let create = Action::CreateAgent {
            workspace: "w1".into(),
            provider: Some("talos".into()),
            title: None,
        };
        assert!(backend.submit(create.clone()));
        assert!(matches!(
            actions.try_recv().unwrap(),
            Action::CreateAgent { .. }
        ));
        backend.apply(Update::Result {
            action: create,
            result: Ok(serde_json::json!({
                "session": {"id": "s1", "agent_id": "a1"},
                "agent": {"provider": "talos"}
            })),
        });
        assert_eq!(backend.created_session.as_deref(), Some("s1"));
        assert!(backend.ready.contains("s1"));
        assert!(actions.try_recv().is_err());
        assert!(!backend.view.entries.contains_key("s1"));
    }

    #[test]
    fn late_prompt_acceptance_does_not_restart_a_finished_run() {
        let mut backend = Backend::new(|| {}, true);
        backend.view.run.insert("s1".into(), false);
        backend.apply_success(
            Action::Prompt {
                session: "s1".into(),
                text: "hello".into(),
                images: Vec::new(),
                key: "key".into(),
            },
            serde_json::json!({"status": "running"}),
        );
        assert!(!backend.view.running("s1"));
    }

    #[test]
    fn snapshot_does_not_overwrite_durable_run_state() {
        let mut view = ViewModel::default();
        view.run.insert("s1".into(), false);
        let snapshot = serde_json::json!({
            "sessions": [{"id": "s1", "agent_id": "a1"}],
            "agents": [{"id": "a1", "lifecycle": "Busy"}],
        });
        apply_snapshot(&mut view, &snapshot, true);
        assert!(!view.running("s1"));
    }

    #[test]
    fn token_formatting() {
        assert_eq!(format_tokens(999), "999");
        assert_eq!(format_tokens(1_500), "1.5k");
        assert_eq!(format_tokens(2_400_000), "2.4M");
    }

    #[test]
    fn snapshot_entries_are_parsed_in_order() {
        let value = serde_json::json!({
            "session": {"id": "s1"},
            "events": [
                {"id": "e1", "session_id": "s1", "sequence": 1, "kind": {"UserText": {"text": "hi"}}},
                {"id": "e2", "session_id": "s1", "sequence": 2, "kind": {"AssistantText": {"text": "hello"}}}
            ]
        });
        let entries = entries_from_snapshot(&value);
        assert_eq!(entries.len(), 2);
        assert!(matches!(&entries[0].kind, EntryKind::User { text } if text == "hi"));
        assert!(matches!(&entries[1].kind, EntryKind::Assistant { text, .. } if text == "hello"));
    }

    #[test]
    fn adjacent_completed_tools_group() {
        let tool = |id: &str| Entry {
            id: id.into(),
            run: None,
            at: String::new(),
            kind: EntryKind::Tool {
                call_id: id.into(),
                name: "Read".into(),
                target: "a.rs".into(),
                state: ToolState::Completed,
                input: String::new(),
                output: String::new(),
            },
        };
        let entries = vec![tool("a"), tool("b")];
        assert_eq!(group_tools(&entries).len(), 1);
    }

    #[test]
    fn snapshot_parses_provider_models_and_modes() {
        let providers = serde_json::json!([{
            "id": "talos", "display_name": "Talos", "available": true,
            "models": [{"id": "m1", "label": "Model 1", "is_default": true}],
            "modes": [{"id": "auto", "label": "Auto"}]
        }]);
        let parsed = parse_providers(Some(&providers));
        assert_eq!(parsed.len(), 1);
        assert_eq!(parsed[0].models[0].label, "Model 1");
        assert_eq!(parsed[0].modes[0].0, "auto");
        assert!(parsed[0].default);
    }

    #[test]
    fn independent_model_catalogue_preserves_full_ids_and_labels() {
        let catalogue = serde_json::json!([
            {"id": "openai/gpt-6.1-sol", "label": "gpt-6.1-sol"},
            {"id": "deepseek/deepseek-v4-pro", "label": "DeepSeek V4 Pro"}
        ]);
        let parsed = parse_model_summaries(catalogue.as_array().map(Vec::as_slice));
        assert_eq!(parsed.len(), 2);
        assert_eq!(parsed[0].id, "openai/gpt-6.1-sol");
        assert_eq!(parsed[0].label, "gpt-6.1-sol");
        assert_eq!(parsed[1].id, "deepseek/deepseek-v4-pro");
    }

    #[test]
    fn snapshot_parses_detailed_and_legacy_model_shapes() {
        let providers = serde_json::json!([
            {
                "id": "talos", "display_name": "Talos",
                "model_options": [{"id": "openai/gpt-5.6-luna", "label": "GPT 5.6 Luna"}],
                "models": [{"provider": "talos", "model_id": "openai/gpt-5.6-luna"}]
            },
            {
                "id": "claude", "display_name": "Claude",
                "models": [{"provider": "claude", "model_id": "claude-sonnet-4-6"}]
            }
        ]);
        let parsed = parse_providers(Some(&providers));
        assert_eq!(parsed[0].models[0].id, "openai/gpt-5.6-luna");
        assert_eq!(parsed[0].models[0].label, "GPT 5.6 Luna");
        assert_eq!(parsed[1].models[0].id, "claude-sonnet-4-6");
        assert_eq!(parsed[1].models[0].label, "claude-sonnet-4-6");
    }

    #[test]
    fn sessions_merge_agent_metadata() {
        let sessions = serde_json::json!([{"id": "s1", "agent_id": "a1"}]);
        let agents = serde_json::json!([{
            "id": "a1", "title": "Fix parser", "provider": "talos",
            "workspace_id": "w1", "lifecycle": "Ready"
        }]);
        let workspaces = vec![Workspace {
            id: "w1".into(),
            name: "paseo".into(),
            path: "/repo".into(),
            ..Workspace::default()
        }];
        let parsed = parse_sessions(Some(&sessions), Some(&agents), &workspaces);
        assert_eq!(parsed[0].title, "Fix parser");
        assert_eq!(parsed[0].cwd, "/repo");
        assert_eq!(parsed[0].provider, "talos");
    }
}
