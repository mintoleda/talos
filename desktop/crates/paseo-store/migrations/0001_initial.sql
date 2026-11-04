CREATE TABLE workspaces (
 id TEXT PRIMARY KEY NOT NULL, name TEXT NOT NULL, path TEXT NOT NULL, kind TEXT NOT NULL,
 lifecycle TEXT NOT NULL, metadata TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE agents (
 id TEXT PRIMARY KEY NOT NULL, workspace_id TEXT NOT NULL REFERENCES workspaces(id), provider TEXT NOT NULL,
 model TEXT, mode TEXT, lifecycle TEXT NOT NULL, title TEXT NOT NULL, config TEXT NOT NULL,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE sessions (
 id TEXT PRIMARY KEY NOT NULL, agent_id TEXT NOT NULL REFERENCES agents(id), lifecycle TEXT NOT NULL,
 provider_session_id TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE timeline_events (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 sequence INTEGER NOT NULL CHECK(sequence > 0), id TEXT NOT NULL UNIQUE, run_id TEXT,
 timestamp TEXT NOT NULL, kind TEXT NOT NULL, payload TEXT NOT NULL,
 PRIMARY KEY(session_id, sequence)
);
CREATE TABLE config_metadata (key TEXT PRIMARY KEY NOT NULL, value TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE INDEX timeline_events_run ON timeline_events(run_id, sequence);
CREATE INDEX sessions_agent ON sessions(agent_id, created_at);
