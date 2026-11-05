//! Async bridge between the egui shell and the local `talosd` daemon.
//!
//! A dedicated thread owns a current-thread Tokio runtime. The UI thread sends
//! [`Action`]s through a bounded channel and drains [`Update`]s once per frame;
//! it never blocks on I/O. The worker auto-starts the daemon when the socket is
//! missing, reconnects with backoff, and replays durable events after a
//! reconnect so late events never land in the wrong session.

use std::{
    path::{Path, PathBuf},
    process::{Command as Process, Stdio},
    time::Duration,
};

use eframe::egui;
use serde_json::Value;
use talos_ipc::{Command, EventNotification, ServerFrame, WireEvent};
use tokio::sync::mpsc;

use crate::state::{
    Entry, PendingState, ProviderSummary, Session, SessionStatus, ToolState, Usage, ViewModel,
    Workspace, parse_session,
};

const BATCH: u32 = 64;

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

#[derive(Clone, Debug)]
pub enum Action {
    Load(String),
    Prompt {
        session: String,
        text: String,
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
    Refresh,
    Shutdown,
}

impl Action {
    fn command(&self) -> Option<Command> {
        Some(match self {
            Self::Load(session) => Command::LoadSession {
                session_id: session.clone(),
            },
            Self::Prompt { session, text, key } => Command::Prompt {
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
            Self::Rename { .. } | Self::Archive(_) => return None,
            Self::Refresh => Command::Snapshot,
            Self::Shutdown => return None,
        })
    }
}

// ---------------------------------------------------------------------------
// Updates
// ---------------------------------------------------------------------------

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

/// Backend handle owned by the UI thread.
pub struct Backend {
    pub view: ViewModel,
    sender: mpsc::Sender<Action>,
    receiver: mpsc::Receiver<Update>,
    next_key: u64,
    pub failed_prompt: Option<(String, String)>,
    pub endpoint: String,
    pub auto_started: bool,
    pub created_session: Option<String>,
    pub created_workspace: Option<String>,
}

impl Backend {
    pub fn new(ctx: egui::Context) -> Self {
        let (sender, actions) = mpsc::channel(BATCH as usize);
        let (updates, receiver) = mpsc::channel(512);
        let endpoint = socket_path().display().to_string();
        std::thread::Builder::new()
            .name("talos-desktop-backend".into())
            .spawn(move || {
                let Ok(runtime) = tokio::runtime::Builder::new_current_thread()
                    .enable_all()
                    .build()
                else {
                    return;
                };
                runtime.block_on(worker(ctx, actions, updates));
            })
            .ok();
        Self {
            view: ViewModel {
                status: "Connecting…".into(),
                ..ViewModel::default()
            },
            sender,
            receiver,
            next_key: 0,
            failed_prompt: None,
            endpoint,
            auto_started: false,
            created_session: None,
            created_workspace: None,
        }
    }

    /// Queue an action; returns false (and surfaces a notice) when offline.
    pub fn submit(&mut self, action: Action) -> bool {
        if self.sender.try_send(action).is_ok() {
            true
        } else {
            self.view.notice = Some("Backend queue is full; try again in a moment.".into());
            false
        }
    }

    /// Send a prompt with a stable client key for de-duplication.
    pub fn send(&mut self, session: &str, text: String) -> bool {
        self.next_key += 1;
        let key = format!("desktop-{}-{}", std::process::id(), self.next_key);
        let echoed = text.clone();
        if self.submit(Action::Prompt {
            session: session.into(),
            text,
            key,
        }) {
            self.view.echo_user_prompt(session, &echoed);
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
                self.auto_started = true;
                apply_snapshot(&mut self.view, &snapshot, false);
            }
            Update::Snapshot(snapshot) => apply_snapshot(&mut self.view, &snapshot, true),
            Update::Offline(reason) => {
                if self.view.sessions.is_empty() {
                    self.view = ViewModel::demo();
                    self.view.error = Some(reason);
                } else {
                    self.view.online = false;
                    self.view.status = "Reconnecting…".into();
                    self.view.notice = Some(reason);
                }
            }
            Update::Notice(message) => self.view.notice = Some(message),
            Update::Events(events) => {
                for event in &events {
                    self.view.apply_event(event);
                }
            }
            Update::Result { action, result } => match result {
                Ok(value) => self.apply_success(action, value),
                Err(error) => self.apply_failure(action, error),
            },
        }
    }

