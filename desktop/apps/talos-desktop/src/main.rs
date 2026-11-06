//! Talos Desktop — a native egui cockpit for coding agents.
//!
//! Talos (`talos acp`) is the default backend; Claude Code is the only secondary
//! provider. The UI never blocks on daemon I/O: the backend thread owns the
//! connection and this thread only drains bounded batches each frame.

// UI geometry intentionally converts bounded floating-point layout/animation
// values. Several immediate-mode view functions are long because they own one
// cohesive layout; unused theme/icon helpers are reserved for the component API.
#![allow(
    clippy::cast_possible_truncation,
    clippy::cast_precision_loss,
    clippy::cast_sign_loss,
    clippy::struct_excessive_bools,
    clippy::too_many_lines,
    dead_code
)]

mod app;
mod attachments;
mod backend;
mod state;
mod theme;
mod ui;

fn main() -> eframe::Result {
    let options = eframe::NativeOptions {
        viewport: eframe::egui::ViewportBuilder::default()
            .with_inner_size([1360.0, 860.0])
            .with_min_inner_size([720.0, 520.0])
            .with_title("Talos Desktop")
            .with_app_id("talos-desktop"),
        renderer: eframe::Renderer::Wgpu,
        ..Default::default()
    };
    eframe::run_native(
        "Talos Desktop",
        options,
        Box::new(|cc| {
            theme::install(&cc.egui_ctx, false);
            let preview = std::env::var_os("TALOSD_PREVIEW").is_some();
            Ok(Box::new(app::Desktop::new(cc.egui_ctx.clone(), preview)))
        }),
    )
}
