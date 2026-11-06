//! Crash recovery without weakening the importer's create-new exclusion marker.

use std::{
    fs::{self, File, OpenOptions},
    io::{ErrorKind, Write},
    os::unix::fs::{FileTypeExt, MetadataExt, OpenOptionsExt, PermissionsExt},
    path::{Path, PathBuf},
    time::Duration,
};

use anyhow::{Context, bail};
use nix::{errno::Errno, sys::signal::kill, unistd::Pid};
use tokio::net::{UnixListener, UnixStream};
use uuid::Uuid;

pub struct InstanceGuard {
    // Never unlink this inode: competing daemon starts must lock the same file.
    _guard: File,
    path: PathBuf,
    token: String,
    lock_device: u64,
    lock_inode: u64,
}

impl InstanceGuard {
    pub fn acquire(home: &Path) -> anyhow::Result<Self> {
        let guard = OpenOptions::new()
            .read(true)
            .write(true)
            .create(true)
            .truncate(false)
            .mode(0o600)
            .custom_flags(nix::libc::O_NOFOLLOW | nix::libc::O_NONBLOCK)
            .open(home.join("talosd.guard"))?;
        let metadata = guard.metadata()?;
        if !metadata.is_file() || metadata.nlink() != 1 {
            bail!("daemon guard must be a regular file with one link");
        }
        guard
            .try_lock()
            .context("daemon already running or guard unavailable")?;
        guard.set_permissions(fs::Permissions::from_mode(0o600))?;

        let path = home.join("talosd.lock");
        match fs::symlink_metadata(&path) {
            Ok(metadata) => {
                if !metadata.is_file() || metadata.nlink() != 1 {
                    bail!("daemon lock must be a regular file with one link");
                }
                let token = fs::read_to_string(&path)?;
                // Older daemons wrote only a PID. Importer/unknown/empty markers
                // remain fail-closed; never guess whether an import is finished.
                let pid = token
                    .strip_prefix("talosd pid=")
                    .map_or(token.as_str(), |rest| {
                        rest.split_whitespace().next().unwrap_or("")
                    });
                let pid = pid.trim().parse::<i32>().ok().filter(|pid| *pid > 0);
                if !pid.is_some_and(|pid| kill(Pid::from_raw(pid), None) == Err(Errno::ESRCH)) {
                    bail!("daemon already running or lock owner cannot be verified dead");
                }
                fs::remove_file(&path)?;
            }
            Err(error) if error.kind() == ErrorKind::NotFound => {}
            Err(error) => return Err(error.into()),
        }

        let token = format!("talosd pid={} id={}", std::process::id(), Uuid::new_v4());
        // Publish a complete marker atomically, retaining create-new exclusion
        // with paseo-migrate. The guard serializes concurrent daemon recovery.
        let temporary = home.join(format!(".talosd.lock.tmp-{}", Uuid::new_v4()));
        let mut file = OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&temporary)?;
        let lock_metadata = file.metadata()?;
        let staged = SocketGuard::own(temporary.clone(), &lock_metadata);
        file.write_all(token.as_bytes())?;
        file.sync_all()?;
        fs::hard_link(&temporary, &path).context("daemon or importer acquired the lock")?;
        drop(staged);
        Ok(Self {
            _guard: guard,
            path,
            token,
            lock_device: lock_metadata.dev(),
            lock_inode: lock_metadata.ino(),
        })
    }

    pub async fn bind_socket(&self) -> anyhow::Result<(UnixListener, SocketGuard)> {
        let path = self.path.with_file_name("talosd.sock");
        match fs::symlink_metadata(&path) {
            Ok(metadata) => {
                if !metadata.file_type().is_socket() {
                    bail!(
                        "daemon socket path is not a Unix socket: {}",
                        path.display()
                    );
                }
                let stale_socket = SocketGuard::observe(path.clone(), &metadata);
                // A live or unresponsive listener is never removed, even if its
                // sentinel was manually deleted. Only ECONNREFUSED proves stale.
                match tokio::time::timeout(Duration::from_millis(500), UnixStream::connect(&path))
                    .await
                {
                    Ok(Err(error)) if error.kind() == ErrorKind::ConnectionRefused => {
                        stale_socket.remove_if_owned();
                    }
                    Ok(Err(error)) if error.kind() == ErrorKind::NotFound => {}
                    _ => bail!("daemon socket is live or cannot be verified stale"),
                }
            }
            Err(error) if error.kind() == ErrorKind::NotFound => {}
            Err(error) => return Err(error.into()),
        }
        let listener = UnixListener::bind(&path)?;
        let cleanup = SocketGuard::own(path.clone(), &fs::symlink_metadata(&path)?);
        fs::set_permissions(&path, fs::Permissions::from_mode(0o600))?;
        Ok((listener, cleanup))
    }
}

impl Drop for InstanceGuard {
    fn drop(&mut self) {
        if fs::symlink_metadata(&self.path).is_ok_and(|metadata| {
            metadata.dev() == self.lock_device
                && metadata.ino() == self.lock_inode
                && fs::read_to_string(&self.path).is_ok_and(|contents| contents == self.token)
        }) {
            let _ = fs::remove_file(&self.path);
        }
    }
}

pub struct SocketGuard {
    path: PathBuf,
    device: u64,
    inode: u64,
    remove_on_drop: bool,
}

