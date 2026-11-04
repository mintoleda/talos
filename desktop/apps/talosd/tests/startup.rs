#![cfg(unix)]

use std::{
    fs,
    os::unix::fs::{PermissionsExt, symlink},
    path::Path,
    process::{Command, Output, Stdio},
    thread,
    time::{Duration, Instant},
};

fn run_to_failure(home: &Path, user_home: &Path) -> Output {
    let mut child = Command::new(env!("CARGO_BIN_EXE_talosd"))
        .env_clear()
        .env("HOME", user_home)
        .env("TALOSD_HOME", home)
        .env("RUST_LOG", "error")
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    let deadline = Instant::now() + Duration::from_secs(5);
    loop {
        if child.try_wait().unwrap().is_some() {
            return child.wait_with_output().unwrap();
        }
        if Instant::now() >= deadline {
            let _ = child.kill();
            let output = child.wait_with_output().unwrap();
            panic!("startup did not fail within its deadline: {output:?}");
        }
        thread::sleep(Duration::from_millis(10));
    }
}

#[test]
fn legacy_home_and_symlink_aliases_are_rejected_before_any_writes() {
    let temp = tempfile::tempdir().unwrap();
    let user_home = temp.path().join("user");
    let legacy = user_home.join(".paseo");
    fs::create_dir_all(&legacy).unwrap();
    fs::set_permissions(&legacy, fs::Permissions::from_mode(0o750)).unwrap();
    let sentinel = legacy.join("untouched");
    fs::write(&sentinel, "legacy sentinel").unwrap();
    let alias = temp.path().join("alias");
    symlink(&legacy, &alias).unwrap();
    for selected in [legacy.clone(), legacy.join("child"), alias.join("child")] {
        let output = run_to_failure(&selected, &user_home);
        assert!(!output.status.success());
        assert!(
            String::from_utf8_lossy(&output.stdout).contains("legacy"),
            "{output:?}"
        );
        assert!(!legacy.join("child").exists());
        assert!(!legacy.join("talosd.lock").exists());
        assert!(!legacy.join("auth.token").exists());
        assert_eq!(fs::read_to_string(&sentinel).unwrap(), "legacy sentinel");
        assert_eq!(
            fs::metadata(&legacy).unwrap().permissions().mode() & 0o777,
            0o750
        );
    }
}

#[test]
fn invalid_config_cleans_up_the_socket_and_lock_for_the_next_attempt() {
    let temp = tempfile::tempdir().unwrap();
    let home = temp.path().join("rust-home");
    let user_home = temp.path().join("user");
    fs::create_dir_all(&home).unwrap();
    fs::create_dir_all(&user_home).unwrap();
    fs::write(home.join("config.toml"), "invalid = [").unwrap();
    for _ in 0..2 {
        let output = run_to_failure(&home, &user_home);
        assert!(!output.status.success());
        assert!(
            String::from_utf8_lossy(&output.stdout).contains("configuration"),
            "{output:?}"
        );
        assert!(!home.join("talosd.sock").exists());
        assert!(!home.join("talosd.lock").exists());
    }
}
