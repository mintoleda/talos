use std::{
    cmp::Reverse,
    collections::{BTreeMap, BinaryHeap, HashSet, VecDeque},
    path::{Path, PathBuf},
    sync::Arc,
    time::Duration,
};

use serde_json::{Value, json};
use talos_core::{AppService, CoreError, HomePaths, ProviderDiagnostic, ProviderError};
use talos_domain::{
    AgentId, EventCursor, PermissionRequestId, ProjectId, ProviderId, RunLifecycle, Session,
    SessionId, TimelineEvent, TimelineEventKind, WorkspaceId, WorkspaceKind, WorkspaceLifecycle,
};
use talos_ipc::{
    Command, EventNotification, PROTOCOL_VERSION, PermissionDecision, RpcError, WireEvent,
    serve_connection_with_events,
};
use tokio::{
    io::AsyncWriteExt,
    sync::{Mutex, Notify, broadcast},
};
use tracing::{error, info, warn};
use uuid::Uuid;

mod instance;
mod terminal;
use instance::InstanceGuard;
use terminal::TerminalManager;

#[derive(Clone)]
struct Daemon {
    home: PathBuf,
    service: AppService,
    event_tx: broadcast::Sender<EventNotification>,
    shutdown: Arc<Notify>,
    terminals: TerminalManager,
    terminal_workspace_gate: Arc<Mutex<()>>,
}

impl Daemon {
    fn new(
        home: PathBuf,
        service: AppService,
        shutdown: Arc<Notify>,
    ) -> (Self, tokio::task::JoinHandle<()>) {
        let (event_tx, _) = broadcast::channel(256);
        // Subscribe synchronously before accepting any commands.
        let mut events = service.subscribe_events();
        let daemon = Self {
            home,
            service,
            event_tx: event_tx.clone(),
            shutdown,
            terminals: TerminalManager::new(),
            terminal_workspace_gate: Arc::new(Mutex::new(())),
        };
        let bridge = tokio::spawn(async move {
            loop {
                match events.recv().await {
                    Ok(event) => {
                        let _ = event_tx.send(event_notification(event));
                    }
                    Err(broadcast::error::RecvError::Lagged(skipped)) => {
                        warn!(skipped, "daemon event bridge lagged; replay required");
                        let _ = event_tx.send(EventNotification::replay_required(
                            "daemon_event_lag",
                            skipped,
                        ));
                    }
                    Err(broadcast::error::RecvError::Closed) => break,
                }
            }
        });
        (daemon, bridge)
    }

    #[allow(clippy::too_many_lines)] // Central exhaustive IPC dispatch keeps wire handling auditable.
    async fn command(&self, command: Command) -> Result<Value, RpcError> {
        match command {
            Command::Hello { .. } => Err(RpcError::new(-32600, "hello must be first")),
            Command::Health => self.health().await,
            Command::Snapshot => self.snapshot().await,
            Command::ListWorkspaces => self.list_workspaces().await,
            Command::GetWorkspace { workspace_id } => self.get_workspace(&workspace_id).await,
            Command::CreateWorkspace { name, path } => self.create_workspace(name, path).await,
            Command::RenameWorkspace { workspace_id, name } => {
                self.rename_workspace(&workspace_id, name).await
            }
            Command::ArchiveWorkspace { workspace_id } => {
                self.archive_workspace(&workspace_id).await
            }
            Command::RestoreWorkspace { workspace_id } => {
                self.restore_workspace(&workspace_id).await
            }
            Command::CreateProject { name, root_path } => {
                self.create_project(name, root_path).await
            }
            Command::ListProjects => self.list_projects().await,
            Command::GetProject { project_id } => self.get_project(&project_id).await,
            Command::RenameProject { project_id, name } => {
                self.rename_project(&project_id, name).await
            }
            Command::DeleteProject { project_id } => self.delete_project(&project_id).await,
            Command::SetWorkspaceProject {
                workspace_id,
                project_id,
            } => {
                self.set_workspace_project(&workspace_id, project_id.as_deref())
                    .await
            }
            Command::CreateManagedWorktree {
                project_id,
                name,
                branch,
                base_ref,
            } => {
                self.create_managed_worktree(&project_id, name, branch, base_ref)
                    .await
            }
            Command::DeleteManagedWorktree { workspace_id } => {
                self.delete_managed_worktree(&workspace_id).await
            }
            Command::ListProviders => Ok(serialize_provider_diagnostics(
                &self.service.provider_diagnostics().await,
            )),
            Command::ProviderDiagnostics { provider_id } => {
                self.provider_diagnostics(provider_id.as_deref()).await
            }
            Command::ListAgents { workspace_id } => self.list_agents(&workspace_id).await,
            Command::GetAgent { agent_id } => self.get_agent(&agent_id).await,
            Command::CreateAgent {
                workspace_id,
                provider_id,
                title,
            } => self.create_agent(&workspace_id, provider_id, title).await,
            Command::ListSessions { workspace_id } => {
                serialize(self.list_sessions(workspace_id.as_deref()).await?)
            }
            Command::GetSession { session_id } => self.get_session(&session_id).await,
            Command::LoadSession { session_id } => self.load_session(&session_id).await,
            Command::Prompt {
                session_id,
                text,
                request_key,
            } => self.prompt(&session_id, text, request_key).await,
            Command::PromptWithImages {
                session_id,
                text,
                image_paths,
                request_key,
            } => {
                self.prompt_with_images(&session_id, text, image_paths, request_key)
                    .await
            }
            Command::Cancel { session_id } => self.cancel(&session_id).await,
            Command::PermissionDecision {
                permission_id,
                decision,
            } => self.permission_decision(&permission_id, decision).await,
            Command::SetModel {
                session_id,
                model_id,
            } => self.set_model(&session_id, model_id).await,
            Command::SetMode {
                session_id,
                mode_id,
            } => self.set_mode(&session_id, mode_id).await,
            Command::SetConfigOption {
                session_id,
                option_id,
                value,
            } => self.set_config_option(&session_id, option_id, value).await,
            Command::ListPendingPermissions => self.list_pending_permissions().await,
            Command::ListModels { provider_id } => self.list_models(&provider_id).await,
            Command::ArchiveSession { session_id } => self.archive_session(&session_id).await,
            Command::RenameSession { session_id, title } => {
                self.rename_session(&session_id, title).await
            }
            Command::SetThinking {
                session_id,
                thinking_option_id,
            } => self.set_thinking(&session_id, thinking_option_id).await,
            Command::UsageSummary { session_id } => self.usage_summary(session_id.as_deref()).await,
            Command::Shutdown => Ok(self.shutdown()),
            Command::Replay { after, limit } => self.replay(after, limit).await,
            Command::ReplaySessions { after, limit } => self.replay_sessions(after, limit).await,
            Command::ReplaySession {
                session_id,
                after,
                limit,
            } => self.replay_session(&session_id, after, limit).await,
            Command::SearchTimeline {
                query,
                session_id,
                limit,
            } => {
                self.search_timeline(query, session_id.as_deref(), limit)
                    .await
            }
            Command::CreateTerminal {
                workspace_id,
                cwd,
                program,
                args,
                rows,
                cols,
            } => {
                self.create_terminal(&workspace_id, cwd.as_deref(), &program, &args, rows, cols)
                    .await
            }
            Command::ListTerminals { workspace_id } => self.list_terminals(&workspace_id).await,
            Command::TerminalInput {
                workspace_id,
                terminal_id,
                data,
            } => self.terminal_input(&workspace_id, &terminal_id, &data),
            Command::ResizeTerminal {
                workspace_id,
                terminal_id,
                rows,
                cols,
            } => self.resize_terminal(&workspace_id, &terminal_id, rows, cols),
            Command::CaptureTerminal {
                workspace_id,
                terminal_id,
                limit,
            } => self.capture_terminal(&workspace_id, &terminal_id, limit),
            Command::KillTerminal {
                workspace_id,
                terminal_id,
            } => self.kill_terminal(&workspace_id, &terminal_id),
        }
    }

    async fn create_terminal(
        &self,
        workspace_value: &str,
        cwd: Option<&str>,
        program: &str,
        args: &[String],
        rows: u16,
        cols: u16,
    ) -> Result<Value, RpcError> {
        let _workspace_operation = self.terminal_workspace_gate.lock().await;
        let workspace_id = parse_id::<WorkspaceId>(workspace_value)?;
        let workspace = self
            .service
            .workspace(workspace_id)
            .await
            .map_err(map_core_error)?;
        if workspace.lifecycle != WorkspaceLifecycle::Active {
            return Err(RpcError::new(-32020, "workspace is not active"));
        }
        let terminal = self
            .terminals
            .create(
                workspace_id.to_string(),
                Path::new(&workspace.path),
                cwd.map(Path::new),
                program,
                args,
                rows,
                cols,
            )
            .map_err(|error| terminal_rpc_error(error.to_string()))?;
        serialize(terminal)
    }

    async fn list_terminals(&self, workspace_value: &str) -> Result<Value, RpcError> {
        let workspace_id = parse_id::<WorkspaceId>(workspace_value)?;
        self.service
            .workspace(workspace_id)
            .await
            .map_err(map_core_error)?;
        serialize(
            self.terminals
                .list(&workspace_id.to_string())
                .map_err(|error| terminal_rpc_error(error.to_string()))?,
        )
    }

    fn terminal_input(
        &self,
        workspace_value: &str,
        terminal_value: &str,
        data: &str,
    ) -> Result<Value, RpcError> {
        let workspace_id = parse_id::<WorkspaceId>(workspace_value)?;
        let terminal_id = parse_id::<Uuid>(terminal_value)?;
        self.terminals
            .input(&workspace_id.to_string(), terminal_id, data)
            .map_err(|error| terminal_rpc_error(error.to_string()))?;
        Ok(json!({"terminal_id": terminal_id, "written": data.len()}))
    }

    fn resize_terminal(
        &self,
        workspace_value: &str,
        terminal_value: &str,
        rows: u16,
        cols: u16,
    ) -> Result<Value, RpcError> {
        let workspace_id = parse_id::<WorkspaceId>(workspace_value)?;
        let terminal_id = parse_id::<Uuid>(terminal_value)?;
        self.terminals
            .resize(&workspace_id.to_string(), terminal_id, rows, cols)
            .map_err(|error| terminal_rpc_error(error.to_string()))?;
        Ok(json!({"terminal_id": terminal_id, "rows": rows, "cols": cols}))
    }

    fn capture_terminal(
        &self,
        workspace_value: &str,
        terminal_value: &str,
        limit: Option<u32>,
    ) -> Result<Value, RpcError> {
        let workspace_id = parse_id::<WorkspaceId>(workspace_value)?;
        let terminal_id = parse_id::<Uuid>(terminal_value)?;
        serialize(
            self.terminals
                .capture(
                    &workspace_id.to_string(),
                    terminal_id,
                    limit.unwrap_or(65_536) as usize,
                )
                .map_err(|error| terminal_rpc_error(error.to_string()))?,
        )
    }

    fn kill_terminal(
        &self,
        workspace_value: &str,
        terminal_value: &str,
    ) -> Result<Value, RpcError> {
        let workspace_id = parse_id::<WorkspaceId>(workspace_value)?;
        let terminal_id = parse_id::<Uuid>(terminal_value)?;
        self.terminals
            .kill(&workspace_id.to_string(), terminal_id)
            .map_err(|error| terminal_rpc_error(error.to_string()))?;
        Ok(json!({"terminal_id": terminal_id, "killed": true}))
    }

    async fn list_pending_permissions(&self) -> Result<Value, RpcError> {
        serialize(
            self.service
                .list_pending_permissions()
                .await
                .map_err(core_error)?,
        )
    }

    async fn health(&self) -> Result<Value, RpcError> {
        let diagnostics = self.service.diagnostics().await.map_err(map_core_error)?;
        Ok(json!({
            "status": "ok",
            "version": env!("CARGO_PKG_VERSION"),
            "schema_version": diagnostics.schema_version,
        }))
    }

    async fn list_workspaces(&self) -> Result<Value, RpcError> {
        serialize(
            self.service
                .list_workspaces()
                .await
                .map_err(map_core_error)?,
        )
    }

    async fn get_workspace(&self, value: &str) -> Result<Value, RpcError> {
        let id = parse_id::<WorkspaceId>(value)?;
        serialize(self.service.workspace(id).await.map_err(map_core_error)?)
    }

    async fn create_workspace(&self, name: String, path: String) -> Result<Value, RpcError> {
        let path = PathBuf::from(path);
        if !path.is_absolute() {
            return Err(RpcError::new(-32602, "workspace path must be absolute"));
        }
        let workspace = self
            .service
            .create_workspace(name, path, WorkspaceKind::Directory)
            .await
            .map_err(map_core_error)?;
        serialize(workspace)
    }