    fn apply_success(&mut self, action: Action, value: Value) {
        match action {
            Action::Load(session) => {
                let entries = entries_from_snapshot(&value);
                if !entries.is_empty() {
                    self.view.entries.insert(session, entries);
                }
            }
            Action::Prompt { .. } => {}
            Action::Cancel(session) => {
                self.view.run.insert(session, false);
            }
            Action::Decision { id, session, .. } => {
                self.view.pending.remove(&id);
                self.view.entries.entry(session.clone()).and_modify(|entries| {
                    entries.retain(|entry| {
                        !matches!(&entry.kind, crate::state::EntryKind::Permission { id: pending, .. } if pending == &id)
                    });
                });
            }
            Action::CreateAgent { workspace, .. } => {
                if let Some(session) = value.get("session").and_then(parse_session) {
                    let agent = &value["agent"];
                    let mut session = session;
                    session.title = agent
                        .get("title")
                        .and_then(Value::as_str)
                        .unwrap_or("New agent")
                        .to_owned();
                    session.provider = agent
                        .get("provider")
                        .and_then(Value::as_str)
                        .unwrap_or("talos")
                        .to_owned();
                    session.workspace = workspace.clone();
                    session.cwd = self
                        .view
                        .workspaces
                        .iter()
                        .find(|item| item.id == workspace)
                        .map(|item| item.path.clone())
                        .unwrap_or_default();
                    session.status = SessionStatus::Idle;
                    self.created_session = Some(session.id.clone());
                    self.view.sessions.insert(0, session);
                    self.view.notice = Some("Agent created.".into());
                    // The provider's model catalogue is advertised by ACP when
                    // its session is created. Refresh provider summaries now so
                    // the composer picker can expose those concrete model IDs.
                    self.submit(Action::Refresh);
                }
            }
            Action::CreateWorkspace { name, path } => {
                if let Some(id) = value.get("id").and_then(Value::as_str) {
                    self.created_workspace = Some(id.to_owned());
                    self.view.workspaces.push(Workspace {
                        id: id.to_owned(),
                        name: name.clone(),
                        path: path.clone(),
                    });
                    self.view.notice = Some(format!("Workspace “{name}” added."));
                }
            }
            Action::SetModel { session, model } => {
                if let Some(item) = self.view.session_mut(&session) {
                    item.model = Some(model);
                }
            }
            Action::SetConfig { .. } => {}
            Action::Rename { session, title } => {
                if let Some(item) = self.view.session_mut(&session) {
                    item.title = title;
                }
            }
            Action::Archive(session) => {
                self.view.sessions.retain(|item| item.id != session);
                self.view.entries.remove(&session);
            }
            Action::Refresh => apply_snapshot(&mut self.view, &value, true),
            Action::Shutdown => {
                self.view.online = false;
                self.view.status = "Daemon stopped".into();
            }
        }
    }

