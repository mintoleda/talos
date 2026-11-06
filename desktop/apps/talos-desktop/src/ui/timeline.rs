//! Conversation timeline.
//!
//! The timeline is the reading surface: user turns, streamed assistant text,
//! collapsible reasoning, tool activity, permission decisions and diagnostics.
//! It owns scroll-follow behavior but never mutates application state; user
//! intent is returned as [`TimelineAction`].

use std::collections::HashMap;

use eframe::egui::{
    self, Align, Color32, CornerRadius, FontId, Layout, Margin, RichText, Stroke, TextWrapMode,
};

use crate::state::{Entry, EntryKind, PendingState, ToolState};
use crate::theme;
use crate::ui::icons::{self, Icon};
use crate::ui::markdown::MarkdownCache;
use crate::ui::widgets;

/// Borrowed inputs for one timeline frame.
pub struct TimelineState<'a> {
    pub session_id: &'a str,
    pub entries: &'a [Entry],
    pub running: bool,
    pub motion: bool,
    pub cache: &'a mut MarkdownCache,
    pub stick: &'a mut bool,
    pub provider: &'a str,
}

/// A decision the timeline wants the application to perform.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum TimelineAction {
    Decide {
        session: String,
        id: String,
        allow: bool,
    },
}

/// Render the timeline. Returns actions requested by the user this frame.
pub fn show(ui: &mut egui::Ui, state: &mut TimelineState<'_>) -> Vec<TimelineAction> {
    let mut actions = Vec::new();
    let viewport = ui.available_rect_before_wrap();
    let scroll_id = egui::Id::unique(("timeline", state.session_id));
    ui.spacing_mut().item_spacing.y = 0.0;

    let output = egui::ScrollArea::vertical()
        .id_salt(scroll_id)
        .auto_shrink([false, false])
        .stick_to_bottom(*state.stick)
        .show(ui, |ui| {
            ui.add_space(26.0);
            ui.vertical_centered(|ui| {
                ui.set_max_width(theme::READING_MAX + 24.0);
                ui.with_layout(Layout::top_down(Align::Min), |ui| {
                    let mut index = 0;
                    while index < state.entries.len() {
                        let entry = &state.entries[index];
                        match &entry.kind {
                            EntryKind::Tool { .. } => {
                                let mut end = index + 1;
                                while end < state.entries.len()
                                    && matches!(state.entries[end].kind, EntryKind::Tool { .. })
                                {
                                    end += 1;
                                }
                                tool_group(ui, &state.entries[index..end]);
                                index = end;
                                continue;
                            }
                            _ => {
                                entry_block(ui, entry, state, &mut actions);
                            }
                        }
                        index += 1;
                    }
                    if state.running {
                        working_indicator(ui, state.motion);
                    }
                    ui.add_space(28.0);
                });
            });
        });

    let offset = output.state.clamped_offset().y;
    let content = output.content_size.y;
    let view = output.inner_rect.height();
    let at_bottom = offset + view + 40.0 >= content;
    let scrolled_up = ui.input(|input| input.smooth_scroll_delta.y > 0.0);
    if scrolled_up && !at_bottom {
        *state.stick = false;
    } else if at_bottom {
        *state.stick = true;
    }

    if !*state.stick {
        let button_rect = egui::Rect::from_center_size(
            egui::pos2(viewport.center().x, viewport.bottom() - 26.0),
            egui::vec2(132.0, 34.0),
        );
        let response = ui.put(
            button_rect,
            egui::Button::new(
                RichText::new("Jump to latest")
                    .font(theme::medium(12.0))
                    .color(theme::TEXT),
            )
            .fill(theme::SURFACE_HIGH)
            .stroke(Stroke::new(1.0, theme::LINE_STRONG))
            .corner_radius(17),
        );
        if response.clicked() {
            *state.stick = true;
        }
    }

    actions
}