    async fn rename_workspace(&self, value: &str, name: String) -> Result<Value, RpcError> {
        let id = parse_id::<WorkspaceId>(value)?;
        let workspace = self
            .service
            .rename_workspace(id, name)
            .await
            .map_err(map_core_error)?;
        serialize(workspace)
    }

    async fn archive_workspace(&self, value: &str) -> Result<Value, RpcError> {
        let _workspace_operation = self.terminal_workspace_gate.lock().await;
        let id = parse_id::<WorkspaceId>(value)?;
        let workspace = self
            .service
            .archive_workspace(id)
            .await
            .map_err(map_core_error)?;
        self.terminals.kill_workspace(&id.to_string());
        serialize(workspace)
    }

    async fn restore_workspace(&self, value: &str) -> Result<Value, RpcError> {
        let _workspace_operation = self.terminal_workspace_gate.lock().await;
        let id = parse_id::<WorkspaceId>(value)?;
        let workspace = self
            .service
            .restore_workspace(id)
            .await
            .map_err(map_core_error)?;
        serialize(workspace)
    }

    async fn create_project(&self, name: String, root_path: String) -> Result<Value, RpcError> {
        let path = PathBuf::from(root_path);
        if !path.is_absolute() {
            return Err(RpcError::new(-32602, "project root path must be absolute"));
        }
        serialize(
            self.service
                .create_project(name, path)
                .await
                .map_err(map_core_error)?,
        )
    }

    async fn list_projects(&self) -> Result<Value, RpcError> {
        serialize(self.service.list_projects().await.map_err(map_core_error)?)
    }

    async fn get_project(&self, value: &str) -> Result<Value, RpcError> {
        let id = parse_id::<ProjectId>(value)?;
        serialize(self.service.project(id).await.map_err(map_core_error)?)
    }

    async fn rename_project(&self, value: &str, name: String) -> Result<Value, RpcError> {
        let id = parse_id::<ProjectId>(value)?;
        serialize(
            self.service
                .rename_project(id, name)
                .await
                .map_err(map_core_error)?,
        )
    }

    async fn delete_project(&self, value: &str) -> Result<Value, RpcError> {
        let id = parse_id::<ProjectId>(value)?;
        self.service
            .delete_project(id)
            .await
            .map_err(map_core_error)?;
        Ok(json!({"project_id": id, "deleted": true}))
    }

    async fn set_workspace_project(
        &self,
        workspace_value: &str,
        project_value: Option<&str>,
    ) -> Result<Value, RpcError> {
        let workspace_id = parse_id::<WorkspaceId>(workspace_value)?;
        let project_id = project_value.map(parse_id::<ProjectId>).transpose()?;
        serialize(
            self.service
                .set_workspace_project(workspace_id, project_id)
                .await
                .map_err(map_core_error)?,
        )
    }

    async fn create_managed_worktree(
        &self,
        project_value: &str,
        name: String,
        branch: String,
        base_ref: String,
    ) -> Result<Value, RpcError> {
        let project_id = parse_id::<ProjectId>(project_value)?;
        serialize(
            self.service
                .create_managed_worktree(project_id, name, branch, base_ref)
                .await
                .map_err(map_core_error)?,
        )
    }

    async fn delete_managed_worktree(&self, workspace_value: &str) -> Result<Value, RpcError> {
        let _workspace_operation = self.terminal_workspace_gate.lock().await;
        let workspace_id = parse_id::<WorkspaceId>(workspace_value)?;
        if !self
            .terminals
            .list(&workspace_id.to_string())
            .map_err(|error| terminal_rpc_error(error.to_string()))?
            .is_empty()
        {
            return Err(RpcError::new(
                -32009,
                "workspace has active terminals; kill them before removing its worktree",
            ));
        }
        serialize(
            self.service
                .delete_managed_worktree(workspace_id)
                .await
                .map_err(map_core_error)?,
        )
    }

    async fn provider_diagnostics(&self, provider_id: Option<&str>) -> Result<Value, RpcError> {
        let diagnostics: Vec<_> = self
            .service
            .provider_diagnostics()
            .await
            .into_iter()
            .filter(|item| provider_id.is_none_or(|id| item.descriptor.id.as_str() == id))
            .map(|item| provider_diagnostic_value(&item))
            .collect();
        if let Some(id) = provider_id {
            diagnostics
                .into_iter()
                .next()
                .ok_or_else(|| RpcError::new(-32004, format!("provider {id} not found")))
        } else {
            Ok(json!({"providers": diagnostics}))
        }
    }

    async fn list_agents(&self, value: &str) -> Result<Value, RpcError> {
        let id = parse_id::<WorkspaceId>(value)?;
        serialize(self.service.list_agents(id).await.map_err(map_core_error)?)
    }

    async fn get_agent(&self, value: &str) -> Result<Value, RpcError> {
        let id = parse_id::<AgentId>(value)?;
        serialize(self.service.agent(id).await.map_err(map_core_error)?)
    }

    async fn create_agent(
        &self,
        workspace: &str,
        provider: Option<String>,
        title: Option<String>,
    ) -> Result<Value, RpcError> {
        let workspace_id = parse_id::<WorkspaceId>(workspace)?;
        let provider = provider
            .map(ProviderId::new)
            .transpose()
            .map_err(|error| RpcError::new(-32602, error.to_string()))?;
        let created = self
            .service
            .create_agent(
                workspace_id,
                provider,
                title.unwrap_or_else(|| "Agent".into()),
            )
            .await
            .map_err(map_core_error)?;
        Ok(json!({"agent": created.agent, "session": created.session}))
    }

    async fn get_session(&self, value: &str) -> Result<Value, RpcError> {
        let id = parse_id::<SessionId>(value)?;
        let snapshot = self
            .service
            .session_snapshot(id, 1024)
            .await
            .map_err(map_core_error)?;
        Ok(json!({
            "session": snapshot.session,
            "timeline": snapshot.events,
            "cursor": snapshot.cursor,
            "truncated": snapshot.events.len() == 1024,
        }))
    }

    async fn load_session(&self, value: &str) -> Result<Value, RpcError> {
        let id = parse_id::<SessionId>(value)?;
        serialize(
            self.service
                .load_session(id)
                .await
                .map_err(map_core_error)?,
        )
    }

    async fn prompt(
        &self,
        value: &str,
        text: String,
        request_key: Option<String>,
    ) -> Result<Value, RpcError> {
        let id = parse_id::<SessionId>(value)?;
        if let Some(request_key) = request_key {
            let accepted = self
                .service
                .send_prompt_keyed(id, text, request_key.clone())
                .await
                .map_err(map_core_error)?;
            return Ok(json!({
                "run_id": accepted.run_id,
                "request_key": request_key,
                "status": run_status(&accepted.state),
                "restored": accepted.restored,
            }));
        }
        let run_id = self
            .service
            .send_prompt(id, text)
            .await
            .map_err(map_core_error)?;
        Ok(json!({"run_id": run_id, "request_key": Value::Null, "status": "running"}))
    }

    async fn prompt_with_images(
        &self,
        value: &str,
        text: String,
        image_paths: Vec<String>,
        request_key: Option<String>,
    ) -> Result<Value, RpcError> {
        let id = parse_id::<SessionId>(value)?;
        let accepted = self
            .service
            .send_prompt_with_images(
                id,
                text,
                image_paths.into_iter().map(PathBuf::from).collect(),
                request_key.clone(),
            )
            .await
            .map_err(map_core_error)?;
        Ok(json!({
            "run_id": accepted.run_id,
            "request_key": request_key,
            "status": run_status(&accepted.state),
            "restored": accepted.restored,
        }))
    }

    async fn cancel(&self, value: &str) -> Result<Value, RpcError> {
        let id = parse_id::<SessionId>(value)?;
        self.service
            .cancel_session(id)
            .await
            .map_err(map_core_error)?;
        Ok(json!({"session_id": id, "cancel_requested": true}))
    }

    async fn permission_decision(
        &self,
        value: &str,
        decision: PermissionDecision,
    ) -> Result<Value, RpcError> {
        let id = parse_id::<PermissionRequestId>(value)?;
        let option_id = self.permission_option(id, decision).await?;
        self.service
            .respond_to_permission(id, &option_id)
            .await
            .map_err(map_core_error)?;
        Ok(
            json!({"permission_id": id, "decision": decision_name(decision), "option_id": option_id}),
        )
    }

    async fn set_model(&self, value: &str, model_id: String) -> Result<Value, RpcError> {
        let id = parse_id::<SessionId>(value)?;
        self.service
            .set_model(id, model_id)
            .await
            .map_err(map_core_error)?;
        Ok(json!({"session_id": id, "updated": true}))
    }

    async fn set_mode(&self, value: &str, mode_id: String) -> Result<Value, RpcError> {
        let id = parse_id::<SessionId>(value)?;
        self.service
            .set_mode(id, mode_id)
            .await
            .map_err(map_core_error)?;
        Ok(json!({"session_id": id, "updated": true}))
    }

    async fn set_config_option(
        &self,
        value: &str,
        option_id: String,
        option_value: String,
    ) -> Result<Value, RpcError> {
        let id = parse_id::<SessionId>(value)?;
        self.service
            .set_config_option(id, &option_id, &option_value)
            .await
            .map_err(map_core_error)?;
        Ok(json!({ "session_id": id, "option_id": option_id, "updated": true }))
    }

    async fn list_models(&self, value: &str) -> Result<Value, RpcError> {
        let provider_id =
            ProviderId::new(value).map_err(|error| RpcError::new(-32602, error.to_string()))?;
        serialize(
            self.service
                .list_models(&provider_id)
                .await
                .map_err(map_core_error)?,
        )
    }

    async fn archive_session(&self, value: &str) -> Result<Value, RpcError> {
        let id = parse_id::<SessionId>(value)?;
        let session = self
            .service
            .archive_session(id)
            .await
            .map_err(map_core_error)?;
        Ok(json!({ "session_id": id, "lifecycle": session.lifecycle, "archived": true }))
    }

    async fn rename_session(&self, value: &str, title: String) -> Result<Value, RpcError> {
        let id = parse_id::<SessionId>(value)?;
        let agent = self
            .service
            .rename_session(id, title)
            .await
            .map_err(map_core_error)?;
        Ok(json!({ "session_id": id, "agent_id": agent.id, "title": agent.title }))
    }

    async fn set_thinking(
        &self,
        value: &str,
        thinking_option_id: String,
    ) -> Result<Value, RpcError> {
        let id = parse_id::<SessionId>(value)?;
        self.service
            .set_thinking(id, thinking_option_id.clone())
            .await
            .map_err(map_core_error)?;
        Ok(json!({ "session_id": id, "thinking_option_id": thinking_option_id, "updated": true }))
    }

    async fn usage_summary(&self, session_id: Option<&str>) -> Result<Value, RpcError> {
        let id = session_id.map(parse_id::<SessionId>).transpose()?;
        serialize(
            self.service
                .usage_summary(id)
                .await
                .map_err(map_core_error)?,
        )
    }

    fn shutdown(&self) -> Value {
        // Give the connection task a moment to flush the success response before
        // the accept loop stops and tears the daemon down.
        let notify = self.shutdown.clone();
        tokio::spawn(async move {
            tokio::time::sleep(Duration::from_millis(50)).await;
            notify.notify_one();
        });
        json!({ "shutdown": true })
    }

    async fn replay(&self, after: u64, limit: Option<u32>) -> Result<Value, RpcError> {
        let limit = limit.unwrap_or(256).clamp(1, 4096) as usize;
        let sessions = self.list_sessions(None).await?;
        let mut pages = Vec::new();
        for session in sessions {
            let page = self
                .service
                .replay_events(session.id, Some(EventCursor { sequence: after }), limit)
                .await
                .map_err(map_core_error)?;
            pages.push(page);
        }
        let (events, _) = merge_replay_pages(pages, limit);
        // A cursor must never acknowledge records discarded by truncation.
        let mut cursors = serde_json::Map::new();
        for event in &events {
            cursors.insert(event.session_id.to_string(), json!(event.sequence));
        }
        let next_cursor = events
            .iter()
            .map(|event| event.sequence)
            .max()
            .unwrap_or(after);
        Ok(json!({
            "events": events,
            "next_cursor": next_cursor,
            "cursor_scope": "per_session_sequence",
            "session_cursors": cursors,
        }))
    }

