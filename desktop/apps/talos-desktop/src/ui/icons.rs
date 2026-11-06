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

/// Paint a consistent 24-grid outline icon centered in `rect`.
pub fn paint(painter: &egui::Painter, rect: Rect, icon: Icon, color: Color32, stroke_width: f32) {
    let stroke = Stroke::new(stroke_width, color);
    let paths: &[&[(f32, f32)]] = match icon {
        Icon::Home => &[
            &[(3.0, 10.0), (12.0, 3.0), (21.0, 10.0)],
            &[(5.0, 9.0), (5.0, 21.0), (19.0, 21.0), (19.0, 9.0)],
            &[(9.0, 21.0), (9.0, 14.0), (15.0, 14.0), (15.0, 21.0)],
        ],
        Icon::Conversations => &[
            &[
                (4.0, 5.0),
                (20.0, 5.0),
                (20.0, 17.0),
                (12.0, 17.0),
                (7.0, 21.0),
                (7.0, 17.0),
                (4.0, 17.0),
                (4.0, 5.0),
            ],
            &[(8.0, 9.0), (16.0, 9.0)],
            &[(8.0, 13.0), (14.0, 13.0)],
        ],
        Icon::Search => &[
            &[
                (10.5, 3.5),
                (15.0, 4.5),
                (18.0, 8.0),
                (18.0, 12.5),
                (15.0, 16.0),
                (10.5, 17.0),
                (6.0, 16.0),
                (3.0, 12.5),
                (3.0, 8.0),
                (6.0, 4.5),
                (10.5, 3.5),
            ],
            &[(15.0, 15.0), (21.0, 21.0)],
        ],
        Icon::Inbox => &[
            &[
                (3.0, 12.0),
                (7.0, 12.0),
                (9.0, 16.0),
                (15.0, 16.0),
                (17.0, 12.0),
                (21.0, 12.0),
                (19.0, 21.0),
                (5.0, 21.0),
                (3.0, 12.0),
            ],
            &[(5.0, 12.0), (7.0, 4.0), (17.0, 4.0), (19.0, 12.0)],
        ],
        Icon::Settings | Icon::Sliders => &[
            &[(4.0, 6.0), (20.0, 6.0)],
            &[(4.0, 12.0), (20.0, 12.0)],
            &[(4.0, 18.0), (20.0, 18.0)],
            &[(9.0, 4.0), (9.0, 8.0)],
            &[(15.0, 10.0), (15.0, 14.0)],
            &[(7.0, 16.0), (7.0, 20.0)],
        ],
        Icon::Plus => &[&[(12.0, 5.0), (12.0, 19.0)], &[(5.0, 12.0), (19.0, 12.0)]],
        Icon::Paperclip => &[
            &[
                (8.0, 12.0),
                (8.0, 6.0),
                (11.0, 3.0),
                (16.0, 3.0),
                (19.0, 6.0),
                (19.0, 15.0),
                (15.0, 20.0),
                (10.0, 21.0),
                (5.0, 18.0),
                (4.0, 14.0),
                (4.0, 8.0),
            ],
            &[
                (12.0, 7.0),
                (12.0, 15.0),
                (14.0, 17.0),
                (16.0, 15.0),
                (16.0, 7.0),
            ],
        ],
        Icon::ArrowUp => &[
            &[(12.0, 20.0), (12.0, 4.0)],
            &[(5.0, 11.0), (12.0, 4.0), (19.0, 11.0)],
        ],
        Icon::Stop => &[&[
            (7.0, 7.0),
            (17.0, 7.0),
            (17.0, 17.0),
            (7.0, 17.0),
            (7.0, 7.0),
        ]],
        Icon::ChevronDown => &[&[(5.0, 9.0), (12.0, 16.0), (19.0, 9.0)]],
        Icon::ChevronRight => &[&[(9.0, 5.0), (16.0, 12.0), (9.0, 19.0)]],
        Icon::ChevronLeft => &[&[(15.0, 5.0), (8.0, 12.0), (15.0, 19.0)]],
        Icon::Check => &[&[(4.0, 12.0), (10.0, 18.0), (20.0, 6.0)]],
        Icon::Close => &[&[(6.0, 6.0), (18.0, 18.0)], &[(18.0, 6.0), (6.0, 18.0)]],
        Icon::More => &[
            &[(5.0, 12.0), (5.2, 12.0)],
            &[(12.0, 12.0), (12.2, 12.0)],
            &[(19.0, 12.0), (19.2, 12.0)],
        ],
        Icon::Terminal => &[
            &[
                (4.0, 5.0),
                (20.0, 5.0),
                (20.0, 19.0),
                (4.0, 19.0),
                (4.0, 5.0),
            ],
            &[(7.0, 9.0), (10.0, 12.0), (7.0, 15.0)],
            &[(12.0, 15.0), (17.0, 15.0)],
        ],
        Icon::File => &[
            &[
                (6.0, 3.0),
                (14.0, 3.0),
                (19.0, 8.0),
                (19.0, 21.0),
                (6.0, 21.0),
                (6.0, 3.0),
            ],
            &[(14.0, 3.0), (14.0, 8.0), (19.0, 8.0)],
        ],
        Icon::Folder => &[&[
            (3.0, 6.0),
            (10.0, 6.0),
            (12.0, 9.0),
            (21.0, 9.0),
            (19.0, 19.0),
            (4.0, 19.0),
            (3.0, 6.0),
        ]],
        Icon::Branch => &[
            &[(7.0, 5.0), (7.0, 19.0)],
            &[(7.0, 8.0), (15.0, 8.0), (17.0, 10.0), (17.0, 14.0)],
            &[(7.0, 14.0), (13.0, 14.0)],
        ],
        Icon::Sparkle | Icon::Zap => &[&[
            (12.0, 2.0),
            (14.0, 9.0),
            (21.0, 12.0),
            (14.0, 14.0),
            (12.0, 22.0),
            (10.0, 14.0),
            (3.0, 12.0),
            (10.0, 9.0),
            (12.0, 2.0),
        ]],
        Icon::Shield | Icon::ShieldOff => &[
            &[
                (12.0, 3.0),
                (20.0, 6.0),
                (19.0, 14.0),
                (16.0, 19.0),
                (12.0, 21.0),
                (8.0, 19.0),
                (5.0, 14.0),
                (4.0, 6.0),
                (12.0, 3.0),
            ],
            &[(8.0, 12.0), (11.0, 15.0), (16.0, 9.0)],
        ],
        Icon::Clock => &[
            &[
                (12.0, 3.0),
                (17.0, 4.0),
                (20.0, 8.0),
                (21.0, 13.0),
                (19.0, 18.0),
                (15.0, 21.0),
                (9.0, 21.0),
                (5.0, 18.0),
                (3.0, 13.0),
                (4.0, 8.0),
                (7.0, 4.0),
                (12.0, 3.0),
            ],
            &[(12.0, 7.0), (12.0, 12.0), (16.0, 14.0)],
        ],
        Icon::Copy => &[
            &[
                (8.0, 8.0),
                (19.0, 8.0),
                (19.0, 21.0),
                (8.0, 21.0),
                (8.0, 8.0),
            ],
            &[
                (15.0, 8.0),
                (15.0, 4.0),
                (4.0, 4.0),
                (4.0, 17.0),
                (8.0, 17.0),
            ],
        ],
        Icon::Refresh => &[
            &[(19.0, 9.0), (19.0, 4.0), (14.0, 4.0)],
            &[
                (19.0, 5.0),
                (16.0, 4.0),
                (12.0, 3.0),
                (7.0, 5.0),
                (4.0, 10.0),
                (4.0, 15.0),
                (7.0, 19.0),
                (12.0, 21.0),
                (17.0, 19.0),
                (20.0, 15.0),
            ],
        ],
        Icon::PanelLeft => &[
            &[
                (4.0, 4.0),
                (20.0, 4.0),
                (20.0, 20.0),
                (4.0, 20.0),
                (4.0, 4.0),
            ],
            &[(9.0, 4.0), (9.0, 20.0)],
        ],
        Icon::Command => &[&[(8.0, 8.0), (16.0, 16.0)], &[(16.0, 8.0), (8.0, 16.0)]],
        Icon::Mic => &[
            &[
                (9.0, 4.0),
                (9.0, 13.0),
                (11.0, 16.0),
                (14.0, 16.0),
                (16.0, 13.0),
                (16.0, 4.0),
                (14.0, 2.0),
                (11.0, 2.0),
                (9.0, 4.0),
            ],
            &[
                (5.0, 11.0),
                (5.0, 14.0),
                (8.0, 18.0),
                (12.0, 20.0),
                (16.0, 18.0),
                (19.0, 14.0),
                (19.0, 11.0),
            ],
            &[(12.0, 20.0), (12.0, 23.0)],
        ],
        Icon::Waveform => &[&[
            (3.0, 12.0),
            (6.0, 12.0),
            (8.0, 6.0),
            (11.0, 18.0),
            (14.0, 4.0),
            (17.0, 19.0),
            (19.0, 12.0),
            (21.0, 12.0),
        ]],
        Icon::Cpu => &[
            &[
                (6.0, 6.0),
                (18.0, 6.0),
                (18.0, 18.0),
                (6.0, 18.0),
                (6.0, 6.0),
            ],
            &[
                (9.0, 9.0),
                (15.0, 9.0),
                (15.0, 15.0),
                (9.0, 15.0),
                (9.0, 9.0),
            ],
            &[(9.0, 2.0), (9.0, 6.0)],
            &[(15.0, 2.0), (15.0, 6.0)],
            &[(9.0, 18.0), (9.0, 22.0)],
            &[(15.0, 18.0), (15.0, 22.0)],
        ],
        Icon::Alert => &[
            &[(12.0, 3.0), (22.0, 20.0), (2.0, 20.0), (12.0, 3.0)],
            &[(12.0, 9.0), (12.0, 14.0)],
            &[(12.0, 17.0), (12.2, 17.0)],
        ],
        Icon::Trash => &[
            &[(5.0, 7.0), (19.0, 7.0)],
            &[(9.0, 7.0), (9.0, 4.0), (15.0, 4.0), (15.0, 7.0)],
            &[(7.0, 7.0), (8.0, 20.0), (16.0, 20.0), (17.0, 7.0)],
            &[(10.0, 10.0), (10.0, 17.0)],
            &[(14.0, 10.0), (14.0, 17.0)],
        ],
        Icon::Archive => &[
            &[(4.0, 5.0), (20.0, 5.0), (20.0, 9.0), (4.0, 9.0), (4.0, 5.0)],
            &[(6.0, 9.0), (7.0, 20.0), (17.0, 20.0), (18.0, 9.0)],
            &[(9.0, 13.0), (15.0, 13.0)],
        ],
        Icon::Compose => &[
            &[
                (13.0, 5.0),
                (5.0, 5.0),
                (4.0, 20.0),
                (19.0, 19.0),
                (19.0, 12.0),
            ],
            &[
                (12.0, 17.0),
                (20.0, 9.0),
                (15.0, 5.0),
                (7.0, 13.0),
                (6.0, 18.0),
                (12.0, 17.0),
            ],
        ],
        Icon::History => &[
            &[
                (12.0, 4.0),
                (17.0, 5.0),
                (20.0, 9.0),
                (20.0, 15.0),
                (17.0, 19.0),
                (12.0, 20.0),
                (7.0, 19.0),
                (4.0, 15.0),
                (4.0, 9.0),
                (7.0, 5.0),
                (12.0, 4.0),
            ],
            &[(12.0, 7.0), (12.0, 12.0), (16.0, 14.0)],
        ],
        Icon::External => &[
            &[(13.0, 4.0), (20.0, 4.0), (20.0, 11.0)],
            &[(20.0, 4.0), (11.0, 13.0)],
            &[
                (18.0, 13.0),
                (18.0, 20.0),
                (4.0, 20.0),
                (4.0, 6.0),
                (11.0, 6.0),
            ],
        ],
        Icon::Keyboard => &[
            &[
                (3.0, 6.0),
                (21.0, 6.0),
                (21.0, 18.0),
                (3.0, 18.0),
                (3.0, 6.0),
            ],
            &[(6.0, 10.0), (6.2, 10.0)],
            &[(10.0, 10.0), (10.2, 10.0)],
            &[(14.0, 10.0), (14.2, 10.0)],
            &[(18.0, 10.0), (18.2, 10.0)],
            &[(6.0, 14.0), (18.0, 14.0)],
        ],
    };
    for path in paths {
        polyline(painter, rect, path, stroke, false);
    }
    if icon == Icon::More || icon == Icon::Search || icon == Icon::Inbox {
        let dots: &[(f32, f32, f32)] = match icon {
            Icon::More => &[(5.0, 12.0, 1.0), (12.0, 12.0, 1.0), (19.0, 12.0, 1.0)],
            Icon::Search => &[(8.0, 8.0, 0.4)],
            _ => &[(12.0, 10.0, 1.0)],
        };
        for &(x, y, radius) in dots {
            painter.circle_filled(point(rect, x, y), radius * rect.width() / GRID, color);
        }
    }
}

