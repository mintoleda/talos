//! SQLite persistence for Talos desktop domain aggregates and ordered event streams.
#![forbid(unsafe_code)]

use chrono::{DateTime, Utc};
use rusqlite::{Connection, OptionalExtension, Transaction, TransactionBehavior, params};
use serde::{Serialize, de::DeserializeOwned};
use std::{path::Path, str::FromStr, time::Duration};
use talos_domain::{
    Agent, AgentId, EventCursor, ProviderId, RunId, RunLifecycle, Session, SessionId,
    TimelineEvent, TimelineEventKind, ValidationError, Workspace, WorkspaceId,
};
use thiserror::Error;

const MIGRATION_1: &str = include_str!("../migrations/0001_initial.sql");
const MIGRATION_2: &str = include_str!("../migrations/0002_prompt_requests.sql");
const CURRENT_SCHEMA_VERSION: i64 = 2;

#[derive(Debug, Error)]
pub enum StoreError {
    #[error("sqlite error: {0}")]
    Sqlite(#[from] rusqlite::Error),
    #[error("serialization error: {0}")]
    Serialization(#[from] serde_json::Error),
    #[error("domain validation error: {0}")]
    Validation(#[from] ValidationError),
    #[error("record not found: {0}")]
    NotFound(String),
    #[error("sequence conflict for session {session}: sequence {sequence}")]
    SequenceConflict { session: SessionId, sequence: u64 },
    #[error("invalid persisted value: {0}")]
    InvalidData(String),
    #[error("schema version {0} is newer than this build supports")]
    UnsupportedSchemaVersion(u32),
}

pub type Result<T> = std::result::Result<T, StoreError>;

pub struct SqliteStore {
    connection: Connection,
}
impl SqliteStore {
    pub fn open(path: impl AsRef<Path>) -> Result<Self> {
        let c = Connection::open(path)?;
        Self::from_connection(c)
    }
    pub fn open_in_memory() -> Result<Self> {
        Self::from_connection(Connection::open_in_memory()?)
    }
    fn from_connection(connection: Connection) -> Result<Self> {
        connection.busy_timeout(Duration::from_secs(5))?;
        connection.pragma_update(None, "foreign_keys", "ON")?;
        connection.pragma_update(None, "journal_mode", "WAL")?;
        connection.pragma_update(None, "synchronous", "NORMAL")?;
        let mut store = Self { connection };
        store.migrate()?;
        Ok(store)
    }
    fn migrate(&mut self) -> Result<()> {
        self.connection.execute_batch("CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);")?;
        let version: i64 = self.connection.query_row(
            "SELECT COALESCE(MAX(version),0) FROM schema_migrations",
            [],
            |r| r.get(0),
        )?;
        if version > CURRENT_SCHEMA_VERSION {
            return Err(StoreError::UnsupportedSchemaVersion(
                u32::try_from(version).unwrap_or(u32::MAX),
            ));
        }
        if version < 1 {
            let tx = self.connection.transaction()?;
            tx.execute_batch(MIGRATION_1)?;
            tx.execute(
                "INSERT INTO schema_migrations(version,applied_at) VALUES(1,?1)",
                params![Utc::now().to_rfc3339()],
            )?;
            tx.commit()?;
        }
        let version: i64 = self.connection.query_row(
            "SELECT COALESCE(MAX(version),0) FROM schema_migrations",
            [],
            |r| r.get(0),
        )?;
        if version < 2 {
            let tx = self.connection.transaction()?;
            tx.execute_batch(MIGRATION_2)?;
            tx.execute(
                "INSERT INTO schema_migrations(version,applied_at) VALUES(2,?1)",
                params![Utc::now().to_rfc3339()],
            )?;
            tx.commit()?;
        }
        Ok(())
    }
    pub fn schema_version(&self) -> Result<u32> {
        Ok(self.connection.query_row(
            "SELECT COALESCE(MAX(version),0) FROM schema_migrations",
            [],
            |r| r.get::<_, u32>(0),
        )?)
    }

    pub fn save_workspace(&mut self, value: &Workspace) -> Result<()> {
        self.connection.execute("INSERT INTO workspaces(id,name,path,kind,lifecycle,metadata,created_at,updated_at) VALUES(?1,?2,?3,?4,?5,?6,?7,?8) ON CONFLICT(id) DO UPDATE SET name=excluded.name,path=excluded.path,kind=excluded.kind,lifecycle=excluded.lifecycle,metadata=excluded.metadata,updated_at=excluded.updated_at",
            params![value.id.to_string(),value.name,value.path,json(&value.kind)?,json(&value.lifecycle)?,json(&value.metadata)?,ts(value.created_at),ts(value.updated_at)])?;
        Ok(())
    }
    pub fn workspace(&self, id: WorkspaceId) -> Result<Option<Workspace>> {
        self.connection.query_row("SELECT id,name,path,kind,lifecycle,metadata,created_at,updated_at FROM workspaces WHERE id=?1",[id.to_string()],|r| Ok(Workspace{id:parse_id(r.get::<_,String>(0)?)?,name:r.get(1)?,path:r.get(2)?,kind:decode(r.get(3)?)?,lifecycle:decode(r.get(4)?)?,metadata:decode(r.get(5)?)?,created_at:parse_ts(r.get(6)?)?,updated_at:parse_ts(r.get(7)?)?})).optional().map_err(Into::into)
    }
    pub fn list_workspaces(&self) -> Result<Vec<Workspace>> {
        let mut s=self.connection.prepare("SELECT id,name,path,kind,lifecycle,metadata,created_at,updated_at FROM workspaces ORDER BY created_at,id")?;
        let rows = s.query_map([], |r| {
            Ok(Workspace {
                id: parse_id(r.get::<_, String>(0)?)?,
                name: r.get(1)?,
                path: r.get(2)?,
                kind: decode(r.get(3)?)?,
                lifecycle: decode(r.get(4)?)?,
                metadata: decode(r.get(5)?)?,
                created_at: parse_ts(r.get(6)?)?,
                updated_at: parse_ts(r.get(7)?)?,
            })
        })?;
        rows.collect::<std::result::Result<Vec<_>, _>>()
            .map_err(Into::into)
    }
    pub fn save_agent(&mut self, value: &Agent) -> Result<()> {
        self.connection.execute("INSERT INTO agents(id,workspace_id,provider,model,mode,lifecycle,title,config,created_at,updated_at) VALUES(?1,?2,?3,?4,?5,?6,?7,?8,?9,?10) ON CONFLICT(id) DO UPDATE SET workspace_id=excluded.workspace_id,provider=excluded.provider,model=excluded.model,mode=excluded.mode,lifecycle=excluded.lifecycle,title=excluded.title,config=excluded.config,updated_at=excluded.updated_at",
        params![value.id.to_string(),value.workspace_id.to_string(),value.provider.as_str(),option_json(value.model.as_ref())?,value.mode,json(&value.lifecycle)?,value.title,json(&value.config)?,ts(value.created_at),ts(value.updated_at)])?;
        Ok(())
    }
    pub fn agent(&self, id: AgentId) -> Result<Option<Agent>> {
        self.connection.query_row("SELECT id,workspace_id,provider,model,mode,lifecycle,title,config,created_at,updated_at FROM agents WHERE id=?1",[id.to_string()],|r|Ok(Agent{id:parse_id(r.get::<_,String>(0)?)?,workspace_id:parse_id(r.get::<_,String>(1)?)?,provider:ProviderId::new(r.get::<_,String>(2)?).map_err(to_sql_conversion)?,model:decode_optional(r.get(3)?)?,mode:r.get(4)?,lifecycle:decode(r.get(5)?)?,title:r.get(6)?,config:decode(r.get(7)?)?,created_at:parse_ts(r.get(8)?)?,updated_at:parse_ts(r.get(9)?)?})).optional().map_err(Into::into)
    }
    pub fn list_agents(&self, workspace_id: WorkspaceId) -> Result<Vec<Agent>> {
        let mut s=self.connection.prepare("SELECT id,workspace_id,provider,model,mode,lifecycle,title,config,created_at,updated_at FROM agents WHERE workspace_id=?1 ORDER BY created_at,id")?;
        s.query_map([workspace_id.to_string()], |r| {
            Ok(Agent {
                id: parse_id(r.get::<_, String>(0)?)?,
                workspace_id: parse_id(r.get::<_, String>(1)?)?,
                provider: ProviderId::new(r.get::<_, String>(2)?).map_err(to_sql_conversion)?,
                model: decode_optional(r.get(3)?)?,
                mode: r.get(4)?,
                lifecycle: decode(r.get(5)?)?,
                title: r.get(6)?,
                config: decode(r.get(7)?)?,
                created_at: parse_ts(r.get(8)?)?,
                updated_at: parse_ts(r.get(9)?)?,
            })
        })?
        .collect::<std::result::Result<Vec<_>, _>>()
        .map_err(StoreError::Sqlite)
    }
    pub fn save_session(&mut self, value: &Session) -> Result<()> {
        self.connection.execute("INSERT INTO sessions(id,agent_id,lifecycle,provider_session_id,created_at,updated_at) VALUES(?1,?2,?3,?4,?5,?6) ON CONFLICT(id) DO UPDATE SET agent_id=excluded.agent_id,lifecycle=excluded.lifecycle,provider_session_id=excluded.provider_session_id,updated_at=excluded.updated_at",params![value.id.to_string(),value.agent_id.to_string(),json(&value.lifecycle)?,value.provider_session_id,ts(value.created_at),ts(value.updated_at)])?;
        Ok(())
    }
    pub fn session(&self, id: SessionId) -> Result<Option<Session>> {
        self.connection.query_row("SELECT id,agent_id,lifecycle,provider_session_id,created_at,updated_at FROM sessions WHERE id=?1",[id.to_string()],|r|Ok(Session{id:parse_id(r.get::<_,String>(0)?)?,agent_id:parse_id(r.get::<_,String>(1)?)?,lifecycle:decode(r.get(2)?)?,provider_session_id:r.get(3)?,created_at:parse_ts(r.get(4)?)?,updated_at:parse_ts(r.get(5)?)?})).optional().map_err(Into::into)
    }
    pub fn list_sessions(&self, agent_id: AgentId) -> Result<Vec<Session>> {
        let mut s=self.connection.prepare("SELECT id,agent_id,lifecycle,provider_session_id,created_at,updated_at FROM sessions WHERE agent_id=?1 ORDER BY created_at,id")?;
        s.query_map([agent_id.to_string()], |r| {
            Ok(Session {
                id: parse_id(r.get::<_, String>(0)?)?,
                agent_id: parse_id(r.get::<_, String>(1)?)?,
                lifecycle: decode(r.get(2)?)?,
                provider_session_id: r.get(3)?,
                created_at: parse_ts(r.get(4)?)?,
                updated_at: parse_ts(r.get(5)?)?,
            })
        })?
        .collect::<std::result::Result<Vec<_>, _>>()
        .map_err(StoreError::Sqlite)
    }
    pub fn append_event(&mut self, event: &TimelineEvent) -> Result<()> {
        let tx = self.connection.transaction()?;
        insert_event(&tx, event)?;
        tx.commit()?;
        Ok(())
    }
    pub fn append_events(&mut self, events: &[TimelineEvent]) -> Result<()> {
        let tx = self.connection.transaction()?;
        for event in events {
            insert_event(&tx, event)?;
        }
        tx.commit()?;
        Ok(())
    }
    pub fn replay_events(
        &self,
        session: SessionId,
        after: Option<EventCursor>,
        limit: usize,
    ) -> Result<Vec<TimelineEvent>> {
        let limit = i64::try_from(limit.clamp(1, 10_000)).expect("bounded limit fits i64");
        let after = after.map_or(0, |cursor| cursor.sequence);
        let after = i64::try_from(after)
            .map_err(|_| StoreError::InvalidData("event cursor exceeds SQLite range".into()))?;
        let mut s=self.connection.prepare("SELECT id,session_id,run_id,sequence,timestamp,payload FROM timeline_events WHERE session_id=?1 AND sequence>?2 ORDER BY sequence LIMIT ?3")?;
        s.query_map(params![session.to_string(), after, limit], |r| {
            Ok(TimelineEvent {
                id: parse_id(r.get::<_, String>(0)?)?,
                session_id: parse_id(r.get::<_, String>(1)?)?,
                run_id: r.get::<_, Option<String>>(2)?.map(parse_id).transpose()?,
                sequence: u64::try_from(r.get::<_, i64>(3)?).map_err(to_sql_conversion)?,
                timestamp: parse_ts(r.get(4)?)?,
                kind: decode(r.get(5)?)?,
            })
        })?
        .collect::<std::result::Result<Vec<_>, _>>()
        .map_err(StoreError::Sqlite)
    }
    pub fn snapshot(&self, session: SessionId, limit: usize) -> Result<SessionSnapshot> {
        let record = self
            .session(session)?
            .ok_or_else(|| StoreError::NotFound(format!("session {session}")))?;
        let events = self.replay_events(session, None, limit)?;
        let cursor = events.last().map(|event| EventCursor {
            sequence: event.sequence,
        });
        Ok(SessionSnapshot {
            session: record,
            events,
            cursor,
        })
    }
    /// Look up a previously accepted prompt request for this session/key pair.
    pub fn prompt_request(
        &self,
        session_id: SessionId,
        request_key: &str,
    ) -> Result<Option<PromptRequestRecord>> {
        read_prompt_request(&self.connection, session_id, request_key)
    }

    /// Atomically accept a keyed prompt with its first timeline events.
    ///
    /// The request row, user acceptance event and initial run-state event commit
    /// together. An existing key with the same fingerprint returns the original
    /// acceptance without inserting the supplied events. A different fingerprint
    /// is reported as a conflict. Inserted events must belong to the request session.
    pub fn accept_prompt_request(
        &mut self,
        record: &PromptRequestRecord,
        events: &[TimelineEvent],
    ) -> Result<PromptRequestAcceptance> {
        if record.request_key.trim().is_empty() {
            return Err(ValidationError::Empty.into());
        }
        if record.request_key.len() > 256 {
            return Err(ValidationError::TooLong(256).into());
        }
        if record.payload_fingerprint.trim().is_empty() {
            return Err(ValidationError::Empty.into());
        }
        if events
            .iter()
            .any(|event| event.session_id != record.session_id)
        {
            return Err(StoreError::InvalidData(
                "prompt acceptance events must belong to the request session".into(),
            ));
        }
        if events
            .iter()
            .any(|event| event.run_id.is_some_and(|run_id| run_id != record.run_id))
        {
            return Err(StoreError::InvalidData(
                "prompt acceptance events must not belong to another run".into(),
            ));
        }
        // Take the write lock before checking the key. A deferred transaction can
        // let two connections both observe an absent key and then race the insert;
        // the loser would surface a raw uniqueness error instead of the original run.
        let tx = self
            .connection
            .transaction_with_behavior(TransactionBehavior::Immediate)?;
        if let Some(existing) = read_prompt_request(&tx, record.session_id, &record.request_key)? {
            tx.rollback()?;
            return Ok(
                if existing.payload_fingerprint == record.payload_fingerprint {
                    PromptRequestAcceptance::Existing(existing)
                } else {
                    PromptRequestAcceptance::Conflict { existing }
                },
            );
        }
        tx.execute(
            "INSERT INTO prompt_requests(session_id,request_key,payload_fingerprint,run_id,accepted_at) VALUES(?1,?2,?3,?4,?5)",
            params![
                record.session_id.to_string(),
                record.request_key,
                record.payload_fingerprint,
                record.run_id.to_string(),
                ts(record.accepted_at)
            ],
        )?;
        for event in events {
            insert_event(&tx, event)?;
        }
        tx.commit()?;
        Ok(PromptRequestAcceptance::Accepted(record.clone()))
    }

    /// Latest durable lifecycle state recorded for a run, if any.
    pub fn run_state(&self, session_id: SessionId, run_id: RunId) -> Result<Option<RunLifecycle>> {
        let payload: Option<String> = self
            .connection
            .query_row(
                "SELECT payload FROM timeline_events WHERE session_id=?1 AND run_id=?2 AND kind='RunState' ORDER BY sequence DESC LIMIT 1",
                params![session_id.to_string(), run_id.to_string()],
                |row| row.get(0),
            )
            .optional()?;
        let Some(payload) = payload else {
            return Ok(None);
        };
        match serde_json::from_str::<TimelineEventKind>(&payload)? {
            TimelineEventKind::RunState { state } => Ok(Some(state)),
            _ => Err(StoreError::InvalidData(
                "persisted run-state event has an unexpected payload".into(),
            )),
        }
    }

    pub fn set_config_metadata(&mut self, key: &str, value: &serde_json::Value) -> Result<()> {
        if key.trim().is_empty() {
            return Err(ValidationError::Empty.into());
        }
        self.connection.execute("INSERT INTO config_metadata(key,value,updated_at) VALUES(?1,?2,?3) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at",params![key,json(value)?,ts(Utc::now())])?;
        Ok(())
    }
    pub fn config_metadata(&self, key: &str) -> Result<Option<serde_json::Value>> {
        self.connection
            .query_row(
                "SELECT value FROM config_metadata WHERE key=?1",
                [key],
                |r| decode(r.get(0)?),
            )
            .optional()
            .map_err(Into::into)
    }
    pub fn integrity_check(&self) -> Result<bool> {
        Ok(self
            .connection
            .query_row("PRAGMA integrity_check", [], |r| r.get::<_, String>(0))?
            == "ok")
    }
}

#[derive(Clone, Debug)]
pub struct SessionSnapshot {
    pub session: Session,
    pub events: Vec<TimelineEvent>,
    pub cursor: Option<EventCursor>,
}

/// Durable idempotency record for one keyed prompt acceptance.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PromptRequestRecord {
    pub session_id: SessionId,
    pub request_key: String,
    pub payload_fingerprint: String,
    pub run_id: RunId,
    pub accepted_at: DateTime<Utc>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum PromptRequestAcceptance {
    Accepted(PromptRequestRecord),
    Existing(PromptRequestRecord),
    Conflict { existing: PromptRequestRecord },
}

fn read_prompt_request(
    connection: &Connection,
    session_id: SessionId,
    request_key: &str,
) -> Result<Option<PromptRequestRecord>> {
    connection
        .query_row(
            "SELECT session_id,request_key,payload_fingerprint,run_id,accepted_at FROM prompt_requests WHERE session_id=?1 AND request_key=?2",
            params![session_id.to_string(), request_key],
            |row| {
                Ok(PromptRequestRecord {
                    session_id: parse_id(row.get::<_, String>(0)?)?,
                    request_key: row.get(1)?,
                    payload_fingerprint: row.get(2)?,
                    run_id: parse_id(row.get::<_, String>(3)?)?,
                    accepted_at: parse_ts(row.get(4)?)?,
                })
            },
        )
        .optional()
        .map_err(Into::into)
}
fn insert_event(tx: &Transaction<'_>, event: &TimelineEvent) -> Result<()> {
    let payload = json(&event.kind)?;
    let kind = serde_json::to_value(&event.kind)?
        .as_object()
        .and_then(|o| o.keys().next().cloned())
        .unwrap_or_else(|| "unknown".into());
    let sequence = i64::try_from(event.sequence)
        .map_err(|_| StoreError::InvalidData("event sequence exceeds SQLite range".into()))?;
    let result=tx.execute("INSERT INTO timeline_events(session_id,sequence,id,run_id,timestamp,kind,payload) VALUES(?1,?2,?3,?4,?5,?6,?7)",params![event.session_id.to_string(),sequence,event.id.to_string(),event.run_id.map(|id|id.to_string()),ts(event.timestamp),kind,payload]);
    match result {
        Ok(_) => Ok(()),
        Err(rusqlite::Error::SqliteFailure(e, _))
            if e.extended_code == rusqlite::ffi::SQLITE_CONSTRAINT_PRIMARYKEY
                || e.extended_code == rusqlite::ffi::SQLITE_CONSTRAINT_UNIQUE =>
        {
            Err(StoreError::SequenceConflict {
                session: event.session_id,
                sequence: event.sequence,
            })
        }
        Err(e) => Err(e.into()),
    }
}
fn json<T: Serialize>(value: &T) -> Result<String> {
    Ok(serde_json::to_string(value)?)
}
fn option_json<T: Serialize>(value: Option<&T>) -> Result<Option<String>> {
    value.map(json).transpose()
}
#[allow(clippy::needless_pass_by_value)] // rusqlite row extraction naturally owns these values.
fn decode<T: DeserializeOwned>(text: String) -> rusqlite::Result<T> {
    serde_json::from_str(&text).map_err(to_sql_conversion)
}
fn decode_optional<T: DeserializeOwned>(text: Option<String>) -> rusqlite::Result<Option<T>> {
    text.map(decode).transpose()
}
#[allow(clippy::needless_pass_by_value)] // UUID parsing is performed on owned row values.
fn parse_id<T: FromStr<Err = ValidationError>>(value: String) -> rusqlite::Result<T> {
    value.parse().map_err(to_sql_conversion)
}
#[allow(clippy::needless_pass_by_value)] // Timestamp parsing is performed on owned row values.
fn parse_ts(value: String) -> rusqlite::Result<DateTime<Utc>> {
    DateTime::parse_from_rfc3339(&value)
        .map(|d| d.with_timezone(&Utc))
        .map_err(to_sql_conversion)
}
fn ts(value: DateTime<Utc>) -> String {
    value.to_rfc3339()
}
fn to_sql_conversion(error: impl std::fmt::Display) -> rusqlite::Error {
    rusqlite::Error::FromSqlConversionFailure(
        0,
        rusqlite::types::Type::Text,
        Box::new(std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            error.to_string(),
        )),
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use talos_domain::{TimelineEventKind, WorkspaceKind};
    fn fixture() -> (Workspace, Agent, Session) {
        let w = Workspace::new("project", "/tmp/project", WorkspaceKind::Directory).unwrap();
        let a = Agent::new(w.id, ProviderId::new("talos").unwrap(), "agent").unwrap();
        let s = Session::new(a.id);
        (w, a, s)
    }
    #[test]
    fn persistence_survives_reopen_and_replay_cursor() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("state.sqlite");
        let (w, a, s) = fixture();
        {
            let mut db = SqliteStore::open(&path).unwrap();
            db.save_workspace(&w).unwrap();
            db.save_agent(&a).unwrap();
            db.save_session(&s).unwrap();
            let e = TimelineEvent::new(
                s.id,
                None,
                1,
                TimelineEventKind::AssistantText {
                    text: "hello".into(),
                },
            )
            .unwrap();
            db.append_event(&e).unwrap();
            db.set_config_metadata("theme", &serde_json::json!({"accent":"blue"}))
                .unwrap();
            assert_eq!(db.schema_version().unwrap(), 2);
        }
        {
            let db = SqliteStore::open(&path).unwrap();
            assert_eq!(db.workspace(w.id).unwrap().unwrap(), w);
            assert_eq!(db.agent(a.id).unwrap().unwrap(), a);
            let snap = db.snapshot(s.id, 100).unwrap();
            assert_eq!(snap.events.len(), 1);
            assert_eq!(snap.cursor, Some(EventCursor { sequence: 1 }));
            assert_eq!(db.replay_events(s.id, snap.cursor, 100).unwrap().len(), 0);
            assert_eq!(
                db.config_metadata("theme").unwrap().unwrap()["accent"],
                "blue"
            );
            assert!(db.integrity_check().unwrap());
        }
    }
    #[test]
    fn migrations_are_idempotent_and_events_ordered() {
        let mut db = SqliteStore::open_in_memory().unwrap();
        let (w, a, s) = fixture();
        db.save_workspace(&w).unwrap();
        db.save_agent(&a).unwrap();
        db.save_session(&s).unwrap();
        db.migrate().unwrap();
        let events = (1..=2)
            .map(|n| {
                TimelineEvent::new(
                    s.id,
                    None,
                    n,
                    TimelineEventKind::UserText {
                        text: n.to_string(),
                    },
                )
                .unwrap()
            })
            .collect::<Vec<_>>();
        db.append_events(&events).unwrap();
        assert_eq!(
            db.replay_events(s.id, None, 10)
                .unwrap()
                .iter()
                .map(|e| e.sequence)
                .collect::<Vec<_>>(),
            vec![1, 2]
        );
        assert!(matches!(
            db.append_event(&events[0]),
            Err(StoreError::SequenceConflict { .. })
        ));
    }

    fn prompt_record(
        session_id: SessionId,
        request_key: &str,
        run_id: RunId,
        fingerprint: char,
    ) -> PromptRequestRecord {
        PromptRequestRecord {
            session_id,
            request_key: request_key.into(),
            payload_fingerprint: fingerprint.to_string().repeat(64),
            run_id,
            accepted_at: Utc::now(),
        }
    }

    #[test]
    fn prompt_acceptance_is_atomic_identity_scoped_and_ordered() {
        let mut db = SqliteStore::open_in_memory().unwrap();
        let (w, a, s) = fixture();
        db.save_workspace(&w).unwrap();
        db.save_agent(&a).unwrap();
        db.save_session(&s).unwrap();

        let run_id = RunId::new();
        let record = prompt_record(s.id, "key-1", run_id, 'a');
        let events = vec![
            TimelineEvent::new(
                s.id,
                Some(run_id),
                1,
                TimelineEventKind::UserText {
                    text: "hello".into(),
                },
            )
            .unwrap(),
            TimelineEvent::new(
                s.id,
                Some(run_id),
                2,
                TimelineEventKind::RunState {
                    state: RunLifecycle::Starting,
                },
            )
            .unwrap(),
        ];
        assert!(matches!(
            db.accept_prompt_request(&record, &events).unwrap(),
            PromptRequestAcceptance::Accepted(_)
        ));
        assert_eq!(db.prompt_request(s.id, "key-1").unwrap().unwrap(), record);
        assert_eq!(
            db.run_state(s.id, run_id).unwrap(),
            Some(RunLifecycle::Starting)
        );

        let retry = vec![
            TimelineEvent::new(
                s.id,
                Some(run_id),
                3,
                TimelineEventKind::UserText {
                    text: "retry".into(),
                },
            )
            .unwrap(),
        ];
        assert!(matches!(
            db.accept_prompt_request(&record, &retry).unwrap(),
            PromptRequestAcceptance::Existing(existing) if existing == record
        ));
        assert_eq!(db.replay_events(s.id, None, 10).unwrap().len(), 2);

        let conflict = prompt_record(s.id, "key-1", RunId::new(), 'b');
        assert!(matches!(
            db.accept_prompt_request(&conflict, &[]).unwrap(),
            PromptRequestAcceptance::Conflict { existing } if existing == record
        ));
        assert_eq!(db.replay_events(s.id, None, 10).unwrap().len(), 2);

        let running = TimelineEvent::new(
            s.id,
            Some(run_id),
            3,
            TimelineEventKind::RunState {
                state: RunLifecycle::Running,
            },
        )
        .unwrap();
        db.append_event(&running).unwrap();
        assert_eq!(
            db.run_state(s.id, run_id).unwrap(),
            Some(RunLifecycle::Running)
        );
    }

    #[test]
    fn prompt_acceptance_survives_reopen_without_duplicate_events() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("prompt.sqlite");
        let (w, a, s) = fixture();
        let run_id = RunId::new();
        let record = prompt_record(s.id, "reopen", run_id, 'd');
        let events = vec![
            TimelineEvent::new(
                s.id,
                Some(run_id),
                1,
                TimelineEventKind::UserText {
                    text: "persist me".into(),
                },
            )
            .unwrap(),
            TimelineEvent::new(
                s.id,
                Some(run_id),
                2,
                TimelineEventKind::RunState {
                    state: RunLifecycle::Starting,
                },
            )
            .unwrap(),
        ];
        {
            let mut db = SqliteStore::open(&path).unwrap();
            db.save_workspace(&w).unwrap();
            db.save_agent(&a).unwrap();
            db.save_session(&s).unwrap();
            assert!(matches!(
                db.accept_prompt_request(&record, &events).unwrap(),
                PromptRequestAcceptance::Accepted(_)
            ));
        }
        {
            let mut db = SqliteStore::open(&path).unwrap();
            assert_eq!(db.prompt_request(s.id, "reopen").unwrap().unwrap(), record);
            assert_eq!(
                db.run_state(s.id, run_id).unwrap(),
                Some(RunLifecycle::Starting)
            );
            assert!(matches!(
                db.accept_prompt_request(&record, &[]).unwrap(),
                PromptRequestAcceptance::Existing(existing) if existing == record
            ));
            let conflict = prompt_record(s.id, "reopen", RunId::new(), 'e');
            assert!(matches!(
                db.accept_prompt_request(&conflict, &[]).unwrap(),
                PromptRequestAcceptance::Conflict { existing } if existing == record
            ));
            assert_eq!(db.replay_events(s.id, None, 10).unwrap().len(), 2);
        }
    }

    #[test]
    fn prompt_acceptance_is_session_scoped_and_rolls_back_on_event_failure() {
        let mut db = SqliteStore::open_in_memory().unwrap();
        let (w, a, s) = fixture();
        db.save_workspace(&w).unwrap();
        db.save_agent(&a).unwrap();
        db.save_session(&s).unwrap();
        let run_id = RunId::new();
        let record = prompt_record(s.id, "key-1", run_id, 'a');
        let initial = vec![
            TimelineEvent::new(
                s.id,
                Some(run_id),
                1,
                TimelineEventKind::UserText {
                    text: "initial".into(),
                },
            )
            .unwrap(),
        ];
        assert!(matches!(
            db.accept_prompt_request(&record, &initial).unwrap(),
            PromptRequestAcceptance::Accepted(_)
        ));

        let second = Session::new(a.id);
        db.save_session(&second).unwrap();
        let second_record = prompt_record(second.id, "key-1", RunId::new(), 'a');
        assert!(matches!(
            db.accept_prompt_request(&second_record, &[]).unwrap(),
            PromptRequestAcceptance::Accepted(_)
        ));
        assert_ne!(
            db.prompt_request(s.id, "key-1").unwrap().unwrap().run_id,
            db.prompt_request(second.id, "key-1")
                .unwrap()
                .unwrap()
                .run_id
        );

        let reused_run = prompt_record(s.id, "key-2", run_id, 'a');
        assert!(db.accept_prompt_request(&reused_run, &[]).is_err());
        assert!(db.prompt_request(s.id, "key-2").unwrap().is_none());

        let failing_run = RunId::new();
        let failing = prompt_record(s.id, "key-3", failing_run, 'a');
        let duplicate = vec![
            TimelineEvent::new(
                s.id,
                Some(failing_run),
                1,
                TimelineEventKind::UserText {
                    text: "duplicate".into(),
                },
            )
            .unwrap(),
        ];
        assert!(matches!(
            db.accept_prompt_request(&failing, &duplicate),
            Err(StoreError::SequenceConflict { .. })
        ));
        assert!(db.prompt_request(s.id, "key-3").unwrap().is_none());
        assert_eq!(db.replay_events(s.id, None, 10).unwrap().len(), 1);

        let empty = prompt_record(s.id, "  ", RunId::new(), 'a');
        assert!(matches!(
            db.accept_prompt_request(&empty, &[]),
            Err(StoreError::Validation(ValidationError::Empty))
        ));

        let wrong_run = prompt_record(s.id, "key-4", RunId::new(), 'a');
        let wrong_event = vec![
            TimelineEvent::new(
                s.id,
                Some(RunId::new()),
                1,
                TimelineEventKind::UserText {
                    text: "wrong run".into(),
                },
            )
            .unwrap(),
        ];
        assert!(matches!(
            db.accept_prompt_request(&wrong_run, &wrong_event),
            Err(StoreError::InvalidData(_))
        ));
        assert!(db.prompt_request(s.id, "key-4").unwrap().is_none());
    }

    const CONCURRENT_ROUNDS: u64 = 8;

    fn race_prompt_acceptance(
        path: &Path,
        session_id: SessionId,
        sequence: u64,
        records: [PromptRequestRecord; 2],
    ) -> Vec<PromptRequestAcceptance> {
        let barrier = std::sync::Arc::new(std::sync::Barrier::new(2));
        let workers = records
            .into_iter()
            .map(|record| {
                let path = path.to_path_buf();
                let barrier = barrier.clone();
                let event = TimelineEvent::new(
                    session_id,
                    Some(record.run_id),
                    sequence,
                    TimelineEventKind::UserText {
                        text: format!("race {sequence}"),
                    },
                )
                .unwrap();
                std::thread::spawn(move || {
                    let mut db = SqliteStore::open(path).unwrap();
                    barrier.wait();
                    db.accept_prompt_request(&record, &[event])
                })
            })
            .collect::<Vec<_>>();
        workers
            .into_iter()
            .map(|worker| worker.join().unwrap().unwrap())
            .collect()
    }

    #[test]
    fn concurrent_prompt_acceptance_is_atomic_across_connections() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("concurrent.sqlite");
        let (w, a, s) = fixture();
        {
            let mut db = SqliteStore::open(&path).unwrap();
            db.save_workspace(&w).unwrap();
            db.save_agent(&a).unwrap();
            db.save_session(&s).unwrap();
        }

        let mut accepted_records = Vec::new();
        for round in 0..CONCURRENT_ROUNDS {
            let key = format!("identical-{round}");
            let results = race_prompt_acceptance(
                &path,
                s.id,
                round + 1,
                [
                    prompt_record(s.id, &key, RunId::new(), 'a'),
                    prompt_record(s.id, &key, RunId::new(), 'a'),
                ],
            );
            assert_eq!(
                results
                    .iter()
                    .filter(|result| matches!(result, PromptRequestAcceptance::Accepted(_)))
                    .count(),
                1,
                "round {round}: {results:?}"
            );
            assert_eq!(
                results
                    .iter()
                    .filter(|result| matches!(result, PromptRequestAcceptance::Existing(_)))
                    .count(),
                1,
                "round {round}: {results:?}"
            );
            accepted_records.extend(results.iter().find_map(|result| match result {
                PromptRequestAcceptance::Accepted(record) => Some(record.clone()),
                _ => None,
            }));
        }

        for round in 0..CONCURRENT_ROUNDS {
            let key = format!("conflict-{round}");
            let results = race_prompt_acceptance(
                &path,
                s.id,
                CONCURRENT_ROUNDS + round + 1,
                [
                    prompt_record(s.id, &key, RunId::new(), 'a'),
                    prompt_record(s.id, &key, RunId::new(), 'b'),
                ],
            );
            let accepted = results
                .iter()
                .find_map(|result| match result {
                    PromptRequestAcceptance::Accepted(record) => Some(record.clone()),
                    _ => None,
                })
                .expect("one concurrent payload should be accepted");
            let conflict = results
                .iter()
                .find_map(|result| match result {
                    PromptRequestAcceptance::Conflict { existing } => Some(existing),
                    _ => None,
                })
                .expect("the competing payload should conflict");
            assert_eq!(conflict, &accepted, "round {round}: {results:?}");
            accepted_records.push(accepted);
        }

        let db = SqliteStore::open(&path).unwrap();
        assert_eq!(
            db.replay_events(s.id, None, 100).unwrap().len(),
            usize::try_from(CONCURRENT_ROUNDS * 2).unwrap()
        );
        for record in accepted_records {
            assert_eq!(
                db.prompt_request(record.session_id, &record.request_key)
                    .unwrap()
                    .unwrap(),
                record
            );
        }
        assert!(db.integrity_check().unwrap());
    }

