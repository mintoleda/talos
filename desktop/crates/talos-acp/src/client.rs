//! Bidirectional JSON-RPC client, ACP negotiation, and session operations.

use std::{
    collections::HashMap,
    future::Future,
    pin::Pin,
    sync::{
        Arc, Mutex, RwLock, Weak,
        atomic::{AtomicU64, Ordering},
    },
    time::Duration,
};

use serde::{Serialize, de::DeserializeOwned};
use serde_json::{Value, json};
use tokio::sync::{mpsc, oneshot, watch};
use tokio_util::sync::CancellationToken;

use crate::{
    ACP_PROTOCOL_VERSION, AcpError, ConfigOption, InitializeRequest, InitializeResponse, McpServer,
    McpTransport, PermissionDecision, PermissionRequest, ProcessConfig, ProcessStatus,
    PromptRequest, PromptResponse, RpcErrorObject, RpcId, SessionIdRequest, SessionListRequest,
    SessionListResponse, SessionLoadState, SessionState, SessionUpdateNotification,
    SetConfigOptionRequest, SetConfigOptionResponse, SetModeRequest, SetModelRequest,
    process::{ProcessIo, WriterCommand, spawn_process, wait_for_exit},
};

const EVENT_QUEUE_CAPACITY: usize = 256;
const MAX_REPLAY_UPDATES: usize = 4096;
const MAX_REPLAY_BYTES: usize = 64 * 1024 * 1024;

pub const TALOS_PROVIDER_ID: &str = "talos";

/// Preferred Talos provider metadata.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ProviderDefaults {
    pub provider_id: &'static str,
    pub command: String,
    pub args: Vec<String>,
}

pub fn talos_provider_defaults(executable: impl Into<String>) -> ProviderDefaults {
    ProviderDefaults {
        provider_id: TALOS_PROVIDER_ID,
        command: executable.into(),
        args: vec!["acp".to_owned()],
    }
}

pub type PermissionFuture<'a> = Pin<Box<dyn Future<Output = PermissionDecision> + Send + 'a>>;

/// The UI/application supplies permission decisions; the transport never
/// grants permission implicitly. Cancellation always resolves as denied.
pub trait PermissionHandler: Send + Sync + 'static {
    fn request_permission<'a>(
        &'a self,
        request: PermissionRequest,
        cancellation: CancellationToken,
    ) -> PermissionFuture<'a>;
}

struct RejectPermissions;

impl PermissionHandler for RejectPermissions {
    fn request_permission<'a>(
        &'a self,
        _request: PermissionRequest,
        _cancellation: CancellationToken,
    ) -> PermissionFuture<'a> {
        Box::pin(async { PermissionDecision::Cancelled })
    }
}

#[derive(Debug, Clone)]
pub enum AcpEvent {
    SessionUpdate(Box<SessionUpdateNotification>),
    /// All session/load replay updates are emitted atomically after a successful
    /// load response; consumers can replace rather than append their projection.
    SessionReplay {
        session_id: String,
        updates: Vec<SessionUpdateNotification>,
    },
    Extension {
        method: String,
        params: Value,
    },
    Diagnostic {
        message: String,
    },
}

struct PendingCall {
    sender: oneshot::Sender<Result<Value, AcpError>>,
    method: String,
    load_session_id: Option<String>,
}

#[derive(Default)]
struct SessionPreferences {
    modes: Vec<String>,
    models: Vec<String>,
    config_options: Vec<ConfigOption>,
}

#[derive(Default)]
struct LoadBuffer {
    updates: Vec<SessionUpdateNotification>,
    encoded_bytes: usize,
}

struct Shared {
    pending: Mutex<HashMap<RpcId, PendingCall>>,
    next_id: AtomicU64,
    writer: mpsc::Sender<WriterCommand>,
    events: mpsc::Sender<AcpEvent>,
    status: watch::Receiver<ProcessStatus>,
    kill: watch::Sender<bool>,
    cancellation: CancellationToken,
    stderr: Arc<crate::StderrTail>,
    capabilities: RwLock<Option<InitializeResponse>>,
    sessions: Mutex<HashMap<String, SessionPreferences>>,
    loading: Mutex<HashMap<String, LoadBuffer>>,
    permission_requests: Mutex<HashMap<String, Vec<(u64, CancellationToken)>>>,
    next_permission_id: AtomicU64,
    max_frame_size: usize,
    failure: Mutex<Option<AcpError>>,
    permission_handler: Arc<dyn PermissionHandler>,
}

