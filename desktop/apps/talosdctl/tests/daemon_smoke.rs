//! Ignored wrapper for the deterministic daemon + CLI process smoke harness.
//!
//! The real scenarios live in `scripts/talosd-smoke.py` so they can also be run
//! directly without cargo. This test only launches that script and enforces a
//! bounded timeout with process cleanup on failure. It is ignored by default
//! because the workspace does not build sibling binaries for integration tests.
//!
//! Prerequisites:
//!
//! ```text
//! cargo build -p talosd -p talosdctl -p talos-acp --bins
//! cargo test -p talosdctl --test daemon_smoke -- --ignored --nocapture
//! ```
//!
//! See `scripts/README.md` for binary path overrides and scenario coverage.
//!
//! Timeout handling is deliberately graceful: the script receives its own
//! `--timeout` alarm (so its `finally` cleanup runs) and the wrapper sends
//! `SIGINT` before a hard kill. Daemons and fixtures are started in separate
//! process groups by the script, which records fixture PIDs for cleanup.

use std::{
    io::Read,
    path::PathBuf,
    process::{Command, Stdio},
    thread,
    time::{Duration, Instant},
};

const DEFAULT_TIMEOUT_SECS: u64 = 300;

#[test]
#[ignore = "requires prebuilt talosd, talosdctl and acp-fixture; see scripts/README.md"]
fn daemon_cli_smoke_script_passes() {
    let root = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..");
    let script = root.join("scripts/talosd-smoke.py");
    assert!(
        script.is_file(),
        "smoke script is missing at {}",
        script.display()
    );
    let python = std::env::var_os("TALOSD_SMOKE_PYTHON").unwrap_or_else(|| "python3".into());
    let timeout_secs = std::env::var("TALOSD_SMOKE_TIMEOUT_SECS")
        .ok()
        .and_then(|value| value.parse::<u64>().ok())
        .unwrap_or(DEFAULT_TIMEOUT_SECS);
    let script_timeout = timeout_secs.saturating_sub(10).max(1);

    let mut child = Command::new(python)
        .arg(&script)
        .arg("--verbose")
        .arg("--timeout")
        .arg(script_timeout.to_string())
        .current_dir(&root)
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("spawn the Python smoke harness");
    let mut child_stdout = child.stdout.take().expect("child stdout is piped");
    let mut child_stderr = child.stderr.take().expect("child stderr is piped");
    let stdout_reader = thread::spawn(move || {
        let mut text = String::new();
        let _ = child_stdout.read_to_string(&mut text);
        text
    });
    let stderr_reader = thread::spawn(move || {
        let mut text = String::new();
        let _ = child_stderr.read_to_string(&mut text);
        text
    });

    let deadline = Instant::now() + Duration::from_secs(timeout_secs);
    let status = loop {
        if let Some(status) = child.try_wait().expect("poll the smoke harness") {
            break status;
        }
        if Instant::now() >= deadline {
            request_graceful_stop(&mut child);
            let stdout = stdout_reader.join().unwrap_or_default();
            let stderr = stderr_reader.join().unwrap_or_default();
            panic!("smoke harness exceeded {timeout_secs}s\nstdout:\n{stdout}\nstderr:\n{stderr}");
        }
        thread::sleep(Duration::from_millis(100));
    };
    let stdout = stdout_reader.join().unwrap_or_default();
    let stderr = stderr_reader.join().unwrap_or_default();
    assert!(
        status.success(),
        "process-level smoke harness failed ({status})\nstdout:\n{stdout}\nstderr:\n{stderr}"
    );
    println!("{stdout}");
}

/// Ask the harness to unwind and clean up, then hard-kill only after a grace period.
fn request_graceful_stop(child: &mut std::process::Child) {
    let _ = Command::new("kill")
        .arg("-INT")
        .arg(child.id().to_string())
        .status();
    let deadline = Instant::now() + Duration::from_secs(20);
    while Instant::now() < deadline {
        if child.try_wait().is_ok_and(|status| status.is_some()) {
            return;
        }
        thread::sleep(Duration::from_millis(100));
    }
    let _ = child.kill();
    let _ = child.wait();
}
