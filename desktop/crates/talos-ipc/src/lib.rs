//! Typed, versioned local daemon IPC protocol.
//!
//! Wire types are deliberately independent of the domain crates. Adapters can
//! implement [`TryFrom`] conversions at the daemon/UI boundary.
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::{collections::VecDeque, future::Future, io};
use thiserror::Error;
use tokio::io::{AsyncBufRead, AsyncBufReadExt, AsyncRead, AsyncWrite, AsyncWriteExt, BufReader};

pub const PROTOCOL_VERSION: u16 = 1;
pub const MAX_FRAME_SIZE: usize = 1024 * 1024;
const CLIENT_EVENT_QUEUE_LIMIT: usize = 1024;

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
#[serde(tag = "method", content = "params", rename_all = "snake_case")]
pub enum Command {
    Hello {
        min_version: u16,
        max_version: u16,
    },
    Health,
    Snapshot,
    ListWorkspaces,
    GetWorkspace {
        workspace_id: String,
    },
    CreateWorkspace {
        name: String,
        path: String,
    },
    RenameWorkspace {
        workspace_id: String,
        name: String,
    },
    ArchiveWorkspace {
        workspace_id: String,
    },
    RestoreWorkspace {
        workspace_id: String,
    },
    ListProviders,
    ProviderDiagnostics {
        provider_id: Option<String>,
    },
    ListAgents {
        workspace_id: String,
    },
    GetAgent {
        agent_id: String,
    },
    CreateAgent {
        workspace_id: String,
        provider_id: Option<String>,
        title: Option<String>,
    },
    ListSessions {
        workspace_id: Option<String>,
    },
    GetSession {
        session_id: String,
    },
    LoadSession {
        session_id: String,
    },
    Prompt {
        session_id: String,
        text: String,
        request_key: Option<String>,
    },
    Cancel {
        session_id: String,
    },
    PermissionDecision {
        permission_id: String,
        decision: PermissionDecision,
    },
    SetModel {
        session_id: String,
        model_id: String,
    },
    SetMode {
        session_id: String,
        mode_id: String,
    },
    SetConfigOption {
        session_id: String,
        option_id: String,
        value: String,
    },
    ListPendingPermissions,
    ListModels {
        provider_id: String,
    },
    ArchiveSession {
        session_id: String,
    },
    RenameSession {
        session_id: String,
        title: String,
    },
    SetThinking {
        session_id: String,
        thinking_option_id: String,
    },
    UsageSummary {
        session_id: Option<String>,
    },
    Shutdown,
    Replay {
        after: u64,
        limit: Option<u32>,
    },
    ReplaySession {
        session_id: String,
        after: u64,
        limit: Option<u32>,
    },
    /// Lossless multi-session pagination. A scalar sequence is only meaningful
    /// within one session, never as a global replay watermark.
    ReplaySessions {
        after: std::collections::BTreeMap<String, u64>,
        limit: Option<u32>,
    },
    SearchTimeline {
        query: String,
        session_id: Option<String>,
        limit: Option<u32>,
    },
}