impl Shared {
    fn fail(&self, error: AcpError, kill: bool) {
        let mut failure = self.failure.lock().expect("failure mutex poisoned");
        if failure.is_none() {
            *failure = Some(error.clone());
        }
        let error = failure.clone().expect("failure was just recorded");
        drop(failure);
        let pending = std::mem::take(&mut *self.pending.lock().expect("pending lock poisoned"));
        for (_, call) in pending {
            let _ = call.sender.send(Err(error.clone()));
        }
        self.cancellation.cancel();
        if kill {
            self.kill.send_replace(true);
        }
    }

    fn send_event(&self, event: AcpEvent) -> Result<(), AcpError> {
        match self.events.try_send(event) {
            Ok(()) => Ok(()),
            Err(mpsc::error::TrySendError::Full(_)) => Err(AcpError::EventQueueFull),
            Err(mpsc::error::TrySendError::Closed(_)) => Err(AcpError::ConnectionClosed),
        }
    }

    fn register_session_state(
        &self,
        id: String,
        modes: Option<Vec<String>>,
        models: Option<Vec<String>>,
        options: Vec<ConfigOption>,
    ) {
        let preferences = SessionPreferences {
            modes: modes.unwrap_or_default(),
            models: models.unwrap_or_default(),
            config_options: options,
        };
        self.sessions
            .lock()
            .expect("sessions lock poisoned")
            .insert(id, preferences);
    }

    fn buffer_update(&self, update: SessionUpdateNotification) -> Result<(), AcpError> {
        let id = update.session_id.clone();
        let encoded_bytes = serde_json::to_vec(&update).map_or(0, |json| json.len());
        let mut loading = self.loading.lock().expect("load buffer lock poisoned");
        if let Some(buffer) = loading.get_mut(&id) {
            if buffer.updates.len() >= MAX_REPLAY_UPDATES
                || buffer.encoded_bytes.saturating_add(encoded_bytes) > MAX_REPLAY_BYTES
            {
                return Err(AcpError::Protocol(format!(
                    "session/load replay exceeded bounded buffer for session {id}"
                )));
            }
            buffer.encoded_bytes += encoded_bytes;
            buffer.updates.push(update);
            return Ok(());
        }
        drop(loading);
        self.send_event(AcpEvent::SessionUpdate(Box::new(update)))
    }

    fn complete_load(&self, id: &str, success: bool) -> Result<(), AcpError> {
        let buffer = self
            .loading
            .lock()
            .expect("load buffer lock poisoned")
            .remove(id);
        if !success {
            return Ok(());
        }
        let updates = buffer.unwrap_or_default().updates;
        self.send_event(AcpEvent::SessionReplay {
            session_id: id.to_owned(),
            updates,
        })
    }

    fn abort_load(&self, id: &str) {
        self.loading
            .lock()
            .expect("load buffer lock poisoned")
            .remove(id);
    }

    fn register_permission(&self, session_id: &str) -> (u64, CancellationToken) {
        let id = self.next_permission_id.fetch_add(1, Ordering::Relaxed);
        let token = self.cancellation.child_token();
        self.permission_requests
            .lock()
            .expect("permission lock poisoned")
            .entry(session_id.to_owned())
            .or_default()
            .push((id, token.clone()));
        (id, token)
    }

    fn unregister_permission(&self, session_id: &str, id: u64) {
        let mut requests = self
            .permission_requests
            .lock()
            .expect("permission lock poisoned");
        if let Some(active) = requests.get_mut(session_id) {
            active.retain(|(request_id, _)| *request_id != id);
            if active.is_empty() {
                requests.remove(session_id);
            }
        }
    }

    fn cancel_permissions(&self, session_id: &str) {
        if let Some(active) = self
            .permission_requests
            .lock()
            .expect("permission lock poisoned")
            .get(session_id)
        {
            for (_, cancellation) in active {
                cancellation.cancel();
            }
        }
    }
}

/// A single ACP subprocess connection. Methods may be called concurrently;
/// session prompts are intentionally not serialized globally.
pub struct AcpClient {
    shared: Arc<Shared>,
    event_receiver: tokio::sync::Mutex<mpsc::Receiver<AcpEvent>>,
    initialize_lock: tokio::sync::Mutex<()>,
    process_tasks: Vec<tokio::task::JoinHandle<()>>,
}

impl AcpClient {
    pub async fn spawn(config: ProcessConfig) -> Result<Self, AcpError> {
        Self::spawn_with_permission_handler(config, Arc::new(RejectPermissions)).await
    }

    pub async fn spawn_with_permission_handler(
        config: ProcessConfig,
        permission_handler: Arc<dyn PermissionHandler>,
    ) -> Result<Self, AcpError> {
        let process = spawn_process(config).await?;
        Self::from_process(process, permission_handler)
    }