fn entry_block(
    ui: &mut egui::Ui,
    entry: &Entry,
    state: &mut TimelineState<'_>,
    _actions: &mut Vec<TimelineAction>,
) {
    ui.push_id(&entry.id, |ui| match &entry.kind {
        EntryKind::User { text } => user_block(ui, text),
        EntryKind::Assistant { text, streaming } => {
            assistant_block(ui, text, *streaming, state.provider, state.cache);
        }
        EntryKind::Thought { text, streaming } => thought_block(ui, text, *streaming),
        EntryKind::Permission { .. } => {}
        EntryKind::Notice { message, .. } => notice_block(ui, message),
        EntryKind::Error { message, .. } => error_block(ui, message),
        EntryKind::Attachment { name, mime, size } => notice_block(
            ui,
            &format!("Image · {name} · {mime} · {} KiB", size / 1024),
        ),
        EntryKind::Tool { .. } => {}
    });
}

fn user_block(ui: &mut egui::Ui, text: &str) {
    ui.add_space(18.0);
    ui.with_layout(Layout::right_to_left(Align::TOP), |ui| {
        egui::Frame::new()
            .fill(theme::SURFACE)
            .stroke(Stroke::new(1.0, theme::LINE))
            .corner_radius(CornerRadius::same(16))
            .inner_margin(Margin::symmetric(16, 11))
            .show(ui, |ui| {
                ui.set_max_width((theme::READING_MAX * 0.78).max(220.0));
                ui.add(
                    egui::Label::new(RichText::new(text).font(theme::ui(14.5)).color(theme::TEXT))
                        .wrap_mode(TextWrapMode::Wrap),
                );
            });
    });
    ui.add_space(4.0);
}

fn assistant_block(
    ui: &mut egui::Ui,
    text: &str,
    streaming: bool,
    provider: &str,
    cache: &mut MarkdownCache,
) {
    ui.add_space(16.0);
    ui.horizontal(|ui| {
        let (rect, _) = ui.allocate_exact_size(egui::vec2(18.0, 18.0), egui::Sense::hover());
        paint_agent_mark(ui, rect);
        ui.add_space(2.0);
        ui.label(
            RichText::new(provider.to_uppercase())
                .font(theme::semibold(10.0))
                .color(theme::MUTED),
        );
        if streaming {
            ui.label(
                RichText::new("writing")
                    .font(theme::ui(10.5))
                    .color(theme::FAINT),
            );
        }
        ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
            if ui
                .add(
                    egui::Button::new(
                        RichText::new("Copy")
                            .font(theme::ui(10.5))
                            .color(theme::FAINT),
                    )
                    .frame(false),
                )
                .clicked()
            {
                ui.ctx().copy_text(text.to_owned());
            }
        });
    });
    ui.add_space(6.0);
    ui.scope(|ui| {
        ui.style_mut().visuals.hyperlink_color = theme::BLUE;
        cache.show(ui, text);
    });
    ui.add_space(6.0);
}

fn thought_block(ui: &mut egui::Ui, text: &str, streaming: bool) {
    ui.add_space(10.0);
    let id = ui.make_persistent_id("thought");
    let collapsed =
        egui::collapsing_header::CollapsingState::load_with_default_open(ui.ctx(), id, false);
    let summary = if streaming {
        "Thinking…"
    } else {
        "Thought process"
    };
    collapsed
        .show_header(ui, |ui| {
            icons::icon(ui, Icon::Sparkle, 12.0, theme::FAINT);
            ui.label(
                RichText::new(summary)
                    .font(theme::ui(11.5))
                    .color(theme::MUTED),
            );
        })
        .body(|ui| {
            ui.add_space(2.0);
            ui.scope(|ui| {
                ui.style_mut().visuals.override_text_color = Some(theme::MUTED);
                ui.label(
                    RichText::new(text)
                        .font(theme::ui(12.5))
                        .color(theme::MUTED),
                );
            });
        });
}

