use std::{
    path::PathBuf,
    sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    },
    time::Duration,
};

use paseo_acp::{
    AcpClient, AcpError, AcpEvent, ContentBlock, HttpHeader, McpServer, McpTransport,
    PermissionDecision, PermissionFuture, PermissionHandler, ProcessConfig, RpcId,
};
use serde_json::{Value, json};
use tokio_util::sync::CancellationToken;

fn fixture(mode: &str) -> ProcessConfig {
    let mut config = ProcessConfig::new(env!("CARGO_BIN_EXE_acp-fixture"));
    config
        .env
        .insert("TALOSD_ACP_FIXTURE_MODE".to_owned(), mode.to_owned());
    config
}

async fn initialized(mode: &str) -> AcpClient {
    let client = AcpClient::spawn(fixture(mode))
        .await
        .expect("spawn fixture");
    let capabilities = client.initialize().await.expect("initialize fixture");
    assert_eq!(capabilities.protocol_version, 1);
    assert!(capabilities.auth_methods.is_empty());
    client
}

async fn new_session(client: &AcpClient) -> String {
    client
        .new_session("/tmp", &[])
        .await
        .expect("new session")
        .session_id
}

#[tokio::test]
async fn streaming_session_methods_and_string_rpc_ids() {
    let client = initialized("stream").await;
    let session_id = new_session(&client).await;
    assert_eq!(session_id, "fixture-session");

    let echo: Value = client
        .call_with_id(
            RpcId::String("caller-id".to_owned()),
            "fixture/echo",
            json!({"hello":"world"}),
        )
        .await
        .expect("string ID response");
    assert_eq!(echo, json!({"hello":"world"}));
    let unsigned_echo: Value = client
        .call_with_id(
            RpcId::Unsigned(u64::MAX),
            "fixture/echo",
            json!({"unsigned":true}),
        )
        .await
        .expect("unsigned integer ID response");
    assert_eq!(unsigned_echo, json!({"unsigned":true}));

    let response = client
        .prompt(
            &session_id,
            &[ContentBlock::Text {
                text: "say hi".to_owned(),
                extra: Default::default(),
            }],
        )
        .await
        .expect("prompt");
    assert_eq!(response.stop_reason, "end_turn");
    assert_eq!(response.usage.expect("usage").total_tokens, 3);

    let first = client
        .next_event()
        .await
        .expect("available commands update");
    assert!(matches!(first, AcpEvent::SessionUpdate(_)));
    let second = client.next_event().await.expect("first text update");
    let third = client.next_event().await.expect("second text update");
    for event in [second, third] {
        let AcpEvent::SessionUpdate(update) = event else {
            panic!("expected session update")
        };
        assert_eq!(update.update.session_update, "agent_message_chunk");
        assert!(update.update.content.is_some());
    }

    let listed = client
        .list_sessions(None, None)
        .await
        .expect("list sessions");
    assert!(listed.sessions.is_empty());
    client
        .set_mode(&session_id, "ask")
        .await
        .expect("set advertised mode");
    assert!(client.set_mode(&session_id, "panic").await.is_err());
    client
        .set_model(&session_id, "vendor/model")
        .await
        .expect("set advertised model");
    assert!(
        client
            .set_model(&session_id, "vendor/not-advertised")
            .await
            .is_err()
    );
    client
        .set_config_option(&session_id, "thought_level", "off")
        .await
        .expect("config option");
    client
        .close_session(&session_id)
        .await
        .expect("close session");
}

struct AllowPermission(Arc<AtomicUsize>);

impl PermissionHandler for AllowPermission {
    fn request_permission<'a>(
        &'a self,
        request: paseo_acp::PermissionRequest,
        _cancellation: CancellationToken,
    ) -> PermissionFuture<'a> {
        self.0.fetch_add(1, Ordering::SeqCst);
        Box::pin(async move {
            assert_eq!(request.session_id, "fixture-session");
            assert_eq!(request.tool_call.tool_call_id, "tool-1");
            PermissionDecision::selected("allow_once")
        })
    }
}

#[tokio::test]
async fn reverse_permission_request_does_not_block_prompt_reader() {
    let count = Arc::new(AtomicUsize::new(0));
    let client = AcpClient::spawn_with_permission_handler(
        fixture("permission"),
        Arc::new(AllowPermission(count.clone())),
    )
    .await
    .expect("spawn fixture");
    client.initialize().await.expect("initialize");
    let session_id = new_session(&client).await;
    let response = client
        .prompt(
            &session_id,
            &[ContentBlock::Text {
                text: "request permission".into(),
                extra: Default::default(),
            }],
        )
        .await
        .expect("prompt completes after permission response");
    assert_eq!(response.stop_reason, "end_turn");
    assert_eq!(count.load(Ordering::SeqCst), 1);
}

