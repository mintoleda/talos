use super::*;
use gpui::{
    Context, PlatformInput, Render, ScrollDelta, ScrollWheelEvent, TestAppContext, point, size,
};

struct View {
    transcript: Transcript,
    builds: Rc<Cell<usize>>,
    composer_height: f32,
    expanded: bool,
}

impl View {
    fn set_rows(&mut self, count: usize) {
        self.transcript.sync(&entries(count), false, false, None);
    }
}

impl Render for View {
    fn render(&mut self, _: &mut Window, _: &mut Context<Self>) -> impl IntoElement {
        let builds = self.builds.clone();
        let expanded = self.expanded;
        div()
            .size_full()
            .flex()
            .flex_col()
            .child(self.transcript.viewport("chat", move |index, _, _| {
                builds.set(builds.get() + 1);
                div()
                    .w_full()
                    .h(px(
                        if expanded { 200. } else { 100. } + [0., 10., 20.][index % 3]
                    ))
                    .child(format!("row {index}"))
                    .into_any_element()
            }))
            .child(div().h(px(self.composer_height)).flex_shrink_0())
    }
}

fn entries(count: usize) -> Vec<Entry> {
    (0..count)
        .map(|index| Entry {
            id: index.to_string(),
            run: None,
            at: String::new(),
            kind: EntryKind::User {
                text: format!("row {index}"),
            },
        })
        .collect()
}

type Opened = (
    gpui::WindowHandle<View>,
    ListState,
    Rc<Cell<bool>>,
    Rc<Cell<usize>>,
);

fn open(cx: &mut TestAppContext, count: usize) -> Opened {
    let mut transcript = Transcript::default();
    transcript.sync(&entries(count), false, false, None);
    let state = transcript.state.clone();
    let following = transcript.following.clone();
    let builds = Rc::new(Cell::new(0));
    let window = cx.open_window(size(px(800.), px(600.)), |_, _| View {
        transcript,
        builds: builds.clone(),
        composer_height: 140.,
        expanded: false,
    });
    cx.run_until_parked();
    (window, state, following, builds)
}

fn wheel(window: &gpui::WindowHandle<View>, cx: &mut TestAppContext, delta: ScrollDelta, y: f32) {
    window
        .update(cx, |_, window, cx| {
            window.dispatch_event(
                PlatformInput::ScrollWheel(ScrollWheelEvent {
                    position: point(px(400.), px(y)),
                    delta,
                    ..Default::default()
                }),
                cx,
            );
        })
        .unwrap();
}

fn pixels(y: f32) -> ScrollDelta {
    ScrollDelta::Pixels(point(px(0.), px(y)))
}
fn at_bottom(state: &ListState) -> bool {
    state.max_offset_for_scrollbar().y + state.scroll_px_offset_for_scrollbar().y == px(0.)
}

#[gpui::test]
fn long_history_only_builds_visible_rows_on_open_and_scroll(cx: &mut TestAppContext) {
    let (window, state, _, builds) = open(cx, 2_000);
    assert!(at_bottom(&state));
    assert!(builds.get() < 30, "built {} rows on open", builds.get());
    builds.set(0);
    wheel(&window, cx, pixels(120.), 200.);
    cx.run_until_parked();
    assert!(
        builds.get() > 0 && builds.get() < 30,
        "built {} rows on scroll",
        builds.get()
    );
    // Walk past the initially measured overdraw into previously unseen history.
    for _ in 0..20 {
        wheel(&window, cx, pixels(120.), 200.);
        cx.run_until_parked();
    }
    assert!(state.logical_scroll_top().item_ix < 1_985);
    wheel(&window, cx, pixels(-100_000.), 200.);
    cx.run_until_parked();
    assert!(at_bottom(&state));
}