    fn apply_failure(&mut self, action: Action, error: String) {
        match action {
            Action::Load(session) => {
                self.view.entries.entry(session).or_default();
            }
            Action::Prompt { session, text, .. } => {
                self.view.run.insert(session.clone(), false);
                self.failed_prompt = Some((session, text));
            }
            Action::Decision { id, .. } => {
                self.view.pending.insert(id, PendingState::Failed);
            }
            Action::SetModel { session, .. }
            | Action::SetConfig { session, .. }
            | Action::Rename { session, .. }
            | Action::CreateAgent {
                workspace: session, ..
            } => {
                let _ = session;
            }
            Action::Cancel(_) | Action::Archive(_) | Action::Refresh | Action::Shutdown => {}
            Action::CreateWorkspace { .. } => {}
        }
        self.view.error = Some(error);
    }
}

// ---------------------------------------------------------------------------
// Snapshot parsing
// ---------------------------------------------------------------------------

fn apply_snapshot(view: &mut ViewModel, snapshot: &Value, preserve_entries: bool) {
    view.online = true;
    view.status = snapshot.get("home").and_then(Value::as_str).map_or_else(
        || "Connected · talosd".into(),
        |home| format!("Connected · {}", compact_path(home)),
    );
    view.workspaces = snapshot
        .get("workspaces")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(|item| {
            Some(Workspace {
                id: item.get("id")?.as_str()?.to_owned(),
                name: item.get("name")?.as_str()?.to_owned(),
                path: item
                    .get("path")
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned(),
            })
        })
        .collect();
    view.providers = parse_providers(snapshot.get("providers"));
    view.sessions = parse_sessions(
        snapshot.get("sessions"),
        snapshot.get("agents"),
        &view.workspaces,
    );
    view.pending.clear();
    for pending in snapshot
        .get("pending_permissions")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
    {
        if let Some(id) = pending.get("id").and_then(Value::as_str) {
            view.pending.insert(id.to_owned(), PendingState::Idle);
        }
    }
    if let Some(usage) = snapshot.get("usage").and_then(Value::as_object) {
        for (session, value) in usage {
            view.usage.insert(session.clone(), parse_usage(value));
        }
    }
    for session in &view.sessions {
        if session.status == SessionStatus::Attention {
            view.run.insert(session.id.clone(), false);
        }
    }
    if !preserve_entries {
        view.entries.clear();
    }
    view.notice = None;
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
            let models = raw_models
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
                .collect();
            let modes = item
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
                modes,
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
                parsed.title = agent
                    .get("title")
                    .and_then(Value::as_str)
                    .unwrap_or("Agent")
                    .to_owned();
                parsed.provider = agent
                    .get("provider")
                    .and_then(Value::as_str)
                    .unwrap_or("talos")
                    .to_owned();
                parsed.workspace = agent
                    .get("workspace_id")
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned();
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
                parsed.archived = agent
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
    if let Some(home) = directories::BaseDirs::new().map(|dirs| dirs.home_dir().to_path_buf()) {
        if let Ok(rest) = Path::new(path).strip_prefix(&home) {
            return format!("~/{}", rest.display());
        }
    }
    path.to_owned()
}

/// Convert a `SessionSnapshot` payload into timeline entries.
fn entries_from_snapshot(value: &Value) -> Vec<Entry> {
    let session_id = value
        .get("session")
        .and_then(|session| session.get("id"))
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned();
    let mut view = ViewModel::default();
    for event in value
        .get("events")
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

// ---------------------------------------------------------------------------
// Worker
// ---------------------------------------------------------------------------

async fn worker(
    ctx: egui::Context,
    mut actions: mpsc::Receiver<Action>,
    updates: mpsc::Sender<Update>,
) {
    loop {
        match connect_and_run(&ctx, &mut actions, &updates).await {
            Ok(true) => return,
            Ok(false) => {}
            Err(error) => {
                let _ = updates.send(Update::Offline(error)).await;
                ctx.request_repaint();
            }
        }
        // Backoff before reconnecting; also drains shutdown requests.
        tokio::select! {
            () = tokio::time::sleep(Duration::from_millis(1200)) => {}
            action = actions.recv() => {
                match action {
                    None | Some(Action::Shutdown) => return,
                    Some(action) => {
                        let _ = updates.send(Update::Offline("Daemon unavailable; action dropped.".into())).await;
                        let _ = action;
                    }
                }
            }
        }
    }
}

/// Connect, stream and serve actions. Returns `Ok(true)` when the app asked to
/// exit, `Ok(false)` when the connection dropped and should be retried.
async fn connect_and_run(
    ctx: &egui::Context,
    actions: &mut mpsc::Receiver<Action>,
    updates: &mpsc::Sender<Update>,
) -> Result<bool, String> {
    ensure_daemon(updates, ctx).await;
    let socket = socket_path();
    let token = auth_token();
    let mut client = talos_ipc::unix::connect(&socket, token)
        .await
        .map_err(|error| format!("Connect to talosd: {error}"))?;
    client
        .negotiate()
        .await
        .map_err(|error| format!("Protocol negotiation failed: {error}"))?;
    let snapshot = client
        .call(Command::Snapshot)
        .await
        .map_err(|error| format!("Snapshot failed: {error}"))?;
    let _ = updates.send(Update::Connected(snapshot)).await;
    ctx.request_repaint();

    loop {
        tokio::select! {
            action = actions.recv() => {
                let Some(action) = action else { return Ok(true) };
                if matches!(action, Action::Shutdown) {
                    return Ok(true);
                }
                let Some(command) = action.command() else { continue };
                let result = tokio::time::timeout(Duration::from_secs(60), client.call(command))
                    .await
                    .map_err(|_| "Request timed out".to_owned())
                    .and_then(|result| result.map_err(|error| error.to_string()));
                let _ = updates.send(Update::Result { action, result }).await;
                ctx.request_repaint();
                // Drain events the client buffered while awaiting the response.
                loop {
                    match tokio::time::timeout(Duration::from_millis(1), client.next_frame()).await {
                        Ok(Ok(ServerFrame::Event(event))) => {
                            let _ = updates.send(Update::Events(vec![event])).await;
                        }
                        Ok(Ok(ServerFrame::Response(_))) => {}
                        Ok(Err(_)) | Err(_) => break,
                    }
                }
            }
            frame = client.next_frame() => {
                match frame {
                    Ok(ServerFrame::Event(event)) => {
                        let _ = updates.send(Update::Events(vec![event])).await;
                        ctx.request_repaint();
                    }
                    Ok(ServerFrame::Response(_)) => {}
                    Err(error) => return Err(format!("Connection lost: {error}")),
                }
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
    if let Ok(token) = std::env::var("TALOSD_AUTH_TOKEN") {
        if !token.is_empty() {
            return Some(token);
        }
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
    if let Ok(exe) = std::env::current_exe() {
        if let Some(parent) = exe.parent() {
            let candidate = parent.join(if cfg!(windows) {
                "talosd.exe"
            } else {
                "talosd"
            });
            if candidate.is_file() {
                return Some(candidate);
            }
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

async fn ensure_daemon(updates: &mpsc::Sender<Update>, ctx: &egui::Context) {
    let socket = socket_path();
    if socket.exists() {
        return;
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
        if is_quiet_tool {
            if let Some(last) = groups.last_mut() {
                if matches!(
                    last.first().map(|entry| &entry.kind),
                    Some(crate::state::EntryKind::Tool {
                        state: ToolState::Completed,
                        ..
                    })
                ) {
                    last.push(entry);
                    continue;
                }
            }
        }
        groups.push(vec![entry]);
    }
    groups
}

/// Formats a token count compactly (`1.2k`, `18.4k`, `1.1M`).
#[must_use]
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
        }];
        let parsed = parse_sessions(Some(&sessions), Some(&agents), &workspaces);
        assert_eq!(parsed[0].title, "Fix parser");
        assert_eq!(parsed[0].cwd, "/repo");
        assert_eq!(parsed[0].provider, "talos");
    }
}
