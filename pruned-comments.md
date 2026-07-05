# Pruned Comments — Current State

Below is every `//` comment (non-godoc, non-`//go:embed`, non-`// indirect`) found in the Go source tree after your recent edits. Comments that merely restate what the code already expresses — without adding context about non-obvious algorithms, library quirks, performance trade-offs, or business rules — are identified as pruning candidates per the system prompt rule.

---

## cmd/talos/main.go

```
300: // Inject a per-turn reminder listing files the model has actually opened
301: // in this session. Surfaced via Request.Volatile so it does not break
302: // the cacheable prefix (system + tools + messages[:-1]).
```
**Keep** — Explains the Volatile mechanism and why it's used.

```
359: // Ephemeral session: -p runs must not leave traces on disk.
360: // Remove the transcript and reads files so the session does not
361: // show up in /resume or the resume picker.
```
**Keep** — Explains the reason (no disk traces for -p mode), not obvious from `os.RemoveAll` alone.

```
482: // renderTo returns an EmitFunc that writes events to out in a human-readable
483: // format suitable for one-shot (-p) mode.
```
**Prune** — Function name and return type already say this.

```
534: // Unknown event type: ignore safely so new events don't break
535: // older frontends.
```
**Keep** — Explains *why* you ignore (forward compat), which is a design decision.

```
540: // newProvider creates an LLM provider and compactor from the given config.
541: // Extracted so the switchProvider closure can re-create them at runtime.
```
**Prune** — First sentence restates the function name. Second sentence is useful — maybe keep that half.

```
555: // For all OpenAI-compatible providers, look up the canonical base URL
556: // from the known-provider registry so switchProvider works correctly
557: // after a model picker selection. Always check known providers first,
558: // and only fall back to cfg.BaseURL for custom/unknown providers —
559: // the default "https://api.deepseek.com" should not override a known
560: // provider's endpoint.
```
**Keep** — Explains the resolution order and the default-override problem, which is non-obvious.

```
575: // Build the compactor. By default, use a deterministic, zero-cost
576: // placeholder summarizer that keeps the prefix cacheable after compaction.
577: // When the user sets summary_model in config, an LLM-based summarizer
578: // (using the specified model) replaces it for richer summaries.
```
**Keep** — Explains the caching property of the placeholder, which is a non-obvious design choice.

```
597: // cleanBaseURL strips trailing /v1 (and /v1/) suffixes from a provider base URL.
598: // The anthropic and openai clients already append their own /v1/<endpoint> paths,
599: // so a user-provided URL like https://api.openai.com/v1 would produce double /v1
600: // segments (e.g. /v1/v1/chat/completions).
```
**Keep** — Explains *why* the stripping is needed (double /v1 bug), which is a library quirk.

```
641: // /model <provider/model> — switch directly.
```
**Prune** — The `if len(parts) >= 2` branch makes this obvious.

```
662: // /model with no args — fetch and display available models.
```
**Prune** — The `entries, err := a.fetchModels()` call makes this obvious.

```
694: // Load the existing session transcript so newly-attached clients see
695: // messages and tool calls that happened before they connected.
```
**Keep** — Explains *why* the transcript is loaded (for new clients), which is a design detail.

```
728: // Forward server events to the Bubble Tea program.
```
**Prune** — `go func() { for ev := range events { p.Send(...) } }()` is self-evident.

```
738: // pickSession lists this project's sessions and lets the user choose one,
739: // falling back to the latest on empty input.
```
**Prune** — Function name + parameters say this.

```
757: // pickRunningServer returns the session ID of a running talos server.
758: // If exactly one server is alive it returns it immediately.
759: // If multiple are running it prompts the user to choose.
```
**Prune** — Function name says this.

---

## cmd/talos/app.go

```
133: // Wrap the engine cleanup to also close the per-tab loop.
```
**Keep** — Explains the reason for wrapping, which is not obvious from the code.

```
237: // Anthropic doesn't expose a /v1/models endpoint; skip it and fall
238: // back to its hardcoded model list elsewhere.
```
**Keep** — Explains the API quirk (Anthropic missing endpoint), which is a library/workaround detail.

---

## internal/agents/definition.go

```
1-11: // Package agents implements ... (package doc)
```
**Keep** — Package doc.

```
24: //go:embed builtin/*.md
```
**Keep** — Compiler directive.

```
27: // Definition is a fully-parsed agent loadout.
```
**Prune** — Type name says this.

```
29-37: field comments
```
**Keep** — These document struct fields in a public type.

```
40: // frontmatter mirrors the YAML header of an agent markdown file.
```
**Prune** — Type name `frontmatter` says this.

```
51: // Dir is a directory to scan for agent markdown files.
```
**Prune** — Struct field name says this.

```
57: // Load builds the agent set: embedded builtins first, then each dir in order,
58: // later definitions overriding earlier ones with the same name.
```
**Prune** — Function name + the loading loop logic is clear.

```
105: // parse splits a markdown file into its YAML frontmatter and prompt body.
```
**Prune** — Function name says this.

```
120: // Support "provider/model" shorthand in the model field.
```
**Keep** — Documents a supported format, not obvious from the code.

```
139: // splitFrontmatter separates a leading `--- ... ---` YAML block from the body.
140: // If no frontmatter is present, the whole text is treated as the body.
```
**Prune** — Function name + the `if !strings.HasPrefix` guard makes this clear.

```
148: // Closing delimiter must start a line.
```
**Prune** — `strings.Index(rest, "\n---")` makes this obvious.

```
155: // Drop the remainder of the closing "---" line.
```
**Prune** — `body = rest[idx+len("\n---"):]` makes this obvious.

```
162: // RenderListing returns a compact markdown snippet naming the subagents the
163: // primary agent may spawn, for appending to its system prompt. It mirrors the
164: // skills listing so the model is nudged to delegate.
```
**Keep** — Explains the purpose (nudge to delegate), not obvious from function name.

---

## internal/agents/builder.go

```
25: // defaultMaxDepth bounds subagent nesting regardless of definition wiring, as a
26: // backstop against pathological recursion. The primary agent is depth 0.
```
**Keep** — Explains the reason (backstop against recursion), not obvious.

```
29: // Config carries the shared runtime settings a Builder needs to construct nested
30: // agent loops. Subagents inherit the caller's provider, credentials, and working
31: // directory; a definition may override the model, thinking level, and provider.
```
**Keep** — Documents config struct semantics.

```
48: // Builder constructs subagent loops and the spawn tools that drive them.
```
**Prune** — Type name says this.

```
84: // SpawnTools returns one spawn tool per named agent that exists, for injection
85: // into a registry. These are the agents the holder may delegate to.
```
**Prune** — Function name says this.

```
102: // builtLoop bundles a constructed subagent loop with the data needed to account
103: // for it after the run.
```
**Prune** — Type name says this.

```
112: // build assembles a fresh, isolated loop for one subagent run. depth is the
113: // depth of the agent being built (used to wire its own nested spawn tools).
```
**Keep** — Explains the depth parameter semantics.

```
131: // The agent's own subagents become spawn tools one level deeper.
```
**Prune** — The loop below makes this clear.

