//! Backend-derived view model for the desktop shell.
//!
//! [`ViewModel`] is the read-only projection of daemon state (workspaces,
//! sessions, timeline entries, permissions, usage). Ephemeral UI state (focus,
//! drafts, open popovers) lives in the application struct instead, so this type
//! stays cheap to clone and easy to test.

use std::collections::{HashMap, HashSet};

use serde_json::Value;

pub const PLACEHOLDER: &str = "Ask anything. @ files, $ skills, / commands";

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

#[derive(Clone, Copy, PartialEq, Eq, Debug, Default)]
pub enum SessionStatus {
    #[default]
    Idle,
    Running,
    Attention,
    Error,
    Offline,
}

impl SessionStatus {
    #[must_use]
    pub fn label(self) -> &'static str {
        match self {
            Self::Idle => "Idle",
            Self::Running => "Running",
            Self::Attention => "Needs you",
            Self::Error => "Error",
            Self::Offline => "Offline",
        }
    }
}

#[derive(Clone, Debug, Default)]
pub struct Session {
    pub id: String,
    pub agent_id: String,
    pub title: String,
    pub detail: String,
    pub model: Option<String>,
    pub mode: Option<String>,
    pub provider: String,
    pub workspace: String,
    pub cwd: String,
    pub time: String,
    pub status: SessionStatus,
    pub archived: bool,
    pub lifecycle: String,
}

impl Session {
    #[must_use]
    pub fn model_label(&self) -> String {
        self.model.clone().unwrap_or_else(|| "Default model".into())
    }
}

// ---------------------------------------------------------------------------
// Timeline entries
// ---------------------------------------------------------------------------

#[derive(Clone, Copy, PartialEq, Eq, Debug, Default)]
pub enum ToolState {
    #[default]
    Running,
    Completed,
    Failed,
}

impl ToolState {
    #[must_use]
    pub fn label(self) -> &'static str {
        match self {
            Self::Running => "Running",
            Self::Completed => "Done",
            Self::Failed => "Failed",
        }
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Usage {
    pub input_tokens: Option<u64>,
    pub output_tokens: Option<u64>,
    pub cached_read_tokens: Option<u64>,
    pub context_used: Option<u64>,
    pub context_size: Option<u64>,
    pub cost_micros: Option<u64>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum EntryKind {
    User {
        text: String,
    },
    Assistant {
        text: String,
        streaming: bool,
    },
    Thought {
        text: String,
        streaming: bool,
    },
    Tool {
        call_id: String,
        name: String,
        target: String,
        state: ToolState,
        input: String,
        output: String,
    },
    Permission {
        id: String,
        action: String,
        target: String,
        risk: String,
        reason: String,
    },
    Notice {
        code: Option<String>,
        message: String,
    },
    Error {
        code: Option<String>,
        message: String,
    },
    Attachment {
        name: String,
        mime: String,
        size: u64,
    },
}

#[derive(Clone, Debug)]
pub struct Entry {
    pub id: String,
    pub run: Option<String>,
    pub at: String,
    pub kind: EntryKind,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug, Default)]
pub enum PendingState {
    #[default]
    Idle,
    Submitting,
    Failed,
}

// ---------------------------------------------------------------------------
// View model
// ---------------------------------------------------------------------------

#[derive(Clone, Debug, Default)]
pub struct Workspace {
    pub id: String,
    pub name: String,
    pub path: String,
    pub project: Option<String>,
    pub archived: bool,
    pub managed: bool,
}

#[derive(Default)]
pub struct ViewModel {
    pub sessions: Vec<Session>,
    pub entries: HashMap<String, Vec<Entry>>,
    pub workspaces: Vec<Workspace>,
    pub providers: Vec<ProviderSummary>,
    pub projects: Vec<Value>,
    pub search: Value,
    pub terminals: Value,
    pub terminal_capture: Value,
    pub diagnostics: Value,
    pub usage_summary: Value,
    pub config_options: HashMap<String, Value>,
    pub pending: HashMap<String, PendingState>,
    pub usage: HashMap<String, Usage>,
    pub run: HashMap<String, bool>,
    pub status: String,
    pub error: Option<String>,
    pub notice: Option<String>,
    pub online: bool,
    seen: HashSet<(String, String)>,
}

#[derive(Clone, Debug)]
pub struct ProviderSummary {
    pub id: String,
    pub name: String,
    pub available: bool,
    pub default: bool,
    pub message: Option<String>,
    pub models: Vec<ModelSummary>,
    pub modes: Vec<(String, String)>,
    pub capabilities: Value,
}

impl ProviderSummary {
    pub fn supports(&self, capability: &str) -> bool {
        self.capabilities.get(capability).and_then(Value::as_bool) == Some(true)
    }
}

#[derive(Clone, Debug)]
pub struct ModelSummary {
    pub id: String,
    pub label: String,
    pub default: bool,
}

impl ViewModel {
    /// Pending-list snapshots carry request identity; resolution events only
    /// carry an outcome and cannot identify simultaneous requests in one run.
    pub fn apply_pending(&mut self, request: &Value) {
        let (Some(session), Some(id)) = (request["session_id"].as_str(), request["id"].as_str())
        else {
            return;
        };
        self.push_entry(
            session,
            Entry {
                id: id.into(),
                run: request["run_id"].as_str().map(str::to_owned),
                at: String::new(),
                kind: EntryKind::Permission {
                    id: id.into(),
                    action: request["action"].as_str().unwrap_or("Permission").into(),
                    target: request["target"].as_str().unwrap_or_default().into(),
                    risk: request["risk"].as_str().unwrap_or("Unknown").into(),
                    reason: request["details"]["reason"]
                        .as_str()
                        .map_or_else(|| pretty_value(&request["details"]), str::to_owned),
                },
            },
        );
    }
    #[must_use]
    pub fn running(&self, session: &str) -> bool {
        self.run.get(session).copied().unwrap_or(false)
    }

