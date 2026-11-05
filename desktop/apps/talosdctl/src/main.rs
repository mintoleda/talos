//! Native, daemon-only command-line client for talosd. Never opens legacy state.
use anyhow::{Context, Result, anyhow, bail};
use clap::{Parser, Subcommand};
use serde_json::Value;
use std::{collections::BTreeMap, ffi::OsString, path::PathBuf};
use talos_ipc::{Command, IpcError, PermissionDecision, ServerFrame, unix};

#[derive(Debug, Parser)]
#[command(name = "talosdctl", version, about = "Control the local talosd daemon")]
struct Cli {
    /// Rust-owned home (default: ~/.talosd; never ~/.paseo).
    #[arg(long, env = "TALOSD_HOME", global = true)]
    home: Option<PathBuf>,
    /// Daemon Unix socket (default: <home>/talosd.sock).
    #[arg(long, env = "TALOSD_SOCKET", global = true)]
    socket: Option<PathBuf>,
    /// Daemon bearer token, if configured. Avoid supplying secrets in shell history.
    #[arg(long, env = "TALOSD_AUTH_TOKEN", global = true, hide_env_values = true)]
    auth_token: Option<String>,
    /// Emit the unmodified daemon result as JSON.
    #[arg(long, global = true)]
    json: bool,
    #[command(subcommand)]
    command: Action,
}

#[derive(Debug, Subcommand)]
enum Action {
    /// Check daemon liveness.
    Health,
    /// Show the daemon's current snapshot.
    Status,
    /// Inspect available providers.
    Provider {
        #[command(subcommand)]
        action: ProviderAction,
    },
    /// Manage workspaces.
    Workspace {
        #[command(subcommand)]
        action: WorkspaceAction,
    },
    /// Manage agent sessions.
    Session {
        #[command(subcommand)]
        action: SessionAction,
    },
    /// Search durable conversation text.
    History {
        #[command(subcommand)]
        action: HistoryAction,
    },
    /// Replay durable timeline events, or tail the live stream.
    Events {
        #[arg(long, default_value_t = 0)]
        after: u64,
        #[arg(long)]
        limit: Option<u32>,
        #[command(subcommand)]
        command: Option<EventsCommand>,
    },
    /// Inspect agents (the title-bearing owners of sessions).
    Agent {
        #[command(subcommand)]
        action: AgentAction,
    },
    /// Inspect provider model catalogues.
    Model {
        #[command(subcommand)]
        action: ModelAction,
    },
    /// Shut down the daemon.
    Shutdown,
    /// Inspect and resolve permission requests.
    Permission {
        #[command(subcommand)]
        action: PermissionAction,
    },
}

#[derive(Debug, Subcommand)]
enum ProviderAction {
    List,
    /// Inspect a provider's daemon-reported availability and capabilities.
    Diagnostic {
        id: String,
    },
}

#[derive(Debug, Subcommand)]
enum WorkspaceAction {
    List,
    Show {
        id: String,
    },
    #[command(alias = "create")]
    Add {
        name: String,
        path: PathBuf,
    },
}

#[derive(Debug, Subcommand)]
enum AgentAction {
    List {
        #[arg(long)]
        workspace: Option<String>,
    },
    Show {
        id: String,
    },
}

#[derive(Debug, Subcommand)]
enum ModelAction {
    /// List models advertised by a provider's loaded sessions.
    List {
        #[arg(long)]
        provider: String,
    },
}

#[derive(Debug, Subcommand)]
enum EventsCommand {
    /// Replay with independent per-session cursors for lossless pagination.
    Replay {
        /// JSON object mapping session ids to their last delivered sequence.
        #[arg(long, default_value = "{}", value_parser = parse_session_cursors)]
        cursors: BTreeMap<String, u64>,
        #[arg(long)]
        limit: Option<u32>,
    },
    /// Tail the live event stream after an optional replay cursor.
    Tail {
        #[arg(long, default_value_t = 0)]
        after: u64,
        #[arg(long)]
        limit: Option<u32>,
        /// Keep streaming new events until interrupted.
        #[arg(long)]
        follow: bool,
    },
}