#[tokio::test]
async fn cancellation_is_a_notification_and_keeps_prompt_pending() {
    let client = Arc::new(initialized("cancel").await);
    let session_id = new_session(&client).await;
    let cancellation = CancellationToken::new();
    let prompt_client = client.clone();
    let prompt_cancel = cancellation.clone();
    let prompt = tokio::spawn(async move {
        prompt_client
            .prompt_cancellable(
                &session_id,
                &[ContentBlock::Text {
                    text: "long-running".into(),
                    extra: Default::default(),
                }],
                &prompt_cancel,
            )
            .await
    });

    let event = tokio::time::timeout(Duration::from_secs(3), client.next_event())
        .await
        .expect("partial update timeout")
        .expect("partial update");
    let AcpEvent::SessionUpdate(update) = event else {
        panic!("expected streamed update")
    };
    assert_eq!(update.update.session_update, "available_commands_update");
    // Drain the partial content update after the create-session command update.
    let event = client.next_event().await.expect("partial content");
    assert!(matches!(event, AcpEvent::SessionUpdate(_)));
    cancellation.cancel();
    let response = tokio::time::timeout(Duration::from_secs(3), prompt)
        .await
        .expect("cancelled prompt response timeout")
        .expect("prompt task")
        .expect("prompt response");
    assert_eq!(response.stop_reason, "cancelled");
}

#[tokio::test]
async fn idle_event_reader_does_not_busy_poll_after_process_starts() {
    use std::{
        cell::Cell,
        future::{Future, poll_fn},
    };
    let client = initialized("stream").await;
    let _session = new_session(&client).await;
    let _commands = client.next_event().await.expect("initial commands");
    {
        let mut idle = std::pin::pin!(client.next_event());
        let polls = Cell::new(0_u32);
        let observed = poll_fn(|cx| {
            polls.set(polls.get() + 1);
            idle.as_mut().poll(cx)
        });
        assert!(
            tokio::time::timeout(Duration::from_millis(30), observed)
                .await
                .is_err()
        );
        assert!(
            polls.get() < 10,
            "idle reader repeatedly woke itself: {} polls",
            polls.get()
        );
    }
    client.shutdown(Duration::from_millis(200)).await;
}

#[tokio::test]
async fn large_and_newline_escaped_prompt_frames_round_trip() {
    let client = initialized("stream").await;
    let session_id = new_session(&client).await;
    let _commands = client.next_event().await.expect("commands update");
    let large_text = format!("{}\n{}", "x".repeat(1024 * 1024), "y".repeat(1024 * 1024));
    let response = client
        .prompt(
            &session_id,
            &[ContentBlock::Text {
                text: large_text.clone(),
                extra: Default::default(),
            }],
        )
        .await
        .expect("2 MiB frame under the 64 MiB limit");
    assert_eq!(response.stop_reason, "end_turn");
    let event = client.next_event().await.expect("large streamed update");
    let AcpEvent::SessionUpdate(update) = event else {
        panic!("expected session update")
    };
    assert_eq!(
        update
            .update
            .content
            .as_ref()
            .and_then(|content| content.get("text"))
            .and_then(Value::as_str),
        Some(large_text.as_str())
    );
}

