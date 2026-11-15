//! A turn summary folds into a compact list; each step owns its own disclosure.
use gpui::{App, ClickEvent, Div, FontWeight, SharedString, Window, div, prelude::*, px, rgb};
use std::{collections::HashSet, rc::Rc};
use talos_ui_core::{
    activity::{ActivityGroup, step_label},
    state::{Entry, EntryKind},
};

use crate::{
    INK, MUTED, SECONDARY, disclosure_card, formatted_tool_output, shimmer::ShimmerText,
    tool_detail, workflow,
};

pub fn row(
    group: &ActivityGroup<'_>,
    expanded: bool,
    expanded_steps: &HashSet<String>,
    id: &str,
    on_toggle: impl Fn(&ClickEvent, &mut Window, &mut App) + 'static,
    on_step_toggle: impl Fn(&str, &mut Window, &mut App) + 'static,
) -> Div {
    let on_step_toggle = Rc::new(on_step_toggle);
    let title = group.label();
    let count = group.steps.len();
    disclosure_card()
        .flex()
        .flex_col()
        .debug_selector(|| format!("activity-{id}"))
        .child(
            text_trigger(format!("activity-toggle-{id}"))
                .debug_selector(|| format!("activity-toggle-{id}"))
                .w_full()
                .flex_shrink_0()
                .px(px(12.))
                .py(px(9.))
                .flex()
                .items_center()
                .gap(px(10.))
                .cursor_pointer()
                .on_click(on_toggle)
                .when(group.running(), |header| {
                    header.child(crate::working_indicator())
                })
                .child(
                    div()
                        .debug_selector(|| format!("activity-title-{id}"))
                        .flex_1()
                        .min_w_0()
                        .overflow_hidden()
                        .whitespace_nowrap()
                        .text_ellipsis()
                        .font_family("JetBrains Mono")
                        .text_size(px(12.))
                        .child(shimmer_text(title, format!("activity-sheen-{id}"))),
                )
                .child(div().flex_shrink_0().child(inherited_label(
                    format!("{count} {}", if count == 1 { "step" } else { "steps" }),
                    11.,
                    FontWeight::NORMAL,
                ))),
        )
        .when(expanded, |card| {
            // One border around the whole list keeps the rail continuous, including
            // when individual steps reveal their details.
            let mut steps = div()
                .debug_selector(|| format!("activity-rail-{id}"))
                .w_full()
                .border_l_1()
                .border_color(rgb(crate::BORDER))
                .flex()
                .flex_col();
            for entry in &group.steps {
                let key = format!("{id}:{}", entry.id);
                let expanded = expanded_steps.contains(&key);
                let on_toggle = on_step_toggle.clone();
                let toggle_key = key.clone();
                steps = steps.child(step_row(
                    entry,
                    group.active,
                    expanded,
                    &key,
                    move |_, window, cx| {
                        on_toggle(&toggle_key, window, cx);
                    },
                ));
            }
            card.child(
                div()
                    .debug_selector(|| format!("activity-details-{id}"))
                    .w_full()
                    .flex_shrink_0()
                    .pr(px(12.))
                    .pb(px(12.))
                    .child(steps),
            )
        })
}

fn step_row(
    entry: &Entry,
    active: bool,
    expanded: bool,
    id: &str,
    on_toggle: impl Fn(&ClickEvent, &mut Window, &mut App) + 'static,
) -> Div {
    div()
        .debug_selector(|| format!("activity-step-{id}"))
        .w_full()
        .flex_shrink_0()
        // Match the summary's 12px inset, accounting for the rail's 1px border.
        .pl(px(11.))
        .flex()
        .flex_col()
        .child(
            text_trigger(format!("activity-step-toggle-{id}"))
                .debug_selector(|| format!("activity-step-toggle-{id}"))
                .w_full()
                .min_w_0()
                .flex_shrink_0()
                .py(px(7.))
                .rounded(px(4.))
                .flex()
                .items_center()
                .gap(px(8.))
                .cursor_pointer()
                .on_click(on_toggle)
                .child(
                    div()
                        .debug_selector(|| format!("activity-step-title-{id}"))
                        .flex_1()
                        .min_w_0()
                        .overflow_hidden()
                        .whitespace_nowrap()
                        .text_ellipsis()
                        .font_family("JetBrains Mono")
                        .text_size(px(12.))
                        .child(shimmer_text(
                            step_label(entry, active),
                            format!("activity-step-sheen-{id}"),
                        )),
                ),
        )
        .when(expanded, |row| {
            row.child(
                div()
                    .debug_selector(|| format!("activity-step-details-{id}"))
                    .w_full()
                    .flex_shrink_0()
                    .pt(px(6.))
                    .pb(px(10.))
                    .child(step(entry)),
            )
        })
}

