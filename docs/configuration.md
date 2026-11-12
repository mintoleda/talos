# configuration

all configuration lives under `~/.talos/`. no environment variables.

resolution order (lowest → highest precedence): defaults → `config.toml` → CLI flags.

## api keys (`~/.talos/auth.json`)

managed via `/login` in the tui, or edit directly:

```json
{
  "deepseek":  {"type": "api_key", "key": "sk-..."},
  "openai":    {"type": "api_key", "key": "sk-..."},
  "anthropic": {"type": "api_key", "key": "sk-ant-..."}
}
```

## config file (`~/.talos/config.toml`)

```toml
base_url               = ""
model                  = "gpt-6.1-sol"
provider               = "openai-codex"
permission_mode        = "auto"
bash_timeout_seconds   = 120
bash_max_timeout_seconds = 600
bash_max_output        = 30720
thinking_level         = "off"
search_url             = ""
```

The provider/model above is an explicit example, not an implicit default. New
homes have no provider, model, or endpoint selected. Configure them here, use
`--provider` and `--model`, or select a model through ACP before prompting.
Registered providers resolve their own endpoint when `base_url` is empty.
Existing explicit configuration and saved-session model selections are preserved.

`permission_mode` options: `plan`, `prompt`, `auto`, `headless`.

## system prompt resolution

resolved in this order (highest precedence first):

1. `AGENTS.md` in the project root — per-project instructions
2. `~/.talos/SYSTEM_PROMPT.md` — global overrides (create this file)
3. `internal/config/CORE.md` — ships with the binary; agent identity and tone

## skills and subagents

- `~/.talos/skills/` — user-installed skills (markdown-defined tool loadouts)
- `~/.talos/subagents/` — subagent definitions (see [subagents.md](subagents.md))
- `<repo>/.talos/subagents/` — project-local subagent definitions (committed to repo, override global by name)

## advanced config

```toml
max_agent_depth = 3   # max subagent nesting depth
```
