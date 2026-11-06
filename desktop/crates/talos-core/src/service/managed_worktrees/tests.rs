use super::*;
use std::process::Command;
use talos_domain::WorkspaceLifecycle;
use uuid::Uuid;

struct Fixture {
    root: PathBuf,
    repo: PathBuf,
}

impl Fixture {
    fn new() -> Self {
        let root = std::env::temp_dir().join(format!("talos-worktree-recovery-{}", Uuid::now_v7()));
        let repo = root.join("repo with spaces");
        std::fs::create_dir_all(&repo).unwrap();
        let fixture = Self { root, repo };
        fixture.git(&["init", "--quiet", "-b", "main"]);
        fixture.git(&["config", "user.name", "Talos Fixture"]);
        fixture.git(&["config", "user.email", "fixture@example.invalid"]);
        std::fs::write(fixture.repo.join("tracked"), "original\n").unwrap();
        fixture.git(&["add", "tracked"]);
        fixture.git(&["commit", "--quiet", "-m", "fixture"]);
        fixture
    }

    fn git(&self, args: &[&str]) {
        let output = Command::new("git")
            .current_dir(&self.repo)
            .args(args)
            .output()
            .unwrap();
        assert!(
            output.status.success(),
            "{args:?}: {}",
            String::from_utf8_lossy(&output.stderr)
        );
    }

    async fn open(&self) -> AppService {
        AppService::open_at(self.root.join("home")).await.unwrap()
    }

    async fn create(&self, service: &AppService) -> Workspace {
        let project = service.create_project("Fixture", &self.repo).await.unwrap();
        service
            .create_managed_worktree(
                project.id,
                "Feature",
                "feature/fixture".into(),
                "HEAD".into(),
            )
            .await
            .unwrap()
    }
}

impl Drop for Fixture {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.root);
    }
}

fn fault(service: &AppService, point: &'static str) {
    *service.inner.worktree_fault.lock().unwrap() = Some(point);
}

#[tokio::test]
async fn invalid_names_are_rejected_before_roots_intents_or_git_changes() {
    let fixture = Fixture::new();
    let service = fixture.open().await;
    let project = service
        .create_project("Fixture", &fixture.repo)
        .await
        .unwrap();
    for name in [String::new(), "   ".into(), "x".repeat(257)] {
        assert!(matches!(
            service
                .create_managed_worktree(
                    project.id,
                    name,
                    "feature/preflight".into(),
                    "HEAD".into()
                )
                .await,
            Err(CoreError::InvalidWorkspaceName(_))
        ));
    }
    assert_eq!(
        service.pending_worktree_operations().await.unwrap().len(),
        0
    );
    assert_eq!(service.list_workspaces().await.unwrap().len(), 0);
    assert!(!service.home().join("worktrees").exists());
    let output = Command::new("git")
        .current_dir(&fixture.repo)
        .args([
            "show-ref",
            "--verify",
            "--quiet",
            "refs/heads/feature/preflight",
        ])
        .output()
        .unwrap();
    assert_eq!(output.status.code(), Some(1));
    service.shutdown().await;
}

#[tokio::test]
async fn create_boundaries_reconcile_idempotently_and_preserve_dirty_files() {
    for point in ["create_intent", "create_git", "create_commit"] {
        let fixture = Fixture::new();
        let service = fixture.open().await;
        let project = service
            .create_project("Fixture", &fixture.repo)
            .await
            .unwrap();
        fault(&service, point);
        assert!(
            service
                .create_managed_worktree(
                    project.id,
                    "Feature",
                    "feature/restart".into(),
                    "HEAD".into()
                )
                .await
                .is_err()
        );
        let pending = service.pending_worktree_operations().await.unwrap();
        let workspace = if point == "create_commit" {
            assert_eq!(pending.len(), 0);
            service.list_workspaces().await.unwrap().remove(0)
        } else {
            assert_eq!(pending.len(), 1);
            assert_eq!(service.list_workspaces().await.unwrap().len(), 0);
            pending[0].workspace.clone()
        };
        if point != "create_intent" {
            std::fs::write(
                Path::new(&workspace.path).join("untracked"),
                "keep user data",
            )
            .unwrap();
            std::fs::write(Path::new(&workspace.path).join("tracked"), "user edits").unwrap();
        }
        service.shutdown().await;
        for _ in 0..2 {
            let reopened = fixture.open().await;
            assert_eq!(
                reopened.pending_worktree_operations().await.unwrap().len(),
                0
            );
            if point == "create_intent" {
                assert_eq!(reopened.list_workspaces().await.unwrap().len(), 0);
                assert!(!Path::new(&workspace.path).exists());
            } else {
                assert_eq!(reopened.workspace(workspace.id).await.unwrap(), workspace);
                assert_eq!(
                    std::fs::read_to_string(Path::new(&workspace.path).join("untracked")).unwrap(),
                    "keep user data"
                );
                assert_eq!(
                    std::fs::read_to_string(Path::new(&workspace.path).join("tracked")).unwrap(),
                    "user edits"
                );
            }
            reopened.shutdown().await;
        }
    }
}

