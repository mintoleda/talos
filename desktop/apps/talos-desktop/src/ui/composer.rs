//! The composer — the visual anchor of the shell.
//!
//! A wide, low rounded shell: multiline input on top, a shared control baseline
//! below (attach, provider/model, mode, context meter, send/stop). Motion is
//! limited to focus glow, hover washes and the send affordance.

use eframe::egui::{
    self, Align, Color32, CornerRadius, Layout, Margin, Response, RichText, Sense, Stroke,
    UiBuilder, Vec2,
};

use crate::state::{ProviderSummary, Session, Usage, can_send, draft_lines};
use crate::theme;
use crate::ui::icons::{self, Icon};
use crate::ui::motion;

/// Everything the composer needs for one frame.
pub struct ComposerState<'a> {
    pub session_id: &'a str,
    pub provider_id: &'a str,
    pub draft: &'a mut String,
    pub running: bool,
    pub online: bool,
    pub session: Option<&'a Session>,
    pub remembered_model: Option<&'a str>,
    pub providers: &'a [ProviderSummary],
    pub usage: Option<&'a Usage>,
    pub motion: bool,
    pub attachments: &'a [serde_json::Value],
    pub config_options: &'a serde_json::Value,
}

/// User intent produced by the composer.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ComposerAction {
    Send,
    Stop,
    SetModel { provider: String, model: String },
    Attach,
    RemoveAttachment(usize),
    SetMode(String),
    SetConfig { option: String, value: String },
}

/// Stable widget id for focusing the composer with a keyboard shortcut.
#[must_use]
pub fn composer_id(session: &str) -> egui::Id {
    egui::Id::unique(("composer", session))
}

#[derive(Clone, Copy)]
struct EnterKeyState {
    focused: bool,
    enter_pressed: bool,
    shift: bool,
    ime_active: bool,
    ready: bool,
}

fn send_key_submits(state: EnterKeyState) -> bool {
    state.focused && state.enter_pressed && !state.shift && !state.ime_active && state.ready
}

/// Total shell height for a given draft and available width.
#[must_use]
pub fn height(draft: &str, width: f32) -> f32 {
    let rows = draft_lines(draft, width.min(theme::COMPOSER_MAX));
    92.0 + rows.saturating_sub(1) as f32 * 19.0
}

/// Height for the composer when validated image chips are present.
#[must_use]
pub fn height_with_attachments(draft: &str, width: f32, attachment_count: usize) -> f32 {
    let columns = ((width - 64.0) / 230.0).floor().max(1.0) as usize;
    let rows = attachment_count.div_ceil(columns);
    height(draft, width) + (rows as f32) * 30.0
}

/// Draw the composer and return any requested action.
pub fn show(ui: &mut egui::Ui, state: &mut ComposerState<'_>) -> Option<ComposerAction> {
    let available = ui.available_width();
    let width = (available - 16.0).clamp(260.0, theme::COMPOSER_MAX);
    let rows = draft_lines(state.draft, width);
    let shell_height = height_with_attachments(state.draft, width, state.attachments.len());
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

        let inner = rect.shrink2(Vec2::new(12.0, 10.0));
        ui.scope_builder(
            UiBuilder::new()
                .max_rect(inner)
                .layout(Layout::top_down(Align::Min)),
            |ui| {
                ui.spacing_mut().item_spacing = Vec2::new(4.0, 4.0);
                ui.spacing_mut().button_padding = Vec2::new(6.0, 4.0);

                if !state.attachments.is_empty() {
                    ui.horizontal_wrapped(|ui| {
                        for (index, attachment) in state.attachments.iter().enumerate() {
                            let name = attachment["name"].as_str().unwrap_or("Image");
                            let mime = attachment["mime"].as_str().unwrap_or("image");
                            let size = attachment["size"].as_u64().unwrap_or_default() / 1024;
                            if ui
                                .add(
                                    egui::Button::new(
                                        RichText::new(format!(
                                            "{} · {mime} · {size} KiB  ×",
                                            truncate_name(name, 22)
                                        ))
                                        .font(theme::ui(10.5))
                                        .color(theme::SECONDARY),
                                    )
                                    .fill(theme::SURFACE_HIGH)
                                    .stroke(Stroke::new(1.0, theme::LINE))
                                    .corner_radius(8),
                                )
                                .on_hover_text("Remove attachment")
                                .clicked()
                            {
                                action = Some(ComposerAction::RemoveAttachment(index));
                            }
                        }
                    });
                }

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
                    .lock_focus(true)
                    .return_key(egui::KeyboardShortcut::new(
                        egui::Modifiers::SHIFT,
                        egui::Key::Enter,
                    ));
                let response = ui.add_sized([ui.available_width(), input_height], edit);

                let quiet_input = ui.input(|input| {
                    input
                        .events
                        .iter()
                        .any(|event| matches!(event, egui::Event::Ime(_)))
                });
                let enter_pressed = ui.input(|input| input.key_pressed(egui::Key::Enter));
                let shift = ui.input(|input| input.modifiers.shift);
                let running = state.running;
                let ready = state.online && can_send(state.draft, state.attachments.len(), running);
                if send_key_submits(EnterKeyState {
                    focused: response.has_focus(),
                    enter_pressed,
                    shift,
                    ime_active: quiet_input,
                    ready,
                }) {
                    action = Some(ComposerAction::Send);
                }

                // Bottom control baseline.
                ui.with_layout(Layout::left_to_right(Align::Center), |ui| {
                    if icon_control(
                        ui,
                        Icon::Plus,
                        "Attach images · PNG, JPEG, GIF, WebP",
                        state.online
                            && state
                                .providers
                                .iter()
                                .find(|p| p.id == state.provider_id)
                                .is_some_and(|p| p.supports("images")),
                    )
                    .clicked()
                    {
                        action = Some(ComposerAction::Attach);
                    }
                    model_selector(ui, state, &mut action);
                    config_selector(ui, state, &mut action);
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
                        context_meter(ui, state);
                    });
                });
            },
        );
    });
    action
}