    async fn replay_sessions(
        &self,
        after: BTreeMap<String, u64>,
        limit: Option<u32>,
    ) -> Result<Value, RpcError> {
        let limit = limit.unwrap_or(256).clamp(1, 4096) as usize;
        let sessions = self.list_sessions(None).await?;
        let known: HashSet<_> = sessions
            .iter()
            .map(|session| session.id.to_string())
            .collect();
        if after.keys().any(|id| !known.contains(id)) {
            return Err(RpcError::new(
                -32602,
                "replay cursor references an unknown session",
            ));
        }
        let mut cursors = after;
        let mut pages = Vec::new();
        for session in sessions {
            let id = session.id.to_string();
            let sequence = *cursors.entry(id).or_insert(0);
            let page = self
                .service
                .replay_events(session.id, Some(EventCursor { sequence }), limit + 1)
                .await
                .map_err(core_error)?;
            pages.push(page);
        }
        let (events, has_more) = merge_replay_pages(pages, limit);
        for event in &events {
            cursors.insert(event.session_id.to_string(), event.sequence);
        }
        Ok(json!({
            "events": events,
            "session_cursors": cursors,
            "cursor_scope": "per_session_sequence",
            "has_more": has_more,
        }))
    }

    async fn replay_session(
        &self,
        value: &str,
        after: u64,
        limit: Option<u32>,
    ) -> Result<Value, RpcError> {
        let id = parse_id::<SessionId>(value)?;
        let limit = limit.unwrap_or(256).clamp(1, 4096) as usize;
        let events = self
            .service
            .replay_events(id, Some(EventCursor { sequence: after }), limit)
            .await
            .map_err(map_core_error)?;
        let next_cursor = events.last().map_or(after, |event| event.sequence);
        Ok(json!({"session_id": id, "events": events, "next_cursor": next_cursor}))
    }

    async fn search_timeline(
        &self,
        query: String,
        session_id: Option<&str>,
        limit: Option<u32>,
    ) -> Result<Value, RpcError> {
        if query.trim().is_empty() {
            return Err(RpcError::new(
                -32602,
                "timeline search query must not be empty",
            ));
        }
        if query.len() > 512 {
            return Err(RpcError::new(
                -32602,
                "timeline search query exceeds 512 bytes",
            ));
        }
        let query = query.trim().to_owned();
        let session_id = session_id.map(parse_id::<SessionId>).transpose()?;
        let limit = limit.unwrap_or(50).clamp(1, 1000) as usize;
        let (events, has_more) = self
            .service
            .search_timeline(query.clone(), session_id, limit)
            .await
            .map_err(map_core_error)?;
        Ok(json!({
            "query": query,
            "events": events,
            "has_more": has_more,
        }))
    }

    async fn snapshot(&self) -> Result<Value, RpcError> {
        let workspaces = self.service.list_workspaces().await.map_err(core_error)?;
        let sessions = self.list_sessions(None).await?;
        let diagnostics = self.service.diagnostics().await.map_err(core_error)?;
        let mut session_cursors = serde_json::Map::new();
        for session in &sessions {
            session_cursors.insert(
                session.id.to_string(),
                json!(self.latest_sequence(session.id).await?),
            );
        }
        let latest_cursor = session_cursors
            .values()
            .filter_map(Value::as_u64)
            .max()
            .unwrap_or(0);
        let mut agents = Vec::new();
        for workspace in &workspaces {
            agents.extend(
                self.service
                    .list_agents(workspace.id)
                    .await
                    .map_err(core_error)?,
            );
        }
        let mut providers = Vec::with_capacity(diagnostics.providers.len());
        for diagnostic in &diagnostics.providers {
            providers.push(self.snapshot_provider(diagnostic).await?);
        }
        let pending_permissions = self
            .service
            .list_pending_permissions()
            .await
            .map_err(core_error)?;
        let usage = self.service.usage_summary(None).await.map_err(core_error)?;
        Ok(json!({
            "version": PROTOCOL_VERSION,
            "home": self.home,
            "schema_version": diagnostics.schema_version,
            "active_runs": diagnostics.active_runs,
            "latest_cursor": latest_cursor,
            "session_cursors": session_cursors,
            "workspaces": workspaces,
            "agents": agents,
            "sessions": sessions,
            "providers": providers,
            "pending_permissions": pending_permissions,
            "usage": usage,
        }))
    }

    #[allow(clippy::similar_names)] // `modes`/`models` are domain terms, not confusable locals.
    async fn snapshot_provider(&self, diagnostic: &ProviderDiagnostic) -> Result<Value, RpcError> {
        let mut value = provider_diagnostic_value(diagnostic);
        let provider_id = diagnostic.descriptor.id.clone();
        // Snapshot is best-effort: a provider that cannot list models (or is
        // currently unavailable) still renders with an empty picker rather than
        // failing the whole snapshot.
        let detailed_models = match self.service.list_models(&provider_id).await {
            Ok(models) => models,
            Err(
                CoreError::Provider(ProviderError::Unsupported { .. })
                | CoreError::ProviderUnavailable(_)
                | CoreError::ProviderNotRegistered(_),
            ) => Vec::new(),
            Err(error) => return Err(core_error(error)),
        };
        let session_modes = match self.service.list_modes(&provider_id).await {
            Ok(modes) => modes,
            Err(
                CoreError::Provider(ProviderError::Unsupported { .. })
                | CoreError::ProviderUnavailable(_)
                | CoreError::ProviderNotRegistered(_),
            ) => Vec::new(),
            Err(error) => return Err(core_error(error)),
        };

        let object = value
            .as_object_mut()
            .ok_or_else(|| RpcError::new(-32603, "provider snapshot entry was not an object"))?;

        let mut modes: Vec<Value> = diagnostic
            .descriptor
            .modes
            .iter()
            .map(|mode| serde_json::to_value(mode).unwrap_or(Value::Null))
            .collect();
        let mut seen_modes: HashSet<String> = diagnostic
            .descriptor
            .modes
            .iter()
            .map(|mode| mode.id.clone())
            .collect();
        for mode in session_modes {
            if seen_modes.insert(mode.id.clone()) {
                modes.push(serde_json::to_value(&mode).unwrap_or(Value::Null));
            }
        }
        object.insert("modes".into(), Value::Array(modes));

        let mut models: Vec<Value> = diagnostic
            .descriptor
            .models
            .iter()
            .map(|model| serde_json::to_value(model).unwrap_or(Value::Null))
            .collect();
        let mut seen_models: HashSet<String> = diagnostic
            .descriptor
            .models
            .iter()
            .map(|model| model.model_id.clone())
            .collect();
        for model in &detailed_models {
            if seen_models.insert(model.id.clone()) {
                models.push(json!({ "provider": provider_id, "model_id": model.id }));
            }
        }
        object.insert("models".into(), Value::Array(models));
        object.insert(
            "model_options".into(),
            serde_json::to_value(&detailed_models).unwrap_or(Value::Null),
        );
        Ok(value)
    }

    async fn latest_sequence(&self, session_id: SessionId) -> Result<u64, RpcError> {
        let mut cursor = None;
        let mut latest = 0;
        loop {
            let page = self
                .service
                .replay_events(session_id, cursor, 10_000)
                .await
                .map_err(core_error)?;
            let count = page.len();
            if let Some(event) = page.last() {
                latest = event.sequence;
                cursor = Some(EventCursor {
                    sequence: event.sequence,
                });
            }
            if count < 10_000 {
                return Ok(latest);
            }
        }
    }

    async fn list_sessions(&self, workspace_id: Option<&str>) -> Result<Vec<Session>, RpcError> {
        let workspace_ids = if let Some(workspace_id) = workspace_id {
            let id = parse_id::<WorkspaceId>(workspace_id)?;
            self.service.workspace(id).await.map_err(core_error)?;
            vec![id]
        } else {
            self.service
                .list_workspaces()
                .await
                .map_err(core_error)?
                .into_iter()
                .map(|workspace| workspace.id)
                .collect()
        };
        let mut sessions = Vec::new();
        for workspace_id in workspace_ids {
            for agent in self
                .service
                .list_agents(workspace_id)
                .await
                .map_err(core_error)?
            {
                sessions.extend(
                    self.service
                        .list_sessions(agent.id)
                        .await
                        .map_err(core_error)?,
                );
            }
        }
        Ok(sessions)
    }

    async fn permission_option(
        &self,
        permission_id: PermissionRequestId,
        decision: PermissionDecision,
    ) -> Result<String, RpcError> {
        let sessions = self.list_sessions(None).await?;
        for session in sessions {
            let mut cursor = None;
            loop {
                let page = self
                    .service
                    .replay_events(session.id, cursor, 10_000)
                    .await
                    .map_err(core_error)?;
                let count = page.len();
                let next_cursor = page.last().map(|event| EventCursor {
                    sequence: event.sequence,
                });
                for event in page {
                    if let TimelineEventKind::PermissionRequested(permission) = event.kind
                        && permission.id == permission_id
                    {
                        let options = permission
                            .details
                            .get("options")
                            .and_then(Value::as_array)
                            .ok_or_else(|| {
                                RpcError::new(-32603, "permission options are missing")
                            })?;
                        return select_permission_option(options, decision);
                    }
                }
                if count < 10_000 {
                    break;
                }
                cursor = next_cursor;
            }
        }
        Err(RpcError::new(-32004, "permission request not found"))
    }
}

fn select_permission_option(
    options: &[Value],
    decision: PermissionDecision,
) -> Result<String, RpcError> {
    let preferred = match decision {
        PermissionDecision::Allow => "allow_once",
        PermissionDecision::Deny => "reject_once",
    };
    let option = options
        .iter()
        .find(|option| {
            option
                .get("id")
                .and_then(Value::as_str)
                .is_some_and(|id| id.eq_ignore_ascii_case(preferred))
        })
        .or_else(|| {
            options.iter().find(|option| {
                let id = option.get("id").and_then(Value::as_str).unwrap_or_default();
                let label = option
                    .get("label")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                let kind = option
                    .get("kind")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                let text = format!("{id} {label} {kind}").to_ascii_lowercase();
                match decision {
                    PermissionDecision::Allow => {
                        (text.contains("allow") || text.contains("accept"))
                            && !text.contains("always")
                    }
                    PermissionDecision::Deny => text.contains("reject") || text.contains("deny"),
                }
            })
        })
        .and_then(|option| option.get("id").and_then(Value::as_str));
    option
        .map(str::to_owned)
        .ok_or_else(|| RpcError::new(-32602, "no matching permission option is available"))
}

fn decision_name(decision: PermissionDecision) -> &'static str {
    match decision {
        PermissionDecision::Allow => "allow",
        PermissionDecision::Deny => "deny",
    }
}

fn serialize<T: serde::Serialize>(value: T) -> Result<Value, RpcError> {
    serde_json::to_value(value).map_err(|error| RpcError::new(-32603, error.to_string()))
}

fn serialize_provider_diagnostics(providers: &[ProviderDiagnostic]) -> Value {
    json!(
        providers
            .iter()
            .map(provider_diagnostic_value)
            .collect::<Vec<_>>()
    )
}

fn provider_diagnostic_value(diagnostic: &ProviderDiagnostic) -> Value {
    let descriptor = &diagnostic.descriptor;
    json!({
        "id": descriptor.id,
        "display_name": descriptor.display_name,
        "available": descriptor.available,
        "capabilities": descriptor.capabilities,
        "modes": descriptor.modes,
        "models": descriptor.models,
        "command": diagnostic.command,
        "executable_found": diagnostic.executable_found,
        "message": diagnostic.message,
    })
}

// Merge only ordered session heads: a wall-clock adjustment must never put
// sequence N+1 before N and acknowledge an undelivered record on truncation.
fn merge_replay_pages(pages: Vec<Vec<TimelineEvent>>, limit: usize) -> (Vec<TimelineEvent>, bool) {
    let mut pages: Vec<VecDeque<_>> = pages.into_iter().map(VecDeque::from).collect();
    let mut heads = BinaryHeap::new();
    for (index, page) in pages.iter().enumerate() {
        if let Some(event) = page.front() {
            heads.push(Reverse((
                event.timestamp,
                event.session_id,
                event.sequence,
                index,
            )));
        }
    }
    let mut merged = Vec::new();
    while merged.len() < limit {
        let Some(Reverse((_, _, _, index))) = heads.pop() else {
            break;
        };
        if let Some(event) = pages[index].pop_front() {
            merged.push(event);
        }
        if let Some(event) = pages[index].front() {
            heads.push(Reverse((
                event.timestamp,
                event.session_id,
                event.sequence,
                index,
            )));
        }
    }
    (merged, !heads.is_empty())
}

