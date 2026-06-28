# subagents

subagents are small, single-purpose agents the primary agent can delegate to as ordinary tools. each is a markdown file with a yaml frontmatter header.

talos ships three built-in subagents (`scout`, `researcher`, `worker`) that inherit your configured model and provider. you can override them or add your own by dropping `.md` files into `~/.talos/subagents/`.

## definition format

```markdown
---
name: scout
description: Read-only code scout — locates files, symbols, and call sites.
tools: [read, search, find, ls]
subagents: []
model: ""
provider: ""
thinking: ""
---

System prompt goes here.
```

### fields

| field | description |
|---|---|
| `name` | unique name; becomes the tool name the primary agent calls |
| `description` | one-line summary shown to the calling agent |
| `tools` | list of allowed tool names; `["*"]` means all tools |
| `subagents` | names of agents this agent may itself spawn |
| `model` | model override; empty inherits from the primary agent. also accepts `provider/model` shorthand (e.g. `anthropic/claude-haiku-4-5`) |
| `provider` | provider override; empty inherits from the primary agent |
| `thinking` | thinking level override (`off`, `low`, `medium`, `high`, `xhigh`); empty inherits |

### provider/model shorthand

instead of separate `provider` and `model` fields, you can write both in one line:

```yaml
model: anthropic/claude-haiku-4-5
```

the api key for the specified provider is resolved from `~/.talos/auth.json`.

## load order

definitions are loaded in this order, later entries override earlier ones by name:

1. built-in defaults (embedded in binary — `scout`, `researcher`, `worker` with inherited model)
2. `~/.talos/subagents/*.md` (your global definitions)
3. `<repo>/.talos/subagents/*.md` (project-local, committed to the repo)

## built-in subagents

### scout

read-only navigator. tools: `read`, `search`, `find`, `ls`. no write access, no shell.

use for: locating files, finding symbol definitions, tracing call sites.

### researcher

web researcher. tools: `web_search`, `web_fetch`, `read`.

use for: current external information, documentation lookups, anything that needs a live web search.

### worker

general-purpose worker. tools: `read`, `write`, `edit`, `bash`, `search`, `find`, `ls`, and background bash tools. can spawn `scout` and `researcher`.

use for: delegating a focused, self-contained implementation task.

## example: custom subagent

```markdown
---
name: db-reviewer
description: Reviews sql migrations for safety and correctness against our schema conventions.
tools: [read, search, find, ls]
subagents: []
model: anthropic/claude-opus-4-8
thinking: high
---

You are a database migration reviewer. You have read-only access to the codebase.

Your job: given a migration file path, read it and the surrounding schema, then
report any safety issues, missing indexes, or convention violations.

Report format:
- one-line verdict (safe / needs changes / blocking issue)
- bulleted list of findings with file:line references
- suggested fixes for anything blocking
```
