use std::time::Duration;

use paseo_ipc::{
    Client, Command, EventNotification, PROTOCOL_VERSION, Request, Response, RpcOutcome,
    ServerFrame, serve_connection_with_events, write_frame,
};
use serde_json::json;
use tokio::{
    io::{AsyncWriteExt, BufReader, duplex},
    sync::broadcast,
};

#[tokio::test]
async fn events_between_request_fragments_do_not_corrupt_the_request() {
    let (client_io, server_io) = duplex(4096);
    let (sender, receiver) = broadcast::channel(8);
    let server = tokio::spawn(async move {
        serve_connection_with_events(server_io, None, |_| async { Ok(json!("ok")) }, receiver).await
    });
    let (read, mut write) = tokio::io::split(client_io);
    let mut read = BufReader::new(read);
    write_frame(
        &mut write,
        &Request {
            version: PROTOCOL_VERSION,
            id: 1,
            auth_token: None,
            command: Command::Hello {
                min_version: 1,
                max_version: 1,
            },
        },
    )
    .await
    .unwrap();
    let _ = paseo_ipc::read_frame(&mut read).await.unwrap();
    write.write_all(b"{\"version\":1,\"id\":2,").await.unwrap();
    // Let the request reader consume the prefix and await the rest.
    tokio::time::sleep(Duration::from_millis(20)).await;
    sender
        .send(EventNotification::replay_required("test", 1))
        .unwrap();
    let bytes = paseo_ipc::read_frame(&mut read).await.unwrap();
    assert!(matches!(
        serde_json::from_slice::<ServerFrame>(&bytes).unwrap(),
        ServerFrame::Event(_)
    ));
    write
        .write_all(b"\"command\":{\"method\":\"health\"}}\n")
        .await
        .unwrap();
    let bytes = tokio::time::timeout(Duration::from_secs(2), paseo_ipc::read_frame(&mut read))
        .await
        .unwrap()
        .unwrap();
    assert!(matches!(
        serde_json::from_slice::<ServerFrame>(&bytes).unwrap(),
        ServerFrame::Response(Response {
            id: 2,
            outcome: RpcOutcome::Success { .. },
            ..
        })
    ));
    drop(read);
    drop(write);
    server.await.unwrap().unwrap();
}

#[tokio::test]
async fn cancelled_client_event_read_retains_partial_frame() {
    let (client_io, mut server_io) = duplex(4096);
    let expected = EventNotification::replay_required("fixture", 3);
    let mut encoded = serde_json::to_vec(&ServerFrame::Event(expected.clone())).unwrap();
    encoded.push(b'\n');
    let (ready, received) = tokio::sync::oneshot::channel();
    let (resume, resumed) = tokio::sync::oneshot::channel();
    let server = tokio::spawn(async move {
        server_io.write_all(&encoded[..12]).await.unwrap();
        ready.send(()).unwrap();
        resumed.await.unwrap();
        server_io.write_all(&encoded[12..]).await.unwrap();
    });
    let mut client = Client::new(client_io, None);
    received.await.unwrap();
    assert!(
        tokio::time::timeout(Duration::from_millis(10), client.next_frame())
            .await
            .is_err()
    );
    resume.send(()).unwrap();
    let frame = tokio::time::timeout(Duration::from_secs(1), client.next_frame())
        .await
        .unwrap()
        .unwrap();
    assert_eq!(frame, ServerFrame::Event(expected));
    server.await.unwrap();
}

#[tokio::test]
async fn authentication_and_protocol_are_checked_on_every_command() {
    use std::sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    };
    let calls = Arc::new(AtomicUsize::new(0));
    let count = calls.clone();
    let (client_io, server_io) = duplex(4096);
    let server = tokio::spawn(async move {
        paseo_ipc::serve_connection(server_io, Some("secret".into()), move |_| {
            count.fetch_add(1, Ordering::SeqCst);
            async { Ok(json!("ok")) }
        })
        .await
    });
    let (read, mut write) = tokio::io::split(client_io);
    let mut read = BufReader::new(read);
    for (id, version, token, command, expected_error) in [
        (
            1,
            PROTOCOL_VERSION,
            Some("secret"),
            Command::Hello {
                min_version: 1,
                max_version: 1,
            },
            None,
        ),
        (2, PROTOCOL_VERSION, None, Command::Health, Some(-32003)),
        (
            3,
            PROTOCOL_VERSION,
            Some("wrong"),
            Command::Health,
            Some(-32003),
        ),
        (
            4,
            PROTOCOL_VERSION + 1,
            Some("secret"),
            Command::Health,
            Some(-32001),
        ),
        (5, PROTOCOL_VERSION, Some("secret"), Command::Health, None),
    ] {
        write_frame(
            &mut write,
            &Request {
                version,
                id,
                auth_token: token.map(str::to_owned),
                command,
            },
        )
        .await
        .unwrap();
        let bytes = paseo_ipc::read_frame(&mut read).await.unwrap();
        let ServerFrame::Response(response) = serde_json::from_slice(&bytes).unwrap() else {
            panic!("expected a response");
        };
        assert_eq!(response.id, id);
        match (response.outcome, expected_error) {
            (RpcOutcome::Failure { error }, Some(code)) => assert_eq!(error.code, code),
            (RpcOutcome::Success { .. }, None) => {}
            other => panic!("unexpected response: {other:?}"),
        }
    }
    assert_eq!(calls.load(Ordering::SeqCst), 1);
    drop(read);
    drop(write);
    server.await.unwrap().unwrap();
}

#[tokio::test]
async fn overflowing_client_queue_requires_replay_instead_of_silent_loss() {
    let (client_io, server_io) = duplex(4096);
    let server = tokio::spawn(async move {
        let (read, mut write) = tokio::io::split(server_io);
        let mut read = BufReader::new(read);
        let _ = paseo_ipc::read_frame(&mut read).await.unwrap();
        write_frame(
            &mut write,
            &ServerFrame::Response(Response {
                version: PROTOCOL_VERSION,
                id: 1,
                outcome: RpcOutcome::Success {
                    result: json!({"version":1}),
                },
            }),
        )
        .await
        .unwrap();
        let _ = paseo_ipc::read_frame(&mut read).await.unwrap();
        for cursor in 1..=1100 {
            let mut event = EventNotification::replay_required("fixture", 0);
            event.event.kind = "timeline".into();
            event.cursor = cursor;
            write_frame(&mut write, &ServerFrame::Event(event))
                .await
                .unwrap();
        }
        write_frame(
            &mut write,
            &ServerFrame::Response(Response {
                version: PROTOCOL_VERSION,
                id: 2,
                outcome: RpcOutcome::Success {
                    result: json!("ok"),
                },
            }),
        )
        .await
        .unwrap();
    });
    let mut client = Client::new(client_io, None);
    client.negotiate().await.unwrap();
    assert_eq!(client.call(Command::Health).await.unwrap(), json!("ok"));
    let ServerFrame::Event(marker) = client.next_frame().await.unwrap() else {
        panic!("expected replay marker")
    };
    assert_eq!(marker.event.kind, "replay_required");
    assert_eq!(marker.event.payload["reason"], "client_event_lag");
    assert_eq!(marker.cursor, 0);
    server.await.unwrap();
}