fn truncate_name(name: &str, limit: usize) -> String {
    if name.chars().count() <= limit {
        return name.to_owned();
    }
    let mut name: String = name.chars().take(limit.saturating_sub(1)).collect();
    name.push('…');
    name
}

fn model_selector(
    ui: &mut egui::Ui,
    state: &ComposerState<'_>,
    action: &mut Option<ComposerAction>,
) {
    let provider_id = state.provider_id;
    let provider = state
        .providers
        .iter()
        .find(|provider| provider.id == provider_id);
    let model_label = state
        .session
        .and_then(|session| session.model.as_deref())
        .filter(|model| !model.is_empty())
        .or(state.remembered_model)
        .unwrap_or("Default model");
    let provider_label = provider.map_or(provider_id, |provider| provider.name.as_str());
    let display_model = provider
        .and_then(|provider| provider.models.iter().find(|model| model.id == model_label))
        .map_or_else(
            || model_label.rsplit('/').next().unwrap_or(model_label),
            |model| model.label.as_str(),
        );
    let response = chip(
        ui,
        Icon::Sparkle,
        theme::MUTED,
        display_model,
        state.online && !state.running && state.session.is_none_or(|s| !s.archived),
    )
    .on_hover_text(format!("{provider_label} · {model_label}"));
    let picker_id = egui::Id::unique(("model-picker-state", state.session_id));
    let mut picker = ui.ctx().data_mut(|data| {
        data.get_temp::<ModelPicker>(picker_id)
            .unwrap_or_else(|| ModelPicker {
                provider: provider_id.to_owned(),
                query: String::new(),
            })
    });
    let max_width = (ui.ctx().content_rect().width() - 32.0).clamp(260.0, 600.0);
    egui::Popup::menu(&response)
        .id(egui::Id::unique("model-menu"))
        .show(|ui| {
            ui.set_width(max_width);
            ui.spacing_mut().item_spacing = Vec2::new(4.0, 4.0);
            let search = ui.add(
                egui::TextEdit::singleline(&mut picker.query)
                    .hint_text("Search models…")
                    .desired_width(f32::INFINITY)
                    .font(theme::ui(14.0))
                    .frame(egui::Frame::NONE),
            );
            if response.clicked() {
                search.request_focus();
            }
            ui.add_space(6.0);
            ui.separator();
            let list_height = (ui.ctx().content_rect().height() - 110.0).clamp(90.0, 300.0);
            let query = picker.query.trim().to_lowercase();
            if !state.providers.iter().any(|p| p.id == picker.provider)
                && let Some(provider) = state.providers.first() {
                picker.provider.clone_from(&provider.id);
            }
            let sidebar_width = (max_width * 0.30).clamp(90.0, 170.0);
            ui.horizontal_top(|ui| {
                ui.allocate_ui_with_layout(
                    Vec2::new(sidebar_width, list_height + 30.0),
                    Layout::top_down(Align::Min),
                    |ui| {
                        ui.set_width(sidebar_width);
                        for provider in state.providers {
                            let selected = picker.provider == provider.id;
                            let response = ui.add_sized(
                                [sidebar_width, 38.0],
                                egui::Button::new(RichText::new(&provider.name)
                                    .font(theme::ui(13.0))
                                    .color(if selected { theme::TEXT } else { theme::MUTED }))
                                    .fill(if selected { theme::SELECTED } else { Color32::TRANSPARENT })
                                    .stroke(Stroke::NONE)
                                    .corner_radius(8)
                                    .wrap_mode(egui::TextWrapMode::Truncate),
                            );
                            if response.clicked() { picker.provider.clone_from(&provider.id); }
                            if !provider.available { response.on_hover_text("Provider unavailable"); }
                        }
                    },
                );
                ui.separator();
                ui.vertical(|ui| {
                    ui.set_width((max_width - sidebar_width - 24.0).max(100.0));
                    if let Some(provider) = state.providers.iter().find(|p| p.id == picker.provider) {
                        let models: Vec<_> = provider.models.iter().filter(|m| model_matches(&query, &m.label, &m.id)).collect();
                        ui.horizontal(|ui| {
                            ui.label(RichText::new(&provider.name).font(theme::medium(13.0)).color(theme::TEXT));
                            ui.label(RichText::new(format!("{} models", models.len())).font(theme::ui(11.0)).color(theme::MUTED));
                        });
                        ui.add_space(8.0);
                        egui::ScrollArea::vertical().id_salt("model-picker-list").max_height(list_height).show(ui, |ui| {
                            ui.set_min_width(ui.available_width());
                            if !provider.available {
                                ui.label(RichText::new("Provider unavailable").font(theme::ui(12.0)).color(theme::MUTED));
                            }
                            if models.is_empty() {
                                ui.label(RichText::new(if query.is_empty() { "No models reported yet" } else { "No matching models" })
                                    .font(theme::ui(12.0)).color(theme::MUTED));
                            }
                            for model in models {
                                let current_provider = provider.id == provider_id;
                                let selected = current_provider && model.id == model_label;
                                let enabled = state.online && provider.available
                                    && (current_provider || state.session.is_none())
                                    && provider.supports("model_selection") && !state.running
                                    && state.session.is_none_or(|s| !s.archived);
                                let mut label = egui::text::LayoutJob::default();
                                label.append(&format!("{}  {}", if selected { "✓" } else { " " }, model.label),
                                    0.0, egui::TextFormat { font_id: theme::ui(13.0), color: theme::TEXT, ..Default::default() });
                                label.append(&format!("  {}", model.id), 0.0,
                                    egui::TextFormat { font_id: theme::ui(11.5), color: theme::MUTED, ..Default::default() });
                                let response = ui.add_enabled(enabled,
                                    egui::Button::new(label)
                                        .min_size(Vec2::new(ui.available_width(), 38.0))
                                        .fill(if selected { theme::SELECTED } else { Color32::TRANSPARENT })
                                        .stroke(Stroke::NONE)
                                        .corner_radius(8)
                                        .wrap_mode(egui::TextWrapMode::Truncate))
                                    .on_hover_text(if !current_provider && state.session.is_some() {
                                        format!("{}\nStart a new conversation to change provider.", model.id)
                                    } else { model.id.clone() });
                                if response.clicked() {
                                    *action = Some(ComposerAction::SetModel { provider: provider.id.clone(), model: model.id.clone() });
                                    ui.close();
                                }
                            }
                        });
                    }
                });
            });
        });
    ui.ctx()
        .data_mut(|data| data.insert_temp(picker_id, picker));
}

