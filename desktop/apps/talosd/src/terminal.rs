use std::{
    collections::{HashMap, VecDeque},
    io::{Read, Write},
    path::{Path, PathBuf},
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, Ordering},
    },
    thread,
};

use anyhow::{Context, anyhow, bail};
use portable_pty::{Child, CommandBuilder, MasterPty, PtySize, native_pty_system};
use uuid::Uuid;

const CAPTURE_CAPACITY: usize = 1024 * 1024;
const INPUT_LIMIT: usize = 64 * 1024;
const DIMENSION_MAX: u16 = 500;
const MAX_TERMINALS: usize = 64;

#[derive(Clone)]
pub struct TerminalManager {
    entries: Arc<Mutex<HashMap<Uuid, Arc<Terminal>>>>,
}

struct Terminal {
    workspace_id: String,
    cwd: PathBuf,
    writer: Mutex<Box<dyn Write + Send>>,
    master: Mutex<Box<dyn MasterPty + Send>>,
    child: Mutex<Box<dyn Child + Send + Sync>>,
    output: Arc<Mutex<VecDeque<u8>>>,
    output_truncated: Arc<AtomicBool>,
    running: Arc<AtomicBool>,
}

#[derive(serde::Serialize)]
pub struct TerminalInfo {
    pub id: Uuid,
    pub workspace_id: String,
    pub cwd: PathBuf,
    pub running: bool,
}

#[derive(serde::Serialize)]
pub struct TerminalCapture {
    pub terminal_id: Uuid,
    pub data: String,
    pub truncated: bool,
    pub running: bool,
}

impl Default for TerminalManager {
    fn default() -> Self {
        Self::new()
    }
}

impl TerminalManager {
    #[must_use]
    pub fn new() -> Self {
        Self {
            entries: Arc::new(Mutex::new(HashMap::new())),
        }
    }

    #[allow(clippy::too_many_arguments)] // Keep all process-boundary parameters explicit at the spawn site.
    pub fn create(
        &self,
        workspace_id: String,
        workspace_root: &Path,
        requested_cwd: Option<&Path>,
        program: &str,
        args: &[String],
        rows: u16,
        cols: u16,
    ) -> anyhow::Result<TerminalInfo> {
        validate_dimensions(rows, cols)?;
        if program.trim().is_empty() {
            bail!("terminal program cannot be empty");
        }
        let root = workspace_root
            .canonicalize()
            .context("workspace path must exist and be canonicalizable")?;
        if !root.is_dir() {
            bail!("workspace path is not a directory");
        }
        let cwd = match requested_cwd {
            Some(path) if path.is_absolute() => path.to_path_buf(),
            Some(path) => root.join(path),
            None => root.clone(),
        }
        .canonicalize()
        .context("terminal working directory must exist")?;
        if !cwd.starts_with(&root) || !cwd.is_dir() {
            bail!("terminal working directory must be a directory inside its workspace");
        }

        // Keep concurrent create requests below a fixed process/resource budget.
        let mut entries = self
            .entries
            .lock()
            .map_err(|_| anyhow!("terminal table lock poisoned"))?;
        if entries.len() >= MAX_TERMINALS {
            bail!("terminal limit of {MAX_TERMINALS} reached");
        }

        let system = native_pty_system();
        let pair = system
            .openpty(PtySize {
                rows,
                cols,
                pixel_width: 0,
                pixel_height: 0,
            })
            .context("cannot create PTY")?;
        let mut command = CommandBuilder::new(program);
        command.args(args);
        command.cwd(&cwd);
        let mut reader = pair.master.try_clone_reader().context("cannot read PTY")?;
        let writer = pair.master.take_writer().context("cannot write PTY")?;
        let id = Uuid::new_v4();
        let output = Arc::new(Mutex::new(VecDeque::with_capacity(CAPTURE_CAPACITY)));
        let output_truncated = Arc::new(AtomicBool::new(false));
        let running = Arc::new(AtomicBool::new(true));
        let reader_output = Arc::clone(&output);
        let reader_truncated = Arc::clone(&output_truncated);
        let reader_running = Arc::clone(&running);
        thread::Builder::new()
            .name(format!("talos-pty-{id}"))
            .spawn(move || {
                let mut buffer = [0_u8; 8192];
                loop {
                    match reader.read(&mut buffer) {
                        Ok(0) | Err(_) => break,
                        Ok(count) => {
                            if let Ok(mut output) = reader_output.lock() {
                                for byte in &buffer[..count] {
                                    if output.len() == CAPTURE_CAPACITY {
                                        output.pop_front();
                                        reader_truncated.store(true, Ordering::Release);
                                    }
                                    output.push_back(*byte);
                                }
                            } else {
                                break;
                            }
                        }
                    }
                }
                reader_running.store(false, Ordering::Release);
            })
            .context("cannot start PTY capture worker")?;

        let child = pair
            .slave
            .spawn_command(command)
            .with_context(|| format!("cannot start terminal program {program:?}"))?;
        drop(pair.slave);

        let terminal = Arc::new(Terminal {
            workspace_id: workspace_id.clone(),
            cwd: cwd.clone(),
            writer: Mutex::new(writer),
            master: Mutex::new(pair.master),
            child: Mutex::new(child),
            output,
            output_truncated,
            running: Arc::clone(&running),
        });
        entries.insert(id, terminal);
        Ok(TerminalInfo {
            id,
            workspace_id,
            cwd,
            running: true,
        })
    }

