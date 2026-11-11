//! Opt-in real-daemon acceptance for the bridge used by the GPUI frontend.
//! scripts/gpui-smoke.py supplies an isolated fixture home; no paid provider calls.
use std::{
    sync::mpsc,
    time::{Duration, Instant},
};
use talos_ui_core::{
    backend::{Action, Backend},
    state::EntryKind,
};

fn wait(backend: &mut Backend, wakes: &mpsc::Receiver<()>, predicate: impl Fn(&Backend) -> bool) {
    let deadline = Instant::now() + Duration::from_secs(30);
    loop {
        for _ in 0..8 {
            backend.drain();
        }
        assert!(
            backend.view.error.is_none(),
            "backend error: {:?}",
            backend.view.error
        );
        if predicate(backend) {
            return;
        }
        assert!(
            Instant::now() < deadline,
            "timed out; status: {}; connection: {:?}; notice: {:?}",
            backend.view.status,
            backend.connection_error,
            backend.view.notice
        );
        let _ = wakes.recv_timeout(Duration::from_millis(100));
    }
}
fn assistant_contains(backend: &Backend, session: &str, needle: &str) -> bool {
    backend.view.entries.get(session).into_iter().flatten().any(
        |entry| matches!(&entry.kind, EntryKind::Assistant { text, .. } if text.contains(needle)),
    )
}

#[test]
#[ignore = "run through scripts/gpui-smoke.py with an isolated fixture daemon"]
fn daemon_workflow() {
    let root = std::env::var("TALOS_GPUI_TEST_ROOT").expect("isolated test root is required");
    let home = std::env::var("TALOSD_HOME").expect("isolated daemon home is required");
    assert!(std::path::Path::new(&home).starts_with(&root));
    let (tx, wakes) = mpsc::channel();
    let mut backend = Backend::new(
        move || {
            let _ = tx.send(());
        },
        false,
    );
    wait(&mut backend, &wakes, |b| b.view.online);
    assert!(backend.submit(Action::CreateWorkspace {
        name: "GPUI acceptance".into(),
        path: format!("{root}/work")
    }));
    wait(&mut backend, &wakes, |b| b.created_workspace.is_some());
    let workspace = backend.created_workspace.take().unwrap();
    assert!(backend.submit(Action::Refresh));
    wait(&mut backend, &wakes, |b| {
        b.view.workspaces.iter().any(|w| w.id == workspace)
    });
    assert!(backend.submit(Action::CreateAgent {
        workspace,
        provider: Some("talos".into()),
        title: Some("GPUI acceptance".into())
    }));
    wait(&mut backend, &wakes, |b| b.created_session.is_some());
    let session = backend.created_session.take().unwrap();
    assert!(backend.submit(Action::SetModel {
        session: session.clone(),
        model: "vendor/model".into()
    }));
    wait(&mut backend, &wakes, |b| {
        b.view
            .session(&session)
            .is_some_and(|s| s.model.as_deref() == Some("vendor/model"))
    });
    assert!(backend.submit(Action::Rpc(talos_ipc::Command::SetMode {
        session_id: session.clone(),
        mode_id: "ask".into()
    })));
    wait(&mut backend, &wakes, |b| {
        b.view
            .session(&session)
            .is_some_and(|s| s.mode.as_deref() == Some("ask"))
    });
    assert!(backend.send(&session, "hello".into(), vec![]));
    wait(&mut backend, &wakes, |b| {
        assistant_contains(b, &session, "hello") && !b.view.running(&session)
    });
    assert!(backend.send(&session, "permission".into(), vec![]));
    wait(&mut backend, &wakes, |b| {
        b.view
            .entries
            .get(&session)
            .into_iter()
            .flatten()
            .any(|e| matches!(&e.kind, EntryKind::Permission { .. }))
    });
    let permission = backend.view.entries[&session]
        .iter()
        .find_map(|e| match &e.kind {
            EntryKind::Permission { id, .. } => Some(id.clone()),
            _ => None,
        })
        .unwrap();
    assert!(backend.submit(Action::Decision {
        id: permission,
        session: session.clone(),
        allow: false
    }));
    wait(&mut backend, &wakes, |b| {
        assistant_contains(b, &session, "permission denied") && !b.view.running(&session)
    });
    assert!(backend.send(&session, "cancel".into(), vec![]));
    wait(&mut backend, &wakes, |b| {
        assistant_contains(b, &session, "partial")
    });
    assert!(backend.submit(Action::Cancel(session.clone())));
    wait(&mut backend, &wakes, |b| !b.view.running(&session));
    // A fresh bridge loads the same persisted history instead of inventing sample turns.
    drop(backend);
    let (tx, wakes) = mpsc::channel();
    let mut backend = Backend::new(
        move || {
            let _ = tx.send(());
        },
        false,
    );
    wait(&mut backend, &wakes, |b| b.view.online);
    assert!(backend.submit(Action::Load(session.clone())));
    wait(&mut backend, &wakes, |b| b.ready.contains(&session));
    assert!(assistant_contains(&backend, &session, "permission denied"));
}