    fn from_process(
        process: ProcessIo,
        permission_handler: Arc<dyn PermissionHandler>,
    ) -> Result<Self, AcpError> {
        let ProcessIo {
            writer,
            mut frames,
            status,
            kill,
            cancellation,
            stderr,
            max_frame_size,
            _tasks,
        } = process;
        let (events, event_receiver) = mpsc::channel(EVENT_QUEUE_CAPACITY);
        let shared = Arc::new(Shared {
            pending: Mutex::new(HashMap::new()),
            next_id: AtomicU64::new(1),
            writer,
            events,
            status,
            kill,
            cancellation,
            stderr,
            capabilities: RwLock::new(None),
            sessions: Mutex::new(HashMap::new()),
            loading: Mutex::new(HashMap::new()),
            permission_requests: Mutex::new(HashMap::new()),
            next_permission_id: AtomicU64::new(1),
            max_frame_size,
            failure: Mutex::new(None),
            permission_handler,
        });
        let reader_shared = shared.clone();
        let reader_task = tokio::spawn(async move {
            while let Some(frame) = frames.recv().await {
                match frame {
                    Ok(bytes) => {
                        if let Err(error) = handle_frame(reader_shared.clone(), &bytes) {
                            reader_shared.fail(error, true);
                            break;
                        }
                    }
                    Err(AcpError::ConnectionClosed) => {
                        let error = terminal_error(&reader_shared).await;
                        reader_shared.fail(error, true);
                        break;
                    }
                    Err(error) => {
                        reader_shared.fail(error, true);
                        break;
                    }
                }
            }
        });
        let mut process_tasks = _tasks;
        process_tasks.push(reader_task);
        Ok(Self {
            shared,
            event_receiver: tokio::sync::Mutex::new(event_receiver),
            initialize_lock: tokio::sync::Mutex::new(()),
            process_tasks,
        })
    }

    /// Initialize once, retain all negotiated fields, then send `initialized`.
    pub async fn initialize(&self) -> Result<InitializeResponse, AcpError> {
        if let Some(capabilities) = self.capabilities() {
            return Ok(capabilities);
        }
        let _guard = self.initialize_lock.lock().await;
        if let Some(capabilities) = self.capabilities() {
            return Ok(capabilities);
        }
        let response: InitializeResponse = self
            .call("initialize", &InitializeRequest::default())
            .await?;
        if response.protocol_version != ACP_PROTOCOL_VERSION {
            return Err(AcpError::UnsupportedProtocolVersion {
                actual: response.protocol_version,
                expected: ACP_PROTOCOL_VERSION,
            });
        }
        self.notify("initialized", &json!({})).await?;
        *self
            .shared
            .capabilities
            .write()
            .expect("capability lock poisoned") = Some(response.clone());
        Ok(response)
    }

    pub fn capabilities(&self) -> Option<InitializeResponse> {
        self.shared
            .capabilities
            .read()
            .expect("capability lock poisoned")
            .clone()
    }

    pub fn status(&self) -> ProcessStatus {
        self.shared.status.borrow().clone()
    }

    pub fn stderr_tail(&self) -> String {
        self.shared.stderr.tail()
    }

    /// Receive protocol updates and diagnostics. There is one ordered consumer.
    pub async fn next_event(&self) -> Option<AcpEvent> {
        let mut receiver = self.event_receiver.lock().await;
        // Keep the watch receiver's seen version across iterations. Re-cloning
        // the original receiver after a nonterminal transition re-observes the
        // same change forever and busy-spins whenever the event queue is empty.
        let mut status = self.shared.status.clone();
        loop {
            match receiver.try_recv() {
                Ok(event) => return Some(event),
                Err(mpsc::error::TryRecvError::Disconnected) => return None,
                Err(mpsc::error::TryRecvError::Empty) => {}
            }
            if status.borrow().is_terminal() {
                return receiver.try_recv().ok();
            }
            tokio::select! {
                event = receiver.recv() => return event,
                changed = status.changed() => {
                    if changed.is_err() || status.borrow().is_terminal() {
                        return receiver.try_recv().ok();
                    }
                }
            }
        }
    }

    pub async fn new_session(
        &self,
        cwd: impl AsRef<str>,
        mcp_servers: &[McpServer],
    ) -> Result<SessionState, AcpError> {
        self.ensure_initialized().await?;
        self.validate_mcp(mcp_servers)?;
        self.register_mcp_secrets(mcp_servers);
        let request = json!({ "cwd": cwd.as_ref(), "mcpServers": mcp_servers });
        self.call("session/new", &request).await
    }