fn event_notification(event: TimelineEvent) -> EventNotification {
    let kind_value = serde_json::to_value(&event.kind).unwrap_or(Value::Null);
    let kind = kind_value
        .as_object()
        .and_then(|object| object.keys().next())
        .cloned()
        .unwrap_or_else(|| "unknown".into());
    EventNotification {
        version: PROTOCOL_VERSION,
        cursor: event.sequence,
        event_id: event.id.to_string(),
        event: WireEvent {
            kind,
            payload: serde_json::to_value(event).unwrap_or(Value::Null),
        },
    }
}

fn parse_id<T>(value: &str) -> Result<T, RpcError>
where
    T: std::str::FromStr,
    T::Err: std::fmt::Display,
{
    value
        .parse()
        .map_err(|error: T::Err| RpcError::new(-32602, error.to_string()))
}

fn terminal_rpc_error(message: String) -> RpcError {
    let code = if message.contains("not found") || message.contains("does not belong") {
        -32004
    } else {
        -32602
    };
    RpcError::new(code, message)
}

fn map_core_error(error: CoreError) -> RpcError {
    core_error(error)
}

fn core_error(error: CoreError) -> RpcError {
    let (code, message) = match error {
        CoreError::Config(error) => (-32602, error.to_string()),
        CoreError::Io(error) => (-32603, format!("filesystem operation failed: {error}")),
        CoreError::Store(message) => (-32603, format!("SQLite operation failed: {message}")),
        CoreError::Provider(ProviderError::Unsupported { provider, feature }) => (
            -32010,
            format!("provider {provider} does not support {feature}"),
        ),
        CoreError::ImagePromptUnsupported => {
            (-32010, "provider does not support image prompts".into())
        }
        CoreError::InvalidImage(message) | CoreError::InvalidWorkspaceName(message) => {
            (-32602, message)
        }
        CoreError::Provider(error) => (-32011, error.to_string()),
        CoreError::ProviderNotRegistered(provider) => {
            (-32010, format!("provider {provider} is not registered"))
        }
        CoreError::ProviderUnavailable(provider) => (
            -32010,
            format!("provider {provider} is disabled or unavailable"),
        ),
        CoreError::WorkspaceNotFound(id) => (-32004, format!("workspace {id} was not found")),
        CoreError::ProjectNotFound(id) => (-32004, format!("project {id} was not found")),
        CoreError::ProjectHasWorkspaces(id) => (
            -32009,
            format!("project {id} still has associated workspaces"),
        ),
        CoreError::AgentNotFound(id) => (-32004, format!("agent {id} was not found")),
        CoreError::SessionNotFound(id) => (-32004, format!("session {id} was not found")),
        CoreError::RunNotFound(id) => (-32004, format!("run {id} was not found")),
        CoreError::InvalidWorkspacePath(path) => (
            -32602,
            format!("workspace path is not an existing directory: {path}"),
        ),
        CoreError::WorkspaceHasActiveRuns(id) => (
            -32009,
            format!("workspace {id} has active runs and cannot be archived"),
        ),
        CoreError::WorkspaceArchived => (
            -32602,
            "workspace is archived and cannot start a new agent".into(),
        ),
        CoreError::AgentArchived(id) => (
            -32602,
            format!("archived agent {id} cannot create or load provider sessions"),
        ),
        CoreError::NativeSessionMissing(id) => (
            -32602,
            format!("session {id} has no persisted provider-native session handle"),
        ),
        CoreError::SessionNotLoaded(id) => (
            -32602,
            format!("session {id} is not loaded; call load_session before sending a prompt"),
        ),
        CoreError::SessionBusy => (-32009, "session already has an active run".into()),
        CoreError::EmptyPromptRequestKey => (-32602, "prompt request_key must not be empty".into()),
        CoreError::PromptRequestKeyTooLong(max) => (
            -32602,
            format!("prompt request_key exceeds maximum length of {max} bytes"),
        ),
        CoreError::PromptRequestKeyConflict { session } => (
            -32012,
            format!(
                "prompt request_key was already accepted with a different payload for session {session}"
            ),
        ),
        CoreError::PermissionNotPending(id) => {
            (-32004, format!("permission request {id} is not pending"))
        }
        CoreError::InvalidPermissionOption { request, option } => (
            -32602,
            format!("permission option {option:?} is not valid for request {request}"),
        ),
        CoreError::InvalidThinkingOption { config, option } => (
            -32602,
            format!("thinking option {option:?} is not valid for config option {config}"),
        ),
        CoreError::ShuttingDown => (-32603, "service is shutting down".into()),
        CoreError::Other(message) => (-32603, format!("operation failed: {message}")),
    };
    RpcError::new(code, message)
}

fn run_status(state: &RunLifecycle) -> &'static str {
    match state {
        RunLifecycle::Queued => "queued",
        RunLifecycle::Starting => "starting",
        RunLifecycle::Running => "running",
        RunLifecycle::CancelRequested => "cancel_requested",
        RunLifecycle::Succeeded => "succeeded",
        RunLifecycle::Failed => "failed",
        RunLifecycle::Cancelled => "cancelled",
        RunLifecycle::Interrupted => "interrupted",
    }
}

fn home_dir() -> anyhow::Result<PathBuf> {
    if let Some(home) = std::env::var_os("TALOSD_HOME") {
        return Ok(PathBuf::from(home));
    }
    let base = directories::BaseDirs::new()
        .ok_or_else(|| anyhow::anyhow!("home directory is unavailable"))?;
    Ok(base.home_dir().join(".talosd"))
}

/// Validate the selected home, adopt pre-rename data, then create it privately.
async fn open_home(home: &Path) -> anyhow::Result<()> {
    let paths = HomePaths::at(home);
    paths.validate()?;
    if paths.adopt_pre_rename_home()? {
        info!(home = %home.display(), "moved pre-rename ~/.paseo-rs home");
    }
    prepare_home(home).await
}

async fn prepare_home(path: &Path) -> anyhow::Result<()> {
    tokio::fs::create_dir_all(path).await?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        tokio::fs::set_permissions(path, std::fs::Permissions::from_mode(0o700)).await?;
    }
    Ok(())
}

async fn load_token(home: &Path) -> anyhow::Result<String> {
    let path = home.join("auth.token");
    match tokio::fs::symlink_metadata(&path).await {
        Ok(metadata) if !metadata.file_type().is_file() => {
            anyhow::bail!("auth token must be a regular file: {}", path.display());
        }
        Ok(_) => {}
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
        Err(error) => return Err(error.into()),
    }
    match tokio::fs::read_to_string(&path).await {
        Ok(token) if !token.trim().is_empty() => Ok(token.trim().to_owned()),
        Ok(_) => anyhow::bail!("auth token file is empty: {}", path.display()),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            let token = Uuid::new_v4().to_string() + &Uuid::new_v4().to_string();
            let temporary = home.join(format!("auth.token.tmp.{}", Uuid::new_v4()));
            let mut options = tokio::fs::OpenOptions::new();
            options.write(true).create_new(true);
            #[cfg(unix)]
            options.mode(0o600);
            let mut file = options.open(&temporary).await?;
            let result = async {
                file.write_all(token.as_bytes()).await?;
                file.sync_all().await?;
                tokio::fs::rename(&temporary, &path).await
            }
            .await;
            if result.is_err() {
                let _ = tokio::fs::remove_file(&temporary).await;
            }
            result?;
            Ok(token)
        }
        Err(error) => Err(error.into()),
    }
}

#[cfg(unix)]
async fn run() -> anyhow::Result<()> {
    let home = home_dir()?;
    // Validate before chmod, token creation, lock creation or socket binding.
    open_home(&home).await?;
    let instance = InstanceGuard::acquire(&home)?;
    let token = load_token(&home).await?;
    let socket_path = home.join("talosd.sock");
    let (listener, socket_guard) = instance.bind_socket().await?;
    let service = AppService::open_at(home.clone()).await?;
    let shutdown_notify = Arc::new(Notify::new());
    let (daemon, event_bridge) =
        Daemon::new(home.clone(), service.clone(), shutdown_notify.clone());
    info!(socket = %socket_path.display(), home = %home.display(), "talosd listening");
    let shutdown = tokio::signal::ctrl_c();
    let mut terminate = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())?;
    tokio::pin!(shutdown);
    let mut connections = tokio::task::JoinSet::new();
    loop {
        tokio::select! {
            _ = &mut shutdown => break,
            _ = terminate.recv() => break,
            () = shutdown_notify.notified() => break,
            Some(result) = connections.join_next(), if !connections.is_empty() => {
                if let Err(error) = result {
                    warn!(%error, "client task failed");
                }
            }
            accepted = listener.accept() => {
                let (stream, _) = accepted?;
                // Idle unauthenticated peers cannot create unbounded tasks.
                if connections.len() >= 64 {
                    drop(stream);
                    continue;
                }
                let daemon = daemon.clone();
                let token = token.clone();
                let events = daemon.event_tx.subscribe();
                connections.spawn(async move {
                    let result = serve_connection_with_events(
                        stream,
                        Some(token),
                        move |command| {
                            let daemon = daemon.clone();
                            async move { daemon.command(command).await }
                        },
                        events,
                    ).await;
                    if let Err(error) = result {
                        warn!(%error, "client connection ended with protocol error");
                    }
                });
            }
        }
    }
    drop(listener);
    drop(socket_guard);
    // Let accepted commands (including shutdown's acknowledgement) finish
    // before closing storage. Bound the drain; idle clients cannot hold exit.
    let _ = tokio::time::timeout(Duration::from_millis(500), async {
        while connections.join_next().await.is_some() {}
    })
    .await;
    connections.abort_all();
    while connections.join_next().await.is_some() {}
    daemon.terminals.kill_all();
    service.shutdown().await;
    event_bridge.abort();
    let _ = event_bridge.await;
    info!("talosd stopped cleanly");
    Ok(())
}

#[cfg(not(unix))]
async fn run() -> anyhow::Result<()> {
    anyhow::bail!("talosd currently requires Unix domain socket support")
}

#[tokio::main]
async fn main() {
    tracing_subscriber::fmt()
        .json()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "talosd=info".into()),
        )
        .init();
    if let Err(error) = run().await {
        error!(%error, "daemon failed");
        std::process::exit(1);
    }
}

#[cfg(test)]
mod tests {
    use std::{path::Path, sync::Arc, time::Duration};

    use async_trait::async_trait;
    use talos_acp::{ConfigOption, ConfigOptionValue};
    use talos_core::{
        AgentProvider, PermissionHandler, ProviderDiagnostic, ProviderError, ProviderEvent,
        ProviderImage, ProviderModel, ProviderPermissionDecision, ProviderPermissionOption,
        ProviderPermissionRequest, ProviderPromptResult, ProviderSession, ProviderThinkingOption,
    };
    use talos_domain::{
        Capabilities, ModelRef, ProviderDescriptor, ProviderId, ProviderMode, RunId, RunLifecycle,
        Workspace,
    };
    use talos_ipc::{Client, Command, ServerFrame};
    use tokio::sync::{Mutex, mpsc};
    use tokio_util::sync::CancellationToken;

    use super::*;

    struct FixtureProvider {
        id: ProviderId,
    }

    #[async_trait]
    impl AgentProvider for FixtureProvider {
        fn id(&self) -> &ProviderId {
            &self.id
        }