```
134: // interactive=true: a human is reachable via the event stream, so risky
135: // bash in ask-mode routes a PermissionRequested up to the same dialog.
```
**Keep** — Explains the interactive parameter's effect on permission routing.

```
154: // providerFor builds an LLM client for a subagent. If the definition specifies
155: // a provider override, that provider's credentials are resolved from auth.json
156: // and its default base URL is used. Otherwise the caller's provider is inherited.
```
**Keep** — Explains resolution order for credentials.

```
168: // Don't inherit the caller's generic default for a known provider.
```
**Keep** — Explains the non-obvious conditional.

```
179: // OpenAI-compatible: check known providers first, only fall back to
180: // b.cfg.BaseURL for custom/unknown providers — the default
181: // "https://api.deepseek.com" should not override a known provider's
182: // endpoint.
```
**Keep** — Same pattern as in main.go; documents resolution order.

---

## internal/agents/spawn.go

```
13: // spawnTool is the per-agent delegation tool. Its name is the agent's name, so
14: // the calling model sees `scout`, `researcher`, `worker`, … as distinct tools.
15: // Which spawn tools land in an agent's registry *is* the "who may call whom"
16: // rule — an agent literally cannot name a tool it was not given.
```
**Keep** — Explains the security/design pattern (registry as ACL).

```
47: // ExecuteWithEmit runs the subagent to completion in an isolated loop, forwarding
48: // its activity as Subagent* events, and returns only its final message — the one
49: // thing the calling agent's context absorbs.
```
**Keep** — Explains what's returned vs emitted.

```
66: // Each subagent gets its own cancellable context so it can be killed
67: // individually without cancelling the primary turn.
```
**Keep** — Explains the reason for per-subagent contexts.

```
79: // Wrap the subagent's events so they route to its own view. Approval gates
80: // pass through unwrapped so the existing dialog answers them.
```
**Keep** — Explains event routing design.

```
111: // finalMessage extracts the subagent's last assistant text — its report to the
112: // caller. A run error still returns any text produced, flagged as an error.
```
**Prune** — Function name and usage in calling code make this clear.

---

## internal/config/config.go

```
18-27: //go:embed directives
```
**Keep** — Compiler directives.

```
42-52: struct field comments
```
**Keep** — Document public struct fields.

```
76: // Resolution order (lowest to highest precedence):
77: //   CORE.md (shipped) → SYSTEM_PROMPT.md (global) → AGENTS.md (project)
78: // Command-line flags are applied last by the caller.
```
**Keep** — Documents the non-obvious precedence chain.

```
86: // ~/.talos/SYSTEM_PROMPT.md takes precedence over config.toml
87: // but is overridden by project-level AGENTS.md (loaded in main).
```
**Keep** — Same, precedence documentation.

```
118: // Normalize provider aliases to canonical names for auth.json lookups.
```
**Keep** — Explains *why* the normalization is done.

```
131: // fileConfig mirrors the small set of keys allowed in ~/.talos/config.toml.
132: // Pointers/zero-checks let us distinguish "absent" from "explicit zero" so the
133: // file only overrides a default when the key is actually present.
```
**Keep** — Explains the pointer-type design choice.

```
146: // Deprecated: use thinking_level. Kept for backward compat.
```
**Keep** — Deprecation notice.

```
159: // notifyFileConfig mirrors the [notifications] section of config.toml.
```
**Prune** — Type name says this.

```
190: // Backward compat: map old thinking_budget to an equivalent level.
```
**Keep** — Explains backward compat mapping.

```
260: // PermissionDescription returns the prompt text describing the current
261: // permission mode. The embedded default is used unless the user has created
262: // ~/.talos/<mode>.md, which takes precedence.
```
**Keep** — Documents override precedence.

---

## internal/executor/executor.go

```
46: // Serialize permission prompts so parallel tools don't race on the
47: // single frontend dialog / headless stdin path.
```
**Keep** — Explains *why* serialization is needed (race prevention).

```
73: // No one sent a reply and context not done yet. In headless mode
74: // without a renderer this would deadlock, so fail closed.
```
**Keep** — Explains the deadlock risk and fail-closed design.

```
83: // Tools that surface live activity (e.g. subagent spawn tools) receive the
84: // turn's emit function so their child events reach the frontend.
```
**Keep** — Explains the emit function wiring.

---

## internal/fff/index.go

```
1-3: // Package fff provides ... (package doc)
```
**Keep** — Package doc.

```
112: // Base score: prefer shorter paths and files closer to root.
```
**Borderline** — Describes the scoring intent, not just restating code. Borderline keep.

```
237: // pathSource adapts a string slice to sahilm/fuzzy's Source interface.
238: // It normalizes common path separators to spaces so queries like
239: // "prompt build" match "prompt_builder.go".
```
**Keep** — Explains the non-obvious normalization strategy.

```
260: // SearchFiles reads each indexed file's contents and returns lines that fuzzy
261: // match query. This is the naive implementation; a faster version would keep an
262: // inverted index.
```
**Keep** — Notes the performance trade-off (naive vs inverted index).

---

## internal/jsonutil/deterministic.go

```
1: // Package jsonutil provides ... (package doc)
10-13: // MarshalDeterministic returns ... (godoc)
23: // json.Encoder adds a trailing newline; strip it.
```
**Keep** — Explains the trailing newline quirk of `json.Encoder`.

---

## internal/loop/assemble.go

```
13: // Compiled regexes for markdown normalization.
```
**Prune** — The `var fenceRe, sentenceRe` declarations make this clear.

```
15: // fenceRe matches triple-or-more backtick or tilde sequences that are
16: // NOT on their own line (preceded by a non-newline character).
17: // The model sometimes writes "text.```go" instead of "text.\n\n```go",
18: // which glamour refuses to render as a code block.
```
**Keep** — Explains the regex purpose and the model-quirk it works around.

```
21: // sentenceRe matches missing space after sentence-ending punctuation
22: // when the next word starts with an uppercase letter. The model often
23: // concatenates sentences: "components.Let" → "components.Let".
```
**Keep** — Explains the regex purpose and the model-quirk it works around.

```
27: // normalizeMarkdown fixes common formatting mistakes in model-written markdown
28: // so glamour (the TUI renderer) can render them correctly. It is applied to
29: // every text block both during streaming and at final assembly.
```
**Keep** — Explains why normalization exists (glamour compatibility).

```
36: // streamWithRetry wraps streamAndAssemble with exponential-backoff retries for
37: // transient provider errors. It only retries when no text has been emitted to
38: // the UI yet — once streaming has started, a partial response can't be cleanly
39: // replayed, so the error is returned as-is.
```
**Keep** — Explains the non-obvious retry-only-before-streaming strategy.

```
87: // emitThinking flushes the accumulated thinking text as a single block.
88: // Called just before the first text delta so it appears before the response
89: // regardless of whether the provider streams one chunk or many per block.
```
**Keep** — Explains the ordering constraint.

```
125: // Normalize the full text too — catches any edge cases that fell
126: // across chunk boundaries during streaming.
```
**Keep** — Explains why full normalization is needed in addition to streaming.

---

## internal/loop/loop.go

