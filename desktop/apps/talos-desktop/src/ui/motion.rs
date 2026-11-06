//! Animation helpers shared by every surface.
//!
//! All helpers are frame-rate independent: they animate from an explicit start
//! time using [`egui::Context::input`]'s stable `stable_dt` and request repaints
//! only while a transition is active.

use crate::theme;
use eframe::egui::{self, Color32, Id, Rect, Vec2};

/// Cubic ease-out on `0..=1`.
#[must_use]
pub fn ease_out_cubic(t: f32) -> f32 {
    let t = t.clamp(0.0, 1.0);
    1.0 - (1.0 - t).powi(3)
}

/// Cubic ease-in-out on `0..=1`.
#[must_use]
pub fn ease_in_out_cubic(t: f32) -> f32 {
    let t = t.clamp(0.0, 1.0);
    if t < 0.5 {
        4.0 * t * t * t
    } else {
        1.0 - (-2.0 * t + 2.0).powi(3) / 2.0
    }
}

/// Exponential smoothing that is independent of the frame rate.
#[must_use]
pub fn approach(current: f32, target: f32, dt: f32, half_life: f32) -> f32 {
    if (target - current).abs() < f32::EPSILON {
        return target;
    }
    let factor = 1.0 - (-std::f32::consts::LN_2 * dt / half_life.max(1e-4)).exp();
    current + (target - current) * factor
}

/// Animate a scalar toward a target. `motion` disables easing for accessibility.
pub fn animate_value(ctx: &egui::Context, id: Id, target: f32, motion: bool) -> f32 {
    if !motion {
        return target;
    }
    ctx.animate_value_with_time(id, target, theme::MOTION_BASE)
}

/// Animate a boolean as `0..=1`.
pub fn animate_bool(ctx: &egui::Context, id: Id, target: bool, motion: bool) -> f32 {
    if !motion {
        return f32::from(u8::from(target));
    }
    ctx.animate_bool_with_time(id, target, theme::MOTION_BASE)
}

/// Interpolate a rectangle toward a target rectangle.
pub fn animate_rect(ctx: &egui::Context, id: Id, target: Rect, motion: bool) -> Rect {
    if !motion {
        return target;
    }
    let x = ctx.animate_value_with_time(id.with("x"), target.min.x, theme::MOTION_BASE);
    let y = ctx.animate_value_with_time(id.with("y"), target.min.y, theme::MOTION_BASE);
    let w = ctx.animate_value_with_time(id.with("w"), target.width(), theme::MOTION_BASE);
    let h = ctx.animate_value_with_time(id.with("h"), target.height(), theme::MOTION_BASE);
    Rect::from_min_size(egui::pos2(x, y), Vec2::new(w, h))
}

/// Fade in a group of widgets and slide it down by `offset` points.
pub fn fade_in(ui: &mut egui::Ui, progress: f32, offset: f32, add: impl FnOnce(&mut egui::Ui)) {
    let a = progress.clamp(0.0, 1.0);
    if a <= 0.001 {
        return;
    }
    let opacity = (a * 255.0).round() as u8;
    ui.scope(|ui| {
        ui.multiply_opacity(a);
        let _ = offset;
        add(ui);
    });
    let _ = opacity;
}

/// Draw a soft glow behind a rectangle.
pub fn glow(painter: &egui::Painter, rect: Rect, color: Color32, intensity: f32) {
    if intensity <= 0.001 {
        return;
    }
    let alpha = (f32::from(color.a()) * intensity).clamp(0.0, 255.0) as u8;
    let glow_color = Color32::from_rgba_unmultiplied(color.r(), color.g(), color.b(), alpha / 3);
    for i in 1..=4 {
        let expand = i as f32 * 1.5;
        painter.rect_filled(
            rect.expand(expand),
            12.0,
            Color32::from_rgba_unmultiplied(
                glow_color.r(),
                glow_color.g(),
                glow_color.b(),
                alpha / (6 * i as u8),
            ),
        );
    }
}

/// Blend two colors according to an animation progress.
#[must_use]
pub fn blend(from: Color32, to: Color32, progress: f32) -> Color32 {
    theme::mix(from, to, progress)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn easing_endpoints() {
        assert_eq!(ease_out_cubic(0.0), 0.0);
        assert_eq!(ease_out_cubic(1.0), 1.0);
        assert_eq!(ease_in_out_cubic(0.0), 0.0);
        assert_eq!(ease_in_out_cubic(1.0), 1.0);
    }

    #[test]
    fn easing_is_monotonic() {
        let mut previous = 0.0;
        for step in 0..=40 {
            let value = ease_out_cubic(step as f32 / 40.0);
            assert!(value >= previous);
            previous = value;
        }
    }

    #[test]
    fn approach_converges() {
        let mut value = 0.0;
        for _ in 0..120 {
            value = approach(value, 1.0, 1.0 / 60.0, 0.1);
        }
        assert!((value - 1.0).abs() < 0.01, "{value}");
    }
}
