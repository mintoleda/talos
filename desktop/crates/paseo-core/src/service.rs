//! Public application service and provider-neutral orchestration.

use std::{
    collections::{HashMap, HashSet},
    path::{Path, PathBuf},
    sync::{
        Arc, Mutex as StdMutex,
        atomic::{AtomicBool, Ordering},
    },
    time::Duration,
};

use chrono::Utc;
use paseo_acp::{AcpEvent, SessionUpdateNotification};
use paseo_domain::{
    Agent, AgentId, AgentLifecycle, DomainError, EventCursor, ModelRef, Notice, PermissionOutcome,
    PermissionRequest, PermissionRequestId, PermissionRisk, ProviderDescriptor, ProviderId,
    ProviderMode, RunId, RunLifecycle, Session, SessionId, SessionLifecycle, TimelineEvent,
    TimelineEventKind, ToolCall, ToolStatus, Usage, Workspace, WorkspaceId, WorkspaceKind,
    WorkspaceLifecycle,
};
use paseo_store::{PromptRequestAcceptance, PromptRequestRecord, SessionSnapshot};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use thiserror::Error;
use tokio::sync::{Mutex, RwLock, broadcast, oneshot, watch};
use tokio::task::JoinHandle;
use tokio_util::sync::CancellationToken;

use crate::{
    config::{self, AppConfig, ConfigError, HomePaths},
    provider::{
        self, AgentProvider, PermissionHandler, ProviderDiagnostic, ProviderError, ProviderEvent,
        ProviderModel, ProviderPermissionDecision, ProviderPermissionRequest, ProviderPromptResult,
        ProviderSession,
    },
    store_actor::StoreActor,
};

#[derive(Debug, Error)]
pub enum CoreError {
    #[error(transparent)]
    Config(#[from] ConfigError),
    #[error("filesystem operation failed: {0}")]
    Io(#[from] std::io::Error),
    #[error("SQLite operation failed: {0}")]
    Store(String),
    #[error(transparent)]
    Provider(#[from] ProviderError),
    #[error("provider {0} is not registered")]
    ProviderNotRegistered(String),
    #[error("provider {0} is disabled or unavailable")]
    ProviderUnavailable(String),
    #[error("workspace {0} was not found")]
    WorkspaceNotFound(WorkspaceId),
    #[error("agent {0} was not found")]
    AgentNotFound(AgentId),
    #[error("session {0} was not found")]
    SessionNotFound(SessionId),
    #[error("run {0} was not found")]
    RunNotFound(RunId),
    #[error("workspace path is not an existing directory: {0}")]
    InvalidWorkspacePath(String),
    #[error("workspace is archived and cannot start a new agent")]
    WorkspaceArchived,
    #[error("session {0} has no persisted provider-native session handle")]
    NativeSessionMissing(SessionId),
    #[error("session {0} is not loaded; call load_session before sending a prompt")]
    SessionNotLoaded(SessionId),
    #[error("session already has an active run")]
    SessionBusy,
    #[error("prompt request key must not be empty")]
    EmptyPromptRequestKey,
    #[error("prompt request key exceeds maximum length of {0} bytes")]
    PromptRequestKeyTooLong(usize),
    #[error(
        "prompt request key was already accepted with a different payload for session {session}"
    )]
    PromptRequestKeyConflict { session: SessionId },
    #[error("permission request {0} is not pending")]
    PermissionNotPending(PermissionRequestId),
    #[error("permission option {option:?} is not valid for request {request}")]
    InvalidPermissionOption {
        request: PermissionRequestId,
        option: String,
    },
    #[error("thinking option {option:?} is not valid for config option {config}")]
    InvalidThinkingOption { config: String, option: String },
    #[error("service is shutting down")]
    ShuttingDown,
    #[error("operation failed: {0}")]
    Other(String),
}

pub type CoreResult<T> = Result<T, CoreError>;

#[derive(Clone, Debug)]
pub struct CreatedAgent {
    pub agent: Agent,
    pub session: Session,
}

/// Outcome of a durably accepted prompt submission.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct AcceptedPrompt {
    pub run_id: RunId,
    pub state: RunLifecycle,
    /// True when an existing `request_key` record was replayed instead of
    /// starting a new provider run.
    pub restored: bool,
}

const MAX_PROMPT_REQUEST_KEY_BYTES: usize = 256;

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct PermissionOptionSummary {
    pub id: String,
    pub label: String,
    pub kind: Option<String>,
}

/// A pending permission request with enough context for the UI to render a card.
#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub struct PendingPermissionSummary {
    pub id: PermissionRequestId,
    pub session_id: SessionId,
    pub run_id: RunId,
    pub action: String,
    pub target: Option<String>,
    pub risk: PermissionRisk,
    pub details: Value,
}

#[derive(Clone, Copy, Debug, Default, Serialize, Deserialize, PartialEq, Eq)]
pub struct UsageTotals {
    pub input_tokens: u64,
    pub output_tokens: u64,
    pub cached_read_tokens: u64,
    pub cached_write_tokens: u64,
    pub cost_micros: u64,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq, Eq)]
pub struct SessionUsage {
    pub session_id: SessionId,
    pub input_tokens: u64,
    pub output_tokens: u64,
    pub cached_read_tokens: u64,
    pub cached_write_tokens: u64,
    pub cost_micros: u64,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq, Eq)]
pub struct UsageSummary {
    pub sessions: Vec<SessionUsage>,
    pub totals: UsageTotals,
}

struct PendingPermission {
    session_id: SessionId,
    run_id: RunId,
    options: HashSet<String>,
    option_kinds: HashMap<String, Option<String>>,
    summary: PendingPermissionSummary,
    reply: oneshot::Sender<Option<String>>,
}

struct SessionRuntime {
    session: Session,
    agent: Agent,
    provider: Arc<dyn ProviderSession>,
    active_run: Arc<StdMutex<Option<RunId>>>,
    event_task: Mutex<Option<JoinHandle<()>>>,
}

struct RunControl {
    session_id: SessionId,
    cancellation: CancellationToken,
    state: Mutex<RunLifecycle>,
    state_tx: watch::Sender<RunLifecycle>,
}

struct Inner {
    paths: HomePaths,
    config: RwLock<AppConfig>,
    store: StoreActor,
    providers: RwLock<HashMap<String, Arc<dyn AgentProvider>>>,
    sessions: RwLock<HashMap<SessionId, Arc<SessionRuntime>>>,
    runs: RwLock<HashMap<RunId, Arc<RunControl>>>,
    run_tasks: Mutex<HashMap<RunId, JoinHandle<()>>>,
    run_start_gate: Mutex<()>,
    permission_start_gate: Mutex<()>,
    permissions: Mutex<HashMap<PermissionRequestId, PendingPermission>>,
    agent_updates: Mutex<()>,
    sequence: Mutex<HashMap<SessionId, u64>>,
    events: broadcast::Sender<TimelineEvent>,
    shutdown: CancellationToken,
    shutdown_done: watch::Sender<bool>,
    shutting_down: AtomicBool,
}

#[derive(Clone)]
pub struct AppService {
    inner: Arc<Inner>,
}

impl AppService {
    /// Open the new Rust-owned home (`TALOSD_HOME` or `~/.talosd`).
    pub async fn open() -> CoreResult<Self> {
        let paths = HomePaths::discover()?;
        Self::open_at(paths.home).await
    }

    /// Open an explicitly selected Rust-owned home. This never reads or writes `~/.paseo`.
    pub async fn open_at(home: impl Into<PathBuf>) -> CoreResult<Self> {
        let paths = HomePaths::at(home);
        paths.validate()?;
        tokio::fs::create_dir_all(&paths.home).await?;
        set_private_directory_permissions(&paths.home).await?;
        let config = config::load_or_create(&paths.config).await?;
        let store = StoreActor::open(paths.database.clone())
            .await
            .map_err(CoreError::Store)?;
        let providers = build_providers(&config)?;
        let (events, _) = broadcast::channel(config.event_capacity);
        let (shutdown_done, _) = watch::channel(false);
        let service = Self {
            inner: Arc::new(Inner {
                paths,
                config: RwLock::new(config),
                store,
                providers: RwLock::new(providers),
                sessions: RwLock::new(HashMap::new()),
                runs: RwLock::new(HashMap::new()),
                run_tasks: Mutex::new(HashMap::new()),
                run_start_gate: Mutex::new(()),
                permission_start_gate: Mutex::new(()),
                permissions: Mutex::new(HashMap::new()),
                agent_updates: Mutex::new(()),
                sequence: Mutex::new(HashMap::new()),
                events,
                shutdown: CancellationToken::new(),
                shutdown_done,
                shutting_down: AtomicBool::new(false),
            }),
        };
        service.recover_durable_state().await?;
        Ok(service)
    }

    /// Reconcile timeline-only run state before the service accepts new work.
    /// Any run without a durable terminal state is marked Interrupted; permission
    /// requests without a recorded resolution are durably cancelled.
    async fn recover_durable_state(&self) -> CoreResult<()> {
        let sessions = self.all_sessions().await?;
        let mut recovered_runs = HashMap::new();
        let mut updated_agents = HashSet::new();

        for session in sessions {
            let mut cursor = None;
            let mut run_states = HashMap::<RunId, RunLifecycle>::new();
            let mut permission_counts = HashMap::<RunId, (u64, u64)>::new();
            loop {
                let page = self
                    .inner
                    .store
                    .run(move |store| store.replay_events(session.id, cursor, 10_000))
                    .await
                    .map_err(CoreError::Store)?;
                let count = page.len();
                for event in &page {
                    let Some(run_id) = event.run_id else {
                        continue;
                    };
                    run_states.entry(run_id).or_insert(RunLifecycle::Queued);
                    match &event.kind {
                        TimelineEventKind::RunState { state } => {
                            run_states.insert(run_id, state.clone());
                        }
                        TimelineEventKind::PermissionRequested(_) => {
                            let count = &mut permission_counts.entry(run_id).or_default().0;
                            *count = count.saturating_add(1);
                        }
                        TimelineEventKind::PermissionResolved(_) => {
                            let count = &mut permission_counts.entry(run_id).or_default().1;
                            *count = count.saturating_add(1);
                        }
                        _ => {}
                    }
                }
                if count < 10_000 {
                    break;
                }
                cursor = page.last().map(|event| EventCursor {
                    sequence: event.sequence,
                });
            }

            for (run_id, state) in &mut run_states {
                if !is_terminal_run(state) {
                    self.emit(
                        session.id,
                        Some(*run_id),
                        TimelineEventKind::RunState {
                            state: RunLifecycle::Interrupted,
                        },
                    )
                    .await?;
                    *state = RunLifecycle::Interrupted;
                }
            }
            for (run_id, (requested, resolved)) in permission_counts {
                for _ in 0..requested.saturating_sub(resolved) {
                    self.emit(
                        session.id,
                        Some(run_id),
                        TimelineEventKind::PermissionResolved(PermissionOutcome::Cancelled),
                    )
                    .await?;
                }
            }
            for (run_id, state) in run_states {
                recovered_runs.insert(run_id, (session.id, state));
            }

            if updated_agents.insert(session.agent_id) {
                let mut agent = self.agent(session.agent_id).await?;
                if agent.lifecycle == AgentLifecycle::Busy {
                    agent
                        .lifecycle
                        .transition(AgentLifecycle::Ready)
                        .map_err(|error| CoreError::Other(error.to_string()))?;
                    agent.updated_at = Utc::now();
                    let saved = agent.clone();
                    self.inner
                        .store
                        .run(move |store| store.save_agent(&saved))
                        .await
                        .map_err(CoreError::Store)?;
                }
            }
        }

        let mut runs = self.inner.runs.write().await;
        for (run_id, (session_id, state)) in recovered_runs {
            runs.insert(run_id, Arc::new(RunControl::restored(session_id, state)));
        }
        Ok(())
    }

    #[must_use]
    pub fn home(&self) -> &Path {
        &self.inner.paths.home
    }

    pub async fn config(&self) -> AppConfig {
        self.inner.config.read().await.clone()
    }

    /// Validate and atomically replace configuration. Sessions already created retain their provider.
    pub async fn update_config(&self, config: AppConfig) -> CoreResult<()> {
        let _start_guard = self.inner.run_start_gate.lock().await;
        self.ensure_running()?;
        config.validate()?;
        let providers = build_providers(&config)?;
        config::save(&self.inner.paths.config, &config).await?;
        *self.inner.providers.write().await = providers;
        *self.inner.config.write().await = config;
        Ok(())
    }

    /// Add or replace a custom provider implementation for this daemon instance.
    pub async fn register_provider(&self, provider: Arc<dyn AgentProvider>) {
        self.inner
            .providers
            .write()
            .await
            .insert(provider.id().to_string(), provider);
    }

    pub async fn provider_diagnostics(&self) -> Vec<ProviderDiagnostic> {
        let providers: Vec<_> = self
            .inner
            .providers
            .read()
            .await
            .values()
            .cloned()
            .collect();
        let mut diagnostics = Vec::with_capacity(providers.len());
        for provider in providers {
            diagnostics.push(provider.diagnostic().await);
        }
        diagnostics.sort_by(|a, b| a.descriptor.id.as_str().cmp(b.descriptor.id.as_str()));
        diagnostics
    }

    pub async fn providers(&self) -> Vec<ProviderDescriptor> {
        self.provider_diagnostics()
            .await
            .into_iter()
            .map(|item| item.descriptor)
            .collect()
    }

