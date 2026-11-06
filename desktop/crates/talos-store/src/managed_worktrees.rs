//! Durable Git/SQLite intents. Finishing a transition and clearing its intent
//! always share a transaction; filesystem decisions belong to talos-core.

use super::{
    Result, SqliteStore, StoreError, WorkspaceArchiveResult, decode, json, parse_id,
    save_workspace_row, update_workspace_row, workspace_has_active_runs_on, workspace_on,
};
use chrono::Utc;
use rusqlite::{Connection, OptionalExtension, TransactionBehavior, params};
use serde::{Deserialize, Serialize};
use talos_domain::{Workspace, WorkspaceId, WorkspaceKind, WorkspaceLifecycle};

#[derive(Clone, Copy, Debug, Deserialize, Serialize, PartialEq, Eq)]
pub enum WorktreeOperationKind {
    Create,
    Delete,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq, Eq)]
pub struct ManagedWorktreeOperation {
    pub kind: WorktreeOperationKind,
    /// Intended create record, or the record before deletion archived it.
    pub workspace: Workspace,
    /// Same-parent destination used to preserve a deleted checkout atomically.
    #[serde(default)]
    pub preserved_path: Option<String>,
    pub repo_root: String,
    pub repo_common_dir: String,
    #[serde(default)]
    pub last_error: Option<String>,
}

#[derive(Clone, Copy, Debug)]
pub enum WorktreeOperationOutcome {
    Created,
    CreateAbsent,
    Deleted,
    DeleteCancelled,
    DeletePreserved,
}

fn conflict() -> StoreError {
    StoreError::InvalidData("managed worktree operation conflicts with current records".into())
}

fn operation_on(
    connection: &Connection,
    id: WorkspaceId,
) -> Result<Option<ManagedWorktreeOperation>> {
    connection
        .query_row(
            "SELECT operation,last_error FROM managed_worktree_operations WHERE workspace_id=?1",
            [id.to_string()],
            |row| {
                let mut operation: ManagedWorktreeOperation = decode(row.get(0)?)?;
                operation.last_error = row.get(1)?;
                Ok(operation)
            },
        )
        .optional()
        .map_err(Into::into)
}

pub(super) fn ensure_no_worktree_operation(connection: &Connection, id: WorkspaceId) -> Result<()> {
    if operation_on(connection, id)?.is_some() {
        return Err(StoreError::WorktreeOperationPending(id));
    }
    Ok(())
}

fn insert_operation(connection: &Connection, operation: &ManagedWorktreeOperation) -> Result<()> {
    let workspace = &operation.workspace;
    if workspace.kind != WorkspaceKind::Worktree {
        return Err(conflict());
    }
    let project_id = workspace.project_id.ok_or_else(conflict)?;
    connection.execute(
        "INSERT INTO managed_worktree_operations(workspace_id,project_id,operation,last_error) VALUES(?1,?2,?3,?4)",
        params![workspace.id.to_string(), project_id.to_string(), json(operation)?, operation.last_error],
    )?;
    Ok(())
}

impl SqliteStore {
    pub fn pending_worktree_operations(&self) -> Result<Vec<ManagedWorktreeOperation>> {
        let mut statement = self.connection.prepare(
            "SELECT workspace_id FROM managed_worktree_operations ORDER BY workspace_id",
        )?;
        let ids = statement
            .query_map([], |row| parse_id(row.get::<_, String>(0)?))?
            .collect::<std::result::Result<Vec<WorkspaceId>, _>>()?;
        ids.into_iter()
            .map(|id| operation_on(&self.connection, id)?.ok_or_else(conflict))
            .collect()
    }

    pub fn begin_worktree_create(&mut self, operation: &ManagedWorktreeOperation) -> Result<()> {
        let tx = self
            .connection
            .transaction_with_behavior(TransactionBehavior::Immediate)?;
        if operation.kind != WorktreeOperationKind::Create
            || workspace_on(&tx, operation.workspace.id)?.is_some()
        {
            return Err(conflict());
        }
        insert_operation(&tx, operation)?;
        tx.commit()?;
        Ok(())
    }

    /// Archive and record intent atomically, excluding persisted active runs.
    pub fn begin_worktree_delete(
        &mut self,
        operation: &ManagedWorktreeOperation,
    ) -> Result<WorkspaceArchiveResult> {
        let tx = self
            .connection
            .transaction_with_behavior(TransactionBehavior::Immediate)?;
        let id = operation.workspace.id;
        let Some(mut workspace) = workspace_on(&tx, id)? else {
            return Ok(WorkspaceArchiveResult::NotFound);
        };
        if operation.kind != WorktreeOperationKind::Delete || workspace != operation.workspace {
            return Err(conflict());
        }
        if workspace_has_active_runs_on(&tx, id)? {
            return Ok(WorkspaceArchiveResult::ActiveRuns);
        }
        insert_operation(&tx, operation)?;
        workspace.archive()?;
        update_workspace_row(&tx, &workspace)?;
        tx.commit()?;
        Ok(WorkspaceArchiveResult::Archived(workspace))
    }

