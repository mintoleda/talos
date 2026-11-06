//! Git mutations are preceded by SQLite intent. Recovery observes Git rather
//! than guessing whether a command finished, and never removes a present path.

use super::{AppService, CoreError, CoreResult, git_output};
use serde_json::json;
use std::path::{Path, PathBuf};
use talos_domain::{ProjectId, Workspace, WorkspaceId, WorkspaceKind};
use talos_store::{
    ManagedWorktreeOperation, WorkspaceArchiveResult, WorktreeOperationKind,
    WorktreeOperationOutcome,
};

#[cfg(test)]
mod tests;

fn invalid(message: &str) -> CoreError {
    CoreError::Other(message.into())
}

async fn git_text(directory: &Path, args: &[&str]) -> CoreResult<String> {
    let output = git_output(
        directory,
        &args.iter().map(ToString::to_string).collect::<Vec<_>>(),
    )
    .await?;
    if !output.status.success() {
        return Err(invalid(&format!(
            "git {} failed: {}",
            args.join(" "),
            String::from_utf8_lossy(&output.stderr).trim()
        )));
    }
    String::from_utf8(output.stdout).map_err(|_| invalid("Git returned a non-UTF-8 path"))
}

async fn common_dir(repo: &Path) -> CoreResult<PathBuf> {
    let path = git_text(
        repo,
        &["rev-parse", "--path-format=absolute", "--git-common-dir"],
    )
    .await?;
    Ok(tokio::fs::canonicalize(path.trim()).await?)
}

async fn validate_repo(repo: &Path) -> CoreResult<()> {
    let top = git_text(repo, &["rev-parse", "--show-toplevel"]).await?;
    if tokio::fs::canonicalize(top.trim()).await? != repo {
        return Err(invalid("project root is not the root of a Git worktree"));
    }
    Ok(())
}

/// `-z` avoids Git quoting paths containing spaces, tabs or newlines.
async fn registration(repo: &Path, path: &Path) -> CoreResult<Option<String>> {
    let listing = git_text(repo, &["worktree", "list", "--porcelain", "-z"]).await?;
    let prefix = format!("worktree {}", path.to_string_lossy());
    Ok(listing
        .split("\0\0")
        .find(|record| record.split('\0').next() == Some(prefix.as_str()))
        .map(str::to_owned))
}

/// Same-parent destination so Git's worktree move is an atomic directory rename.
/// The checkout is deliberately retained here instead of recursively removed.
fn quarantine_path(path: &Path, id: WorkspaceId) -> PathBuf {
    path.with_file_name(format!(".talos-preserved-{id}"))
}

async fn path_exists(path: &Path) -> CoreResult<bool> {
    match tokio::fs::symlink_metadata(path).await {
        Ok(_) => Ok(true),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(false),
        Err(error) => Err(error.into()),
    }
}

async fn validate_checkout_clean(path: &Path) -> CoreResult<()> {
    let status = git_text(
        path,
        &[
            "status",
            "--porcelain",
            "--untracked-files=all",
            "--ignored=matching",
        ],
    )
    .await?;
    if !status.is_empty() {
        return Err(invalid("refusing to remove a dirty managed worktree"));
    }

    // These index flags can hide user edits from both status and Git's own
    // non-forced removal check. Refuse rather than clearing user flags.
    let files = git_text(path, &["ls-files", "-v", "-z"]).await?;
    if files
        .split('\0')
        .filter_map(|entry| entry.as_bytes().first())
        .any(|tag| *tag == b'S' || tag.is_ascii_lowercase())
    {
        return Err(invalid(
            "refusing to remove a worktree with hidden index entries",
        ));
    }
    Ok(())
}

impl AppService {
    /// Retained intents include a durable explanation when ownership or Git
    /// state is ambiguous. They are retried on reopen and explicit operations.
    pub async fn pending_worktree_operations(&self) -> CoreResult<Vec<ManagedWorktreeOperation>> {
        self.inner
            .store
            .run(|store| store.pending_worktree_operations())
            .await
            .map_err(CoreError::Store)
    }

    async fn managed_destination(
        &self,
        project: ProjectId,
        id: WorkspaceId,
        create: bool,
    ) -> CoreResult<PathBuf> {
        let home = tokio::fs::canonicalize(&self.inner.paths.home).await?;
        let mut root = home.clone();
        for component in ["worktrees".to_owned(), project.to_string()] {
            root.push(component);
            if create {
                match tokio::fs::create_dir(&root).await {
                    Ok(()) => {}
                    Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => {}
                    Err(error) => return Err(error.into()),
                }
            }
            let metadata = tokio::fs::symlink_metadata(&root).await?;
            if metadata.file_type().is_symlink()
                || !metadata.is_dir()
                || tokio::fs::canonicalize(&root).await? != root
            {
                return Err(invalid(
                    "managed worktree root escapes daemon home or contains a symlink",
                ));
            }
        }
        let path = root.join(id.to_string());
        if !path.starts_with(home) {
            return Err(invalid("managed worktree path escapes daemon home"));
        }
        Ok(path)
    }