#[tokio::test]
async fn mcp_descriptors_are_forwarded_and_optional_capabilities_are_gated() {
    let client = initialized("stream").await;
    let descriptor = McpServer {
        transport: Some(McpTransport::Http),
        name: "paseo".into(),
        url: Some("https://example.invalid/mcp".into()),
        headers: vec![HttpHeader {
            name: "Authorization".into(),
            value: "Bearer private-token".into(),
        }],
        ..McpServer::default()
    };
    let state = client
        .new_session("/tmp", &[descriptor])
        .await
        .expect("MCP session");
    assert_eq!(state.extra["receivedMcpServers"][0]["type"], "http");
    assert_eq!(
        state.extra["receivedMcpServers"][0]["headers"][0]["value"],
        "Bearer private-token"
    );

    let minimal = initialized("minimal").await;
    let session_id = new_session(&minimal).await;
    assert!(matches!(
        minimal.load_session(&session_id, "/tmp", &[]).await,
        Err(AcpError::UnsupportedCapability("session/load"))
    ));
    assert!(matches!(
        minimal.list_sessions(None, None).await,
        Err(AcpError::UnsupportedCapability("session/list"))
    ));
    assert!(matches!(
        minimal.close_session(&session_id).await,
        Err(AcpError::UnsupportedCapability("session/close"))
    ));
    assert!(matches!(
        minimal
            .prompt(
                &session_id,
                &[ContentBlock::Image {
                    mime_type: Some("image/png".into()),
                    data: Some("abc".into()),
                    uri: None,
                    extra: Default::default()
                }]
            )
            .await,
        Err(AcpError::UnsupportedCapability("prompt image"))
    ));
    assert!(matches!(
        minimal
            .new_session(
                "/tmp",
                &[McpServer {
                    transport: Some(McpTransport::Http),
                    name: "remote".into(),
                    ..McpServer::default()
                }]
            )
            .await,
        Err(AcpError::UnsupportedCapability("MCP HTTP"))
    ));
}

#[tokio::test]
async fn load_replay_is_buffered_then_delivered_as_one_event() {
    let client = initialized("stream").await;
    let session_id = new_session(&client).await;
    let _commands = client.next_event().await.expect("commands update");
    let loaded = client
        .load_session(&session_id, "/tmp", &[])
        .await
        .expect("load session");
    assert_eq!(loaded.modes.expect("modes").current_mode_id, "ask");
    let replay = client.next_event().await.expect("atomic replay");
    let AcpEvent::SessionReplay {
        session_id: replay_id,
        updates,
    } = replay
    else {
        panic!("expected replay event")
    };
    assert_eq!(replay_id, session_id);
    assert_eq!(updates.len(), 1);
    assert_eq!(updates[0].update.session_update, "agent_message_chunk");
}

#[tokio::test]
async fn malformed_and_oversized_stdout_are_protocol_failures() {
    let malformed = AcpClient::spawn(fixture("malformed"))
        .await
        .expect("spawn malformed fixture");
    let error = malformed
        .initialize()
        .await
        .expect_err("malformed stdout must fail");
    assert!(matches!(error, AcpError::Protocol(_)));

    let mut oversized_config = fixture("oversized");
    oversized_config.max_frame_size = 256;
    let oversized = AcpClient::spawn(oversized_config)
        .await
        .expect("spawn oversized fixture");
    let error = oversized
        .initialize()
        .await
        .expect_err("oversized stdout must fail");
    assert!(matches!(error, AcpError::FrameTooLarge { limit: 256 }));
}

#[tokio::test]
async fn stderr_is_drained_bounded_and_secrets_are_redacted() {
    let mut config = fixture("stderr");
    config.stderr_tail_bytes = 4096;
    config
        .env
        .insert("FIXTURE_SECRET".into(), "fixture diagnostic".into());
    let client = AcpClient::spawn(config).await.expect("spawn fixture");
    client
        .initialize()
        .await
        .expect("initialize despite stderr flood");
    let tail = client.stderr_tail();
    assert!(tail.len() <= 4096);
    assert!(!tail.contains("fixture diagnostic"));
    assert!(tail.contains("[REDACTED]"));
}

#[tokio::test]
async fn process_exit_fails_pending_prompt_and_shutdown_is_bounded() {
    let client = initialized("eof").await;
    let session_id = new_session(&client).await;
    let result = tokio::time::timeout(
        Duration::from_secs(3),
        client.prompt(
            &session_id,
            &[ContentBlock::Text {
                text: "exit now".into(),
                extra: Default::default(),
            }],
        ),
    )
    .await
    .expect("process exit propagation timeout");
    assert!(matches!(
        result,
        Err(AcpError::ProcessExited { .. }) | Err(AcpError::ConnectionClosed)
    ));
    let status = client.shutdown(Duration::from_millis(100)).await;
    assert!(status.is_terminal());
}

#[tokio::test]
async fn talos_provider_defaults_use_acp_subcommand() {
    let defaults = paseo_acp::talos_provider_defaults("/opt/talos");
    assert_eq!(defaults.provider_id, "talos");
    assert_eq!(defaults.command, "/opt/talos");
    assert_eq!(defaults.args, ["acp"]);
    let talos = ProcessConfig::talos(PathBuf::from("/opt/talos"));
    assert_eq!(talos.args, ["acp"]);
}
