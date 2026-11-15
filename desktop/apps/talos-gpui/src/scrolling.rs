use gpui::{Div, ElementId, Stateful, div, prelude::*};

pub fn reading_viewport(id: impl Into<ElementId>) -> Stateful<Div> {
    div()
        .id(id)
        .flex_1()
        .min_h_0()
        .w_full()
        .overflow_y_scroll()
        .flex()
        // A row's default cross-axis stretch clamps the message column to the
        // viewport height. GPUI measures direct children for scroll overflow,
        // so let the column keep its natural height instead.
        .items_start()
        .justify_center()
}

#[cfg(test)]
mod tests {
    use super::*;
    use gpui::{
        Context, PlatformInput, Render, ScrollDelta, ScrollHandle, ScrollWheelEvent,
        TestAppContext, Window, point, px, size,
    };

    struct ReadingView {
        scroll: ScrollHandle,
        rows: usize,
    }

    impl Render for ReadingView {
        fn render(&mut self, _: &mut Window, _: &mut Context<Self>) -> impl IntoElement {
            let mut messages = div()
                .w_full()
                .max_w(px(790.))
                .flex()
                .flex_col()
                .gap(px(26.))
                .py(px(36.));
            for _ in 0..self.rows {
                messages = messages.child(div().h(px(100.)).flex_shrink_0());
            }
            div()
                .size_full()
                .overflow_hidden()
                .flex()
                .flex_col()
                .child(
                    reading_viewport("conversation-scroll")
                        .track_scroll(&self.scroll)
                        .child(messages),
                )
                .child(div().h(px(140.)).flex_shrink_0())
        }
    }

    struct DisclosureView {
        scroll: ScrollHandle,
        expanded: bool,
        constrained: bool,
    }

    impl Render for DisclosureView {
        fn render(&mut self, _: &mut Window, _: &mut Context<Self>) -> impl IntoElement {
            let mut messages = div()
                .w_full()
                .max_w(px(790.))
                .flex()
                .flex_col()
                .gap(px(26.))
                .py(px(36.));
            // Reopening a session lays out all the saved rows at once. The
            // clipped disclosure cards must not shrink to their borders when
            // mixed with non-shrinking text in an overflowing conversation.
            for (selector, title) in [("thought-card", "Thinking"), ("tool-card", "bash")] {
                let mut card = crate::disclosure_card()
                    .debug_selector(|| selector.into())
                    .child(div().px(px(14.)).py(px(12.)).child(crate::label(
                        title,
                        12.,
                        crate::INK,
                        gpui::FontWeight::MEDIUM,
                    )));
                if self.expanded {
                    card = card.child(div().px(px(14.)).pb(px(14.)).child(crate::label(
                        "saved details\n".repeat(20),
                        12.,
                        crate::INK,
                        gpui::FontWeight::NORMAL,
                    )));
                }
                messages = messages.child(card);
            }
            messages = messages
                .when(self.constrained, |column| column.h(px(400.)))
                .child(crate::label(
                    "Saved assistant response\n".repeat(60),
                    14.,
                    crate::INK,
                    gpui::FontWeight::NORMAL,
                ));
            div()
                .size_full()
                .flex()
                .flex_col()
                .child(
                    reading_viewport("conversation-scroll")
                        .track_scroll(&self.scroll)
                        .child(messages),
                )
                .child(div().h(px(140.)).flex_shrink_0())
        }
    }