    pub fn list(&self, workspace_id: &str) -> anyhow::Result<Vec<TerminalInfo>> {
        let entries = self
            .entries
            .lock()
            .map_err(|_| anyhow!("terminal table lock poisoned"))?;
        Ok(entries
            .iter()
            .filter(|(_, terminal)| terminal.workspace_id == workspace_id)
            .map(|(id, terminal)| TerminalInfo {
                id: *id,
                workspace_id: terminal.workspace_id.clone(),
                cwd: terminal.cwd.clone(),
                running: terminal.running.load(Ordering::Acquire),
            })
            .collect())
    }

    pub fn input(&self, workspace_id: &str, id: Uuid, data: &str) -> anyhow::Result<()> {
        if data.len() > INPUT_LIMIT {
            bail!("terminal input exceeds {INPUT_LIMIT} bytes");
        }
        let terminal = self.get_owned(workspace_id, id)?;
        if !terminal.running.load(Ordering::Acquire) {
            bail!("terminal has exited");
        }
        terminal
            .writer
            .lock()
            .map_err(|_| anyhow!("terminal writer lock poisoned"))?
            .write_all(data.as_bytes())
            .context("cannot write terminal input")
    }

    pub fn resize(&self, workspace_id: &str, id: Uuid, rows: u16, cols: u16) -> anyhow::Result<()> {
        validate_dimensions(rows, cols)?;
        let terminal = self.get_owned(workspace_id, id)?;
        terminal
            .master
            .lock()
            .map_err(|_| anyhow!("terminal PTY lock poisoned"))?
            .resize(PtySize {
                rows,
                cols,
                pixel_width: 0,
                pixel_height: 0,
            })
            .context("cannot resize PTY")
    }

    pub fn capture(
        &self,
        workspace_id: &str,
        id: Uuid,
        limit: usize,
    ) -> anyhow::Result<TerminalCapture> {
        if limit == 0 || limit > CAPTURE_CAPACITY {
            bail!("capture limit must be between 1 and {CAPTURE_CAPACITY} bytes");
        }
        let terminal = self.get_owned(workspace_id, id)?;
        let output = terminal
            .output
            .lock()
            .map_err(|_| anyhow!("terminal capture lock poisoned"))?;
        let take = output.len().min(limit);
        let truncated = output.len() > take || terminal.output_truncated.load(Ordering::Acquire);
        let data: Vec<u8> = output.iter().skip(output.len() - take).copied().collect();
        Ok(TerminalCapture {
            terminal_id: id,
            data: String::from_utf8_lossy(&data).into_owned(),
            truncated,
            running: terminal.running.load(Ordering::Acquire),
        })
    }

    pub fn kill(&self, workspace_id: &str, id: Uuid) -> anyhow::Result<()> {
        let terminal = {
            let mut entries = self
                .entries
                .lock()
                .map_err(|_| anyhow!("terminal table lock poisoned"))?;
            let terminal = entries
                .get(&id)
                .cloned()
                .ok_or_else(|| anyhow!("terminal not found"))?;
            if terminal.workspace_id != workspace_id {
                bail!("terminal does not belong to workspace");
            }
            entries.remove(&id).expect("entry was just verified")
        };
        terminate(&terminal)
    }

