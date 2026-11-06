//! Design tokens and egui styling for the Talos desktop shell.
//!
//! Every color, radius, spacing step and motion duration used by the UI lives
//! here so the visual language stays consistent across views. The palette is a
//! near-black cockpit with a soft blue primary accent and a warm provider mark.

use std::sync::Arc;

use eframe::egui::{
    self, Color32, CornerRadius, FontData, FontDefinitions, FontFamily, FontId, Stroke, TextStyle,
};

// ---------------------------------------------------------------------------
// Palette
// ---------------------------------------------------------------------------

/// Window canvas, the deepest layer.
pub const CANVAS: Color32 = Color32::from_rgb(16, 19, 18);
/// Navigation rail; one step above the canvas.
pub const RAIL: Color32 = Color32::from_rgb(19, 23, 22);
/// Sidebar / history panel surface.
pub const SIDEBAR: Color32 = Color32::from_rgb(21, 25, 24);
/// Raised card surface (tool cards, user messages).
pub const SURFACE: Color32 = Color32::from_rgb(27, 32, 31);
/// Higher emphasis surface (menus, popovers, chips).
pub const SURFACE_HIGH: Color32 = Color32::from_rgb(33, 39, 38);
/// Hovered surface.
pub const HOVER: Color32 = Color32::from_rgb(38, 45, 44);
/// Selected surface with a blue wash.
pub const SELECTED: Color32 = Color32::from_rgb(30, 42, 54);
/// Code block / inline code surface.
pub const CODE: Color32 = Color32::from_rgb(24, 29, 28);

// Premultiplied white hairlines: rgb == alpha for a neutral translucent line.
pub const LINE: Color32 = Color32::from_rgba_premultiplied(18, 18, 18, 18);
pub const LINE_STRONG: Color32 = Color32::from_rgba_premultiplied(28, 28, 28, 28);

pub const TEXT: Color32 = Color32::from_rgb(236, 240, 239);
pub const SECONDARY: Color32 = Color32::from_rgb(168, 180, 178);
pub const MUTED: Color32 = Color32::from_rgb(124, 137, 135);
pub const FAINT: Color32 = Color32::from_rgb(92, 104, 102);

/// Primary accent: a calm, slightly cool blue.
pub const BLUE: Color32 = Color32::from_rgb(108, 168, 240);
pub const BLUE_HOVER: Color32 = Color32::from_rgb(140, 192, 247);
pub const BLUE_PRESSED: Color32 = Color32::from_rgb(76, 143, 216);
pub const BLUE_DIM: Color32 = Color32::from_rgb(45, 74, 104);
/// Very low-alpha white used for hairlines over raised surfaces.
pub const HAIRLINE: Color32 = Color32::from_rgba_premultiplied(14, 14, 14, 14);

/// Warm provider mark. Used only for the provider glyph, never for status.
pub const ORANGE: Color32 = Color32::from_rgb(219, 156, 108);
pub const GREEN: Color32 = Color32::from_rgb(111, 191, 155);
pub const AMBER: Color32 = Color32::from_rgb(216, 166, 87);
pub const RED: Color32 = Color32::from_rgb(224, 115, 106);

// ---------------------------------------------------------------------------
// Geometry
// ---------------------------------------------------------------------------

pub const RAIL_WIDTH: f32 = 60.0;
pub const SIDEBAR_MIN: f32 = 244.0;
pub const SIDEBAR_MAX: f32 = 340.0;
pub const SIDEBAR_DEFAULT: f32 = 286.0;
pub const READING_MAX: f32 = 760.0;
pub const COMPOSER_MAX: f32 = 880.0;
pub const COMPOSER_RADIUS: u8 = 22;
pub const CARD_RADIUS: u8 = 14;
pub const CHIP_RADIUS: u8 = 8;

pub const GAP_XS: f32 = 4.0;
pub const GAP_S: f32 = 8.0;
pub const GAP_M: f32 = 12.0;
pub const GAP_L: f32 = 16.0;
pub const GAP_XL: f32 = 24.0;

// ---------------------------------------------------------------------------
// Motion
// ---------------------------------------------------------------------------

pub const MOTION_FAST: f32 = 0.10;
pub const MOTION_BASE: f32 = 0.16;
pub const MOTION_SLOW: f32 = 0.22;

// ---------------------------------------------------------------------------
// Fonts
// ---------------------------------------------------------------------------

pub const FAMILY_UI: &str = "inter";
pub const FAMILY_MEDIUM: &str = "inter-medium";
pub const FAMILY_SEMIBOLD: &str = "inter-semibold";
pub const FAMILY_BOLD: &str = "inter-bold";
pub const FAMILY_MONO: &str = "jetbrains-mono";
pub const FAMILY_MONO_MEDIUM: &str = "jetbrains-mono-medium";