    pub fn record_worktree_operation_error(&mut self, id: WorkspaceId, error: &str) -> Result<()> {
        self.connection.execute(
            "UPDATE managed_worktree_operations SET last_error=?2 WHERE workspace_id=?1",
            params![id.to_string(), error],
        )?;
        Ok(())
    }

    pub fn finish_worktree_operation(
        &mut self,
        id: WorkspaceId,
        outcome: WorktreeOperationOutcome,
    ) -> Result<Option<Workspace>> {
        let tx = self
            .connection
            .transaction_with_behavior(TransactionBehavior::Immediate)?;
        let operation = operation_on(&tx, id)?.ok_or_else(conflict)?;
        let result = match (operation.kind, outcome) {
            (WorktreeOperationKind::Create, WorktreeOperationOutcome::Created) => {
                let path_owner: bool = tx.query_row(
                    "SELECT EXISTS(SELECT 1 FROM workspaces WHERE path=?1 OR id=?2)",
                    params![operation.workspace.path, id.to_string()],
                    |row| row.get(0),
                )?;
                if path_owner {
                    return Err(conflict());
                }
                save_workspace_row(&tx, &operation.workspace)?;
                Some(operation.workspace)
            }
            (WorktreeOperationKind::Create, WorktreeOperationOutcome::CreateAbsent) => None,
            (
                WorktreeOperationKind::Delete,
                WorktreeOperationOutcome::Deleted
                | WorktreeOperationOutcome::DeleteCancelled
                | WorktreeOperationOutcome::DeletePreserved,
            ) => {
                let mut workspace = workspace_on(&tx, id)?.ok_or_else(conflict)?;
                let original = &operation.workspace;
                if workspace.path != original.path
                    || workspace.project_id != original.project_id
                    || workspace.kind != original.kind
                    || workspace.metadata != original.metadata
                    || workspace.lifecycle != WorkspaceLifecycle::Archived
                    || workspace_has_active_runs_on(&tx, id)?
                {
                    return Err(conflict());
                }
                if matches!(outcome, WorktreeOperationOutcome::DeletePreserved) {
                    let preserved_path = operation.preserved_path.as_ref().ok_or_else(conflict)?;
                    workspace.path.clone_from(preserved_path);
                    workspace.metadata["managedWorktree"]["path"] = json!(preserved_path);
                    workspace.metadata["managedWorktree"]["preserved"] = json!(true);
                }
                workspace.lifecycle = if matches!(
                    outcome,
                    WorktreeOperationOutcome::Deleted | WorktreeOperationOutcome::DeletePreserved
                ) {
                    WorkspaceLifecycle::Archived
                } else {
                    original.lifecycle.clone()
                };
                workspace.updated_at = Utc::now();
                update_workspace_row(&tx, &workspace)?;
                Some(workspace)
            }
            _ => return Err(conflict()),
        };
        tx.execute(
            "DELETE FROM managed_worktree_operations WHERE workspace_id=?1",
            [id.to_string()],
        )?;
        tx.commit()?;
        Ok(result)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use talos_domain::{
        Agent, Project, ProviderId, RunId, RunLifecycle, Session, TimelineEvent, TimelineEventKind,
    };

    fn operation(db: &mut SqliteStore) -> ManagedWorktreeOperation {
        let project = Project::new("Fixture", "/tmp/fixture").unwrap();
        db.save_project(&project).unwrap();
        let mut workspace =
            Workspace::new("Feature", "/tmp/managed/feature", WorkspaceKind::Worktree).unwrap();
        workspace.project_id = Some(project.id);
        ManagedWorktreeOperation {
            kind: WorktreeOperationKind::Create,
            workspace,
            preserved_path: None,
            repo_root: project.root_path,
            repo_common_dir: "/tmp/fixture/.git".into(),
            last_error: None,
        }
    }

    #[test]
    fn create_finish_failure_rolls_back_workspace_and_keeps_intent_across_reopen() {
        let directory = tempfile::tempdir().unwrap();
        let path = directory.path().join("state.sqlite");
        let mut db = SqliteStore::open(&path).unwrap();
        let operation = operation(&mut db);
        let id = operation.workspace.id;
        db.begin_worktree_create(&operation).unwrap();
        // Fail after inserting the workspace but before clearing intent.
        db.connection.execute_batch("CREATE TRIGGER fail_finish BEFORE DELETE ON managed_worktree_operations BEGIN SELECT RAISE(ABORT,'injected finish failure'); END;").unwrap();
        assert!(
            db.finish_worktree_operation(id, WorktreeOperationOutcome::Created)
                .is_err()
        );
        assert!(db.workspace(id).unwrap().is_none());
        drop(db);
        let mut db = SqliteStore::open(&path).unwrap();
        assert_eq!(
            db.pending_worktree_operations().unwrap(),
            vec![operation.clone()]
        );
        db.connection
            .execute_batch("DROP TRIGGER fail_finish")
            .unwrap();
        db.finish_worktree_operation(id, WorktreeOperationOutcome::Created)
            .unwrap();
        drop(db);
        let db = SqliteStore::open(&path).unwrap();
        assert_eq!(db.pending_worktree_operations().unwrap().len(), 0);
        assert_eq!(db.workspace(id).unwrap(), Some(operation.workspace));
    }

    #[test]
    fn delete_prepare_and_finish_are_atomic_and_block_restore_and_reassociation() {
        let directory = tempfile::tempdir().unwrap();
        let path = directory.path().join("state.sqlite");
        let mut db = SqliteStore::open(&path).unwrap();
        let mut operation = operation(&mut db);
        let id = operation.workspace.id;
        db.save_workspace(&operation.workspace).unwrap();
        operation.kind = WorktreeOperationKind::Delete;
        db.connection.execute_batch("CREATE TRIGGER fail_archive BEFORE UPDATE ON workspaces BEGIN SELECT RAISE(ABORT,'injected archive failure'); END;").unwrap();
        assert!(db.begin_worktree_delete(&operation).is_err());
        assert_eq!(db.pending_worktree_operations().unwrap().len(), 0);
        assert_eq!(db.workspace(id).unwrap(), Some(operation.workspace.clone()));
        db.connection
            .execute_batch("DROP TRIGGER fail_archive")
            .unwrap();
        db.begin_worktree_delete(&operation).unwrap();
        assert!(matches!(
            db.save_workspace(&operation.workspace),
            Err(StoreError::WorktreeOperationPending(_))
        ));
        assert!(matches!(
            db.restore_workspace(id),
            Err(StoreError::WorktreeOperationPending(_))
        ));
        assert!(matches!(
            db.archive_workspace(id),
            Err(StoreError::WorktreeOperationPending(_))
        ));
        assert!(matches!(
            db.set_workspace_project(id, None),
            Err(StoreError::WorktreeOperationPending(_))
        ));
        db.connection.execute_batch("CREATE TRIGGER fail_finish BEFORE DELETE ON managed_worktree_operations BEGIN SELECT RAISE(ABORT,'injected finish failure'); END;").unwrap();
        assert!(
            db.finish_worktree_operation(id, WorktreeOperationOutcome::DeleteCancelled)
                .is_err()
        );
        assert_eq!(
            db.workspace(id).unwrap().unwrap().lifecycle,
            WorkspaceLifecycle::Archived
        );
        drop(db);
        let mut db = SqliteStore::open(&path).unwrap();
        assert_eq!(db.pending_worktree_operations().unwrap(), vec![operation]);
        db.connection
            .execute_batch("DROP TRIGGER fail_finish")
            .unwrap();
        db.finish_worktree_operation(id, WorktreeOperationOutcome::DeleteCancelled)
            .unwrap();
        assert_eq!(
            db.workspace(id).unwrap().unwrap().lifecycle,
            WorkspaceLifecycle::Active
        );
        assert_eq!(db.pending_worktree_operations().unwrap().len(), 0);
    }

    #[test]
    fn delete_intent_excludes_active_runs_without_partial_archive() {
        let mut db = SqliteStore::open_in_memory().unwrap();
        let mut operation = operation(&mut db);
        operation.kind = WorktreeOperationKind::Delete;
        let workspace = &operation.workspace;
        db.save_workspace(workspace).unwrap();
        let agent = Agent::new(workspace.id, ProviderId::new("talos").unwrap(), "Fixture").unwrap();
        db.save_agent(&agent).unwrap();
        let session = Session::new(agent.id);
        db.save_session(&session).unwrap();
        let event = TimelineEvent::new(
            session.id,
            Some(RunId::new()),
            1,
            TimelineEventKind::RunState {
                state: RunLifecycle::Running,
            },
        )
        .unwrap();
        db.append_event(&event).unwrap();
        assert_eq!(
            db.begin_worktree_delete(&operation).unwrap(),
            WorkspaceArchiveResult::ActiveRuns
        );
        assert_eq!(db.pending_worktree_operations().unwrap().len(), 0);
        assert_eq!(
            db.workspace(workspace.id).unwrap().as_ref(),
            Some(workspace)
        );
    }

    #[test]
    fn version_three_upgrades_without_losing_rows() {
        let directory = tempfile::tempdir().unwrap();
        let path = directory.path().join("state.sqlite");
        let mut db = SqliteStore::open(&path).unwrap();
        let operation = operation(&mut db);
        db.save_workspace(&operation.workspace).unwrap();
        db.connection.execute_batch("DROP TABLE managed_worktree_operations; DELETE FROM schema_migrations WHERE version=4;").unwrap();
        drop(db);
        let db = SqliteStore::open(&path).unwrap();
        assert_eq!(db.schema_version().unwrap(), 4);
        assert_eq!(
            db.workspace(operation.workspace.id).unwrap(),
            Some(operation.workspace)
        );
        assert_eq!(db.pending_worktree_operations().unwrap().len(), 0);
        assert!(db.integrity_check().unwrap());
    }
}