    pub async fn load_session(
        &self,
        session_id: &str,
        cwd: impl AsRef<str>,
        mcp_servers: &[McpServer],
    ) -> Result<SessionLoadState, AcpError> {
        self.ensure_initialized().await?;
        let capabilities = self.capabilities().expect("initialized capabilities");
        if !capabilities.agent_capabilities.load_session {
            return Err(AcpError::UnsupportedCapability("session/load"));
        }
        self.validate_mcp(mcp_servers)?;
        self.register_mcp_secrets(mcp_servers);
        {
            let mut loading = self
                .shared
                .loading
                .lock()
                .expect("load buffer lock poisoned");
            if loading.contains_key(session_id) {
                return Err(AcpError::Protocol(format!(
                    "session {session_id} is already loading"
                )));
            }
            loading.insert(session_id.to_owned(), LoadBuffer::default());
        }
        let request =
            json!({ "sessionId": session_id, "cwd": cwd.as_ref(), "mcpServers": mcp_servers });
        match self
            .call_with_load("session/load", &request, session_id)
            .await
        {
            Ok(response) => Ok(response),
            Err(error) => {
                self.shared.abort_load(session_id);
                Err(error)
            }
        }
    }

    pub async fn list_sessions(
        &self,
        cwd: Option<&str>,
        cursor: Option<&str>,
    ) -> Result<SessionListResponse, AcpError> {
        self.ensure_initialized().await?;
        let capabilities = self.capabilities().expect("initialized capabilities");
        if capabilities
            .agent_capabilities
            .session_capabilities
            .as_ref()
            .and_then(|caps| caps.list.as_ref())
            .is_none()
        {
            return Err(AcpError::UnsupportedCapability("session/list"));
        }
        self.call("session/list", &SessionListRequest { cwd, cursor })
            .await
    }

    pub async fn close_session(&self, session_id: &str) -> Result<(), AcpError> {
        self.ensure_initialized().await?;
        let capabilities = self.capabilities().expect("initialized capabilities");
        if capabilities
            .agent_capabilities
            .session_capabilities
            .as_ref()
            .and_then(|caps| caps.close.as_ref())
            .is_none()
        {
            return Err(AcpError::UnsupportedCapability("session/close"));
        }
        self.shared.cancel_permissions(session_id);
        self.call_value("session/close", json!({ "sessionId": session_id }))
            .await?;
        self.shared
            .sessions
            .lock()
            .expect("sessions lock poisoned")
            .remove(session_id);
        Ok(())
    }

    pub async fn prompt(
        &self,
        session_id: &str,
        prompt: &[crate::ContentBlock],
    ) -> Result<PromptResponse, AcpError> {
        self.ensure_initialized().await?;
        self.validate_prompt(prompt)?;
        self.call("session/prompt", &PromptRequest { session_id, prompt })
            .await
    }

    /// Cancel the server-side turn. ACP defines this as a notification.
    pub async fn cancel_session(&self, session_id: &str) -> Result<(), AcpError> {
        self.ensure_initialized().await?;
        self.shared.cancel_permissions(session_id);
        self.notify("session/cancel", &SessionIdRequest { session_id })
            .await
    }

    /// Cancel a prompt via token, but keep reading updates and await its final
    /// response (normally `stopReason: cancelled`).
    pub async fn prompt_cancellable(
        &self,
        session_id: &str,
        prompt: &[crate::ContentBlock],
        cancellation: &CancellationToken,
    ) -> Result<PromptResponse, AcpError> {
        self.ensure_initialized().await?;
        self.validate_prompt(prompt)?;
        let value = serde_json::to_value(PromptRequest { session_id, prompt })
            .map_err(|error| AcpError::Protocol(error.to_string()))?;
        let response = self
            .call_cancellable("session/prompt", value, cancellation, Some(session_id))
            .await?;
        serde_json::from_value(response).map_err(|error| AcpError::Protocol(error.to_string()))
    }

    pub async fn set_mode(&self, session_id: &str, mode_id: &str) -> Result<(), AcpError> {
        self.ensure_initialized().await?;
        let supported = self.session_preferences(session_id)?.modes;
        if !supported.iter().any(|mode| mode == mode_id) {
            return Err(AcpError::Protocol(format!(
                "mode {mode_id:?} was not advertised for session {session_id}"
            )));
        }
        self.call_value(
            "session/set_mode",
            serde_json::to_value(SetModeRequest {
                session_id,
                mode_id,
            })
            .map_err(protocol_serialize)?,
        )
        .await?;
        Ok(())
    }

    pub async fn set_model(&self, session_id: &str, model_id: &str) -> Result<(), AcpError> {
        self.ensure_initialized().await?;
        let supported = self.session_preferences(session_id)?.models;
        if !supported.iter().any(|model| model == model_id) {
            return Err(AcpError::Protocol(format!(
                "model {model_id:?} was not advertised for session {session_id}"
            )));
        }
        self.call_value(
            "session/set_model",
            serde_json::to_value(SetModelRequest {
                session_id,
                model_id,
            })
            .map_err(protocol_serialize)?,
        )
        .await?;
        Ok(())
    }