fn text_hover(style: gpui::StyleRefinement) -> gpui::StyleRefinement {
    style.text_color(rgb(INK))
}

fn text_trigger(id: String) -> gpui::Stateful<Div> {
    div()
        .id(SharedString::from(id))
        .text_color(rgb(MUTED))
        .hover(text_hover)
}

fn inherited_label(text: impl Into<SharedString>, size: f32, weight: FontWeight) -> Div {
    div()
        .text_size(px(size))
        .font_weight(weight)
        .child(text.into())
}

fn shimmer_text(text: String, id: String) -> ShimmerText {
    ShimmerText::new(text, id, rgb(MUTED).into(), rgb(INK).into())
}

pub fn step(entry: &Entry) -> Div {
    let mut details = div().w_full().flex_shrink_0().flex().flex_col().gap(px(8.));
    match &entry.kind {
        EntryKind::Thought { text, .. } => {
            details = details.child(
                div()
                    .font_family("JetBrains Mono")
                    .text_size(px(12.))
                    .line_height(px(19.))
                    .text_color(rgb(SECONDARY))
                    .child(workflow::bounded_preview(text)),
            );
        }
        EntryKind::Tool { input, output, .. } => {
            if !input.trim().is_empty() {
                details = details.child(tool_detail("INPUT", input));
            }
            let output = formatted_tool_output(output);
            if !output.trim().is_empty() {
                details = details.child(tool_detail("OUTPUT", &output));
            }
        }
        _ => {}
    }
    details
}

#[cfg(test)]
mod tests {
    use super::*;
    use gpui::{Context, Render, TestAppContext, VisualTestContext, size};
    use talos_ui_core::{
        activity::{TimelineItem, timeline},
        state::ToolState,
    };

    struct ActivityView {
        entries: Vec<Entry>,
        expanded: bool,
        expanded_steps: HashSet<String>,
    }