#[tokio::test]
async fn delete_boundaries_reconcile_without_retrying_removal_of_present_files() {
    for initially_archived in [false, true] {
        for point in ["delete_intent", "delete_git", "delete_commit"] {
            let fixture = Fixture::new();
            let service = fixture.open().await;
            let mut workspace = fixture.create(&service).await;
            if initially_archived {
                workspace = service.archive_workspace(workspace.id).await.unwrap();
            }
            fault(&service, point);
            assert!(service.delete_managed_worktree(workspace.id).await.is_err());
            assert_eq!(
                service.workspace(workspace.id).await.unwrap().lifecycle,
                WorkspaceLifecycle::Archived
            );
            if point == "delete_intent" {
                assert!(service.restore_workspace(workspace.id).await.is_err());
                std::fs::write(
                    Path::new(&workspace.path).join("ignored-user-data"),
                    "preserve",
                )
                .unwrap();
            }
            service.shutdown().await;
            for _ in 0..2 {
                let reopened = fixture.open().await;
                assert_eq!(
                    reopened.pending_worktree_operations().await.unwrap().len(),
                    0
                );
                let actual = reopened.workspace(workspace.id).await.unwrap();
                if point == "delete_intent" {
                    assert_eq!(actual.lifecycle, workspace.lifecycle);
                    assert_eq!(
                        std::fs::read_to_string(Path::new(&actual.path).join("ignored-user-data"))
                            .unwrap(),
                        "preserve"
                    );
                    assert!(reopened.delete_managed_worktree(actual.id).await.is_err());
                } else {
                    assert_eq!(actual.lifecycle, WorkspaceLifecycle::Archived);
                    assert!(!Path::new(&actual.path).exists());
                    assert!(reopened.restore_workspace(actual.id).await.is_err());
                }
                reopened.shutdown().await;
            }
        }
    }
}

#[tokio::test]
async fn incomplete_create_is_retained_with_durable_diagnostics_and_project_ownership() {
    let fixture = Fixture::new();
    let service = fixture.open().await;
    let project = service
        .create_project("Fixture", &fixture.repo)
        .await
        .unwrap();
    fault(&service, "create_intent");
    assert!(
        service
            .create_managed_worktree(
                project.id,
                "Feature",
                "feature/partial".into(),
                "HEAD".into()
            )
            .await
            .is_err()
    );
    let operation = service
        .pending_worktree_operations()
        .await
        .unwrap()
        .remove(0);
    fixture.git(&["branch", "feature/partial"]);
    std::fs::create_dir(&operation.workspace.path).unwrap();
    let data = Path::new(&operation.workspace.path).join("partial-user-file");
    std::fs::write(&data, "preserve incomplete checkout").unwrap();
    assert!(matches!(
        service.delete_project(project.id).await,
        Err(CoreError::ProjectHasWorkspaces(_))
    ));
    service.shutdown().await;
    for _ in 0..2 {
        let reopened = fixture.open().await;
        let pending = reopened.pending_worktree_operations().await.unwrap();
        assert_eq!(pending.len(), 1);
        assert!(
            pending[0]
                .last_error
                .as_ref()
                .unwrap()
                .contains("without its Git registration")
        );
        assert_eq!(reopened.list_workspaces().await.unwrap().len(), 0);
        assert_eq!(
            std::fs::read_to_string(&data).unwrap(),
            "preserve incomplete checkout"
        );
        fixture.git(&["show-ref", "--verify", "refs/heads/feature/partial"]);
        reopened.shutdown().await;
    }
    // Once the ambiguity is resolved externally, retry converges without
    // removing the retained branch or manufacturing a replacement checkout.
    std::fs::remove_file(data).unwrap();
    std::fs::remove_dir(&operation.workspace.path).unwrap();
    let reopened = fixture.open().await;
    assert_eq!(
        reopened.pending_worktree_operations().await.unwrap().len(),
        0
    );
    fixture.git(&["show-ref", "--verify", "refs/heads/feature/partial"]);
    reopened.shutdown().await;
}

