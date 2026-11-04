//! Supervised stdio subprocess transport and bounded diagnostics.

use std::{
    collections::{BTreeMap, VecDeque},
    path::PathBuf,
    sync::{Arc, Mutex, RwLock},
    time::Duration,
};

use thiserror::Error;
use tokio::{
    io::{AsyncBufRead, AsyncBufReadExt, AsyncReadExt, AsyncWriteExt, BufReader},
    process::{Child, Command},
    sync::{mpsc, watch},
    task::JoinHandle,
};
use tokio_util::sync::CancellationToken;

pub const MAX_FRAME_SIZE: usize = 64 * 1024 * 1024;
const DEFAULT_STDERR_TAIL: usize = 64 * 1024;
const FRAME_CHANNEL_CAPACITY: usize = 8;
const WRITER_CHANNEL_CAPACITY: usize = 16;

#[derive(Debug, Clone)]
pub struct ProcessConfig {
    pub command: PathBuf,
    pub args: Vec<String>,
    pub env: BTreeMap<String, String>,
    pub cwd: Option<PathBuf>,
    /// A hard limit, clamped to 64 MiB. The default accepts the full ACP limit.
    pub max_frame_size: usize,
    /// Maximum retained stderr tail, in bytes.
    pub stderr_tail_bytes: usize,
}

impl ProcessConfig {
    pub fn new(command: impl Into<PathBuf>) -> Self {
        Self {
            command: command.into(),
            args: Vec::new(),
            env: BTreeMap::new(),
            cwd: None,
            max_frame_size: MAX_FRAME_SIZE,
            stderr_tail_bytes: DEFAULT_STDERR_TAIL,
        }
    }