    pub async fn set_config_option(
        &self,
        session_id: &str,
        config_id: &str,
        value: &str,
    ) -> Result<SetConfigOptionResponse, AcpError> {
        self.ensure_initialized().await?;
        let options = self.session_preferences(session_id)?.config_options;
        let option = options
            .iter()
            .find(|option| option.id == config_id)
            .ok_or_else(|| {
                AcpError::Protocol(format!(
                    "config option {config_id:?} was not advertised for session {session_id}"
                ))
            })?;
        if !option.options.is_empty()
            && !option
                .options
                .iter()
                .any(|supported| supported.value == value)
        {
            return Err(AcpError::Protocol(format!(
                "value {value:?} was not advertised for config option {config_id}"
            )));
        }
        let request = SetConfigOptionRequest {
            session_id,
            config_id,
            value,
        };
        let response: SetConfigOptionResponse =
            self.call("session/set_config_option", &request).await?;
        if let Some(session) = self
            .shared
            .sessions
            .lock()
            .expect("sessions lock poisoned")
            .get_mut(session_id)
        {
            session.config_options = response.config_options.clone();
        }
        Ok(response)
    }

    pub async fn shutdown(&self, grace: Duration) -> ProcessStatus {
        let deadline = tokio::time::Instant::now() + grace;
        let supports_close = self.capabilities().is_some_and(|capabilities| {
            capabilities
                .agent_capabilities
                .session_capabilities
                .as_ref()
                .and_then(|caps| caps.close.as_ref())
                .is_some()
        });
        if supports_close {
            let session_ids: Vec<_> = self
                .shared
                .sessions
                .lock()
                .expect("sessions lock poisoned")
                .keys()
                .cloned()
                .collect();
            for session_id in session_ids {
                if tokio::time::Instant::now() >= deadline {
                    break;
                }
                let _ = tokio::time::timeout_at(deadline, self.close_session(&session_id)).await;
            }
        }
        let _ =
            tokio::time::timeout_at(deadline, self.shared.writer.send(WriterCommand::Close)).await;
        self.shared.cancellation.cancel();
        let remaining = deadline.saturating_duration_since(tokio::time::Instant::now());
        let mut status = self.shared.status.clone();
        wait_for_exit(&mut status, remaining, &self.shared.kill).await
    }

    async fn ensure_initialized(&self) -> Result<(), AcpError> {
        self.initialize().await.map(|_| ())
    }

    fn session_preferences(&self, session_id: &str) -> Result<SessionPreferences, AcpError> {
        self.shared
            .sessions
            .lock()
            .expect("sessions lock poisoned")
            .get(session_id)
            .map(|prefs| SessionPreferences {
                modes: prefs.modes.clone(),
                models: prefs.models.clone(),
                config_options: prefs.config_options.clone(),
            })
            .ok_or_else(|| AcpError::Protocol(format!("unknown ACP session {session_id}")))
    }

    fn validate_prompt(&self, prompt: &[crate::ContentBlock]) -> Result<(), AcpError> {
        let caps = self
            .capabilities()
            .expect("initialized capabilities")
            .agent_capabilities
            .prompt_capabilities;
        for block in prompt {
            match block {
                crate::ContentBlock::Image { .. } if !caps.image => {
                    return Err(AcpError::UnsupportedCapability("prompt image"));
                }
                crate::ContentBlock::Audio { .. } if !caps.audio => {
                    return Err(AcpError::UnsupportedCapability("prompt audio"));
                }
                crate::ContentBlock::Resource { .. } if !caps.embedded_context => {
                    return Err(AcpError::UnsupportedCapability("embedded context"));
                }
                _ => {}
            }
        }
        Ok(())
    }

    fn validate_mcp(&self, servers: &[McpServer]) -> Result<(), AcpError> {
        let caps = self
            .capabilities()
            .expect("initialized capabilities")
            .agent_capabilities
            .mcp_capabilities;
        for server in servers {
            match server.transport {
                Some(McpTransport::Http) if !caps.http => {
                    return Err(AcpError::UnsupportedCapability("MCP HTTP"));
                }
                Some(McpTransport::Sse) if !caps.sse => {
                    return Err(AcpError::UnsupportedCapability("MCP SSE"));
                }
                _ => {}
            }
        }
        Ok(())
    }

    fn register_mcp_secrets(&self, servers: &[McpServer]) {
        for server in servers {
            for env in &server.env {
                self.shared.stderr.add_secret(env.value.clone());
            }
            for header in &server.headers {
                self.shared.stderr.add_secret(header.value.clone());
            }
        }
    }