    #[must_use]
    pub fn session(&self, id: &str) -> Option<&Session> {
        self.sessions.iter().find(|session| session.id == id)
    }

    #[must_use]
    pub fn session_mut(&mut self, id: &str) -> Option<&mut Session> {
        self.sessions.iter_mut().find(|session| session.id == id)
    }

    #[must_use]
    pub fn provider(&self, id: &str) -> Option<&ProviderSummary> {
        self.providers.iter().find(|provider| provider.id == id)
    }

    /// Number of sessions awaiting a permission decision.
    #[must_use]
    pub fn attention_count(&self) -> usize {
        self.sessions
            .iter()
            .filter(|session| session.status == SessionStatus::Attention)
            .count()
    }

    /// Sessions for a workspace, optionally grouped: running first then recency.
    #[must_use]
    pub fn sessions_for(&self, workspace: Option<&str>) -> Vec<&Session> {
        let mut sessions: Vec<&Session> = self
            .sessions
            .iter()
            .filter(|session| {
                workspace.is_none_or(|id| session.workspace.is_empty() || session.workspace == id)
            })
            .collect();
        sessions.sort_by_key(|session| match session.status {
            SessionStatus::Attention => 0,
            SessionStatus::Running => 1,
            _ => 2,
        });
        sessions
    }

    // -- event application ---------------------------------------------------

    /// Apply one durable timeline event, coalescing streamed deltas.
    pub fn apply_event(&mut self, event: &talos_ipc::EventNotification) {
        let payload = &event.event.payload;
        let Some(session_id) = payload.get("session_id").and_then(Value::as_str) else {
            return;
        };
        if !event.event_id.is_empty()
            && !self
                .seen
                .insert((session_id.to_owned(), event.event_id.clone()))
        {
            return;
        }
        let run = payload
            .get("run_id")
            .and_then(Value::as_str)
            .map(str::to_owned);
        let at = payload
            .get("timestamp")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned();
        let Some((name, content)) = payload
            .get("kind")
            .and_then(Value::as_object)
            .and_then(|map| map.iter().next())
        else {
            return;
        };
        self.apply_kind(session_id, name, content, &event.event_id, run, at);
    }