```
44: // MaxIterations caps how many tool-call round-trips a single turn may
45: // make. 0 (the default) means unlimited — the turn runs until the model
46: // stops requesting tools or the context is cancelled.
```
**Keep** — Documents the "0 = unlimited" semantics.

```
51: // SteerFunc is called after tool execution to drain pending steer messages
52: // queued by the TUI while the agent was busy. Each element is a single
53: // user message ([]ContentBlock for text + optional images). Returning nil
54: // means nothing is pending. Called from the loop goroutine, so it must be
55: // thread-safe. Messages are injected before the next LLM call — the same
56: // pattern as pi's "steer" mechanism.
```
**Keep** — Documents the callback contract (thread-safety, nil semantics).

```
64: // Restore aggregate token usage from the transcript's last stats snapshot.
65: // This is a no-op for fresh transcripts (e.g. subagents) and is what makes
66: // `/stats` carry across `talos -c` restarts — the previous session's usage
67: // would otherwise be invisible after a reload. The accumulator is updated
68: // in-place on every turn; restoring here seeds it from disk.
```
**Keep** — Explains the restart-survival design.

```
85: // Close flushes aggregate stats to the transcript before closing.
```
**Prune** — Function name + `l.stats.Flush()` call make this clear.

```
100: // CompactNow forces a compaction of the oldest conversation chunk, optionally
101: // guided by a focus message that tells the summarizer what to preserve. It is
102: // a no-op (returns empty string) when there is nothing to compact. The summary
103: // text is returned for display.
```
**Keep** — Documents the focus parameter and empty-return semantics.

```
115: // SetProvider swaps the LLM provider at runtime. Used by /provider and /model
116: // commands in the TUI/CLI to switch providers mid-session without losing the
117: // conversation transcript.
```
**Keep** — Explains the use case (mid-session switch without transcript loss).

```
122: // SetTranscript swaps in a fresh transcript (e.g. for /new), closing the old one.
123: // Zone A (system+tools) is unchanged, so the provider's prefix cache for the
124: // stable portion stays warm even after starting a new conversation.
125: // Stats are flushed to the old transcript and restored from the new one.
```
**Keep** — Explains the cache-warming design.

```
146: // indexedResult pairs a tool result with its original position in the
147: // assistant's tool_use list so the final tool message can be assembled in
148: // deterministic order even when tools run concurrently.
```
**Keep** — Explains the purpose (deterministic ordering).

```
154: // runToolsParallel executes all tool calls concurrently, preserving the order
155: // required by the LLM. If the batch contains more than one exclusive tool
156: // (edit/write) they are serialised to prevent file-state races.
```
**Keep** — Documents the serialisation policy for exclusive tools.

```
211: // Fill in any gaps left by context cancellation that prevented a goroutine
212: // from assigning its result.
```
**Prune** — The `if results[i].ContentBlock.Type == ""` loop makes this clear.

```
259: // Check for steering messages submitted while the agent was busy
260: // (e.g. during tool execution or streaming). They are injected
261: // into the transcript before the next LLM call, just like pi's
262: // "steer" mechanism — messages queued during a turn become
263: // additional user context on the next reasoning step.
```
**Keep** — Explains the timing of steer injection.

```
298: // Includes ctx.Canceled on user interrupt; the caller distinguishes it.
```
**Keep** — Explains the error semantics (caller must distinguish Canceled).

---

## internal/loop/loop_test.go

```
60: // concurrentExecutor blocks until a latch opens so we can detect overlap.
```
**Keep** — Explains the test helper's purpose.

```
146: // turn 1: model asks for two tools in one assistant message
```
**Prune** — The test steps are clear from the code.

```
153: // turn 2: model finishes with no tools
```
**Prune** — Same.

```
179: // The transcript should hold: user, assistant(tools), tool(2 results), assistant(final).
```
**Prune** — The assertion logic makes this clear.

```
212: // TestNewRestoresStatsFromTranscript guards the `talos -c` behavior that
213: // `/stats` shows the previous session's usage after a reload. The transcript
214: // is created and pre-loaded with a stats record, then `loop.New` should
215: // initialize its accumulator from that record.
```
**Keep** — Explains the regression it guards.

```
222: // Write a real message first; Transcript.Close() deletes the on-disk file
223: // when frozen+summaries are both empty, so a stats-only file wouldn't
224: // survive to be reloaded by the next process.
```
**Keep** — Explains the non-obvious setup detail.

```
236: // Close so the bytes are flushed, then reopen via Load so the in-memory
237: // state matches what `talos -c` would see on startup.
```
**Keep** — Explains why close+reopen is needed.

```
258: // TestNewFreshTranscriptLeavesStatsZero ensures we don't accidentally seed
259: // stats from a brand-new (unloaded) transcript. Subagents rely on this.
```
**Keep** — Explains the subagent dependency.

---

## internal/loop/prompt.go

```
34: // SetContextFn installs a per-turn reminder that is surfaced via
35: // Request.Volatile at request-build time. Used to surface dynamic state
36: // (e.g. "files read this session") without invalidating the cacheable
37: // prefix — Volatile is rendered outside any cache breakpoint, so changes
38: // here never bust the cache and the transcript itself is never mutated.
39: // The function should be cheap and must not mutate the transcript.
```
**Keep** — Documents Volatile cache-invalidation semantics.

```
44: // SetPermissionModeText sets a brief description of the current permission
45: // mode that is surfaced via Request.Volatile so the model knows how its tool
46: // calls will be handled. Since Volatile is rendered outside any cache
47: // breakpoint, it never busts the cacheable prefix.
```
**Keep** — Same Volatile semantics.

```
95: // EstimatedTokens returns a rough token count (1 token ≈ 4 bytes) for the
96: // prompt. This is intentionally approximate; providers bill by their own
97: // tokenizers.
```
**Keep** — Notes the approximation and why.

```
120: // Exclude the message appended this turn so the hash reflects only the stable
121: // cacheable prefix; on a healthy session it must not change between turns.
```
**Keep** — Explains the cache-hash design.

---

## internal/loop/prompt_inject_test.go

```
26: // The transcript's last message must be left untouched — reminders go
27: // into Volatile, not into the message content.
```
**Keep** — Documents the invariant being tested.

```
65: // Unlike the old last-user-message injection, Volatile is populated
66: // regardless of which role produced the last transcript message — the
67: // provider translation layer decides how to merge it.
```
**Keep** — Explains the design difference from the old approach.

```
74: // The assistant message itself must be untouched.
```
**Prune** — The assertion makes this clear.

---

## internal/memory/memory.go

*(no comments remain — already pruned)*

---

## internal/mcp/bridge.go

```
13: // mcpConn is the subset of *ServerConn required by the tool bridge.
14: // Extracted as an interface so tests can use a fake without a real MCP connection.
```
**Keep** — Explains the interface extraction rationale.

---

## internal/mcp/bridge_test.go

```
11: // fakeConn implements mcpConn for testing the bridge.
```
**Prune** — Type name + package makes this clear.

```
117: // Verify that only text content blocks are collected.
```
**Prune** — The assertion below makes this clear.

```
140: // bridgeTools needs a *ServerConn, not a fakeConn.
141: // This is a compile-check: we test the bridge directly above.
142: // For the helper, verify it works with a real ServerConn.
```
**Keep** — Explains the compile-check pattern.