    async fn call<P, R>(&self, method: &str, params: &P) -> Result<R, AcpError>
    where
        P: Serialize + ?Sized,
        R: DeserializeOwned,
    {
        let value = serde_json::to_value(params).map_err(protocol_serialize)?;
        let result = self.call_value(method, value).await?;
        serde_json::from_value(result).map_err(|error| AcpError::Protocol(error.to_string()))
    }

    async fn call_value(&self, method: &str, params: Value) -> Result<Value, AcpError> {
        self.call_value_inner(method, params, None, None).await
    }

    async fn call_with_load<R: DeserializeOwned>(
        &self,
        method: &str,
        params: &Value,
        session_id: &str,
    ) -> Result<R, AcpError> {
        let result = self
            .call_value_inner(method, params.clone(), None, Some(session_id))
            .await?;
        serde_json::from_value(result).map_err(|error| AcpError::Protocol(error.to_string()))
    }

    async fn call_cancellable(
        &self,
        method: &str,
        params: Value,
        cancellation: &CancellationToken,
        cancel_session: Option<&str>,
    ) -> Result<Value, AcpError> {
        self.call_value_inner(method, params, Some(cancellation), cancel_session)
            .await
    }

    async fn call_value_inner(
        &self,
        method: &str,
        params: Value,
        cancellation: Option<&CancellationToken>,
        cancel_session: Option<&str>,
    ) -> Result<Value, AcpError> {
        if let Some(error) = self
            .shared
            .failure
            .lock()
            .expect("failure mutex poisoned")
            .clone()
        {
            return Err(error);
        }
        let raw_id = self.shared.next_id.fetch_add(1, Ordering::Relaxed);
        let id = RpcId::Integer(
            i64::try_from(raw_id)
                .map_err(|_| AcpError::Protocol("JSON-RPC request ID exhausted".into()))?,
        );
        self.call_with_id_inner(id, method, params, cancellation, cancel_session, None)
            .await
    }

    /// Low-level request for peers/fixtures that require a caller-selected ID.
    pub async fn call_with_id<R: DeserializeOwned>(
        &self,
        id: RpcId,
        method: &str,
        params: Value,
    ) -> Result<R, AcpError> {
        let result = self
            .call_with_id_inner(id, method, params, None, None, None)
            .await?;
        serde_json::from_value(result).map_err(|error| AcpError::Protocol(error.to_string()))
    }

    async fn call_with_id_inner(
        &self,
        id: RpcId,
        method: &str,
        params: Value,
        cancellation: Option<&CancellationToken>,
        cancel_session: Option<&str>,
        _unused: Option<()>,
    ) -> Result<Value, AcpError> {
        let (sender, mut receiver) = oneshot::channel();
        let load_session_id = if method == "session/load" {
            params
                .get("sessionId")
                .and_then(Value::as_str)
                .map(str::to_owned)
        } else {
            None
        };
        {
            let mut pending = self.shared.pending.lock().expect("pending lock poisoned");
            if pending.contains_key(&id) {
                return Err(AcpError::DuplicateRpcId(format_rpc_id(&id)));
            }
            pending.insert(
                id.clone(),
                PendingCall {
                    sender,
                    method: method.to_owned(),
                    load_session_id,
                },
            );
        }
        let mut guard = PendingGuard {
            shared: Arc::downgrade(&self.shared),
            id: id.clone(),
            armed: true,
        };
        let request = json!({ "jsonrpc": "2.0", "id": id, "method": method, "params": params });
        if let Err(error) = self.write_json(request).await {
            guard.remove();
            return Err(error);
        }

        if let Some(cancellation) = cancellation {
            tokio::select! {
                result = &mut receiver => {
                    guard.armed = false;
                    return receive_response(result);
                }
                _ = cancellation.cancelled() => {
                    if let Some(session_id) = cancel_session {
                        self.shared.cancel_permissions(session_id);
                        self.notify("session/cancel", &SessionIdRequest { session_id }).await?;
                    }
                    let result = receiver.await;
                    guard.armed = false;
                    return receive_response(result);
                }
            }
        }
        let result = receiver.await;
        guard.armed = false;
        receive_response(result)
    }

    async fn notify<P: Serialize + ?Sized>(
        &self,
        method: &str,
        params: &P,
    ) -> Result<(), AcpError> {
        let params = serde_json::to_value(params).map_err(protocol_serialize)?;
        let message = json!({ "jsonrpc": "2.0", "method": method, "params": params });
        self.write_json(message).await
    }