    fn apply_kind(
        &mut self,
        session: &str,
        name: &str,
        content: &Value,
        event_id: &str,
        run: Option<String>,
        at: String,
    ) {
        let text = || {
            content
                .get("text")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned()
        };
        match name {
            "UserText" => {
                self.set_status(session, SessionStatus::Running);
                // Replace the optimistic local echo with the durable event. The
                // backend's event ID is canonical, while the optimistic entry
                // has a local ID; without this reconciliation every sent prompt
                // is rendered twice once the daemon event arrives.
                let user_text = text();
                if let Some(entries) = self.entries.get_mut(session)
                    && let Some(index) = entries.iter().position(|entry| {
                        entry.id.starts_with("local-")
                            && matches!(&entry.kind, EntryKind::User { text } if text == &user_text)
                    })
                {
                    entries.remove(index);
                }
                self.push_entry(
                    session,
                    Entry {
                        id: event_id.into(),
                        run,
                        at,
                        kind: EntryKind::User { text: user_text },
                    },
                );
            }
            "AssistantText" => {
                self.append_stream(session, run, at, event_id, &text(), false);
                self.set_status(session, SessionStatus::Running);
            }
            "Thought" => {
                self.append_stream(session, run, at, event_id, &text(), true);
            }
            "Tool" => {
                let call_id = content
                    .get("id")
                    .and_then(Value::as_str)
                    .unwrap_or(event_id)
                    .to_owned();
                let state = match content.get("status").and_then(Value::as_str) {
                    Some("Completed") => ToolState::Completed,
                    Some("Failed") => ToolState::Failed,
                    _ => ToolState::Running,
                };
                let input = content
                    .get("input")
                    .filter(|value| !value.is_null())
                    .map(pretty_value)
                    .unwrap_or_default();
                let output = content
                    .get("output")
                    .filter(|value| !value.is_null())
                    .map(pretty_value)
                    .unwrap_or_default();
                let name = content
                    .get("name")
                    .and_then(Value::as_str)
                    .unwrap_or("Tool")
                    .to_owned();
                let target = content
                    .get("title")
                    .and_then(Value::as_str)
                    .filter(|title| !title.is_empty())
                    .map_or_else(|| tool_target(&input), str::to_owned);
                let entries = self.entries.entry(session.into()).or_default();
                if let Some(entry) = entries
                    .iter_mut()
                    .rev()
                    .find(|entry| matches!(&entry.kind, EntryKind::Tool { call_id: id, .. } if id == &call_id))
                {
                    if let EntryKind::Tool {
                        name: known_name,
                        target: known_target,
                        state: known_state,
                        input: known_input,
                        output: known_output,
                        ..
                    } = &mut entry.kind
                    {
                        *known_name = name;
                        if !target.is_empty() {
                            *known_target = target;
                        }
                        *known_state = state;
                        if !input.is_empty() {
                            *known_input = input;
                        }
                        if !output.is_empty() {
                            *known_output = output;
                        }
                    }
                    entry.at = at;
                } else {
                    entries.push(Entry {
                        id: call_id.clone(),
                        run,
                        at,
                        kind: EntryKind::Tool {
                            call_id,
                            name,
                            target,
                            state,
                            input,
                            output,
                        },
                    });
                }
            }
            "PermissionRequested" => {
                let id = content
                    .get("id")
                    .and_then(Value::as_str)
                    .unwrap_or(event_id)
                    .to_owned();
                let risk = content
                    .get("risk")
                    .and_then(Value::as_str)
                    .unwrap_or("Medium")
                    .to_owned();
                let reason = content
                    .get("details")
                    .and_then(|details| details.get("reason"))
                    .and_then(Value::as_str)
                    .map(str::to_owned)
                    .or_else(|| {
                        content
                            .get("details")
                            .filter(|details| !details.is_null())
                            .map(pretty_value)
                    })
                    .unwrap_or_default();
                self.pending.entry(id.clone()).or_default();
                self.set_status(session, SessionStatus::Attention);
                self.push_entry(
                    session,
                    Entry {
                        id: id.clone(),
                        run,
                        at,
                        kind: EntryKind::Permission {
                            id,
                            action: content
                                .get("action")
                                .and_then(Value::as_str)
                                .unwrap_or("Permission")
                                .to_owned(),
                            target: content
                                .get("target")
                                .and_then(Value::as_str)
                                .unwrap_or_default()
                                .to_owned(),
                            risk,
                            reason,
                        },
                    },
                );
            }
            "PermissionResolved" => self.resolve_permission(session, run.as_deref()),
            "RunState" => {
                let state = content
                    .get("state")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                let running =
                    matches!(state, "Queued" | "Starting" | "Running" | "CancelRequested");
                self.run.insert(session.into(), running);
                if !running {
                    self.finish_streams(session);
                    self.resolve_permission(session, run.as_deref());
                }
                let status = match state {
                    "Failed" => SessionStatus::Error,
                    "Queued" | "Starting" | "Running" | "CancelRequested" => SessionStatus::Running,
                    _ if self.has_pending_permission(session) => SessionStatus::Attention,
                    _ => SessionStatus::Idle,
                };
                self.set_status(session, status);
            }
            "Usage" => {
                let usage = Usage {
                    input_tokens: content.get("input_tokens").and_then(Value::as_u64),
                    output_tokens: content.get("output_tokens").and_then(Value::as_u64),
                    cached_read_tokens: content.get("cached_read_tokens").and_then(Value::as_u64),
                    context_used: content.get("context_used").and_then(Value::as_u64),
                    context_size: content.get("context_size").and_then(Value::as_u64),
                    cost_micros: content.get("cost_micros").and_then(Value::as_u64),
                };
                self.usage.insert(session.into(), usage);
            }
            "Attachment" => self.push_entry(
                session,
                Entry {
                    id: event_id.into(),
                    run,
                    at,
                    kind: EntryKind::Attachment {
                        name: content
                            .get("name")
                            .and_then(Value::as_str)
                            .unwrap_or("Image")
                            .into(),
                        mime: content
                            .get("mime_type")
                            .and_then(Value::as_str)
                            .unwrap_or("image")
                            .into(),
                        size: content
                            .get("size_bytes")
                            .and_then(Value::as_u64)
                            .unwrap_or_default(),
                    },
                },
            ),
            "Notice" => {
                let message = content
                    .get("message")
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned();
                if message.is_empty() {
                    return;
                }
                self.push_entry(
                    session,
                    Entry {
                        id: event_id.into(),
                        run,
                        at,
                        kind: EntryKind::Notice {
                            code: content
                                .get("code")
                                .and_then(Value::as_str)
                                .map(str::to_owned),
                            message,
                        },
                    },
                );
            }
            "Error" => {
                let message = content
                    .get("message")
                    .and_then(Value::as_str)
                    .unwrap_or("The run failed.")
                    .to_owned();
                self.push_entry(
                    session,
                    Entry {
                        id: event_id.into(),
                        run,
                        at,
                        kind: EntryKind::Error {
                            code: content
                                .get("code")
                                .and_then(Value::as_str)
                                .map(str::to_owned),
                            message,
                        },
                    },
                );
                self.set_status(session, SessionStatus::Error);
            }
            "Extension" => self.apply_extension(session, content),
            _ => {}
        }
    }