/// Install fonts and the global visual style. Idempotent per frame.
pub fn install(ctx: &egui::Context, high_contrast: bool) {
    install_fonts(ctx);
    install_style(ctx, high_contrast);
}

fn install_fonts(ctx: &egui::Context) {
    if ctx.data(|data| data.get_temp::<bool>(egui::Id::unique("talos-fonts"))) == Some(true) {
        return;
    }
    let mut fonts = FontDefinitions::default();
    for (name, file) in [
        (FAMILY_UI, "Inter-Regular.ttf"),
        (FAMILY_MEDIUM, "Inter-Medium.ttf"),
        (FAMILY_SEMIBOLD, "Inter-SemiBold.ttf"),
        (FAMILY_BOLD, "Inter-Bold.ttf"),
        (FAMILY_MONO, "JetBrainsMono-Regular.ttf"),
        (FAMILY_MONO_MEDIUM, "JetBrainsMono-Medium.ttf"),
    ] {
        fonts.font_data.insert(
            name.to_owned(),
            Arc::new(FontData::from_static(match file {
                "Inter-Regular.ttf" => include_bytes!("../../../assets/fonts/Inter-Regular.ttf"),
                "Inter-Medium.ttf" => include_bytes!("../../../assets/fonts/Inter-Medium.ttf"),
                "Inter-SemiBold.ttf" => include_bytes!("../../../assets/fonts/Inter-SemiBold.ttf"),
                "Inter-Bold.ttf" => include_bytes!("../../../assets/fonts/Inter-Bold.ttf"),
                "JetBrainsMono-Regular.ttf" => {
                    include_bytes!("../../../assets/fonts/JetBrainsMono-Regular.ttf")
                }
                _ => include_bytes!("../../../assets/fonts/JetBrainsMono-Medium.ttf"),
            })),
        );
        fonts
            .families
            .entry(FontFamily::Name(name.into()))
            .or_default()
            .insert(0, name.to_owned());
    }
    // Proportional falls back to the UI face; keep egui's emoji fallbacks.
    let mut proportional = vec![FAMILY_UI.to_owned()];
    proportional.extend(
        fonts
            .families
            .get(&FontFamily::Proportional)
            .cloned()
            .unwrap_or_default(),
    );
    fonts
        .families
        .insert(FontFamily::Proportional, proportional);
    let mut mono = vec![FAMILY_MONO.to_owned()];
    mono.extend(
        fonts
            .families
            .get(&FontFamily::Monospace)
            .cloned()
            .unwrap_or_default(),
    );
    fonts.families.insert(FontFamily::Monospace, mono);
    ctx.set_fonts(fonts);
    ctx.data_mut(|data| data.insert_temp(egui::Id::unique("talos-fonts"), true));
}

fn family(name: &str) -> FontFamily {
    FontFamily::Name(name.into())
}

/// Regular UI font at a size.
#[must_use]
pub fn ui(size: f32) -> FontId {
    FontId::new(size, family(FAMILY_UI))
}
/// Medium weight, for labels and controls.
#[must_use]
pub fn medium(size: f32) -> FontId {
    FontId::new(size, family(FAMILY_MEDIUM))
}
/// Semibold, for titles and emphasis.
#[must_use]
pub fn semibold(size: f32) -> FontId {
    FontId::new(size, family(FAMILY_SEMIBOLD))
}
/// Bold, for strong display headings.
#[must_use]
pub fn bold(size: f32) -> FontId {
    FontId::new(size, family(FAMILY_BOLD))
}
/// Monospace for paths, commands and code.
#[must_use]
pub fn mono(size: f32) -> FontId {
    FontId::new(size, family(FAMILY_MONO))
}
/// Medium monospace.
#[must_use]
pub fn mono_medium(size: f32) -> FontId {
    FontId::new(size, family(FAMILY_MONO_MEDIUM))
}