    pub fn kill_workspace(&self, workspace_id: &str) {
        let ids = self
            .entries
            .lock()
            .map(|entries| {
                entries
                    .iter()
                    .filter(|(_, terminal)| terminal.workspace_id == workspace_id)
                    .map(|(id, _)| *id)
                    .collect::<Vec<_>>()
            })
            .unwrap_or_default();
        for id in ids {
            let _ = self.kill(workspace_id, id);
        }
    }

    pub fn kill_all(&self) {
        let terminals = self
            .entries
            .lock()
            .map(|mut entries| {
                entries
                    .drain()
                    .map(|(_, terminal)| terminal)
                    .collect::<Vec<_>>()
            })
            .unwrap_or_default();
        for terminal in terminals {
            let _ = terminate(&terminal);
        }
    }

    fn get_owned(&self, workspace_id: &str, id: Uuid) -> anyhow::Result<Arc<Terminal>> {
        let terminal = self
            .entries
            .lock()
            .map_err(|_| anyhow!("terminal table lock poisoned"))?
            .get(&id)
            .cloned()
            .ok_or_else(|| anyhow!("terminal not found"))?;
        if terminal.workspace_id != workspace_id {
            bail!("terminal does not belong to workspace");
        }
        Ok(terminal)
    }
}

fn terminate(terminal: &Terminal) -> anyhow::Result<()> {
    terminal.running.store(false, Ordering::Release);
    let mut child = terminal
        .child
        .lock()
        .map_err(|_| anyhow!("terminal child lock poisoned"))?;
    if child
        .try_wait()
        .context("cannot inspect terminal process")?
        .is_none()
    {
        #[cfg(unix)]
        {
            let group = terminal
                .master
                .lock()
                .map_err(|_| anyhow!("terminal PTY lock poisoned"))?
                .process_group_leader();
            if let Some(group) = group {
                use nix::{
                    sys::signal::{Signal, killpg},
                    unistd::Pid,
                };
                if let Err(error) = killpg(Pid::from_raw(group), Signal::SIGKILL)
                    && error != nix::errno::Errno::ESRCH
                {
                    return Err(anyhow!("cannot kill terminal process group: {error}"));
                }
            } else {
                child.kill().context("cannot kill terminal process")?;
            }
        }
        #[cfg(not(unix))]
        child.kill().context("cannot kill terminal process")?;
    }
    child.wait().context("cannot wait for terminal process")?;
    Ok(())
}

fn validate_dimensions(rows: u16, cols: u16) -> anyhow::Result<()> {
    if !(1..=DIMENSION_MAX).contains(&rows) || !(1..=DIMENSION_MAX).contains(&cols) {
        bail!("terminal rows and columns must be between 1 and {DIMENSION_MAX}");
    }
    Ok(())
}

#[cfg(all(test, unix))]
mod tests {
    use super::*;
    use std::{
        fs,
        time::{Duration, Instant},
    };

    fn create(
        manager: &TerminalManager,
        workspace_id: &str,
        root: &Path,
        program: &str,
        args: &[&str],
    ) -> TerminalInfo {
        manager
            .create(
                workspace_id.into(),
                root,
                None,
                program,
                &args.iter().map(|arg| (*arg).into()).collect::<Vec<_>>(),
                24,
                80,
            )
            .unwrap()
    }