        async fn diagnostic(&self) -> ProviderDiagnostic {
            ProviderDiagnostic {
                descriptor: ProviderDescriptor {
                    id: self.id.clone(),
                    display_name: "IPC fixture".into(),
                    available: true,
                    capabilities: Capabilities {
                        streaming: true,
                        images: true,
                        tools: true,
                        modes: true,
                        model_selection: true,
                        config_options: true,
                        permissions: true,
                        ..Capabilities::default()
                    },
                    modes: Vec::new(),
                    models: Vec::new(),
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
            Ok(Box::new(FixtureSession::new(self.id.clone(), permissions)))
        }

        async fn resume_session(
            &self,
            _cwd: &Path,
            native_session_id: &str,
            permissions: Arc<dyn PermissionHandler>,
        ) -> Result<Box<dyn ProviderSession>, ProviderError> {
            let mut session = FixtureSession::new(self.id.clone(), permissions);
            session.native_id = native_session_id.to_owned();
            Ok(Box::new(session))
        }
    }

    struct FixtureSession {
        id: ProviderId,
        native_id: String,
        events: Mutex<mpsc::UnboundedReceiver<ProviderEvent>>,
        event_tx: mpsc::UnboundedSender<ProviderEvent>,
        permissions: Arc<dyn PermissionHandler>,
    }

    impl FixtureSession {
        fn new(id: ProviderId, permissions: Arc<dyn PermissionHandler>) -> Self {
            let (event_tx, event_rx) = mpsc::unbounded_channel();
            Self {
                id,
                native_id: "fixture-native-session".into(),
                events: Mutex::new(event_rx),
                event_tx,
                permissions,
            }
        }
    }

    #[async_trait]
    impl ProviderSession for FixtureSession {
        fn provider_id(&self) -> &ProviderId {
            &self.id
        }

        fn native_session_id(&self) -> &str {
            &self.native_id
        }

        fn capabilities(&self) -> Capabilities {
            Capabilities {
                streaming: true,
                images: true,
                tools: true,
                modes: true,
                model_selection: true,
                config_options: true,
                permissions: true,
                ..Capabilities::default()
            }
        }

        fn modes(&self) -> Vec<ProviderMode> {
            vec![ProviderMode::new("fixture-mode", "Fixture mode").unwrap()]
        }

        fn models(&self) -> Vec<ModelRef> {
            vec![ModelRef::new(self.id.clone(), "fixture/model-a").unwrap()]
        }

        fn config_options(&self) -> Vec<ConfigOption> {
            vec![ConfigOption {
                id: "thought_level".into(),
                name: "Thought level".into(),
                description: Some("How much thinking to surface".into()),
                category: Some("thinking".into()),
                option_type: "select".into(),
                current_value: "minimal".into(),
                options: vec![
                    ConfigOptionValue {
                        value: "off".into(),
                        name: "Off".into(),
                        extra: serde_json::Map::default(),
                    },
                    ConfigOptionValue {
                        value: "minimal".into(),
                        name: "Minimal".into(),
                        extra: serde_json::Map::default(),
                    },
                    ConfigOptionValue {
                        value: "high".into(),
                        name: "High".into(),
                        extra: serde_json::Map::default(),
                    },
                ],
                extra: serde_json::Map::default(),
            }]
        }

        fn models_detailed(&self) -> Vec<ProviderModel> {
            vec![ProviderModel {
                id: "fixture/model-a".into(),
                label: "Fixture Model A".into(),
                description: Some("Fixture model with thinking options".into()),
                thinking_options: vec![
                    ProviderThinkingOption {
                        id: "off".into(),
                        label: "Off".into(),
                    },
                    ProviderThinkingOption {
                        id: "minimal".into(),
                        label: "Minimal".into(),
                    },
                    ProviderThinkingOption {
                        id: "high".into(),
                        label: "High".into(),
                    },
                ],
                default_thinking_option_id: Some("minimal".into()),
                available: true,
            }]
        }

        async fn prompt(
            &self,
            text: &str,
            cancellation: &CancellationToken,
        ) -> Result<ProviderPromptResult, ProviderError> {
            let stop_reason = match text {
                "usage" => {
                    return Ok(ProviderPromptResult {
                        stop_reason: "end_turn".into(),
                        usage: Some(talos_acp::PromptUsage {
                            input_tokens: Some(10),
                            output_tokens: Some(20),
                            cached_read_tokens: Some(5),
                            cached_write_tokens: Some(2),
                            total_tokens: Some(30),
                            extra: serde_json::Map::default(),
                        }),
                    });
                }
                "permission" => {
                    let request = ProviderPermissionRequest {
                        native_session_id: self.native_id.clone(),
                        title: "Fixture tool".into(),
                        kind: Some("execute".into()),
                        tool_call_id: "fixture-tool-1".into(),
                        raw_input: Some(json!({"command":"printf fixture"})),
                        locations: Vec::new(),
                        options: vec![
                            ProviderPermissionOption {
                                id: "allow_once".into(),
                                label: "Allow once".into(),
                                kind: Some("allow_once".into()),
                            },
                            ProviderPermissionOption {
                                id: "reject_once".into(),
                                label: "Reject once".into(),
                                kind: Some("reject_once".into()),
                            },
                        ],
                    };
                    match self
                        .permissions
                        .request_permission(request, cancellation.clone())
                        .await
                    {
                        ProviderPermissionDecision::Selected(_) => "end_turn",
                        ProviderPermissionDecision::Cancelled => "cancelled",
                    }
                }
                "cancel" => {
                    tokio::select! {
                        () = cancellation.cancelled() => "cancelled",
                        () = tokio::time::sleep(Duration::from_secs(30)) => "end_turn",
                    }
                }
                _ => {
                    self.event_tx
                        .send(ProviderEvent::Acp(talos_acp::AcpEvent::Diagnostic {
                            message: format!("fixture received prompt ({})", text.len()),
                        }))
                        .map_err(|error| ProviderError::Other(error.to_string()))?;
                    "end_turn"
                }
            };
            Ok(ProviderPromptResult {
                stop_reason: stop_reason.into(),
                usage: None,
            })
        }

        async fn prompt_with_images(
            &self,
            text: &str,
            _images: &[ProviderImage],
            cancellation: &CancellationToken,
        ) -> Result<ProviderPromptResult, ProviderError> {
            self.prompt(text, cancellation).await
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
            self.events.lock().await.recv().await
        }

        async fn shutdown(&self, _grace: Duration) {}
    }

    async fn fixture_daemon() -> (Daemon, tokio::task::JoinHandle<()>, tempfile::TempDir) {
        let temp = tempfile::tempdir().unwrap();
        let service = AppService::open_at(temp.path().join("home")).await.unwrap();
        service
            .register_provider(Arc::new(FixtureProvider {
                id: ProviderId::new("fixture").unwrap(),
            }))
            .await;
        let (daemon, bridge) =
            Daemon::new(temp.path().join("home"), service, Arc::new(Notify::new()));
        (daemon, bridge, temp)
    }

    #[cfg(unix)]
    #[tokio::test]
    async fn restore_workspace_rpc_rejects_managed_checkout_symlinks_and_replacements() {
        use std::os::unix::fs::symlink;
        use std::process::Command as ProcessCommand;

        for replacement in ["symlink", "directory"] {
            let (daemon, bridge, temp) = fixture_daemon().await;
            let repo = temp.path().join("repo");
            std::fs::create_dir(&repo).unwrap();
            let git = |args: &[&str]| {
                let output = ProcessCommand::new("git")
                    .args(args)
                    .current_dir(&repo)
                    .output()
                    .unwrap();
                assert!(
                    output.status.success(),
                    "git {args:?}: {}",
                    String::from_utf8_lossy(&output.stderr)
                );
            };
            git(&["init", "--quiet", "-b", "main"]);
            git(&["config", "user.name", "Daemon Fixture"]);
            git(&["config", "user.email", "daemon-fixture@example.invalid"]);
            std::fs::write(repo.join("tracked"), "preserve\n").unwrap();
            git(&["add", "tracked"]);
            git(&["commit", "--quiet", "-m", "fixture"]);

            let project: Value = daemon
                .command(Command::CreateProject {
                    name: "Fixture".into(),
                    root_path: repo.to_string_lossy().into_owned(),
                })
                .await
                .unwrap();
            let workspace: Value = daemon
                .command(Command::CreateManagedWorktree {
                    project_id: project["id"].as_str().unwrap().into(),
                    name: "Feature".into(),
                    branch: "feature/daemon-restore".into(),
                    base_ref: "HEAD".into(),
                })
                .await
                .unwrap();
            let workspace_id = workspace["id"].as_str().unwrap().to_owned();
            let path = PathBuf::from(workspace["path"].as_str().unwrap());
            daemon
                .service
                .archive_workspace(parse_id::<WorkspaceId>(&workspace_id).unwrap())
                .await
                .unwrap();
            let moved = temp.path().join("moved-checkout");
            std::fs::rename(&path, &moved).unwrap();
            if replacement == "symlink" {
                symlink(&moved, &path).unwrap();
            } else {
                std::fs::create_dir(&path).unwrap();
                std::fs::write(path.join("replacement-data"), "preserve replacement").unwrap();
            }

            let error = daemon
                .command(Command::RestoreWorkspace {
                    workspace_id: workspace_id.clone(),
                })
                .await
                .unwrap_err();
            assert_ne!(error.code, 0);
            assert_eq!(
                daemon
                    .service
                    .workspace(parse_id::<WorkspaceId>(&workspace_id).unwrap())
                    .await
                    .unwrap()
                    .lifecycle,
                WorkspaceLifecycle::Archived
            );
            assert_eq!(
                std::fs::read_to_string(moved.join("tracked")).unwrap(),
                "preserve\n"
            );
            if replacement == "directory" {
                assert_eq!(
                    std::fs::read_to_string(path.join("replacement-data")).unwrap(),
                    "preserve replacement"
                );
            }
            bridge.abort();
            daemon.service.shutdown().await;
        }
    }

    async fn fixture_session(daemon: &Daemon, root: &Path, name: &str) -> Session {
        let workspace_path = root.join(name);
        tokio::fs::create_dir(&workspace_path).await.unwrap();
        let workspace: Workspace = serde_json::from_value(
            daemon
                .command(Command::CreateWorkspace {
                    name: format!("{name} workspace"),
                    path: workspace_path.to_string_lossy().into_owned(),
                })
                .await
                .unwrap(),
        )
        .unwrap();
        let created = daemon
            .command(Command::CreateAgent {
                workspace_id: workspace.id.to_string(),
                provider_id: Some("fixture".into()),
                title: Some(format!("{name} agent")),
            })
            .await
            .unwrap();
        serde_json::from_value(created["session"].clone()).unwrap()
    }

    async fn poll_pending(daemon: &Daemon, expected: usize) -> Vec<Value> {
        for _ in 0..100 {
            let entries = daemon
                .command(Command::ListPendingPermissions)
                .await
                .unwrap()
                .as_array()
                .unwrap()
                .clone();
            if entries.len() == expected {
                return entries;
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
        daemon
            .command(Command::ListPendingPermissions)
            .await
            .unwrap()
            .as_array()
            .unwrap()
            .clone()
    }

    #[tokio::test]
    async fn keyed_prompt_retries_are_authoritative_and_conflicts_are_actionable() {
        let (daemon, bridge, temp) = fixture_daemon().await;
        let session = fixture_session(&daemon, temp.path(), "keyed").await;
        let first = daemon
            .command(Command::Prompt {
                session_id: session.id.to_string(),
                text: "hello".into(),
                request_key: Some("key-1".into()),
            })
            .await
            .unwrap();
        assert_eq!(first["request_key"], "key-1");
        assert_eq!(first["status"], "running");
        assert_eq!(first["restored"], false);
        let run_id: RunId = first["run_id"].as_str().unwrap().parse().unwrap();
        assert_eq!(
            daemon.service.wait_run(run_id).await.unwrap(),
            RunLifecycle::Succeeded
        );

        let retry = daemon
            .command(Command::Prompt {
                session_id: session.id.to_string(),
                text: "hello".into(),
                request_key: Some("key-1".into()),
            })
            .await
            .unwrap();
        assert_eq!(retry["run_id"], first["run_id"]);
        assert_eq!(retry["status"], "succeeded");
        assert_eq!(retry["restored"], true);

        let conflict = daemon
            .command(Command::Prompt {
                session_id: session.id.to_string(),
                text: "different".into(),
                request_key: Some("key-1".into()),
            })
            .await
            .unwrap_err();
        assert_eq!(conflict.code, -32012);
        assert!(conflict.message.contains("different payload"));

        let other = fixture_session(&daemon, temp.path(), "other").await;
        let other_first = daemon
            .command(Command::Prompt {
                session_id: other.id.to_string(),
                text: "hello".into(),
                request_key: Some("key-1".into()),
            })
            .await
            .unwrap();
        assert_ne!(other_first["run_id"], first["run_id"]);
        assert_eq!(other_first["restored"], false);

        let invalid = daemon
            .command(Command::Prompt {
                session_id: session.id.to_string(),
                text: "hello".into(),
                request_key: Some("   ".into()),
            })
            .await
            .unwrap_err();
        assert_eq!(invalid.code, -32602);

        let events = daemon
            .service
            .replay_events(session.id, None, 100)
            .await
            .unwrap();
        assert_eq!(
            events
                .iter()
                .filter(|event| matches!(
                    &event.kind,
                    TimelineEventKind::UserText { text } if text == "hello"
                ))
                .count(),
            1
        );
        bridge.abort();
        daemon.service.shutdown().await;
    }

    #[tokio::test]
    async fn keyed_prompt_retry_while_busy_returns_the_original_run() {
        let (daemon, bridge, temp) = fixture_daemon().await;
        let session = fixture_session(&daemon, temp.path(), "busy").await;
        let first = daemon
            .command(Command::Prompt {
                session_id: session.id.to_string(),
                text: "permission".into(),
                request_key: Some("busy-key".into()),
            })
            .await
            .unwrap();
        let pending = poll_pending(&daemon, 1).await;
        let run_id: RunId = first["run_id"].as_str().unwrap().parse().unwrap();

        let retry = daemon
            .command(Command::Prompt {
                session_id: session.id.to_string(),
                text: "permission".into(),
                request_key: Some("busy-key".into()),
            })
            .await
            .unwrap();
        assert_eq!(retry["run_id"], first["run_id"]);
        assert_eq!(retry["status"], "running");
        assert_eq!(retry["restored"], true);
        assert_eq!(poll_pending(&daemon, 1).await.len(), 1);

        daemon
            .command(Command::PermissionDecision {
                permission_id: pending[0]["id"].as_str().unwrap().to_owned(),
                decision: PermissionDecision::Allow,
            })
            .await
            .unwrap();
        assert_eq!(
            daemon.service.wait_run(run_id).await.unwrap(),
            RunLifecycle::Succeeded
        );
        bridge.abort();
        daemon.service.shutdown().await;
    }

    #[test]
    fn replay_merge_preserves_session_sequence_when_wall_clock_moves_backwards() {
        let session = SessionId::new();
        let mut first = TimelineEvent::new(
            session,
            None,
            1,
            TimelineEventKind::UserText {
                text: "first".into(),
            },
        )
        .unwrap();
        let mut second = TimelineEvent::new(
            session,
            None,
            2,
            TimelineEventKind::UserText {
                text: "second".into(),
            },
        )
        .unwrap();
        first.timestamp = serde_json::from_value(json!("2040-01-01T00:00:00Z")).unwrap();
        second.timestamp = serde_json::from_value(json!("2030-01-01T00:00:00Z")).unwrap();
        let (page, has_more) = merge_replay_pages(vec![vec![first, second]], 1);
        assert_eq!(page.len(), 1);
        assert_eq!(page[0].sequence, 1);
        assert!(has_more);
    }

    #[tokio::test]
    async fn project_crud_and_workspace_association_are_available_over_ipc() {
        let (daemon, bridge, temp) = fixture_daemon().await;
        let project_root = temp.path().join("project-root");
        let workspace_path = project_root.join("workspace");
        tokio::fs::create_dir_all(&workspace_path).await.unwrap();
        let project = daemon
            .command(Command::CreateProject {
                name: "Example".into(),
                root_path: project_root.to_string_lossy().into_owned(),
            })
            .await
            .unwrap();
        let project_id = project["id"].as_str().unwrap().to_owned();
        assert_eq!(
            daemon
                .command(Command::ListProjects)
                .await
                .unwrap()
                .as_array()
                .unwrap()
                .len(),
            1
        );
        assert_eq!(
            daemon
                .command(Command::GetProject {
                    project_id: project_id.clone()
                })
                .await
                .unwrap()["name"],
            "Example"
        );
        daemon
            .command(Command::RenameProject {
                project_id: project_id.clone(),
                name: "Renamed".into(),
            })
            .await
            .unwrap();
        let workspace = daemon
            .command(Command::CreateWorkspace {
                name: "Associated".into(),
                path: workspace_path.to_string_lossy().into_owned(),
            })
            .await
            .unwrap();
        let workspace_id = workspace["id"].as_str().unwrap().to_owned();
        let linked = daemon
            .command(Command::SetWorkspaceProject {
                workspace_id: workspace_id.clone(),
                project_id: Some(project_id.clone()),
            })
            .await
            .unwrap();
        assert_eq!(linked["project_id"], project_id);
        let rejected_delete = daemon
            .command(Command::DeleteProject {
                project_id: project_id.clone(),
            })
            .await
            .unwrap_err();
        assert_eq!(rejected_delete.code, -32009);
        assert!(rejected_delete.message.contains("associated workspaces"));
        daemon
            .command(Command::SetWorkspaceProject {
                workspace_id,
                project_id: None,
            })
            .await
            .unwrap();
        daemon
            .command(Command::DeleteProject {
                project_id: project_id.clone(),
            })
            .await
            .unwrap();
        assert_eq!(
            daemon
                .command(Command::ListProjects)
                .await
                .unwrap()
                .as_array()
                .unwrap()
                .len(),
            0
        );
        bridge.abort();
        daemon.service.shutdown().await;
    }

    #[tokio::test]
    #[allow(clippy::too_many_lines)] // Exercise the complete daemon terminal protocol in one fixture.
    async fn terminal_lifecycle_is_exposed_over_ipc_and_workspace_scoped() {
        let (daemon, bridge, temp) = fixture_daemon().await;
        let _first_session = fixture_session(&daemon, temp.path(), "terminal-first").await;
        let _second_session = fixture_session(&daemon, temp.path(), "terminal-second").await;
        let workspaces = daemon.service.list_workspaces().await.unwrap();
        let first = workspaces
            .iter()
            .find(|workspace| workspace.name == "terminal-first workspace")
            .unwrap();
        let second = workspaces
            .iter()
            .find(|workspace| workspace.name == "terminal-second workspace")
            .unwrap();
        let terminal: Value = daemon
            .command(Command::CreateTerminal {
                workspace_id: first.id.to_string(),
                cwd: None,
                program: "sh".into(),
                args: vec![
                    "-c".into(),
                    "printf READY; read line; printf 'GOT:%s' \"$line\"".into(),
                ],
                rows: 24,
                cols: 80,
            })
            .await
            .unwrap();
        let terminal_id = terminal["id"].as_str().unwrap().to_owned();
        let listed = daemon
            .command(Command::ListTerminals {
                workspace_id: first.id.to_string(),
            })
            .await
            .unwrap();
        assert_eq!(listed.as_array().unwrap().len(), 1);
        assert_eq!(
            daemon
                .command(Command::ListTerminals {
                    workspace_id: second.id.to_string()
                })
                .await
                .unwrap()
                .as_array()
                .unwrap()
                .len(),
            0
        );
        let deadline = tokio::time::Instant::now() + Duration::from_secs(2);
        loop {
            let captured = daemon
                .command(Command::CaptureTerminal {
                    workspace_id: first.id.to_string(),
                    terminal_id: terminal_id.clone(),
                    limit: Some(2048),
                })
                .await
                .unwrap();
            if captured["data"].as_str().unwrap().contains("READY") {
                break;
            }
            assert!(
                tokio::time::Instant::now() < deadline,
                "terminal did not start"
            );
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
        let wrong_workspace = daemon
            .command(Command::TerminalInput {
                workspace_id: second.id.to_string(),
                terminal_id: terminal_id.clone(),
                data: "bad\n".into(),
            })
            .await
            .unwrap_err();
        assert_eq!(wrong_workspace.code, -32004);
        daemon
            .command(Command::ResizeTerminal {
                workspace_id: first.id.to_string(),
                terminal_id: terminal_id.clone(),
                rows: 35,
                cols: 100,
            })
            .await
            .unwrap();
        daemon
            .command(Command::TerminalInput {
                workspace_id: first.id.to_string(),
                terminal_id: terminal_id.clone(),
                data: "ipc-input\n".into(),
            })
            .await
            .unwrap();
        let deadline = tokio::time::Instant::now() + Duration::from_secs(2);
        loop {
            let captured = daemon
                .command(Command::CaptureTerminal {
                    workspace_id: first.id.to_string(),
                    terminal_id: terminal_id.clone(),
                    limit: Some(2048),
                })
                .await
                .unwrap();
            if captured["data"].as_str().unwrap().contains("GOT:ipc-input") {
                break;
            }
            assert!(
                tokio::time::Instant::now() < deadline,
                "terminal input did not arrive"
            );
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
        daemon
            .command(Command::KillTerminal {
                workspace_id: first.id.to_string(),
                terminal_id,
            })
            .await
            .unwrap();
        assert_eq!(
            daemon
                .command(Command::ListTerminals {
                    workspace_id: first.id.to_string()
                })
                .await
                .unwrap()
                .as_array()
                .unwrap()
                .len(),
            0
        );
        bridge.abort();
        daemon.terminals.kill_all();
        daemon.service.shutdown().await;
    }

    #[tokio::test]
    async fn multi_session_replay_pages_never_acknowledge_undelivered_events() {
        let (daemon, bridge, temp) = fixture_daemon().await;
        let first = fixture_session(&daemon, temp.path(), "first").await;
        let second = fixture_session(&daemon, temp.path(), "second").await;
        for session in [&first, &second] {
            let run = daemon
                .service
                .send_prompt(session.id, "hello")
                .await
                .unwrap();
            daemon.service.wait_run(run).await.unwrap();
        }
        let mut expected = Vec::new();
        for session in [&first, &second] {
            expected.extend(
                daemon
                    .service
                    .replay_events(session.id, None, 100)
                    .await
                    .unwrap()
                    .into_iter()
                    .map(|event| event.id.to_string()),
            );
        }
        let mut cursors = BTreeMap::new();
        let mut delivered = Vec::new();
        loop {
            let page = daemon
                .command(Command::ReplaySessions {
                    after: cursors.clone(),
                    limit: Some(1),
                })
                .await
                .unwrap();
            let events = page["events"].as_array().unwrap();
            assert!(events.len() <= 1);
            let next: BTreeMap<String, u64> =
                serde_json::from_value(page["session_cursors"].clone()).unwrap();
            for event in events {
                let session = event["session_id"].as_str().unwrap();
                assert_eq!(next[session], event["sequence"].as_u64().unwrap());
                delivered.push(event["id"].as_str().unwrap().to_owned());
            }
            for (session, cursor) in &next {
                if events
                    .iter()
                    .all(|event| event["session_id"] != session.as_str())
                {
                    assert_eq!(*cursor, cursors.get(session).copied().unwrap_or(0));
                }
            }
            cursors = next;
            if page["has_more"] == false {
                break;
            }
            assert!(delivered.len() <= expected.len());
        }
        delivered.sort();
        expected.sort();
        assert_eq!(delivered, expected);
        let invalid = daemon
            .command(Command::ReplaySessions {
                after: [("missing".into(), 0)].into(),
                limit: None,
            })
            .await
            .unwrap_err();
        assert_eq!(invalid.code, -32602);
        daemon.service.shutdown().await;
        bridge.abort();
    }

    #[tokio::test]
    async fn timeline_search_is_available_over_ipc_and_can_be_session_scoped() {
        let (daemon, bridge, temp) = fixture_daemon().await;
        let first = fixture_session(&daemon, temp.path(), "first-search").await;
        let second = fixture_session(&daemon, temp.path(), "second-search").await;
        for (session, prompt) in [
            (&first, "needle belongs to first session"),
            (&second, "needle belongs to second session"),
        ] {
            let run = daemon
                .service
                .send_prompt(session.id, prompt)
                .await
                .unwrap();
            daemon.service.wait_run(run).await.unwrap();
        }

        let page = daemon
            .command(Command::SearchTimeline {
                query: "NEEDLE".into(),
                session_id: None,
                limit: Some(1),
            })
            .await
            .unwrap();
        assert_eq!(page["query"], "NEEDLE");
        assert_eq!(page["events"].as_array().unwrap().len(), 1);
        assert_eq!(page["has_more"], true);

        let scoped = daemon
            .command(Command::SearchTimeline {
                query: "needle".into(),
                session_id: Some(first.id.to_string()),
                limit: None,
            })
            .await
            .unwrap();
        assert_eq!(scoped["events"].as_array().unwrap().len(), 1);
        assert_eq!(scoped["events"][0]["session_id"], first.id.to_string());
        assert_eq!(
            scoped["events"][0]["kind"]["UserText"]["text"],
            "needle belongs to first session"
        );
        assert_eq!(scoped["has_more"], false);

        let invalid = daemon
            .command(Command::SearchTimeline {
                query: "   ".into(),
                session_id: None,
                limit: None,
            })
            .await
            .unwrap_err();
        assert_eq!(invalid.code, -32602);
        let invalid_session = daemon
            .command(Command::SearchTimeline {
                query: "needle".into(),
                session_id: Some("not-a-session-id".into()),
                limit: None,
            })
            .await
            .unwrap_err();
        assert_eq!(invalid_session.code, -32602);
        daemon.service.shutdown().await;
        bridge.abort();
    }

    #[tokio::test]
    async fn token_creation_is_private_and_rejects_symlink_sources() {
        use std::os::unix::{fs::PermissionsExt, fs::symlink};
        let temp = tempfile::tempdir().unwrap();
        let token = load_token(temp.path()).await.unwrap();
        let path = temp.path().join("auth.token");
        assert_eq!(
            tokio::fs::metadata(&path)
                .await
                .unwrap()
                .permissions()
                .mode()
                & 0o777,
            0o600
        );
        assert_eq!(load_token(temp.path()).await.unwrap(), token);
        tokio::fs::remove_file(&path).await.unwrap();
        let source = temp.path().join("must-not-read");
        tokio::fs::write(&source, "not-a-daemon-token")
            .await
            .unwrap();
        symlink(&source, &path).unwrap();
        assert!(load_token(temp.path()).await.is_err());
        assert_eq!(
            tokio::fs::read_to_string(source).await.unwrap(),
            "not-a-daemon-token"
        );
    }

    #[tokio::test]
    async fn legacy_replay_cursors_only_cover_delivered_records() {
        let (daemon, bridge, temp) = fixture_daemon().await;
        let first = fixture_session(&daemon, temp.path(), "first").await;
        let second = fixture_session(&daemon, temp.path(), "second").await;
        for session in [first, second] {
            let run = daemon
                .service
                .send_prompt(session.id, "hello")
                .await
                .unwrap();
            daemon.service.wait_run(run).await.unwrap();
        }
        let page = daemon
            .command(Command::Replay {
                after: 0,
                limit: Some(1),
            })
            .await
            .unwrap();
        let events = page["events"].as_array().unwrap();
        assert_eq!(events.len(), 1);
        let cursors = page["session_cursors"].as_object().unwrap();
        assert_eq!(cursors.len(), 1);
        let session = events[0]["session_id"].as_str().unwrap();
        assert_eq!(cursors[session], events[0]["sequence"]);
        daemon.service.shutdown().await;
        bridge.abort();
    }

    #[tokio::test]
    async fn end_to_end_fixture_creates_agent_prompts_and_replays_durable_timeline() {
        let (daemon, bridge, temp) = fixture_daemon().await;
        let session = fixture_session(&daemon, temp.path(), "project").await;
        let prompt = daemon
            .command(Command::Prompt {
                session_id: session.id.to_string(),
                text: "hello".into(),
                request_key: None,
            })
            .await
            .unwrap();
        assert!(prompt["run_id"].as_str().is_some());

        let mut timeline = Vec::new();
        for _ in 0..100 {
            timeline = daemon
                .command(Command::ReplaySession {
                    session_id: session.id.to_string(),
                    after: 0,
                    limit: Some(64),
                })
                .await
                .unwrap()["events"]
                .as_array()
                .unwrap()
                .clone();
            if timeline
                .iter()
                .any(|event| event["kind"]["RunState"]["state"] == "Succeeded")
            {
                break;
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
        assert!(
            timeline
                .iter()
                .any(|event| event["kind"]["UserText"]["text"] == "hello")
        );
        assert!(
            timeline
                .iter()
                .any(|event| event["kind"]["RunState"]["state"] == "Succeeded")
        );
        let snapshot = daemon
            .command(Command::GetSession {
                session_id: session.id.to_string(),
            })
            .await
            .unwrap();
        assert_eq!(
            snapshot["timeline"].as_array().unwrap().len(),
            timeline.len()
        );
        let home = daemon.home.clone();
        bridge.abort();
        daemon.service.shutdown().await;
        let reopened = AppService::open_at(home).await.unwrap();
        let persisted = reopened.replay_events(session.id, None, 64).await.unwrap();
        assert_eq!(persisted.len(), timeline.len());
        reopened.shutdown().await;
    }

    #[tokio::test]
    async fn image_prompt_is_routed_over_ipc_and_stored_as_a_durable_reference() {
        let (daemon, bridge, temp) = fixture_daemon().await;
        let session = fixture_session(&daemon, temp.path(), "image-prompt").await;
        let image_path = temp.path().join("input.png");
        let image_bytes = b"\x89PNG\r\n\x1a\nfixture image";
        tokio::fs::write(&image_path, image_bytes).await.unwrap();

        let response = daemon
            .command(Command::PromptWithImages {
                session_id: session.id.to_string(),
                text: "describe the image".into(),
                image_paths: vec![image_path.to_string_lossy().into_owned()],
                request_key: Some("image-request".into()),
            })
            .await
            .unwrap();
        assert_eq!(response["restored"], false);
        let run_id: RunId = response["run_id"].as_str().unwrap().parse().unwrap();
        assert_eq!(
            daemon.service.wait_run(run_id).await.unwrap(),
            RunLifecycle::Succeeded
        );

        let snapshot = daemon
            .service
            .session_snapshot(session.id, 32)
            .await
            .unwrap();
        let attachment = snapshot
            .events
            .iter()
            .find_map(|event| match &event.kind {
                TimelineEventKind::Attachment(attachment) => Some(attachment),
                _ => None,
            })
            .expect("IPC image prompt persists attachment metadata");
        assert_eq!(attachment.mime_type, "image/png");
        assert_eq!(
            tokio::fs::read(
                daemon
                    .home
                    .join("attachments")
                    .join(&attachment.storage_key)
            )
            .await
            .unwrap(),
            image_bytes
        );
        let timeline_json = serde_json::to_string(&snapshot.events).unwrap();
        assert!(!timeline_json.contains("iVBORw0KGgo="));

        bridge.abort();
        daemon.service.shutdown().await;
    }

    #[tokio::test]
    async fn workspace_agent_session_listing_and_load_commands_are_wired() {
        let (daemon, bridge, temp) = fixture_daemon().await;
        let session = fixture_session(&daemon, temp.path(), "list-controls").await;
        let agent = daemon
            .command(Command::GetAgent {
                agent_id: session.agent_id.to_string(),
            })
            .await
            .unwrap();
        let workspace_id = agent["workspace_id"].as_str().unwrap().to_owned();

        assert_eq!(
            daemon
                .command(Command::ListWorkspaces)
                .await
                .unwrap()
                .as_array()
                .unwrap()
                .len(),
            1
        );
        assert_eq!(
            daemon
                .command(Command::GetWorkspace {
                    workspace_id: workspace_id.clone()
                })
                .await
                .unwrap()["id"],
            workspace_id
        );
        assert_eq!(
            daemon
                .command(Command::ListAgents {
                    workspace_id: workspace_id.clone()
                })
                .await
                .unwrap()
                .as_array()
                .unwrap()
                .len(),
            1
        );
        assert_eq!(
            daemon
                .command(Command::ListSessions {
                    workspace_id: Some(workspace_id)
                })
                .await
                .unwrap()
                .as_array()
                .unwrap()
                .len(),
            1
        );
        assert_eq!(
            daemon
                .command(Command::LoadSession {
                    session_id: session.id.to_string()
                })
                .await
                .unwrap()["id"],
            session.id.to_string()
        );
        bridge.abort();
        daemon.service.shutdown().await;
    }

    #[tokio::test]
    async fn provider_diagnostics_and_agent_controls_are_wired() {
        let (daemon, bridge, temp) = fixture_daemon().await;
        let session = fixture_session(&daemon, temp.path(), "provider-controls").await;
        assert!(
            daemon
                .command(Command::ListProviders)
                .await
                .unwrap()
                .as_array()
                .unwrap()
                .iter()
                .any(|provider| provider["id"] == "fixture")
        );
        assert_eq!(
            daemon
                .command(Command::ProviderDiagnostics {
                    provider_id: Some("fixture".into())
                })
                .await
                .unwrap()["available"],
            true
        );
        for command in [
            Command::SetModel {
                session_id: session.id.to_string(),
                model_id: "unknown-model".into(),
            },
            Command::SetMode {
                session_id: session.id.to_string(),
                mode_id: "unknown-mode".into(),
            },
            Command::SetConfigOption {
                session_id: session.id.to_string(),
                option_id: "thought_level".into(),
                value: "unknown-value".into(),
            },
        ] {
            let error = daemon.command(command).await.unwrap_err();
            assert_eq!(error.code, -32011);
        }
        assert!(
            daemon
                .command(Command::SetModel {
                    session_id: session.id.to_string(),
                    model_id: "fixture/model-a".into()
                })
                .await
                .unwrap()["updated"]
                .as_bool()
                .unwrap()
        );
        assert!(
            daemon
                .command(Command::SetMode {
                    session_id: session.id.to_string(),
                    mode_id: "fixture-mode".into()
                })
                .await
                .unwrap()["updated"]
                .as_bool()
                .unwrap()
        );
        assert!(
            daemon
                .command(Command::SetConfigOption {
                    session_id: session.id.to_string(),
                    option_id: "thought_level".into(),
                    value: "high".into()
                })
                .await
                .unwrap()["updated"]
                .as_bool()
                .unwrap()
        );
        bridge.abort();
        daemon.service.shutdown().await;
    }

    #[tokio::test]
    async fn permission_decision_and_cancel_resolve_runs_safely() {
        let (daemon, bridge, temp) = fixture_daemon().await;
        let session = fixture_session(&daemon, temp.path(), "permissions").await;
        let permission_prompt = daemon
            .command(Command::Prompt {
                session_id: session.id.to_string(),
                text: "permission".into(),
                request_key: None,
            })
            .await
            .unwrap();
        let permission_run: RunId = permission_prompt["run_id"]
            .as_str()
            .unwrap()
            .parse()
            .unwrap();
        let mut permission_id = None;
        for _ in 0..100 {
            let events = daemon
                .command(Command::ReplaySession {
                    session_id: session.id.to_string(),
                    after: 0,
                    limit: Some(64),
                })
                .await
                .unwrap();
            permission_id = events["events"]
                .as_array()
                .unwrap()
                .iter()
                .find_map(|event| {
                    event["kind"]["PermissionRequested"]["id"]
                        .as_str()
                        .map(str::to_owned)
                });
            if permission_id.is_some() {
                break;
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
        let permission_id = permission_id.expect("fixture permission request should be durable");
        let decision = daemon
            .command(Command::PermissionDecision {
                permission_id,
                decision: PermissionDecision::Allow,
            })
            .await
            .unwrap();
        assert_eq!(decision["option_id"], "allow_once");
        assert_eq!(
            daemon.service.wait_run(permission_run).await.unwrap(),
            RunLifecycle::Succeeded
        );

        let cancel_prompt = tokio::time::timeout(Duration::from_secs(1), async {
            loop {
                match daemon
                    .command(Command::Prompt {
                        session_id: session.id.to_string(),
                        text: "cancel".into(),
                        request_key: None,
                    })
                    .await
                {
                    Ok(response) => break response,
                    Err(error) if error.code == -32009 => {
                        tokio::time::sleep(Duration::from_millis(1)).await;
                    }
                    Err(error) => panic!("unexpected prompt failure: {error:?}"),
                }
            }
        })
        .await
        .expect("completed run should release the session");
        let cancel_run: RunId = cancel_prompt["run_id"].as_str().unwrap().parse().unwrap();
        daemon
            .command(Command::Cancel {
                session_id: session.id.to_string(),
            })
            .await
            .unwrap();
        assert_eq!(
            daemon.service.wait_run(cancel_run).await.unwrap(),
            RunLifecycle::Cancelled
        );
        bridge.abort();
        daemon.service.shutdown().await;
    }

    #[tokio::test]
    async fn list_pending_permissions_reports_session_scoped_requests() {
        let (daemon, bridge, temp) = fixture_daemon().await;
        let first = fixture_session(&daemon, temp.path(), "perm-one").await;
        let second = fixture_session(&daemon, temp.path(), "perm-two").await;
        let first_run: RunId = daemon
            .command(Command::Prompt {
                session_id: first.id.to_string(),
                text: "permission".into(),
                request_key: None,
            })
            .await
            .unwrap()["run_id"]
            .as_str()
            .unwrap()
            .parse()
            .unwrap();
        let second_run: RunId = daemon
            .command(Command::Prompt {
                session_id: second.id.to_string(),
                text: "permission".into(),
                request_key: None,
            })
            .await
            .unwrap()["run_id"]
            .as_str()
            .unwrap()
            .parse()
            .unwrap();

        let pending = poll_pending(&daemon, 2).await;
        assert_eq!(pending.len(), 2);

        let first_key = first.id.to_string();
        let second_key = second.id.to_string();
        let first_entry = pending
            .iter()
            .find(|item| item["session_id"].as_str() == Some(first_key.as_str()))
            .expect("first pending permission");
        let second_entry = pending
            .iter()
            .find(|item| item["session_id"].as_str() == Some(second_key.as_str()))
            .expect("second pending permission");
        assert_eq!(
            first_entry["run_id"].as_str().unwrap(),
            first_run.to_string()
        );
        assert_eq!(first_entry["action"].as_str().unwrap(), "Fixture tool");
        assert_eq!(
            second_entry["run_id"].as_str().unwrap(),
            second_run.to_string()
        );
        let first_permission = first_entry["id"].as_str().unwrap().to_owned();
        let second_permission = second_entry["id"].as_str().unwrap().to_owned();

        daemon
            .command(Command::PermissionDecision {
                permission_id: first_permission,
                decision: PermissionDecision::Allow,
            })
            .await
            .unwrap();
        assert_eq!(
            daemon.service.wait_run(first_run).await.unwrap(),
            RunLifecycle::Succeeded
        );
        let remaining = daemon
            .command(Command::ListPendingPermissions)
            .await
            .unwrap();
        let remaining = remaining.as_array().unwrap();
        assert_eq!(remaining.len(), 1);
        assert_eq!(
            remaining[0]["session_id"].as_str().unwrap(),
            second.id.to_string()
        );

        daemon
            .command(Command::PermissionDecision {
                permission_id: second_permission,
                decision: PermissionDecision::Deny,
            })
            .await
            .unwrap();
        assert_eq!(
            daemon.service.wait_run(second_run).await.unwrap(),
            RunLifecycle::Succeeded
        );
        let cleared = daemon
            .command(Command::ListPendingPermissions)
            .await
            .unwrap();
        assert_eq!(cleared.as_array().unwrap().len(), 0);
        bridge.abort();
        daemon.service.shutdown().await;
    }

    #[tokio::test]
    async fn usage_summary_aggregates_billed_token_usage() {
        let (daemon, bridge, temp) = fixture_daemon().await;
        let session = fixture_session(&daemon, temp.path(), "usage").await;
        let run: RunId = daemon
            .command(Command::Prompt {
                session_id: session.id.to_string(),
                text: "usage".into(),
                request_key: None,
            })
            .await
            .unwrap()["run_id"]
            .as_str()
            .unwrap()
            .parse()
            .unwrap();
        assert_eq!(
            daemon.service.wait_run(run).await.unwrap(),
            RunLifecycle::Succeeded
        );
        let unknown_session = fixture_session(&daemon, temp.path(), "usage-unknown").await;
        let summary = daemon
            .command(Command::UsageSummary { session_id: None })
            .await
            .unwrap();
        assert_eq!(summary["totals"]["input_tokens"], 10);
        assert_eq!(summary["totals"]["output_tokens"], 20);
        assert_eq!(summary["totals"]["cached_read_tokens"], 5);
        assert_eq!(summary["totals"]["cached_write_tokens"], 2);
        assert!(summary["totals"]["cost_micros"].is_null());
        let sessions = summary["sessions"].as_array().unwrap();
        assert_eq!(sessions.len(), 2);
        assert_eq!(
            sessions[0]["session_id"].as_str().unwrap(),
            session.id.to_string()
        );
        assert_eq!(sessions[0]["input_tokens"], 10);
        let unknown = sessions
            .iter()
            .find(|entry| entry["session_id"] == unknown_session.id.to_string())
            .unwrap();
        assert!(unknown["input_tokens"].is_null());
        assert!(unknown["cost_micros"].is_null());

        let repeated = daemon
            .command(Command::UsageSummary { session_id: None })
            .await
            .unwrap();
        assert_eq!(repeated["totals"], summary["totals"]);

        let single = daemon
            .command(Command::UsageSummary {
                session_id: Some(session.id.to_string()),
            })
            .await
            .unwrap();
        assert_eq!(single["sessions"].as_array().unwrap().len(), 1);
        bridge.abort();
        daemon.service.shutdown().await;
    }

    #[tokio::test]
    async fn archive_and_rename_sessions() {
        let (daemon, bridge, temp) = fixture_daemon().await;
        let session = fixture_session(&daemon, temp.path(), "archive-rename").await;

        let renamed = daemon
            .command(Command::RenameSession {
                session_id: session.id.to_string(),
                title: "Renamed title".into(),
            })
            .await
            .unwrap();
        assert_eq!(renamed["title"], "Renamed title");
        assert_eq!(
            daemon
                .command(Command::GetAgent {
                    agent_id: session.agent_id.to_string(),
                })
                .await
                .unwrap()["title"],
            "Renamed title"
        );

        let archived = daemon
            .command(Command::ArchiveSession {
                session_id: session.id.to_string(),
            })
            .await
            .unwrap();
        assert_eq!(archived["archived"], true);
        let loaded = daemon
            .command(Command::GetSession {
                session_id: session.id.to_string(),
            })
            .await
            .unwrap();
        assert_eq!(loaded["session"]["lifecycle"], "Archived");
        bridge.abort();
        daemon.service.shutdown().await;
    }

    #[tokio::test]
    async fn workspace_lifecycle_commands_preserve_history_and_reject_active_runs() {
        let (daemon, bridge, temp) = fixture_daemon().await;
        let session = fixture_session(&daemon, temp.path(), "workspace-lifecycle").await;
        let original = daemon.service.list_workspaces().await.unwrap().remove(0);
        let accepted = daemon
            .command(Command::Prompt {
                session_id: session.id.to_string(),
                text: "permission".into(),
                request_key: None,
            })
            .await
            .unwrap();
        let run_id: RunId = accepted["run_id"].as_str().unwrap().parse().unwrap();
        let pending = poll_pending(&daemon, 1).await;
        let error = daemon
            .command(Command::ArchiveWorkspace {
                workspace_id: original.id.to_string(),
            })
            .await
            .unwrap_err();
        assert_eq!(error.code, -32009);
        assert_eq!(
            daemon
                .service
                .workspace(original.id)
                .await
                .unwrap()
                .lifecycle,
            talos_domain::WorkspaceLifecycle::Active
        );

        let renamed = daemon
            .command(Command::RenameWorkspace {
                workspace_id: original.id.to_string(),
                name: "renamed workspace".into(),
            })
            .await
            .unwrap();
        assert_eq!(renamed["id"], original.id.to_string());
        assert_eq!(renamed["name"], "renamed workspace");

        daemon
            .command(Command::PermissionDecision {
                permission_id: pending[0]["id"].as_str().unwrap().to_owned(),
                decision: PermissionDecision::Allow,
            })
            .await
            .unwrap();
        assert_eq!(
            daemon.service.wait_run(run_id).await.unwrap(),
            RunLifecycle::Succeeded
        );
        let history_before_archive = daemon
            .command(Command::ReplaySession {
                session_id: session.id.to_string(),
                after: 0,
                limit: Some(10),
            })
            .await
            .unwrap()["events"]
            .as_array()
            .unwrap()
            .len();
        let archived = daemon
            .command(Command::ArchiveWorkspace {
                workspace_id: original.id.to_string(),
            })
            .await
            .unwrap();
        assert_eq!(archived["lifecycle"], "Archived");
        let restored = daemon
            .command(Command::RestoreWorkspace {
                workspace_id: original.id.to_string(),
            })
            .await
            .unwrap();
        assert_eq!(restored["lifecycle"], "Active");
        assert_eq!(restored["id"], original.id.to_string());

        let loaded = daemon
            .command(Command::GetSession {
                session_id: session.id.to_string(),
            })
            .await
            .unwrap();
        assert_eq!(loaded["session"]["id"], session.id.to_string());
        let history = daemon
            .command(Command::ReplaySession {
                session_id: session.id.to_string(),
                after: 0,
                limit: Some(10),
            })
            .await
            .unwrap();
        assert_eq!(
            history["events"].as_array().unwrap().len(),
            history_before_archive
        );

        bridge.abort();
        daemon.service.shutdown().await;
    }

    #[tokio::test]
    async fn list_models_exposes_thinking_options_and_shutdown_signals() {
        let shutdown = Arc::new(Notify::new());
        let temp = tempfile::tempdir().unwrap();
        let service = AppService::open_at(temp.path().join("home")).await.unwrap();
        service
            .register_provider(Arc::new(FixtureProvider {
                id: ProviderId::new("fixture").unwrap(),
            }))
            .await;
        let (daemon, bridge) = Daemon::new(temp.path().join("home"), service, shutdown.clone());
        let session = fixture_session(&daemon, temp.path(), "models").await;

        let models = daemon
            .command(Command::ListModels {
                provider_id: "fixture".into(),
            })
            .await
            .unwrap();
        let models = models.as_array().unwrap();
        assert_eq!(models.len(), 1);
        assert_eq!(models[0]["id"], "fixture/model-a");
        assert_eq!(models[0]["label"], "Fixture Model A");
        assert_eq!(models[0]["default_thinking_option_id"], "minimal");
        assert_eq!(models[0]["thinking_options"].as_array().unwrap().len(), 3);

        let thinking = daemon
            .command(Command::SetThinking {
                session_id: session.id.to_string(),
                thinking_option_id: "high".into(),
            })
            .await
            .unwrap();
        assert_eq!(thinking["updated"], true);

        let response = daemon.command(Command::Shutdown).await.unwrap();
        assert_eq!(response["shutdown"], true);
        tokio::time::timeout(Duration::from_secs(1), shutdown.notified())
            .await
            .expect("shutdown should notify the accept loop");
        bridge.abort();
        daemon.service.shutdown().await;
    }

    #[tokio::test]
    async fn snapshot_keeps_unavailable_provider_without_catalogue() {
        let (daemon, bridge, _temp) = fixture_daemon().await;
        let mut diagnostic = FixtureProvider {
            id: ProviderId::new("missing-provider").unwrap(),
        }
        .diagnostic()
        .await;
        diagnostic.descriptor.available = false;
        diagnostic.executable_found = false;
        let value = daemon.snapshot_provider(&diagnostic).await.unwrap();
        assert_eq!(value["available"], false);
        assert_eq!(value["models"], json!([]));
        assert_eq!(value["modes"], json!([]));
        bridge.abort();
        daemon.service.shutdown().await;
    }

    #[tokio::test]
    async fn snapshot_includes_pending_usage_and_provider_models() {
        let (daemon, bridge, temp) = fixture_daemon().await;
        let session = fixture_session(&daemon, temp.path(), "snapshot").await;
        daemon
            .command(Command::Prompt {
                session_id: session.id.to_string(),
                text: "permission".into(),
                request_key: None,
            })
            .await
            .unwrap();
        let _pending = poll_pending(&daemon, 1).await;
        let snapshot = daemon.command(Command::Snapshot).await.unwrap();
        assert_eq!(snapshot["version"], PROTOCOL_VERSION);
        assert_eq!(snapshot["pending_permissions"].as_array().unwrap().len(), 1);
        assert!(snapshot["usage"]["totals"].is_object());
        let provider = snapshot["providers"]
            .as_array()
            .unwrap()
            .iter()
            .find(|provider| provider["id"] == "fixture")
            .expect("fixture provider in snapshot");
        assert_eq!(provider["model_options"][0]["id"], "fixture/model-a");
        bridge.abort();
        daemon.service.shutdown().await;
    }

    #[cfg(unix)]
    #[tokio::test]
    async fn authenticated_socket_exposes_streamed_durable_timeline_events() {
        use tokio::net::UnixListener;

        let (daemon, bridge, temp) = fixture_daemon().await;
        let session = fixture_session(&daemon, temp.path(), "socket-project").await;

        let socket = temp.path().join("daemon.sock");
        let listener = UnixListener::bind(&socket).unwrap();
        let server_daemon = daemon.clone();
        let server_events = server_daemon.event_tx.subscribe();
        let server = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            serve_connection_with_events(
                stream,
                Some("fixture-token".into()),
                move |command| {
                    let daemon = server_daemon.clone();
                    async move { daemon.command(command).await }
                },
                server_events,
            )
            .await
            .unwrap();
        });
        let stream = tokio::net::UnixStream::connect(&socket).await.unwrap();
        let mut client = Client::new(stream, Some("fixture-token".into()));
        client.negotiate().await.unwrap();
        let result = client
            .call(Command::Prompt {
                session_id: session.id.to_string(),
                text: "from socket".into(),
                request_key: None,
            })
            .await
            .unwrap();
        assert!(result["run_id"].as_str().is_some());
        let frame = tokio::time::timeout(Duration::from_secs(2), client.next_frame())
            .await
            .unwrap()
            .unwrap();
        let ServerFrame::Event(event) = frame else {
            panic!("expected a durable timeline notification");
        };
        assert_eq!(event.version, PROTOCOL_VERSION);
        assert!(event.event.payload["session_id"].as_str().is_some());
        drop(client);
        server.await.unwrap();
        bridge.abort();
        daemon.service.shutdown().await;
    }

    #[test]
    fn permission_decisions_map_to_advertised_provider_option_ids() {
        let options = vec![
            json!({"id":"allow_always","label":"Always allow"}),
            json!({"id":"allow_once","label":"Allow once"}),
            json!({"id":"reject_once","label":"Reject once"}),
        ];
        assert_eq!(
            select_permission_option(&options, PermissionDecision::Allow).unwrap(),
            "allow_once"
        );
        assert_eq!(
            select_permission_option(&options, PermissionDecision::Deny).unwrap(),
            "reject_once"
        );
    }
}