    fn apply_extension(&mut self, session: &str, content: &Value) {
        let kind = content
            .get("kind")
            .and_then(Value::as_str)
            .unwrap_or_default();
        let payload = content.get("payload").cloned().unwrap_or(Value::Null);
        if kind == "provider_config_options" {
            self.config_options.insert(
                session.into(),
                payload.get("options").cloned().unwrap_or_default(),
            );
        } else if kind == "provider_mode_changed"
            && let Some(mode) = payload.get("modeId").and_then(Value::as_str)
            && let Some(item) = self.session_mut(session)
        {
            item.mode = Some(mode.to_owned());
        }
    }

    fn append_stream(
        &mut self,
        session: &str,
        run: Option<String>,
        at: String,
        event_id: &str,
        delta: &str,
        thought: bool,
    ) {
        if delta.is_empty() {
            return;
        }
        let entries = self.entries.entry(session.into()).or_default();
        let matches_stream = |kind: &EntryKind| {
            if thought {
                matches!(
                    kind,
                    EntryKind::Thought {
                        streaming: true,
                        ..
                    }
                )
            } else {
                matches!(
                    kind,
                    EntryKind::Assistant {
                        streaming: true,
                        ..
                    }
                )
            }
        };
        if let Some(entry) = entries
            .last_mut()
            .filter(|entry| entry.run == run && matches_stream(&entry.kind))
        {
            match &mut entry.kind {
                EntryKind::Assistant { text, .. } | EntryKind::Thought { text, .. } => {
                    text.push_str(delta);
                }
                _ => {}
            }
            entry.at = at;
        } else {
            let kind = if thought {
                EntryKind::Thought {
                    text: delta.to_owned(),
                    streaming: true,
                }
            } else {
                EntryKind::Assistant {
                    text: delta.to_owned(),
                    streaming: true,
                }
            };
            entries.push(Entry {
                id: event_id.into(),
                run,
                at,
                kind,
            });
        }
    }

