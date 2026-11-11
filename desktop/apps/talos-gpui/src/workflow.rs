//! Small UI-only workflow helpers, independent of GPUI and the provider runtime.

#[derive(Clone, Debug)]
pub struct PendingPrompt {
    pub text: String,
    pub images: Vec<String>,
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
        "New conversation".into()
    } else {
        title
    }
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
        assert_eq!(prompt_title(""), "New conversation");
        assert_eq!(prompt_title("First line\nrest"), "First line");
        assert_eq!(prompt_title(&"界".repeat(100)).chars().count(), 72);
    }
    #[test]
    fn preview_is_utf8_safe_and_bounded() {
        assert_eq!(bounded_preview("small"), "small");
        let preview = bounded_preview(&"界".repeat(9000));
        assert!(preview.starts_with(&"界".repeat(8000)));
        assert!(preview.ends_with("truncated."));
    }
}