    /// Defaults for the primary Talos ACP provider (`talos acp`).
    pub fn talos(executable: impl Into<PathBuf>) -> Self {
        let mut config = Self::new(executable);
        config.args.push("acp".to_owned());
        config
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ProcessStatus {
    Starting,
    Running,
    Draining,
    Exited { code: Option<i32> },
    Failed { message: String, code: Option<i32> },
}

impl ProcessStatus {
    pub fn is_terminal(&self) -> bool {
        matches!(self, Self::Exited { .. } | Self::Failed { .. })
    }
}

#[derive(Debug, Clone, Error)]
pub enum AcpError {
    #[error("failed to start ACP process {command}: {message}")]
    Spawn { command: String, message: String },
    #[error("ACP process I/O failed: {0}")]
    Io(String),
    #[error("ACP protocol error: {0}")]
    Protocol(String),
    #[error("ACP frame exceeded the {limit}-byte limit")]
    FrameTooLarge { limit: usize },
    #[error("ACP connection closed")]
    ConnectionClosed,
    #[error("ACP process exited with code {code:?}; stderr: {stderr}")]
    ProcessExited { code: Option<i32>, stderr: String },
    #[error("ACP process failed: {0}")]
    ProcessFailed(String),
    #[error("remote JSON-RPC error {code}: {message}")]
    RemoteRpc {
        code: i64,
        message: String,
        data: Option<serde_json::Value>,
    },
    #[error("ACP agent negotiated protocol version {actual}, expected {expected}")]
    UnsupportedProtocolVersion { actual: u32, expected: u32 },
    #[error("ACP capability is not available: {0}")]
    UnsupportedCapability(&'static str),
    #[error("duplicate outstanding JSON-RPC id {0}")]
    DuplicateRpcId(String),
    #[error("request was cancelled")]
    Cancelled,
    #[error("ACP event consumer fell behind; bounded event queue is full")]
    EventQueueFull,
}

#[derive(Debug)]
pub(crate) enum WriterCommand {
    Frame(Vec<u8>),
    Close,
}

pub(crate) struct ProcessIo {
    pub writer: mpsc::Sender<WriterCommand>,
    pub frames: mpsc::Receiver<Result<Vec<u8>, AcpError>>,
    pub status: watch::Receiver<ProcessStatus>,
    pub kill: watch::Sender<bool>,
    pub cancellation: CancellationToken,
    pub stderr: Arc<StderrTail>,
    pub max_frame_size: usize,
    pub(crate) _tasks: Vec<JoinHandle<()>>,
}

pub(crate) async fn spawn_process(config: ProcessConfig) -> Result<ProcessIo, AcpError> {
    let mut command = Command::new(&config.command);
    command
        .args(&config.args)
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::piped())
        .kill_on_drop(true);
    if let Some(cwd) = &config.cwd {
        command.current_dir(cwd);
    }
    command.envs(&config.env);
    let mut child = command.spawn().map_err(|error| AcpError::Spawn {
        command: config.command.display().to_string(),
        message: error.to_string(),
    })?;

    let stdin = child
        .stdin
        .take()
        .ok_or_else(|| AcpError::Io("child stdin was not piped".into()))?;
    let stdout = child
        .stdout
        .take()
        .ok_or_else(|| AcpError::Io("child stdout was not piped".into()))?;
    let child_stderr = child
        .stderr
        .take()
        .ok_or_else(|| AcpError::Io("child stderr was not piped".into()))?;
    let limit = config.max_frame_size.clamp(1, MAX_FRAME_SIZE);
    let stderr = Arc::new(StderrTail::new(config.stderr_tail_bytes.max(1)));
    for secret in config.env.values() {
        stderr.add_secret(secret.clone());
    }
    let runtime = Arc::new(Runtime::new(stderr.clone()));
    let (writer_tx, writer_rx) = mpsc::channel(WRITER_CHANNEL_CAPACITY);
    let (frame_tx, frame_rx) = mpsc::channel(FRAME_CHANNEL_CAPACITY);

    let writer_runtime = runtime.clone();
    let writer_task = tokio::spawn(async move {
        writer_loop(stdin, writer_rx, writer_runtime).await;
    });
    let reader_runtime = runtime.clone();
    let reader_task = tokio::spawn(async move {
        stdout_loop(stdout, frame_tx, limit, reader_runtime).await;
    });
    let stderr_runtime = runtime.clone();
    let stderr_task = tokio::spawn(async move {
        drain_stderr(child_stderr, stderr_runtime).await;
    });
    let supervisor_runtime = runtime.clone();
    let supervisor_task = tokio::spawn(async move {
        supervise(child, supervisor_runtime).await;
    });

    Ok(ProcessIo {
        writer: writer_tx,
        frames: frame_rx,
        status: runtime.status_rx(),
        kill: runtime.kill_tx.clone(),
        cancellation: runtime.cancellation.clone(),
        stderr,
        max_frame_size: limit,
        _tasks: vec![writer_task, reader_task, stderr_task, supervisor_task],
    })
}

struct Runtime {
    status_tx: watch::Sender<ProcessStatus>,
    kill_tx: watch::Sender<bool>,
    cancellation: CancellationToken,
    failure: Mutex<Option<String>>,
    stderr: Arc<StderrTail>,
}

impl Runtime {
    fn new(stderr: Arc<StderrTail>) -> Self {
        let (status_tx, _) = watch::channel(ProcessStatus::Starting);
        let (kill_tx, _) = watch::channel(false);
        Self {
            status_tx,
            kill_tx,
            cancellation: CancellationToken::new(),
            failure: Mutex::new(None),
            stderr,
        }
    }

    fn status_rx(&self) -> watch::Receiver<ProcessStatus> {
        self.status_tx.subscribe()
    }

    fn fail(&self, message: impl Into<String>) {
        let mut failure = self.failure.lock().expect("failure mutex poisoned");
        if failure.is_none() {
            *failure = Some(message.into());
        }
        drop(failure);
        self.cancellation.cancel();
        self.kill_tx.send_replace(true);
    }
}

async fn writer_loop(
    mut stdin: tokio::process::ChildStdin,
    mut receiver: mpsc::Receiver<WriterCommand>,
    runtime: Arc<Runtime>,
) {
    loop {
        tokio::select! {
            _ = runtime.cancellation.cancelled() => break,
            command = receiver.recv() => match command {
                Some(WriterCommand::Frame(frame)) => {
                    if let Err(error) = stdin.write_all(&frame).await {
                        runtime.fail(format!("writing ACP stdin: {error}"));
                        break;
                    }
                    if let Err(error) = stdin.flush().await {
                        runtime.fail(format!("flushing ACP stdin: {error}"));
                        break;
                    }
                }
                Some(WriterCommand::Close) | None => break,
            }
        }
    }
}

async fn stdout_loop(
    stdout: tokio::process::ChildStdout,
    sender: mpsc::Sender<Result<Vec<u8>, AcpError>>,
    max_frame: usize,
    runtime: Arc<Runtime>,
) {
    let mut reader = BufReader::new(stdout);
    loop {
        match read_frame(&mut reader, max_frame).await {
            Ok(Some(frame)) if frame.is_empty() => continue,
            Ok(Some(frame)) => {
                if sender.send(Ok(frame)).await.is_err() {
                    return;
                }
            }
            Ok(None) => {
                let _ = sender.send(Err(AcpError::ConnectionClosed)).await;
                runtime.cancellation.cancel();
                return;
            }
            Err(error) => {
                runtime.fail(error.to_string());
                let _ = sender.send(Err(error)).await;
                return;
            }
        }
    }
}

async fn read_frame<R>(reader: &mut R, max_frame: usize) -> Result<Option<Vec<u8>>, AcpError>
where
    R: AsyncBufRead + Unpin,
{
    let mut frame = Vec::with_capacity(max_frame.min(16 * 1024));
    loop {
        let available = reader
            .fill_buf()
            .await
            .map_err(|error| AcpError::Io(error.to_string()))?;
        if available.is_empty() {
            return if frame.is_empty() {
                Ok(None)
            } else {
                Err(AcpError::Protocol(
                    "EOF inside newline-delimited frame".into(),
                ))
            };
        }
        let newline = available.iter().position(|byte| *byte == b'\n');
        let count = newline.unwrap_or(available.len());
        if frame.len().saturating_add(count) > max_frame {
            return Err(AcpError::FrameTooLarge { limit: max_frame });
        }
        frame.extend_from_slice(&available[..count]);
        let consumed = count + usize::from(newline.is_some());
        reader.consume(consumed);
        if newline.is_some() {
            if frame.last() == Some(&b'\r') {
                frame.pop();
            }
            return Ok(Some(frame));
        }
    }
}

async fn drain_stderr(mut stderr: tokio::process::ChildStderr, runtime: Arc<Runtime>) {
    let mut chunk = [0_u8; 8192];
    loop {
        match stderr.read(&mut chunk).await {
            Ok(0) => return,
            Ok(count) => runtime.stderr.push(&chunk[..count]),
            Err(error) => {
                runtime
                    .stderr
                    .push(format!("[stderr read error: {error}]").as_bytes());
                return;
            }
        }
    }
}

async fn supervise(mut child: Child, runtime: Arc<Runtime>) {
    let _ = runtime.status_tx.send(ProcessStatus::Running);
    let mut kill = runtime.kill_tx.subscribe();
    let result = tokio::select! {
        result = child.wait() => result,
        changed = kill.changed() => {
            if changed.is_ok() && *kill.borrow() {
                let _ = child.start_kill();
                child.wait().await
            } else {
                child.wait().await
            }
        }
    };
    runtime.cancellation.cancel();
    let code = result.ok().and_then(|status| status.code());
    let failure = runtime
        .failure
        .lock()
        .expect("failure mutex poisoned")
        .clone();
    if let Some(message) = failure {
        let _ = runtime
            .status_tx
            .send(ProcessStatus::Failed { message, code });
    } else {
        let _ = runtime.status_tx.send(ProcessStatus::Exited { code });
    }
}

#[derive(Debug)]
pub struct StderrTail {
    capacity: usize,
    bytes: Mutex<VecDeque<u8>>,
    secrets: RwLock<Vec<String>>,
}

impl StderrTail {
    fn new(capacity: usize) -> Self {
        Self {
            capacity,
            bytes: Mutex::new(VecDeque::with_capacity(capacity)),
            secrets: RwLock::new(Vec::new()),
        }
    }

    pub(crate) fn add_secret(&self, secret: String) {
        if secret.is_empty() {
            return;
        }
        let mut secrets = self.secrets.write().expect("secrets lock poisoned");
        if !secrets.contains(&secret) {
            secrets.push(secret);
            secrets.sort_by_key(|value| std::cmp::Reverse(value.len()));
        }
    }

    fn push(&self, bytes: &[u8]) {
        let mut tail = self.bytes.lock().expect("stderr lock poisoned");
        for byte in bytes {
            tail.push_back(*byte);
            if tail.len() > self.capacity {
                tail.pop_front();
            }
        }
    }

    /// Return a bounded UTF-8 diagnostic tail with configured values redacted.
    pub fn tail(&self) -> String {
        let bytes = self.bytes.lock().expect("stderr lock poisoned");
        let mut text =
            String::from_utf8_lossy(&bytes.iter().copied().collect::<Vec<_>>()).into_owned();
        drop(bytes);
        let secrets = self.secrets.read().expect("secrets lock poisoned");
        for secret in secrets.iter() {
            text = text.replace(secret, "[REDACTED]");
        }
        text
    }

    pub fn capacity(&self) -> usize {
        self.capacity
    }
}

pub(crate) async fn wait_for_exit(
    status: &mut watch::Receiver<ProcessStatus>,
    grace: Duration,
    kill: &watch::Sender<bool>,
) -> ProcessStatus {
    let wait = async {
        loop {
            let current = status.borrow().clone();
            if current.is_terminal() {
                return current;
            }
            if status.changed().await.is_err() {
                return ProcessStatus::Exited { code: None };
            }
        }
    };
    match tokio::time::timeout(grace, wait).await {
        Ok(final_status) => final_status,
        Err(_) => {
            kill.send_replace(true);
            loop {
                let current = status.borrow().clone();
                if current.is_terminal() {
                    return current;
                }
                if status.changed().await.is_err() {
                    return ProcessStatus::Exited { code: None };
                }
            }
        }
    }
}