    pub async fn create_workspace(
        &self,
        name: impl Into<String>,
        path: impl AsRef<Path>,
        kind: WorkspaceKind,
    ) -> CoreResult<Workspace> {
        self.ensure_running()?;
        let requested = path.as_ref();
        let canonical = tokio::fs::canonicalize(requested)
            .await
            .map_err(|_| CoreError::InvalidWorkspacePath(requested.display().to_string()))?;
        if !tokio::fs::metadata(&canonical).await?.is_dir() {
            return Err(CoreError::InvalidWorkspacePath(
                canonical.display().to_string(),
            ));
        }
        let mut workspace = Workspace::new(name, canonical.to_string_lossy(), kind)
            .map_err(|error| CoreError::Other(error.to_string()))?;
        workspace.updated_at = Utc::now();
        let save = workspace.clone();
        self.inner
            .store
            .run(move |store| store.save_workspace(&save))
            .await
            .map_err(CoreError::Store)?;
        Ok(workspace)
    }

    pub async fn list_workspaces(&self) -> CoreResult<Vec<Workspace>> {
        self.inner
            .store
            .run(|store| store.list_workspaces())
            .await
            .map_err(CoreError::Store)
    }

    pub async fn workspace(&self, id: WorkspaceId) -> CoreResult<Workspace> {
        self.inner
            .store
            .run(move |store| store.workspace(id))
            .await
            .map_err(CoreError::Store)?
            .ok_or(CoreError::WorkspaceNotFound(id))
    }

    pub async fn list_agents(&self, workspace_id: WorkspaceId) -> CoreResult<Vec<Agent>> {
        self.inner
            .store
            .run(move |store| store.list_agents(workspace_id))
            .await
            .map_err(CoreError::Store)
    }

    pub async fn agent(&self, id: AgentId) -> CoreResult<Agent> {
        self.inner
            .store
            .run(move |store| store.agent(id))
            .await
            .map_err(CoreError::Store)?
            .ok_or(CoreError::AgentNotFound(id))
    }

    pub async fn list_sessions(&self, agent_id: AgentId) -> CoreResult<Vec<Session>> {
        self.inner
            .store
            .run(move |store| store.list_sessions(agent_id))
            .await
            .map_err(CoreError::Store)
    }

    pub async fn create_agent(
        &self,
        workspace_id: WorkspaceId,
        provider: Option<ProviderId>,
        title: impl Into<String>,
    ) -> CoreResult<CreatedAgent> {
        let _start_guard = self.inner.run_start_gate.lock().await;
        self.ensure_running()?;
        let workspace = self.workspace(workspace_id).await?;
        if workspace.lifecycle != WorkspaceLifecycle::Active {
            return Err(CoreError::WorkspaceArchived);
        }
        let provider_id = match provider {
            Some(provider) => provider,
            None => ProviderId::new(self.inner.config.read().await.default_provider.clone())
                .map_err(|error| CoreError::Other(error.to_string()))?,
        };
        self.require_provider(&provider_id).await?;
        let agent = Agent::new(workspace_id, provider_id, title)
            .map_err(|error| CoreError::Other(error.to_string()))?;
        let save = agent.clone();
        self.inner
            .store
            .run(move |store| store.save_agent(&save))
            .await
            .map_err(CoreError::Store)?;
        match self
            .create_session_for_agent(agent.clone(), workspace)
            .await
        {
            Ok(session) => Ok(CreatedAgent {
                agent: self.agent(agent.id).await?,
                session,
            }),
            Err(error) => Err(error),
        }
    }

    pub async fn create_session(&self, agent_id: AgentId) -> CoreResult<Session> {
        let _start_guard = self.inner.run_start_gate.lock().await;
        self.ensure_running()?;
        let agent = self.agent(agent_id).await?;
        let workspace = self.workspace(agent.workspace_id).await?;
        if workspace.lifecycle != WorkspaceLifecycle::Active {
            return Err(CoreError::WorkspaceArchived);
        }
        self.create_session_for_agent(agent, workspace).await
    }

    async fn create_session_for_agent(
        &self,
        agent: Agent,
        workspace: Workspace,
    ) -> CoreResult<Session> {
        let provider = self.require_provider(&agent.provider).await?;
        let mut session = Session::new(agent.id);
        let initial_session = session.clone();
        self.inner
            .store
            .run(move |store| store.save_session(&initial_session))
            .await
            .map_err(CoreError::Store)?;
        let active_run = Arc::new(StdMutex::new(None));
        let permission_handler: Arc<dyn PermissionHandler> = Arc::new(ServicePermissionHandler {
            inner: Arc::downgrade(&self.inner),
            session_id: session.id,
            active_run: active_run.clone(),
        });
        let runtime = match provider
            .create_session(Path::new(&workspace.path), permission_handler)
            .await
        {
            Ok(runtime) => runtime,
            Err(error) => {
                session.lifecycle = SessionLifecycle::Interrupted;
                session.updated_at = Utc::now();
                let failed_session = session.clone();
                let _ = self
                    .inner
                    .store
                    .run(move |store| store.save_session(&failed_session))
                    .await;
                let _ = self
                    .update_agent(agent.id, |agent| {
                        if agent.lifecycle == AgentLifecycle::Created {
                            agent
                                .lifecycle
                                .transition(AgentLifecycle::Failed)
                                .map_err(|error| CoreError::Other(error.to_string()))?;
                        }
                        Ok(())
                    })
                    .await;
                return Err(error.into());
            }
        };
        session.provider_session_id = Some(runtime.native_session_id().to_owned());
        session.lifecycle = SessionLifecycle::Active;
        session.updated_at = Utc::now();
        let saved_session = session.clone();
        if let Err(error) = self
            .inner
            .store
            .run(move |store| store.save_session(&saved_session))
            .await
        {
            runtime.shutdown(Duration::from_millis(500)).await;
            return Err(CoreError::Store(error));
        }
        let agent = match self
            .update_agent(agent.id, |agent| {
                if !matches!(
                    agent.lifecycle,
                    AgentLifecycle::Ready
                        | AgentLifecycle::Busy
                        | AgentLifecycle::AttentionRequired
                ) {
                    agent
                        .lifecycle
                        .transition(AgentLifecycle::Ready)
                        .map_err(|error| CoreError::Other(error.to_string()))?;
                }
                Ok(())
            })
            .await
        {
            Ok(agent) => agent,
            Err(error) => {
                runtime.shutdown(Duration::from_millis(500)).await;
                return Err(error);
            }
        };
        self.install_runtime(session.clone(), agent, active_run, runtime)
            .await;
        Ok(session)
    }

    /// Load/resume using the provider ID and native handle stored on this session.
    pub async fn load_session(&self, session_id: SessionId) -> CoreResult<Session> {
        let _start_guard = self.inner.run_start_gate.lock().await;
        self.ensure_running()?;
        if self.inner.sessions.read().await.contains_key(&session_id) {
            return self
                .inner
                .store
                .run(move |store| store.session(session_id))
                .await
                .map_err(CoreError::Store)?
                .ok_or(CoreError::SessionNotFound(session_id));
        }
        let session = self
            .inner
            .store
            .run(move |store| store.session(session_id))
            .await
            .map_err(CoreError::Store)?
            .ok_or(CoreError::SessionNotFound(session_id))?;
        let agent = self.agent(session.agent_id).await?;
        let workspace = self.workspace(agent.workspace_id).await?;
        let native = session
            .provider_session_id
            .clone()
            .ok_or(CoreError::NativeSessionMissing(session.id))?;
        let provider = self.require_provider(&agent.provider).await?;
        let active_run = Arc::new(StdMutex::new(None));
        let permission_handler: Arc<dyn PermissionHandler> = Arc::new(ServicePermissionHandler {
            inner: Arc::downgrade(&self.inner),
            session_id,
            active_run: active_run.clone(),
        });
        let runtime = provider
            .resume_session(Path::new(&workspace.path), &native, permission_handler)
            .await?;
        let mut loaded_session = session;
        loaded_session.lifecycle = SessionLifecycle::Active;
        loaded_session.updated_at = Utc::now();
        let saved = loaded_session.clone();
        self.inner
            .store
            .run(move |store| store.save_session(&saved))
            .await
            .map_err(CoreError::Store)?;
        self.install_runtime(loaded_session.clone(), agent, active_run, runtime)
            .await;
        Ok(loaded_session)
    }

    async fn install_runtime(
        &self,
        session: Session,
        agent: Agent,
        active_run: Arc<StdMutex<Option<RunId>>>,
        provider: Box<dyn ProviderSession>,
    ) {
        let runtime: Arc<dyn ProviderSession> = Arc::from(provider);
        let session_runtime = Arc::new(SessionRuntime {
            session: session.clone(),
            agent,
            provider: runtime.clone(),
            active_run: active_run.clone(),
            event_task: Mutex::new(None),
        });
        self.inner
            .sessions
            .write()
            .await
            .insert(session.id, session_runtime.clone());
        let weak = Arc::downgrade(&self.inner);
        let event_provider = runtime.clone();
        let event_active_run = active_run;
        let task = tokio::spawn(async move {
            provider_event_loop(weak, session.id, event_provider, event_active_run).await;
        });
        *session_runtime.event_task.lock().await = Some(task);
    }

    /// Submit a prompt without idempotency metadata.
    pub async fn send_prompt(
        &self,
        session_id: SessionId,
        text: impl Into<String>,
    ) -> CoreResult<RunId> {
        Ok(self
            .submit_prompt(session_id, text.into(), None)
            .await?
            .run_id)
    }

    /// Submit a prompt with a durable, session-scoped idempotency key.
    ///
    /// Identical retries return the original run without a second provider call
    /// or user-event append, including while the session is busy and after a
    /// restart. Reusing a key with a different payload is an explicit conflict.
    /// Acceptance is recorded atomically with the user event and initial run
    /// state, so a crash before the provider starts reconciles to `Interrupted`
    /// and is never automatically re-executed.
    pub async fn send_prompt_keyed(
        &self,
        session_id: SessionId,
        text: impl Into<String>,
        request_key: impl Into<String>,
    ) -> CoreResult<AcceptedPrompt> {
        self.submit_prompt(session_id, text.into(), Some(request_key.into()))
            .await
    }

    async fn submit_prompt(
        &self,
        session_id: SessionId,
        text: String,
        request_key: Option<String>,
    ) -> CoreResult<AcceptedPrompt> {
        // Acceptance runs in an owned task so dropping the requesting future cannot
        // strand a reserved active-run slot or leave a key without its startup.
        // The task is serialized by `run_start_gate`, which shutdown also acquires.
        let service = self.clone();
        let (reply, response) = oneshot::channel();
        tokio::spawn(async move {
            let result = service
                .submit_prompt_owned(session_id, text, request_key)
                .await;
            let _ = reply.send(result);
        });
        response
            .await
            .map_err(|_| CoreError::Other("prompt submission task was dropped".into()))?
    }

    async fn submit_prompt_owned(
        &self,
        session_id: SessionId,
        text: String,
        request_key: Option<String>,
    ) -> CoreResult<AcceptedPrompt> {
        self.ensure_running()?;
        if text.trim().is_empty() {
            return Err(CoreError::Other("prompt text must not be empty".into()));
        }
        let keyed = request_key
            .map(|key| -> CoreResult<(String, String)> {
                validate_prompt_request_key(&key)?;
                Ok((key, prompt_payload_fingerprint(&text)))
            })
            .transpose()?;
        let _start_guard = self.inner.run_start_gate.lock().await;
        self.ensure_running()?;

        // A known key is authoritative even when the session is busy, unloaded,
        // or the stored run already reached a terminal state. Resolve it before
        // requiring a live provider runtime.
        if let Some((request_key, fingerprint)) = &keyed
            && let Some(existing) = self.lookup_prompt_request(session_id, request_key).await?
        {
            if existing.payload_fingerprint != *fingerprint {
                return Err(CoreError::PromptRequestKeyConflict {
                    session: session_id,
                });
            }
            let state = self
                .accepted_prompt_state(session_id, existing.run_id)
                .await?;
            return Ok(AcceptedPrompt {
                run_id: existing.run_id,
                state,
                restored: true,
            });
        }

        let runtime = self
            .inner
            .sessions
            .read()
            .await
            .get(&session_id)
            .cloned()
            .ok_or(CoreError::SessionNotLoaded(session_id))?;
        let run_id = RunId::new();
        {
            let mut active = runtime
                .active_run
                .lock()
                .map_err(|_| CoreError::Other("active run lock poisoned".into()))?;
            if active.is_some() {
                return Err(CoreError::SessionBusy);
            }
            *active = Some(run_id);
        }
        let control = Arc::new(RunControl::new(session_id));
        self.inner
            .runs
            .write()
            .await
            .insert(run_id, control.clone());
        if let Some((request_key, fingerprint)) = keyed {
            self.start_keyed_prompt(runtime, control, run_id, text, request_key, fingerprint)
                .await
        } else {
            self.start_unkeyed_prompt(runtime, control, run_id, text)
                .await
        }
    }

    async fn start_unkeyed_prompt(
        &self,
        runtime: Arc<SessionRuntime>,
        control: Arc<RunControl>,
        run_id: RunId,
        text: String,
    ) -> CoreResult<AcceptedPrompt> {
        let session_id = runtime.session.id;
        let startup = async {
            self.emit(
                session_id,
                Some(run_id),
                TimelineEventKind::UserText { text: text.clone() },
            )
            .await?;
            self.set_run_state(run_id, RunLifecycle::Starting).await?;
            self.mark_agent_busy(runtime.agent.id).await?;
            if control.cancellation.is_cancelled() {
                return Err(CoreError::Other(
                    "run was cancelled before execution".into(),
                ));
            }
            self.set_run_state(run_id, RunLifecycle::Running).await
        }
        .await;
        if let Err(error) = startup {
            if let Ok(mut active) = runtime.active_run.lock()
                && *active == Some(run_id)
            {
                *active = None;
            }
            let state = control.state.lock().await.clone();
            if state == RunLifecycle::Queued {
                self.inner.runs.write().await.remove(&run_id);
            } else if !is_terminal_run(&state) {
                let terminal = if control.cancellation.is_cancelled() {
                    RunLifecycle::Cancelled
                } else {
                    RunLifecycle::Failed
                };
                let _ = self.set_run_state(run_id, terminal).await;
            }
            return Err(error);
        }
        self.spawn_run(run_id, runtime, control, text).await;
        Ok(AcceptedPrompt {
            run_id,
            state: RunLifecycle::Running,
            restored: false,
        })
    }