#[tokio::test]
async fn git_refusal_cancels_delete_and_preserves_the_original_archive_state() {
    for initially_archived in [false, true] {
        let fixture = Fixture::new();
        let service = fixture.open().await;
        let mut workspace = fixture.create(&service).await;
        if initially_archived {
            workspace = service.archive_workspace(workspace.id).await.unwrap();
        }
        fixture.git(&["worktree", "lock", &workspace.path]);
        assert!(service.delete_managed_worktree(workspace.id).await.is_err());
        assert_eq!(
            service.workspace(workspace.id).await.unwrap().lifecycle,
            workspace.lifecycle
        );
        assert!(Path::new(&workspace.path).join("tracked").is_file());
        assert_eq!(
            service.pending_worktree_operations().await.unwrap().len(),
            0
        );
        service.shutdown().await;
    }
}

#[tokio::test]
async fn ignored_file_created_after_preflight_is_preserved_by_immediate_recheck() {
    let fixture = Fixture::new();
    let service = fixture.open().await;
    let workspace = fixture.create(&service).await;
    let path = Path::new(&workspace.path);
    std::fs::write(path.join(".gitignore"), "delete-race-data\n").unwrap();
    for args in [
        &["add", ".gitignore"][..],
        &["commit", "-qm", "ignore race file"][..],
    ] {
        let output = Command::new("git")
            .current_dir(path)
            .args(args)
            .output()
            .unwrap();
        assert!(
            output.status.success(),
            "git {args:?}: {}",
            String::from_utf8_lossy(&output.stderr)
        );
    }
    fault(&service, "delete_race_ignored");

    assert!(service.delete_managed_worktree(workspace.id).await.is_err());
    assert_eq!(
        std::fs::read_to_string(path.join("delete-race-data")).unwrap(),
        "preserve this race-created file"
    );
    assert_eq!(
        service.workspace(workspace.id).await.unwrap().lifecycle,
        WorkspaceLifecycle::Active
    );
    assert_eq!(
        service.pending_worktree_operations().await.unwrap().len(),
        0
    );
    assert!(registration(&fixture.repo, path).await.unwrap().is_some());
    service.shutdown().await;
}

#[tokio::test]
async fn missing_checkout_registration_is_removed_without_force_on_restart() {
    let fixture = Fixture::new();
    let service = fixture.open().await;
    let workspace = fixture.create(&service).await;
    fault(&service, "delete_intent");
    assert!(service.delete_managed_worktree(workspace.id).await.is_err());
    std::fs::remove_dir_all(&workspace.path).unwrap();
    service.shutdown().await;
    let reopened = fixture.open().await;
    assert_eq!(
        reopened.pending_worktree_operations().await.unwrap().len(),
        0
    );
    assert!(
        registration(&fixture.repo, Path::new(&workspace.path))
            .await
            .unwrap()
            .is_none()
    );
    assert_eq!(
        reopened.workspace(workspace.id).await.unwrap().lifecycle,
        WorkspaceLifecycle::Archived
    );
    reopened.shutdown().await;
}

#[cfg(unix)]
#[tokio::test]
async fn recovery_refuses_symlink_replacements_and_leaves_delete_quarantined() {
    use std::os::unix::fs::symlink;
    let fixture = Fixture::new();
    let service = fixture.open().await;
    let workspace = fixture.create(&service).await;
    fault(&service, "delete_intent");
    assert!(service.delete_managed_worktree(workspace.id).await.is_err());
    let moved = fixture.root.join("user-checkout");
    std::fs::rename(&workspace.path, &moved).unwrap();
    symlink(&moved, &workspace.path).unwrap();
    service.shutdown().await;
    for _ in 0..2 {
        let reopened = fixture.open().await;
        let pending = reopened.pending_worktree_operations().await.unwrap();
        assert_eq!(pending.len(), 1);
        assert!(pending[0].last_error.as_ref().unwrap().contains("symlink"));
        assert_eq!(
            std::fs::read_to_string(moved.join("tracked")).unwrap(),
            "original\n"
        );
        assert!(reopened.restore_workspace(workspace.id).await.is_err());
        assert_eq!(
            reopened.workspace(workspace.id).await.unwrap().lifecycle,
            WorkspaceLifecycle::Archived
        );
        reopened.shutdown().await;
    }
}

