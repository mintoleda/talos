use paseo_ipc::{
    Client, Command, EventNotification, IpcError, MAX_FRAME_SIZE, PROTOCOL_VERSION, RpcError,
    ServerFrame, WireEvent, serve_connection, serve_connection_with_events,
};
use serde_json::{Value, json};
use tokio::{
    io::{AsyncWriteExt, duplex},
    sync::broadcast,
};

#[tokio::test]
async fn handshake_commands_and_replay_cursor_work_over_local_stream() {
    let (client_io, server_io) = duplex(4096);
    let server = tokio::spawn(async move {
        serve_connection(server_io, Some("secret".into()), |command| async move {
            match command {
                Command::Replay { after, .. } => {
                    Ok(json!({"after": after, "events": [{"cursor": 8}]}))
                }
                Command::Health => Ok(json!({"status": "ok"})),
                _ => Err(RpcError::new(-32601, "unsupported")),
            }
        })
        .await
    });
    let mut client = Client::new(client_io, Some("secret".into()));
    assert_eq!(client.negotiate().await.unwrap(), PROTOCOL_VERSION);
    assert_eq!(
        client.call(Command::Health).await.unwrap(),
        json!({"status":"ok"})
    );
    assert_eq!(
        client
            .call(Command::Replay {
                after: 7,
                limit: Some(20)
            })
            .await
            .unwrap(),
        json!({"after":7,"events":[{"cursor":8}]})
    );
    drop(client);
    server.await.unwrap().unwrap();
}

#[tokio::test]
async fn authentication_is_rejected() {
    let (client_io, server_io) = duplex(1024);
    let server = tokio::spawn(async move {
        serve_connection(server_io, Some("expected".into()), |_| async {
            Ok(Value::Null)
        })
        .await
    });
    let mut client = Client::new(client_io, Some("wrong".into()));
    assert!(matches!(
        client.negotiate().await,
        Err(IpcError::Remote(-32003, _))
    ));
    server.await.unwrap().unwrap();
}

#[tokio::test]
async fn malformed_and_oversized_frames_fail_closed() {
    let (mut writer, reader) = duplex(1024);
    writer.write_all(b"{not json}\n").await.unwrap();
    let mut buffered = tokio::io::BufReader::new(reader);
    let bytes = paseo_ipc::read_frame(&mut buffered).await.unwrap();
    assert!(serde_json::from_slice::<Value>(&bytes).is_err());

    let (mut writer, reader) = duplex(MAX_FRAME_SIZE + 16);
    writer
        .write_all(&vec![b'x'; MAX_FRAME_SIZE + 1])
        .await
        .unwrap();
    let mut buffered = tokio::io::BufReader::new(reader);
    assert!(matches!(
        paseo_ipc::read_frame(&mut buffered).await,
        Err(IpcError::FrameTooLarge)
    ));
}

#[tokio::test]
async fn event_notifications_are_buffered_while_waiting_for_rpc_response() {
    let (event_tx, _) = broadcast::channel(8);
    let (client_io, server_io) = duplex(4096);
    let server_events = event_tx.subscribe();
    let server = tokio::spawn(async move {
        serve_connection_with_events(
            server_io,
            None,
            |_| async { Ok(json!({"status":"ok"})) },
            server_events,
        )
        .await
    });
    let mut client = Client::new(client_io, None);
    client.negotiate().await.unwrap();
    event_tx
        .send(EventNotification {
            version: PROTOCOL_VERSION,
            cursor: 3,
            event_id: "event-3".into(),
            event: WireEvent {
                kind: "timeline.updated".into(),
                payload: json!({"session_id":"session-1"}),
            },
        })
        .unwrap();
    assert_eq!(
        client.call(Command::Health).await.unwrap(),
        json!({"status":"ok"})
    );
    assert!(matches!(
        client.next_frame().await.unwrap(),
        ServerFrame::Event(_)
    ));
    drop(client);
    server.await.unwrap().unwrap();
}

#[test]
fn session_replay_command_has_a_stable_session_scoped_wire_shape() {
    let value = serde_json::to_value(Command::ReplaySession {
        session_id: "session-1".into(),
        after: 9,
        limit: Some(20),
    })
    .unwrap();
    assert_eq!(
        value,
        json!({
            "method":"replay_session",
            "params":{"session_id":"session-1","after":9,"limit":20}
        })
    );
}

#[test]
fn event_notifications_preserve_ordered_cursors_on_wire() {
    let events = [4, 5, 6].map(|cursor| {
        ServerFrame::Event(EventNotification {
            version: PROTOCOL_VERSION,
            cursor,
            event_id: format!("event-{cursor}"),
            event: WireEvent {
                kind: "session.updated".into(),
                payload: json!({}),
            },
        })
    });
    let decoded: Vec<ServerFrame> = events
        .iter()
        .map(|frame| serde_json::from_str(&serde_json::to_string(frame).unwrap()).unwrap())
        .collect();
    let cursors: Vec<_> = decoded
        .iter()
        .map(|frame| match frame {
            ServerFrame::Event(event) => event.cursor,
            ServerFrame::Response(_) => 0,
        })
        .collect();
    assert_eq!(cursors, vec![4, 5, 6]);
}