    async fn start_keyed_prompt(
        &self,
        runtime: Arc<SessionRuntime>,
        control: Arc<RunControl>,
        run_id: RunId,
        text: String,
        request_key: String,
        fingerprint: String,
    ) -> CoreResult<AcceptedPrompt> {
        let session_id = runtime.session.id;
        let record = PromptRequestRecord {
            session_id,
            request_key,
            payload_fingerprint: fingerprint,
            run_id,
            accepted_at: Utc::now(),
        };
        // Publish the provisional lifecycle before awaiting the store so a cancel
        // that lands during acceptance is observed after the transaction commits.
        *control.state.lock().await = RunLifecycle::Starting;
        control.state_tx.send_replace(RunLifecycle::Starting);
        // Sequence allocation and the key + UserText + Starting transaction share
        // this lock so concurrent emitters cannot allocate the same sequences.
        let mut sequences = self.inner.sequence.lock().await;
        let prepared = self
            .prepare_keyed_acceptance_locked(session_id, run_id, &text, &mut sequences)
            .await;
        let (second, events) = match prepared {
            Ok(prepared) => prepared,
            Err(error) => {
                drop(sequences);
                self.discard_unstarted_run(&runtime, run_id).await;
                return Err(error);
            }
        };
        let acceptance = match self
            .inner
            .store
            .run({
                let record = record.clone();
                let events = events.clone();
                move |store| store.accept_prompt_request(&record, &events)
            })
            .await
        {
            Ok(acceptance) => acceptance,
            Err(error) => {
                drop(sequences);
                self.discard_unstarted_run(&runtime, run_id).await;
                return Err(CoreError::Store(error));
            }
        };
        match acceptance {
            PromptRequestAcceptance::Accepted(_) => {
                sequences.insert(session_id, second);
            }
            PromptRequestAcceptance::Existing(existing) => {
                drop(sequences);
                self.discard_unstarted_run(&runtime, run_id).await;
                let state = self
                    .accepted_prompt_state(session_id, existing.run_id)
                    .await?;
                return Ok(AcceptedPrompt {
                    run_id: existing.run_id,
                    state,
                    restored: true,
                });
            }
            PromptRequestAcceptance::Conflict { .. } => {
                drop(sequences);
                self.discard_unstarted_run(&runtime, run_id).await;
                return Err(CoreError::PromptRequestKeyConflict {
                    session: session_id,
                });
            }
        }
        drop(sequences);
        for event in events {
            let _ = self.inner.events.send(event);
        }

        if let Err(error) = self.mark_agent_busy(runtime.agent.id).await {
            self.settle_started_prompt(&runtime, run_id, RunLifecycle::Failed)
                .await;
            return Err(error);
        }
        if control.cancellation.is_cancelled() {
            self.settle_started_prompt(&runtime, run_id, RunLifecycle::Cancelled)
                .await;
            return Ok(AcceptedPrompt {
                run_id,
                state: RunLifecycle::Cancelled,
                restored: false,
            });
        }
        if let Err(error) = self.set_run_state(run_id, RunLifecycle::Running).await {
            self.settle_started_prompt(&runtime, run_id, RunLifecycle::Failed)
                .await;
            return Err(error);
        }
        self.spawn_run(run_id, runtime, control, text).await;
        Ok(AcceptedPrompt {
            run_id,
            state: RunLifecycle::Running,
            restored: false,
        })
    }

    async fn prepare_keyed_acceptance_locked(
        &self,
        session_id: SessionId,
        run_id: RunId,
        text: &str,
        sequences: &mut HashMap<SessionId, u64>,
    ) -> CoreResult<(u64, Vec<TimelineEvent>)> {
        let first = self.next_sequence_locked(session_id, sequences).await?;
        let second = first
            .checked_add(1)
            .ok_or_else(|| CoreError::Other("event sequence is exhausted".into()))?;
        let events = vec![
            TimelineEvent::new(
                session_id,
                Some(run_id),
                first,
                TimelineEventKind::UserText {
                    text: text.to_owned(),
                },
            )
            .map_err(|error| CoreError::Other(error.to_string()))?,
            TimelineEvent::new(
                session_id,
                Some(run_id),
                second,
                TimelineEventKind::RunState {
                    state: RunLifecycle::Starting,
                },
            )
            .map_err(|error| CoreError::Other(error.to_string()))?,
        ];
        Ok((second, events))
    }

    async fn mark_agent_busy(&self, agent_id: AgentId) -> CoreResult<()> {
        self.update_agent(agent_id, |agent| {
            if matches!(
                agent.lifecycle,
                AgentLifecycle::Ready | AgentLifecycle::AttentionRequired
            ) {
                agent
                    .lifecycle
                    .transition(AgentLifecycle::Busy)
                    .map_err(|error| CoreError::Other(error.to_string()))?;
            }
            Ok(())
        })
        .await?;
        Ok(())
    }

    async fn spawn_run(
        &self,
        run_id: RunId,
        runtime: Arc<SessionRuntime>,
        control: Arc<RunControl>,
        text: String,
    ) {
        let service = self.clone();
        let task = tokio::spawn(async move {
            service.execute_run(run_id, runtime, control, text).await;
        });
        let mut tasks = self.inner.run_tasks.lock().await;
        tasks.retain(|_, task| !task.is_finished());
        tasks.insert(run_id, task);
    }

    async fn discard_unstarted_run(&self, runtime: &Arc<SessionRuntime>, run_id: RunId) {
        if let Ok(mut active) = runtime.active_run.lock()
            && *active == Some(run_id)
        {
            *active = None;
        }
        self.inner.runs.write().await.remove(&run_id);
    }

    async fn settle_started_prompt(
        &self,
        runtime: &Arc<SessionRuntime>,
        run_id: RunId,
        terminal: RunLifecycle,
    ) {
        if let Ok(mut active) = runtime.active_run.lock()
            && *active == Some(run_id)
        {
            *active = None;
        }
        let _ = self.set_run_state(run_id, terminal).await;
    }

    async fn lookup_prompt_request(
        &self,
        session_id: SessionId,
        request_key: &str,
    ) -> CoreResult<Option<PromptRequestRecord>> {
        let request_key = request_key.to_owned();
        self.inner
            .store
            .run(move |store| store.prompt_request(session_id, &request_key))
            .await
            .map_err(CoreError::Store)
    }

    async fn accepted_prompt_state(
        &self,
        session_id: SessionId,
        run_id: RunId,
    ) -> CoreResult<RunLifecycle> {
        if let Some(control) = self.inner.runs.read().await.get(&run_id).cloned() {
            return Ok(control.state.lock().await.clone());
        }
        self.inner
            .store
            .run(move |store| store.run_state(session_id, run_id))
            .await
            .map_err(CoreError::Store)?
            .ok_or(CoreError::RunNotFound(run_id))
    }

    async fn execute_run(
        &self,
        run_id: RunId,
        runtime: Arc<SessionRuntime>,
        control: Arc<RunControl>,
        text: String,
    ) {
        let result = run_provider_prompt(
            &runtime.provider,
            &text,
            &control.cancellation,
            Duration::from_millis(self.inner.config.read().await.shutdown_grace_ms),
        )
        .await;
        if let Ok(outcome) = &result
            && let Some(usage) = &outcome.usage
        {
            let usage = Usage {
                input_tokens: Some(usage.input_tokens),
                output_tokens: Some(usage.output_tokens),
                cached_read_tokens: usage.cached_read_tokens,
                cached_write_tokens: usage.cached_write_tokens,
                context_used: None,
                context_size: None,
                cost_micros: None,
            };
            let _ = self
                .emit(
                    runtime.session.id,
                    Some(run_id),
                    TimelineEventKind::Usage(usage),
                )
                .await;
        }
        let terminal = match &result {
            Ok(outcome)
                if outcome.stop_reason == "cancelled" || control.cancellation.is_cancelled() =>
            {
                RunLifecycle::Cancelled
            }
            Ok(_) => RunLifecycle::Succeeded,
            Err(_) if control.cancellation.is_cancelled() => RunLifecycle::Cancelled,
            Err(_) => RunLifecycle::Failed,
        };
        if let Err(error) = result {
            let _ = self
                .emit(
                    runtime.session.id,
                    Some(run_id),
                    TimelineEventKind::Error(DomainError {
                        code: Some("provider_error".into()),
                        message: error.to_string(),
                        retryable: true,
                    }),
                )
                .await;
        }
        let _start_guard = self.inner.run_start_gate.lock().await;
        let permission_guard = self.inner.permission_start_gate.lock().await;
        if let Ok(mut current) = runtime.active_run.lock()
            && *current == Some(run_id)
        {
            *current = None;
        }
        drop(permission_guard);
        if let Err(error) = self.cancel_run_permissions(run_id).await {
            tracing::warn!(
                %error,
                %run_id,
                "failed to settle pending permissions for a completed run"
            );
        }
        if let Err(error) = self
            .update_agent(runtime.agent.id, |agent| {
                if matches!(
                    agent.lifecycle,
                    AgentLifecycle::Busy | AgentLifecycle::AttentionRequired
                ) {
                    agent
                        .lifecycle
                        .transition(AgentLifecycle::Ready)
                        .map_err(|error| CoreError::Other(error.to_string()))?;
                }
                Ok(())
            })
            .await
        {
            tracing::warn!(%error, %run_id, "failed to release agent after a completed run");
        }
        if let Err(error) = self.set_run_state(run_id, terminal.clone()).await {
            tracing::error!(
                %error,
                %run_id,
                terminal = ?terminal,
                "failed to persist terminal run state"
            );
        }
    }

    pub async fn cancel_run(&self, run_id: RunId) -> CoreResult<()> {
        let control = self
            .inner
            .runs
            .read()
            .await
            .get(&run_id)
            .cloned()
            .ok_or(CoreError::RunNotFound(run_id))?;
        let state = control.state.lock().await.clone();
        match state {
            RunLifecycle::Running => {
                let result = self
                    .set_run_state(run_id, RunLifecycle::CancelRequested)
                    .await;
                control.cancellation.cancel();
                result?;
            }
            RunLifecycle::Starting => {
                // Starting -> CancelRequested is not a valid domain transition. Signal
                // cancellation and let the start path publish Cancelled after releasing
                // the runtime's active-run slot.
                control.cancellation.cancel();
            }
            _ => {}
        }
        Ok(())
    }

    pub async fn cancel_session(&self, session_id: SessionId) -> CoreResult<()> {
        let run = self
            .inner
            .sessions
            .read()
            .await
            .get(&session_id)
            .and_then(|runtime| runtime.active_run.lock().ok().and_then(|active| *active));
        if let Some(run) = run {
            self.cancel_run(run).await?;
        }
        Ok(())
    }

    pub async fn wait_run(&self, run_id: RunId) -> CoreResult<RunLifecycle> {
        let control = self
            .inner
            .runs
            .read()
            .await
            .get(&run_id)
            .cloned()
            .ok_or(CoreError::RunNotFound(run_id))?;
        let mut receiver = control.state_tx.subscribe();
        loop {
            let state = receiver.borrow().clone();
            if is_terminal_run(&state) {
                return Ok(state);
            }
            receiver
                .changed()
                .await
                .map_err(|_| CoreError::Other("run state channel closed".into()))?;
        }
    }

    pub async fn run_state(&self, run_id: RunId) -> CoreResult<RunLifecycle> {
        let control = self
            .inner
            .runs
            .read()
            .await
            .get(&run_id)
            .cloned()
            .ok_or(CoreError::RunNotFound(run_id))?;
        Ok(control.state.lock().await.clone())
    }

    pub async fn set_model(
        &self,
        session_id: SessionId,
        model_id: impl Into<String>,
    ) -> CoreResult<()> {
        let _start_guard = self.inner.run_start_gate.lock().await;
        self.ensure_running()?;
        let model_id = model_id.into();
        let runtime = self.loaded_runtime(session_id).await?;
        if runtime
            .active_run
            .lock()
            .map_err(|_| CoreError::Other("active run lock poisoned".into()))?
            .is_some()
        {
            return Err(CoreError::SessionBusy);
        }
        if !runtime.provider.capabilities().model_selection {
            return Err(ProviderError::Unsupported {
                provider: runtime.agent.provider.to_string(),
                feature: "model selection",
            }
            .into());
        }
        let model = ModelRef::new(runtime.agent.provider.clone(), model_id.clone())
            .map_err(|error| CoreError::Other(error.to_string()))?;
        let advertised = runtime.provider.models();
        if !advertised.is_empty() && !advertised.iter().any(|model| model.model_id == model_id) {
            return Err(ProviderError::InvalidOption {
                provider: runtime.agent.provider.to_string(),
                feature: "model selection",
                option: model_id,
            }
            .into());
        }
        runtime.provider.set_model(&model.model_id).await?;
        self.update_agent(runtime.agent.id, move |agent| {
            agent.model = Some(model);
            Ok(())
        })
        .await?;
        Ok(())
    }

    pub async fn set_mode(
        &self,
        session_id: SessionId,
        mode_id: impl Into<String>,
    ) -> CoreResult<()> {
        let _start_guard = self.inner.run_start_gate.lock().await;
        self.ensure_running()?;
        let mode_id = mode_id.into();
        let runtime = self.loaded_runtime(session_id).await?;
        if runtime
            .active_run
            .lock()
            .map_err(|_| CoreError::Other("active run lock poisoned".into()))?
            .is_some()
        {
            return Err(CoreError::SessionBusy);
        }
        if !runtime.provider.capabilities().modes {
            return Err(ProviderError::Unsupported {
                provider: runtime.agent.provider.to_string(),
                feature: "modes",
            }
            .into());
        }
        if mode_id.trim().is_empty() {
            return Err(ProviderError::InvalidOption {
                provider: runtime.agent.provider.to_string(),
                feature: "mode selection",
                option: mode_id,
            }
            .into());
        }
        let advertised = runtime.provider.modes();
        if !advertised.is_empty() && !advertised.iter().any(|mode| mode.id == mode_id) {
            return Err(ProviderError::InvalidOption {
                provider: runtime.agent.provider.to_string(),
                feature: "mode selection",
                option: mode_id,
            }
            .into());
        }
        runtime.provider.set_mode(&mode_id).await?;
        self.update_agent(runtime.agent.id, move |agent| {
            agent.mode = Some(mode_id);
            Ok(())
        })
        .await?;
        Ok(())
    }