    fn finish_streams(&mut self, session: &str) {
        if let Some(entries) = self.entries.get_mut(session) {
            for entry in entries {
                match &mut entry.kind {
                    EntryKind::Assistant { streaming, .. }
                    | EntryKind::Thought { streaming, .. } => {
                        *streaming = false;
                    }
                    _ => {}
                }
            }
        }
    }

    fn push_entry(&mut self, session: &str, entry: Entry) {
        let entries = self.entries.entry(session.into()).or_default();
        if let Some(existing) = entries.iter_mut().find(|known| known.id == entry.id) {
            *existing = entry;
        } else {
            entries.push(entry);
        }
    }

    fn resolve_permission(&mut self, session: &str, run: Option<&str>) {
        if let Some(entries) = self.entries.get_mut(session) {
            let removed: Vec<String> = entries
                .iter()
                .filter_map(|entry| match &entry.kind {
                    EntryKind::Permission { id, .. }
                        if run.is_none_or(|run| entry.run.as_deref() == Some(run)) =>
                    {
                        Some(id.clone())
                    }
                    _ => None,
                })
                .collect();
            entries.retain(|entry| {
                !matches!(&entry.kind, EntryKind::Permission { id, .. } if removed.contains(id))
            });
            for id in removed {
                self.pending.remove(&id);
            }
        }
        self.refresh_attention(session);
    }

    fn has_pending_permission(&self, session: &str) -> bool {
        self.entries.get(session).is_some_and(|entries| {
            entries
                .iter()
                .any(|entry| matches!(entry.kind, EntryKind::Permission { .. }))
        })
    }

    pub fn refresh_attention(&mut self, session: &str) {
        let attention = self.has_pending_permission(session);
        let running = self.running(session);
        if let Some(item) = self.session_mut(session) {
            item.status = if attention {
                SessionStatus::Attention
            } else if running {
                SessionStatus::Running
            } else if item.status == SessionStatus::Attention {
                SessionStatus::Idle
            } else {
                item.status
            };
        }
    }

    fn set_status(&mut self, session: &str, status: SessionStatus) {
        let status = if self.has_pending_permission(session) {
            SessionStatus::Attention
        } else {
            status
        };
        if let Some(item) = self.session_mut(session)
            && (item.status != SessionStatus::Attention || status != SessionStatus::Idle)
        {
            item.status = status;
        }
    }

    /// Mark a just-sent prompt as running and optimistically echo it.
    pub fn echo_user_prompt(&mut self, session: &str, text: &str) {
        self.run.insert(session.into(), true);
        self.set_status(session, SessionStatus::Running);
        if text.is_empty() {
            return;
        }
        self.push_entry(
            session,
            Entry {
                id: format!("local-{}", self.next_local_id()),
                run: None,
                at: String::new(),
                kind: EntryKind::User {
                    text: text.to_owned(),
                },
            },
        );
    }

    fn next_local_id(&self) -> usize {
        self.entries
            .values()
            .map(Vec::len)
            .max()
            .unwrap_or_default()
    }

