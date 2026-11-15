//! Read-only workspace decoration on the bridge worker, never the paint thread.
use std::{collections::BTreeMap, process::Stdio, time::Duration};

use serde_json::Value;

pub(crate) async fn enrich_snapshot(snapshot: &mut Value) {
    let Some(workspaces) = snapshot["workspaces"].as_array_mut() else {
        return;
    };
    let mut paths = BTreeMap::<String, Vec<usize>>::new();
    for (index, workspace) in workspaces.iter_mut().enumerate() {
        workspace["git_branch"] = Value::Null;
        if workspace["lifecycle"] != "Archived"
            && let Some(path) = workspace["path"].as_str().filter(|path| !path.is_empty())
        {
            paths.entry(path.to_owned()).or_default().push(index);
        }
    }
    // Deduplicate paths, cap subprocess concurrency, and bound the entire pass.
    // Dropping JoinSet aborts outstanding tasks; kill_on_drop reaps their children.
    let _ = tokio::time::timeout(Duration::from_secs(2), async {
        let mut paths = paths.into_iter();
        let mut tasks = tokio::task::JoinSet::new();
        loop {
            while tasks.len() < 4 {
                let Some((path, indices)) = paths.next() else {
                    break;
                };
                tasks.spawn(async move { (indices, current_branch(&path).await) });
            }
            let Some(result) = tasks.join_next().await else {
                break;
            };
            if let Ok((indices, Some(branch))) = result {
                for index in indices {
                    workspaces[index]["git_branch"] = Value::String(branch.clone());
                }
            }
        }
    })
    .await;
}

async fn current_branch(path: &str) -> Option<String> {
    let output = tokio::process::Command::new("git")
        .args(["-C", path, "symbolic-ref", "--quiet", "--short", "HEAD"])
        .env_remove("GIT_DIR")
        .env_remove("GIT_WORK_TREE")
        .env_remove("GIT_COMMON_DIR")
        .env("GIT_OPTIONAL_LOCKS", "0")
        .stdin(Stdio::null())
        .stderr(Stdio::null())
        .kill_on_drop(true)
        .output()
        .await
        .ok()?;
    if !output.status.success() {
        return None;
    }
    let branch = String::from_utf8(output.stdout).ok()?.trim().to_owned();
    (!branch.is_empty()).then_some(branch)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn branches_follow_checkout_and_hide_detached_or_missing_repositories() {
        let root = std::env::temp_dir().join(format!(
            "talos-sidebar-git-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir_all(&root).unwrap();
        let git = |args: &[&str]| {
            assert!(
                std::process::Command::new("git")
                    .arg("-C")
                    .arg(&root)
                    .args([
                        "-c",
                        "core.hooksPath=/dev/null",
                        "-c",
                        "commit.gpgSign=false"
                    ])
                    .args(args)
                    .stdout(Stdio::null())
                    .stderr(Stdio::null())
                    .status()
                    .unwrap()
                    .success()
            );
        };
        git(&["init", "-b", "feature/compact"]);
        git(&[
            "-c",
            "user.name=Fixture",
            "-c",
            "user.email=fixture@example.invalid",
            "commit",
            "--allow-empty",
            "-m",
            "fixture",
        ]);
        let linked = root.join("linked");
        git(&[
            "worktree",
            "add",
            "-b",
            "feature/linked",
            linked.to_str().unwrap(),
            "HEAD",
        ]);
        let mut snapshot = serde_json::json!({"workspaces": [
            {"path": root, "lifecycle": "Active"},
            {"path": root, "lifecycle": "Active"},
            {"path": root.join("missing"), "metadata": {"branch": "stale"}},
            {"path": root, "lifecycle": "Archived"},
            {"path": linked, "lifecycle": "Active"}
        ]});
        enrich_snapshot(&mut snapshot).await;
        assert_eq!(snapshot["workspaces"][0]["git_branch"], "feature/compact");
        assert_eq!(snapshot["workspaces"][1]["git_branch"], "feature/compact");
        assert!(snapshot["workspaces"][2]["git_branch"].is_null());
        assert!(snapshot["workspaces"][3]["git_branch"].is_null());
        assert_eq!(snapshot["workspaces"][4]["git_branch"], "feature/linked");
        git(&["checkout", "--detach"]);
        enrich_snapshot(&mut snapshot).await;
        assert!(snapshot["workspaces"][0]["git_branch"].is_null());
        git(&["checkout", "-b", "next"]);
        enrich_snapshot(&mut snapshot).await;
        assert_eq!(snapshot["workspaces"][0]["git_branch"], "next");
        std::fs::remove_dir_all(root).unwrap();
    }
}