---

## internal/mcp/integration_test.go

```
15: // buildTestMCPServer builds the test MCP server binary once for all tests.
```
**Keep** — Explains the once-per-test-suite setup.

```
52: // Verify echo tool
```
**Prune** — The test assertion below makes this clear.

```
64: // Verify add tool, etc.
```
**Prune** — Same pattern throughout.

```
351: // Test that the config.toml parsing properly loads MCP server config
```
**Prune** — Test function name says this.

```
356: // Write a config with MCP server definitions, etc.
```
**Prune** — Code makes this clear.

```
418: // Verify the test MCP server binary works standalone (smoke test)
422: // Send one initialize request via stdin and check response, etc.
```
**Prune** — Same pattern.

---

## internal/mcp/server_test.go

```
10: // fakeTransport responds to requests with canned responses.
```
**Prune** — Type name says this.

```
42: // Both Command and URL set
52: // Neither set
```
**Prune** — The test data struct makes this clear.

```
76: // Build ServerConn directly with the fake transport.
82: // Manually initialize
142: // Initialize
152: // List tools
159: // Call tool
```
**Prune** — All restate obvious test steps.

```
221: // This tests the Manager with invalid server configs (no command/url).
222: // NewManager should return errors for each invalid config.
```
**Prune** — Test function name says this.

---

## internal/mcp/stdio.go

```
113: // Kill the process if it doesn't exit on stdin close.
```
**Keep** — Explains the reason for a kill fallback.

---

## internal/mcp/stdio_test.go

```
10-11: // fakeStdioServer simulates... (type doc)
47-48: // run starts the fake server... (method doc)
50-65: // testing strategy explanation
70: // Simulate the pending map and drain.
95: // Send two requests out of order
104: // Write response for ID 2 first, then ID 1
```
**Prune** — Most restate obvious test steps. The strategy explanation (50-65) is borderline useful but verbose.

```
119-120: // bidiPipe is a pair...
133: // bidiReader implements io.Reader...
159: // bidiWriter implements io.WriteCloser...
178: // bidiScanner wraps a bidiReader...
```
**Prune** — Type/function names say this.

---

## internal/mcp/testdata/mcp-server/main.go

```
14: // Hard-coded tool catalog.
```
**Prune** — The `var catalog` declaration makes this clear.

```
108: // Method not found
```
**Prune** — The error case below is clear.

---

## internal/models/loader.go

```
12: // Filter returns entries matching all whitespace-separated words in query
13: // (case-insensitive). Matches against the full "provider/id" string.
```
**Keep** — Documents the filter semantics.

---

## internal/notify/dispatch.go

```
1-3: // Package notify... (package doc)
14-16: // Send dispatches... (godoc)
21-22: // send attempts each... (method doc)
```
**Keep** — All are godoc-style function/package docs.

```
24: // 1. Try notify-send — works on most Linux desktops, WSL with X, etc.
29: // 2. macOS via osascript.
36: // 3. Fallback: terminal bell (audible beep / visual flash).
```
**Keep** — Documents the fallback chain strategy.

---

## internal/notify/notify.go

```
9: // Config controls which events trigger desktop notifications.
11: // Enabled is the master switch...
14-15: // NotifyOnPermission sends...
18-19: // NotifyOnTurnEnded sends...
22-23: // NotifyOnError sends...
27-29: // DefaultConfig returns... (godoc)
39-42: // Wrap returns... (godoc)
```
**Keep** — All are godoc-style type/function docs.

---

## internal/notify/notify_test.go

```
10: // Send should never panic regardless of platform conditions.
```
**Keep** — Explains the non-obvious test concern (panic on any platform).

```
67: // This should not deadlock; the notification is async.
```
**Keep** — Explains the async deadlock concern.

---

## internal/pricing/fetch.go

```
25: // FetchLive fetches the live model catalog from pi.dev/models and returns it
26: // in the same compact format as data.json. Any network or parse failure
27: // returns a non-nil error; callers should fall back to cached or embedded data.
```
**Keep** — Documents the fallback contract.

---

## internal/pricing/gen/main.go

```
1-2: // gen regenerates... (package doc for a main)
4-6: // Usage... (usage instructions)
```
**Keep** — Generator main needs usage doc.

---

## internal/pricing/pricing.go

```
1-13: // Package pricing... (package doc + go:generate)
28: //go:embed data.json
31-35: // Price holds... (type doc + field comments)
38: // rawPrice mirrors... (type doc)
45: // Table is a lookup... (type doc)
50: // Default is the built-in table... (var doc)
69-84: // Load returns... (godoc with example)
101: // Refresh the cache in the background if it's stale.
139-147: // Lookup resolves... (godoc)
152-153: // Extract the model-id segment...
171-172: // Cost returns... (godoc)
181: // ContextWindow returns... (godoc)
```
**Keep** — All are godoc/field docs.

---

## internal/pricing/pricing_test.go

```
20: // Cost of 1M in + 1M out should equal input+output per-million rates.
```
**Prune** — The assertion math makes this clear.

```
32: // Provider-namespaced IDs should resolve to the bare model entry.
```
**Prune** — The test input makes this clear.

---

## internal/protocol/event.go

```
27-29: // PermissionRequested is emitted... (godoc)
37-39: // SubagentStarted is emitted... (godoc)
46-51: // SubagentEvent wraps... (godoc)
58-59: // SubagentUsage carries... (godoc)
68-71: // PromptEstimate is emitted... (godoc)
77-79: // UserInput is emitted... (godoc)
84-85: // SubagentFinished is emitted... (godoc)
94-95: // ModelChanged is emitted... (godoc)
102-103: // ThinkingBlock carries... (godoc)
106-108: // EngineSnapshot is sent... (godoc)
```
**Keep** — All are godoc on event types.

---

## internal/protocol/message.go

```
28-31: // ImageBlock carries... (godoc + field comments)
```
**Keep** — Godoc.

---

## internal/protocol/request.go

```
33-34: // PEThinking is emitted... (godoc)
```
**Keep** — Godoc.

---

## internal/provider/anthropic/client.go

```
24-25: // New creates... (godoc)
78-79: // ListModels is not supported... (godoc)
```
**Keep** — Godoc, and explains API limitation.

---

## internal/provider/anthropic/stream.go

```
133: // final; channel will be closed by the defer
```
**Prune** — The empty `case "message_stop":` and `defer close(out)` make this clear.

---

## internal/provider/anthropic/translate.go

```
11: // Config holds Anthropic-specific tunables.
14: // Deprecated: use ThinkingLevel...
28: // Zone A: system block with breakpoint at the end.
37: // Zone A: tools with breakpoint on the last tool.
50-54: // Zone B: conversation history...
67-71: // Zone C: volatile tail...
93: // Anthropic uses "user" for tool results, not "tool".
160-161: // Could be a normal user message or a tool-result carrier...
195-197: // thinkingBudget returns...
```
**Keep** — All document non-obvious Anthropic API translation logic (cache breakpoints, role mapping, zone design).

---

## internal/provider/anthropic/translate_test.go

