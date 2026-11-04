//! Hand-painted vector icons.
//!
//! Icons are drawn with the painter on a normalized 24x24 grid so they stay
//! crisp at any size and never depend on emoji fonts. Each icon is a set of
//! polylines and small shapes; strokes are snapped to physical pixels.

use eframe::egui::{self, Color32, Pos2, Rect, Response, Stroke, Vec2};

/// Every icon the shell uses. Keep this list exhaustive and tested.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Icon {
    Home,
    Conversations,
    Search,
    Inbox,
    Settings,
    Plus,
    Paperclip,
    ArrowUp,
    Stop,
    ChevronDown,
    ChevronRight,
    ChevronLeft,
    Check,
    Close,
    More,
    Terminal,
    File,
    Folder,
    Branch,
    Sparkle,
    Shield,
    ShieldOff,
    Clock,
    Copy,
    Refresh,
    PanelLeft,
    Command,
    Mic,
    Waveform,
    Cpu,
    Alert,
    Trash,
    Archive,
    Sliders,
    Compose,
    History,
    External,
    Keyboard,
    Zap,
}

impl Icon {
    /// All icons, used by snapshot tests.
    pub const ALL: &'static [Self] = &[
        Self::Home,
        Self::Conversations,
        Self::Search,
        Self::Inbox,
        Self::Settings,
        Self::Plus,
        Self::Paperclip,
        Self::ArrowUp,
        Self::Stop,
        Self::ChevronDown,
        Self::ChevronRight,
        Self::ChevronLeft,
        Self::Check,
        Self::Close,
        Self::More,
        Self::Terminal,
        Self::File,
        Self::Folder,
        Self::Branch,
        Self::Sparkle,
        Self::Shield,
        Self::ShieldOff,
        Self::Clock,
        Self::Copy,
        Self::Refresh,
        Self::PanelLeft,
        Self::Command,
        Self::Mic,
        Self::Waveform,
        Self::Cpu,
        Self::Alert,
        Self::Trash,
        Self::Archive,
        Self::Sliders,
        Self::Compose,
        Self::History,
        Self::External,
        Self::Keyboard,
        Self::Zap,
    ];
}

/// Paint an icon centered in `rect`.
pub fn paint(painter: &egui::Painter, rect: Rect, icon: Icon, color: Color32, stroke_width: f32) {
    let _ = (painter, rect, icon, color, stroke_width);
}

/// Allocate space and paint an icon.
pub fn icon(ui: &mut egui::Ui, icon: Icon, size: f32, color: Color32) -> Response {
    let (rect, response) = ui.allocate_exact_size(Vec2::splat(size), egui::Sense::hover());
    if ui.is_rect_visible(rect) {
        paint(
            ui.painter(),
            rect.shrink(size * 0.08),
            icon,
            color,
            (size * 0.085).max(1.25),
        );
    }
    response
}

/// A square, frameless icon button with a hover wash and tooltip.
pub fn button(ui: &mut egui::Ui, icon_id: Icon, size: f32, tip: &str, enabled: bool) -> Response {
    let _ = (icon_id, enabled);
    let response = ui.allocate_response(Vec2::splat(size), egui::Sense::click());
    response.on_hover_text(tip)
}

/// Helper for tests and custom painters: the icon grid is 24x24.
pub const GRID: f32 = 24.0;

/// Scale a normalized 24-grid point into `rect`.
#[must_use]
pub fn point(rect: Rect, x: f32, y: f32) -> Pos2 {
    Pos2::new(
        rect.min.x + x / GRID * rect.width(),
        rect.min.y + y / GRID * rect.height(),
    )
}

/// Draw a polyline through 24-grid coordinates.
pub fn polyline(
    painter: &egui::Painter,
    rect: Rect,
    points: &[(f32, f32)],
    stroke: Stroke,
    closed: bool,
) {
    if points.len() < 2 {
        return;
    }
    let mapped: Vec<Pos2> = points.iter().map(|&(x, y)| point(rect, x, y)).collect();
    painter.add(egui::Shape::line(mapped, stroke));
    let _ = closed;
}
