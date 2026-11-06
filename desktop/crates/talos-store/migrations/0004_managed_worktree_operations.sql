-- Intent is committed before Git is allowed to mutate a managed checkout.
-- No workspace FK: a create intent precedes the workspace row itself.
CREATE TABLE managed_worktree_operations (
 workspace_id TEXT PRIMARY KEY NOT NULL,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 operation TEXT NOT NULL,
 last_error TEXT
);
