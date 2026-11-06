CREATE TABLE projects (
 id TEXT PRIMARY KEY NOT NULL,
 name TEXT NOT NULL,
 root_path TEXT NOT NULL UNIQUE,
 metadata TEXT NOT NULL,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
ALTER TABLE workspaces ADD COLUMN project_id TEXT REFERENCES projects(id) ON DELETE SET NULL;
CREATE INDEX workspaces_project ON workspaces(project_id, created_at);