#[derive(Debug, Subcommand)]
enum SessionAction {
    List {
        #[arg(long)]
        workspace: Option<String>,
    },
    /// Create an agent and its first session in one call.
    New {
        workspace: String,
        #[arg(long)]
        provider: Option<String>,
        #[arg(long)]
        title: Option<String>,
    },
    Show {
        id: String,
    },
    Load {
        id: String,
    },
    Archive {
        id: String,
    },
    Rename {
        id: String,
        title: String,
    },
    SetModel {
        id: String,
        model: String,
    },
    SetMode {
        id: String,
        mode: String,
    },
    SetConfig {
        id: String,
        key: String,
        value: String,
    },
    SetThinking {
        id: String,
        thinking: String,
    },
    Prompt {
        id: String,
        text: String,
        #[arg(long)]
        request_key: Option<String>,
    },
    Cancel {
        id: String,
    },
    /// Replay one session, or use the legacy aggregate view when omitted.
    Replay {
        #[arg(long)]
        session: Option<String>,
        #[arg(long, default_value_t = 0)]
        after: u64,
        #[arg(long)]
        limit: Option<u32>,
    },
}

#[derive(Debug, Subcommand)]
enum HistoryAction {
    /// Search user and assistant text events, newest first.
    Search {
        query: String,
        /// Restrict results to one session ID.
        #[arg(long)]
        session: Option<String>,
        /// Maximum number of matching events to return (default 50, maximum 1000).
        #[arg(long)]
        limit: Option<u32>,
    },
}

#[derive(Debug, Subcommand)]
enum PermissionAction {
    /// List pending permission requests.
    List,
    Allow {
        id: String,
    },
    Deny {
        id: String,
    },
}

impl Cli {
    fn home_path(&self) -> Result<PathBuf> {
        match &self.home {
            Some(path) => Ok(path.clone()),
            None => Ok(PathBuf::from(
                std::env::var_os("HOME").context("HOME is unset; use --home or --socket")?,
            )
            .join(".talosd")),
        }
    }
    fn socket_path(&self) -> Result<PathBuf> {
        match &self.socket {
            Some(path) => Ok(path.clone()),
            None => Ok(self.home_path()?.join("talosd.sock")),
        }
    }
    async fn token(&self) -> Result<Option<String>> {
        if let Some(token) = &self.auth_token {
            return Ok(Some(token.clone()));
        }
        // Only read credentials in the Rust-owned home, never the legacy Paseo tree.
        let path = self.home_path()?.join("auth.token");
        let metadata = match tokio::fs::symlink_metadata(&path).await {
            Ok(metadata) => metadata,
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(None),
            Err(error) => {
                return Err(error).with_context(|| format!("cannot inspect {}", path.display()));
            }
        };
        if !metadata.file_type().is_file() {
            bail!("auth token must be a regular file: {}", path.display());
        }
        let token = tokio::fs::read_to_string(&path)
            .await
            .with_context(|| format!("cannot read {}", path.display()))?;
        if token.trim().is_empty() {
            bail!("auth token file is empty: {}", path.display());
        }
        Ok(Some(token.trim().to_owned()))
    }
}

fn command_for(action: &Action) -> Result<Command> {
    match action {
        Action::Health => Ok(Command::Health),
        Action::Status => Ok(Command::Snapshot),
        Action::Provider { .. } => Ok(Command::ListProviders),
        Action::Workspace { action } => workspace_command(action),
        Action::Session { action } => session_command(action),
        Action::History { action } => history_command(action),
        Action::Events {
            after,
            limit,
            command,
        } => Ok(events_command(*after, *limit, command.as_ref())),
        Action::Agent { action } => Ok(agent_command(action)),
        Action::Model { action } => Ok(model_command(action)),
        Action::Shutdown => Ok(Command::Shutdown),
        Action::Permission { action } => Ok(permission_command(action)),
    }
}

fn workspace_command(action: &WorkspaceAction) -> Result<Command> {
    match action {
        WorkspaceAction::List => Ok(Command::ListWorkspaces),
        WorkspaceAction::Show { id } => Ok(Command::GetWorkspace {
            workspace_id: id.clone(),
        }),
        WorkspaceAction::Add { name, path } => {
            if name.trim().is_empty() {
                bail!("workspace name cannot be empty");
            }
            Ok(Command::CreateWorkspace {
                name: name.clone(),
                path: path
                    .to_str()
                    .context("workspace path must be valid UTF-8")?
                    .to_owned(),
            })
        }
    }
}