    pub async fn set_config_option(
        &self,
        session_id: SessionId,
        option_id: &str,
        value: &str,
    ) -> CoreResult<()> {
        let _start_guard = self.inner.run_start_gate.lock().await;
        self.ensure_running()?;
        let runtime = self.loaded_runtime(session_id).await?;
        if runtime
            .active_run
            .lock()
            .map_err(|_| CoreError::Other("active run lock poisoned".into()))?
            .is_some()
        {
            return Err(CoreError::SessionBusy);
        }
        if !runtime.provider.capabilities().config_options {
            return Err(ProviderError::Unsupported {
                provider: runtime.agent.provider.to_string(),
                feature: "configuration options",
            }
            .into());
        }
        let advertised = runtime.provider.config_options();
        if let Some(option) = advertised.iter().find(|option| option.id == option_id) {
            if !option.options.is_empty()
                && !option
                    .options
                    .iter()
                    .any(|supported| supported.value == value)
            {
                return Err(ProviderError::InvalidOption {
                    provider: runtime.agent.provider.to_string(),
                    feature: "configuration",
                    option: format!("{option_id}={value}"),
                }
                .into());
            }
        } else if !advertised.is_empty() {
            return Err(ProviderError::InvalidOption {
                provider: runtime.agent.provider.to_string(),
                feature: "configuration",
                option: option_id.to_owned(),
            }
            .into());
        }
        runtime.provider.set_config_option(option_id, value).await?;
        self.emit(
            session_id,
            None,
            TimelineEventKind::Extension {
                kind: "provider_config_option_changed".into(),
                payload: json!({"id":option_id,"value":value}),
            },
        )
        .await?;
        Ok(())
    }

    pub async fn resolve_permission(
        &self,
        id: PermissionRequestId,
        option_id: &str,
    ) -> CoreResult<()> {
        self.ensure_running()?;
        let mut pending = self.inner.permissions.lock().await;
        let request = pending
            .get(&id)
            .ok_or(CoreError::PermissionNotPending(id))?;
        if !request.options.contains(option_id) {
            return Err(CoreError::InvalidPermissionOption {
                request: id,
                option: option_id.to_owned(),
            });
        }
        let run_id = request.run_id;
        let session_id = request.session_id;
        let control = self.inner.runs.read().await.get(&run_id).cloned();
        let Some(control) = control else {
            let _ = self
                .emit(
                    session_id,
                    Some(run_id),
                    TimelineEventKind::PermissionResolved(PermissionOutcome::Cancelled),
                )
                .await;
            let request = pending.remove(&id).expect("permission was checked above");
            let _ = request.reply.send(None);
            return Err(CoreError::PermissionNotPending(id));
        };
        let active_matches = self
            .inner
            .sessions
            .read()
            .await
            .get(&session_id)
            .and_then(|runtime| {
                runtime
                    .active_run
                    .lock()
                    .ok()
                    .map(|active| *active == Some(run_id))
            })
            .unwrap_or(false);
        let state = control.state.lock().await;
        if !active_matches || *state != RunLifecycle::Running || control.cancellation.is_cancelled()
        {
            let _ = self
                .emit(
                    session_id,
                    Some(run_id),
                    TimelineEventKind::PermissionResolved(PermissionOutcome::Cancelled),
                )
                .await;
            let request = pending.remove(&id).expect("permission was checked above");
            let _ = request.reply.send(None);
            return Err(CoreError::PermissionNotPending(id));
        }
        let option_kind = request
            .option_kinds
            .get(option_id)
            .and_then(Option::as_deref);
        let outcome = permission_outcome_kind(option_id, option_kind);
        self.emit(
            session_id,
            Some(run_id),
            TimelineEventKind::PermissionResolved(outcome),
        )
        .await?;
        let request = pending.remove(&id).expect("permission was checked above");
        let _ = request.reply.send(Some(option_id.to_owned()));
        Ok(())
    }

    pub async fn cancel_permission(&self, id: PermissionRequestId) -> CoreResult<()> {
        self.ensure_running()?;
        if self.cancel_pending_permission(id).await? {
            Ok(())
        } else {
            Err(CoreError::PermissionNotPending(id))
        }
    }

    /// Pending permission requests currently awaiting a human decision.
    pub async fn list_pending_permissions(&self) -> CoreResult<Vec<PendingPermissionSummary>> {
        let mut summaries: Vec<_> = self
            .inner
            .permissions
            .lock()
            .await
            .values()
            .map(|request| request.summary.clone())
            .collect();
        summaries.sort_by_key(|summary| summary.id);
        Ok(summaries)
    }

    /// Aggregate the rich model catalogue for a provider from its loaded sessions.
    pub async fn list_models(&self, provider_id: &ProviderId) -> CoreResult<Vec<ProviderModel>> {
        self.ensure_running()?;
        let provider = self.require_provider(provider_id).await?;
        if !provider
            .diagnostic()
            .await
            .descriptor
            .capabilities
            .model_selection
        {
            return Err(ProviderError::Unsupported {
                provider: provider_id.to_string(),
                feature: "model listing",
            }
            .into());
        }
        let mut models = Vec::new();
        let mut seen = HashSet::new();
        for runtime in self.inner.sessions.read().await.values() {
            if runtime.agent.provider != *provider_id {
                continue;
            }
            for model in runtime.provider.models_detailed() {
                if seen.insert(model.id.clone()) {
                    models.push(model);
                }
            }
        }
        models.sort_by_key(|model| model.id.clone());
        Ok(models)
    }

    /// Aggregate provider modes from its loaded sessions (union, de-duplicated).
    pub async fn list_modes(&self, provider_id: &ProviderId) -> CoreResult<Vec<ProviderMode>> {
        self.ensure_running()?;
        let provider = self.require_provider(provider_id).await?;
        if !provider.diagnostic().await.descriptor.capabilities.modes {
            return Err(ProviderError::Unsupported {
                provider: provider_id.to_string(),
                feature: "modes",
            }
            .into());
        }
        let mut modes = Vec::new();
        let mut seen = HashSet::new();
        for runtime in self.inner.sessions.read().await.values() {
            if runtime.agent.provider != *provider_id {
                continue;
            }
            for mode in runtime.provider.modes() {
                if seen.insert(mode.id.clone()) {
                    modes.push(mode);
                }
            }
        }
        modes.sort_by_key(|mode| mode.id.clone());
        Ok(modes)
    }

    /// Archive a session, unloading its provider runtime when it is not busy.
    pub async fn archive_session(&self, session_id: SessionId) -> CoreResult<Session> {
        let _start_guard = self.inner.run_start_gate.lock().await;
        self.ensure_running()?;
        let session = self
            .inner
            .store
            .run(move |store| store.session(session_id))
            .await
            .map_err(CoreError::Store)?
            .ok_or(CoreError::SessionNotFound(session_id))?;
        if session.lifecycle == SessionLifecycle::Archived {
            return Ok(session);
        }
        let runtime = self.inner.sessions.write().await.remove(&session_id);
        if let Some(runtime) = runtime {
            let busy = runtime
                .active_run
                .lock()
                .map_err(|_| CoreError::Other("active run lock poisoned".into()))?
                .is_some();
            if busy {
                self.inner
                    .sessions
                    .write()
                    .await
                    .insert(session_id, runtime);
                return Err(CoreError::SessionBusy);
            }
            if let Some(task) = runtime.event_task.lock().await.take() {
                task.abort();
            }
            let grace = Duration::from_millis(self.inner.config.read().await.shutdown_grace_ms);
            let _ = tokio::time::timeout(
                grace + Duration::from_millis(500),
                runtime.provider.shutdown(grace),
            )
            .await;
        }
        let mut archived = session;
        archived.lifecycle = SessionLifecycle::Archived;
        archived.updated_at = Utc::now();
        let save = archived.clone();
        self.inner
            .store
            .run(move |store| store.save_session(&save))
            .await
            .map_err(CoreError::Store)?;
        Ok(archived)
    }

    /// Rename the agent that owns a session (the session's display title).
    pub async fn rename_session(
        &self,
        session_id: SessionId,
        title: impl Into<String>,
    ) -> CoreResult<Agent> {
        self.ensure_running()?;
        let title = title.into();
        if title.trim().is_empty() {
            return Err(CoreError::Other("title must not be empty".into()));
        }
        if title.len() > 512 {
            return Err(CoreError::Other(
                "title exceeds maximum length of 512 bytes".into(),
            ));
        }
        let session = self
            .inner
            .store
            .run(move |store| store.session(session_id))
            .await
            .map_err(CoreError::Store)?
            .ok_or(CoreError::SessionNotFound(session_id))?;
        self.update_agent(session.agent_id, move |agent| {
            agent.title = title;
            Ok(())
        })
        .await
    }

    /// Select a thinking depth via the provider's thinking config option.
    pub async fn set_thinking(
        &self,
        session_id: SessionId,
        thinking_option_id: impl Into<String>,
    ) -> CoreResult<()> {
        let _start_guard = self.inner.run_start_gate.lock().await;
        self.ensure_running()?;
        let thinking_option_id = thinking_option_id.into();
        let runtime = self.loaded_runtime(session_id).await?;
        if runtime
            .active_run
            .lock()
            .map_err(|_| CoreError::Other("active run lock poisoned".into()))?
            .is_some()
        {
            return Err(CoreError::SessionBusy);
        }
        if !runtime.provider.capabilities().config_options {
            return Err(ProviderError::Unsupported {
                provider: runtime.agent.provider.to_string(),
                feature: "thinking options",
            }
            .into());
        }
        let config_options = runtime.provider.config_options();
        let config = provider::thinking_config_option(&config_options).ok_or_else(|| {
            ProviderError::Unsupported {
                provider: runtime.agent.provider.to_string(),
                feature: "thinking options",
            }
        })?;
        if !config.options.is_empty()
            && !config
                .options
                .iter()
                .any(|supported| supported.value == thinking_option_id)
        {
            return Err(CoreError::InvalidThinkingOption {
                config: config.id.clone(),
                option: thinking_option_id,
            });
        }
        runtime
            .provider
            .set_config_option(&config.id, &thinking_option_id)
            .await?;
        self.emit(
            session_id,
            None,
            TimelineEventKind::Extension {
                kind: "provider_config_option_changed".into(),
                payload: json!({ "id": config.id, "value": thinking_option_id }),
            },
        )
        .await?;
        Ok(())
    }

    /// Aggregate billed token/cost usage from the durable timeline.
    pub async fn usage_summary(&self, session_id: Option<SessionId>) -> CoreResult<UsageSummary> {
        self.ensure_running()?;
        let sessions = if let Some(session_id) = session_id {
            let session = self
                .inner
                .store
                .run(move |store| store.session(session_id))
                .await
                .map_err(CoreError::Store)?
                .ok_or(CoreError::SessionNotFound(session_id))?;
            vec![session]
        } else {
            self.all_sessions().await?
        };
        let mut summary = UsageSummary::default();
        for session in sessions {
            let mut usage = SessionUsage {
                session_id: session.id,
                ..SessionUsage::default()
            };
            let mut cursor = None;
            loop {
                let page = self.replay_events(session.id, cursor, 10_000).await?;
                let count = page.len();
                let next_cursor = page.last().map(|event| EventCursor {
                    sequence: event.sequence,
                });
                for event in page {
                    if let TimelineEventKind::Usage(record) = event.kind {
                        usage.input_tokens += record.input_tokens.unwrap_or(0);
                        usage.output_tokens += record.output_tokens.unwrap_or(0);
                        usage.cached_read_tokens += record.cached_read_tokens.unwrap_or(0);
                        usage.cached_write_tokens += record.cached_write_tokens.unwrap_or(0);
                        usage.cost_micros += record.cost_micros.unwrap_or(0);
                    }
                }
                if count < 10_000 {
                    break;
                }
                cursor = next_cursor;
            }
            summary.totals.input_tokens += usage.input_tokens;
            summary.totals.output_tokens += usage.output_tokens;
            summary.totals.cached_read_tokens += usage.cached_read_tokens;
            summary.totals.cached_write_tokens += usage.cached_write_tokens;
            summary.totals.cost_micros += usage.cost_micros;
            summary.sessions.push(usage);
        }
        Ok(summary)
    }

    #[must_use]
    pub fn subscribe_events(&self) -> broadcast::Receiver<TimelineEvent> {
        self.inner.events.subscribe()
    }

    pub async fn session_snapshot(
        &self,
        session_id: SessionId,
        limit: usize,
    ) -> CoreResult<SessionSnapshot> {
        self.inner
            .store
            .run(move |store| store.snapshot(session_id, limit))
            .await
            .map_err(CoreError::Store)
    }

    pub async fn replay_events(
        &self,
        session_id: SessionId,
        cursor: Option<EventCursor>,
        limit: usize,
    ) -> CoreResult<Vec<TimelineEvent>> {
        self.inner
            .store
            .run(move |store| store.replay_events(session_id, cursor, limit))
            .await
            .map_err(CoreError::Store)
    }

    pub async fn respond_to_permission(
        &self,
        id: PermissionRequestId,
        option_id: &str,
    ) -> CoreResult<()> {
        self.resolve_permission(id, option_id).await
    }

