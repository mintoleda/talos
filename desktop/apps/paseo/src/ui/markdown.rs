//! Markdown rendering for assistant messages.

use eframe::egui;

/// Cached markdown renderer state.
#[derive(Default)]
pub struct MarkdownCache {
    /// Rendering cache, reused across frames for stable layout and speed.
    pub inner: egui_commonmark::CommonMarkCache,
}

impl MarkdownCache {
    /// Render `text` as GitHub-flavored markdown inside the current layout.
    pub fn show(&mut self, ui: &mut egui::Ui, text: &str) {
        egui_commonmark::CommonMarkViewer::new().show(ui, &mut self.inner, text);
    }
}

/// Render inline markdown with the shared cache.
pub fn show(ui: &mut egui::Ui, cache: &mut MarkdownCache, text: &str) {
    cache.show(ui, text);
}
