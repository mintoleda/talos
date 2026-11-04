# Provider contract decisions

- Provider identity is explicit and durable. A session created with Talos remains a Talos session; a crash does not silently move it to Claude Code.
- Talos is the default for new sessions when available. Claude Code is the only secondary provider and is never an automatic replacement.
- Talos and Claude Code share the ACP process/transport implementation while retaining independent provider IDs and negotiated capabilities.
- Domain/UI behavior is capability-driven: streaming, resume, permissions, images, models, modes, configuration options, session list/close, and commands are negotiated or adapter-reported.
- Paseo persists its own projection and native session handle. The provider runtime remains authoritative for its canonical model transcript.
- Cancellation is two-phase: protocol cancel/abort, then bounded process-tree termination only if the runtime does not settle.
- Permission failures, missing UI routes, disconnects, and timeouts fail closed.
- Provider stdout is protocol-only. Stderr is bounded diagnostic data and never parsed as assistant output.