    pub async fn diagnostics(&self) -> CoreResult<CoreDiagnostics> {
        Ok(CoreDiagnostics {
            home: self.inner.paths.home.clone(),
            schema_version: self
                .inner
                .store
                .run(|store| store.schema_version())
                .await
                .map_err(CoreError::Store)?,
            providers: self.provider_diagnostics().await,
            active_runs: self
                .inner
                .runs
                .read()
                .await
                .values()
                .filter(|run| {
                    !run.state
                        .try_lock()
                        .is_ok_and(|state| is_terminal_run(&state))
                })
                .count(),
        })
    }

    pub async fn shutdown(&self) {
        if !self.inner.shutting_down.swap(true, Ordering::AcqRel) {
            let service = self.clone();
            tokio::spawn(async move {
                service.shutdown_inner().await;
                service.inner.shutdown_done.send_replace(true);
            });
        }
        let mut done = self.inner.shutdown_done.subscribe();
        while !*done.borrow() {
            if done.changed().await.is_err() {
                break;
            }
        }
    }

    async fn shutdown_inner(&self) {
        self.inner.shutdown.cancel();
        {
            let _start_guard = self.inner.run_start_gate.lock().await;
            let _permission_guard = self.inner.permission_start_gate.lock().await;
        }
        let controls: Vec<_> = self.inner.runs.read().await.values().cloned().collect();
        for control in &controls {
            control.cancellation.cancel();
        }
        self.cancel_all_pending_permissions().await;

        let sessions: Vec<_> = self.inner.sessions.read().await.values().cloned().collect();
        let grace = Duration::from_millis(self.inner.config.read().await.shutdown_grace_ms);
        let shutdown_limit = grace + Duration::from_millis(500);
        let mut provider_shutdowns = tokio::task::JoinSet::new();
        for session in &sessions {
            let provider = session.provider.clone();
            provider_shutdowns.spawn(async move {
                let _ = tokio::time::timeout(shutdown_limit, provider.shutdown(grace)).await;
            });
        }
        let _ = tokio::time::timeout(shutdown_limit, async {
            while provider_shutdowns.join_next().await.is_some() {}
        })
        .await;
        provider_shutdowns.abort_all();
        while provider_shutdowns.join_next().await.is_some() {}

        for session in sessions {
            if let Some(task) = session.event_task.lock().await.take() {
                task.abort();
                let _ = task.await;
            }
        }

        let mut run_tasks = std::mem::take(&mut *self.inner.run_tasks.lock().await);
        let run_tasks_done = async {
            for task in run_tasks.values_mut() {
                let _ = task.await;
            }
        };
        if tokio::time::timeout(shutdown_limit, run_tasks_done)
            .await
            .is_err()
        {
            for task in run_tasks.values() {
                task.abort();
            }
            for (_, task) in run_tasks {
                let _ = task.await;
            }
        }
        self.inner.store.shutdown().await;
    }

    async fn loaded_runtime(&self, id: SessionId) -> CoreResult<Arc<SessionRuntime>> {
        self.inner
            .sessions
            .read()
            .await
            .get(&id)
            .cloned()
            .ok_or(CoreError::SessionNotLoaded(id))
    }

    async fn all_sessions(&self) -> CoreResult<Vec<Session>> {
        let mut sessions = Vec::new();
        for workspace in self.list_workspaces().await? {
            for agent in self.list_agents(workspace.id).await? {
                sessions.extend(self.list_sessions(agent.id).await?);
            }
        }
        Ok(sessions)
    }

    async fn cancel_pending_permission(&self, id: PermissionRequestId) -> CoreResult<bool> {
        let mut pending = self.inner.permissions.lock().await;
        let Some(request) = pending.get(&id) else {
            return Ok(false);
        };
        let session_id = request.session_id;
        let run_id = request.run_id;
        let result = self
            .emit(
                session_id,
                Some(run_id),
                TimelineEventKind::PermissionResolved(PermissionOutcome::Cancelled),
            )
            .await;
        let request = pending.remove(&id).expect("permission was checked above");
        let _ = request.reply.send(None);
        result.map(|_| true)
    }

    async fn cancel_run_permissions(&self, run_id: RunId) -> CoreResult<()> {
        let ids: Vec<_> = self
            .inner
            .permissions
            .lock()
            .await
            .iter()
            .filter_map(|(id, request)| (request.run_id == run_id).then_some(*id))
            .collect();
        let mut first_error = None;
        for id in ids {
            if let Err(error) = self.cancel_pending_permission(id).await {
                first_error.get_or_insert(error);
            }
        }
        first_error.map_or(Ok(()), Err)
    }

    async fn cancel_all_pending_permissions(&self) {
        let ids: Vec<_> = self
            .inner
            .permissions
            .lock()
            .await
            .keys()
            .copied()
            .collect();
        for id in ids {
            let _ = self.cancel_pending_permission(id).await;
        }
    }

    async fn update_agent<F>(&self, id: AgentId, update: F) -> CoreResult<Agent>
    where
        F: FnOnce(&mut Agent) -> CoreResult<()> + Send,
    {
        let _guard = self.inner.agent_updates.lock().await;
        let mut agent = self.agent(id).await?;
        update(&mut agent)?;
        agent.updated_at = Utc::now();
        let saved = agent.clone();
        self.inner
            .store
            .run(move |store| store.save_agent(&saved))
            .await
            .map_err(CoreError::Store)?;
        Ok(agent)
    }

    async fn require_provider(&self, id: &ProviderId) -> CoreResult<Arc<dyn AgentProvider>> {
        let provider = self
            .inner
            .providers
            .read()
            .await
            .get(id.as_str())
            .cloned()
            .ok_or_else(|| CoreError::ProviderNotRegistered(id.to_string()))?;
        let diagnostic = provider.diagnostic().await;
        if !diagnostic.descriptor.available {
            return Err(CoreError::ProviderUnavailable(id.to_string()));
        }
        Ok(provider)
    }

    fn ensure_running(&self) -> CoreResult<()> {
        if self.inner.shutting_down.load(Ordering::Acquire) {
            Err(CoreError::ShuttingDown)
        } else {
            Ok(())
        }
    }

    async fn set_run_state(&self, id: RunId, next: RunLifecycle) -> CoreResult<()> {
        let control = self
            .inner
            .runs
            .read()
            .await
            .get(&id)
            .cloned()
            .ok_or(CoreError::RunNotFound(id))?;
        let mut state = control.state.lock().await;
        if *state != next {
            let mut proposed = state.clone();
            proposed
                .transition(next.clone())
                .map_err(|error| CoreError::Other(error.to_string()))?;
            self.emit(
                control.session_id,
                Some(id),
                TimelineEventKind::RunState {
                    state: next.clone(),
                },
            )
            .await?;
            *state = next.clone();
            control.state_tx.send_replace(next);
        }
        Ok(())
    }

    async fn emit(
        &self,
        session_id: SessionId,
        run_id: Option<RunId>,
        kind: TimelineEventKind,
    ) -> CoreResult<TimelineEvent> {
        let mut sequences = self.inner.sequence.lock().await;
        let next = self
            .next_sequence_locked(session_id, &mut sequences)
            .await?;
        let event = TimelineEvent::new(session_id, run_id, next, kind)
            .map_err(|error| CoreError::Other(error.to_string()))?;
        let persisted = event.clone();
        self.inner
            .store
            .run(move |store| store.append_event(&persisted))
            .await
            .map_err(CoreError::Store)?;
        sequences.insert(session_id, next);
        let _ = self.inner.events.send(event.clone());
        Ok(event)
    }

    async fn next_sequence_locked(
        &self,
        session_id: SessionId,
        sequences: &mut HashMap<SessionId, u64>,
    ) -> CoreResult<u64> {
        if let Some(sequence) = sequences.get(&session_id) {
            return Ok(sequence.saturating_add(1));
        }
        let mut cursor = None;
        let mut latest = 0_u64;
        loop {
            let page = self
                .inner
                .store
                .run(move |store| store.replay_events(session_id, cursor, 10_000))
                .await
                .map_err(CoreError::Store)?;
            let count = page.len();
            if let Some(last) = page.last() {
                latest = last.sequence;
                cursor = Some(EventCursor { sequence: latest });
            }
            if count < 10_000 {
                break;
            }
        }
        Ok(latest.saturating_add(1))
    }
}

impl RunControl {
    fn new(session_id: SessionId) -> Self {
        Self::restored(session_id, RunLifecycle::Queued)
    }

    fn restored(session_id: SessionId, state: RunLifecycle) -> Self {
        let (state_tx, _) = watch::channel(state.clone());
        Self {
            session_id,
            cancellation: CancellationToken::new(),
            state: Mutex::new(state),
            state_tx,
        }
    }
}

#[derive(Clone, Debug)]
pub struct CoreDiagnostics {
    pub home: PathBuf,
    pub schema_version: u32,
    pub providers: Vec<ProviderDiagnostic>,
    pub active_runs: usize,
}

struct ServicePermissionHandler {
    inner: std::sync::Weak<Inner>,
    session_id: SessionId,
    active_run: Arc<StdMutex<Option<RunId>>>,
}

#[async_trait::async_trait]
impl PermissionHandler for ServicePermissionHandler {
    async fn request_permission(
        &self,
        request: ProviderPermissionRequest,
        cancellation: CancellationToken,
    ) -> ProviderPermissionDecision {
        let Some(inner) = self.inner.upgrade() else {
            return ProviderPermissionDecision::Cancelled;
        };
        let permission_guard = inner.permission_start_gate.lock().await;
        if cancellation.is_cancelled() || inner.shutdown.is_cancelled() {
            return ProviderPermissionDecision::Cancelled;
        }
        let run_id = self.active_run.lock().ok().and_then(|active| *active);
        let Some(run_id) = run_id else {
            return ProviderPermissionDecision::Cancelled;
        };
        let Some(control) = inner.runs.read().await.get(&run_id).cloned() else {
            return ProviderPermissionDecision::Cancelled;
        };
        if control.cancellation.is_cancelled()
            || *control.state.lock().await != RunLifecycle::Running
        {
            return ProviderPermissionDecision::Cancelled;
        }
        let Some((id, receive)) =
            begin_permission_request(&inner, self.session_id, run_id, &request).await
        else {
            return ProviderPermissionDecision::Cancelled;
        };
        drop(permission_guard);
        finish_permission_request(
            &inner,
            self.session_id,
            run_id,
            id,
            &control,
            cancellation,
            receive,
        )
        .await
    }
}

async fn begin_permission_request(
    inner: &Arc<Inner>,
    session_id: SessionId,
    run_id: RunId,
    request: &ProviderPermissionRequest,
) -> Option<(PermissionRequestId, oneshot::Receiver<Option<String>>)> {
    let id = PermissionRequestId::new();
    let action = if request.title.trim().is_empty() {
        "tool request".into()
    } else {
        request.title.clone()
    };
    let details = json!({
        "nativeSessionId": request.native_session_id,
        "toolCallId": request.tool_call_id,
        "kind": request.kind,
        "rawInput": request.raw_input,
        "locations": request.locations.iter().map(|(path,line)| json!({"path":path,"line":line})).collect::<Vec<_>>(),
        "options": request.options.iter().map(|option| json!({"id":option.id,"label":option.label,"kind":option.kind})).collect::<Vec<_>>(),
    });
    let target = request.locations.first().map(|location| location.0.clone());
    let permission = PermissionRequest {
        id,
        run_id,
        action: action.clone(),
        target: target.clone(),
        risk: PermissionRisk::High,
        details: details.clone(),
        expires_at: None,
    };
    let summary = PendingPermissionSummary {
        id,
        session_id,
        run_id,
        action,
        target,
        risk: PermissionRisk::High,
        details,
    };
    let (reply, receive) = oneshot::channel();
    let options = request
        .options
        .iter()
        .map(|option| option.id.clone())
        .collect();
    let option_kinds = request
        .options
        .iter()
        .map(|option| (option.id.clone(), option.kind.clone()))
        .collect();
    let mut pending = inner.permissions.lock().await;
    pending.insert(
        id,
        PendingPermission {
            session_id,
            run_id,
            options,
            option_kinds,
            summary,
            reply,
        },
    );
    let service = AppService {
        inner: inner.clone(),
    };
    if service
        .emit(
            session_id,
            Some(run_id),
            TimelineEventKind::PermissionRequested(permission),
        )
        .await
        .is_err()
    {
        pending.remove(&id);
        return None;
    }
    drop(pending);
    Some((id, receive))
}

async fn finish_permission_request(
    inner: &Arc<Inner>,
    session_id: SessionId,
    run_id: RunId,
    id: PermissionRequestId,
    control: &RunControl,
    cancellation: CancellationToken,
    receive: oneshot::Receiver<Option<String>>,
) -> ProviderPermissionDecision {
    let answer = tokio::select! {
        answer = receive => answer.ok().flatten(),
        () = cancellation.cancelled() => None,
        () = inner.shutdown.cancelled() => None,
    };
    let active_matches = inner
        .sessions
        .read()
        .await
        .get(&session_id)
        .and_then(|runtime| {
            runtime
                .active_run
                .lock()
                .ok()
                .map(|active| *active == Some(run_id))
        })
        .unwrap_or(false);
    let run_is_running = *control.state.lock().await == RunLifecycle::Running;
    if !active_matches || control.cancellation.is_cancelled() || !run_is_running {
        let _ = AppService {
            inner: inner.clone(),
        }
        .cancel_pending_permission(id)
        .await;
        return ProviderPermissionDecision::Cancelled;
    }
    if let Some(option_id) = answer {
        ProviderPermissionDecision::Selected(option_id)
    } else {
        let _ = AppService {
            inner: inner.clone(),
        }
        .cancel_pending_permission(id)
        .await;
        ProviderPermissionDecision::Cancelled
    }
}

