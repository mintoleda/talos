//! A one-shot sheen through glyph colors, never a painted background.
use std::{ops::Range, time::Duration};

use gpui::{
    Animation, AnimationExt, App, HighlightStyle, Hsla, IntoElement, RenderOnce, SharedString,
    StyledText, Window,
};

#[derive(IntoElement)]
pub struct ShimmerText {
    text: SharedString,
    id: SharedString,
    muted: Hsla,
    foreground: Hsla,
}

impl ShimmerText {
    pub fn new(text: String, id: String, muted: Hsla, foreground: Hsla) -> Self {
        Self {
            text: text.into(),
            id: id.into(),
            muted,
            foreground,
        }
    }
}

impl RenderOnce for ShimmerText {
    fn render(self, window: &mut Window, cx: &mut App) -> impl IntoElement {
        // The trigger owns semantic text colors; its inherited hover color is
        // also the animation switch. Removing the animation on pointer exit
        // resets it for the next hover and leaves no idle frame loop behind.
        let hovered = window.text_style().color == self.foreground;
        if !hovered || cx.reduce_motion() || self.muted == self.foreground {
            return StyledText::new(self.text).into_any_element();
        }
        let text = self.text.clone();
        StyledText::new(self.text)
            .with_animation(
                self.id,
                Animation::new(Duration::from_millis(650)),
                move |element, progress| {
                    element.with_highlights(highlights(
                        &text,
                        self.muted,
                        self.foreground,
                        progress,
                    ))
                },
            )
            .into_any_element()
    }
}

// Character positions are normalized visual coordinates, not exact counters.
#[allow(clippy::cast_precision_loss)]
fn highlights(
    text: &str,
    muted: Hsla,
    foreground: Hsla,
    progress: f32,
) -> Vec<(Range<usize>, HighlightStyle)> {
    let count = text.chars().count();
    let denominator = count.saturating_sub(1).max(1) as f32;
    text.char_indices()
        .enumerate()
        .map(|(index, (start, character))| {
            let amount = sheen(index as f32 / denominator, progress);
            let color = if amount >= 1.0 {
                foreground
            } else {
                muted.blend(foreground.opacity(amount))
            };
            (
                start..start + character.len_utf8(),
                HighlightStyle {
                    color: Some(color),
                    ..HighlightStyle::default()
                },
            )
        })
        .collect()
}

fn sheen(position: f32, progress: f32) -> f32 {
    // A soft moving glint rides over a gradual transition to foreground.
    // At rest the text is exactly muted; at the end it is exactly foreground.
    let settle = (progress * 1.35).clamp(0.0, 1.0);
    let settle = settle * settle * (3.0 - 2.0 * settle);
    let center = progress * 1.4 - 0.2;
    let glint = (1.0 - (position - center).abs() / 0.18).max(0.0).powi(2);
    settle + (1.0 - settle) * glint * 0.8
}

#[cfg(test)]
mod tests {
    use super::*;
    use gpui::rgb;

    #[test]
    fn sheen_moves_through_text_then_settles_without_an_idle_loop() {
        assert_eq!(sheen(0.0, 0.0), 0.0);
        assert_eq!(sheen(1.0, 0.0), 0.0);
        assert!(sheen(0.22, 0.3) > sheen(0.8, 0.3));
        for position in [0.0, 0.25, 0.5, 0.75, 1.0] {
            assert_eq!(sheen(position, 1.0), 1.0);
            for progress in [0.0, 0.1, 0.3, 0.7, 1.0] {
                assert!((0.0..=1.0).contains(&sheen(position, progress)));
            }
        }
    }

    #[test]
    fn highlight_ranges_are_utf8_safe_and_use_the_supplied_theme_colors() {
        let text = "Thought → Read café/文件.rs";
        // Exercise both dark and light palettes without assuming white is the
        // destination color or gray is a hardcoded RGB value in the effect.
        for (muted, foreground) in [
            (rgb(0x89958D).into(), rgb(0xEEF2EF).into()),
            (rgb(0x888888).into(), rgb(0x222222).into()),
        ] {
            for (progress, expected) in [(0.0, muted), (1.0, foreground)] {
                let runs = highlights(text, muted, foreground, progress);
                assert_eq!(runs.len(), text.chars().count());
                assert_eq!(runs.first().unwrap().0.start, 0);
                assert_eq!(runs.last().unwrap().0.end, text.len());
                for (range, style) in runs {
                    assert!(text.is_char_boundary(range.start));
                    assert!(text.is_char_boundary(range.end));
                    assert_eq!(style.color, Some(expected));
                }
            }
        }
        assert_eq!(
            highlights("", rgb(0).into(), rgb(0xFFFFFF).into(), 0.5),
            Vec::new()
        );
    }
}