#[gpui::test]
fn tiny_upward_scroll_pauses_until_exact_bottom_and_growth_keeps_anchor(cx: &mut TestAppContext) {
    let (window, state, following, _) = open(cx, 12);
    wheel(&window, cx, pixels(0.25), 200.);
    cx.run_until_parked();
    assert!(!following.get());
    assert!(!at_bottom(&state));
    // A downward gesture that stops 0.125px short must not re-enable follow.
    wheel(&window, cx, pixels(-0.125), 200.);
    cx.run_until_parked();
    assert!(!following.get());
    assert_eq!(
        state.max_offset_for_scrollbar().y + state.scroll_px_offset_for_scrollbar().y,
        px(0.125)
    );
    let anchor = state.logical_scroll_top();
    window
        .update(cx, |view, _, cx| {
            view.set_rows(14);
            cx.notify();
        })
        .unwrap();
    cx.run_until_parked();
    assert_eq!(state.logical_scroll_top().item_ix, anchor.item_ix);
    assert_eq!(
        state.logical_scroll_top().offset_in_item,
        anchor.offset_in_item
    );
    wheel(&window, cx, pixels(-10_000.), 200.);
    cx.run_until_parked();
    assert!(following.get());
    assert!(at_bottom(&state));
    window
        .update(cx, |view, _, cx| {
            view.set_rows(16);
            cx.notify();
        })
        .unwrap();
    cx.run_until_parked();
    assert!(at_bottom(&state));
}

#[gpui::test]
fn upward_intent_wins_before_bottom_frame_is_painted(cx: &mut TestAppContext) {
    let (window, state, following, _) = open(cx, 12);
    wheel(&window, cx, pixels(-10_000.), 200.);
    wheel(&window, cx, pixels(0.1), 200.);
    cx.run_until_parked();
    assert!(!following.get());
    let anchor = state.logical_scroll_top();
    window
        .update(cx, |view, _, cx| {
            view.set_rows(14);
            cx.notify();
        })
        .unwrap();
    cx.run_until_parked();
    assert_eq!(state.logical_scroll_top().item_ix, anchor.item_ix);
    assert_eq!(
        state.logical_scroll_top().offset_in_item,
        anchor.offset_in_item
    );
}

#[gpui::test]
fn line_wheels_follow_but_composer_and_zero_delta_do_not(cx: &mut TestAppContext) {
    let (window, _, following, _) = open(cx, 12);
    wheel(&window, cx, pixels(120.), 550.);
    wheel(&window, cx, pixels(0.), 200.);
    cx.run_until_parked();
    assert!(following.get());
    wheel(&window, cx, ScrollDelta::Lines(point(0., 1.)), 200.);
    cx.run_until_parked();
    assert!(!following.get());
    wheel(&window, cx, ScrollDelta::Lines(point(0., -100.)), 200.);
    cx.run_until_parked();
    assert!(following.get());
}

#[gpui::test]
fn short_to_long_growth_resize_and_disclosure_preserve_follow_choice(cx: &mut TestAppContext) {
    let (window, state, following, _) = open(cx, 1);
    assert!(at_bottom(&state));
    window
        .update(cx, |view, _, cx| {
            view.set_rows(12);
            view.composer_height = 220.;
            cx.notify();
        })
        .unwrap();
    cx.run_until_parked();
    assert!(at_bottom(&state));
    window
        .update(cx, |view, _, cx| {
            view.expanded = true;
            view.transcript.state.remeasure();
            cx.notify();
        })
        .unwrap();
    cx.run_until_parked();
    assert!(at_bottom(&state));
    wheel(&window, cx, pixels(1.), 200.);
    cx.run_until_parked();
    let anchor = state.logical_scroll_top();
    window
        .update(cx, |view, _, cx| {
            view.composer_height = 300.;
            cx.notify();
        })
        .unwrap();
    cx.run_until_parked();
    assert!(!following.get());
    assert_eq!(state.logical_scroll_top().item_ix, anchor.item_ix);
    assert_eq!(
        state.logical_scroll_top().offset_in_item,
        anchor.offset_in_item
    );
}

#[gpui::test]
fn downward_input_uses_old_extent_when_content_arrives_before_next_frame(cx: &mut TestAppContext) {
    let (window, state, following, _) = open(cx, 12);
    wheel(&window, cx, pixels(120.), 200.);
    cx.run_until_parked();
    assert!(!following.get());
    wheel(&window, cx, pixels(-10_000.), 200.);
    window
        .update(cx, |view, _, cx| {
            view.set_rows(14);
            cx.notify();
        })
        .unwrap();
    cx.run_until_parked();
    assert!(following.get());
    assert!(at_bottom(&state));
}