#[derive(Clone)]
struct ModelPicker {
    provider: String,
    query: String,
}

fn model_matches(query: &str, label: &str, id: &str) -> bool {
    query.is_empty() || label.to_lowercase().contains(query) || id.to_lowercase().contains(query)
}

/// A small icon + label chip with a smooth hover wash.
fn chip(
    ui: &mut egui::Ui,
    icon: Icon,
    icon_color: Color32,
    label: &str,
    interactive: bool,
) -> Response {
    let label = if ui.available_width() < 390.0 {
        "Model"
    } else {
        label
    };
    let galley = ui.painter().layout(
        label.to_owned(),
        theme::medium(12.0),
        theme::SECONDARY,
        (ui.available_width() - 160.0).clamp(30.0, 240.0),
    );
    let size = Vec2::new(
        (galley.size().x + 46.0)
            .min((ui.available_width() - 90.0).max(80.0))
            .min(300.0),
        36.0,
    );
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
    response.widget_info(|| egui::WidgetInfo::labeled(egui::Role::Button, interactive, label));
    response
}

fn context_meter(ui: &mut egui::Ui, state: &ComposerState<'_>) {
    let usage = state.usage;
    let fraction = usage.and_then(|usage| match (usage.context_used, usage.context_size) {
        (Some(used), Some(size)) if size > 0 => Some(used as f32 / size as f32),
        _ => None,
    });
    if fraction.is_none() {
        return;
    }
    let (rect, response) = ui.allocate_exact_size(Vec2::splat(26.0), Sense::hover());
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
    let (rect, response) = ui.allocate_exact_size(
        Vec2::splat(32.0),
        if ready || running {
            Sense::click()
        } else {
            Sense::hover()
        },
    );
    response.widget_info(|| {
        egui::WidgetInfo::labeled(
            egui::Role::Button,
            ready || running,
            if running { "Stop run" } else { "Send prompt" },
        )
    });
    if response.has_focus() {
        ui.painter()
            .circle_stroke(rect.center(), 21.0, Stroke::new(2.0, theme::BLUE));
    }
    let hover =
        ui.ctx()
            .animate_bool_with_time(egui::Id::unique("send-hover"), response.hovered(), 0.09);
    let fill = if running {
        theme::RED
    } else if ready {
        theme::mix(theme::GREEN, theme::TEXT, hover * 0.15)
    } else {
        theme::SURFACE_HIGH
    };
    let painter = ui.painter();
    let color = if ready || running {
        Color32::from_rgb(13, 18, 22)
    } else {
        theme::FAINT
    };
    painter.rect_filled(rect.shrink(2.0), 9.0, fill);
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
    let (rect, response) = ui.allocate_exact_size(
        Vec2::splat(36.0),
        if enabled {
            Sense::click()
        } else {
            Sense::hover()
        },
    );
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
    response.widget_info(|| egui::WidgetInfo::labeled(egui::Role::Button, enabled, tip));
    if response.has_focus() {
        ui.painter().rect_stroke(
            rect,
            7.0,
            Stroke::new(2.0, theme::BLUE),
            egui::StrokeKind::Inside,
        );
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

fn config_selector(
    ui: &mut egui::Ui,
    state: &ComposerState<'_>,
    action: &mut Option<ComposerAction>,
) {
    let Some(session) = state.session else { return };
    let Some(provider) = state.providers.iter().find(|p| p.id == session.provider) else {
        return;
    };
    if !provider.supports("modes") && !provider.supports("config_options") {
        return;
    }
    ui.add_enabled_ui(state.online && !state.running && !session.archived, |ui| {
        ui.menu_button("Options", |ui| {
            if provider.supports("modes") {
                ui.label("Session mode");
                for (id, label) in &provider.modes {
                    if ui
                        .selectable_label(session.mode.as_ref() == Some(id), label)
                        .clicked()
                    {
                        *action = Some(ComposerAction::SetMode(id.clone()));
                        ui.close();
                    }
                }
            }
            if provider.supports("config_options") {
                for option in state.config_options.as_array().into_iter().flatten() {
                    let (Some(id), Some(name)) = (option["id"].as_str(), option["name"].as_str())
                    else {
                        continue;
                    };
                    ui.label(name);
                    for choice in option["options"].as_array().into_iter().flatten() {
                        let Some(value) = choice["value"].as_str() else {
                            continue;
                        };
                        if ui
                            .selectable_label(
                                option["currentValue"] == value,
                                choice["name"].as_str().unwrap_or(value),
                            )
                            .clicked()
                        {
                            *action = Some(ComposerAction::SetConfig {
                                option: id.into(),
                                value: value.into(),
                            });
                            ui.close();
                        }
                    }
                }
            }
        });
    });
}

#[cfg(test)]
mod picker_tests {
    use super::model_matches;

    #[test]
    fn search_matches_model_labels_and_ids() {
        assert!(model_matches("luna", "GPT Luna", "codex/model"));
        assert!(model_matches("codex", "GPT Luna", "openai-codex/model"));
        assert!(model_matches("", "GPT Luna", "codex/model"));
        assert!(!model_matches("claude", "GPT Luna", "codex/model"));
    }
}