```
19-23: // TestBuildBodyBreakpointOnToolResultMessage verifies... (test doc)
66: // The earlier assistant message must not carry a breakpoint.
75-79: // TestBuildBodyVolatileMergedIntoLastUserMessage verifies... (test doc)
102-103: // Breakpoint block must come first...
115-117: // TestBuildBodyVolatileMergedIntoToolResultMessage... (test doc)
157-160: // TestBuildBodyVolatileFallsBackWhenLastMessageIsAssistant... (test doc)
188: // The assistant message itself must be unaffected.
195-196: // TestBuildBodyVolatileFallsBackWhenNoMessages... (test doc)
217-218: // TestBuildBodyNoVolatileNoChange... (test doc)
```
**Keep** — All explain the test's purpose and the expected behavior.

---

## internal/provider/known.go

```
10-12: // All is the list of providers...
```
**Keep** — Documents the list and the Anthropic caveat.

---

## internal/provider/openai/translate.go

```
49: // An assistant message with neither content nor tool_calls is invalid
50: // for OpenAI-compatible APIs. Skip it entirely — it contributes nothing.
```
**Keep** — Explains the API requirement (assistant must have content or tool_calls).

```
91: // Set a generous max_tokens; many providers require it for streaming.
```
**Keep** — Explains why max_tokens is set.

```
105-107: // userContent builds the JSON content field... (godoc)
```
**Keep** — Godoc.

---

## internal/provider/routing.go

```
57-60: // PrimaryWithFallback always tries... (godoc)
```
**Keep** — Notes it's a placeholder, which is important context.

---

## internal/provider/thinking.go

```
3-4: // Thinking levels follow pi's convention... (package/doc)
16-20: // modelThinkingCaps maps... (var doc)
22: // ---- off + high only... (section labels for model cap lists)
38, 46, 51, 73, 79, 83, 87, 91, 94: // ---- <caps> ----
```
**Keep** — The section labels are necessary to understand which models have which caps in the map.

```
129-131: // SupportedLevels returns... (godoc)
136: // Suffix match for provider-prefixed model IDs...
145-146: // ClampThinkingLevel snaps... (godoc)
196-200: // MapThinkingToOpenAIEffort returns... (godoc)
```
**Keep** — Godoc + suffix-match explanation.

---

## internal/safety/checkpoint_test.go

```
58: // git log (no --all) must stay clean: still just the initial commit.
```
**Keep** — Explains the expected invariant.

---

## internal/safety/policy.go

```
60-67: // resolve applies the permission mode... (godoc with decision table)
78: // base == Prompt
85: default: // ModeAsk
```
**Keep** — The decision table and inline notes document the security model.
```
**Prune** for line 78, 85 — These are redundant labels before obvious switch branches.

---

## internal/safety/policy_test.go

```
51: // catastrophic always blocks regardless of mode/interactivity.
```
**Prune** — The test assertion makes this clear.

---

## internal/server/client.go

```
27-33: // RunClient connects... (godoc with numbered steps)
```
**Keep** — Godoc.

---

## internal/server/client_test.go

```
19: // Start a server that sends bad handshake.
57: // Major version 1 vs client major 0 → incompatible.
59: // Read client request then close.
103: // version.VERSION is "0.2.0" — so "0.2.0" is compatible.
```
**Prune** — All restate obvious test steps.

---

## internal/server/engine.go

```
15-17: // SlashHandler is called... (godoc)
37: // Live state for the benefit of newly-attached clients.
59-60: // SetNotifyConfig sets... (godoc)
97-98: // SetSlashHandler installs... (godoc)
119-120: // Snapshot returns... (godoc)
133-134: // trackState returns... (godoc)
174-175: // Slash commands... are handled by the server...
181-182: // Broadcast the user's input...
196: // Mark the engine as busy...
204-205: // Intercept permission/plan/merge requests...
216-217: // Wrap with state tracking...
```
**Keep** — Godoc (lines 15-17, 59-60, 97-98, 119-120, 133-134) and inline explanations of event routing design (174-217).

---

## internal/server/server.go

```
52: // New creates a server... (godoc)
83-84: // Start listens... (godoc)
119: // Accept with a short timeout so we can check ctx/idle above.
177-178: // Send a snapshot... (godoc)
252: // ReadPID reads... (godoc)
266-267: // IsAlive tries... (godoc)
277-278: // ListRunning returns... (godoc)
318-319: // Kill sends SIGTERM... (godoc)
```
**Keep** — Godoc + line 119 timeout explanation.

---

## internal/server/server_test.go

```
16: // testEngine is a minimal Engine implementation for testing.
```
**Prune** — Type name says this.

```
87: // Wait for socket to appear.
95: // Connect as a client.
104: // Read hello.
116: // Send input.
157: // Read hello.
160: // Send interrupt.
200: // Read hello.
203: // Emit an event...
208: // Read the event...
268-270: // LoopEngine requires concrete... tested indirectly...
```
**Prune** — All restate obvious test steps.

---

## internal/session/compact.go

```
14-16: // WithFocus returns... (godoc)
39-42: // DropSummarizer replaces... (godoc)
51-52: // LLMSummarizer uses... (godoc)
112-113: // Compactor decides... (godoc)
130-131: // Clamp snaps... (godoc)
162-166: // alignedChunk returns... (godoc)
203: // See if the result exists beyond 'end'.
235-237: // compactChunk summarises... (godoc)
278-284: // MaybeCompact compacts... (godoc)
297-299: // Emergency: above EmergencyThreshold...
315-317: // CompactNow forces... (godoc)
```
**Keep** — All godoc + the emergency explanation.
```
**Prune** for line 203 — The `if ... < end { ... }` check is clear.

---

## internal/session/compact_test.go

```
62-63: // The chunk boundary (size 3) falls...
70-71: // position 2/3 annotations
82-83: // ChunkSize=3 but the tool_call...
90-92: // position annotations
103-104: // The tool call and its result...
167: // force compaction
183: // Close and reload; the summary should survive.
```
**Prune** — Test step comments and position annotations restate the obvious.

---

## internal/session/preview.go

```
21-22: // ListSessionPreviews returns... (godoc)
52-53: // lastUserMessage scans... (godoc)
```
**Keep** — Godoc.

---

## internal/session/transcript.go