impl SocketGuard {
    fn own(path: PathBuf, metadata: &fs::Metadata) -> Self {
        Self {
            path,
            device: metadata.dev(),
            inode: metadata.ino(),
            remove_on_drop: true,
        }
    }

    fn observe(path: PathBuf, metadata: &fs::Metadata) -> Self {
        Self {
            path,
            device: metadata.dev(),
            inode: metadata.ino(),
            remove_on_drop: false,
        }
    }

    fn remove_if_owned(&self) {
        if fs::symlink_metadata(&self.path)
            .is_ok_and(|metadata| metadata.dev() == self.device && metadata.ino() == self.inode)
        {
            let _ = fs::remove_file(&self.path);
        }
    }
}

impl Drop for SocketGuard {
    fn drop(&mut self) {
        if self.remove_on_drop {
            self.remove_if_owned();
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::{fs::symlink, net::UnixListener as StdListener};

    #[test]
    fn concurrent_starts_and_importers_cannot_steal_the_lock() {
        let temp = tempfile::tempdir().unwrap();
        let path = temp.path().join("talosd.lock");
        let owner = InstanceGuard::acquire(temp.path()).unwrap();
        assert!(InstanceGuard::acquire(temp.path()).is_err());
        assert!(
            OpenOptions::new()
                .write(true)
                .create_new(true)
                .open(&path)
                .is_err()
        );
        drop(owner);
        assert!(!path.exists());
        // The guard inode is retained, but its OS lock is released.
        assert!(InstanceGuard::acquire(temp.path()).is_ok());
    }

    #[test]
    fn live_legacy_pid_and_unknown_importer_markers_are_preserved() {
        let temp = tempfile::tempdir().unwrap();
        let path = temp.path().join("talosd.lock");
        for token in [
            std::process::id().to_string(),
            "paseo-migrate pid=1 id=fixture".into(),
            String::new(),
            "invalid".into(),
        ] {
            fs::write(&path, &token).unwrap();
            assert!(InstanceGuard::acquire(temp.path()).is_err());
            assert_eq!(fs::read_to_string(&path).unwrap(), token);
        }
    }

    #[tokio::test]
    async fn dead_pid_and_disconnected_socket_recover_without_manual_deletion() {
        let temp = tempfile::tempdir().unwrap();
        let mut child = std::process::Command::new("sh")
            .args(["-c", "exit 0"])
            .env_clear()
            .env("HOME", temp.path())
            .spawn()
            .unwrap();
        let pid = child.id();
        child.wait().unwrap();
        fs::write(temp.path().join("talosd.lock"), pid.to_string()).unwrap();
        let socket = temp.path().join("talosd.sock");
        drop(StdListener::bind(&socket).unwrap());
        let guard = InstanceGuard::acquire(temp.path()).unwrap();
        let (listener, cleanup) = guard.bind_socket().await.unwrap();
        assert!(UnixStream::connect(&socket).await.is_ok());
        drop(listener);
        drop(cleanup);
        drop(guard);
        assert!(!socket.exists());
        assert!(!temp.path().join("talosd.lock").exists());
    }

    #[tokio::test]
    async fn live_socket_and_non_socket_paths_are_preserved() {
        let temp = tempfile::tempdir().unwrap();
        let socket = temp.path().join("talosd.sock");
        let listener = StdListener::bind(&socket).unwrap();
        let guard = InstanceGuard::acquire(temp.path()).unwrap();
        let bind_error = guard.bind_socket().await.err();
        assert!(bind_error.is_some());
        assert!(
            socket.exists(),
            "live socket path disappeared: {bind_error:?}"
        );
        drop(listener);
        fs::remove_file(&socket).unwrap();
        fs::write(&socket, "user data").unwrap();
        assert!(guard.bind_socket().await.is_err());
        assert_eq!(fs::read_to_string(&socket).unwrap(), "user data");
        fs::remove_file(&socket).unwrap();
        symlink(temp.path().join("talosd.lock"), &socket).unwrap();
        assert!(guard.bind_socket().await.is_err());
        assert!(socket.is_symlink());
    }

    #[tokio::test]
    async fn socket_cleanup_does_not_remove_a_replacement_path() {
        let temp = tempfile::tempdir().unwrap();
        let guard = InstanceGuard::acquire(temp.path()).unwrap();
        let (listener, cleanup) = guard.bind_socket().await.unwrap();
        let socket = temp.path().join("talosd.sock");
        drop(listener);
        fs::remove_file(&socket).unwrap();
        fs::write(&socket, "replacement owned by another process").unwrap();
        drop(cleanup);
        assert_eq!(
            fs::read_to_string(socket).unwrap(),
            "replacement owned by another process"
        );
    }

    #[test]
    fn symlink_and_hardlink_locks_are_rejected_without_changing_the_target() {
        let temp = tempfile::tempdir().unwrap();
        let target = temp.path().join("keep");
        fs::write(&target, "retained").unwrap();
        for name in ["talosd.guard", "talosd.lock"] {
            let path = temp.path().join(name);
            if path.exists() {
                fs::remove_file(&path).unwrap();
            }
            symlink(&target, &path).unwrap();
            assert!(InstanceGuard::acquire(temp.path()).is_err());
            assert!(path.is_symlink());
            fs::remove_file(&path).unwrap();
            fs::hard_link(&target, &path).unwrap();
            assert!(InstanceGuard::acquire(temp.path()).is_err());
            fs::remove_file(&path).unwrap();
        }
        assert_eq!(fs::read_to_string(target).unwrap(), "retained");
    }
}
