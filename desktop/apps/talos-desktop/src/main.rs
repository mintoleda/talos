//! Talos Desktop — a native egui cockpit for coding agents.
//!
//! Talos (`talos acp`) is the default backend; Claude Code is the only secondary
//! provider. The UI never blocks on daemon I/O: the backend thread owns the
//! connection and this thread only drains bounded batches each frame.

mod app;
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