#[must_use]
pub fn icon_label(icon: Icon) -> &'static str {
    match icon {
        Icon::Home => "Home",
        Icon::Conversations => "Conversations",
        Icon::Search => "Search",
        Icon::Inbox => "Inbox",
        Icon::Settings => "Settings",
        Icon::Plus => "Add",
        Icon::Paperclip => "Attach",
        Icon::ArrowUp => "Send",
        Icon::Stop => "Stop",
        Icon::ChevronDown => "Expand",
        Icon::ChevronRight => "Next",
        Icon::ChevronLeft => "Previous",
        Icon::Check => "Done",
        Icon::Close => "Close",
        Icon::More => "More",
        Icon::Terminal => "Terminal",
        Icon::File => "File",
        Icon::Folder => "Folder",
        Icon::Branch => "Branch",
        Icon::Sparkle => "Assistant",
        Icon::Shield => "Permission",
        Icon::ShieldOff => "Permission denied",
        Icon::Clock => "Time",
        Icon::Copy => "Copy",
        Icon::Refresh => "Refresh",
        Icon::PanelLeft => "Sidebar",
        Icon::Command => "Command",
        Icon::Mic => "Microphone",
        Icon::Waveform => "Activity",
        Icon::Cpu => "Usage",
        Icon::Alert => "Alert",
        Icon::Trash => "Delete",
        Icon::Archive => "Archive",
        Icon::Sliders => "Options",
        Icon::Compose => "Compose",
        Icon::History => "History",
        Icon::External => "Open externally",
        Icon::Keyboard => "Keyboard",
        Icon::Zap => "Action",
    }
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

