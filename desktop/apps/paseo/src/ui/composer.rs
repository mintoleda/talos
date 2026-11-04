//! The composer — the visual anchor of the shell.
//!
//! A wide, low rounded shell: multiline input on top, a shared control baseline
//! below (attach, provider/model, mode, context meter, send/stop). Motion is
//! limited to focus glow, hover washes and the send affordance.

use eframe::egui::{
    self, Align, Color32, CornerRadius, Layout, Margin, Response, RichText, Sense, Stroke,
    TextWrapMode, UiBuilder, Vec2,
};

use crate::state::{ProviderSummary, Session, Usage, can_send, draft_lines};
use crate::theme;
use crate::ui::icons::{self, Icon};
use crate::ui::motion;

/// Everything the composer needs for one frame.
pub struct ComposerState<'a> {
    pub session_id: &'a str,
    pub draft: &'a mut String,
    pub running: bool,
    pub online: bool,
    pub session: Option<&'a Session>,
    pub providers: &'a [ProviderSummary],
    pub usage: Option<&'a Usage>,
    pub motion: bool,
}

/// User intent produced by the composer.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ComposerAction {
    Send,
    Stop,
    SetModel { provider: String, model: String },
    SetMode { mode: String },
    Attach,
    Voice,
}

/// Stable widget id for focusing the composer with a keyboard shortcut.
#[must_use]
pub fn composer_id(session: &str) -> egui::Id {
    egui::Id::unique(("composer", session))
}

/// Total shell height for a given draft and available width.
#[must_use]
pub fn height(draft: &str, width: f32) -> f32 {
    let rows = draft_lines(draft, width.min(theme::COMPOSER_MAX));
    104.0 + (rows.saturating_sub(1) as f32) * 19.0
}

/// Draw the composer and return any requested action.
pub fn show(ui: &mut egui::Ui, state: &mut ComposerState<'_>) -> Option<ComposerAction> {
    let available = ui.available_width();
    let width = (available - 40.0).clamp(260.0, theme::COMPOSER_MAX);
    let rows = draft_lines(state.draft, width);
    let shell_height = height(state.draft, width);
    let mut action = None;

    ui.vertical_centered(|ui| {
        let (rect, _) = ui.allocate_exact_size(Vec2::new(width, shell_height), Sense::hover());
        let focus_id = composer_id(state.session_id);
        let has_focus = ui.memory(|memory| memory.has_focus(focus_id));
        let focus = motion::animate_bool(
            ui.ctx(),
            egui::Id::unique(("composer-focus", state.session_id)),
            has_focus,
            state.motion,
        );
        let disabled = !state.online;

        // Focus halo, then the shell.
        if focus > 0.01 {
            ui.painter().rect_filled(
                rect.expand(1.0),
                CornerRadius::same(theme::COMPOSER_RADIUS),
                Color32::from_rgba_unmultiplied(
                    theme::BLUE.r(),
                    theme::BLUE.g(),
                    theme::BLUE.b(),
                    (26.0 * focus) as u8,
                ),
            );
        }
        ui.painter().rect(
            rect,
            CornerRadius::same(theme::COMPOSER_RADIUS),
            if disabled {
                theme::SIDEBAR
            } else {
                theme::SURFACE
            },
            Stroke::new(1.0, theme::mix(theme::LINE_STRONG, theme::BLUE_DIM, focus)),
            egui::StrokeKind::Inside,
        );

        let inner = rect.shrink2(Vec2::new(16.0, 12.0));
        ui.scope_builder(
            UiBuilder::new()
                .max_rect(inner)
                .layout(Layout::top_down(Align::Min)),
            |ui| {
                ui.spacing_mut().item_spacing = Vec2::new(8.0, 6.0);

                let input_height = 26.0 + (rows.saturating_sub(1) as f32) * 19.0;
                let edit = egui::TextEdit::multiline(state.draft)
                    .id(focus_id)
                    .hint_text(
                        RichText::new(crate::state::PLACEHOLDER)
                            .font(theme::ui(14.5))
                            .color(theme::MUTED),
                    )
                    .desired_rows(rows)
                    .desired_width(f32::INFINITY)
                    .frame(egui::Frame::NONE)
                    .margin(Margin::ZERO)
                    .font(theme::ui(14.5))
                    .text_color(theme::TEXT)
                    .lock_focus(true);
                let response = ui.add_sized([ui.available_width(), input_height], edit);

                let quiet_input = ui.input(|input| {
                    input
                        .events
                        .iter()
                        .any(|event| matches!(event, egui::Event::Ime(_)))
                });
                let enter = response.has_focus()
                    && ui.input(|input| {
                        input.key_pressed(egui::Key::Enter)
                            && !input.modifiers.shift
                            && !input.modifiers.command
                    })
                    && !quiet_input;
                let command_enter = response.has_focus()
                    && ui.input(|input| {
                        input.key_pressed(egui::Key::Enter)
                            && input.modifiers.command
                            && !input.modifiers.shift
                    })
                    && !quiet_input;

                let running = state.running;
                let ready = state.online && can_send(state.draft, 0, running);
                if (enter || command_enter) && ready {
                    action = Some(ComposerAction::Send);
                }

                // Bottom control baseline.
                ui.with_layout(Layout::left_to_right(Align::Center), |ui| {
                    if icon_control(ui, Icon::Plus, "Attach a file", true).clicked() {
                        action = Some(ComposerAction::Attach);
                    }
                    model_selector(ui, state, &mut action);
                    mode_selector(ui, state, &mut action);
                    ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                        let send = send_button(ui, ready, running);
                        if send.clicked() {
                            action = if running {
                                Some(ComposerAction::Stop)
                            } else if ready {
                                Some(ComposerAction::Send)
                            } else {
                                None
                            };
                        }
                        if icon_control(
                            ui,
                            Icon::Waveform,
                            "Dictation isn't wired to this daemon yet",
                            false,
                        )
                        .clicked()
                        {
                            action = Some(ComposerAction::Voice);
                        }
                        if icon_control(
                            ui,
                            Icon::Mic,
                            "Voice input isn't wired to this daemon yet",
                            false,
                        )
                        .clicked()
                        {
                            action = Some(ComposerAction::Voice);
                        }
                        context_meter(ui, state);
                    });
                });
            },
        );
    });
    action
}