    impl Render for ActivityView {
        fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
            let items = timeline(&self.entries, false);
            let TimelineItem::Activity(group) = &items[0] else {
                panic!("expected activity")
            };
            div().size_full().child(row(
                group,
                self.expanded,
                &self.expanded_steps,
                "test",
                cx.listener(|this, _, _, cx| {
                    this.expanded = !this.expanded;
                    cx.notify();
                }),
                cx.listener(|this, key: &str, _, cx| {
                    crate::toggle_expanded(&mut this.expanded_steps, key);
                    cx.notify();
                }),
            ))
        }
    }

    fn saved_entries() -> Vec<Entry> {
        vec![
            Entry {
                id: "thought".into(),
                run: Some("r".into()),
                at: String::new(),
                kind: EntryKind::Thought {
                    text: "saved thinking".into(),
                    streaming: false,
                },
            },
            Entry {
                id: "tool".into(),
                run: Some("r".into()),
                at: String::new(),
                kind: EntryKind::Tool {
                    call_id: "tool".into(),
                    name: "read".into(),
                    target: "file.rs".into(),
                    state: ToolState::Completed,
                    input: r#"{"path":"file.rs"}"#.into(),
                    output: "saved output".into(),
                },
            },
        ]
    }

    fn click(cx: &mut VisualTestContext, selector: &'static str) {
        let toggle = cx.debug_bounds(selector).unwrap().center();
        cx.simulate_click(toggle, gpui::Modifiers::none());
        cx.run_until_parked();
    }

    #[test]
    fn activity_summary_is_unboxed_when_folded_or_unfolded() {
        let entries = saved_entries();
        let items = timeline(&entries, false);
        let TimelineItem::Activity(group) = &items[0] else {
            panic!("expected activity")
        };
        for expanded in [false, true] {
            let mut element = row(
                group,
                expanded,
                &HashSet::new(),
                "test",
                |_, _, _| {},
                |_, _, _| {},
            );
            let style = element.style();
            assert!(style.background.is_none());
            assert!(style.border_color.is_none());
            for width in [
                style.border_widths.top,
                style.border_widths.right,
                style.border_widths.bottom,
                style.border_widths.left,
            ] {
                assert!(width.is_none());
            }
        }
    }

    #[test]
    fn trigger_hover_refines_text_only_and_uses_semantic_colors() {
        let mut trigger = text_trigger("hover-test".into());
        assert_eq!(trigger.style().text.color, Some(rgb(MUTED).into()));
        assert!(trigger.style().background.is_none());
        let hover = text_hover(gpui::StyleRefinement::default());
        assert_eq!(hover.text.color, Some(rgb(INK).into()));
        assert!(hover.background.is_none());
        assert!(hover.border_color.is_none());
    }

    #[derive(gpui::IntoElement)]
    struct ColorProbe(Rc<std::cell::Cell<gpui::Hsla>>);

    impl gpui::RenderOnce for ColorProbe {
        fn render(self, window: &mut Window, _: &mut App) -> impl IntoElement {
            self.0.set(window.text_style().color);
            div().size(px(1.))
        }
    }

    struct HoverView {
        color: Rc<std::cell::Cell<gpui::Hsla>>,
    }

    impl Render for HoverView {
        fn render(&mut self, _: &mut Window, _: &mut Context<Self>) -> impl IntoElement {
            div().size_full().child(
                text_trigger("hover-test".into())
                    .debug_selector(|| "hover-test".into())
                    .w_full()
                    .h(px(40.))
                    .child(ColorProbe(self.color.clone()))
                    .child(shimmer_text(
                        "Thought 2 times and read 2 files".into(),
                        "hover-sheen".into(),
                    )),
            )
        }
    }

    #[gpui::test]
    fn pointer_hover_brightens_inherited_text_and_exit_restores_muted(cx: &mut TestAppContext) {
        cx.update(|cx| cx.set_reduce_motion(true));
        let color = Rc::new(std::cell::Cell::new(rgb(MUTED).into()));
        let window = cx.open_window(size(px(800.), px(600.)), |_, _| HoverView {
            color: color.clone(),
        });
        cx.run_until_parked();
        let cx = VisualTestContext::from_window(*window, cx).into_mut();
        let outside = gpui::point(px(400.), px(500.));
        cx.simulate_mouse_move(outside, None, gpui::Modifiers::none());
        cx.run_until_parked();
        assert_eq!(color.get(), rgb(MUTED).into());
        let inside = cx.debug_bounds("hover-test").unwrap().center();
        cx.simulate_mouse_move(inside, None, gpui::Modifiers::none());
        cx.run_until_parked();
        assert_eq!(color.get(), rgb(INK).into());
        cx.simulate_mouse_move(outside, None, gpui::Modifiers::none());
        cx.run_until_parked();
        assert_eq!(color.get(), rgb(MUTED).into());
    }

    fn assert_continuous_rail(cx: &mut VisualTestContext) {
        let rail = cx.debug_bounds("activity-rail-test").unwrap();
        let thought = cx.debug_bounds("activity-step-test:thought").unwrap();
        let tool = cx.debug_bounds("activity-step-test:tool").unwrap();
        assert_eq!(thought.origin.y, rail.origin.y);
        assert_eq!(thought.bottom(), tool.origin.y);
        assert_eq!(tool.bottom(), rail.bottom());
        let text_x = cx.debug_bounds("activity-title-test").unwrap().origin.x;
        for selector in [
            "activity-step-title-test:thought",
            "activity-step-title-test:tool",
            "activity-step-details-test:thought",
            "activity-step-details-test:tool",
        ] {
            if let Some(bounds) = cx.debug_bounds(selector) {
                assert_eq!(bounds.origin.x, text_x, "{selector} must not be indented");
            }
        }
    }

    #[gpui::test]
    fn unfolding_shows_a_compact_list_and_each_step_expands_independently(cx: &mut TestAppContext) {
        let window = cx.open_window(size(px(800.), px(600.)), |_, _| ActivityView {
            entries: saved_entries(),
            expanded: false,
            expanded_steps: HashSet::new(),
        });
        cx.run_until_parked();
        let cx = VisualTestContext::from_window(*window, cx).into_mut();
        let collapsed = cx.debug_bounds("activity-test").unwrap();
        assert!(collapsed.size.height >= px(30.) && collapsed.size.height <= px(45.));
        assert!(cx.debug_bounds("activity-details-test").is_none());

        click(cx, "activity-toggle-test");
        assert_continuous_rail(cx);
        for selector in ["activity-step-test:thought", "activity-step-test:tool"] {
            let height = cx.debug_bounds(selector).unwrap().size.height;
            assert!(
                height >= px(24.) && height <= px(40.),
                "list row height: {height:?}"
            );
        }
        assert!(
            cx.debug_bounds("activity-step-details-test:thought")
                .is_none()
        );
        assert!(cx.debug_bounds("activity-step-details-test:tool").is_none());
        let list_height = cx.debug_bounds("activity-test").unwrap().size.height;
        assert!(list_height < collapsed.size.height + px(100.));

        click(cx, "activity-step-toggle-test:tool");
        assert!(cx.debug_bounds("activity-details-test").is_some());
        assert!(cx.debug_bounds("activity-step-details-test:tool").is_some());
        assert!(
            cx.debug_bounds("activity-step-details-test:thought")
                .is_none()
        );
        assert!(cx.debug_bounds("activity-test").unwrap().size.height > list_height + px(50.));
        assert_continuous_rail(cx);

        click(cx, "activity-step-toggle-test:thought");
        assert!(
            cx.debug_bounds("activity-step-details-test:thought")
                .is_some()
        );
        assert!(cx.debug_bounds("activity-step-details-test:tool").is_some());
        assert_continuous_rail(cx);
        click(cx, "activity-step-toggle-test:tool");
        assert!(cx.debug_bounds("activity-step-details-test:tool").is_none());
        assert!(
            cx.debug_bounds("activity-step-details-test:thought")
                .is_some()
        );

        click(cx, "activity-toggle-test");
        assert!(cx.debug_bounds("activity-details-test").is_none());
        assert_eq!(
            cx.debug_bounds("activity-test").unwrap().size.height,
            collapsed.size.height
        );
    }

    #[gpui::test]
    fn reopening_starts_both_disclosure_levels_collapsed(cx: &mut TestAppContext) {
        // A previously expanded window must not determine a fresh view's state.
        let window = cx.open_window(size(px(800.), px(600.)), |_, _| ActivityView {
            entries: saved_entries(),
            expanded: true,
            expanded_steps: HashSet::from(["test:thought".into(), "test:tool".into()]),
        });
        cx.run_until_parked();
        let cx = VisualTestContext::from_window(*window, cx).into_mut();
        assert!(cx.debug_bounds("activity-step-details-test:tool").is_some());
        let reopened = cx.open_window(size(px(800.), px(600.)), |_, _| ActivityView {
            entries: saved_entries(),
            expanded: false,
            expanded_steps: HashSet::new(),
        });
        cx.run_until_parked();
        let cx = VisualTestContext::from_window(*reopened, cx).into_mut();
        assert!(cx.debug_bounds("activity-details-test").is_none());
        click(cx, "activity-toggle-test");
        assert!(
            cx.debug_bounds("activity-step-details-test:thought")
                .is_none()
        );
        assert!(cx.debug_bounds("activity-step-details-test:tool").is_none());
    }

    #[gpui::test]
    fn long_step_titles_do_not_wrap_or_expand_details_in_a_narrow_window(cx: &mut TestAppContext) {
        let mut entries = saved_entries();
        if let EntryKind::Tool { input, .. } = &mut entries[1].kind {
            *input = serde_json::json!({"path": "long-path/".repeat(100)}).to_string();
        }
        let window = cx.open_window(size(px(320.), px(600.)), |_, _| ActivityView {
            entries,
            expanded: true,
            expanded_steps: HashSet::new(),
        });
        cx.run_until_parked();
        let cx = VisualTestContext::from_window(*window, cx).into_mut();
        assert!(
            cx.debug_bounds("activity-step-test:tool")
                .unwrap()
                .size
                .height
                <= px(40.)
        );
        assert!(cx.debug_bounds("activity-step-details-test:tool").is_none());
    }
}