fn permission_outcome_kind(option_id: &str, kind: Option<&str>) -> PermissionOutcome {
    let text = kind.unwrap_or(option_id).to_ascii_lowercase();
    if text.contains("allow") || text.contains("accept") {
        PermissionOutcome::Allowed {
            remember: text.contains("always"),
        }
    } else {
        PermissionOutcome::Denied
    }
}

async fn provider_event_loop(
    inner: std::sync::Weak<Inner>,
    session_id: SessionId,
    provider: Arc<dyn ProviderSession>,
    active_run: Arc<StdMutex<Option<RunId>>>,
) {
    while let Some(event) = provider.next_event().await {
        let Some(inner) = inner.upgrade() else {
            return;
        };
        let run_id = active_run.lock().ok().and_then(|active| *active);
        match event {
            ProviderEvent::Acp(AcpEvent::SessionUpdate(update)) => {
                publish_acp_update(&AppService { inner }, session_id, run_id, *update).await;
            }
            ProviderEvent::Acp(AcpEvent::SessionReplay { updates, .. }) => {
                let service = AppService {
                    inner: inner.clone(),
                };
                let empty = service
                    .replay_events(session_id, None, 1)
                    .await
                    .is_ok_and(|events| events.is_empty());
                if empty {
                    for update in updates {
                        publish_acp_update(&service, session_id, None, update).await;
                    }
                }
            }
            ProviderEvent::Acp(AcpEvent::Extension { method, params }) => {
                let _ = AppService { inner }
                    .emit(
                        session_id,
                        run_id,
                        TimelineEventKind::Extension {
                            kind: method,
                            payload: params,
                        },
                    )
                    .await;
            }
            ProviderEvent::Acp(AcpEvent::Diagnostic { message }) => {
                let _ = AppService { inner }
                    .emit(
                        session_id,
                        run_id,
                        TimelineEventKind::Notice(Notice {
                            code: Some("provider_diagnostic".into()),
                            message,
                        }),
                    )
                    .await;
            }
        }
    }
}

async fn publish_acp_update(
    service: &AppService,
    session_id: SessionId,
    run_id: Option<RunId>,
    notification: SessionUpdateNotification,
) {
    let raw_notification = serde_json::to_value(&notification).unwrap_or(Value::Null);
    let update = notification.update;
    let text = || {
        update
            .content
            .as_ref()
            .and_then(|content| {
                content
                    .get("text")
                    .and_then(Value::as_str)
                    .map(str::to_owned)
                    .or_else(|| content.as_str().map(str::to_owned))
            })
            .unwrap_or_default()
    };
    let kind = match update.session_update.as_str() {
        "agent_message_chunk" => Some(TimelineEventKind::AssistantText { text: text() }),
        "agent_thought_chunk" => Some(TimelineEventKind::Thought { text: text() }),
        // Paseo already records the live submitted user message; ACP replay must not duplicate it.
        "user_message_chunk" => None,
        "tool_call" | "tool_call_update" => {
            let status = match update.status.as_deref() {
                Some("completed") => ToolStatus::Completed,
                Some("failed") => ToolStatus::Failed,
                _ => ToolStatus::Running,
            };
            Some(TimelineEventKind::Tool(ToolCall {
                id: update.tool_call_id.unwrap_or_else(|| "unknown-tool".into()),
                name: update
                    .title
                    .clone()
                    .unwrap_or_else(|| update.kind.clone().unwrap_or_else(|| "tool".into())),
                title: update.title,
                input: update.raw_input.unwrap_or(Value::Null),
                output: update.raw_output.or(update.content),
                status,
            }))
        }
        "usage_update" => Some(TimelineEventKind::Usage(Usage {
            input_tokens: None,
            output_tokens: None,
            cached_read_tokens: None,
            cached_write_tokens: None,
            context_used: update.used,
            context_size: update.size,
            cost_micros: None,
        })),
        "current_mode_update" => Some(TimelineEventKind::Extension {
            kind: "provider_mode_changed".into(),
            payload: json!({"modeId":update.current_mode_id}),
        }),
        "config_option_update" => Some(TimelineEventKind::Extension {
            kind: "provider_config_options".into(),
            payload: json!({"options":update.config_options}),
        }),
        "available_commands_update" => Some(TimelineEventKind::Extension {
            kind: "provider_commands".into(),
            payload: json!({"commands":update.available_commands}),
        }),
        other => Some(TimelineEventKind::Extension {
            kind: format!("acp:{other}"),
            payload: raw_notification,
        }),
    };
    if let Some(kind) = kind {
        let _ = service.emit(session_id, run_id, kind).await;
    }
}

async fn run_provider_prompt(
    provider: &Arc<dyn ProviderSession>,
    text: &str,
    cancellation: &CancellationToken,
    shutdown_grace: Duration,
) -> Result<ProviderPromptResult, ProviderError> {
    let prompt = provider.prompt(text, cancellation);
    tokio::pin!(prompt);
    tokio::select! {
        result = &mut prompt => result,
        () = cancellation.cancelled() => {
            if let Ok(result) = tokio::time::timeout(shutdown_grace, &mut prompt).await {
                result
            } else {
                let _ = tokio::time::timeout(
                    shutdown_grace,
                    provider.shutdown(shutdown_grace),
                )
                .await;
                Err(ProviderError::Other("provider did not settle after cancellation and was stopped".into()))
            }
        }
    }
}

fn validate_prompt_request_key(key: &str) -> CoreResult<()> {
    if key.trim().is_empty() {
        return Err(CoreError::EmptyPromptRequestKey);
    }
    if key.len() > MAX_PROMPT_REQUEST_KEY_BYTES {
        return Err(CoreError::PromptRequestKeyTooLong(
            MAX_PROMPT_REQUEST_KEY_BYTES,
        ));
    }
    Ok(())
}

fn prompt_payload_fingerprint(text: &str) -> String {
    let mut hasher = Sha256::new();
    hasher.update(b"paseo.prompt.v1\0");
    hasher.update(text.as_bytes());
    format!("{:x}", hasher.finalize())
}

fn is_terminal_run(state: &RunLifecycle) -> bool {
    matches!(
        state,
        RunLifecycle::Succeeded
            | RunLifecycle::Failed
            | RunLifecycle::Cancelled
            | RunLifecycle::Interrupted
    )
}

fn build_providers(config: &AppConfig) -> CoreResult<HashMap<String, Arc<dyn AgentProvider>>> {
    let mut providers = HashMap::new();
    for item in &config.providers {
        if !item.enabled {
            continue;
        }
        let provider = provider::provider_from_config(item)?;
        providers.insert(item.id.clone(), provider);
    }
    Ok(providers)
}

#[cfg(unix)]
async fn set_private_directory_permissions(path: &Path) -> Result<(), std::io::Error> {
    use std::os::unix::fs::PermissionsExt;
    tokio::fs::set_permissions(path, std::fs::Permissions::from_mode(0o700)).await
}