#[derive(Clone, Copy, Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum PermissionDecision {
    Allow,
    Deny,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub struct Request {
    pub version: u16,
    pub id: u64,
    /// Optional per-install bearer capability; transport permissions remain mandatory.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub auth_token: Option<String>,
    pub command: Command,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub struct Response {
    pub version: u16,
    pub id: u64,
    #[serde(flatten)]
    pub outcome: RpcOutcome,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
#[serde(untagged)]
pub enum RpcOutcome {
    Success { result: Value },
    Failure { error: RpcError },
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct RpcError {
    pub code: i32,
    pub message: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub data: Option<Value>,
}

impl RpcError {
    pub fn new(code: i32, message: impl Into<String>) -> Self {
        Self {
            code,
            message: message.into(),
            data: None,
        }
    }
    #[must_use]
    pub fn version_mismatch(server: u16, client_min: u16, client_max: u16) -> Self {
        Self::new(
            -32001,
            format!(
                "no compatible protocol version (server {server}, client {client_min}..={client_max})"
            ),
        )
    }
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum ServerFrame {
    Response(Response),
    Event(EventNotification),
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub struct EventNotification {
    pub version: u16,
    pub cursor: u64,
    pub event_id: String,
    pub event: WireEvent,
}

impl EventNotification {
    /// Control notification, not a durable timeline event. Its zero cursor must
    /// never advance any session's replay position.
    #[must_use]
    pub fn replay_required(reason: &str, skipped: u64) -> Self {
        Self {
            version: PROTOCOL_VERSION,
            cursor: 0,
            event_id: String::new(),
            event: WireEvent {
                kind: "replay_required".into(),
                payload: serde_json::json!({"reason": reason, "skipped": skipped}),
            },
        }
    }
}

/// Extensible provider-neutral event payload; unknown event kinds remain forward-compatible.
#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub struct WireEvent {
    pub kind: String,
    #[serde(default)]
    pub payload: Value,
}

#[derive(Debug, Error)]
pub enum IpcError {
    #[error("I/O error: {0}")]
    Io(#[from] io::Error),
    #[error("invalid JSON frame: {0}")]
    Json(#[from] serde_json::Error),
    #[error("frame exceeds maximum size of {MAX_FRAME_SIZE} bytes")]
    FrameTooLarge,
    #[error("connection closed")]
    Closed,
    #[error("unexpected response id: expected {expected}, got {actual}")]
    UnexpectedId { expected: u64, actual: u64 },
    #[error("remote error {0}: {1}")]
    Remote(i32, String),
    #[error("protocol negotiation failed: {0}")]
    Negotiation(String),
}

/// Read one newline-delimited JSON frame without allocating beyond the frame limit.
pub async fn read_frame<R: AsyncBufRead + Unpin>(reader: &mut R) -> Result<Vec<u8>, IpcError> {
    read_frame_buffered(reader, &mut Vec::new()).await
}

// Partial bytes outlive a cancelled read when an event wins the select.
async fn read_frame_buffered<R: AsyncBufRead + Unpin>(
    reader: &mut R,
    frame: &mut Vec<u8>,
) -> Result<Vec<u8>, IpcError> {
    loop {
        let available = reader.fill_buf().await?;
        if available.is_empty() {
            return if frame.is_empty() {
                Err(IpcError::Closed)
            } else {
                Err(IpcError::Io(io::Error::new(
                    io::ErrorKind::UnexpectedEof,
                    "unterminated frame",
                )))
            };
        }
        let end = available
            .iter()
            .position(|byte| *byte == b'\n')
            .unwrap_or(available.len());
        if frame.len().saturating_add(end) > MAX_FRAME_SIZE {
            return Err(IpcError::FrameTooLarge);
        }
        frame.extend_from_slice(&available[..end]);
        let newline = end < available.len();
        reader.consume(end + usize::from(newline));
        if newline {
            if frame.last() == Some(&b'\r') {
                frame.pop();
            }
            return Ok(std::mem::take(frame));
        }
    }
}

pub async fn write_frame<W: AsyncWrite + Unpin, T: Serialize>(
    writer: &mut W,
    value: &T,
) -> Result<(), IpcError> {
    let encoded = serde_json::to_vec(value)?;
    if encoded.len() > MAX_FRAME_SIZE {
        return Err(IpcError::FrameTooLarge);
    }
    writer.write_all(&encoded).await?;
    writer.write_all(b"\n").await?;
    writer.flush().await?;
    Ok(())
}

/// Serve one client connection. The first frame must be a version-negotiation `Hello`.
pub async fn serve_connection<S, F, Fut>(
    stream: S,
    auth_token: Option<String>,
    handler: F,
) -> Result<(), IpcError>
where
    S: AsyncRead + AsyncWrite + Unpin,
    F: FnMut(Command) -> Fut,
    Fut: Future<Output = Result<Value, RpcError>>,
{
    serve_connection_inner(stream, auth_token, handler, None).await
}

/// Serve a connection and interleave durable-event notifications with responses.
///
/// Events use bounded broadcast delivery. A lagged receiver emits a
/// `replay_required` control event; recover using durable per-session cursors.
pub async fn serve_connection_with_events<S, F, Fut>(
    stream: S,
    auth_token: Option<String>,
    handler: F,
    events: tokio::sync::broadcast::Receiver<EventNotification>,
) -> Result<(), IpcError>
where
    S: AsyncRead + AsyncWrite + Unpin,
    F: FnMut(Command) -> Fut,
    Fut: Future<Output = Result<Value, RpcError>>,
{
    serve_connection_inner(stream, auth_token, handler, Some(events)).await
}

async fn serve_connection_inner<S, F, Fut>(
    stream: S,
    auth_token: Option<String>,
    mut handler: F,
    mut events: Option<tokio::sync::broadcast::Receiver<EventNotification>>,
) -> Result<(), IpcError>
where
    S: AsyncRead + AsyncWrite + Unpin,
    F: FnMut(Command) -> Fut,
    Fut: Future<Output = Result<Value, RpcError>>,
{
    let (read, mut write) = tokio::io::split(stream);
    let mut read = BufReader::new(read);
    let negotiated = tokio::time::timeout(
        std::time::Duration::from_secs(5),
        negotiate_connection(&mut read, &mut write, auth_token.as_deref()),
    )
    .await
    .map_err(|_| {
        IpcError::Io(io::Error::new(
            io::ErrorKind::TimedOut,
            "IPC handshake timed out",
        ))
    })??;
    if !negotiated {
        return Ok(());
    }
    let mut partial_frame = Vec::new();
    loop {
        match next_connection_item(&mut read, &mut partial_frame, &mut events).await? {
            None => return Ok(()),
            Some(ConnectionItem::Event(event)) => {
                if !write_connection_frame(&mut write, &ServerFrame::Event(event)).await? {
                    return Ok(());
                }
            }
            Some(ConnectionItem::Request(bytes)) => {
                let request: Request = serde_json::from_slice(&bytes)?;
                let response = dispatch_request(request, auth_token.as_deref(), &mut handler).await;
                if !write_connection_frame(&mut write, &ServerFrame::Response(response)).await? {
                    return Ok(());
                }
            }
        }
    }
}

async fn negotiate_connection<R, W>(
    reader: &mut BufReader<R>,
    writer: &mut W,
    auth_token: Option<&str>,
) -> Result<bool, IpcError>
where
    R: AsyncRead + Unpin,
    W: AsyncWrite + Unpin,
{
    let first: Request = serde_json::from_slice(&read_frame(reader).await?)?;
    if auth_token.is_some_and(|expected| first.auth_token.as_deref() != Some(expected)) {
        let response = Response {
            version: PROTOCOL_VERSION,
            id: first.id,
            outcome: RpcOutcome::Failure {
                error: RpcError::new(-32003, "authentication failed"),
            },
        };
        let _ = write_connection_frame(writer, &ServerFrame::Response(response)).await?;
        return Ok(false);
    }
    let Command::Hello {
        min_version,
        max_version,
    } = first.command
    else {
        return Err(IpcError::Negotiation("first request must be hello".into()));
    };
    let response = if (min_version..=max_version).contains(&PROTOCOL_VERSION) {
        Response {
            version: PROTOCOL_VERSION,
            id: first.id,
            outcome: RpcOutcome::Success {
                result: serde_json::json!({"version": PROTOCOL_VERSION}),
            },
        }
    } else {
        Response {
            version: PROTOCOL_VERSION,
            id: first.id,
            outcome: RpcOutcome::Failure {
                error: RpcError::version_mismatch(PROTOCOL_VERSION, min_version, max_version),
            },
        }
    };
    let accepted = matches!(response.outcome, RpcOutcome::Success { .. });
    if !write_connection_frame(writer, &ServerFrame::Response(response)).await? {
        return Ok(false);
    }
    Ok(accepted)
}

enum ConnectionItem {
    Request(Vec<u8>),
    Event(EventNotification),
}

async fn next_connection_item<R>(
    reader: &mut BufReader<R>,
    partial_frame: &mut Vec<u8>,
    events: &mut Option<tokio::sync::broadcast::Receiver<EventNotification>>,
) -> Result<Option<ConnectionItem>, IpcError>
where
    R: AsyncRead + Unpin,
{
    loop {
        if let Some(receiver) = events.as_mut() {
            tokio::select! {
                result = read_frame_buffered(reader, partial_frame) => return match result {
                    Ok(bytes) => Ok(Some(ConnectionItem::Request(bytes))),
                    Err(IpcError::Closed) => Ok(None),
                    Err(error) => Err(error),
                },
                notification = receiver.recv() => match notification {
                    Ok(event) => return Ok(Some(ConnectionItem::Event(event))),
                    Err(tokio::sync::broadcast::error::RecvError::Lagged(skipped)) => {
                        return Ok(Some(ConnectionItem::Event(EventNotification::replay_required(
                            "server_event_lag", skipped,
                        ))));
                    }
                    Err(tokio::sync::broadcast::error::RecvError::Closed) => {
                        *events = None;
                        continue;
                    }
                }
            }
        }
        return match read_frame_buffered(reader, partial_frame).await {
            Ok(bytes) => Ok(Some(ConnectionItem::Request(bytes))),
            Err(IpcError::Closed) => Ok(None),
            Err(error) => Err(error),
        };
    }
}

async fn dispatch_request<F, Fut>(
    request: Request,
    auth_token: Option<&str>,
    handler: &mut F,
) -> Response
where
    F: FnMut(Command) -> Fut,
    Fut: Future<Output = Result<Value, RpcError>>,
{
    let outcome = if request.version != PROTOCOL_VERSION {
        RpcOutcome::Failure {
            error: RpcError::new(-32001, "protocol version mismatch; reconnect and negotiate"),
        }
    } else if auth_token.is_some_and(|expected| request.auth_token.as_deref() != Some(expected)) {
        RpcOutcome::Failure {
            error: RpcError::new(-32003, "authentication failed"),
        }
    } else {
        match handler(request.command).await {
            Ok(result) => RpcOutcome::Success { result },
            Err(error) => RpcOutcome::Failure { error },
        }
    };
    Response {
        version: PROTOCOL_VERSION,
        id: request.id,
        outcome,
    }
}

async fn write_connection_frame<W, T>(writer: &mut W, frame: &T) -> Result<bool, IpcError>
where
    W: AsyncWrite + Unpin,
    T: Serialize,
{
    let result = tokio::time::timeout(
        std::time::Duration::from_secs(5),
        write_frame(writer, frame),
    )
    .await
    .map_err(|_| {
        IpcError::Io(io::Error::new(
            io::ErrorKind::TimedOut,
            "IPC peer stopped reading",
        ))
    })?;
    match result {
        Ok(()) => Ok(true),
        Err(IpcError::Io(error))
            if matches!(
                error.kind(),
                io::ErrorKind::BrokenPipe
                    | io::ErrorKind::ConnectionReset
                    | io::ErrorKind::NotConnected
            ) =>
        {
            Ok(false)
        }
        Err(error) => Err(error),
    }
}

/// A framed client for any async stream. Call `negotiate` before issuing commands.
pub struct Client<S> {
    reader: BufReader<tokio::io::ReadHalf<S>>,
    writer: tokio::io::WriteHalf<S>,
    next_id: u64,
    version: u16,
    auth_token: Option<String>,
    queued_events: VecDeque<ServerFrame>,
    partial_frame: Vec<u8>,
}
impl<S: AsyncRead + AsyncWrite + Unpin> Client<S> {
    pub fn new(stream: S, auth_token: Option<String>) -> Self {
        let (read, writer) = tokio::io::split(stream);
        Self {
            reader: BufReader::new(read),
            writer,
            next_id: 1,
            version: PROTOCOL_VERSION,
            auth_token,
            queued_events: VecDeque::new(),
            partial_frame: Vec::new(),
        }
    }
    async fn exchange(&mut self, version: u16, command: Command) -> Result<Response, IpcError> {
        let id = self.next_id;
        self.next_id = self.next_id.wrapping_add(1);
        write_frame(
            &mut self.writer,
            &Request {
                version,
                id,
                auth_token: self.auth_token.clone(),
                command,
            },
        )
        .await?;
        loop {
            let frame: ServerFrame = serde_json::from_slice(
                &read_frame_buffered(&mut self.reader, &mut self.partial_frame).await?,
            )?;
            match frame {
                ServerFrame::Event(event) => {
                    if self.queued_events.len() == CLIENT_EVENT_QUEUE_LIMIT {
                        // Keep the RPC response readable without silently losing
                        // timeline data: retain a bounded replay marker instead.
                        self.queued_events.clear();
                        self.queued_events.push_back(ServerFrame::Event(
                            EventNotification::replay_required("client_event_lag", 1),
                        ));
                    }
                    if self.queued_events.front().is_some_and(|frame| {
                        matches!(frame, ServerFrame::Event(event) if event.event.kind == "replay_required")
                    }) {
                        continue;
                    }
                    self.queued_events.push_back(ServerFrame::Event(event));
                }
                ServerFrame::Response(response) if response.id == id => return Ok(response),
                ServerFrame::Response(response) => {
                    return Err(IpcError::UnexpectedId {
                        expected: id,
                        actual: response.id,
                    });
                }
            }
        }
    }
    pub async fn negotiate(&mut self) -> Result<u16, IpcError> {
        let response = self
            .exchange(
                PROTOCOL_VERSION,
                Command::Hello {
                    min_version: PROTOCOL_VERSION,
                    max_version: PROTOCOL_VERSION,
                },
            )
            .await?;
        match response.outcome {
            RpcOutcome::Success { result } => {
                self.version =
                    u16::try_from(result.get("version").and_then(Value::as_u64).ok_or_else(
                        || IpcError::Negotiation("missing negotiated version".into()),
                    )?)
                    .map_err(|_| IpcError::Negotiation("negotiated version exceeds u16".into()))?;
                Ok(self.version)
            }
            RpcOutcome::Failure { error } => Err(IpcError::Remote(error.code, error.message)),
        }
    }
    pub async fn call(&mut self, command: Command) -> Result<Value, IpcError> {
        let response = self.exchange(self.version, command).await?;
        match response.outcome {
            RpcOutcome::Success { result } => Ok(result),
            RpcOutcome::Failure { error } => Err(IpcError::Remote(error.code, error.message)),
        }
    }
    pub async fn next_frame(&mut self) -> Result<ServerFrame, IpcError> {
        if let Some(frame) = self.queued_events.pop_front() {
            return Ok(frame);
        }
        Ok(serde_json::from_slice(
            &read_frame_buffered(&mut self.reader, &mut self.partial_frame).await?,
        )?)
    }
}

#[cfg(unix)]
pub mod unix {
    use super::{Client, Command, Future, IpcError, RpcError, Value, serve_connection};
    use tokio::net::{UnixListener, UnixStream};
    pub async fn connect(
        path: impl AsRef<std::path::Path>,
        auth_token: Option<String>,
    ) -> Result<Client<UnixStream>, IpcError> {
        Ok(Client::new(UnixStream::connect(path).await?, auth_token))
    }
    pub async fn serve<F, Fut>(
        listener: UnixListener,
        auth_token: Option<String>,
        handler: F,
    ) -> Result<(), IpcError>
    where
        F: FnMut(Command) -> Fut,
        Fut: Future<Output = Result<Value, RpcError>>,
    {
        let (stream, _) = listener.accept().await?;
        serve_connection(stream, auth_token, handler).await
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn interrupted_frame_read_preserves_consumed_prefix() {
        use tokio::io::{AsyncWriteExt, duplex};
        let (mut writer, reader) = duplex(256);
        let mut reader = BufReader::new(reader);
        let mut partial = Vec::new();
        writer.write_all(b"{\"method\":").await.unwrap();
        assert!(
            tokio::time::timeout(
                std::time::Duration::from_millis(10),
                read_frame_buffered(&mut reader, &mut partial),
            )
            .await
            .is_err()
        );
        assert_eq!(partial, b"{\"method\":");
        writer.write_all(b"\"health\"}\n").await.unwrap();
        let bytes = read_frame_buffered(&mut reader, &mut partial)
            .await
            .unwrap();
        assert_eq!(bytes, b"{\"method\":\"health\"}");
        assert_eq!(partial, Vec::<u8>::new());
    }

    #[tokio::test]
    async fn server_lag_is_explicit_and_closed_events_do_not_close_requests() {
        use tokio::io::{AsyncWriteExt, duplex};
        let (sender, receiver) = tokio::sync::broadcast::channel(1);
        let mut events = Some(receiver);
        sender
            .send(EventNotification::replay_required("first", 0))
            .unwrap();
        sender
            .send(EventNotification::replay_required("second", 0))
            .unwrap();
        let (mut writer, reader) = duplex(256);
        let mut reader = BufReader::new(reader);
        let mut partial = Vec::new();
        let item = next_connection_item(&mut reader, &mut partial, &mut events)
            .await
            .unwrap();
        assert!(matches!(item, Some(ConnectionItem::Event(event))
            if event.event.kind == "replay_required" && event.event.payload["reason"] == "server_event_lag"));
        // Drain the remaining notification, then close only the event source.
        let _ = next_connection_item(&mut reader, &mut partial, &mut events)
            .await
            .unwrap();
        drop(sender);
        writer.write_all(b"{}\n").await.unwrap();
        let item = next_connection_item(&mut reader, &mut partial, &mut events)
            .await
            .unwrap();
        assert!(matches!(item, Some(ConnectionItem::Request(bytes)) if bytes == b"{}"));
    }

    fn round_trip(command: &Command) {
        let encoded = serde_json::to_value(command).expect("command serializes");
        let decoded: Command = serde_json::from_value(encoded).expect("command deserializes");
        assert_eq!(command, &decoded);
    }

    #[test]
    fn new_commands_serde_round_trip() {
        round_trip(&Command::RenameWorkspace {
            workspace_id: "workspace-1".into(),
            name: "Renamed workspace".into(),
        });
        round_trip(&Command::ArchiveWorkspace {
            workspace_id: "workspace-1".into(),
        });
        round_trip(&Command::RestoreWorkspace {
            workspace_id: "workspace-1".into(),
        });
        round_trip(&Command::ListPendingPermissions);
        round_trip(&Command::ListModels {
            provider_id: "talos".into(),
        });
        round_trip(&Command::ArchiveSession {
            session_id: "session-1".into(),
        });
        round_trip(&Command::RenameSession {
            session_id: "session-1".into(),
            title: "New title".into(),
        });
        round_trip(&Command::SetThinking {
            session_id: "session-1".into(),
            thinking_option_id: "high".into(),
        });
        round_trip(&Command::UsageSummary { session_id: None });
        round_trip(&Command::UsageSummary {
            session_id: Some("session-1".into()),
        });
        round_trip(&Command::Shutdown);
        round_trip(&Command::ReplaySessions {
            after: [("session-1".into(), 4)].into(),
            limit: Some(10),
        });
        round_trip(&Command::SearchTimeline {
            query: "search term".into(),
            session_id: Some("session-1".into()),
            limit: Some(25),
        });
    }

    #[test]
    fn new_commands_use_stable_snake_case_method_names() {
        assert_eq!(
            serde_json::to_value(&Command::RenameWorkspace {
                workspace_id: "w".into(),
                name: "n".into(),
            })
            .unwrap()["method"],
            "rename_workspace"
        );
        assert_eq!(
            serde_json::to_value(&Command::ArchiveWorkspace {
                workspace_id: "w".into(),
            })
            .unwrap()["method"],
            "archive_workspace"
        );
        assert_eq!(
            serde_json::to_value(&Command::RestoreWorkspace {
                workspace_id: "w".into(),
            })
            .unwrap()["method"],
            "restore_workspace"
        );
        assert_eq!(
            serde_json::to_value(&Command::ListPendingPermissions).unwrap()["method"],
            "list_pending_permissions"
        );
        assert_eq!(
            serde_json::to_value(&Command::ListModels {
                provider_id: "talos".into()
            })
            .unwrap()["method"],
            "list_models"
        );
        assert_eq!(
            serde_json::to_value(&Command::ArchiveSession {
                session_id: "s".into()
            })
            .unwrap()["method"],
            "archive_session"
        );
        assert_eq!(
            serde_json::to_value(&Command::RenameSession {
                session_id: "s".into(),
                title: "t".into()
            })
            .unwrap()["method"],
            "rename_session"
        );
        assert_eq!(
            serde_json::to_value(&Command::SetThinking {
                session_id: "s".into(),
                thinking_option_id: "high".into()
            })
            .unwrap()["method"],
            "set_thinking"
        );
        assert_eq!(
            serde_json::to_value(&Command::UsageSummary { session_id: None }).unwrap()["method"],
            "usage_summary"
        );
        assert_eq!(
            serde_json::to_value(&Command::Shutdown).unwrap()["method"],
            "shutdown"
        );
        assert_eq!(
            serde_json::to_value(&Command::SearchTimeline {
                query: "search term".into(),
                session_id: None,
                limit: Some(25),
            })
            .unwrap()["method"],
            "search_timeline"
        );
    }
}