fn tool_group(ui: &mut egui::Ui, entries: &[Entry]) {
    ui.add_space(12.0);
    let all_done = entries.iter().all(|entry| {
        matches!(
            entry.kind,
            EntryKind::Tool {
                state: ToolState::Completed | ToolState::Failed,
                ..
            }
        )
    });
    let id = egui::Id::unique(("tool-group", entries.first().map(|entry| &entry.id)));
    let default_open = !all_done;
    let collapsing = egui::collapsing_header::CollapsingState::load_with_default_open(
        ui.ctx(),
        id,
        default_open,
    );
    ui.scope(|ui| {
        ui.spacing_mut().item_spacing.y = 4.0;
        collapsing
            .show_header(ui, |ui| {
                let failed = entries.iter().any(|entry| {
                    matches!(
                        entry.kind,
                        EntryKind::Tool {
                            state: ToolState::Failed,
                            ..
                        }
                    )
                });
                let color = if failed {
                    theme::RED
                } else if all_done {
                    theme::GREEN
                } else {
                    theme::BLUE
                };
                let (dot, _) = ui.allocate_exact_size(egui::vec2(8.0, 8.0), egui::Sense::hover());
                ui.painter().circle_filled(dot.center(), 3.0, color);
                let label = if entries.len() == 1 {
                    "1 tool call".to_owned()
                } else {
                    format!("{} tool calls", entries.len())
                };
                ui.label(
                    RichText::new(label)
                        .font(theme::medium(12.5))
                        .color(theme::SECONDARY),
                );
                if let Some(last) = entries.last()
                    && !last.at.is_empty()
                {
                    ui.label(
                        RichText::new(&last.at)
                            .font(theme::ui(10.5))
                            .color(theme::FAINT),
                    );
                }
            })
            .body(|ui| {
                for entry in entries {
                    if let EntryKind::Tool {
                        name,
                        target,
                        state,
                        input,
                        output,
                        ..
                    } = &entry.kind
                    {
                        tool_row(ui, &entry.id, name, target, *state, input, output);
                    }
                }
            });
    });
    ui.add_space(4.0);
}

fn tool_row(
    ui: &mut egui::Ui,
    id: &str,
    name: &str,
    target: &str,
    state: ToolState,
    input: &str,
    output: &str,
) {
    let id = egui::Id::unique(("tool", id));
    let collapsing =
        egui::collapsing_header::CollapsingState::load_with_default_open(ui.ctx(), id, false);
    let color = match state {
        ToolState::Running => theme::BLUE,
        ToolState::Completed => theme::GREEN,
        ToolState::Failed => theme::RED,
    };
    collapsing
        .show_header(ui, |ui| {
            icons::icon(ui, Icon::Terminal, 13.0, theme::MUTED);
            ui.label(
                RichText::new(name)
                    .font(theme::medium(12.5))
                    .color(theme::SECONDARY),
            );
            if !target.is_empty() {
                ui.add(
                    egui::Label::new(
                        RichText::new(target)
                            .font(theme::mono(11.5))
                            .color(theme::MUTED),
                    )
                    .truncate(),
                );
            }
            ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                ui.label(
                    RichText::new(state.label())
                        .font(theme::ui(10.5))
                        .color(color),
                );
                let (dot, _) = ui.allocate_exact_size(egui::vec2(8.0, 8.0), egui::Sense::hover());
                ui.painter().circle_filled(dot.center(), 3.0, color);
            });
        })
        .body(|ui| {
            code_panel(ui, if output.is_empty() { input } else { output });
        });
}

