//! Native folder selection stays off the egui paint thread.
use std::path::{Path, PathBuf};
use std::sync::mpsc::{Receiver, sync_channel};

pub type Selection = Result<Option<PathBuf>, String>;

pub fn open(ctx: eframe::egui::Context) -> Receiver<Selection> {
    let (sender, receiver) = sync_channel(1);
    std::thread::spawn(move || {
        let selection = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .map_err(|error| error.to_string())
            .map(|runtime| {
                runtime.block_on(async {
                    rfd::AsyncFileDialog::new()
                        .set_title("Choose workspace folder")
                        .pick_folder()
                        .await
                        .map(|folder| folder.path().to_path_buf())
                })
            });
        let _ = sender.send(selection);
        ctx.request_repaint();
    });
    receiver
}

pub fn folder_name(path: &Path) -> String {
    path.file_name().map_or_else(
        || path.display().to_string(),
        |name| name.to_string_lossy().into_owned(),
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn uses_folder_name_without_a_naming_form() {
        assert_eq!(folder_name(Path::new("/home/user/repos/talos")), "talos");
        assert_eq!(
            folder_name(Path::new("/home/user/My project/")),
            "My project"
        );
        assert_ne!(folder_name(Path::new("/")), "");
    }
}
