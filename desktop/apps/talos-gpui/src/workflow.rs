//! Small UI-only workflow helpers, independent of GPUI and the provider runtime.

#[derive(Clone, Debug)]
pub struct PendingPrompt {
    pub text: String,
    pub images: Vec<String>,
    pub model: Option<String>,
}

pub fn prompt_title(text: &str) -> String {
    let title: String = text
        .lines()
        .next()
        .unwrap_or_default()
        .chars()
        .take(72)
        .collect();
    if title.trim().is_empty() {
        "New session".into()
    } else {
        title
    }
}

/// A model's inference provider, not its ACP agent backend (e.g. Talos).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ModelProvider {
    pub key: String,
    pub name: String,
    pub icon: &'static str,
}

pub fn model_provider(model_id: &str, backend: &str) -> ModelProvider {
    let prefix = model_id.split_once('/').map(|(provider, _)| provider);
    let provider = prefix
        .unwrap_or(if backend == "claude" {
            "anthropic"
        } else {
            "other"
        })
        .to_ascii_lowercase();
    let (key, name, icon) = match provider.as_str() {
        "openai" | "openai-codex" | "codex" => ("openai-codex", "Codex", "icons/openai.svg"),
        "anthropic" | "claude" | "claude-code" => ("anthropic", "Anthropic", "icons/anthropic.svg"),
        "google" | "gemini" => ("google", "Google", "icons/gemini.svg"),
        "deepseek" => ("deepseek", "DeepSeek", "icons/deepseek.svg"),
        "mistral" => ("mistral", "Mistral", "icons/mistral.svg"),
        "talos" | "other" | "" => ("other", "Other models", "icons/sparkles.svg"),
        _ => (provider.as_str(), provider.as_str(), "icons/sparkles.svg"),
    };
    ModelProvider {
        key: key.into(),
        name: name.into(),
        icon,
    }
}

/// Leave room for the search header, title bar, and composer on short windows.
pub fn model_picker_body_height(viewport_height: f32, new_conversation: bool) -> f32 {
    let available = if new_conversation {
        viewport_height * 0.5 - 90.
    } else {
        viewport_height - 220.
    };
    available.clamp(80., 286.)
}

/// Search both human labels and provider-qualified IDs; every query word must match.
pub fn matches_model_query(query: &str, name: &str, id: &str) -> bool {
    let searchable = format!("{name} {id}").to_lowercase();
    query
        .split_whitespace()
        .all(|word| searchable.contains(&word.to_lowercase()))
}

pub fn bounded_preview(text: &str) -> String {
    let mut chars = text.chars();
    let preview: String = chars.by_ref().take(8000).collect();
    if chars.next().is_some() {
        format!("{preview}\n… Output preview truncated.")
    } else {
        preview
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn title_handles_empty_and_unicode() {
        assert_eq!(prompt_title(""), "New session");
        assert_eq!(prompt_title("First line\nrest"), "First line");
        assert_eq!(prompt_title(&"界".repeat(100)).chars().count(), 72);
    }
    #[test]
    fn inference_providers_are_distinct_from_agent_backends() {
        let openai = model_provider("openai/gpt-sol", "talos");
        let codex = model_provider("openai-codex/gpt-sol", "talos");
        assert_eq!(openai, codex);
        assert_eq!(codex.name, "Codex");
        assert_eq!(codex.icon, "icons/openai.svg");
        assert_eq!(model_provider("codex/gpt-sol", "talos"), codex);
        assert_eq!(
            model_provider("deepseek/deepseek-chat", "talos").name,
            "DeepSeek"
        );
        assert_eq!(model_provider("claude-sonnet", "claude").key, "anthropic");
        assert_eq!(
            model_provider("openrouter/anthropic/sonnet", "talos").key,
            "openrouter"
        );
        assert_eq!(model_provider("talos/unknown", "talos").key, "other");
        assert_eq!(model_provider("unqualified", "talos").key, "other");
    }

    #[test]
    fn model_picker_shrinks_for_short_windows() {
        assert_eq!(model_picker_body_height(900., true), 286.);
        assert_eq!(model_picker_body_height(480., true), 150.);
        assert_eq!(model_picker_body_height(480., false), 260.);
        assert_eq!(model_picker_body_height(200., true), 80.);
    }

    #[test]
    fn model_search_matches_labels_ids_and_multiple_words() {
        assert!(matches_model_query("", "Sol", "openai/gpt-sol"));
        assert!(matches_model_query("  GPT SOL ", "Sol", "openai/gpt-sol"));
        assert!(matches_model_query(
            "openai Sol",
            "GPT Sol",
            "openai/gpt-sol"
        ));
        assert!(matches_model_query("界", "模型界", "vendor/model"));
        assert!(!matches_model_query(
            "sol claude",
            "GPT Sol",
            "openai/gpt-sol"
        ));
        assert!(!matches_model_query("missing", "GPT Sol", "openai/gpt-sol"));
    }

    #[test]
    fn preview_is_utf8_safe_and_bounded() {
        assert_eq!(bounded_preview("small"), "small");
        let preview = bounded_preview(&"界".repeat(9000));
        assert!(preview.starts_with(&"界".repeat(8000)));
        assert!(preview.ends_with("truncated."));
    }
}