    #[test]
    fn owned_pty_captures_input_resizes_and_cleans_up() {
        let temp = tempfile::tempdir().unwrap();
        let workspace = temp.path().join("workspace");
        fs::create_dir(&workspace).unwrap();
        let manager = TerminalManager::new();
        let terminal = create(
            &manager,
            "workspace-a",
            &workspace,
            "sh",
            &["-c", "printf READY; read line; printf 'GOT:%s' \"$line\""],
        );

        let deadline = Instant::now() + Duration::from_secs(2);
        loop {
            if manager
                .capture("workspace-a", terminal.id, 1024)
                .unwrap()
                .data
                .contains("READY")
            {
                break;
            }
            assert!(Instant::now() < deadline, "PTY child did not start");
            thread::sleep(Duration::from_millis(10));
        }
        assert!(manager.input("workspace-a", terminal.id, "hello\n").is_ok());
        manager.resize("workspace-a", terminal.id, 40, 100).unwrap();
        let deadline = Instant::now() + Duration::from_secs(2);
        loop {
            let captured = manager.capture("workspace-a", terminal.id, 1024).unwrap();
            if captured.data.contains("GOT:hello") {
                break;
            }
            assert!(Instant::now() < deadline, "PTY input was not observed");
            thread::sleep(Duration::from_millis(10));
        }
        assert!(manager.input("workspace-b", terminal.id, "bad\n").is_err());
        manager.kill("workspace-a", terminal.id).unwrap();
        assert!(manager.list("workspace-a").unwrap().is_empty());
        assert!(manager.capture("workspace-a", terminal.id, 100).is_err());
    }

    #[test]
    fn working_directory_cannot_escape_workspace_and_shutdown_kills_children() {
        let temp = tempfile::tempdir().unwrap();
        let workspace = temp.path().join("workspace");
        let outside = temp.path().join("outside");
        fs::create_dir(&workspace).unwrap();
        fs::create_dir(&outside).unwrap();
        let manager = TerminalManager::new();
        assert!(
            manager
                .create(
                    "w1".into(),
                    &workspace,
                    Some(&outside),
                    "sh",
                    &["-c".into(), "sleep 30".into()],
                    24,
                    80
                )
                .is_err()
        );
        let terminal = create(&manager, "w1", &workspace, "sh", &["-c", "sleep 30"]);
        assert_eq!(manager.list("w2").unwrap().len(), 0);
        manager.kill_all();
        assert!(manager.list("w1").unwrap().is_empty());
        assert!(manager.input("w1", terminal.id, "\n").is_err());
    }

    #[test]
    fn killing_terminal_removes_descendants_in_its_pty_session() {
        use std::process::Command as ProcessCommand;

        let temp = tempfile::tempdir().unwrap();
        let workspace = temp.path().join("workspace");
        fs::create_dir(&workspace).unwrap();
        let manager = TerminalManager::new();
        let terminal = create(
            &manager,
            "w1",
            &workspace,
            "sh",
            &["-c", "sleep 30 & echo $!; wait"],
        );
        let deadline = Instant::now() + Duration::from_secs(2);
        let child_pid = loop {
            let capture = manager.capture("w1", terminal.id, 1024).unwrap();
            if let Some(pid) = capture
                .data
                .lines()
                .find_map(|line| line.trim().parse::<u32>().ok())
            {
                break pid;
            }
            assert!(Instant::now() < deadline, "descendant PID was not captured");
            thread::sleep(Duration::from_millis(10));
        };
        manager.kill("w1", terminal.id).unwrap();
        let deadline = Instant::now() + Duration::from_secs(2);
        loop {
            let alive = ProcessCommand::new("kill")
                .args(["-0", &child_pid.to_string()])
                .stderr(std::process::Stdio::null())
                .status()
                .is_ok_and(|status| status.success());
            if !alive {
                break;
            }
            assert!(
                Instant::now() < deadline,
                "PTY descendant {child_pid} survived terminal kill"
            );
            thread::sleep(Duration::from_millis(20));
        }
    }

    #[test]
    fn captures_are_bounded_and_report_truncation() {
        let temp = tempfile::tempdir().unwrap();
        let workspace = temp.path().join("workspace");
        fs::create_dir(&workspace).unwrap();
        let manager = TerminalManager::new();
        let terminal = create(
            &manager,
            "w1",
            &workspace,
            "sh",
            &["-c", "printf 1234567890; sleep 1"],
        );
        let deadline = Instant::now() + Duration::from_secs(2);
        let captured = loop {
            let value = manager.capture("w1", terminal.id, 4).unwrap();
            if !value.data.is_empty() {
                break value;
            }
            assert!(Instant::now() < deadline, "PTY output was not captured");
            thread::sleep(Duration::from_millis(10));
        };
        assert!(captured.data.len() <= 4);
        assert!(captured.truncated);
        manager.kill("w1", terminal.id).unwrap();
    }
}