    async fn write_json(&self, value: Value) -> Result<(), AcpError> {
        if !value.is_object() {
            return Err(AcpError::Protocol(
                "outgoing JSON-RPC message must be an object".into(),
            ));
        }
        let mut bytes = serde_json::to_vec(&value).map_err(protocol_serialize)?;
        if bytes.len() > self.shared.max_frame_size {
            return Err(AcpError::FrameTooLarge {
                limit: self.shared.max_frame_size,
            });
        }
        bytes.push(b'\n');
        tokio::select! {
            result = self.shared.writer.send(WriterCommand::Frame(bytes)) => {
                result.map_err(|_| self.closed_error())
            }
            _ = self.shared.cancellation.cancelled() => Err(self.closed_error()),
        }
    }

    fn closed_error(&self) -> AcpError {
        self.shared
            .failure
            .lock()
            .expect("failure mutex poisoned")
            .clone()
            .unwrap_or(AcpError::ConnectionClosed)
    }
}

impl Drop for AcpClient {
    fn drop(&mut self) {
        self.shared.cancellation.cancel();
        self.shared.kill.send_replace(true);
        // Dropping JoinHandles detaches the supervisor. It remains responsible
        // for waiting/reaping the child after the kill request.
        self.process_tasks.clear();
    }
}

struct PendingGuard {
    shared: Weak<Shared>,
    id: RpcId,
    armed: bool,
}

impl PendingGuard {
    fn remove(&mut self) {
        if !self.armed {
            return;
        }
        self.armed = false;
        if let Some(shared) = self.shared.upgrade() {
            let pending = shared
                .pending
                .lock()
                .expect("pending lock poisoned")
                .remove(&self.id);
            if let Some(call) = pending
                && let Some(session_id) = call.load_session_id
            {
                shared.abort_load(&session_id);
            }
        }
    }
}

impl Drop for PendingGuard {
    fn drop(&mut self) {
        self.remove();
    }
}

fn receive_response(
    result: Result<Result<Value, AcpError>, oneshot::error::RecvError>,
) -> Result<Value, AcpError> {
    result.map_err(|_| AcpError::ConnectionClosed)?
}

fn protocol_serialize(error: serde_json::Error) -> AcpError {
    AcpError::Protocol(error.to_string())
}

fn format_rpc_id(id: &RpcId) -> String {
    match id {
        RpcId::Integer(value) => value.to_string(),
        RpcId::Unsigned(value) => value.to_string(),
        RpcId::String(value) => format!("{value:?}"),
    }
}

async fn terminal_error(shared: &Shared) -> AcpError {
    let mut status = shared.status.clone();
    if !status.borrow().is_terminal() {
        let _ = tokio::time::timeout(Duration::from_millis(100), async {
            while !status.borrow().is_terminal() {
                if status.changed().await.is_err() {
                    break;
                }
            }
        })
        .await;
    }
    match status.borrow().clone() {
        ProcessStatus::Exited { code } | ProcessStatus::Failed { code, .. } => {
            AcpError::ProcessExited {
                code,
                stderr: shared.stderr.tail(),
            }
        }
        _ => AcpError::ConnectionClosed,
    }
}

