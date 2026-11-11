//! Local image validation runs on the backend worker, never in the paint loop.
use serde_json::{Value, json};
use std::{fs, io::Read, path::Path};
pub const MAX_IMAGES: usize = 8;
pub const MAX_IMAGE_BYTES: u64 = 16 * 1024 * 1024;
pub const MAX_TOTAL_BYTES: u64 = 32 * 1024 * 1024;

pub fn validate(paths: &[String]) -> Result<Value, String> {
    if paths.len() > MAX_IMAGES {
        return Err("Attach at most 8 images.".into());
    }
    let mut total = 0;
    let mut images = Vec::new();
    for path in paths {
        let path = Path::new(path);
        if !path.is_absolute() {
            return Err("Image paths must be absolute.".into());
        }
        let meta = fs::symlink_metadata(path).map_err(|e| format!("{}: {e}", path.display()))?;
        if !meta.is_file() || meta.file_type().is_symlink() {
            return Err(format!(
                "{}: choose a regular file, not a symlink.",
                path.display()
            ));
        }
        let size = meta.len();
        if size == 0 || size > MAX_IMAGE_BYTES {
            return Err(format!(
                "{}: image must be between 1 byte and 16 MiB.",
                path.display()
            ));
        }
        total += size;
        if total > MAX_TOTAL_BYTES {
            return Err("Images exceed the 32 MiB total limit.".into());
        }
        let mut header = [0; 12];
        let read = fs::File::open(path)
            .and_then(|mut f| f.read(&mut header))
            .map_err(|e| e.to_string())?;
        let bytes = &header[..read];
        let mime = if bytes.starts_with(b"\x89PNG\r\n\x1a\n") {
            "image/png"
        } else if bytes.starts_with(b"\xff\xd8\xff") {
            "image/jpeg"
        } else if bytes.starts_with(b"GIF87a") || bytes.starts_with(b"GIF89a") {
            "image/gif"
        } else if bytes.starts_with(b"RIFF") && bytes.get(8..12) == Some(b"WEBP") {
            "image/webp"
        } else {
            return Err(format!(
                "{}: supported formats are PNG, JPEG, GIF and WebP.",
                path.display()
            ));
        };
        images.push(json!({"path":path,"name":path.file_name().unwrap_or_default().to_string_lossy(),"size":size,"mime":mime}));
    }
    Ok(Value::Array(images))
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn rejects_invalid_and_oversized_files_without_loading_them() {
        let root = std::env::temp_dir().join(format!("talos-ui-image-{}", std::process::id()));
        fs::create_dir_all(&root).unwrap();
        let path = root.join("test.png");
        fs::write(&path, b"not an image").unwrap();
        assert!(
            validate(&[path.display().to_string()])
                .unwrap_err()
                .contains("supported formats")
        );
        fs::write(&path, b"\x89PNG\r\n\x1a\nfixture").unwrap();
        let paths = vec![path.display().to_string()];
        assert_eq!(validate(&paths).unwrap()[0]["mime"], "image/png");
        fs::OpenOptions::new()
            .write(true)
            .open(&path)
            .unwrap()
            .set_len(MAX_IMAGE_BYTES + 1)
            .unwrap();
        assert!(validate(&paths).unwrap_err().contains("16 MiB"));
        fs::remove_dir_all(root).unwrap();
    }
}