fn model_selector(
    ui: &mut egui::Ui,
    state: &ComposerState<'_>,
    action: &mut Option<ComposerAction>,
) {
    let provider_id = state
        .session
        .map(|session| session.provider.as_str())
        .filter(|provider| !provider.is_empty())
        .unwrap_or("talos");
    let provider = state
        .providers
        .iter()
        .find(|provider| provider.id == provider_id);
    let model_label = state
        .session
        .and_then(|session| session.model.clone())
        .filter(|model| !model.is_empty())
        .unwrap_or_else(|| "Default model".into());
    let provider_label = provider.map_or(provider_id, |provider| provider.name.as_str());
    let response = chip(
        ui,
        Icon::Sparkle,
        theme::ORANGE,
        &format!("{provider_label} · {model_label}"),
        true,
    );
    egui::Popup::menu(&response)
        .id(egui::Id::unique("model-menu"))
        .show(|ui| {
            ui.set_min_width(280.0);
            ui.set_max_width(320.0);
            ui.add_space(2.0);
            ui.label(
                RichText::new("MODEL")
                    .font(theme::semibold(10.0))
                    .color(theme::FAINT),
            );
            egui::ScrollArea::vertical().max_height(320.0).show(ui, |ui| {
                for provider in state.providers {
                    let current_provider = provider.id == provider_id;
                    ui.add_space(6.0);
                    ui.horizontal(|ui| {
                        icons::icon(
                            ui,
                            Icon::Cpu,
                            12.0,
                            if current_provider {
                                theme::BLUE
                            } else {
                                theme::MUTED
                            },
                        );
                        ui.label(
                            RichText::new(&provider.name)
                                .font(theme::semibold(11.0))
                                .color(theme::SECONDARY),
                        );
                        if !provider.available {
                            ui.label(
                                RichText::new("unavailable")
                                    .font(theme::ui(10.0))
                                    .color(theme::FAINT),
                            );
                        }
                    });
                    if provider.models.is_empty() {
                        ui.label(
                            RichText::new("   No models reported by this provider")
                                .font(theme::ui(11.0))
                                .color(theme::FAINT),
                        );
                    }
                    for model in &provider.models {
                        let selected = current_provider && model.id == model_label;
                        if ui
                            .add_enabled(
                                provider.available && current_provider,
                                egui::Button::new(
                                    RichText::new(format!(
                                        "   {}{}",
                                        model.label,
                                        if selected { "  ✓" } else { "" }
                                    ))
                                    .font(theme::ui(12.0))
                                    .color(if selected { theme::TEXT } else { theme::SECONDARY }),
                                )
                                .fill(if selected { theme::SELECTED } else { Color32::TRANSPARENT })
                                .corner_radius(7),
                            )
                            .clicked()
                        {
                            *action = Some(ComposerAction::SetModel {
                                provider: provider.id.clone(),
                                model: model.id.clone(),
                            });
                            ui.close();
                        }
                    }
                }
            });
            if state.providers.iter().all(|provider| provider.models.is_empty()) {
                ui.add_space(6.0);
                ui.label(
                    RichText::new(
                        "Talos reports its model catalog during a session. Configure credentials in ~/.paseo-rs/config.toml.",
                    )
                    .font(theme::ui(10.5))
                    .color(theme::FAINT),
                );
            }
        });
}