    /// A representative offline preview so the shell can be inspected without
    /// a daemon. Never presented as live data: the status bar says "Preview".
    #[must_use]
    pub fn demo() -> Self {
        let mut model = Self {
            status: "Preview · daemon offline".into(),
            online: false,
            ..Self::default()
        };
        model.workspaces = vec![
            Workspace {
                id: "paseo-rs".into(),
                name: "paseo-rs".into(),
                path: "/home/adetola/repos/paseo-rs".into(),
                ..Workspace::default()
            },
            Workspace {
                id: "talos".into(),
                name: "talos".into(),
                path: "/home/adetola/repos/talos".into(),
                ..Workspace::default()
            },
        ];
        model.providers = vec![
            ProviderSummary {
                id: "talos".into(),
                name: "Talos".into(),
                available: true,
                default: true,
                message: None,
                models: vec![ModelSummary {
                    id: "deepseek-chat".into(),
                    label: "DeepSeek Chat".into(),
                    default: true,
                }],
                capabilities: serde_json::json!({"images":true,"model_selection":true,"modes":true,"config_options":true}),
                modes: vec![
                    ("ask".into(), "Ask".into()),
                    ("auto".into(), "Auto".into()),
                    ("panic".into(), "Panic".into()),
                ],
            },
            ProviderSummary {
                id: "claude".into(),
                name: "Claude Code".into(),
                available: true,
                default: false,
                message: None,
                models: vec![ModelSummary {
                    id: "claude-sonnet".into(),
                    label: "Claude Sonnet".into(),
                    default: true,
                }],
                capabilities: Value::Null,
                modes: Vec::new(),
            },
        ];
        model.sessions = vec![
            Session {
                id: "demo-1".into(),
                title: "Composer polish pass".into(),
                detail: "Talos".into(),
                model: Some("DeepSeek Chat".into()),
                mode: Some("Auto".into()),
                provider: "talos".into(),
                workspace: "paseo-rs".into(),
                cwd: "/home/adetola/repos/paseo-rs".into(),
                time: "now".into(),
                status: SessionStatus::Running,
                archived: false,
                ..Session::default()
            },
            Session {
                id: "demo-2".into(),
                title: "Investigate stream ordering".into(),
                detail: "Claude Code".into(),
                model: Some("Claude Sonnet".into()),
                mode: None,
                provider: "claude".into(),
                workspace: "talos".into(),
                cwd: "/home/adetola/repos/talos".into(),
                time: "12m".into(),
                status: SessionStatus::Attention,
                archived: false,
                ..Session::default()
            },
            Session {
                id: "demo-3".into(),
                title: "Release checklist review".into(),
                detail: "Talos".into(),
                model: Some("DeepSeek Chat".into()),
                mode: Some("Ask".into()),
                provider: "talos".into(),
                workspace: "paseo-rs".into(),
                cwd: "/home/adetola/repos/paseo-rs".into(),
                time: "1h".into(),
                status: SessionStatus::Idle,
                archived: false,
                ..Session::default()
            },
        ];
        let entries = vec![
            Entry {
                id: "demo-u1".into(),
                run: Some("r1".into()),
                at: "12:01".into(),
                kind: EntryKind::User {
                    text: "Tighten the composer and make streaming feel alive.".into(),
                },
            },
            Entry {
                id: "demo-t1".into(),
                run: Some("r1".into()),
                at: "12:01".into(),
                kind: EntryKind::Tool {
                    call_id: "call-1".into(),
                    name: "Read".into(),
                    target: "apps/talos-desktop/src/main.rs".into(),
                    state: ToolState::Completed,
                    input: "{\"path\":\"apps/talos-desktop/src/main.rs\"}".into(),
                    output: "read 695 lines".into(),
                },
            },
            Entry {
                id: "demo-a1".into(),
                run: Some("r1".into()),
                at: "12:02".into(),
                kind: EntryKind::Assistant {
                    text: "Here is the plan:\n\n1. Rebuild the composer shell around a real `TextEdit`.\n2. Add spring-based hover and drawer motion.\n3. Keep the timeline readable at every width.\n\n```rust\nlet progress = motion::animate_bool(ctx, id, open, true);\n```\n\nThe provider mark stays warm orange so status colors never collide.".into(),
                    streaming: false,
                },
            },
            Entry {
                id: "demo-p1".into(),
                run: Some("r1".into()),
                at: "12:03".into(),
                kind: EntryKind::Permission {
                    id: "perm-1".into(),
                    action: "Run command".into(),
                    target: "cargo test --workspace".into(),
                    risk: "Medium".into(),
                    reason: "Runs the test suite in the workspace root.".into(),
                },
            },
        ];
        model.entries.insert("demo-1".into(), entries);
        model.usage.insert(
            "demo-1".into(),
            Usage {
                input_tokens: Some(124_300),
                output_tokens: Some(8_420),
                cached_read_tokens: Some(98_000),
                context_used: Some(41_200),
                context_size: Some(128_000),
                cost_micros: Some(184_000),
            },
        );
        model.run.insert("demo-1".into(), true);
        model
    }
}

fn pretty_value(value: &Value) -> String {
    serde_json::to_string_pretty(value).unwrap_or_else(|_| value.to_string())
}

fn tool_target(input: &str) -> String {
    input
        .lines()
        .find_map(|line| {
            let trimmed = line.trim();
            let rest = trimmed
                .strip_prefix("\"command\": ")
                .or_else(|| trimmed.strip_prefix("\"path\": "))?;
            Some(rest.trim_matches(|c| c == '"' || c == ',').to_owned())
        })
        .unwrap_or_default()
}

// ---------------------------------------------------------------------------
// Parsing daemon payloads
// ---------------------------------------------------------------------------

pub fn parse_session(value: &Value) -> Option<Session> {
    let id = value.get("id")?.as_str()?.to_owned();
    let model = value
        .get("model")
        .and_then(|model| {
            model
                .get("model_id")
                .and_then(Value::as_str)
                .or_else(|| model.as_str())
        })
        .map(str::to_owned);
    Some(Session {
        id,
        agent_id: value["agent_id"].as_str().unwrap_or_default().into(),
        lifecycle: value["lifecycle"].as_str().unwrap_or("New").into(),
        title: value
            .get("title")
            .and_then(Value::as_str)
            .unwrap_or("Untitled agent")
            .to_owned(),
        detail: value
            .get("detail")
            .and_then(Value::as_str)
            .unwrap_or("Agent session")
            .to_owned(),
        model,
        mode: value.get("mode").and_then(Value::as_str).map(str::to_owned),
        provider: value
            .get("provider")
            .and_then(Value::as_str)
            .unwrap_or("talos")
            .to_owned(),
        workspace: value
            .get("workspace_id")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned(),
        cwd: value
            .get("cwd")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned(),
        time: relative_time(value.get("updated_at").and_then(Value::as_str)),
        status: SessionStatus::Idle,
        archived: value
            .get("archived_at")
            .is_some_and(|archived| !archived.is_null()),
    })
}

/// Compact relative timestamp for list rows.
#[must_use]
pub fn relative_time(timestamp: Option<&str>) -> String {
    let Some(timestamp) = timestamp else {
        return String::new();
    };
    let Ok(parsed) = chrono::DateTime::parse_from_rfc3339(timestamp) else {
        return String::new();
    };
    let delta = chrono::Utc::now().signed_duration_since(parsed);
    let minutes = delta.num_minutes();
    if minutes < 1 {
        "now".into()
    } else if minutes < 60 {
        format!("{minutes}m")
    } else if minutes < 60 * 24 {
        format!("{}h", minutes / 60)
    } else if minutes < 60 * 24 * 7 {
        format!("{}d", minutes / (60 * 24))
    } else {
        parsed.format("%b %-d").to_string()
    }
}

// ---------------------------------------------------------------------------
// Layout helpers
// ---------------------------------------------------------------------------

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Layout {
    Wide,
    Compact,
    Minimal,
}

#[must_use]
pub fn layout_for_width(width: f32) -> Layout {
    if width < 720.0 {
        Layout::Minimal
    } else if width < 1080.0 {
        Layout::Compact
    } else {
        Layout::Wide
    }
}

/// Whether the composer may submit.
#[must_use]
pub fn can_send(text: &str, valid_attachments: usize, running: bool) -> bool {
    !running && (!text.trim().is_empty() || valid_attachments > 0)
}

/// Height in rows for the composer input, clamped for stability.
#[must_use]
pub fn draft_lines(text: &str, width: f32) -> usize {
    let chars_per_line = ((width - 32.0) / 7.2).max(12.0);
    let mut rows = 0;
    for line in text.lines() {
        rows += 1;
        let mut columns = 0.0;
        for _ in line.chars() {
            if columns >= chars_per_line {
                rows += 1;
                columns = 0.0;
            }
            columns += 1.0;
        }
    }
    rows.clamp(1, 6)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use talos_ipc::{EventNotification, WireEvent};

    fn event(id: &str, run: &str, kind: Value) -> EventNotification {
        EventNotification {
            version: 1,
            cursor: 1,
            event_id: id.into(),
            event: WireEvent {
                kind: "timeline".into(),
                payload: json!({
                    "session_id": "s",
                    "run_id": run,
                    "kind": kind,
                    "timestamp": "2026-01-01T00:00:00Z"
                }),
            },
        }
    }

    #[test]
    fn breakpoints_are_exhaustive() {
        assert_eq!(layout_for_width(719.0), Layout::Minimal);
        assert_eq!(layout_for_width(720.0), Layout::Compact);
        assert_eq!(layout_for_width(1080.0), Layout::Wide);
    }

    #[test]
    fn composer_send_gate() {
        assert!(!can_send("  \n ", 0, false));
        assert!(can_send("hi", 0, false));
        assert!(!can_send("hi", 0, true));
        assert!(can_send("", 1, false));
    }

    #[test]
    fn draft_rows_are_bounded() {
        assert_eq!(draft_lines("", 300.0), 1);
        assert_eq!(draft_lines(&"x".repeat(3000), 300.0), 6);
    }

    #[test]
    fn streamed_chunks_coalesce_per_run() {
        let mut view = ViewModel::default();
        view.apply_event(&event("1", "r1", json!({"AssistantText": {"text": "Hel"}})));
        view.apply_event(&event("2", "r1", json!({"AssistantText": {"text": "lo"}})));
        view.apply_event(&event(
            "3",
            "r2",
            json!({"AssistantText": {"text": "New run"}}),
        ));
        let entries = &view.entries["s"];
        assert_eq!(entries.len(), 2);
        assert!(matches!(
            &entries[0].kind,
            EntryKind::Assistant { text, streaming } if text == "Hello" && *streaming
        ));
    }

    #[test]
    fn thoughts_are_separate_from_answers() {
        let mut view = ViewModel::default();
        view.apply_event(&event("1", "r1", json!({"Thought": {"text": "plan"}})));
        view.apply_event(&event(
            "2",
            "r1",
            json!({"AssistantText": {"text": "done"}}),
        ));
        assert_eq!(view.entries["s"].len(), 2);
    }

    #[test]
    fn run_end_finishes_streams() {
        let mut view = ViewModel::default();
        view.apply_event(&event("1", "r1", json!({"AssistantText": {"text": "hi"}})));
        view.apply_event(&event(
            "2",
            "r1",
            json!({"RunState": {"state": "Completed"}}),
        ));
        assert!(matches!(
            &view.entries["s"][0].kind,
            EntryKind::Assistant {
                streaming: false,
                ..
            }
        ));
        assert!(!view.running("s"));
    }

    #[test]
    fn tool_updates_merge_by_call_id() {
        let mut view = ViewModel::default();
        view.apply_event(&event(
            "1",
            "r1",
            json!({"Tool": {"id": "call-1", "name": "Read", "status": "Running", "input": {"path": "a.rs"}}}),
        ));
        view.apply_event(&event(
            "2",
            "r1",
            json!({"Tool": {"id": "call-1", "name": "Read", "status": "Completed", "output": "ok"}}),
        ));
        let entries = &view.entries["s"];
        assert_eq!(entries.len(), 1);
        assert!(matches!(
            &entries[0].kind,
            EntryKind::Tool { state: ToolState::Completed, output, .. } if output == "\"ok\""
        ));
    }

    #[test]
    fn permissions_are_scoped_and_resolved_by_run() {
        let mut view = ViewModel {
            sessions: vec![Session {
                id: "s".into(),
                ..Session::default()
            }],
            ..ViewModel::default()
        };
        view.apply_event(&event(
            "a",
            "r1",
            json!({"PermissionRequested": {"id": "perm-a", "action": "write", "target": "x"}}),
        ));
        view.apply_event(&event(
            "b",
            "r2",
            json!({"PermissionRequested": {"id": "perm-b", "action": "write", "target": "y"}}),
        ));
        view.apply_event(&event("c", "r1", json!({"PermissionResolved": "Denied"})));
        let entries = &view.entries["s"];
        assert_eq!(entries.len(), 1);
        assert!(matches!(&entries[0].kind, EntryKind::Permission { id, .. } if id == "perm-b"));
        assert_eq!(view.attention_count(), 1);
    }

    #[test]
    fn durable_user_event_replaces_optimistic_echo() {
        let mut view = ViewModel::default();
        view.echo_user_prompt("s", "hello");
        view.apply_event(&event("e1", "r1", json!({"UserText": {"text": "hello"}})));
        let entries = &view.entries["s"];
        assert_eq!(entries.len(), 1);
        assert_eq!(entries[0].id, "e1");
        assert!(matches!(&entries[0].kind, EntryKind::User { text } if text == "hello"));
    }

    #[test]
    fn event_application_is_idempotent() {
        let mut view = ViewModel::default();
        let repeat = event("e1", "r1", json!({"UserText": {"text": "hello"}}));
        view.apply_event(&repeat);
        view.apply_event(&repeat);
        assert_eq!(view.entries["s"].len(), 1);
    }

    #[test]
    fn errors_mark_session_failed() {
        let mut view = ViewModel {
            sessions: vec![Session {
                id: "s".into(),
                ..Session::default()
            }],
            ..ViewModel::default()
        };
        view.apply_event(&event(
            "e",
            "r1",
            json!({"Error": {"code": "provider_error", "message": "no key"}}),
        ));
        assert_eq!(view.sessions[0].status, SessionStatus::Error);
        assert!(matches!(
            &view.entries["s"][0].kind,
            EntryKind::Error { .. }
        ));
    }
}