fn install_style(ctx: &egui::Context, high_contrast: bool) {
    ctx.set_theme(egui::Theme::Dark);
    let mut style = (*ctx.style_of(egui::Theme::Dark)).clone();

    style.text_styles = [
        (TextStyle::Small, ui(11.5)),
        (TextStyle::Body, ui(14.5)),
        (TextStyle::Button, medium(13.5)),
        (TextStyle::Heading, semibold(20.0)),
        (TextStyle::Monospace, mono(12.5)),
    ]
    .into();

    style.spacing.item_spacing = egui::vec2(GAP_S, GAP_S);
    style.spacing.button_padding = egui::vec2(10.0, 6.0);
    style.spacing.interact_size = egui::vec2(30.0, 28.0);
    style.spacing.icon_width = 16.0;
    style.spacing.icon_width_inner = 12.0;
    style.spacing.menu_margin = egui::Margin::same(6);
    style.spacing.window_margin = egui::Margin::same(10);
    style.spacing.scroll.bar_width = 8.0;
    style.spacing.scroll.floating = false;

    let visuals = &mut style.visuals;
    let radius = CornerRadius::same(10);
    visuals.dark_mode = true;
    visuals.override_text_color = Some(if high_contrast { Color32::WHITE } else { TEXT });
    visuals.panel_fill = CANVAS;
    visuals.window_fill = SURFACE_HIGH;
    visuals.window_stroke = Stroke::new(1.0, LINE_STRONG);
    visuals.window_corner_radius = CornerRadius::same(14);
    visuals.menu_corner_radius = CornerRadius::same(12);
    visuals.extreme_bg_color = CODE;
    visuals.faint_bg_color = Color32::from_rgb(23, 27, 26);
    visuals.code_bg_color = CODE;
    visuals.hyperlink_color = BLUE;
    visuals.warn_fg_color = AMBER;
    visuals.error_fg_color = RED;
    visuals.window_shadow = egui::epaint::Shadow {
        offset: [0, 12],
        blur: 32,
        spread: 0,
        color: Color32::from_black_alpha(120),
    };
    visuals.popup_shadow = egui::epaint::Shadow {
        offset: [0, 8],
        blur: 24,
        spread: 0,
        color: Color32::from_black_alpha(110),
    };
    visuals.selection.bg_fill = if high_contrast {
        BLUE_PRESSED
    } else {
        BLUE_DIM
    };
    visuals.selection.stroke = Stroke::new(1.0, if high_contrast { Color32::WHITE } else { TEXT });

    let widgets = &mut visuals.widgets;
    widgets.noninteractive.bg_fill = CANVAS;
    widgets.noninteractive.bg_stroke = Stroke::new(1.0, LINE);
    widgets.noninteractive.fg_stroke = Stroke::new(1.0, SECONDARY);
    widgets.noninteractive.corner_radius = radius;

    widgets.inactive.bg_fill = Color32::TRANSPARENT;
    widgets.inactive.weak_bg_fill = SURFACE;
    widgets.inactive.bg_stroke = Stroke::new(1.0, LINE);
    widgets.inactive.fg_stroke = Stroke::new(1.0, SECONDARY);
    widgets.inactive.corner_radius = radius;

    widgets.hovered.bg_fill = HOVER;
    widgets.hovered.weak_bg_fill = HOVER;
    widgets.hovered.bg_stroke = Stroke::new(1.0, LINE_STRONG);
    widgets.hovered.fg_stroke = Stroke::new(1.0, TEXT);
    widgets.hovered.corner_radius = radius;

    widgets.active.bg_fill = SELECTED;
    widgets.active.weak_bg_fill = SURFACE_HIGH;
    widgets.active.bg_stroke = Stroke::new(1.0, BLUE_DIM);
    widgets.active.fg_stroke = Stroke::new(1.0, TEXT);
    widgets.active.corner_radius = radius;

    widgets.open.bg_fill = SURFACE_HIGH;
    widgets.open.weak_bg_fill = SURFACE_HIGH;
    widgets.open.bg_stroke = Stroke::new(1.0, LINE_STRONG);
    widgets.open.fg_stroke = Stroke::new(1.0, TEXT);
    widgets.open.corner_radius = radius;

    ctx.set_style_of(egui::Theme::Dark, style);
}

/// Blend two colors in linear-ish sRGB space, `t` clamped to `0..=1`.
#[must_use]
pub fn mix(from: Color32, to: Color32, t: f32) -> Color32 {
    let t = t.clamp(0.0, 1.0);
    let lerp = |a: u8, b: u8| -> u8 {
        f32::from(a)
            .mul_add(1.0 - t, f32::from(b) * t)
            .round()
            .clamp(0.0, 255.0) as u8
    };
    Color32::from_rgba_unmultiplied(
        lerp(from.r(), to.r()),
        lerp(from.g(), to.g()),
        lerp(from.b(), to.b()),
        lerp(from.a(), to.a()),
    )
}

/// A translucent fill for the canvas background, useful for overlays.
#[must_use]
pub fn scrim() -> Color32 {
    Color32::from_black_alpha(96)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn mix_endpoints_are_stable() {
        assert_eq!(mix(BLUE, ORANGE, 0.0), BLUE);
        assert_eq!(mix(BLUE, ORANGE, 1.0), ORANGE);
    }

    #[test]
    fn mix_is_clamped() {
        assert_eq!(mix(BLUE, ORANGE, -3.0), BLUE);
        assert_eq!(mix(BLUE, ORANGE, 4.0), ORANGE);
    }
}