fn mode_selector(
    ui: &mut egui::Ui,
    state: &ComposerState<'_>,
    action: &mut Option<ComposerAction>,
) {
    let Some(session) = state.session else {
        return;
    };
    let Some(provider) = state
        .providers
        .iter()
        .find(|provider| provider.id == session.provider)
    else {
        return;
    };
    if provider.modes.is_empty() {
        return;
    }
    let current = session
        .mode
        .clone()
        .unwrap_or_else(|| provider.modes[0].0.clone());
    let label = provider
        .modes
        .iter()
        .find(|(id, _)| id == &current)
        .map_or(current.as_str(), |(_, label)| label.as_str());
    let shield = if matches!(current.as_str(), "panic" | "plan" | "read-only") {
        Icon::Shield
    } else {
        Icon::ShieldOff
    };
    let response = chip(ui, shield, theme::MUTED, label, true);
    egui::Popup::menu(&response).show(|ui| {
        ui.set_min_width(220.0);
        ui.label(
            RichText::new("PERMISSION MODE")
                .font(theme::semibold(10.0))
                .color(theme::FAINT),
        );
        for (id, label) in &provider.modes {
            let selected = id == &current;
            if ui
                .add(
                    egui::Button::new(
                        RichText::new(format!("{label}{}", if selected { "  ✓" } else { "" }))
                            .font(theme::ui(12.0))
                            .color(if selected {
                                theme::TEXT
                            } else {
                                theme::SECONDARY
                            }),
                    )
                    .fill(if selected {
                        theme::SELECTED
                    } else {
                        Color32::TRANSPARENT
                    })
                    .corner_radius(7),
                )
                .clicked()
            {
                *action = Some(ComposerAction::SetMode { mode: id.clone() });
                ui.close();
            }
        }
    });
}

/// A small icon + label chip with a smooth hover wash.
fn chip(
    ui: &mut egui::Ui,
    icon: Icon,
    icon_color: Color32,
    label: &str,
    interactive: bool,
) -> Response {
    let galley =
        ui.painter()
            .layout_no_wrap(label.to_owned(), theme::medium(12.0), theme::SECONDARY);
    let size = Vec2::new(galley.size().x + 46.0, 28.0);
    let sense = if interactive {
        Sense::click()
    } else {
        Sense::hover()
    };
    let (rect, response) = ui.allocate_exact_size(size, sense);
    let hover = ui.ctx().animate_bool_with_time(
        egui::Id::unique(("chip-hover", label)),
        response.hovered() && interactive,
        0.09,
    );
    if hover > 0.01 {
        ui.painter().rect_filled(
            rect,
            8.0,
            Color32::from_rgba_unmultiplied(
                theme::HOVER.r(),
                theme::HOVER.g(),
                theme::HOVER.b(),
                (220.0 * hover) as u8,
            ),
        );
    }
    let icon_rect = egui::Rect::from_center_size(
        egui::pos2(rect.left() + 14.0, rect.center().y),
        Vec2::splat(13.0),
    );
    icons::paint(ui.painter(), icon_rect, icon, icon_color, 1.5);
    ui.painter().galley(
        egui::pos2(rect.left() + 25.0, rect.center().y - galley.size().y * 0.5),
        galley,
        theme::SECONDARY,
    );
    icons::paint(
        ui.painter(),
        egui::Rect::from_center_size(
            egui::pos2(rect.right() - 10.0, rect.center().y),
            Vec2::splat(10.0),
        ),
        Icon::ChevronDown,
        theme::FAINT,
        1.4,
    );
    if interactive && response.hovered() {
        ui.ctx().set_cursor_icon(egui::CursorIcon::PointingHand);
    }
    response
}