pub fn pending_requests(
    ui: &mut egui::Ui,
    session: &str,
    entries: &[Entry],
    pending: &HashMap<String, PendingState>,
    online: bool,
) -> Vec<TimelineAction> {
    let mut actions = Vec::new();
    for entry in entries {
        if let EntryKind::Permission { id, .. } = &entry.kind
            && pending.contains_key(id)
        {
            ui.push_id(id, |ui| {
                permission_block(ui, session, pending, online, entry, &mut actions)
            });
        }
    }
    actions
}
fn permission_block(
    ui: &mut egui::Ui,
    session: &str,
    pending: &HashMap<String, PendingState>,
    online: bool,
    entry: &Entry,
    actions: &mut Vec<TimelineAction>,
) {
    let EntryKind::Permission {
        id,
        action,
        target,
        risk,
        reason,
    } = &entry.kind
    else {
        return;
    };
    ui.add_space(14.0);
    egui::Frame::new()
        .fill(theme::SURFACE)
        .stroke(Stroke::new(
            1.0,
            Color32::from_rgba_unmultiplied(216, 166, 87, 90),
        ))
        .corner_radius(theme::CARD_RADIUS)
        .inner_margin(Margin::symmetric(16, 14))
        .show(ui, |ui| {
            ui.set_width((theme::READING_MAX - 24.0).min(ui.available_width()));
            ui.horizontal(|ui| {
                icons::icon(ui, Icon::Shield, 14.0, theme::AMBER);
                ui.label(
                    RichText::new("PERMISSION REQUIRED")
                        .font(theme::semibold(10.0))
                        .color(theme::AMBER),
                );
                ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                    widgets::pill(ui, risk, theme::AMBER);
                });
            });
            ui.add_space(8.0);
            ui.label(
                RichText::new(action)
                    .font(theme::semibold(16.0))
                    .color(theme::TEXT),
            );
            if !target.is_empty() {
                ui.add_space(4.0);
                code_panel(ui, target);
            }
            if !reason.is_empty() {
                ui.add_space(6.0);
                ui.label(
                    RichText::new(reason)
                        .font(theme::ui(12.5))
                        .color(theme::SECONDARY),
                );
            }
            ui.add_space(12.0);
            ui.horizontal(|ui| {
                let pending = pending.get(id).copied().unwrap_or_default();
                match pending {
                    PendingState::Submitting => {
                        ui.spinner();
                        ui.label(
                            RichText::new("Sending decision…")
                                .font(theme::ui(12.0))
                                .color(theme::MUTED),
                        );
                    }
                    PendingState::Idle | PendingState::Failed => {
                        ui.add_enabled_ui(online, |ui| {
                            if ui
                                .add(
                                    egui::Button::new(
                                        RichText::new("Deny")
                                            .font(theme::medium(12.5))
                                            .color(theme::SECONDARY),
                                    )
                                    .fill(theme::SURFACE_HIGH)
                                    .corner_radius(9),
                                )
                                .clicked()
                            {
                                actions.push(TimelineAction::Decide {
                                    session: session.to_owned(),
                                    id: id.to_owned(),
                                    allow: false,
                                });
                            }
                            if ui
                                .add(
                                    egui::Button::new(
                                        RichText::new("Allow once")
                                            .font(theme::medium(12.5))
                                            .color(Color32::from_rgb(14, 20, 26)),
                                    )
                                    .fill(theme::BLUE)
                                    .corner_radius(9),
                                )
                                .clicked()
                            {
                                actions.push(TimelineAction::Decide {
                                    session: session.to_owned(),
                                    id: id.to_owned(),
                                    allow: true,
                                });
                            }
                        });
                        if pending == PendingState::Failed {
                            ui.label(
                                RichText::new("Decision failed — try again")
                                    .font(theme::ui(11.0))
                                    .color(theme::RED),
                            );
                        }
                        if !online {
                            ui.label(
                                RichText::new("Reconnect to submit a decision.")
                                    .font(theme::ui(11.0))
                                    .color(theme::MUTED),
                            );
                        }
                    }
                }
            });
        });
    ui.add_space(6.0);
}

fn notice_block(ui: &mut egui::Ui, message: &str) {
    ui.add_space(8.0);
    ui.horizontal(|ui| {
        icons::icon(ui, Icon::Sparkle, 12.0, theme::FAINT);
        ui.label(
            RichText::new(message)
                .font(theme::ui(11.5))
                .color(theme::MUTED),
        );
    });
}