```
15-17: // CompactionRecord is appended... (godoc)
26-29: // StatsRecord is a type-tagged line... (godoc)
49-51: // Create returns... (godoc)
56: // lazyOpen is idempotent.
93-94: // Compaction records are distinguished...
108-109: // Drop the summarized chunk...
133-137: // Repair: remove orphaned assistant tool_calls...
174-175: // Summaries returns... (godoc)
184-185: // AppendCompaction writes... (godoc)
213-215: // WriteStats appends... (godoc)
240-241: // RepairCount returns... (godoc)
246-247: // RestoreStats returns... (godoc)
254-255: // DropOldest removes... (godoc)
265-266: // PrependSummary builds... (godoc)
279-293: // repair removes orphaned assistant tool_calls... (full strategy doc)
312: // Assistant message with tool_calls — collect expected IDs.
318: // Consume subsequent tool messages.
333: // Check if all expected IDs were matched.
336: // Keep the whole group...
339-341: // Orphaned — drop the assistant...
363-366: // Remove the file if no messages were ever recorded...
```
**Keep** — All godoc, the strategy documentation for repair, and the step-by-step repair logic comments.
```
**Prune** for lines 312, 318, 333, 336, 339-341 — These restate what the code below does.

---

## internal/skills/skills.go

```
1-9: // Package skills implements... (package doc)
31-34: // Scan reads all... (godoc)
88-98: // RenderListing returns... (godoc with example)
112-119: // extractDescription reads... (godoc with rules)
174: // HR: three or more of ---, ***, ___
```
**Keep** — All godoc. Line 174 documents the regex pattern.

---

## internal/testutil/fakeexecutor.go

```
9-10: // FakeExecutor implements... (godoc)
```
**Keep** — Godoc.

---

## internal/testutil/fakeprovider.go

```
9-12: // FakeProvider implements... (godoc)
```
**Keep** — Godoc.

---

## internal/testutil/faketranscript.go

```
10-11: // NewTestTranscript creates... (godoc)
```
**Keep** — Godoc.

---

## internal/tools/background.go

```
14-15: // BackgroundRegistry owns... (godoc)
52: // Read returns... (godoc)
63: // Kill terminates... (godoc)
90: // ringBuffer is a thread-safe capped buffer... (godoc)
```
**Keep** — Godoc.

---

## internal/tools/bash.go

```
177-179: // cappedWriter keeps the first half... (godoc)
```
**Keep** — Godoc.

---

## internal/tools/bash_test.go

```
23: // A child sleep that outlives the parent shell if not group-killed.
```
**Keep** — Explains the non-obvious test concern (process group cleanup).

---

## internal/tools/edit.go

```
69: // Try whitespace-tolerant match: compare line-by-line ignoring leading whitespace.
```
**Keep** — Explains the fuzzy-match fallback strategy.

```
78: // Preserve the file's indentation structure by re-indenting new_string to match the file block.
```
**Keep** — Explains the re-indentation design.

```
88: // replace_all with fuzzy match: replace every occurrence of the actual block
```
**Keep** — Explains the fuzzy `replace_all` behavior.

```
107-108: // fuzzyFind searches... (godoc)
134-135: // reindent adjusts... (godoc)
```
**Keep** — Godoc.

---

## internal/tools/edit_test.go

```
64: // Go code with tabs for indentation
71: // LLM provides old_string with wrong tab count
97: // Agent uses spaces instead of tabs for indentation
116: // Two blocks with identical content but different indentation
```
**Prune** — All restate what the test data shows.

---

## internal/tools/fff.go

*(already pruned)*

---

## internal/tools/find.go

```
13-15: // findTool unifies... (godoc)
```
**Keep** — Godoc.

---

## internal/tools/glob_test.go

```
66: // Should be empty string (no matches).
106: // a.go should come before z.go.
128: // Pattern "dir/**" should match everything under dir.
```
**Prune** — All restate what the assertion checks.

---

## internal/tools/grep_test.go

```
42: // Should find matches.
76: // May error or return no matches...
77: // Either is acceptable — just shouldn't panic.
96: // Another file without the pattern.
131: // Should include both files (with filenames).
```
**Prune** — Line 76-77 are useful ("shouldn't panic"), the rest are obvious.

---

## internal/tools/ls_test.go

```
95: // a.txt should come before z.txt.
```
**Prune** — Assertion is clear.

---

## internal/tools/read_test.go

```
65: // Should contain line3 onward.
69: // Should NOT contain line1.
89: // Should contain truncated notice.
124: // Should contain line numbers.
```
**Prune** — All restate obvious assertions.

---

## internal/tools/readset.go

```
19-26: // ReadSet tracks... (package/godot)
36-38: // NewReadSet returns... (godoc)
60-63: // LoadReadSet reads... (godoc)
99-100: // SetSavePath enables... (godoc)
107-108: // Save flushes... (godoc)
161-163: // SeenAndFresh reports... (godoc)
185-187: // Update records... (godoc)
197: // Already present: remove from current position.
199: // Reindex everything after the removed element.
266: // order is oldest-first; reverse the last n entries.
```
**Keep** — All godoc.
```
**Prune** for lines 197, 199, 266 — These restate what the code does.

---

## internal/tools/registry.go

```
15-16: // BashConfig carries... (godoc)
59-60: // Add appends... (godoc)
65-67: // Filter returns... (godoc)
```
**Keep** — Godoc.

---

## internal/tools/search.go

```
18-20: // searchTool unifies... (godoc)
```
**Keep** — Godoc.

---

## internal/tools/skill.go

*(already pruned)*

---

## internal/tools/tool.go

```
18-22: // EmittingTool is an optional capability... (godoc)
```
**Keep** — Godoc.

---

## internal/tools/websearch.go

*(no inline comments worth pruning — just struct fields)*

---

## internal/tools/write.go

```
45-48: // If the file already exists, require a fresh read...
```
**Keep** — Explains the gate logic (read-before-write requirement).

---

## internal/tools/write_test.go

```
41: // Verify file was written.
80: // Try to write without reading first.
95: // Verify file was NOT overwritten.
156: // New file write should not require a read.
166: // ReadSet should be updated.
```
**Prune** — All restate obvious assertions.

---

## internal/transport/types.go

```
6: // "input" | "interrupt" | "approve"
13: // "hello" | "event" | "error"
```
**Keep** — Documents the valid enum values.

---

## internal/transport/types_test.go

```
33: // Text should be empty/omitted.
```
**Prune** — The assertion makes this clear.

---

## internal/tui/dialogs/confirm_test.go

```
16: // Press 'y' to approve.
```
**Prune** — The key send makes this clear.

---

## internal/tui/dialogs/login.go

```
13: // LoginDoneMsg is sent... (godoc)
20: // LoginProvider describes... (godoc)
34: // LoginDialog is a two-step dialog... (godoc)
```
**Keep** — Godoc.

---

## internal/tui/dialogs/login_test.go

```
33: // Initial selection is 0.
38: // Press down.
45: // Press down again.
52: // Press down at end — should stay.
59: // Press up.
74: // Enter on first provider should move to key step.
107: // Advance to key entry.
111: // Esc should go back to pick step.
125: // Advance to key entry.
129: // Type a key.
132-134: // Manually update... (workaround explanation)
137: // Submit.
145: // The command should produce a LoginDoneMsg.
168: // Advance to key entry.
172: // Submit with empty key.
207: // Advance to key entry.
223: // Cancel during pick step.
```
**Prune** — All restate obvious test steps. Lines 132-134 are useful (explain a workaround).

---

## internal/tui/dialogs/model_picker.go

```
18: // ModelPickerDoneMsg is sent... (godoc)
25: // modelsLoadedMsg carries... (godoc)
31: // FetchModelsFunc fetches... (godoc)
34: // ModelPickerDialog is a full-screen... (godoc)
```
**Keep** — Godoc.

---

## internal/tui/dialogs/model_picker_test.go

```
36: // Simulate models loaded.
69: // Should have selected the entry matching...
80: // Load models.
90: // Type "claude" to filter.
105: // Load models.
116: // Press down.
123: // Press down again.
130: // Press up.
141: // Load models.
151: // Press enter to select.
254: // Err is a simple error string for test purposes.
```
**Prune** — All restate obvious test steps.

---