fn context_meter(ui: &mut egui::Ui, state: &ComposerState<'_>) {
    let usage = state.usage;
    let fraction = usage.and_then(|usage| match (usage.context_used, usage.context_size) {
        (Some(used), Some(size)) if size > 0 => Some(used as f32 / size as f32),
        _ => None,
    });
    let (rect, response) = ui.allocate_exact_size(Vec2::splat(26.0), Sense::click());
    let painter = ui.painter();
    let center = rect.center();
    let radius = 8.0;
    painter.circle_stroke(center, radius, Stroke::new(1.6, theme::LINE_STRONG));
    let color = fraction.map_or(theme::MUTED, |value| {
        if value > 0.9 {
            theme::RED
        } else if value > 0.7 {
            theme::AMBER
        } else {
            theme::BLUE
        }
    });
    if let Some(fraction) = fraction {
        let steps = 28;
        let sweep = std::f32::consts::TAU * fraction.clamp(0.0, 1.0);
        let points: Vec<egui::Pos2> = (0..=steps)
            .map(|step| {
                let angle = -std::f32::consts::FRAC_PI_2 + sweep * step as f32 / steps as f32;
                center + Vec2::angled(angle) * radius
            })
            .collect();
        painter.add(egui::Shape::line(points, Stroke::new(1.8, color)));
    }
    response.on_hover_text(match (usage, fraction) {
        (Some(usage), Some(fraction)) => format!(
            "Context {:.0}% · if available: {} in / {} out",
            fraction * 100.0,
            usage.input_tokens.unwrap_or(0),
            usage.output_tokens.unwrap_or(0)
        ),
        _ => "No usage reported for this session yet".into(),
    });
}

fn send_button(ui: &mut egui::Ui, ready: bool, running: bool) -> Response {
    let (rect, response) = ui.allocate_exact_size(Vec2::splat(38.0), Sense::click());
    let hover =
        ui.ctx()
            .animate_bool_with_time(egui::Id::unique("send-hover"), response.hovered(), 0.09);
    let fill = if running {
        theme::RED
    } else if ready {
        theme::mix(theme::BLUE, theme::BLUE_HOVER, hover)
    } else {
        theme::SURFACE_HIGH
    };
    let painter = ui.painter();
    let color = if ready || running {
        Color32::from_rgb(13, 18, 22)
    } else {
        theme::FAINT
    };
    painter.circle_filled(rect.center(), 19.0, fill);
    if running {
        let square = egui::Rect::from_center_size(rect.center(), Vec2::splat(9.0));
        painter.rect_filled(square, 2.0, color);
    } else {
        icons::paint(
            painter,
            egui::Rect::from_center_size(rect.center(), Vec2::splat(18.0)),
            Icon::ArrowUp,
            color,
            1.9,
        );
    }
    response.on_hover_text(if running {
        "Stop the run"
    } else if ready {
        "Send · Enter"
    } else {
        "Write something to send"
    })
}

fn icon_control(ui: &mut egui::Ui, icon: Icon, tip: &str, enabled: bool) -> Response {
    let (rect, response) = ui.allocate_exact_size(Vec2::splat(30.0), Sense::click());
    let hover = ui.ctx().animate_bool_with_time(
        egui::Id::unique(("icon-hover", tip)),
        response.hovered(),
        0.09,
    );
    if hover > 0.01 {
        ui.painter().rect_filled(
            rect.shrink(2.0),
            7.0,
            Color32::from_rgba_unmultiplied(
                theme::HOVER.r(),
                theme::HOVER.g(),
                theme::HOVER.b(),
                (200.0 * hover) as u8,
            ),
        );
    }
    let color = if !enabled {
        theme::FAINT
    } else if hover > 0.5 {
        theme::TEXT
    } else {
        theme::MUTED
    };
    icons::paint(
        ui.painter(),
        egui::Rect::from_center_size(rect.center(), Vec2::splat(16.0)),
        icon,
        color,
        1.5,
    );
    if response.hovered() {
        ui.ctx().set_cursor_icon(if enabled {
            egui::CursorIcon::PointingHand
        } else {
            egui::CursorIcon::Default
        });
    }
    response.on_hover_text(tip)
}

/// A compact inline hint used by the empty state.
pub fn suggestion_chip(ui: &mut egui::Ui, label: &str) -> Response {
    ui.add(
        egui::Button::new(
            RichText::new(label)
                .font(theme::ui(12.5))
                .color(theme::SECONDARY),
        )
        .fill(theme::SURFACE)
        .stroke(Stroke::new(1.0, theme::LINE))
        .corner_radius(16)
        .min_size(Vec2::new(0.0, 32.0)),
    )
    .on_hover_text("Insert this prompt")
}

/// Wrap a hint line with the keyboard contract.
pub fn hint(ui: &mut egui::Ui) {
    ui.add(
        egui::Label::new(
            RichText::new("Enter to send  ·  Shift+Enter for a new line")
                .font(theme::ui(10.5))
                .color(theme::FAINT),
        )
        .extend(),
    );
}