#[cfg(not(unix))]
async fn set_private_directory_permissions(_path: &Path) -> Result<(), std::io::Error> {
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::provider::ProviderPermissionOption;
    use paseo_domain::{Capabilities, ProviderDescriptor, WorkspaceKind};
    use std::sync::atomic::{AtomicUsize, Ordering as AtomicOrdering};
    use uuid::Uuid;

    struct TestHome {
        root: PathBuf,
    }

    impl TestHome {
        fn new() -> Self {
            Self {
                root: std::env::temp_dir().join(format!("paseo-core-test-{}", Uuid::now_v7())),
            }
        }

        fn home(&self) -> PathBuf {
            self.root.join("home")
        }

        fn workspace(&self) -> PathBuf {
            self.root.join("workspace")
        }
    }

    impl Drop for TestHome {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.root);
        }
    }

    struct FixtureProvider {
        id: ProviderId,
        next_session: AtomicUsize,
        prompts: Arc<AtomicUsize>,
    }

    impl FixtureProvider {
        fn new(id: ProviderId) -> Self {
            Self {
                id,
                next_session: AtomicUsize::new(0),
                prompts: Arc::new(AtomicUsize::new(0)),
            }
        }

        fn prompts(&self) -> usize {
            self.prompts.load(AtomicOrdering::Relaxed)
        }

        fn session(
            &self,
            permissions: Arc<dyn PermissionHandler>,
            native_id: String,
        ) -> Box<dyn ProviderSession> {
            Box::new(FixtureSession {
                id: self.id.clone(),
                native_id,
                permissions,
                prompts: self.prompts.clone(),
            })
        }
    }

    #[async_trait::async_trait]
    impl AgentProvider for FixtureProvider {
        fn id(&self) -> &ProviderId {
            &self.id
        }

        async fn diagnostic(&self) -> ProviderDiagnostic {
            ProviderDiagnostic {
                descriptor: ProviderDescriptor {
                    id: self.id.clone(),
                    display_name: "Fixture".into(),
                    available: true,
                    capabilities: fixture_capabilities(),
                    modes: fixture_modes(),
                    models: fixture_models(&self.id),
                },
                command: PathBuf::from("fixture"),
                executable_found: true,
                message: None,
            }
        }

        async fn create_session(
            &self,
            _cwd: &Path,
            permissions: Arc<dyn PermissionHandler>,
        ) -> Result<Box<dyn ProviderSession>, ProviderError> {
            let number = self.next_session.fetch_add(1, AtomicOrdering::Relaxed);
            Ok(self.session(permissions, format!("fixture-{number}")))
        }

        async fn resume_session(
            &self,
            _cwd: &Path,
            native_session_id: &str,
            permissions: Arc<dyn PermissionHandler>,
        ) -> Result<Box<dyn ProviderSession>, ProviderError> {
            self.next_session.fetch_add(1, AtomicOrdering::Relaxed);
            Ok(self.session(permissions, native_session_id.to_owned()))
        }
    }

    struct FixtureSession {
        id: ProviderId,
        native_id: String,
        permissions: Arc<dyn PermissionHandler>,
        prompts: Arc<AtomicUsize>,
    }

    #[async_trait::async_trait]
    impl ProviderSession for FixtureSession {
        fn provider_id(&self) -> &ProviderId {
            &self.id
        }

        fn native_session_id(&self) -> &str {
            &self.native_id
        }

        fn capabilities(&self) -> paseo_domain::Capabilities {
            fixture_capabilities()
        }

        fn modes(&self) -> Vec<ProviderMode> {
            fixture_modes()
        }

        fn models(&self) -> Vec<ModelRef> {
            fixture_models(&self.id)
        }

        fn config_options(&self) -> Vec<paseo_acp::ConfigOption> {
            fixture_config_options()
        }

        async fn prompt(
            &self,
            _text: &str,
            cancellation: &CancellationToken,
        ) -> Result<ProviderPromptResult, ProviderError> {
            self.prompts.fetch_add(1, AtomicOrdering::Relaxed);
            let request = ProviderPermissionRequest {
                native_session_id: self.native_id.clone(),
                title: "fixture operation".into(),
                kind: Some("execute".into()),
                tool_call_id: "tool-1".into(),
                raw_input: Some(json!({"command":"true"})),
                locations: Vec::new(),
                options: vec![
                    ProviderPermissionOption {
                        id: "allow_once".into(),
                        label: "Allow once".into(),
                        kind: Some("allow_once".into()),
                    },
                    ProviderPermissionOption {
                        id: "deny".into(),
                        label: "Deny".into(),
                        kind: Some("deny".into()),
                    },
                ],
            };
            let permission_cancellation = CancellationToken::new();
            let permission = self
                .permissions
                .request_permission(request, permission_cancellation.clone());
            tokio::pin!(permission);
            tokio::select! {
                decision = &mut permission => Ok(ProviderPromptResult {
                    stop_reason: match decision {
                        ProviderPermissionDecision::Selected(_) => "end_turn",
                        ProviderPermissionDecision::Cancelled => "cancelled",
                    }.into(),
                    usage: None,
                }),
                () = cancellation.cancelled() => {
                    // Mirrors ACP's outer cancellation select, which drops the handler future.
                    permission_cancellation.cancel();
                    Ok(ProviderPromptResult { stop_reason: "cancelled".into(), usage: None })
                }
            }
        }

        async fn cancel(&self) -> Result<(), ProviderError> {
            Ok(())
        }

        async fn set_mode(&self, _mode_id: &str) -> Result<(), ProviderError> {
            Ok(())
        }

        async fn set_model(&self, _model_id: &str) -> Result<(), ProviderError> {
            Ok(())
        }

        async fn set_config_option(&self, _id: &str, _value: &str) -> Result<(), ProviderError> {
            Ok(())
        }

        async fn next_event(&self) -> Option<ProviderEvent> {
            std::future::pending().await
        }

        async fn shutdown(&self, _grace: Duration) {}
    }

    fn fixture_capabilities() -> Capabilities {
        Capabilities {
            streaming: true,
            modes: true,
            permissions: true,
            model_selection: true,
            config_options: true,
            ..Capabilities::default()
        }
    }

    fn fixture_modes() -> Vec<ProviderMode> {
        vec![ProviderMode::new("fast", "Fast").unwrap()]
    }

    fn fixture_models(provider: &ProviderId) -> Vec<ModelRef> {
        vec![ModelRef::new(provider.clone(), "fixture-model").unwrap()]
    }

    fn fixture_config_options() -> Vec<paseo_acp::ConfigOption> {
        vec![
            serde_json::from_value(json!({
                "id": "thought_level",
                "name": "Thinking",
                "type": "select",
                "options": [{"value": "low", "name": "Low"}]
            }))
            .unwrap(),
        ]
    }

    async fn fixture_service(home: &TestHome) -> (AppService, SessionId) {
        let (service, session_id, _) = fixture_service_with_provider(home).await;
        (service, session_id)
    }

    async fn fixture_service_with_provider(
        home: &TestHome,
    ) -> (AppService, SessionId, Arc<FixtureProvider>) {
        tokio::fs::create_dir_all(home.workspace()).await.unwrap();
        let service = AppService::open_at(home.home()).await.unwrap();
        let provider_id = ProviderId::new("fixture").unwrap();
        let provider = Arc::new(FixtureProvider::new(provider_id.clone()));
        service.register_provider(provider.clone()).await;
        let workspace = service
            .create_workspace("fixture", home.workspace(), WorkspaceKind::Directory)
            .await
            .unwrap();
        let created = service
            .create_agent(workspace.id, Some(provider_id), "fixture agent")
            .await
            .unwrap();
        (service, created.session.id, provider)
    }

    async fn wait_for_permissions(
        service: &AppService,
        expected: usize,
    ) -> Vec<PendingPermissionSummary> {
        tokio::time::timeout(Duration::from_secs(5), async {
            loop {
                let pending = service.list_pending_permissions().await.unwrap();
                if pending.len() == expected {
                    return pending;
                }
                tokio::task::yield_now().await;
            }
        })
        .await
        .expect("permission requests should arrive")
    }

    struct StalledProvider {
        id: ProviderId,
    }

    #[async_trait::async_trait]
    impl AgentProvider for StalledProvider {
        fn id(&self) -> &ProviderId {
            &self.id
        }

        async fn diagnostic(&self) -> ProviderDiagnostic {
            ProviderDiagnostic {
                descriptor: ProviderDescriptor {
                    id: self.id.clone(),
                    display_name: "Stalled".into(),
                    available: true,
                    capabilities: fixture_capabilities(),
                    modes: Vec::new(),
                    models: Vec::new(),
                },
                command: PathBuf::from("stalled"),
                executable_found: true,
                message: None,
            }
        }

        async fn create_session(
            &self,
            _cwd: &Path,
            _permissions: Arc<dyn PermissionHandler>,
        ) -> Result<Box<dyn ProviderSession>, ProviderError> {
            Ok(Box::new(StalledSession {
                id: self.id.clone(),
                native_id: "stalled-native".into(),
            }))
        }

        async fn resume_session(
            &self,
            _cwd: &Path,
            _native_session_id: &str,
            permissions: Arc<dyn PermissionHandler>,
        ) -> Result<Box<dyn ProviderSession>, ProviderError> {
            self.create_session(_cwd, permissions).await
        }
    }

    struct StalledSession {
        id: ProviderId,
        native_id: String,
    }

    #[async_trait::async_trait]
    impl ProviderSession for StalledSession {
        fn provider_id(&self) -> &ProviderId {
            &self.id
        }

        fn native_session_id(&self) -> &str {
            &self.native_id
        }

        fn capabilities(&self) -> paseo_domain::Capabilities {
            fixture_capabilities()
        }

        fn modes(&self) -> Vec<ProviderMode> {
            Vec::new()
        }

        fn models(&self) -> Vec<ModelRef> {
            Vec::new()
        }

        fn config_options(&self) -> Vec<paseo_acp::ConfigOption> {
            Vec::new()
        }

        async fn prompt(
            &self,
            _text: &str,
            _cancellation: &CancellationToken,
        ) -> Result<ProviderPromptResult, ProviderError> {
            std::future::pending().await
        }

        async fn cancel(&self) -> Result<(), ProviderError> {
            Ok(())
        }

        async fn set_mode(&self, _mode_id: &str) -> Result<(), ProviderError> {
            Ok(())
        }

        async fn set_model(&self, _model_id: &str) -> Result<(), ProviderError> {
            Ok(())
        }

        async fn set_config_option(&self, _id: &str, _value: &str) -> Result<(), ProviderError> {
            Ok(())
        }

        async fn next_event(&self) -> Option<ProviderEvent> {
            std::future::pending::<Option<ProviderEvent>>().await
        }

        async fn shutdown(&self, _grace: Duration) {
            std::future::pending::<()>().await;
        }
    }

    async fn fixture_stalled_service(home: &TestHome) -> (AppService, SessionId) {
        tokio::fs::create_dir_all(home.workspace()).await.unwrap();
        let service = AppService::open_at(home.home()).await.unwrap();
        let provider_id = ProviderId::new("stalled").unwrap();
        service
            .register_provider(Arc::new(StalledProvider {
                id: provider_id.clone(),
            }))
            .await;
        let workspace = service
            .create_workspace("stalled", home.workspace(), WorkspaceKind::Directory)
            .await
            .unwrap();
        let created = service
            .create_agent(workspace.id, Some(provider_id), "stalled agent")
            .await
            .unwrap();
        (service, created.session.id)
    }

    #[tokio::test]
    async fn stalled_provider_cancellation_settles_durably_without_unbounded_shutdown() {
        let home = TestHome::new();
        let (service, session_id) = fixture_stalled_service(&home).await;
        let run_id = service.send_prompt(session_id, "stall").await.unwrap();
        service.cancel_run(run_id).await.unwrap();
        assert_eq!(
            tokio::time::timeout(Duration::from_secs(10), service.wait_run(run_id))
                .await
                .expect("cancelled run should settle despite an unresponsive provider shutdown")
                .unwrap(),
            RunLifecycle::Cancelled
        );
        let events = service.replay_events(session_id, None, 100).await.unwrap();
        assert!(events.iter().any(|event| matches!(
            event.kind,
            TimelineEventKind::RunState {
                state: RunLifecycle::Cancelled
            }
        )));
        service.shutdown().await;
    }

    #[tokio::test]
    async fn keyed_prompt_retry_is_idempotent_while_busy_and_conflicts_fail() {
        let home = TestHome::new();
        let (service, session_id, provider) = fixture_service_with_provider(&home).await;
        let first = service
            .send_prompt_keyed(session_id, "wait for permission", "key-busy")
            .await
            .unwrap();
        assert_eq!(first.state, RunLifecycle::Running);
        assert!(!first.restored);
        wait_for_permissions(&service, 1).await;

        let retry = service
            .send_prompt_keyed(session_id, "wait for permission", "key-busy")
            .await
            .unwrap();
        assert_eq!(retry.run_id, first.run_id);
        assert_eq!(retry.state, RunLifecycle::Running);
        assert!(retry.restored);
        assert_eq!(provider.prompts(), 1);

        assert!(matches!(
            service
                .send_prompt_keyed(session_id, "different", "key-busy")
                .await,
            Err(CoreError::PromptRequestKeyConflict { session }) if session == session_id
        ));
        assert!(matches!(
            service
                .send_prompt_keyed(session_id, "wait for permission", "   ")
                .await,
            Err(CoreError::EmptyPromptRequestKey)
        ));
        assert!(matches!(
            service
                .send_prompt_keyed(
                    session_id,
                    "wait for permission",
                    "k".repeat(MAX_PROMPT_REQUEST_KEY_BYTES + 1)
                )
                .await,
            Err(CoreError::PromptRequestKeyTooLong(
                MAX_PROMPT_REQUEST_KEY_BYTES
            ))
        ));

        let events = service.replay_events(session_id, None, 100).await.unwrap();
        assert_eq!(
            events
                .iter()
                .filter(|event| matches!(
                    &event.kind,
                    TimelineEventKind::UserText { text } if text == "wait for permission"
                ))
                .count(),
            1
        );

        service.cancel_run(first.run_id).await.unwrap();
        assert_eq!(
            tokio::time::timeout(Duration::from_secs(5), service.wait_run(first.run_id))
                .await
                .unwrap()
                .unwrap(),
            RunLifecycle::Cancelled
        );
        service.shutdown().await;
    }

    #[tokio::test]
    async fn keyed_prompts_are_scoped_per_session() {
        let home = TestHome::new();
        let (service, first_session, provider) = fixture_service_with_provider(&home).await;
        let workspace = service.list_workspaces().await.unwrap().remove(0);
        let second = service
            .create_agent(
                workspace.id,
                Some(ProviderId::new("fixture").unwrap()),
                "second agent",
            )
            .await
            .unwrap();
        let first = service
            .send_prompt_keyed(first_session, "wait for permission", "shared")
            .await
            .unwrap();
        let second_run = service
            .send_prompt_keyed(second.session.id, "wait for permission", "shared")
            .await
            .unwrap();
        assert_ne!(first.run_id, second_run.run_id);
        assert!(!first.restored);
        assert!(!second_run.restored);
        wait_for_permissions(&service, 2).await;
        assert_eq!(provider.prompts(), 2);

        let first_retry = service
            .send_prompt_keyed(first_session, "wait for permission", "shared")
            .await
            .unwrap();
        let second_retry = service
            .send_prompt_keyed(second.session.id, "wait for permission", "shared")
            .await
            .unwrap();
        assert_eq!(first_retry.run_id, first.run_id);
        assert_eq!(second_retry.run_id, second_run.run_id);
        assert!(first_retry.restored);
        assert!(second_retry.restored);
        assert_eq!(provider.prompts(), 2);

        service.cancel_run(first.run_id).await.unwrap();
        service.cancel_run(second_run.run_id).await.unwrap();
        let _ = service.wait_run(first.run_id).await.unwrap();
        let _ = service.wait_run(second_run.run_id).await.unwrap();
        service.shutdown().await;
    }

    #[tokio::test]
    async fn concurrent_keyed_retries_share_one_run() {
        let home = TestHome::new();
        let (service, session_id, provider) = fixture_service_with_provider(&home).await;
        let (left, right) = tokio::join!(
            service.send_prompt_keyed(session_id, "wait for permission", "concurrent"),
            service.send_prompt_keyed(session_id, "wait for permission", "concurrent")
        );
        let left = left.unwrap();
        let right = right.unwrap();
        assert_eq!(left.run_id, right.run_id);
        assert_ne!(left.restored, right.restored);
        wait_for_permissions(&service, 1).await;
        assert_eq!(provider.prompts(), 1);
        service.cancel_run(left.run_id).await.unwrap();
        let _ = service.wait_run(left.run_id).await.unwrap();
        service.shutdown().await;
    }

    #[tokio::test]
    async fn keyed_prompt_retry_survives_restart_without_reexecution() {
        let home = TestHome::new();
        let (service, session_id, provider) = fixture_service_with_provider(&home).await;
        let first = service
            .send_prompt_keyed(session_id, "wait for permission", "restart-key")
            .await
            .unwrap();
        wait_for_permissions(&service, 1).await;
        assert_eq!(provider.prompts(), 1);
        service.shutdown().await;
        assert_eq!(
            service.run_state(first.run_id).await.unwrap(),
            RunLifecycle::Cancelled
        );

        let reopened = AppService::open_at(home.home()).await.unwrap();
        let provider_dyn: Arc<dyn AgentProvider> = provider.clone();
        reopened.register_provider(provider_dyn).await;
        assert!(
            !reopened
                .inner
                .sessions
                .read()
                .await
                .contains_key(&session_id)
        );
        let retry = reopened
            .send_prompt_keyed(session_id, "wait for permission", "restart-key")
            .await
            .unwrap();
        assert_eq!(retry.run_id, first.run_id);
        assert_eq!(retry.state, RunLifecycle::Cancelled);
        assert!(retry.restored);
        assert_eq!(provider.prompts(), 1);
        let events = reopened.replay_events(session_id, None, 100).await.unwrap();
        assert_eq!(
            events
                .iter()
                .filter(|event| matches!(
                    &event.kind,
                    TimelineEventKind::UserText { text } if text == "wait for permission"
                ))
                .count(),
            1
        );
        reopened.shutdown().await;
    }

    #[tokio::test]
    async fn accepted_keyed_prompt_reconciles_to_interrupted_without_reexecution() {
        let home = TestHome::new();
        let (service, session_id, provider) = fixture_service_with_provider(&home).await;
        service.shutdown().await;

        let run_id = RunId::new();
        let record = PromptRequestRecord {
            session_id,
            request_key: "crash-key".into(),
            payload_fingerprint: prompt_payload_fingerprint("wait for permission"),
            run_id,
            accepted_at: Utc::now(),
        };
        let events = vec![
            TimelineEvent::new(
                session_id,
                Some(run_id),
                1,
                TimelineEventKind::UserText {
                    text: "wait for permission".into(),
                },
            )
            .unwrap(),
            TimelineEvent::new(
                session_id,
                Some(run_id),
                2,
                TimelineEventKind::RunState {
                    state: RunLifecycle::Starting,
                },
            )
            .unwrap(),
        ];
        let mut store = paseo_store::SqliteStore::open(home.home().join("state.sqlite3")).unwrap();
        assert!(matches!(
            store.accept_prompt_request(&record, &events).unwrap(),
            PromptRequestAcceptance::Accepted(_)
        ));
        drop(store);

        let reopened = AppService::open_at(home.home()).await.unwrap();
        let provider_dyn: Arc<dyn AgentProvider> = provider.clone();
        reopened.register_provider(provider_dyn).await;
        let retry = reopened
            .send_prompt_keyed(session_id, "wait for permission", "crash-key")
            .await
            .unwrap();
        assert_eq!(retry.run_id, run_id);
        assert_eq!(retry.state, RunLifecycle::Interrupted);
        assert!(retry.restored);
        assert_eq!(provider.prompts(), 0);
        let events = reopened.replay_events(session_id, None, 100).await.unwrap();
        assert_eq!(
            events
                .iter()
                .filter(|event| matches!(
                    &event.kind,
                    TimelineEventKind::UserText { text } if text == "wait for permission"
                ))
                .count(),
            1
        );
        assert_eq!(
            events
                .iter()
                .filter(|event| matches!(
                    event.kind,
                    TimelineEventKind::RunState {
                        state: RunLifecycle::Interrupted
                    }
                ))
                .count(),
            1
        );
        reopened.shutdown().await;
    }

    #[tokio::test]
    async fn unknown_key_requires_loaded_session_and_does_not_leak_acceptance() {
        let home = TestHome::new();
        let (service, session_id, provider) = fixture_service_with_provider(&home).await;
        service.shutdown().await;

        let reopened = AppService::open_at(home.home()).await.unwrap();
        let provider_dyn: Arc<dyn AgentProvider> = provider.clone();
        reopened.register_provider(provider_dyn).await;
        assert!(matches!(
            reopened
                .send_prompt_keyed(session_id, "hello", "fresh-key")
                .await,
            Err(CoreError::SessionNotLoaded(id)) if id == session_id
        ));
        let leaked = reopened
            .inner
            .store
            .run(move |store| store.prompt_request(session_id, "fresh-key"))
            .await
            .unwrap();
        assert!(leaked.is_none());

        reopened.load_session(session_id).await.unwrap();
        let accepted = reopened
            .send_prompt_keyed(session_id, "hello", "fresh-key")
            .await
            .unwrap();
        assert!(!accepted.restored);
        let pending = wait_for_permissions(&reopened, 1).await;
        reopened
            .resolve_permission(pending[0].id, "allow_once")
            .await
            .unwrap();
        assert_eq!(
            tokio::time::timeout(Duration::from_secs(5), reopened.wait_run(accepted.run_id))
                .await
                .unwrap()
                .unwrap(),
            RunLifecycle::Succeeded
        );
        assert_eq!(provider.prompts(), 1);
        reopened.shutdown().await;
    }

    #[tokio::test]
    async fn keyed_retry_is_available_after_session_unload_by_archive() {
        let home = TestHome::new();
        let (service, session_id, provider) = fixture_service_with_provider(&home).await;
        let first = service
            .send_prompt_keyed(session_id, "wait for permission", "archive-key")
            .await
            .unwrap();
        let pending = wait_for_permissions(&service, 1).await;
        service
            .resolve_permission(pending[0].id, "allow_once")
            .await
            .unwrap();
        assert_eq!(
            service.wait_run(first.run_id).await.unwrap(),
            RunLifecycle::Succeeded
        );
        service.archive_session(session_id).await.unwrap();
        assert!(
            !service
                .inner
                .sessions
                .read()
                .await
                .contains_key(&session_id)
        );

        let retry = service
            .send_prompt_keyed(session_id, "wait for permission", "archive-key")
            .await
            .unwrap();
        assert_eq!(retry.run_id, first.run_id);
        assert_eq!(retry.state, RunLifecycle::Succeeded);
        assert!(retry.restored);
        assert_eq!(provider.prompts(), 1);
        service.shutdown().await;
    }

    #[tokio::test]
    async fn dropping_keyed_caller_during_acceptance_does_not_strand_the_run() {
        let home = TestHome::new();
        let (service, session_id, provider) = fixture_service_with_provider(&home).await;
        let caller = service.clone();
        let handle = tokio::spawn(async move {
            caller
                .send_prompt_keyed(session_id, "wait for permission", "drop-key")
                .await
        });
        tokio::task::yield_now().await;
        tokio::time::sleep(Duration::from_millis(5)).await;
        handle.abort();
        let _ = handle.await;

        let retry = tokio::time::timeout(
            Duration::from_secs(5),
            service.send_prompt_keyed(session_id, "wait for permission", "drop-key"),
        )
        .await
        .expect("retry must not block on a stranded active-run slot")
        .unwrap();
        let pending = wait_for_permissions(&service, 1).await;
        service
            .resolve_permission(pending[0].id, "allow_once")
            .await
            .unwrap();
        assert_eq!(
            tokio::time::timeout(Duration::from_secs(5), service.wait_run(retry.run_id))
                .await
                .unwrap()
                .unwrap(),
            RunLifecycle::Succeeded
        );
        assert_eq!(provider.prompts(), 1);

        let follow = service
            .send_prompt_keyed(session_id, "wait for permission", "follow-key")
            .await
            .unwrap();
        let pending = wait_for_permissions(&service, 1).await;
        service
            .resolve_permission(pending[0].id, "allow_once")
            .await
            .unwrap();
        assert_eq!(
            service.wait_run(follow.run_id).await.unwrap(),
            RunLifecycle::Succeeded
        );
        assert_eq!(provider.prompts(), 2);
        let events = service.replay_events(session_id, None, 100).await.unwrap();
        assert_eq!(
            events
                .iter()
                .filter(|event| matches!(
                    &event.kind,
                    TimelineEventKind::UserText { text } if text == "wait for permission"
                ))
                .count(),
            2
        );
        service.shutdown().await;
    }

    #[tokio::test]
    async fn cancel_with_permission_wait_settles_durably_and_releases_run_slot() {
        let home = TestHome::new();
        let (service, session_id) = fixture_service(&home).await;
        let run_id = service
            .send_prompt(session_id, "wait for permission")
            .await
            .unwrap();
        let pending = wait_for_permissions(&service, 1).await;
        service.cancel_run(run_id).await.unwrap();
        assert_eq!(
            tokio::time::timeout(Duration::from_secs(3), service.wait_run(run_id))
                .await
                .unwrap()
                .unwrap(),
            RunLifecycle::Cancelled
        );
        assert_eq!(
            service.list_pending_permissions().await.unwrap(),
            Vec::<PendingPermissionSummary>::new()
        );
        assert!(matches!(
            service
                .resolve_permission(pending[0].id, "allow_once")
                .await,
            Err(CoreError::PermissionNotPending(_))
        ));

        let events = service.replay_events(session_id, None, 100).await.unwrap();
        let permission_requested = events
            .iter()
            .position(|event| matches!(event.kind, TimelineEventKind::PermissionRequested(_)))
            .unwrap();
        let permission_resolved = events
            .iter()
            .position(|event| {
                matches!(
                    event.kind,
                    TimelineEventKind::PermissionResolved(PermissionOutcome::Cancelled)
                )
            })
            .unwrap();
        let terminal = events
            .iter()
            .position(|event| {
                matches!(
                    event.kind,
                    TimelineEventKind::RunState {
                        state: RunLifecycle::Cancelled
                    }
                )
            })
            .unwrap();
        assert!(permission_requested < permission_resolved && permission_resolved < terminal);

        let followup = service.send_prompt(session_id, "follow-up").await.unwrap();
        wait_for_permissions(&service, 1).await;
        service.cancel_run(followup).await.unwrap();
        assert_eq!(
            service.wait_run(followup).await.unwrap(),
            RunLifecycle::Cancelled
        );
        service.shutdown().await;
    }

    #[tokio::test]
    async fn provider_mutations_validate_advertised_values_and_merge_agent_state() {
        let home = TestHome::new();
        let (service, session_id) = fixture_service(&home).await;
        assert!(matches!(
            service.set_model(session_id, "unadvertised").await,
            Err(CoreError::Provider(ProviderError::InvalidOption { .. }))
        ));
        assert!(matches!(
            service.set_mode(session_id, "slow").await,
            Err(CoreError::Provider(ProviderError::InvalidOption { .. }))
        ));
        assert!(matches!(
            service
                .set_config_option(session_id, "thought_level", "turbo")
                .await,
            Err(CoreError::Provider(ProviderError::InvalidOption { .. }))
        ));

        service
            .set_model(session_id, "fixture-model")
            .await
            .unwrap();
        service.set_mode(session_id, "fast").await.unwrap();
        service
            .set_config_option(session_id, "thought_level", "low")
            .await
            .unwrap();
        let agent = service
            .agent(
                service
                    .list_agents(service.list_workspaces().await.unwrap()[0].id)
                    .await
                    .unwrap()[0]
                    .id,
            )
            .await
            .unwrap();
        assert_eq!(agent.model.unwrap().model_id, "fixture-model");
        assert_eq!(agent.mode.as_deref(), Some("fast"));
        service.shutdown().await;
    }

    #[tokio::test]
    async fn concurrent_permission_decisions_are_run_scoped_and_stale_ids_fail_closed() {
        let home = TestHome::new();
        let (service, first_session) = fixture_service(&home).await;
        let workspace = service.list_workspaces().await.unwrap().remove(0);
        let second_agent = service
            .create_agent(
                workspace.id,
                Some(ProviderId::new("fixture").unwrap()),
                "second agent",
            )
            .await
            .unwrap();
        let first_run = service.send_prompt(first_session, "first").await.unwrap();
        let second_run = service
            .send_prompt(second_agent.session.id, "second")
            .await
            .unwrap();
        let pending = wait_for_permissions(&service, 2).await;
        let first_permission = pending
            .iter()
            .find(|item| item.run_id == first_run)
            .unwrap();
        let second_permission = pending
            .iter()
            .find(|item| item.run_id == second_run)
            .unwrap();
        service
            .resolve_permission(first_permission.id, "allow_once")
            .await
            .unwrap();
        assert_eq!(
            service.wait_run(first_run).await.unwrap(),
            RunLifecycle::Succeeded
        );
        let remaining = service.list_pending_permissions().await.unwrap();
        assert_eq!(remaining.len(), 1);
        assert_eq!(remaining[0].id, second_permission.id);
        service.cancel_run(second_run).await.unwrap();
        assert_eq!(
            service.wait_run(second_run).await.unwrap(),
            RunLifecycle::Cancelled
        );
        assert_eq!(
            service.list_pending_permissions().await.unwrap(),
            Vec::<PendingPermissionSummary>::new()
        );
        assert!(matches!(
            service
                .resolve_permission(second_permission.id, "allow_once")
                .await,
            Err(CoreError::PermissionNotPending(_))
        ));
        service.shutdown().await;
    }

    #[tokio::test]
    async fn concurrent_loads_install_only_one_provider_runtime() {
        let home = TestHome::new();
        let (service, session_id) = fixture_service(&home).await;
        service.shutdown().await;

        let reopened = AppService::open_at(home.home()).await.unwrap();
        let provider = Arc::new(FixtureProvider::new(ProviderId::new("fixture").unwrap()));
        reopened.register_provider(provider.clone()).await;
        let (left, right) = tokio::join!(
            reopened.load_session(session_id),
            reopened.load_session(session_id)
        );
        assert_eq!(left.unwrap().id, session_id);
        assert_eq!(right.unwrap().id, session_id);
        assert_eq!(provider.next_session.load(AtomicOrdering::Relaxed), 1);
        reopened.shutdown().await;
    }

    #[tokio::test]
    async fn shutdown_joins_run_writers_before_closing_store() {
        let home = TestHome::new();
        let (service, session_id) = fixture_service(&home).await;
        let run_id = service
            .send_prompt(session_id, "wait for permission")
            .await
            .unwrap();
        wait_for_permissions(&service, 1).await;
        service.shutdown().await;
        assert_eq!(
            service.run_state(run_id).await.unwrap(),
            RunLifecycle::Cancelled
        );

        let reopened = AppService::open_at(home.home()).await.unwrap();
        assert_eq!(
            reopened.run_state(run_id).await.unwrap(),
            RunLifecycle::Cancelled
        );
        let events = reopened.replay_events(session_id, None, 100).await.unwrap();
        assert!(events.iter().any(|event| matches!(
            event.kind,
            TimelineEventKind::RunState {
                state: RunLifecycle::Cancelled
            }
        )));
        assert!(events.iter().any(|event| matches!(
            event.kind,
            TimelineEventKind::PermissionResolved(PermissionOutcome::Cancelled)
        )));
        reopened.shutdown().await;
    }

    fn seed_interrupted_run(home: &TestHome, session_id: SessionId, agent_id: AgentId) -> RunId {
        let run_id = RunId::new();
        let permission = PermissionRequest {
            id: PermissionRequestId::new(),
            run_id,
            action: "fixture operation".into(),
            target: None,
            risk: PermissionRisk::High,
            details: json!({}),
            expires_at: None,
        };
        let kinds = [
            TimelineEventKind::UserText {
                text: "crash".into(),
            },
            TimelineEventKind::RunState {
                state: RunLifecycle::Running,
            },
            TimelineEventKind::PermissionRequested(permission),
        ];
        let events = kinds
            .into_iter()
            .enumerate()
            .map(|(index, kind)| {
                TimelineEvent::new(session_id, Some(run_id), index as u64 + 1, kind).unwrap()
            })
            .collect::<Vec<_>>();
        let mut store = paseo_store::SqliteStore::open(home.home().join("state.sqlite3")).unwrap();
        let mut agent = store.agent(agent_id).unwrap().unwrap();
        agent.lifecycle = AgentLifecycle::Busy;
        store.save_agent(&agent).unwrap();
        store.append_events(&events).unwrap();
        run_id
    }

    fn assert_recovery_is_idempotent(events: &[TimelineEvent]) {
        assert_eq!(
            events
                .iter()
                .filter(|event| matches!(
                    event.kind,
                    TimelineEventKind::RunState {
                        state: RunLifecycle::Interrupted
                    }
                ))
                .count(),
            1
        );
        assert_eq!(
            events
                .iter()
                .filter(|event| matches!(
                    event.kind,
                    TimelineEventKind::PermissionResolved(PermissionOutcome::Cancelled)
                ))
                .count(),
            1
        );
    }

    #[tokio::test]
    async fn startup_marks_orphaned_runs_interrupted_and_cancels_permissions_once() {
        let home = TestHome::new();
        let (service, session_id) = fixture_service(&home).await;
        let workspace_id = service.list_workspaces().await.unwrap()[0].id;
        let agent_id = service.list_agents(workspace_id).await.unwrap()[0].id;
        service.shutdown().await;
        let run_id = seed_interrupted_run(&home, session_id, agent_id);

        let recovered = AppService::open_at(home.home()).await.unwrap();
        assert_eq!(
            recovered.run_state(run_id).await.unwrap(),
            RunLifecycle::Interrupted
        );
        assert_eq!(
            recovered.agent(agent_id).await.unwrap().lifecycle,
            AgentLifecycle::Ready
        );
        let events = recovered
            .replay_events(session_id, None, 100)
            .await
            .unwrap();
        assert_recovery_is_idempotent(&events);
        recovered.shutdown().await;

        let recovered_again = AppService::open_at(home.home()).await.unwrap();
        let events = recovered_again
            .replay_events(session_id, None, 100)
            .await
            .unwrap();
        assert_recovery_is_idempotent(&events);
        recovered_again.shutdown().await;
    }
}