    async fn validate_operation(
        &self,
        operation: &ManagedWorktreeOperation,
    ) -> CoreResult<(PathBuf, PathBuf)> {
        let workspace = &operation.workspace;
        let project_id = workspace
            .project_id
            .ok_or_else(|| invalid("worktree has no project owner"))?;
        let project = self.project(project_id).await?;
        let repo = tokio::fs::canonicalize(project.root_path).await?;
        let managed_path = self
            .managed_destination(project_id, workspace.id, false)
            .await?;
        let marker = &workspace.metadata["managedWorktree"];
        let path = managed_path;
        if workspace.kind != WorkspaceKind::Worktree
            || marker["owner"].as_str() != Some("talosd")
            || marker["projectId"].as_str() != Some(project_id.to_string().as_str())
            || marker["repoRoot"].as_str() != Some(repo.to_string_lossy().as_ref())
            || marker["path"].as_str() != Some(path.to_string_lossy().as_ref())
            || Path::new(&workspace.path) != path
            || Path::new(&operation.repo_root) != repo
        {
            return Err(invalid(
                "managed worktree ownership metadata does not match current paths",
            ));
        }
        validate_repo(&repo).await?;
        if common_dir(&repo).await? != Path::new(&operation.repo_common_dir) {
            return Err(invalid("managed worktree repository identity changed"));
        }
        Ok((repo, path))
    }

    async fn validate_checkout(
        operation: &ManagedWorktreeOperation,
        repo: &Path,
        path: &Path,
        record: &str,
    ) -> CoreResult<()> {
        let metadata = tokio::fs::symlink_metadata(path).await?;
        if metadata.file_type().is_symlink()
            || !metadata.is_dir()
            || tokio::fs::canonicalize(path).await? != path
        {
            return Err(invalid(
                "managed worktree path contains a symlink or is not a directory",
            ));
        }
        let branch = operation.workspace.metadata["branch"]
            .as_str()
            .ok_or_else(|| invalid("managed worktree branch is missing"))?;
        let expected = format!("branch refs/heads/{branch}");
        if !record.split('\0').any(|field| field == expected) {
            return Err(invalid(
                "managed worktree branch no longer matches its owner",
            ));
        }
        validate_repo(path).await?;
        if common_dir(path).await? != common_dir(repo).await? {
            return Err(invalid("checkout belongs to a different Git repository"));
        }
        Ok(())
    }

    /// Restore must prove that the recorded checkout is still the canonical,
    /// registered worktree owned by this project. A directory check alone
    /// accepts symlinks and replacement directories.
    pub(super) async fn validate_managed_worktree_restore(
        &self,
        workspace: &Workspace,
    ) -> CoreResult<()> {
        let repo_root = workspace.metadata["managedWorktree"]["repoRoot"]
            .as_str()
            .ok_or_else(|| invalid("managed worktree repository metadata is missing"))?;
        let repo_common_dir = common_dir(Path::new(repo_root)).await?;
        let operation = ManagedWorktreeOperation {
            kind: WorktreeOperationKind::Delete,
            workspace: workspace.clone(),
            repo_root: repo_root.to_owned(),
            repo_common_dir: repo_common_dir.to_string_lossy().into_owned(),
            last_error: None,
        };
        let (repo, path) = self.validate_operation(&operation).await?;
        let record = registration(&repo, &path)
            .await?
            .ok_or_else(|| invalid("managed worktree has no Git registration"))?;
        Self::validate_checkout(&operation, &repo, &path, &record).await
    }

    async fn finish_worktree(
        &self,
        id: WorkspaceId,
        outcome: WorktreeOperationOutcome,
    ) -> CoreResult<Option<Workspace>> {
        self.inner
            .store
            .run(move |store| store.finish_worktree_operation(id, outcome))
            .await
            .map_err(CoreError::Store)
    }

    /// Call with the worktree gate held, or during startup before serving RPC.
    pub(super) async fn recover_managed_worktrees(&self) -> CoreResult<()> {
        let _start_guard = self.inner.run_start_gate.lock().await;
        for operation in self.pending_worktree_operations().await? {
            if let Err(error) = self.recover_worktree(&operation).await {
                let id = operation.workspace.id;
                let message = error.to_string();
                tracing::warn!(workspace = %id, %message, "managed worktree intent retained; files preserved");
                self.inner
                    .store
                    .run(move |store| store.record_worktree_operation_error(id, &message))
                    .await
                    .map_err(CoreError::Store)?;
            }
        }
        Ok(())
    }