fn error_block(ui: &mut egui::Ui, message: &str) {
    ui.add_space(10.0);
    egui::Frame::new()
        .fill(Color32::from_rgb(38, 26, 26))
        .stroke(Stroke::new(
            1.0,
            Color32::from_rgba_unmultiplied(224, 115, 106, 90),
        ))
        .corner_radius(theme::CARD_RADIUS)
        .inner_margin(Margin::symmetric(14, 12))
        .show(ui, |ui| {
            ui.set_width((theme::READING_MAX - 24.0).min(ui.available_width()));
            ui.horizontal(|ui| {
                icons::icon(ui, Icon::Alert, 14.0, theme::RED);
                ui.label(
                    RichText::new("Run failed")
                        .font(theme::semibold(12.5))
                        .color(theme::RED),
                );
            });
            ui.add_space(4.0);
            ui.label(
                RichText::new(message)
                    .font(theme::ui(12.0))
                    .color(theme::SECONDARY),
            );
        });
    ui.add_space(6.0);
}

fn code_panel(ui: &mut egui::Ui, text: &str) {
    let preview_end = text
        .char_indices()
        .nth(16_000)
        .map_or(text.len(), |(index, _)| index);
    let full = text;
    let text = &text[..preview_end];
    if preview_end < full.len() {
        ui.label("Output preview limited to 16,000 characters");
        if ui.button("Copy complete output").clicked() {
            ui.ctx().copy_text(full.to_owned());
        }
    }
    let preview: String = if text.chars().count() > 6000 {
        let head: String = text.chars().take(6000).collect();
        format!("{head}\n… (truncated preview)")
    } else {
        text.to_owned()
    };
    egui::Frame::new()
        .fill(theme::CODE)
        .corner_radius(8)
        .inner_margin(Margin::symmetric(10, 8))
        .show(ui, |ui| {
            ui.set_width(ui.available_width());
            egui::ScrollArea::vertical()
                .id_salt(("code", text.len()))
                .max_height(320.0)
                .show(ui, |ui| {
                    ui.add(
                        egui::Label::new(
                            RichText::new(preview)
                                .font(FontId::monospace(11.5))
                                .color(theme::SECONDARY),
                        )
                        .wrap_mode(TextWrapMode::Wrap),
                    );
                });
        });
}

fn working_indicator(ui: &mut egui::Ui, motion: bool) {
    ui.add_space(12.0);
    ui.horizontal(|ui| {
        let time = ui.input(|input| input.time) as f32;
        let phase = if motion {
            (time * 1.6).sin() * 0.5 + 0.5
        } else {
            1.0
        };
        let (rect, _) = ui.allocate_exact_size(egui::vec2(8.0, 8.0), egui::Sense::hover());
        ui.painter().circle_filled(
            rect.center(),
            3.0,
            theme::mix(theme::BLUE_DIM, theme::BLUE, phase),
        );
        ui.label(
            RichText::new("Working")
                .font(theme::ui(11.5))
                .color(theme::MUTED),
        );
    });
    if motion {
        ui.ctx()
            .request_repaint_after(std::time::Duration::from_millis(120));
    }
}

fn paint_agent_mark(ui: &egui::Ui, rect: egui::Rect) {
    let painter = ui.painter();
    painter.circle_filled(rect.center(), rect.width() * 0.5, theme::SURFACE_HIGH);
    painter.add(egui::Shape::convex_polygon(
        vec![
            egui::pos2(rect.center().x, rect.top() + 4.0),
            egui::pos2(rect.right() - 4.0, rect.center().y),
            egui::pos2(rect.center().x, rect.bottom() - 4.0),
            egui::pos2(rect.left() + 4.0, rect.center().y),
        ],
        theme::BLUE,
        Stroke::NONE,
    ));
}