fn handle_frame(shared: Arc<Shared>, bytes: &[u8]) -> Result<(), AcpError> {
    let message: Value = serde_json::from_slice(bytes)
        .map_err(|error| AcpError::Protocol(format!("invalid JSON output: {error}")))?;
    let object = message
        .as_object()
        .ok_or_else(|| AcpError::Protocol("JSON-RPC message must be an object".into()))?;
    if object.get("jsonrpc").and_then(Value::as_str) != Some("2.0") {
        return Err(AcpError::Protocol(
            "JSON-RPC message has missing or invalid jsonrpc version".into(),
        ));
    }
    if let Some(method) = object.get("method") {
        let method = method
            .as_str()
            .filter(|method| !method.is_empty())
            .ok_or_else(|| AcpError::Protocol("JSON-RPC method must be a non-empty string".into()))?
            .to_owned();
        let id = object.get("id").map(parse_id).transpose()?;
        let params = object.get("params").cloned().unwrap_or(Value::Null);
        if let Some(id) = id {
            let shared = shared.clone();
            tokio::spawn(async move {
                dispatch_server_request(shared, id, method, params).await;
            });
        } else if method == "session/update" {
            match serde_json::from_value::<SessionUpdateNotification>(params.clone()) {
                Ok(update) => shared.buffer_update(update)?,
                Err(_) => shared.send_event(AcpEvent::Extension { method, params })?,
            }
        } else {
            shared.send_event(AcpEvent::Extension { method, params })?;
        }
        return Ok(());
    }

    let raw_id = object
        .get("id")
        .ok_or_else(|| AcpError::Protocol("JSON-RPC response has no id".into()))?;
    let id = parse_id(raw_id)?;
    let has_result = object.contains_key("result");
    let has_error = object.contains_key("error");
    if has_result == has_error {
        return Err(AcpError::Protocol(
            "JSON-RPC response must contain exactly one of result or error".into(),
        ));
    }
    let result = if has_error {
        let error: RpcErrorObject =
            serde_json::from_value(object["error"].clone()).map_err(|error| {
                AcpError::Protocol(format!("invalid JSON-RPC error object: {error}"))
            })?;
        Err(AcpError::RemoteRpc {
            code: error.code,
            message: error.message,
            data: error.data,
        })
    } else {
        Ok(object["result"].clone())
    };
    let pending = shared
        .pending
        .lock()
        .expect("pending lock poisoned")
        .remove(&id);
    let Some(pending) = pending else {
        shared.send_event(AcpEvent::Diagnostic {
            message: format!(
                "ignored response with unknown JSON-RPC id {}",
                format_rpc_id(&id)
            ),
        })?;
        return Ok(());
    };

    let is_load = pending.method == "session/load";
    let load_session_id = pending.load_session_id.clone();
    if let Ok(response_value) = &result {
        match pending.method.as_str() {
            "session/new" => {
                if let Ok(state) = serde_json::from_value::<SessionState>(response_value.clone()) {
                    shared.register_session_state(
                        state.session_id,
                        state.modes.map(|modes| {
                            modes
                                .available_modes
                                .into_iter()
                                .map(|mode| mode.id)
                                .collect()
                        }),
                        state.models.map(|models| {
                            models
                                .available_models
                                .into_iter()
                                .map(|model| model.model_id)
                                .collect()
                        }),
                        state.config_options,
                    );
                }
            }
            "session/load" => {
                if let (Some(id), Ok(state)) = (
                    load_session_id.as_deref(),
                    serde_json::from_value::<SessionLoadState>(response_value.clone()),
                ) {
                    shared.register_session_state(
                        id.to_owned(),
                        state.modes.map(|modes| {
                            modes
                                .available_modes
                                .into_iter()
                                .map(|mode| mode.id)
                                .collect()
                        }),
                        state.models.map(|models| {
                            models
                                .available_models
                                .into_iter()
                                .map(|model| model.model_id)
                                .collect()
                        }),
                        state.config_options,
                    );
                }
            }
            _ => {}
        }
    }
    let load_success = result.is_ok();
    let _ = pending.sender.send(result);
    if is_load && let Some(session_id) = load_session_id {
        shared.complete_load(&session_id, load_success)?;
    }
    Ok(())
}

fn parse_id(value: &Value) -> Result<RpcId, AcpError> {
    match value {
        Value::String(value) => Ok(RpcId::String(value.clone())),
        Value::Number(number) if number.is_i64() => {
            Ok(RpcId::Integer(number.as_i64().expect("checked integer")))
        }
        Value::Number(number) if number.is_u64() => Ok(RpcId::Unsigned(
            number.as_u64().expect("checked unsigned integer"),
        )),
        _ => Err(AcpError::Protocol(
            "JSON-RPC id must be an integer or string".into(),
        )),
    }
}

async fn dispatch_server_request(shared: Arc<Shared>, id: RpcId, method: String, params: Value) {
    let response = if method == "session/request_permission" {
        match serde_json::from_value::<PermissionRequest>(params) {
            Ok(request) => {
                let session_id = request.session_id.clone();
                let (permission_id, cancellation) = shared.register_permission(&session_id);
                let permission = shared
                    .permission_handler
                    .request_permission(request, cancellation.clone());
                let decision = tokio::select! {
                    decision = permission => decision,
                    _ = cancellation.cancelled() => PermissionDecision::Cancelled,
                };
                shared.unregister_permission(&session_id, permission_id);
                match serde_json::to_value(decision) {
                    Ok(value) => json!({ "jsonrpc": "2.0", "id": id, "result": value }),
                    Err(error) => error_response(id, -32603, error.to_string()),
                }
            }
            Err(error) => {
                error_response(id, -32602, format!("invalid permission request: {error}"))
            }
        }
    } else {
        error_response(id, -32601, format!("method not found: {method}"))
    };
    if let Err(error) = write_server_message(&shared, response).await {
        shared.fail(error, true);
    }
}

fn error_response(id: RpcId, code: i64, message: String) -> Value {
    json!({ "jsonrpc": "2.0", "id": id, "error": { "code": code, "message": message } })
}

async fn write_server_message(shared: &Shared, message: Value) -> Result<(), AcpError> {
    let mut bytes = serde_json::to_vec(&message).map_err(protocol_serialize)?;
    bytes.push(b'\n');
    tokio::select! {
        result = shared.writer.send(WriterCommand::Frame(bytes)) => result.map_err(|_| AcpError::ConnectionClosed),
        _ = shared.cancellation.cancelled() => Err(AcpError::ConnectionClosed),
    }
}