## internal/tui/dialogs/session_picker.go

```
15: // SessionEntry carries... (godoc)
22: // SessionPickerDoneMsg is sent... (godoc)
28: // FetchSessionsFunc returns... (godoc)
31: // DeleteSessionFunc removes... (godoc)
39: // SessionPickerDialog is a full-screen... (godoc)
66: // WithDeleteFn sets... (godoc)
72-73: // WithSize pre-seeds... (godoc)
125: // If in delete confirmation mode, only y/n/esc are accepted.
136: // Refresh after deletion.
246: // Delete confirmation or inline instructions.
```
**Keep** — Godoc. Lines 125, 136, 246 are useful for understanding the mode logic.

---

## internal/tui/dialogs/session_picker_test.go

```
59: // Press down (j variant).
66: // Press up (k variant).
145: // Press 'd' to start delete confirmation.
153: // Confirm with 'y'.
226: // Press 'd' — should not enter delete confirmation since deleteFn is nil.
```
**Prune** — All restate obvious test steps.

---

## internal/tui/filepicker.go

```
32: // Cached directory entries to avoid re-walking on every keystroke.
```
**Keep** — Explains the caching rationale.

```
46-48: // shouldSkipDir returns true... (godoc)
51-69: // Version control / Dependencies / Build output / IDE / Home-dir-scale / Talos own data
```
**Keep** — The category labels document the skip-list groups.

```
76-78: // collectDirEntries walks... (godoc)
130-132: // tryLoadFFFIndex loads... (godoc)
147-148: // fuzzyMatchEntries filters... (godoc)
151: // No query: return all entries sorted by depth...
191-192: // filePickerSource adapts... (godoc)
230: // Get directory-aware results via lightweight walk (primary source).
233-234: // If an FFF index already exists... Never BUILD one...
243: // Merge: FFF results first (better relevance), then dir entries.
271-272: // If query is empty, prefer shallow files (depth <= 2)...
284: // Filter and rank
307-309: // filePickerHeight returns... (godoc)
328: / Compute viewport window centered on the selected item.
346: // Header bar
357: // Truncate if too long
379: // Footer showing scroll position...
```
**Keep** — Lines 230, 233-234, 243, 271-272 explain the merge strategy for FFF + directory walk, which is non-obvious. Lines 284, 328, 346, 357, 379 restate the obvious — prune those.

---

## internal/tui/markdown/render.go

