//! Keep history projection and Markdown parsing out of scroll frames. GPUI only
//! builds and lays out the visible variable-height rows (plus a small overdraw).
use std::{cell::Cell, rc::Rc};

use gpui::{AnyElement, App, ElementId, ListAlignment, ListState, Window, div, prelude::*, px};
use talos_ui_core::{
    activity::{TimelineItem, timeline},
    state::{Entry, EntryKind},
};

use crate::markdown;

#[cfg(test)]
#[path = "transcript_tests.rs"]
mod tests;

pub enum Row {
    Message {
        entry: Entry,
        markdown: Option<markdown::Document>,
    },
    Activity {
        id: String,
        steps: Vec<Entry>,
        active: bool,
    },
    Loading,
    Error(String),
    Working,
}

impl Row {
    fn same_identity(&self, other: &Self) -> bool {
        match (self, other) {
            (Self::Message { entry: a, .. }, Self::Message { entry: b, .. }) => a.id == b.id,
            (Self::Activity { id: a, .. }, Self::Activity { id: b, .. }) => a == b,
            (Self::Loading, Self::Loading)
            | (Self::Error(_), Self::Error(_))
            | (Self::Working, Self::Working) => true,
            _ => false,
        }
    }

    fn same_content(&self, other: &Self) -> bool {
        fn same_entry(a: &Entry, b: &Entry) -> bool {
            a.id == b.id && a.run == b.run && a.at == b.at && a.kind == b.kind
        }
        match (self, other) {
            (Self::Message { entry: a, .. }, Self::Message { entry: b, .. }) => same_entry(a, b),
            (
                Self::Activity {
                    steps: a,
                    active: active_a,
                    ..
                },
                Self::Activity {
                    steps: b,
                    active: active_b,
                    ..
                },
            ) => {
                active_a == active_b
                    && a.len() == b.len()
                    && a.iter().zip(b).all(|(a, b)| same_entry(a, b))
            }
            (Self::Error(a), Self::Error(b)) => a == b,
            (Self::Loading, Self::Loading) | (Self::Working, Self::Working) => true,
            _ => false,
        }
    }
}

pub struct Transcript {
    pub dirty: bool,
    pub rows: Vec<Row>,
    pub state: ListState,
    following: Rc<Cell<bool>>,
    check_bottom: Rc<Cell<bool>>,
}

impl Default for Transcript {
    fn default() -> Self {
        Self {
            dirty: true,
            rows: Vec::new(),
            state: ListState::new(0, ListAlignment::Top, px(300.)),
            following: Rc::new(Cell::new(true)),
            check_bottom: Rc::default(),
        }
    }
}

impl Transcript {
    /// Called on backend/state changes, never just because the user scrolled.
    pub fn sync(&mut self, entries: &[Entry], running: bool, loading: bool, error: Option<&str>) {
        self.resolve_scroll_intent();
        let mut rows = Vec::new();
        if loading {
            rows.push(Row::Loading);
        }
        if let Some(error) = error {
            rows.push(Row::Error(error.into()));
        }
        let mut active_activity = false;
        for item in timeline(entries, running) {
            rows.push(match item {
                TimelineItem::Message(entry) => Row::Message {
                    entry: entry.clone(),
                    markdown: None,
                },
                TimelineItem::Activity(group) => {
                    active_activity |= group.active;
                    Row::Activity {
                        id: group.id.into(),
                        steps: group.steps.into_iter().cloned().collect(),
                        active: group.active,
                    }
                }
            });
        }
        if running && !active_activity {
            rows.push(Row::Working);
        }
        let shared = self
            .rows
            .iter()
            .zip(&rows)
            .take_while(|(a, b)| a.same_identity(b))
            .count();
        for (index, row) in rows.iter_mut().enumerate() {
            if index < shared && self.rows[index].same_content(row) {
                // Retain parsed Markdown for unchanged replies.
                std::mem::swap(&mut self.rows[index], row);
            } else {
                if index < shared {
                    self.invalidate(index);
                }
                if let Row::Message { entry, markdown } = row
                    && let EntryKind::Assistant { text, .. } = &entry.kind
                {
                    *markdown = Some(markdown::Document::new(text));
                }
            }
        }
        if shared != self.rows.len() || shared != rows.len() {
            // The former last row loses its bottom inset when rows append.
            if shared > 0 {
                self.invalidate(shared - 1);
            }
            self.state
                .splice(shared..self.rows.len(), rows.len() - shared);
        }
        self.rows = rows;
        self.dirty = false;
    }

    pub fn invalidate(&self, index: usize) {
        self.state.remeasure_items(index..index + 1);
    }

    fn resolve_scroll_intent(&self) {
        // Resolve downward input against the previous layout before any incoming
        // content is measured. Do not use GPUI's built-in 1px follow threshold:
        // even a fraction of a pixel upward must pause following.
        if self.check_bottom.replace(false) {
            self.following.set(
                self.state.max_offset_for_scrollbar().y
                    + self.state.scroll_px_offset_for_scrollbar().y
                    <= px(0.),
            );
        }
    }

    pub fn viewport(
        &self,
        id: impl Into<ElementId>,
        mut render_item: impl FnMut(usize, &mut Window, &mut App) -> AnyElement + 'static,
    ) -> impl IntoElement {
        self.resolve_scroll_intent();
        if self.following.get() {
            self.state.scroll_to_end();
        }
        let following = self.following.clone();
        let check_bottom = self.check_bottom.clone();
        let count = self.state.item_count();
        div()
            .id(id)
            .flex_1()
            .min_h_0()
            .w_full()
            .flex()
            .on_scroll_wheel(move |event, window, _| {
                let delta = event.delta.pixel_delta(window.line_height()).y;
                if delta > px(0.) {
                    following.set(false);
                    check_bottom.set(false);
                } else if delta < px(0.) {
                    check_bottom.set(true);
                }
            })
            .child(
                gpui::list(self.state.clone(), move |index, window, cx| {
                    // Put insets in measured rows, not List padding: the pinned
                    // GPUI scrollbar extent excludes padding from its maximum.
                    div()
                        .w_full()
                        .pt(px(if index == 0 { 36. } else { 0. }))
                        .pb(px(if index + 1 == count { 36. } else { 26. }))
                        .child(render_item(index, window, cx))
                        .into_any_element()
                })
                .flex_1()
                .min_h_0()
                .w_full(),
            )
    }
}