    #[test]
    fn v1_database_upgrades_transactionally_and_preserves_acceptance() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("legacy.sqlite");
        {
            let legacy = Connection::open(&path).unwrap();
            legacy
                .execute_batch(
                    "CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);",
                )
                .unwrap();
            legacy.execute_batch(MIGRATION_1).unwrap();
            legacy
                .execute(
                    "INSERT INTO schema_migrations(version,applied_at) VALUES(1,?1)",
                    params![Utc::now().to_rfc3339()],
                )
                .unwrap();
        }
        let (w, a, s) = fixture();
        let run_id = RunId::new();
        let record = prompt_record(s.id, "legacy", run_id, 'c');
        let events = vec![
            TimelineEvent::new(
                s.id,
                Some(run_id),
                1,
                TimelineEventKind::UserText {
                    text: "legacy".into(),
                },
            )
            .unwrap(),
            TimelineEvent::new(
                s.id,
                Some(run_id),
                2,
                TimelineEventKind::RunState {
                    state: RunLifecycle::Starting,
                },
            )
            .unwrap(),
        ];
        {
            let mut db = SqliteStore::open(&path).unwrap();
            assert_eq!(db.schema_version().unwrap(), 2);
            db.save_workspace(&w).unwrap();
            db.save_agent(&a).unwrap();
            db.save_session(&s).unwrap();
            assert!(matches!(
                db.accept_prompt_request(&record, &events).unwrap(),
                PromptRequestAcceptance::Accepted(_)
            ));
            assert!(db.integrity_check().unwrap());
        }
        {
            let mut db = SqliteStore::open(&path).unwrap();
            assert_eq!(db.schema_version().unwrap(), 2);
            assert_eq!(db.prompt_request(s.id, "legacy").unwrap().unwrap(), record);
            assert_eq!(
                db.run_state(s.id, run_id).unwrap(),
                Some(RunLifecycle::Starting)
            );
            db.migrate().unwrap();
            assert_eq!(db.schema_version().unwrap(), 2);
            assert!(db.integrity_check().unwrap());
        }
        {
            let connection = Connection::open(&path).unwrap();
            connection
                .execute(
                    "INSERT INTO schema_migrations(version,applied_at) VALUES(99,?1)",
                    params![Utc::now().to_rfc3339()],
                )
                .unwrap();
        }
        assert!(matches!(
            SqliteStore::open(&path),
            Err(StoreError::UnsupportedSchemaVersion(99))
        ));
    }
}