    async fn recover_worktree(&self, operation: &ManagedWorktreeOperation) -> CoreResult<()> {
        let (repo, path) = self.validate_operation(operation).await?;
        let record = registration(&repo, &path).await?;
        let exists = path_exists(&path).await?;

        if operation.kind == WorktreeOperationKind::Delete {
            let quarantine = quarantine_path(&path, operation.workspace.id);
            let quarantine_exists = path_exists(&quarantine).await?;
            let quarantine_record = registration(&repo, &quarantine).await?;
            match (exists, record, quarantine_exists, quarantine_record) {
                (true, Some(record), false, None) => {
                    Self::validate_checkout(operation, &repo, &path, &record).await?;
                    self.finish_worktree(
                        operation.workspace.id,
                        WorktreeOperationOutcome::DeleteCancelled,
                    )
                    .await?;
                    return Ok(());
                }
                (false, None, true, Some(record)) => {
                    Self::validate_checkout(operation, &repo, &quarantine, &record).await?;
                    return Err(invalid(&format!(
                        "managed worktree is preserved at {}; explicit cleanup is required",
                        quarantine.display()
                    )));
                }
                (false, _, false, _) => {
                    return Err(invalid(
                        "managed worktree checkout is missing; Git metadata is retained for explicit resolution",
                    ));
                }
                _ => {
                    return Err(invalid(
                        "managed worktree paths or Git registrations are ambiguous; preserving all files",
                    ));
                }
            }
        }

        let outcome = if exists {
            let record = record.ok_or_else(|| {
                invalid("path exists without its Git registration; preserving files")
            })?;
            Self::validate_checkout(operation, &repo, &path, &record).await?;
            WorktreeOperationOutcome::Created
        } else {
            if record.is_some() {
                return Err(invalid("create left a Git registration without a checkout"));
            }
            WorktreeOperationOutcome::CreateAbsent
        };
        self.finish_worktree(operation.workspace.id, outcome)
            .await?;
        Ok(())
    }

    #[allow(clippy::too_many_lines)] // Keep preflight, durable intent, Git, and atomic completion ordered.
    pub async fn create_managed_worktree(
        &self,
        project_id: ProjectId,
        name: impl Into<String>,
        branch: String,
        base_ref: String,
    ) -> CoreResult<Workspace> {
        self.ensure_running()?;
        // Domain validation precedes every side effect, including root creation.
        let mut workspace = Workspace::new(name, "pending", WorkspaceKind::Worktree)
            .map_err(|error| CoreError::InvalidWorkspaceName(error.to_string()))?;
        let _operation = self.inner.worktree_gate.lock().await;
        self.ensure_running()?;
        self.recover_managed_worktrees().await?;
        let project = self.project(project_id).await?;
        let repo = tokio::fs::canonicalize(project.root_path).await?;
        validate_repo(&repo).await?;
        git_text(&repo, &["check-ref-format", "--branch", &branch]).await?;
        let commit = git_text(
            &repo,
            &[
                "rev-parse",
                "--verify",
                "--end-of-options",
                &format!("{base_ref}^{{commit}}"),
            ],
        )
        .await?;
        let branch_ref = format!("refs/heads/{branch}");
        let existing = git_output(
            &repo,
            &[
                "show-ref".into(),
                "--verify".into(),
                "--quiet".into(),
                branch_ref,
            ],
        )
        .await?;
        if existing.status.code() != Some(1) {
            return Err(invalid("branch already exists or cannot be checked"));
        }
        let path = self
            .managed_destination(project_id, workspace.id, true)
            .await?;
        if tokio::fs::symlink_metadata(&path).await.is_ok()
            || registration(&repo, &path).await?.is_some()
        {
            return Err(invalid("managed worktree destination already exists"));
        }
        workspace.path = path.to_string_lossy().into_owned();
        workspace.project_id = Some(project_id);
        workspace.metadata = json!({
            "mainRepoRoot": repo.to_string_lossy(), "branch": branch, "baseRef": base_ref,
            "managedWorktree": {"owner": "talosd", "projectId": project_id, "repoRoot": repo.to_string_lossy(), "path": workspace.path},
        });
        let operation = ManagedWorktreeOperation {
            kind: WorktreeOperationKind::Create,
            workspace: workspace.clone(),
            repo_root: repo.to_string_lossy().into_owned(),
            repo_common_dir: common_dir(&repo).await?.to_string_lossy().into_owned(),
            last_error: None,
        };
        let saved = operation.clone();
        self.inner
            .store
            .run(move |store| store.begin_worktree_create(&saved))
            .await
            .map_err(CoreError::Store)?;
        #[cfg(test)]
        self.worktree_fault("create_intent")?;
        // Errors, timeouts and cancellation all leave intent for observation on reopen.
        git_text(
            &repo,
            &[
                "worktree",
                "add",
                "-b",
                &branch,
                &workspace.path,
                commit.trim(),
            ],
        )
        .await?;
        #[cfg(test)]
        self.worktree_fault("create_git")?;
        let record = registration(&repo, &path)
            .await?
            .ok_or_else(|| invalid("new worktree is not registered"))?;
        Self::validate_checkout(&operation, &repo, &path, &record).await?;
        self.finish_worktree(workspace.id, WorktreeOperationOutcome::Created)
            .await?;
        #[cfg(test)]
        self.worktree_fault("create_commit")?;
        Ok(workspace)
    }

