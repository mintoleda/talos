//! Stateful fake ACP subprocess used by the integration tests.
//!
//! `PASEO_ACP_FIXTURE_MODE` selects a deterministic behaviour. The `scripted`
//! mode keeps one process usable for the daemon/CLI smoke harness by choosing
//! the scenario from the prompt text; it never consults a real provider.

use std::{
    fs::OpenOptions,
    io::{self, BufRead, Write},
    time::Duration,
};

fn main() {
    let mode = std::env::var("PASEO_ACP_FIXTURE_MODE").unwrap_or_else(|_| "stream".to_owned());
    trace(
        "start",
        serde_json::json!({"mode": mode, "pid": std::process::id()}),
    );
    if mode == "scripted" && std::env::var_os("PASEO_ACP_FIXTURE_ECHO_SECRET").is_some() {
        echo_secret();
    }
    if mode == "malformed" {
        println!("{{not-json");
        let _ = io::stdout().flush();
    }
    if mode == "oversized" {
        println!("{}", "x".repeat(4096));
        let _ = io::stdout().flush();
    }
    if mode == "stderr" {
        let mut stderr = io::stderr().lock();
        for _ in 0..256 {
            let _ = writeln!(stderr, "fixture diagnostic {}", "x".repeat(1024));
        }
    }

    let stdin = io::stdin();
    let mut input = stdin.lock();
    let mut stdout = io::stdout().lock();
    let mut loaded = false;
    while let Some(line) = read_next(&mut input) {
        let Ok(message) = serde_json::from_str::<serde_json::Value>(&line) else {
            continue;
        };
        let Some(method) = message.get("method").and_then(serde_json::Value::as_str) else {
            continue;
        };
        match method {
            "initialize" => {
                trace("initialize", serde_json::json!({}));
                let capabilities = if mode == "minimal" {
                    serde_json::json!({
                        "loadSession": false,
                        "promptCapabilities": {"image": false, "audio": false, "embeddedContext": false},
                        "mcpCapabilities": {"http": false, "sse": false}
                    })
                } else {
                    serde_json::json!({
                        "loadSession": true,
                        "promptCapabilities": {"image": true, "audio": false, "embeddedContext": true},
                        "mcpCapabilities": {"http": true, "sse": true},
                        "sessionCapabilities": {"list": {}, "close": {}}
                    })
                };
                respond(
                    &mut stdout,
                    &message,
                    serde_json::json!({
                        "protocolVersion": 1,
                        "agentCapabilities": capabilities,
                        "authMethods": [],
                        "agentInfo": {"name": "fixture", "version": "1"}
                    }),
                );
            }
            "initialized" | "session/cancel" => {}
            "session/new" => {
                loaded = false;
                trace("session/new", serde_json::json!({}));
                let mut state = session_state();
                if let Some(object) = state.as_object_mut() {
                    object.insert(
                        "receivedMcpServers".into(),
                        message
                            .pointer("/params/mcpServers")
                            .cloned()
                            .unwrap_or(serde_json::json!([])),
                    );
                }
                respond(&mut stdout, &message, state);
                notify(
                    &mut stdout,
                    "session/update",
                    serde_json::json!({"sessionId":"fixture-session","update":{"sessionUpdate":"available_commands_update","availableCommands":[]}}),
                );
            }
            "session/load" => {
                let session_id = message
                    .pointer("/params/sessionId")
                    .cloned()
                    .unwrap_or(serde_json::json!("fixture-session"));
                loaded = true;
                trace(
                    "session/load",
                    serde_json::json!({"sessionId": session_id.clone()}),
                );
                notify(
                    &mut stdout,
                    "session/update",
                    serde_json::json!({"sessionId":session_id,"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"replayed"}}}),
                );
                if mode == "scripted" {
                    replay_tool_updates(&mut stdout, &session_id);
                }
                respond(
                    &mut stdout,
                    &message,
                    serde_json::json!({
                        "modes": {"currentModeId":"ask", "availableModes":[{"id":"ask","name":"Ask"}]},
                        "models": {"currentModelId":"vendor/model", "availableModels":[{"modelId":"vendor/model","name":"Model"}]}
                    }),
                );
            }
            "session/list" => respond(&mut stdout, &message, serde_json::json!({"sessions":[]})),
            "session/close" | "session/set_mode" | "session/set_model" => {
                respond(&mut stdout, &message, serde_json::json!({}))
            }
            "session/set_config_option" => respond(
                &mut stdout,
                &message,
                serde_json::json!({"configOptions":[]}),
            ),
            "fixture/echo" => respond(
                &mut stdout,
                &message,
                message
                    .get("params")
                    .cloned()
                    .unwrap_or(serde_json::Value::Null),
            ),
            "session/prompt" if mode == "scripted" => {
                scripted_prompt(&mut input, &mut stdout, &message, loaded);
            }
            "session/prompt" => match mode.as_str() {
                "eof" => return,
                "permission" => {
                    notify_request(
                        &mut stdout,
                        "fixture-permission",
                        "session/request_permission",
                        serde_json::json!({
                            "sessionId":"fixture-session",
                            "toolCall":{"toolCallId":"tool-1","title":"bash: echo x","kind":"execute","status":"pending","content":[],"rawInput":{"command":"echo x"}},
                            "options":[{"optionId":"allow_once","name":"Allow","kind":"allow_once"}]
                        }),
                    );
                    let _permission_response = read_next(&mut input);
                    notify(
                        &mut stdout,
                        "session/update",
                        serde_json::json!({"sessionId":"fixture-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"after permission"}}}),
                    );
                    respond(
                        &mut stdout,
                        &message,
                        serde_json::json!({"stopReason":"end_turn"}),
                    );
                }
                "cancel" => {
                    notify(
                        &mut stdout,
                        "session/update",
                        serde_json::json!({"sessionId":"fixture-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"partial"}}}),
                    );
                    if let Some(cancel) = read_next(&mut input) {
                        let parsed: serde_json::Value =
                            serde_json::from_str(&cancel).unwrap_or_default();
                        if parsed.get("method").and_then(serde_json::Value::as_str)
                            == Some("session/cancel")
                        {
                            respond(
                                &mut stdout,
                                &message,
                                serde_json::json!({"stopReason":"cancelled"}),
                            );
                        }
                    }
                }
                _ => {
                    let text = message
                        .pointer("/params/prompt/0/text")
                        .and_then(serde_json::Value::as_str)
                        .unwrap_or("hello world");
                    let first = if text.contains('\n') { text } else { "hello " };
                    notify(
                        &mut stdout,
                        "session/update",
                        serde_json::json!({"sessionId":"fixture-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":first}}}),
                    );
                    if !text.contains('\n') {
                        notify(
                            &mut stdout,
                            "session/update",
                            serde_json::json!({"sessionId":"fixture-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"world"}}}),
                        );
                    }
                    respond(
                        &mut stdout,
                        &message,
                        serde_json::json!({"stopReason":"end_turn","usage":{"totalTokens":3,"inputTokens":2,"outputTokens":1}}),
                    );
                }
            },
            _ => {
                if message.get("id").is_some() {
                    let id = message
                        .get("id")
                        .cloned()
                        .unwrap_or(serde_json::Value::Null);
                    let _ = writeln!(
                        stdout,
                        "{{\"jsonrpc\":\"2.0\",\"id\":{id},\"error\":{{\"code\":-32601,\"message\":\"not found\"}}}}"
                    );
                    let _ = stdout.flush();
                }
            }
        }
    }
}

fn scripted_prompt(
    input: &mut impl BufRead,
    stdout: &mut impl Write,
    message: &serde_json::Value,
    loaded: bool,
) {
    let text = message
        .pointer("/params/prompt/0/text")
        .and_then(serde_json::Value::as_str)
        .unwrap_or("hello world");
    let lower = text.to_ascii_lowercase();
    trace("prompt", serde_json::json!({"text": text}));

    if lower.contains("secret-exit") {
        echo_secret();
        std::thread::sleep(Duration::from_millis(50));
        trace("secret-exit", serde_json::json!({}));
        std::process::exit(7);
    }

    if lower.contains("tool") {
        scripted_tool_prompt(stdout, message);
        return;
    }

    if lower.contains("permission") {
        trace("permission-request", serde_json::json!({}));
        notify_request(
            stdout,
            "fixture-permission",
            "session/request_permission",
            serde_json::json!({
                "sessionId":"fixture-session",
                "toolCall":{"toolCallId":"tool-1","title":"bash: echo x","kind":"execute","status":"pending","content":[],"rawInput":{"command":"echo x"}},
                "options":[
                    {"optionId":"allow_once","name":"Allow once","kind":"allow_once"},
                    {"optionId":"reject_once","name":"Reject once","kind":"reject_once"}
                ]
            }),
        );
        let Some(option_id) = await_permission_answer(input) else {
            trace("permission-cancelled", serde_json::json!({}));
            respond(
                stdout,
                message,
                serde_json::json!({"stopReason":"cancelled"}),
            );
            return;
        };
        trace(
            "permission-selected",
            serde_json::json!({"optionId": option_id.clone()}),
        );
        let outcome = match option_id.as_str() {
            "allow_once" => "permission allowed",
            "reject_once" => "permission denied",
            _ => "permission unresolved",
        };
        emit_text(stdout, outcome);
        respond(
            stdout,
            message,
            serde_json::json!({"stopReason":"end_turn"}),
        );
        return;
    }

    if lower.contains("resume-probe") {
        if !loaded {
            trace("resume-missing", serde_json::json!({}));
            respond_error(
                stdout,
                message,
                -32000,
                "scripted fixture requires session/load before a resume probe",
            );
            return;
        }
        trace("resume-ok", serde_json::json!({}));
        emit_text(stdout, "resumed");
        respond(
            stdout,
            message,
            serde_json::json!({"stopReason":"end_turn"}),
        );
        return;
    }

    if lower.contains("cancel") {
        emit_text(stdout, "partial");
        if await_cancel(input) {
            trace("cancel-observed", serde_json::json!({}));
            respond(
                stdout,
                message,
                serde_json::json!({"stopReason":"cancelled"}),
            );
        }
        return;
    }

    emit_text(stdout, "hello ");
    emit_text(stdout, "world");
    respond(
        stdout,
        message,
        serde_json::json!({"stopReason":"end_turn","usage":{"totalTokens":3,"inputTokens":2,"outputTokens":1}}),
    );
}

fn scripted_tool_prompt(stdout: &mut impl Write, message: &serde_json::Value) {
    trace(
        "tool-call",
        serde_json::json!({"toolCallId": "fixture-tool-1"}),
    );
    notify(
        stdout,
        "session/update",
        serde_json::json!({
            "sessionId":"fixture-session",
            "update":{
                "sessionUpdate":"tool_call",
                "toolCallId":"fixture-tool-1",
                "title":"bash: echo tool",
                "kind":"execute",
                "status":"pending",
                "rawInput":{"command":"echo tool"}
            }
        }),
    );
    trace(
        "tool-update",
        serde_json::json!({"toolCallId": "fixture-tool-1", "status": "completed"}),
    );
    notify(
        stdout,
        "session/update",
        serde_json::json!({
            "sessionId":"fixture-session",
            "update":{
                "sessionUpdate":"tool_call_update",
                "toolCallId":"fixture-tool-1",
                "title":"bash: echo tool",
                "kind":"execute",
                "status":"completed",
                "content":[{"type":"text","text":"tool ok"}],
                "rawOutput":{"stdout":"tool ok"}
            }
        }),
    );
    emit_text(stdout, "tool complete");
    respond(
        stdout,
        message,
        serde_json::json!({"stopReason":"end_turn","usage":{"totalTokens":6,"inputTokens":4,"outputTokens":2}}),
    );
}

fn replay_tool_updates(stdout: &mut impl Write, session_id: &serde_json::Value) {
    trace("session/load-replay-tool", serde_json::json!({}));
    notify(
        stdout,
        "session/update",
        serde_json::json!({
            "sessionId": session_id,
            "update":{
                "sessionUpdate":"tool_call",
                "toolCallId":"replayed-tool-1",
                "title":"replayed tool",
                "kind":"execute",
                "status":"pending",
                "rawInput":{"command":"replayed"}
            }
        }),
    );
    notify(
        stdout,
        "session/update",
        serde_json::json!({
            "sessionId": session_id,
            "update":{
                "sessionUpdate":"tool_call_update",
                "toolCallId":"replayed-tool-1",
                "title":"replayed tool",
                "kind":"execute",
                "status":"completed",
                "rawOutput":{"stdout":"replayed"}
            }
        }),
    );
}

fn await_permission_answer(input: &mut impl BufRead) -> Option<String> {
    while let Some(line) = read_next(input) {
        let Ok(parsed) = serde_json::from_str::<serde_json::Value>(&line) else {
            continue;
        };
        if parsed.get("method").and_then(serde_json::Value::as_str) == Some("session/cancel") {
            return None;
        }
        if parsed.get("id").and_then(serde_json::Value::as_str) == Some("fixture-permission") {
            return parsed
                .pointer("/result/outcome/optionId")
                .and_then(serde_json::Value::as_str)
                .map(str::to_owned);
        }
    }
    None
}

fn await_cancel(input: &mut impl BufRead) -> bool {
    while let Some(line) = read_next(input) {
        let Ok(parsed) = serde_json::from_str::<serde_json::Value>(&line) else {
            continue;
        };
        if parsed.get("method").and_then(serde_json::Value::as_str) == Some("session/cancel") {
            return true;
        }
    }
    false
}

fn emit_text(stdout: &mut impl Write, text: &str) {
    notify(
        stdout,
        "session/update",
        serde_json::json!({"sessionId":"fixture-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":text}}}),
    );
}

fn echo_secret() {
    let Ok(secret) = std::env::var("FIXTURE_SECRET") else {
        return;
    };
    let mut stderr = io::stderr().lock();
    let _ = writeln!(stderr, "fixture secret {secret}");
    let _ = stderr.flush();
}

fn trace(event: &str, data: serde_json::Value) {
    let Some(path) = std::env::var_os("PASEO_ACP_FIXTURE_TRACE") else {
        return;
    };
    let Ok(mut file) = OpenOptions::new().create(true).append(true).open(path) else {
        return;
    };
    let _ = writeln!(file, "{}", serde_json::json!({"event":event,"data":data}));
    let _ = file.flush();
}

fn session_state() -> serde_json::Value {
    serde_json::json!({
        "sessionId": "fixture-session",
        "modes": {"currentModeId":"ask", "availableModes":[{"id":"ask","name":"Ask"}]},
        "models": {"currentModelId":"vendor/model", "availableModels":[{"modelId":"vendor/model","name":"Model"}]},
        "configOptions": [{"id":"thought_level","name":"Thinking","type":"select","currentValue":"off","options":[{"value":"off","name":"off"}]}]
    })
}

fn read_next(reader: &mut impl BufRead) -> Option<String> {
    let mut line = String::new();
    reader
        .read_line(&mut line)
        .ok()
        .filter(|count| *count > 0)
        .map(|_| line)
}

fn respond(stdout: &mut impl Write, request: &serde_json::Value, result: serde_json::Value) {
    if let Some(id) = request.get("id") {
        let _ = writeln!(
            stdout,
            "{{\"jsonrpc\":\"2.0\",\"id\":{id},\"result\":{result}}}"
        );
        let _ = stdout.flush();
    }
}

fn respond_error(stdout: &mut impl Write, request: &serde_json::Value, code: i64, message: &str) {
    if let Some(id) = request.get("id") {
        let _ = writeln!(
            stdout,
            "{{\"jsonrpc\":\"2.0\",\"id\":{id},\"error\":{{\"code\":{code},\"message\":{message:?}}}}}"
        );
        let _ = stdout.flush();
    }
}

fn notify(stdout: &mut impl Write, method: &str, params: serde_json::Value) {
    let _ = writeln!(
        stdout,
        "{{\"jsonrpc\":\"2.0\",\"method\":{method:?},\"params\":{params}}}"
    );
    let _ = stdout.flush();
}

fn notify_request(stdout: &mut impl Write, id: &str, method: &str, params: serde_json::Value) {
    let _ = writeln!(
        stdout,
        "{{\"jsonrpc\":\"2.0\",\"id\":{id:?},\"method\":{method:?},\"params\":{params}}}"
    );
    let _ = stdout.flush();
}