/// A square, frameless icon button with a hover wash and accessible label.
pub fn button(ui: &mut egui::Ui, icon_id: Icon, size: f32, tip: &str, enabled: bool) -> Response {
    let (rect, response) = ui.allocate_exact_size(
        Vec2::splat(size),
        if enabled {
            egui::Sense::click()
        } else {
            egui::Sense::hover()
        },
    );
    let response = response.on_hover_text(tip);
    response.widget_info(|| egui::WidgetInfo::labeled(egui::Role::Button, enabled, tip));
    if response.hovered() || response.has_focus() {
        ui.painter()
            .rect_filled(rect.shrink(2.0), 7.0, Color32::from_rgb(38, 45, 44));
    }
    if response.has_focus() {
        ui.painter().rect_stroke(
            rect.shrink(1.0),
            7.0,
            Stroke::new(2.0, Color32::from_rgb(108, 168, 240)),
            egui::StrokeKind::Inside,
        );
    }
    paint(
        ui.painter(),
        rect.shrink((size - 18.0).max(4.0) * 0.5),
        icon_id,
        Color32::from_rgb(168, 180, 178),
        1.6,
    );
    response
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn icon_catalog_is_complete_and_unique() {
        assert_eq!(Icon::ALL.len(), 39);
        let unique: std::collections::HashSet<_> = Icon::ALL.iter().collect();
        assert_eq!(unique.len(), Icon::ALL.len());
    }
}
