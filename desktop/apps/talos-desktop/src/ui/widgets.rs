//! Small composable widgets shared by the shell.

use eframe::egui::{self, Color32, Response, RichText};

use crate::theme;

/// A circular presence dot, optionally pulsing.
pub fn status_dot(
    ui: &mut egui::Ui,
    color: Color32,
    pulsing: bool,
    motion: bool,
    id: egui::Id,
) -> Response {
    let _ = (pulsing, motion, id);
    let (rect, response) = ui.allocate_exact_size(egui::Vec2::splat(8.0), egui::Sense::hover());
    if ui.is_rect_visible(rect) {
        ui.painter().circle_filled(rect.center(), 3.5, color);
    }
    response
}

/// A small rounded label chip.
pub fn pill(ui: &mut egui::Ui, text: impl Into<String>, color: Color32) -> Response {
    let text = text.into();
    let galley = ui
        .painter()
        .layout_no_wrap(text, theme::medium(11.0), color);
    let size = galley.size() + egui::vec2(14.0, 6.0);
    let (rect, response) = ui.allocate_exact_size(size, egui::Sense::hover());
    if ui.is_rect_visible(rect) {
        ui.painter()
            .rect_filled(rect, egui::CornerRadius::same(6), theme::SURFACE_HIGH);
        ui.painter()
            .galley(rect.center() - galley.size() * 0.5, galley, color);
    }
    response
}

/// An uppercase section label used by the sidebar.
pub fn section_label(ui: &mut egui::Ui, text: &str) {
    ui.add_space(6.0);
    ui.label(
        RichText::new(text)
            .font(theme::semibold(10.5))
            .color(theme::FAINT),
    );
    ui.add_space(2.0);
}

/// A hairline separator inset from the panel edges.
pub fn divider(ui: &mut egui::Ui) {
    ui.add_space(4.0);
    let width = ui.available_width();
    let (rect, _) = ui.allocate_exact_size(egui::vec2(width, 1.0), egui::Sense::hover());
    ui.painter().hline(
        rect.x_range(),
        rect.center().y,
        egui::Stroke::new(1.0, theme::LINE),
    );
    ui.add_space(4.0);
}

/// A keyboard hint chip, e.g. `⌘K`.
pub fn kbd(ui: &mut egui::Ui, label: &str) {
    let galley = ui
        .painter()
        .layout_no_wrap(label.to_owned(), theme::mono(10.5), theme::MUTED);
    let size = galley.size() + egui::vec2(10.0, 4.0);
    let (rect, _) = ui.allocate_exact_size(size, egui::Sense::hover());
    if ui.is_rect_visible(rect) {
        ui.painter()
            .rect_filled(rect, egui::CornerRadius::same(5), theme::SURFACE);
        ui.painter().rect_stroke(
            rect,
            egui::CornerRadius::same(5),
            egui::Stroke::new(1.0, theme::LINE),
            egui::StrokeKind::Inside,
        );
        ui.painter()
            .galley(rect.center() - galley.size() * 0.5, galley, theme::MUTED);
    }
}