struct MarkdownView {
    transcript: Transcript,
}

impl Render for MarkdownView {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let view = cx.entity();
        div()
            .size_full()
            .flex()
            .flex_col()
            .child(
                self.transcript
                    .viewport("markdown-chat", move |index, _, cx| {
                        view.update(cx, |this, _| {
                            let Row::Message {
                                entry,
                                markdown: Some(markdown),
                            } = &this.transcript.rows[index]
                            else {
                                panic!("expected Markdown reply");
                            };
                            div()
                                .w_full()
                                .child(markdown.render(&entry.id))
                                .into_any_element()
                        })
                    }),
            )
    }
}

#[gpui::test]
fn streaming_markdown_remeasures_the_same_row_without_losing_the_reading_anchor(
    cx: &mut TestAppContext,
) {
    let mut history = vec![Entry {
        id: "reply".into(),
        run: None,
        at: String::new(),
        kind: EntryKind::Assistant {
            text: "- **Long reply:** content that wraps with `inline code`.\n".repeat(100),
            streaming: true,
        },
    }];
    let mut transcript = Transcript::default();
    transcript.sync(&history, false, false, None);
    let state = transcript.state.clone();
    let following = transcript.following.clone();
    let window = cx.open_window(size(px(480.), px(320.)), |_, _| MarkdownView { transcript });
    cx.run_until_parked();
    assert!(at_bottom(&state));
    window
        .update(cx, |_, window, cx| {
            window.dispatch_event(
                PlatformInput::ScrollWheel(ScrollWheelEvent {
                    position: point(px(200.), px(150.)),
                    delta: pixels(0.25),
                    ..Default::default()
                }),
                cx,
            );
        })
        .unwrap();
    cx.run_until_parked();
    assert!(!following.get());
    let anchor = state.logical_scroll_top();
    let EntryKind::Assistant { text, .. } = &mut history[0].kind else {
        unreachable!()
    };
    text.push_str("\n## More output\n\nStill streaming.\n");
    window
        .update(cx, |view, _, cx| {
            view.transcript.sync(&history, false, false, None);
            cx.notify();
        })
        .unwrap();
    cx.run_until_parked();
    assert_eq!(state.logical_scroll_top().item_ix, anchor.item_ix);
    assert_eq!(
        state.logical_scroll_top().offset_in_item,
        anchor.offset_in_item
    );
    assert!(!following.get());
}

#[test]
fn projection_retains_markdown_and_refreshes_streams_and_activity() {
    let mut transcript = Transcript::default();
    let mut history = entries(1);
    history.push(Entry {
        id: "reply".into(),
        run: None,
        at: String::new(),
        kind: EntryKind::Assistant {
            text: "**partial".into(),
            streaming: true,
        },
    });
    transcript.sync(&history, true, false, None);
    assert_eq!(transcript.rows.len(), 3); // prompt, reply, working
    assert!(matches!(
        &transcript.rows[1],
        Row::Message {
            markdown: Some(_),
            ..
        }
    ));
    history[1].kind = EntryKind::Assistant {
        text: "**complete** response".into(),
        streaming: false,
    };
    transcript.sync(&history, false, false, None);
    assert_eq!(transcript.state.item_count(), 2);
    assert!(
        matches!(&transcript.rows[1], Row::Message { entry, markdown: Some(_) }
        if entry.kind == history[1].kind)
    );
    history.push(Entry {
        id: "thought".into(),
        run: None,
        at: String::new(),
        kind: EntryKind::Thought {
            text: "thinking".into(),
            streaming: true,
        },
    });
    transcript.sync(&history, true, false, None);
    assert!(
        matches!(&transcript.rows[2], Row::Activity { active: true, steps, .. } if steps.len() == 1)
    );
    transcript.sync(&history, false, false, None);
    assert!(matches!(
        &transcript.rows[2],
        Row::Activity { active: false, .. }
    ));
    transcript.sync(&[], false, true, Some("load failed"));
    assert!(matches!(
        &transcript.rows[..],
        [Row::Loading, Row::Error(_)]
    ));
}