    pub async fn delete_managed_worktree(&self, id: WorkspaceId) -> CoreResult<Workspace> {
        self.ensure_running()?;
        let _operation = self.inner.worktree_gate.lock().await;
        self.ensure_running()?;
        self.recover_managed_worktrees().await?;
        let _start_guard = self.inner.run_start_gate.lock().await;
        self.ensure_running()?;
        let workspace = self.workspace(id).await?;
        let project = self
            .project(
                workspace
                    .project_id
                    .ok_or_else(|| invalid("worktree has no project owner"))?,
            )
            .await?;
        let repo = tokio::fs::canonicalize(project.root_path).await?;
        let operation = ManagedWorktreeOperation {
            kind: WorktreeOperationKind::Delete,
            workspace,
            repo_root: repo.to_string_lossy().into_owned(),
            repo_common_dir: common_dir(&repo).await?.to_string_lossy().into_owned(),
            last_error: None,
        };
        let (repo, path) = self.validate_operation(&operation).await?;
        let record = registration(&repo, &path)
            .await?
            .ok_or_else(|| invalid("Git does not identify this path as an owned worktree"))?;
        Self::validate_checkout(&operation, &repo, &path, &record).await?;
        validate_checkout_clean(&path).await?;
        let saved = operation.clone();
        let archived = match self
            .inner
            .store
            .run(move |store| store.begin_worktree_delete(&saved))
            .await
            .map_err(CoreError::Store)?
        {
            WorkspaceArchiveResult::Archived(workspace) => workspace,
            WorkspaceArchiveResult::ActiveRuns => {
                return Err(CoreError::WorkspaceHasActiveRuns(id));
            }
            WorkspaceArchiveResult::NotFound => return Err(CoreError::WorkspaceNotFound(id)),
        };
        #[cfg(test)]
        self.worktree_fault("delete_intent")?;
        #[cfg(test)]
        self.worktree_inject_file("delete_race_ignored", &path.join("delete-race-data"))?;
        // git worktree remove deletes ignored files, so repeat the full check
        // after the durable intent and immediately before asking Git to remove
        // the directory. If the recheck fails, reconcile the intent while the
        // registered checkout is still present, preserving its previous state.
        if let Err(error) = validate_checkout_clean(&path).await {
            let _ = self.recover_worktree(&operation).await;
            return Err(error);
        }
        if let Err(error) =
            git_text(&repo, &["worktree", "remove", &operation.workspace.path]).await
        {
            // Git may have changed state even when it failed. Observe before
            // rolling back lifecycle; preserve ambiguous intent on any error.
            let _ = self.recover_worktree(&operation).await;
            return Err(error);
        }
        #[cfg(test)]
        self.worktree_fault("delete_git")?;
        self.finish_worktree(id, WorktreeOperationOutcome::Deleted)
            .await?;
        #[cfg(test)]
        self.worktree_fault("delete_commit")?;
        Ok(archived)
    }

    #[cfg(test)]
    fn worktree_fault(&self, expected: &'static str) -> CoreResult<()> {
        if self
            .inner
            .worktree_fault
            .lock()
            .unwrap()
            .take_if(|point| *point == expected)
            .is_some()
        {
            return Err(invalid("injected managed worktree interruption"));
        }
        Ok(())
    }

    #[cfg(test)]
    fn worktree_inject_file(&self, expected: &'static str, path: &Path) -> CoreResult<()> {
        let injected = self
            .inner
            .worktree_fault
            .lock()
            .unwrap()
            .take_if(|point| *point == expected)
            .is_some();
        if injected {
            std::fs::write(path, "preserve this race-created file")?;
        }
        Ok(())
    }
}