fn agent_command(action: &AgentAction) -> Command {
    match action {
        AgentAction::List {
            workspace: Some(workspace),
        } => Command::ListAgents {
            workspace_id: workspace.clone(),
        },
        AgentAction::List { workspace: None } => Command::Snapshot,
        AgentAction::Show { id } => Command::GetAgent {
            agent_id: id.clone(),
        },
    }
}

fn model_command(action: &ModelAction) -> Command {
    match action {
        ModelAction::List { provider } => Command::ListModels {
            provider_id: provider.clone(),
        },
    }
}

fn permission_command(action: &PermissionAction) -> Command {
    match action {
        PermissionAction::List => Command::ListPendingPermissions,
        PermissionAction::Allow { id } => Command::PermissionDecision {
            permission_id: id.clone(),
            decision: PermissionDecision::Allow,
        },
        PermissionAction::Deny { id } => Command::PermissionDecision {
            permission_id: id.clone(),
            decision: PermissionDecision::Deny,
        },
    }
}

fn parse_session_cursors(value: &str) -> std::result::Result<BTreeMap<String, u64>, String> {
    serde_json::from_str(value).map_err(|error| format!("invalid session cursor map: {error}"))
}

fn events_command(after: u64, limit: Option<u32>, command: Option<&EventsCommand>) -> Command {
    match command {
        Some(EventsCommand::Replay { cursors, limit }) => Command::ReplaySessions {
            after: cursors.clone(),
            limit: *limit,
        },
        Some(EventsCommand::Tail {
            after: tail_after,
            limit: tail_limit,
            ..
        }) => Command::Replay {
            after: *tail_after,
            limit: *tail_limit,
        },
        None => Command::Replay { after, limit },
    }
}

fn session_command(action: &SessionAction) -> Result<Command> {
    match action {
        SessionAction::List { workspace } => Ok(Command::ListSessions {
            workspace_id: workspace.clone(),
        }),
        SessionAction::New {
            workspace,
            provider,
            title,
        } => {
            if workspace.trim().is_empty() {
                bail!("workspace id cannot be empty");
            }
            if title
                .as_deref()
                .is_some_and(|value| value.trim().is_empty())
            {
                bail!("session title cannot be empty when --title is supplied");
            }
            Ok(Command::CreateAgent {
                workspace_id: workspace.clone(),
                provider_id: provider.clone(),
                title: title.clone(),
            })
        }
        SessionAction::Show { id } => Ok(Command::GetSession {
            session_id: id.clone(),
        }),
        SessionAction::Load { id } => Ok(Command::LoadSession {
            session_id: id.clone(),
        }),
        SessionAction::Archive { id } => Ok(Command::ArchiveSession {
            session_id: id.clone(),
        }),
        SessionAction::Rename { id, title } => {
            if title.trim().is_empty() {
                bail!("session title cannot be empty");
            }
            Ok(Command::RenameSession {
                session_id: id.clone(),
                title: title.clone(),
            })
        }
        SessionAction::SetModel { id, model } => Ok(Command::SetModel {
            session_id: id.clone(),
            model_id: model.clone(),
        }),
        SessionAction::SetMode { id, mode } => Ok(Command::SetMode {
            session_id: id.clone(),
            mode_id: mode.clone(),
        }),
        SessionAction::SetConfig { id, key, value } => Ok(Command::SetConfigOption {
            session_id: id.clone(),
            option_id: key.clone(),
            value: value.clone(),
        }),
        SessionAction::SetThinking { id, thinking } => Ok(Command::SetThinking {
            session_id: id.clone(),
            thinking_option_id: thinking.clone(),
        }),
        SessionAction::Prompt {
            id,
            text,
            request_key,
        } => {
            if text.trim().is_empty() {
                bail!("prompt text cannot be empty");
            }
            Ok(Command::Prompt {
                session_id: id.clone(),
                text: text.clone(),
                request_key: request_key.clone(),
            })
        }
        SessionAction::Cancel { id } => Ok(Command::Cancel {
            session_id: id.clone(),
        }),
        SessionAction::Replay {
            session,
            after,
            limit,
        } => Ok(match session {
            Some(id) => Command::ReplaySession {
                session_id: id.clone(),
                after: *after,
                limit: *limit,
            },
            None => Command::Replay {
                after: *after,
                limit: *limit,
            },
        }),
    }
}