#[tokio::test]
async fn restore_rejects_a_replacement_directory_without_git_ownership() {
    let fixture = Fixture::new();
    let service = fixture.open().await;
    let workspace = fixture.create(&service).await;
    service.archive_workspace(workspace.id).await.unwrap();
    let original = Path::new(&workspace.path);
    let moved = fixture.root.join("moved-checkout");
    std::fs::rename(original, &moved).unwrap();
    std::fs::create_dir(original).unwrap();
    let replacement_data = original.join("replacement-user-data");
    std::fs::write(&replacement_data, "preserve replacement").unwrap();

    assert!(service.restore_workspace(workspace.id).await.is_err());
    assert_eq!(
        service.workspace(workspace.id).await.unwrap().lifecycle,
        WorkspaceLifecycle::Archived
    );
    assert_eq!(
        std::fs::read_to_string(replacement_data).unwrap(),
        "preserve replacement"
    );
    assert_eq!(
        std::fs::read_to_string(moved.join("tracked")).unwrap(),
        "original\n"
    );
    service.shutdown().await;
}

#[tokio::test]
async fn restore_rejects_tampered_managed_worktree_metadata() {
    let fixture = Fixture::new();
    let service = fixture.open().await;
    let workspace = fixture.create(&service).await;
    service.archive_workspace(workspace.id).await.unwrap();
    let mut tampered = service.workspace(workspace.id).await.unwrap();
    tampered.metadata["managedWorktree"]["path"] = serde_json::json!(
        fixture
            .root
            .join("replacement")
            .to_string_lossy()
            .to_string()
    );
    let saved = tampered.clone();
    service
        .inner
        .store
        .run(move |store| store.save_workspace(&saved))
        .await
        .unwrap();

    assert!(service.restore_workspace(workspace.id).await.is_err());
    assert_eq!(
        service.workspace(workspace.id).await.unwrap().lifecycle,
        WorkspaceLifecycle::Archived
    );
    assert!(Path::new(&workspace.path).join("tracked").is_file());
    service.shutdown().await;
}

#[tokio::test]
async fn hidden_index_edits_and_ignored_files_are_never_removed() {
    for flag in ["--assume-unchanged", "--skip-worktree", "ignored"] {
        let fixture = Fixture::new();
        let service = fixture.open().await;
        let workspace = fixture.create(&service).await;
        if flag == "ignored" {
            std::fs::write(Path::new(&workspace.path).join(".gitignore"), "user-data\n").unwrap();
            let output = Command::new("git")
                .current_dir(&workspace.path)
                .args(["add", ".gitignore"])
                .output()
                .unwrap();
            assert!(output.status.success());
            let output = Command::new("git")
                .current_dir(&workspace.path)
                .args(["commit", "--quiet", "-m", "ignore fixture"])
                .output()
                .unwrap();
            assert!(output.status.success());
            std::fs::write(Path::new(&workspace.path).join("user-data"), "ignored data").unwrap();
        } else {
            let output = Command::new("git")
                .current_dir(&workspace.path)
                .args(["update-index", flag, "tracked"])
                .output()
                .unwrap();
            assert!(output.status.success());
            std::fs::write(
                Path::new(&workspace.path).join("tracked"),
                "hidden user edits",
            )
            .unwrap();
        }
        assert!(service.delete_managed_worktree(workspace.id).await.is_err());
        assert_eq!(
            service.workspace(workspace.id).await.unwrap().lifecycle,
            WorkspaceLifecycle::Active
        );
        assert_eq!(
            service.pending_worktree_operations().await.unwrap().len(),
            0
        );
        assert!(Path::new(&workspace.path).join("tracked").is_file());
        service.shutdown().await;
    }
}