```
9: // Renderer renders... (godoc)
15-16: // New creates... (godoc)
19-20: // Use the "dark" built-in style...
26: // Fallback – return a no-op renderer.
35: // Render renders... (godoc)
40: // Glamour adds a trailing newline; strip it.
49-51: // RenderInline renders... (godoc)
53-54: // Glamour wraps in <paragraph>... strip trailing newline.
60: // Rendering a new width requires a new TermRenderer (glamour limitation).
```
**Keep** — Lines 19-20 explain the style choice, 26 handles fallback, 40/53-54/60 explain glamour quirks.
```
**Prune** for lines 9, 15-16, 35, 49 — These are pure godoc restatements of the type/function names.

---

## internal/tui/markdown/render_test.go

```
36: // Strip ANSI to check content is present.
61: // May return empty or just whitespace.
92: // Output should be stripped of trailing newlines...
94: // This test is informational — glamour output format may vary.
```
**Keep** — Line 94 explains that the test is informational (glamour output varies), which is useful context.

---

## internal/tui/model.go

*(This file is large. The following inline comments restate obvious code:)*

```
359: // Active dialog gets first crack at every message.
382: // Recreate the provider so the new key takes effect immediately.
424: // ctrl+v: paste image from clipboard...
438: // ctrl+c: interrupt if busy; otherwise double-press to quit.
475: // Insert mode key handling.
476: // File picker (@) takes priority when active.
600: // Clear completions and submit.
623: // Save to input history (non-slash messages only).
757: // Allow panes to handle their own updates (viewport scrolling, etc.).
971: // ID supplied directly — resume immediately.
979: // No ID: open the session picker dialog.
1084: // No valid @ — deactivate if active.
1098: // Activate / refresh the file picker.
1165: // Find the longest command name for column alignment.
1538: // 1. Check if inside a git repo.
1560: // 2. It is a git repo — check for changes.
1592: // 3. Construct detailed instruction.
1668: // Try Wayland first, then X11.
```
**Prune** — These restate obvious branches/key handlers.

```
84-86, 88-90, 92-94, 96-97, 103-104, 108, 110-111, 113, 115, 117, 119, 121, 123, 125-126, 128-129, 131-134, 136-139, 142-143, 151-152, 155, 157, 161-163, 169, 176-177, 186-187, 200, 207-208, 216, 234, 236-237, 240, 248, 251-252, 254, 257-260, 262, 266, 269, 272-274, 277-281, 285, 292-293, 296-297, 333-334, 339-343, 567-571, 587-588, 649-655, 670, 674, 699, 743-744, 748-749, 766, 784-785, 817-819, 824, 830-835, 848-850, 861-862, 866-870, 876, 885-893, 917-918, 933-934, 1092-1093, 1113-1114, 1143-1146, 1194-1198, 1247, 1263, 1321, 1333, 1342, 1351, 1397-1398, 1424-1428, 1436-1442, 1472-1475, 1483-1485, 1500, 1530-1531, 1610, 1619-1621, 1657-1659
```
**Keep** — Most of these are godoc on types/methods or explain non-obvious design (steer mechanism, textarea pinning, prompt box layout, textarea height bug, etc.).
**Prune** — Lines 359, 382, 424, 438, 475, 476, 600, 623, 757, 971, 979, 1084, 1098, 1165, 1538, 1560, 1592, 1668 are clear restatements.

---

## internal/tui/model_test.go

*(Most test-step comments restate obvious code — prune many.)*

```
23-24: // Chat pane height = terminal height - 3...
37: // A line far longer than the box interior must soft-wrap...
44: // Prompt box = top border + N input rows + bottom border...
49: // No rendered row may exceed the terminal width...
56: // The chat pane must shrink to make room...
62-69: // TestPromptBoxWrapKeystrokeByKeystroke description...
88-89: // Every word typed must still be visible...
113: // Approve with 'y'.
148: // The chat pane should now have 4 entries...
175: // Should have recorded the tool name.
206: // Start a subagent.
221: // Finish it.
326: // Simulate typing "/" to trigger completions.
375: // Simulate submitting text.
377: // The submit path through update is complex; just check history tracking.
413: // Non-home path should stay.
462: // Verify the tool call was replayed...
474: // First esc should set confirm flag.
481: // Second esc should clear the input.
494: // Esc enters normal mode when input is empty.
502: // 'i' enters insert mode.
547: // thinkingLine should produce output.
597: // Should appear in chat.
615: // Withdraw should return the item.
624: // Drain should return empty...
665: // esc with empty input -> normal mode.
```
**Keep** — Lines 62-69 (explains the keystroke-by-keystroke regression), 129-132 (explains `talos -c` behavior), 154-155 (explains empty-history guard), 377 (notes the submit path is complex).
**Prune** — All the rest restate obvious test steps.

---

## internal/tui/panes/chat.go

*(Most are godoc or explain non-obvious reflow/caching design — keep most.)*

```
18-20: // segment is one styled block... (godoc)
28-29: // Rendered markdown cache...
33-34: // Tool-call segments are rendered lazily...
41: // Thinking segments hold...
46: // ChatModel renders... (godoc)
123-125: // wrapText soft-wraps... (godoc)
133-139: // body renders... (godoc with caching strategy)
152: // Cache hit — skip the expensive glamour render.
155-156: // Cache miss... Render now and cache.
168: // Streaming text changes on every delta; there is no cache.
202-203: // markdownSegment pre-renders... (godoc)
222-223: // AppendUserBlocks renders... (godoc)
239-240: // AppendDelta accumulates... (godoc)
256-258: // AppendToolUse adds... (godoc)
260-261: // Group a run of consecutive tool calls...
277: // ToggleToolExpand flips... (godoc)
284: // AppendThinkingBlock adds... (godoc)
298: // ToggleThinkExpand flips... (godoc)
305: // renderThinkingSegment renders... (godoc)
345-347: // renderToolLine styles... (godoc)
369: // renderToolOutput renders... (godoc)
406-408: // FlushStreaming moves... (godoc)
420-423: // PopLastSegment removes... (godoc)
474-477: // syncViewportContent recomputes... (godoc)
```
**Keep** — Most are godoc or explain the caching/reflow design.
**Prune** — Lines 152, 155-156, 168 are borderline — they sit inside obvious `if/else` branches in `body()`.

---

## internal/tui/panes/chat_test.go

```
46: // Should contain [image] marker in the text.
58: // Len should still be 0 — streaming isn't a segment.
153: // Second consecutive tool should have no 'before' separator.
284: // Should not panic.
```
**Prune** — All restate obvious assertions.

---

## internal/tui/panes/format.go

```
16-18: // workdir is resolved once... (godoc)
21-24: // formatToolCall turns... (godoc)
61-62: // genericArgs is the fallback... (godoc)
79-80: // shortPath renders... (godoc)
93-94: // firstLine collapses... (godoc)
104-105: // truncate clips... (godoc)
120-122: // VerticalRule returns... (godoc)
134-136: // statusGlyph maps... (godoc)
149-154: // toolLine composes... (godoc)
169: // Remaining room for the descriptor...
175: // Final guard against any overflow...
179-180: // nameWidth returns... (godoc)
194-196: // windowEntries returns... (godoc)
```
**Keep** — Godoc.
**Prune** — Lines 169, 175 are inline comments in arithmetic — borderline but useful to document the layout calculation.

---

## internal/tui/panes/format_test.go

```
90: // Keys should be sorted.
170: // The longest name is "web_search" (10 chars), capped to 9.
205: // Height 0 is clamped to 1. With 5 items unfocused, shows last 1.
242-243: // Can't test heavily...
```
**Prune** — All restate obvious test outcomes.

---

## internal/tui/panes/subagents.go

```
24-25: // subEntry is one subagent run... (godoc)
35-37: // SubagentsModel renders... (godoc)
65-66: // Count reports... (godoc)
69: // ActiveCount reports... (godoc)
80: // SelectedIsRunning reports... (godoc)
85: // SelectedID returns... (godoc)
93: // SelectedAgent returns... (godoc)
101: // KillConfirmActive reports... (godoc)
104: // KillConfirmStart enters... (godoc)
110: // KillConfirmCancel leaves... (godoc)
139-140: // Click selects... (godoc)
155-157: // HandleEvent folds... (godoc)
183: // Flatten deeper nesting...
216: // Reserve one line for the kill-confirm prompt...
248-249: // selectedRow draws... (godoc)
283: // Nested tool calls... (same package, so we read the container's entries).
297-298: // statsLines renders... (godoc)
329: // humanCount formats... (godoc)
341-342: // clipLines truncates... (godoc)
```
**Keep** — Godoc + non-obvious design notes (flattening, kill-confirm prompt, nested tool calls).

---

## internal/tui/panes/subagents_test.go

```
112: // No panic.
122: // no-op
248: // no panic
257: // second entry
272: // title row
```
**Prune** — All restate obvious test steps.

---

## internal/tui/panes/tools.go

```
33: // ToolsModel renders... (godoc)
65: // expanded viewport: content area minus header...
73-74: // Count reports... (godoc)
121-122: // Click selects... (godoc)
181: // The pane title occupies one row...
203: // titleRow renders... (godoc)
213: // selectedRow draws... (godoc)
```
**Keep** — Godoc. Lines 65, 181 are inline layout comments — borderline but useful.

---

## internal/tui/panes/tools_test.go

```
60: // No panic.
70-71: // Initial cursor at 0. / Press down twice.
80: // Down at end should stay.
94: // Up at top should stay.
122: // No panic.
131: // CursorDown should collapse.
145: // Scroll down while expanded should scroll viewport.
154: // Scroll down without expand should move cursor.
168: // Click on row 2...
173: // Should also toggle expand.
184: // Click on title row (y=0) — noop.
243: // Test the title row via view.
```
**Prune** — All restate obvious test steps.

---

## internal/tui/run.go

```
32-33: // StartEngine launches... (godoc)
107-110: // RunTabs starts... (godoc)
122-123: // Run starts... (godoc)
155-156: // Seed the TUI's cumulative counters...
245: // Only emit TurnEnded on error...
```
**Keep** — Godoc. Lines 155-156 and 245 explain non-obvious design decisions.

---

## internal/tui/styles/styles.go

```
6: // File picker styles
27: // ToolNameStyle is the calm accent...
29-30: // ToolArgStyle dims... (godoc)
32: // ToolHeadingStyle labels... (godoc)
38: // ToolCursorStyle colors... (godoc)
91: // PaneSepStyle colors... (godoc)
94: // ToolPaneTitleStyle is the small header... (godoc)
99: // ToolPaneFocusedTitleStyle highlights... (godoc)
```
**Keep** — The per-style docs explain visual intent. Section headers like "File picker styles" (line 6) are redundant with variable names — prune section headers.

---

## internal/tui/tabs.go

```
17: // TabEventMsg routes... (godoc)
23: // newTabReadyMsg is returned... (godoc)
32: // tabState holds... (godoc)
41-42: // NewTabFunc creates... (godoc)
45: // TabsModel is the root... (godoc)
56-57: // NewTabsModel creates... (godoc)
75-76: // waitForTabEvent is... (godoc)
116-118: // Broadcast spinner ticks to every tab...
145: // Size the new model.
158: // Resize existing tabs now that the tab bar has appeared.
190: // If we're back to one tab, reclaim the tab bar row.
196: // Restart cursor blink for the tab that's now active.
222: // Forward all other messages to the active tab.
279: // Right-align the keybinding hint.
```
**Keep** — Godoc. Lines 116-118 explain the broadcast spinner design.
**Prune** — Lines 145, 158, 190, 196, 222, 279 restate obvious branches/arithmetic.

---

## internal/tui/tabs_test.go

```
12: // stubNewTab returns... (godoc)
```
**Keep** — Godoc.

---

## internal/version/version.go

```
5-6: // VERSION is the current... (godoc)
9-10: // Compatible reports... (godoc)
```
**Keep** — Godoc.