    #[gpui::test]
    fn reopened_disclosures_keep_their_headers_and_expanded_details(cx: &mut TestAppContext) {
        let scroll = ScrollHandle::new();
        let window = cx.open_window(size(px(800.), px(600.)), |_, _| DisclosureView {
            scroll: scroll.clone(),
            expanded: false,
            constrained: false,
        });
        cx.run_until_parked();
        let cx = gpui::VisualTestContext::from_window(*window, cx).into_mut();
        let collapsed = ["thought-card", "tool-card"]
            .map(|selector| cx.debug_bounds(selector).unwrap().size.height);
        for height in collapsed {
            assert!(height >= px(36.), "collapsed disclosure height: {height:?}");
        }
        assert!(scroll.max_offset().y > px(500.));

        window
            .update(cx, |view, _, cx| {
                view.expanded = true;
                cx.notify();
            })
            .unwrap();
        cx.run_until_parked();
        for (selector, collapsed) in ["thought-card", "tool-card"].into_iter().zip(collapsed) {
            let height = cx.debug_bounds(selector).unwrap().size.height;
            assert!(
                height > collapsed + px(200.),
                "expanded disclosure height: {height:?}"
            );
        }

        // A height-constrained ancestor must not erase clipped cards either.
        // Overflow belongs to the viewport, never to a shrunken disclosure.
        window
            .update(cx, |view, _, cx| {
                view.constrained = true;
                cx.notify();
            })
            .unwrap();
        cx.run_until_parked();
        for (selector, collapsed) in ["thought-card", "tool-card"].into_iter().zip(collapsed) {
            let height = cx.debug_bounds(selector).unwrap().size.height;
            assert!(
                height > collapsed + px(200.),
                "constrained disclosure height: {height:?}"
            );
        }
    }

    #[gpui::test]
    fn long_conversation_scrolls_in_both_directions(cx: &mut TestAppContext) {
        let scroll = ScrollHandle::new();
        let window = cx.open_window(size(px(800.), px(600.)), |_, _| ReadingView {
            scroll: scroll.clone(),
            rows: 12,
        });
        cx.run_until_parked();
        assert!(scroll.max_offset().y > px(1000.));
        assert_eq!(scroll.offset().y, px(0.));

        for delta in [-240., 120.] {
            window
                .update(cx, |_, window, cx| {
                    window.dispatch_event(
                        PlatformInput::ScrollWheel(ScrollWheelEvent {
                            position: point(px(400.), px(200.)),
                            delta: ScrollDelta::Pixels(point(px(0.), px(delta))),
                            ..Default::default()
                        }),
                        cx,
                    );
                })
                .unwrap();
            cx.run_until_parked();
            assert_eq!(
                scroll.offset().y,
                px(if delta < 0. { -240. } else { -120. })
            );
        }

        // The composer is outside the scroll viewport.
        window
            .update(cx, |_, window, cx| {
                window.dispatch_event(
                    PlatformInput::ScrollWheel(ScrollWheelEvent {
                        position: point(px(400.), px(550.)),
                        delta: ScrollDelta::Pixels(point(px(0.), px(-240.))),
                        ..Default::default()
                    }),
                    cx,
                );
            })
            .unwrap();
        cx.run_until_parked();
        assert_eq!(scroll.offset().y, px(-120.));

        // Discrete mouse wheels report lines; touchpads report pixels above.
        window
            .update(cx, |_, window, cx| {
                window.dispatch_event(
                    PlatformInput::ScrollWheel(ScrollWheelEvent {
                        position: point(px(400.), px(200.)),
                        delta: ScrollDelta::Lines(point(0., -3.)),
                        ..Default::default()
                    }),
                    cx,
                );
            })
            .unwrap();
        cx.run_until_parked();
        assert!(scroll.offset().y < px(-120.));
    }

    #[gpui::test]
    fn short_conversation_only_scrolls_after_content_grows(cx: &mut TestAppContext) {
        let scroll = ScrollHandle::new();
        let window = cx.open_window(size(px(800.), px(600.)), |_, _| ReadingView {
            scroll: scroll.clone(),
            rows: 1,
        });
        cx.run_until_parked();
        assert_eq!(scroll.max_offset().y, px(0.));

        window
            .update(cx, |view, _, cx| {
                view.rows = 12;
                view.scroll.scroll_to_bottom();
                cx.notify();
            })
            .unwrap();
        cx.run_until_parked();
        assert!(scroll.max_offset().y > px(1000.));
        assert_eq!(scroll.offset().y, -scroll.max_offset().y);
    }
}