fn history_command(action: &HistoryAction) -> Result<Command> {
    match action {
        HistoryAction::Search {
            query,
            session,
            limit,
        } => {
            if query.trim().is_empty() {
                bail!("history search query cannot be empty");
            }
            if query.len() > 512 {
                bail!("history search query cannot exceed 512 bytes");
            }
            if session
                .as_deref()
                .is_some_and(|session| session.trim().is_empty())
            {
                bail!("history search session cannot be empty");
            }
            Ok(Command::SearchTimeline {
                query: query.clone(),
                session_id: session.clone(),
                limit: *limit,
            })
        }
    }
}

/// The `events tail --follow` path streams frames instead of returning once.
fn follow_args(action: &Action) -> Option<(u64, Option<u32>)> {
    match action {
        Action::Events {
            command:
                Some(EventsCommand::Tail {
                    after,
                    limit,
                    follow: true,
                }),
            ..
        } => Some((*after, *limit)),
        _ => None,
    }
}

fn select_result(action: &Action, value: Value) -> Result<Value> {
    if let Action::Provider {
        action: ProviderAction::Diagnostic { id },
    } = action
    {
        let providers = value
            .as_array()
            .or_else(|| value.get("providers").and_then(Value::as_array))
            .context("unexpected provider-list response (expected array or providers array)")?;
        return providers
            .iter()
            .find(|item| item.get("id").and_then(Value::as_str) == Some(id.as_str()))
            .cloned()
            .ok_or_else(|| anyhow!("provider {id:?} not found; run `talosdctl provider list`"));
    }
    if let Action::Agent {
        action: AgentAction::List { workspace: None },
    } = action
    {
        return value
            .get("agents")
            .cloned()
            .ok_or_else(|| anyhow!("unexpected snapshot response (missing agents array)"));
    }
    Ok(value)
}

fn human(value: &Value) -> String {
    // Preserve nested data (especially replay event payloads) rather than silently
    // discarding fields which may carry permission details or diagnostic context.
    if value.get("events").is_some() || value.get("event").is_some() {
        return serde_json::to_string_pretty(value).unwrap_or_else(|_| value.to_string());
    }
    match value {
        Value::Array(items) if items.is_empty() => "(none)".into(),
        Value::Array(items) => items.iter().map(human).collect::<Vec<_>>().join("\n"),
        Value::Object(fields) => {
            if fields.is_empty() {
                return "{}".into();
            }
            if let (Some(agent), Some(session)) = (fields.get("agent"), fields.get("session")) {
                return format!(
                    "agent: {}  title: {}  session: {}  provider: {}",
                    field_of(agent, "id"),
                    field_of(agent, "title"),
                    field_of(session, "id"),
                    field_of(agent, "provider")
                );
            }
            if fields.values().any(Value::is_object) || fields.values().any(Value::is_array) {
                return serde_json::to_string_pretty(value).unwrap_or_else(|_| value.to_string());
            }
            fields
                .iter()
                .map(|(key, value)| format!("{key}: {}", scalar(value)))
                .collect::<Vec<_>>()
                .join("  ")
        }
        _ => scalar(value),
    }
}

fn scalar(value: &Value) -> String {
    value
        .as_str()
        .map_or_else(|| value.to_string(), str::to_owned)
}

fn field_of<'a>(value: &'a Value, key: &str) -> &'a str {
    value.get(key).and_then(Value::as_str).unwrap_or("-")
}

fn format_value(cli: &Cli, value: &Value) -> String {
    if cli.json {
        serde_json::to_string_pretty(value).unwrap_or_else(|_| value.to_string())
    } else {
        human(value)
    }
}

#[cfg(unix)]
async fn execute(cli: &Cli) -> Result<Value> {
    let command = command_for(&cli.command)?;
    let socket = cli.socket_path()?;
    let mut client = unix::connect(&socket, cli.token().await?)
        .await
        .with_context(|| {
            format!(
                "cannot connect to daemon at {}; start `talosd` or pass --socket/--home",
                socket.display()
            )
        })?;
    client.negotiate().await.map_err(|e| ipc_error(&e))?;
    let result = client.call(command).await.map_err(|e| ipc_error(&e))?;
    select_result(&cli.command, result)
}

