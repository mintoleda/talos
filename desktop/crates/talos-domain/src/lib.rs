//! Provider-neutral Talos desktop domain types and validated lifecycle transitions.
#![forbid(unsafe_code)]

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use std::{fmt, str::FromStr};
use thiserror::Error;
use uuid::Uuid;

#[derive(Debug, Error, PartialEq, Eq)]
pub enum ValidationError {
    #[error("value must not be empty")]
    Empty,
    #[error("value exceeds maximum length of {0} bytes")]
    TooLong(usize),
    #[error("invalid identifier: {0}")]
    InvalidId(String),
    #[error("invalid state transition from {from} to {to}")]
    InvalidTransition { from: String, to: String },
    #[error("event sequence must be greater than zero")]
    InvalidSequence,
    #[error("attachment size is zero")]
    EmptyAttachment,
}

macro_rules! id_type {
    ($($name:ident),+ $(,)?) => {$ (
        #[derive(Clone, Copy, Debug, Eq, Hash, Ord, PartialEq, PartialOrd, Serialize, Deserialize)]
        #[serde(transparent)]
        pub struct $name(Uuid);
        impl $name {
            #[must_use] pub fn new() -> Self { Self(Uuid::now_v7()) }
            #[must_use] pub const fn from_uuid(value: Uuid) -> Self { Self(value) }
            #[must_use] pub const fn as_uuid(&self) -> &Uuid { &self.0 }
        }
        impl Default for $name { fn default() -> Self { Self::new() } }
        impl fmt::Display for $name { fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result { self.0.fmt(f) } }
        impl FromStr for $name { type Err = ValidationError; fn from_str(s: &str) -> Result<Self, Self::Err> { Uuid::parse_str(s).map(Self).map_err(|_| ValidationError::InvalidId(s.to_owned())) } }
    )+};
}
id_type!(
    WorkspaceId,
    AgentId,
    SessionId,
    RunId,
    EventId,
    PermissionRequestId,
    AttachmentId,
    CommandId
);

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ProviderId(String);
impl ProviderId {
    pub fn new(value: impl Into<String>) -> Result<Self, ValidationError> {
        Self::bounded(value.into(), 128)
    }
    fn bounded(value: String, max: usize) -> Result<Self, ValidationError> {
        if value.trim().is_empty() {
            return Err(ValidationError::Empty);
        }
        if value.len() > max {
            return Err(ValidationError::TooLong(max));
        }
        Ok(Self(value))
    }
    #[must_use]
    pub fn as_str(&self) -> &str {
        &self.0
    }
}
impl fmt::Display for ProviderId {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ModelRef {
    pub provider: ProviderId,
    pub model_id: String,
}
impl ModelRef {
    pub fn new(provider: ProviderId, model_id: impl Into<String>) -> Result<Self, ValidationError> {
        let model_id = model_id.into();
        if model_id.trim().is_empty() {
            return Err(ValidationError::Empty);
        }
        if model_id.len() > 512 {
            return Err(ValidationError::TooLong(512));
        }
        Ok(Self { provider, model_id })
    }
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ProviderMode {
    pub id: String,
    pub label: String,
    pub description: Option<String>,
}
impl ProviderMode {
    pub fn new(id: impl Into<String>, label: impl Into<String>) -> Result<Self, ValidationError> {
        let id = id.into();
        let label = label.into();
        if id.trim().is_empty() || label.trim().is_empty() {
            return Err(ValidationError::Empty);
        }
        if id.len() > 128 {
            return Err(ValidationError::TooLong(128));
        }
        Ok(Self {
            id,
            label,
            description: None,
        })
    }
}

#[derive(Clone, Copy, Debug, Default, Eq, PartialEq, Serialize, Deserialize)]
#[allow(clippy::struct_excessive_bools)] // Independent negotiated capability flags.
pub struct Capabilities {
    pub streaming: bool,
    pub resume: bool,
    pub modes: bool,
    pub tools: bool,
    pub permissions: bool,
    pub images: bool,
    pub usage: bool,
    pub session_persistence: bool,
    pub audio: bool,
    pub model_selection: bool,
    pub config_options: bool,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ProviderDescriptor {
    pub id: ProviderId,
    pub display_name: String,
    pub available: bool,
    pub capabilities: Capabilities,
    pub modes: Vec<ProviderMode>,
    pub models: Vec<ModelRef>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub enum WorkspaceKind {
    Directory,
    LocalCheckout,
    Worktree,
}
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub enum WorkspaceLifecycle {
    Active,
    Archived,
    Deleting,
}
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Workspace {
    pub id: WorkspaceId,
    pub name: String,
    pub path: String,
    pub kind: WorkspaceKind,
    pub lifecycle: WorkspaceLifecycle,
    pub metadata: serde_json::Value,
    pub created_at: DateTime<Utc>,
    pub updated_at: DateTime<Utc>,
}
impl Workspace {
    pub fn new(
        name: impl Into<String>,
        path: impl Into<String>,
        kind: WorkspaceKind,
    ) -> Result<Self, ValidationError> {
        let name = nonempty(name.into(), 256)?;
        let path = nonempty(path.into(), 4096)?;
        let now = Utc::now();
        Ok(Self {
            id: WorkspaceId::new(),
            name,
            path,
            kind,
            lifecycle: WorkspaceLifecycle::Active,
            metadata: serde_json::json!({}),
            created_at: now,
            updated_at: now,
        })
    }
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub enum AgentLifecycle {
    Created,
    Ready,
    Busy,
    AttentionRequired,
    Stopping,
    Stopped,
    Failed,
    Archived,
}
impl AgentLifecycle {
    pub fn transition(&mut self, next: Self) -> Result<(), ValidationError> {
        let ok = matches!(
            (&*self, &next),
            (Self::Created, Self::Ready | Self::Failed | Self::Archived)
                | (
                    Self::Ready,
                    Self::Busy | Self::Stopping | Self::Failed | Self::Archived
                )
                | (
                    Self::Busy,
                    Self::Ready | Self::AttentionRequired | Self::Stopping | Self::Failed
                )
                | (
                    Self::AttentionRequired,
                    Self::Busy | Self::Ready | Self::Stopping | Self::Failed
                )
                | (Self::Stopping, Self::Stopped | Self::Failed)
                | (Self::Stopped | Self::Failed, Self::Ready | Self::Archived)
        );
        if !ok {
            return Err(transition_error(self, &next));
        }
        *self = next;
        Ok(())
    }
}
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Agent {
    pub id: AgentId,
    pub workspace_id: WorkspaceId,
    pub provider: ProviderId,
    pub model: Option<ModelRef>,
    pub mode: Option<String>,
    pub lifecycle: AgentLifecycle,
    pub title: String,
    pub config: serde_json::Value,
    pub created_at: DateTime<Utc>,
    pub updated_at: DateTime<Utc>,
}
impl Agent {
    pub fn new(
        workspace_id: WorkspaceId,
        provider: ProviderId,
        title: impl Into<String>,
    ) -> Result<Self, ValidationError> {
        let now = Utc::now();
        Ok(Self {
            id: AgentId::new(),
            workspace_id,
            provider,
            model: None,
            mode: None,
            lifecycle: AgentLifecycle::Created,
            title: nonempty(title.into(), 512)?,
            config: serde_json::json!({}),
            created_at: now,
            updated_at: now,
        })
    }
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub enum SessionLifecycle {
    New,
    Active,
    Suspended,
    Interrupted,
    Closed,
    Archived,
}
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub enum RunLifecycle {
    Queued,
    Starting,
    Running,
    CancelRequested,
    Succeeded,
    Failed,
    Cancelled,
    Interrupted,
}
impl RunLifecycle {
    pub fn transition(&mut self, next: Self) -> Result<(), ValidationError> {
        let ok = matches!(
            (&*self, &next),
            (Self::Queued, Self::Starting | Self::Cancelled)
                | (
                    Self::Starting,
                    Self::Running | Self::Failed | Self::Cancelled | Self::Interrupted
                )
                | (
                    Self::Running,
                    Self::CancelRequested
                        | Self::Succeeded
                        | Self::Failed
                        | Self::Cancelled
                        | Self::Interrupted
                )
                | (
                    Self::CancelRequested,
                    Self::Cancelled | Self::Succeeded | Self::Failed | Self::Interrupted
                )
        );
        if !ok {
            return Err(transition_error(self, &next));
        }
        *self = next;
        Ok(())
    }
}
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Session {
    pub id: SessionId,
    pub agent_id: AgentId,
    pub lifecycle: SessionLifecycle,
    pub provider_session_id: Option<String>,
    pub created_at: DateTime<Utc>,
    pub updated_at: DateTime<Utc>,
}
impl Session {
    #[must_use]
    pub fn new(agent_id: AgentId) -> Self {
        let now = Utc::now();
        Self {
            id: SessionId::new(),
            agent_id,
            lifecycle: SessionLifecycle::New,
            provider_session_id: None,
            created_at: now,
            updated_at: now,
        }
    }
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Usage {
    pub input_tokens: Option<u64>,
    pub output_tokens: Option<u64>,
    pub cached_read_tokens: Option<u64>,
    pub cached_write_tokens: Option<u64>,
    pub context_used: Option<u64>,
    pub context_size: Option<u64>,
    pub cost_micros: Option<u64>,
}
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ToolCall {
    pub id: String,
    pub name: String,
    pub title: Option<String>,
    pub input: serde_json::Value,
    pub output: Option<serde_json::Value>,
    pub status: ToolStatus,
}
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub enum ToolStatus {
    Running,
    Completed,
    Failed,
}
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Notice {
    pub code: Option<String>,
    pub message: String,
}
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct DomainError {
    pub code: Option<String>,
    pub message: String,
    pub retryable: bool,
}
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub enum TimelineEventKind {
    UserText {
        text: String,
    },
    AssistantText {
        text: String,
    },
    Thought {
        text: String,
    },
    Tool(ToolCall),
    Usage(Usage),
    Notice(Notice),
    Error(DomainError),
    PermissionRequested(PermissionRequest),
    PermissionResolved(PermissionOutcome),
    Attachment(Attachment),
    RunState {
        state: RunLifecycle,
    },
    Extension {
        kind: String,
        payload: serde_json::Value,
    },
}
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct TimelineEvent {
    pub id: EventId,
    pub session_id: SessionId,
    pub run_id: Option<RunId>,
    pub sequence: u64,
    pub timestamp: DateTime<Utc>,
    pub kind: TimelineEventKind,
}
impl TimelineEvent {
    pub fn new(
        session_id: SessionId,
        run_id: Option<RunId>,
        sequence: u64,
        kind: TimelineEventKind,
    ) -> Result<Self, ValidationError> {
        if sequence == 0 {
            return Err(ValidationError::InvalidSequence);
        }
        Ok(Self {
            id: EventId::new(),
            session_id,
            run_id,
            sequence,
            timestamp: Utc::now(),
            kind,
        })
    }
}
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct PermissionRequest {
    pub id: PermissionRequestId,
    pub run_id: RunId,
    pub action: String,
    pub target: Option<String>,
    pub risk: PermissionRisk,
    pub details: serde_json::Value,
    pub expires_at: Option<DateTime<Utc>>,
}
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub enum PermissionRisk {
    Low,
    Medium,
    High,
    Critical,
}
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub enum PermissionOutcome {
    Pending,
    Allowed { remember: bool },
    Denied,
    Expired,
    Cancelled,
}
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Attachment {
    pub id: AttachmentId,
    pub name: String,
    pub mime_type: String,
    pub size_bytes: u64,
    pub sha256: Option<String>,
    pub storage_key: String,
}
impl Attachment {
    pub fn new(
        name: impl Into<String>,
        mime_type: impl Into<String>,
        size_bytes: u64,
        storage_key: impl Into<String>,
    ) -> Result<Self, ValidationError> {
        if size_bytes == 0 {
            return Err(ValidationError::EmptyAttachment);
        }
        Ok(Self {
            id: AttachmentId::new(),
            name: nonempty(name.into(), 512)?,
            mime_type: nonempty(mime_type.into(), 256)?,
            size_bytes,
            sha256: None,
            storage_key: nonempty(storage_key.into(), 4096)?,
        })
    }
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct CommandEnvelope<T> {
    pub id: CommandId,
    pub issued_at: DateTime<Utc>,
    pub expected_sequence: Option<u64>,
    pub command: T,
}
impl<T> CommandEnvelope<T> {
    #[must_use]
    pub fn new(command: T) -> Self {
        Self {
            id: CommandId::new(),
            issued_at: Utc::now(),
            expected_sequence: None,
            command,
        }
    }
}
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct EventCursor {
    pub sequence: u64,
}

fn nonempty(value: String, max: usize) -> Result<String, ValidationError> {
    if value.trim().is_empty() {
        return Err(ValidationError::Empty);
    }
    if value.len() > max {
        return Err(ValidationError::TooLong(max));
    }
    Ok(value)
}
fn transition_error(from: &impl fmt::Debug, to: &impl fmt::Debug) -> ValidationError {
    ValidationError::InvalidTransition {
        from: format!("{from:?}"),
        to: format!("{to:?}"),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn ids_are_uuid_v7_and_round_trip() {
        let id = AgentId::new();
        assert_eq!(id.as_uuid().get_version_num(), 7);
        assert_eq!(id.to_string().parse::<AgentId>().unwrap(), id);
    }
    #[test]
    fn lifecycle_transitions_are_checked() {
        let mut s = RunLifecycle::Queued;
        s.transition(RunLifecycle::Starting).unwrap();
        s.transition(RunLifecycle::Running).unwrap();
        s.transition(RunLifecycle::Succeeded).unwrap();
        assert!(s.transition(RunLifecycle::Running).is_err());
    }
    #[test]
    fn event_sequence_and_values_are_validated() {
        assert_eq!(
            TimelineEvent::new(
                SessionId::new(),
                None,
                0,
                TimelineEventKind::UserText { text: "x".into() }
            )
            .unwrap_err(),
            ValidationError::InvalidSequence
        );
    }
}
