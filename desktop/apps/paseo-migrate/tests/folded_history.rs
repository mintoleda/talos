//! Real CLI kill/retry acceptance; all source/destination data is synthetic and
//! private to `TempDir`. No daemon, provider, UI or real legacy home is opened.
use serde_json::{Value, json};
use std::{
    fs,
    path::Path,
    process::{Command, Stdio},
    thread,
    time::{Duration, Instant},
};

fn command(base: &Path, sessions: &Path, destination: &Path) -> Command {
    let mut command = Command::new(env!("CARGO_BIN_EXE_paseo-migrate"));
    command
        .args(["--source"])
        .arg(base)
        .args(["--sessions-source"])
        .arg(sessions)
        .args(["--destination"])
        .arg(destination)
        .arg("--apply");
    command
}

fn run(base: &Path, sessions: &Path, destination: &Path) -> Value {
    let output = command(base, sessions, destination).output().unwrap();
    assert!(output.status.success(), "fixture CLI failed");
    serde_json::from_slice(&output.stdout).unwrap()
}

#[test]
#[allow(clippy::too_many_lines)]
fn killed_folded_import_recovers_pending_journal_and_preserves_source_and_images() {
    let temp = tempfile::tempdir().unwrap();
    let base = temp.path().join("base");
    let sessions = temp.path().join("sessions");
    let destination = temp.path().join("destination");
    fs::create_dir_all(base.join("projects")).unwrap();
    fs::create_dir_all(&sessions).unwrap();
    for name in ["projects.json", "workspaces.json"] {
        fs::write(base.join("projects").join(name), b"[]").unwrap();
    }
    // Initialize base state before taking the SQLite writer lock. No session
    // source exists yet, so this creates only the fixture database/base journal.
    run(&base, &sessions, &destination);
    fs::write(
        sessions.join("one.meta.json"),
        json!({
            "id":"one", "dir":temp.path().join("work"), "provider":"fixture", "model":"fixture"
        })
        .to_string(),
    )
    .unwrap();
    let pinned = json!({"role":"user","content":[{"type":"image","image":{"media_type":"image/png","data":"iVBORw0KGgo="}}]});
    let current = format!(
        "{pinned}\n{}\n",
        json!({"role":"assistant","content":[{"type":"text","text":"current"}]})
    );
    let archive = format!(
        "{}\n{}\n",
        json!({"type":"fold_archive","messages":[pinned.clone(),{"role":"assistant","content":[{"type":"text","text":"old"}]}]}),
        json!({"type":"fold_archive","messages":[pinned]})
    );
    fs::write(sessions.join("one.jsonl"), &current).unwrap();
    fs::write(sessions.join("one.jsonl.archive.jsonl"), &archive).unwrap();

    // The importer durably records its pending session and attachment before
    // its atomic batch attempts BEGIN IMMEDIATE. Holding this writer makes the
    // crash window deterministic without production failpoints or timing races.
    let blocker = rusqlite::Connection::open(destination.join("state.sqlite3")).unwrap();
    blocker.execute_batch("BEGIN IMMEDIATE").unwrap();
    let mut child = command(&base, &sessions, &destination)
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .unwrap();
    let started = Instant::now();
    let mut pending_seen = false;
    while started.elapsed() < Duration::from_secs(10) {
        pending_seen = fs::read(destination.join("import-journal.json"))
            .ok()
            .and_then(|bytes| serde_json::from_slice::<Value>(&bytes).ok())
            .is_some_and(|journal| journal["pending_session"].is_object());
        if pending_seen || child.try_wait().unwrap().is_some() {
            break;
        }
        thread::sleep(Duration::from_millis(2));
    }
    let _ = child.kill();
    child.wait().unwrap();
    blocker.execute_batch("ROLLBACK").unwrap();
    drop(blocker);
    assert!(
        pending_seen,
        "fixture did not reach the pending-session crash window"
    );
    let pending: Value =
        serde_json::from_slice(&fs::read(destination.join("import-journal.json")).unwrap())
            .unwrap();
    assert!(pending["pending_session"].is_object());
    // A hard kill intentionally leaves the exclusion lock. Remove it only after
    // wait() has proved this exact fixture importer is no longer running.
    fs::remove_file(destination.join("talosd.lock")).unwrap();
    let first = run(&base, &sessions, &destination);
    assert_eq!(first["imported_sessions"], 1);
    let second = run(&base, &sessions, &destination);
    assert_eq!(second["imported_sessions"], 0);
    assert_eq!(
        fs::read(sessions.join("one.jsonl")).unwrap(),
        current.as_bytes()
    );
    assert_eq!(
        fs::read(sessions.join("one.jsonl.archive.jsonl")).unwrap(),
        archive.as_bytes()
    );
    let db = rusqlite::Connection::open(destination.join("state.sqlite3")).unwrap();
    let count = |sql| {
        db.query_row::<i64, _, _>(sql, [], |row| row.get(0))
            .unwrap()
    };
    assert_eq!(count("SELECT COUNT(*) FROM sessions"), 1);
    assert_eq!(
        count(
            "SELECT COUNT(*) FROM sessions WHERE provider_session_id IS NOT NULL OR lifecycle != '\"Archived\"'"
        ),
        0
    );
    assert_eq!(
        count("SELECT COUNT(*) FROM agents WHERE lifecycle != '\"Archived\"'"),
        0
    );
    assert_eq!(
        count("SELECT COUNT(*) FROM timeline_events WHERE run_id IS NOT NULL"),
        0
    );
    assert_eq!(
        count("SELECT COUNT(*) FROM timeline_events WHERE kind='Attachment'"),
        1
    );
    assert_eq!(
        count("SELECT COUNT(*) FROM timeline_events WHERE kind='AssistantText'"),
        2
    );
    let blobs = fs::read_dir(destination.join("attachments"))
        .unwrap()
        .collect::<Result<Vec<_>, _>>()
        .unwrap();
    assert_eq!(blobs.len(), 1);
    assert_eq!(fs::read(blobs[0].path()).unwrap(), b"\x89PNG\r\n\x1a\n");
    let journal: Value =
        serde_json::from_slice(&fs::read(destination.join("import-journal.json")).unwrap())
            .unwrap();
    assert!(journal["pending_session"].is_null());
    assert_eq!(journal["session_entries"].as_array().unwrap().len(), 1);
}