#[cfg(unix)]
async fn follow_events(cli: &Cli, after: u64, limit: Option<u32>) -> Result<()> {
    let socket = cli.socket_path()?;
    let mut client = unix::connect(&socket, cli.token().await?)
        .await
        .with_context(|| {
            format!(
                "cannot connect to daemon at {}; start `talosd` or pass --socket/--home",
                socket.display()
            )
        })?;
    client
        .negotiate()
        .await
        .map_err(|error| ipc_error(&error))?;
    let sessions = client
        .call(Command::ListSessions { workspace_id: None })
        .await
        .map_err(|error| ipc_error(&error))?;
    let mut cursors: BTreeMap<String, u64> = sessions
        .as_array()
        .context("daemon returned an invalid session list")?
        .iter()
        .filter_map(|session| session.get("id").and_then(Value::as_str))
        .map(|id| (id.to_owned(), after))
        .collect();
    replay_follow_events(cli, &mut client, &mut cursors, limit).await?;
    loop {
        match client.next_frame().await {
            Ok(ServerFrame::Event(event)) => {
                if event.event.kind == "replay_required" {
                    replay_follow_events(cli, &mut client, &mut cursors, limit).await?;
                    continue;
                }
                let payload = &event.event.payload;
                let (session, sequence) = timeline_position(payload)?;
                let previous = cursors.get(session).copied().unwrap_or(0);
                if sequence > previous.saturating_add(1) {
                    replay_follow_events(cli, &mut client, &mut cursors, limit).await?;
                }
                if sequence <= cursors.get(session).copied().unwrap_or(0) {
                    continue;
                }
                cursors.insert(session.to_owned(), sequence);
                let value = serde_json::to_value(&event)?;
                println!("{}", format_value(cli, &value));
            }
            Ok(ServerFrame::Response(_)) => {}
            Err(IpcError::Closed) => break,
            Err(error) => return Err(ipc_error(&error)),
        }
    }
    Ok(())
}

fn timeline_position(event: &Value) -> Result<(&str, u64)> {
    let session = event
        .get("session_id")
        .and_then(Value::as_str)
        .context("timeline event has no session id")?;
    let sequence = event
        .get("sequence")
        .and_then(Value::as_u64)
        .context("timeline event has no sequence")?;
    Ok((session, sequence))
}

#[cfg(unix)]
async fn replay_follow_events(
    cli: &Cli,
    client: &mut talos_ipc::Client<tokio::net::UnixStream>,
    cursors: &mut BTreeMap<String, u64>,
    limit: Option<u32>,
) -> Result<()> {
    loop {
        let page = client
            .call(Command::ReplaySessions {
                after: cursors.clone(),
                limit,
            })
            .await
            .map_err(|error| ipc_error(&error))?;
        let events = page
            .get("events")
            .and_then(Value::as_array)
            .context("daemon returned an invalid replay page")?;
        let mut progressed = false;
        for event in events {
            let (session, sequence) = timeline_position(event)?;
            if sequence > cursors.get(session).copied().unwrap_or(0) {
                cursors.insert(session.to_owned(), sequence);
                progressed = true;
            }
        }
        println!("{}", format_value(cli, &page));
        if page.get("has_more").and_then(Value::as_bool) != Some(true) {
            return Ok(());
        }
        if !progressed {
            bail!("daemon replay did not advance its per-session cursors");
        }
    }
}

#[cfg(unix)]
async fn run(cli: &Cli) -> Result<()> {
    if let Some((after, limit)) = follow_args(&cli.command) {
        return follow_events(cli, after, limit).await;
    }
    let value = execute(cli).await?;
    println!("{}", format_value(cli, &value));
    Ok(())
}

fn ipc_error(error: &IpcError) -> anyhow::Error {
    match error {
        IpcError::Remote(-32003, _) => {
            anyhow!(
                "daemon authentication failed; check <home>/auth.token, --auth-token or TALOSD_AUTH_TOKEN"
            )
        }
        IpcError::Remote(-32001, _) | IpcError::Negotiation(_) => {
            anyhow!("incompatible daemon protocol: {error}; upgrade the daemon and CLI together")
        }
        _ => anyhow!("daemon request failed: {error}"),
    }
}

#[tokio::main]
async fn main() -> Result<()> {
    let cli = Cli::parse_from(std::env::args_os().collect::<Vec<OsString>>());
    #[cfg(unix)]
    {
        run(&cli).await
    }
    #[cfg(not(unix))]
    {
        let _ = &cli;
        bail!("this build requires a Unix socket; Windows IPC is not yet supported");
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use clap::Parser;
    use serde_json::json;

    #[test]
    fn replay_commands_preserve_per_session_positions() {
        let cli = Cli::try_parse_from([
            "talosdctl",
            "events",
            "replay",
            "--cursors",
            "{\"s1\":7,\"s2\":2}",
            "--limit",
            "1",
        ])
        .unwrap();
        assert_eq!(
            command_for(&cli.command).unwrap(),
            Command::ReplaySessions {
                after: [("s1".into(), 7), ("s2".into(), 2)].into(),
                limit: Some(1),
            }
        );
        let cli = Cli::try_parse_from([
            "talosdctl",
            "session",
            "replay",
            "--session",
            "s1",
            "--after",
            "7",
        ])
        .unwrap();
        assert_eq!(
            command_for(&cli.command).unwrap(),
            Command::ReplaySession {
                session_id: "s1".into(),
                after: 7,
                limit: None,
            }
        );
        assert!(
            Cli::try_parse_from(["talosdctl", "events", "replay", "--cursors", "{\"s\":-1}"])
                .is_err()
        );
        assert_eq!(
            timeline_position(&json!({"session_id":"s1","sequence":9})).unwrap(),
            ("s1", 9)
        );
        assert!(timeline_position(&json!({"sequence":9})).is_err());
    }

    #[test]
    fn parser_maps_commands_and_flags() {
        let cli = Cli::try_parse_from([
            "talosdctl",
            "--json",
            "--home",
            "/tmp/talosd-test",
            "session",
            "prompt",
            "s1",
            "hello",
            "--request-key",
            "once",
        ])
        .unwrap();
        assert!(cli.json);
        assert_eq!(
            cli.socket_path().unwrap(),
            PathBuf::from("/tmp/talosd-test/talosd.sock")
        );
        assert_eq!(
            command_for(&cli.command).unwrap(),
            Command::Prompt {
                session_id: "s1".into(),
                text: "hello".into(),
                request_key: Some("once".into())
            }
        );

        let cli = Cli::try_parse_from(["talosdctl", "workspace", "show", "w1"]).unwrap();
        assert_eq!(
            command_for(&cli.command).unwrap(),
            Command::GetWorkspace {
                workspace_id: "w1".into()
            }
        );

        let cli =
            Cli::try_parse_from(["talosdctl", "events", "--after", "3", "--limit", "5"]).unwrap();
        assert_eq!(
            command_for(&cli.command).unwrap(),
            Command::Replay {
                after: 3,
                limit: Some(5)
            }
        );

        let cli = Cli::try_parse_from(["talosdctl", "permission", "deny", "p1"]).unwrap();
        assert_eq!(
            command_for(&cli.command).unwrap(),
            Command::PermissionDecision {
                permission_id: "p1".into(),
                decision: PermissionDecision::Deny
            }
        );
    }

    #[test]
    fn new_verbs_map_to_additive_ipc_commands() {
        let cli = Cli::try_parse_from([
            "talosdctl",
            "session",
            "new",
            "w1",
            "--provider",
            "talos",
            "--title",
            "Refactor",
        ])
        .unwrap();
        assert_eq!(
            command_for(&cli.command).unwrap(),
            Command::CreateAgent {
                workspace_id: "w1".into(),
                provider_id: Some("talos".into()),
                title: Some("Refactor".into())
            }
        );

        let cli = Cli::try_parse_from(["talosdctl", "session", "archive", "s1"]).unwrap();
        assert_eq!(
            command_for(&cli.command).unwrap(),
            Command::ArchiveSession {
                session_id: "s1".into()
            }
        );

        let cli =
            Cli::try_parse_from(["talosdctl", "session", "rename", "s1", "New name"]).unwrap();
        assert_eq!(
            command_for(&cli.command).unwrap(),
            Command::RenameSession {
                session_id: "s1".into(),
                title: "New name".into()
            }
        );

        let cli =
            Cli::try_parse_from(["talosdctl", "session", "set-thinking", "s1", "high"]).unwrap();
        assert_eq!(
            command_for(&cli.command).unwrap(),
            Command::SetThinking {
                session_id: "s1".into(),
                thinking_option_id: "high".into()
            }
        );

        let cli =
            Cli::try_parse_from(["talosdctl", "model", "list", "--provider", "talos"]).unwrap();
        assert_eq!(
            command_for(&cli.command).unwrap(),
            Command::ListModels {
                provider_id: "talos".into()
            }
        );

        let cli = Cli::try_parse_from(["talosdctl", "permission", "list"]).unwrap();
        assert_eq!(
            command_for(&cli.command).unwrap(),
            Command::ListPendingPermissions
        );

        let cli = Cli::try_parse_from(["talosdctl", "agent", "show", "a1"]).unwrap();
        assert_eq!(
            command_for(&cli.command).unwrap(),
            Command::GetAgent {
                agent_id: "a1".into()
            }
        );

        let cli = Cli::try_parse_from(["talosdctl", "shutdown"]).unwrap();
        assert_eq!(command_for(&cli.command).unwrap(), Command::Shutdown);

        let cli = Cli::try_parse_from([
            "talosdctl",
            "history",
            "search",
            "important phrase",
            "--session",
            "s1",
            "--limit",
            "20",
        ])
        .unwrap();
        assert_eq!(
            command_for(&cli.command).unwrap(),
            Command::SearchTimeline {
                query: "important phrase".into(),
                session_id: Some("s1".into()),
                limit: Some(20),
            }
        );
        assert!(
            Cli::try_parse_from(["talosdctl", "history", "search", "   "])
                .map_err(|error| error.to_string())
                .and_then(|cli| command_for(&cli.command).map_err(|error| error.to_string()))
                .is_err()
        );
    }

    #[test]
    fn events_tail_follow_is_detected_for_streaming() {
        let cli = Cli::try_parse_from(["talosdctl", "events", "tail", "--follow", "--after", "9"])
            .unwrap();
        assert_eq!(follow_args(&cli.command), Some((9, None)));
        assert_eq!(
            command_for(&cli.command).unwrap(),
            Command::Replay {
                after: 9,
                limit: None
            }
        );

        let cli = Cli::try_parse_from(["talosdctl", "events", "--after", "4"]).unwrap();
        assert_eq!(follow_args(&cli.command), None);
    }

    #[test]
    fn agent_list_without_workspace_selects_snapshot_agents() {
        let cli = Cli::try_parse_from(["talosdctl", "agent", "list"]).unwrap();
        assert_eq!(command_for(&cli.command).unwrap(), Command::Snapshot);
        assert_eq!(
            select_result(
                &cli.command,
                json!({"agents":[{"id":"a1","title":"Alpha"}],"sessions":[]})
            )
            .unwrap(),
            json!([{"id":"a1","title":"Alpha"}])
        );

        let cli = Cli::try_parse_from(["talosdctl", "agent", "list", "--workspace", "w1"]).unwrap();
        assert_eq!(
            command_for(&cli.command).unwrap(),
            Command::ListAgents {
                workspace_id: "w1".into()
            }
        );
    }

    #[test]
    fn output_and_provider_selection() {
        assert_eq!(human(&json!([])), "(none)");
        assert_eq!(
            human(&json!({"id":"a", "name":"Alpha"})),
            "id: a  name: Alpha"
        );
        assert!(
            human(&json!({"events":[{"event":{"payload":{"text":"hello"}}}]})).contains("hello")
        );
        assert!(
            human(&json!({
                "agent": {"id":"a1","title":"Refactor","provider":"talos"},
                "session": {"id":"s1"}
            }))
            .contains("session: s1")
        );

        let action = Action::Provider {
            action: ProviderAction::Diagnostic { id: "talos".into() },
        };
        assert_eq!(
            select_result(
                &action,
                json!({"providers":[{"id":"talos","available":true}]})
            )
            .unwrap(),
            json!({"id":"talos","available":true})
        );
    }

    #[cfg(unix)]
    #[tokio::test]
    async fn follow_replay_drains_all_pages_with_independent_session_cursors() {
        let (client_io, server_io) = tokio::net::UnixStream::pair().unwrap();
        let mut page = 0;
        let server = tokio::spawn(async move {
            talos_ipc::serve_connection(server_io, None, move |command| {
                let Command::ReplaySessions { after, limit } = command else {
                    panic!("expected replay")
                };
                assert_eq!(limit, Some(1));
                assert_eq!(after["s2"], 1);
                let response = if page == 0 {
                    assert_eq!(after["s1"], 9);
                    json!({"events":[{"session_id":"s1","sequence":10}],"has_more":true})
                } else {
                    assert_eq!(after["s1"], 10);
                    json!({"events":[{"session_id":"s2","sequence":2}],"has_more":false})
                };
                page += 1;
                async move { Ok(response) }
            })
            .await
            .unwrap();
        });
        let cli = Cli::try_parse_from(["talosdctl", "--json", "health"]).unwrap();
        let mut client = talos_ipc::Client::new(client_io, None);
        client.negotiate().await.unwrap();
        let mut cursors = [("s1".into(), 9), ("s2".into(), 1)].into();
        replay_follow_events(&cli, &mut client, &mut cursors, Some(1))
            .await
            .unwrap();
        assert_eq!(cursors, [("s1".into(), 10), ("s2".into(), 2)].into());
        drop(client);
        server.await.unwrap();
    }

    #[cfg(unix)]
    #[tokio::test]
    async fn follow_replay_rejects_a_stalled_page_instead_of_spinning() {
        let (client_io, server_io) = tokio::net::UnixStream::pair().unwrap();
        let server = tokio::spawn(async move {
            talos_ipc::serve_connection(server_io, None, |_| async {
                Ok(json!({"events":[],"has_more":true}))
            })
            .await
            .unwrap();
        });
        let cli = Cli::try_parse_from(["talosdctl", "health"]).unwrap();
        let mut client = talos_ipc::Client::new(client_io, None);
        client.negotiate().await.unwrap();
        let error = replay_follow_events(&cli, &mut client, &mut BTreeMap::new(), Some(1))
            .await
            .unwrap_err();
        assert!(error.to_string().contains("did not advance"));
        drop(client);
        server.await.unwrap();
    }

    #[cfg(unix)]
    #[tokio::test]
    async fn rejected_token_is_reported() {
        use tokio::net::UnixListener;
        let root = std::env::temp_dir().join(format!(
            "talosdctl-auth-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir(&root).unwrap();
        let path = root.join("talosd.sock");
        let listener = UnixListener::bind(&path).unwrap();
        let server = tokio::spawn(async move {
            talos_ipc::unix::serve(listener, Some("expected".into()), |_| async {
                Ok(json!(null))
            })
            .await
            .unwrap();
        });
        let cli = Cli::try_parse_from([
            "talosdctl",
            "--home",
            root.to_str().unwrap(),
            "--auth-token",
            "wrong",
            "health",
        ])
        .unwrap();
        assert!(
            execute(&cli)
                .await
                .unwrap_err()
                .to_string()
                .contains("authentication failed")
        );
        server.await.unwrap();
        std::fs::remove_file(path).unwrap();
        std::fs::remove_dir(root).unwrap();
    }

    #[cfg(unix)]
    #[tokio::test]
    async fn temporary_socket_negotiates_and_calls() {
        use tokio::net::UnixListener;
        let root = std::env::temp_dir().join(format!(
            "talosdctl-test-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir(&root).unwrap();
        let path = root.join("talosd.sock");
        let listener = UnixListener::bind(&path).unwrap();
        std::fs::write(root.join("auth.token"), "secret\n").unwrap();
        let server = tokio::spawn(async move {
            talos_ipc::unix::serve(listener, Some("secret".into()), |command| async move {
                assert_eq!(command, Command::Health);
                Ok(json!({"status":"ok"}))
            })
            .await
            .unwrap();
        });
        let cli =
            Cli::try_parse_from(["talosdctl", "--home", root.to_str().unwrap(), "health"]).unwrap();
        assert_eq!(execute(&cli).await.unwrap(), json!({"status":"ok"}));
        server.await.unwrap();
        std::fs::remove_file(&path).unwrap();
        std::fs::remove_file(root.join("auth.token")).unwrap();
        std::fs::remove_dir(&root).unwrap();
    }
}
