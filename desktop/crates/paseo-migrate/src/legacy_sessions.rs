//! Bounded, raw-byte legacy session importer. This module deliberately never
//! opens a Go transcript through the Go runtime's repairing/append-mode loader.
#![allow(clippy::too_many_lines)]

use super::{
    DATABASE_FILE_NAME, Issue, JOURNAL_FILE_NAME, Journal, MigrationError, PendingSessionImport,
    Report, SessionJournalEntry, absolute_path, acquire_import_lock, create_destination,
    load_journal, normalize_lexical, reject_symlink_components, stable_id, sync_directory,
    validate_database_file, validate_destination_file, write_journal,
};
use base64::{Engine as _, engine::general_purpose::STANDARD as BASE64};
use chrono::{DateTime, Utc};
use serde::Deserialize;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    collections::{BTreeMap, HashMap, HashSet},
    fs::{self, File, OpenOptions},
    io::{self, Read, Write},
    path::{Component, Path, PathBuf},
};
use talos_domain::{
    Agent, AgentId, AgentLifecycle, Attachment, AttachmentId, ModelRef, Project, ProjectId,
    ProviderId, Session, SessionId, SessionLifecycle, TimelineEvent, TimelineEventKind, ToolCall,
    ToolStatus, Workspace, WorkspaceId, WorkspaceKind, WorkspaceLifecycle,
};
use talos_store::{HistoricalSessionBatch, SqliteStore};
use uuid::Uuid;

const MAX_SOURCE_FILES: usize = 10_000;
const MAX_SOURCE_BYTES: u64 = 512 * 1024 * 1024;
const MAX_META_BYTES: u64 = 1024 * 1024;
const MAX_TRANSCRIPT_BYTES: u64 = 32 * 1024 * 1024;
const MAX_JSONL_LINE_BYTES: usize = 2 * 1024 * 1024;
const MAX_SESSION_EVENTS: usize = 100_000;
const MAX_IMAGES_PER_SESSION: usize = 32;
const MAX_IMAGE_BYTES: usize = 10 * 1024 * 1024;
const MAX_SESSION_IMAGE_BYTES: usize = 32 * 1024 * 1024;
const MAX_SESSION_PLAN_SOURCE_BYTES: u64 = 16 * 1024 * 1024;
const MAX_SESSION_EVENT_BYTES: usize = 32 * 1024 * 1024;
const MAX_PREPARED_SESSIONS: usize = 128;
const MAX_PREPARED_SOURCE_BYTES: u64 = 32 * 1024 * 1024;
const MAX_PREPARED_EVENTS: usize = 200_000;
const MAX_PREPARED_EVENT_BYTES: usize = 64 * 1024 * 1024;
const MAX_PREPARED_BLOB_BYTES: usize = 32 * 1024 * 1024;
const MAX_REPORTED_ISSUES: usize = 4_096;
const ATTACHMENTS_DIR_NAME: &str = "attachments";

mod folded_archives;

#[derive(Clone)]
struct SourceFile {
    length: u64,
    sha256: String,
}

struct SourceSnapshot {
    root: PathBuf,
    files: BTreeMap<PathBuf, SourceFile>,
    fingerprint: String,
}

#[derive(Clone, Debug, Deserialize)]
struct LegacyMeta {
    id: String,
    dir: String,
    #[serde(default)]
    project_dir: String,
    #[serde(default)]
    isolation: String,
    #[serde(default)]
    branch: String,
    #[serde(default)]
    default_branch: String,
    #[serde(default)]
    merged: bool,
    #[serde(default)]
    provider: String,
    #[serde(default)]
    model: String,
    #[serde(default)]
    created_at: Option<DateTime<Utc>>,
    #[serde(default)]
    last_active: Option<DateTime<Utc>>,
}

struct Candidate {
    source_key: String,
    fingerprint: String,
    session_id: SessionId,
    batch: HistoricalSessionBatch,
    blobs: Vec<AttachmentBlob>,
    hashes: Vec<String>,
}

struct AttachmentBlob {
    hash: String,
    bytes: Vec<u8>,
}

#[derive(Default)]
struct PreparedBudget {
    sessions: usize,
    source_bytes: u64,
    events: usize,
    event_bytes: usize,
    blob_bytes: usize,
}

struct SizeCounter(usize);

impl Write for SizeCounter {
    fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
        self.0 = self
            .0
            .checked_add(bytes.len())
            .ok_or_else(|| io::Error::other("serialized event size overflow"))?;
        Ok(bytes.len())
    }

    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

impl PreparedBudget {
    fn source_fits(&self, source_bytes: u64) -> bool {
        self.source_bytes
            .checked_add(source_bytes)
            .is_some_and(|total| total <= MAX_PREPARED_SOURCE_BYTES)
    }

    fn reserve(
        &mut self,
        source_bytes: u64,
        events: usize,
        event_bytes: usize,
        blob_bytes: usize,
    ) -> bool {
        let Some(sessions) = self.sessions.checked_add(1) else {
            return false;
        };
        let Some(source_bytes) = self.source_bytes.checked_add(source_bytes) else {
            return false;
        };
        let Some(events) = self.events.checked_add(events) else {
            return false;
        };
        let Some(event_bytes) = self.event_bytes.checked_add(event_bytes) else {
            return false;
        };
        let Some(blob_bytes) = self.blob_bytes.checked_add(blob_bytes) else {
            return false;
        };
        if sessions > MAX_PREPARED_SESSIONS
            || source_bytes > MAX_PREPARED_SOURCE_BYTES
            || events > MAX_PREPARED_EVENTS
            || event_bytes > MAX_PREPARED_EVENT_BYTES
            || blob_bytes > MAX_PREPARED_BLOB_BYTES
        {
            return false;
        }
        self.sessions = sessions;
        self.source_bytes = source_bytes;
        self.events = events;
        self.event_bytes = event_bytes;
        self.blob_bytes = blob_bytes;
        true
    }
}

#[derive(Clone)]
struct ToolUseRef {
    line: usize,
    block: usize,
    id: String,
    name: String,
    input: Value,
}

#[derive(Clone)]
struct ToolResultRef {
    line: usize,
    block: usize,
    id: String,
    content: String,
    is_error: bool,
}

/// Explicit opt-in session import. Every regular file under the selected tree
/// is snapshotted, including unrelated files, then reverified after apply.
pub(super) fn import_sessions(
    source: &Path,
    destination: &Path,
    apply: bool,
    report: &mut Report,
) -> Result<(), MigrationError> {
    report.report_version = 2;
    report.sessions_source = Some(source.to_path_buf());
    report.unsupported.retain(|item| {
        !item.starts_with("legacy agents and sessions")
            && !item.starts_with("non-inline attachment payloads")
    });
    report.unsupported.push(
        "legacy agent configuration, provider-native state, run/permission state, unsupported transcript semantics, and non-inline attachments are not imported; session history becomes archived read-only records".into(),
    );
    report.unsupported.push(
        "worktrees, plugins, model caches, schedules and desktop state are not imported; only validated inline images are copied".into(),
    );

    let source_absolute = absolute_path(source)?;
    reject_symlink_components(&source_absolute)?;
    let source_root = normalize_lexical(&source_absolute);
    let destination_root = normalize_lexical(&absolute_path(destination)?);
    reject_symlink_components(&source_root)?;
    reject_symlink_components(&destination_root)?;
    if source_root == destination_root
        || source_root.starts_with(&destination_root)
        || destination_root.starts_with(&source_root)
    {
        return Err(MigrationError::PathOverlap);
    }
    match fs::symlink_metadata(&source_root) {
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            push_issue(
                &mut report.issues,
                Issue {
                    path: "sessions-source".into(),
                    category: "sessions_source_missing".into(),
                    message:
                        "explicit sessions source directory does not exist; no sessions were read"
                            .into(),
                },
            );
            return Ok(());
        }
        Err(error) => return Err(error.into()),
        Ok(metadata) if metadata.file_type().is_symlink() || !metadata.is_dir() => {
            return Err(MigrationError::UnsafePath);
        }
        Ok(_) => {}
    }

    let destination_root = normalize_lexical(&absolute_path(destination)?);
    reject_symlink_components(&destination_root)?;
    let journal_path = destination_root.join(JOURNAL_FILE_NAME);
    let db_path = destination_root.join(DATABASE_FILE_NAME);
    let _import_lock = if apply {
        create_destination(&destination_root)?;
        validate_destination_file(&journal_path)?;
        validate_database_file(&db_path)?;
        let lock = acquire_import_lock(&destination_root)?;
        validate_database_file(&db_path)?;
        Some(lock)
    } else {
        None
    };
    let mut journal = load_journal(&journal_path)?;

    let snapshot = snapshot_source(&source_root)?;
    report.sessions_source_fingerprint = Some(snapshot.fingerprint.clone());
    let mut candidates = discover_candidates(&snapshot, &mut report.issues)?;
    report.session_candidates = candidates.len();
    if let Some(pending) = &journal.pending_session {
        let pending_key = pending.source_key.clone();
        candidates.sort_by_key(|(metadata, _)| {
            let key = format!(
                "{}::{}",
                snapshot.root.to_string_lossy(),
                metadata.to_string_lossy()
            );
            key != pending_key
        });
    }

    let mut plans = Vec::new();
    let mut budget = PreparedBudget::default();
    for pair in candidates {
        let source_bytes = snapshot
            .files
            .get(&pair.0)
            .and_then(|metadata| {
                snapshot.files.get(&pair.1).and_then(|transcript| {
                    metadata
                        .length
                        .checked_add(transcript.length)
                        .and_then(|bytes| {
                            let archive = pair.1.with_file_name(format!(
                                "{}.archive.jsonl",
                                pair.1.file_name()?.to_str()?
                            ));
                            snapshot
                                .files
                                .get(&archive)
                                .map_or(Some(bytes), |file| bytes.checked_add(file.length))
                        })
                })
            })
            .ok_or(MigrationError::SourceChanged)?;
        let path = pair.0.to_string_lossy().into_owned();
        let is_pending = journal.pending_session.as_ref().is_some_and(|pending| {
            let key = format!(
                "{}::{}",
                snapshot.root.to_string_lossy(),
                pair.0.to_string_lossy()
            );
            pending.source_key == key
        });
        if source_bytes > MAX_SESSION_PLAN_SOURCE_BYTES
            || (!is_pending
                && (!budget.source_fits(source_bytes) || budget.sessions >= MAX_PREPARED_SESSIONS))
        {
            push_issue(&mut report.issues, Issue {
                path,
                category: "prepared_plan_limit".into(),
                message: "session exceeds the bounded aggregate prepared-plan budget and was left unimported".into(),
            });
            continue;
        }
        match prepare_candidate(&snapshot, pair, &mut report.issues) {
            Ok(candidate) => {
                let mut event_size = SizeCounter(0);
                if serde_json::to_writer(&mut event_size, &candidate.batch.events).is_err() {
                    push_issue(&mut report.issues, Issue {
                        path,
                        category: "prepared_plan_limit".into(),
                        message: "session events could not be measured within the prepared-plan budget and were left unimported".into(),
                    });
                    continue;
                }
                let event_bytes = event_size.0;
                if event_bytes > MAX_SESSION_EVENT_BYTES {
                    push_issue(&mut report.issues, Issue {
                        path,
                        category: "prepared_plan_limit".into(),
                        message: "session events exceed the bounded per-session prepared-plan budget and were left unimported".into(),
                    });
                    continue;
                }
                let blob_bytes = candidate
                    .blobs
                    .iter()
                    .try_fold(0_usize, |total, blob| total.checked_add(blob.bytes.len()));
                if !blob_bytes.is_some_and(|bytes| {
                    budget.reserve(
                        source_bytes,
                        candidate.batch.events.len(),
                        event_bytes,
                        bytes,
                    )
                }) {
                    push_issue(&mut report.issues, Issue {
                        path,
                        category: "prepared_plan_limit".into(),
                        message: "session exceeds the bounded aggregate prepared-plan budget and was left unimported".into(),
                    });
                    continue;
                }
                report.session_ids.push(candidate.session_id.to_string());
                report
                    .attachment_hashes
                    .extend(candidate.hashes.iter().cloned());
                plans.push(candidate);
            }
            Err((path, category, message)) => push_issue(
                &mut report.issues,
                Issue {
                    path,
                    category,
                    message,
                },
            ),
        }
    }
    report.session_ids.sort();
    report.session_ids.dedup();
    report.attachment_hashes.sort();
    report.attachment_hashes.dedup();

    if apply {
        // A rejected/oversized source must not strand an existing pending import
        // by making its plan disappear. Recovery always requires the exact plan.
        validate_pending_session(&journal, &plans)?;
        if !plans.is_empty() {
            verify_snapshot(&snapshot)?;
            let mut store = SqliteStore::open(&db_path)?;
            let existing_projects = store.list_projects()?;
            let existing_workspaces = store.list_workspaces()?;
            resolve_path_collisions(&mut plans, &existing_projects, &existing_workspaces)?;
            validate_candidate_collisions(&plans)?;
            validate_pending_session(&journal, &plans)?;
            for candidate in ordered_for_recovery(plans, journal.pending_session.as_ref()) {
                apply_candidate(
                    &snapshot,
                    &destination_root,
                    &journal_path,
                    &mut journal,
                    &mut store,
                    candidate,
                    report,
                )?;
            }
        }
        verify_snapshot(&snapshot)?;
    }
    report.imported_session_ids.sort();
    report.imported_session_ids.dedup();
    Ok(())
}

fn validate_pending_session(journal: &Journal, plans: &[Candidate]) -> Result<(), MigrationError> {
    let Some(pending) = &journal.pending_session else {
        return Ok(());
    };
    if plans.iter().any(|candidate| {
        candidate.source_key == pending.source_key
            && candidate.fingerprint == pending.fingerprint
            && candidate.session_id.to_string() == pending.session_id
            && candidate.hashes == pending.attachment_hashes
    }) {
        Ok(())
    } else {
        Err(MigrationError::JournalMismatch)
    }
}

fn ordered_for_recovery(
    mut plans: Vec<Candidate>,
    pending: Option<&PendingSessionImport>,
) -> Vec<Candidate> {
    if let Some(pending) = pending {
        plans.sort_by_key(|candidate| {
            !((candidate.source_key == pending.source_key)
                && (candidate.fingerprint == pending.fingerprint)
                && (candidate.session_id.to_string() == pending.session_id))
        });
    }
    plans
}

fn validate_candidate_collisions(plans: &[Candidate]) -> Result<(), MigrationError> {
    for (index, candidate) in plans.iter().enumerate() {
        for other in &plans[..index] {
            let candidate_project = candidate
                .batch
                .project
                .as_ref()
                .ok_or(MigrationError::UnsafePath)?;
            let other_project = other
                .batch
                .project
                .as_ref()
                .ok_or(MigrationError::UnsafePath)?;
            if candidate_project.root_path == other_project.root_path
                && candidate_project.name != other_project.name
            {
                return Err(MigrationError::DestinationConflict);
            }
            if candidate.batch.workspace.path == other.batch.workspace.path
                && (candidate.batch.workspace.name != other.batch.workspace.name
                    || candidate_project.root_path != other_project.root_path)
            {
                return Err(MigrationError::DestinationConflict);
            }
        }
    }
    Ok(())
}

fn resolve_path_collisions(
    plans: &mut [Candidate],
    existing_projects: &[Project],
    existing_workspaces: &[Workspace],
) -> Result<(), MigrationError> {
    for candidate in plans {
        let expected_project = candidate
            .batch
            .project
            .as_ref()
            .ok_or(MigrationError::UnsafePath)?
            .clone();
        let project_by_path = existing_projects
            .iter()
            .find(|project| project.root_path == expected_project.root_path);
        if let Some(existing) = project_by_path {
            if existing.name != expected_project.name {
                return Err(MigrationError::DestinationConflict);
            }
            candidate.batch.project = Some(existing.clone());
        } else if existing_projects
            .iter()
            .any(|project| project.id == expected_project.id)
        {
            return Err(MigrationError::DestinationConflict);
        }

        let expected_workspace = candidate.batch.workspace.clone();
        let workspace_by_path = existing_workspaces
            .iter()
            .find(|workspace| workspace.path == expected_workspace.path);
        if let Some(existing) = workspace_by_path {
            if existing.name != expected_workspace.name
                || existing.kind != expected_workspace.kind
                || existing.lifecycle == WorkspaceLifecycle::Deleting
            {
                return Err(MigrationError::DestinationConflict);
            }
            if let Some(project_id) = existing.project_id {
                let associated = existing_projects
                    .iter()
                    .find(|project| project.id == project_id)
                    .ok_or(MigrationError::DestinationConflict)?;
                if associated.root_path != expected_project.root_path {
                    return Err(MigrationError::DestinationConflict);
                }
                candidate.batch.project = Some(associated.clone());
                candidate.batch.workspace = existing.clone();
            } else {
                let compatible_project = candidate
                    .batch
                    .project
                    .as_ref()
                    .ok_or(MigrationError::DestinationConflict)?;
                if compatible_project.root_path != expected_project.root_path {
                    return Err(MigrationError::DestinationConflict);
                }
                candidate.batch.workspace = existing.clone();
                candidate.batch.workspace.project_id = Some(compatible_project.id);
            }
        } else {
            if existing_workspaces
                .iter()
                .any(|workspace| workspace.id == expected_workspace.id)
            {
                return Err(MigrationError::DestinationConflict);
            }
            candidate.batch.workspace.project_id =
                candidate.batch.project.as_ref().map(|project| project.id);
        }
        candidate.batch.agent.workspace_id = candidate.batch.workspace.id;
    }
    Ok(())
}

fn snapshot_source(root: &Path) -> Result<SourceSnapshot, MigrationError> {
    let mut pending = vec![PathBuf::new()];
    let mut files = BTreeMap::new();
    let mut total_bytes = 0_u64;
    while let Some(relative_dir) = pending.pop() {
        let dir = root.join(&relative_dir);
        let metadata = fs::symlink_metadata(&dir)?;
        if metadata.file_type().is_symlink() || !metadata.is_dir() {
            return Err(MigrationError::UnsafePath);
        }
        for entry in fs::read_dir(&dir)? {
            let entry = entry?;
            let path = entry.path();
            let relative = path
                .strip_prefix(root)
                .map_err(|_| MigrationError::UnsafePath)?
                .to_path_buf();
            if relative
                .components()
                .any(|part| !matches!(part, Component::Normal(_)))
                || relative.to_str().is_none()
            {
                return Err(MigrationError::UnsafePath);
            }
            let metadata = fs::symlink_metadata(&path)?;
            if metadata.file_type().is_symlink() {
                return Err(MigrationError::UnsafePath);
            }
            if metadata.is_dir() {
                pending.push(relative);
                continue;
            }
            if !metadata.is_file() {
                return Err(MigrationError::UnsafePath);
            }
            if files.len() >= MAX_SOURCE_FILES {
                return Err(MigrationError::UnsafePath);
            }
            total_bytes = total_bytes
                .checked_add(metadata.len())
                .ok_or(MigrationError::UnsafePath)?;
            if total_bytes > MAX_SOURCE_BYTES {
                return Err(MigrationError::UnsafePath);
            }
            let sha256 = hash_regular_file(root, &relative, metadata.len())?;
            files.insert(
                relative.clone(),
                SourceFile {
                    length: metadata.len(),
                    sha256,
                },
            );
        }
    }
    let fingerprint = source_fingerprint(&files);
    Ok(SourceSnapshot {
        root: root.to_path_buf(),
        files,
        fingerprint,
    })
}

fn hash_regular_file(
    root: &Path,
    relative: &Path,
    expected_length: u64,
) -> Result<String, MigrationError> {
    reject_symlink_components(&root.join(relative))?;
    let path = root.join(relative);
    let before = fs::symlink_metadata(&path)?;
    if before.file_type().is_symlink() || !before.is_file() || before.len() != expected_length {
        return Err(MigrationError::UnsafePath);
    }
    let mut file = open_source_file(&path)?;
    let opened = file.metadata()?;
    if !opened.is_file() || opened.len() != expected_length {
        return Err(MigrationError::SourceChanged);
    }
    let mut hasher = Sha256::new();
    let mut total = 0_u64;
    let mut buffer = vec![0_u8; 64 * 1024].into_boxed_slice();
    loop {
        let count = file.read(&mut buffer)?;
        if count == 0 {
            break;
        }
        total = total
            .checked_add(u64::try_from(count).map_err(|_| MigrationError::UnsafePath)?)
            .ok_or(MigrationError::UnsafePath)?;
        if total > expected_length {
            return Err(MigrationError::SourceChanged);
        }
        hasher.update(&buffer[..count]);
    }
    let after = fs::symlink_metadata(&path)?;
    if after.file_type().is_symlink() || !after.is_file() || total != expected_length {
        return Err(MigrationError::SourceChanged);
    }
    Ok(format!("{:x}", hasher.finalize()))
}

#[cfg(unix)]
fn open_source_file(path: &Path) -> Result<File, MigrationError> {
    use std::os::unix::fs::OpenOptionsExt;
    Ok(OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_CLOEXEC | libc::O_NONBLOCK)
        .open(path)?)
}

#[cfg(not(unix))]
fn open_source_file(path: &Path) -> Result<File, MigrationError> {
    Ok(File::open(path)?)
}

fn source_fingerprint(files: &BTreeMap<PathBuf, SourceFile>) -> String {
    let mut hasher = Sha256::new();
    for (relative, file) in files {
        let name = relative.to_string_lossy();
        hasher.update(u64::try_from(name.len()).unwrap_or(u64::MAX).to_be_bytes());
        hasher.update(name.as_bytes());
        hasher.update(file.length.to_be_bytes());
        hasher.update(file.sha256.as_bytes());
    }
    format!("sha256:{:x}", hasher.finalize())
}

fn verify_snapshot(snapshot: &SourceSnapshot) -> Result<(), MigrationError> {
    let current = snapshot_source(&snapshot.root)?;
    if current.fingerprint == snapshot.fingerprint {
        Ok(())
    } else {
        Err(MigrationError::SourceChanged)
    }
}

fn read_snapshot_file(
    snapshot: &SourceSnapshot,
    relative: &Path,
    limit: u64,
) -> Result<Vec<u8>, MigrationError> {
    let expected = snapshot
        .files
        .get(relative)
        .ok_or(MigrationError::SourceChanged)?;
    if expected.length > limit {
        return Err(MigrationError::UnsafePath);
    }
    let file = open_source_file(&snapshot.root.join(relative))?;
    let metadata = file.metadata()?;
    if !metadata.is_file() || metadata.len() != expected.length {
        return Err(MigrationError::SourceChanged);
    }
    let capacity = usize::try_from(expected.length).map_err(|_| MigrationError::UnsafePath)?;
    let mut bytes = Vec::with_capacity(capacity);
    file.take(limit.saturating_add(1)).read_to_end(&mut bytes)?;
    if u64::try_from(bytes.len()).unwrap_or(u64::MAX) != expected.length
        || format!("{:x}", Sha256::digest(&bytes)) != expected.sha256
    {
        return Err(MigrationError::SourceChanged);
    }
    Ok(bytes)
}

#[allow(clippy::case_sensitive_file_extension_comparisons)] // Legacy filenames encode case-sensitive IDs.
fn discover_candidates(
    snapshot: &SourceSnapshot,
    issues: &mut Vec<Issue>,
) -> Result<Vec<(PathBuf, PathBuf)>, MigrationError> {
    let mut metadata_files = Vec::new();
    let mut transcript_files = Vec::new();
    for relative in snapshot.files.keys() {
        let Some(name) = relative.file_name().and_then(|name| name.to_str()) else {
            return Err(MigrationError::UnsafePath);
        };
        if is_folded_archive_sidecar(name) {
            let transcript = relative.with_file_name(
                name.strip_suffix(".archive.jsonl")
                    .ok_or(MigrationError::UnsafePath)?,
            );
            let metadata = transcript.with_extension("meta.json");
            if !snapshot.files.contains_key(&transcript) || !snapshot.files.contains_key(&metadata)
            {
                push_issue(issues, Issue {
                    path: relative.to_string_lossy().into_owned(),
                    category: "orphan_folded_archive".into(),
                    message: "folded archive has no matching metadata/transcript pair; its source bytes are preserved and no history was imported".into(),
                });
            }
            continue;
        }
        if is_archive_file(name) {
            continue;
        }
        if name.ends_with(".meta.json") {
            metadata_files.push(relative.clone());
        } else if name.ends_with(".jsonl") {
            transcript_files.push(relative.clone());
        }
    }

    let metadata_set = metadata_files.iter().cloned().collect::<HashSet<_>>();
    let transcript_set = transcript_files.iter().cloned().collect::<HashSet<_>>();
    let mut candidates = Vec::new();
    for metadata in metadata_files {
        let name = metadata
            .file_name()
            .and_then(|name| name.to_str())
            .ok_or(MigrationError::UnsafePath)?;
        let id = name
            .strip_suffix(".meta.json")
            .ok_or(MigrationError::UnsafePath)?;
        if is_archive_id(id) {
            continue;
        }
        let transcript_name = format!("{id}.jsonl");
        let transcript = metadata.with_file_name(transcript_name);
        if !transcript_set.contains(&transcript) {
            push_issue(
                issues,
                Issue {
                    path: metadata.to_string_lossy().into_owned(),
                    category: "missing_session_transcript".into(),
                    message: "metadata sidecar has no matching JSONL transcript".into(),
                },
            );
            continue;
        }
        candidates.push((metadata, transcript));
    }
    for transcript in transcript_files {
        let name = transcript
            .file_name()
            .and_then(|name| name.to_str())
            .ok_or(MigrationError::UnsafePath)?;
        let id = name
            .strip_suffix(".jsonl")
            .ok_or(MigrationError::UnsafePath)?;
        if is_archive_id(id) {
            continue;
        }
        let metadata = transcript.with_file_name(format!("{id}.meta.json"));
        if !metadata_set.contains(&metadata) {
            push_issue(issues, Issue {
                path: transcript.to_string_lossy().into_owned(),
                category: "missing_session_metadata".into(),
                message:
                    "transcript has no matching metadata sidecar; working path cannot be resolved"
                        .into(),
            });
        }
    }
    candidates.sort_by(|left, right| left.0.cmp(&right.0));
    Ok(candidates)
}

fn is_archive_file(name: &str) -> bool {
    name.ends_with("_archive.jsonl") || name.ends_with(".archive.jsonl")
}

fn is_folded_archive_sidecar(name: &str) -> bool {
    name.strip_suffix(".jsonl.archive.jsonl")
        .is_some_and(valid_legacy_id)
}

fn is_archive_id(id: &str) -> bool {
    id.ends_with("_archive") || id.ends_with(".archive")
}

fn prepare_candidate(
    snapshot: &SourceSnapshot,
    pair: (PathBuf, PathBuf),
    issues: &mut Vec<Issue>,
) -> Result<Candidate, (String, String, String)> {
    let (metadata_path, transcript_path) = pair;
    let source_key = format!(
        "{}::{}",
        snapshot.root.to_string_lossy(),
        metadata_path.to_string_lossy()
    );
    let metadata_file = snapshot.files.get(&metadata_path).ok_or_else(|| {
        (
            source_key.clone(),
            "source_changed".into(),
            "metadata sidecar disappeared from the source snapshot".into(),
        )
    })?;
    let transcript_file = snapshot.files.get(&transcript_path).ok_or_else(|| {
        (
            source_key.clone(),
            "source_changed".into(),
            "transcript disappeared from the source snapshot".into(),
        )
    })?;
    if metadata_file.length > MAX_META_BYTES {
        return Err((
            source_key,
            "metadata_too_large".into(),
            "metadata sidecar exceeds the bounded importer limit".into(),
        ));
    }
    if transcript_file.length > MAX_TRANSCRIPT_BYTES {
        return Err((
            source_key,
            "transcript_too_large".into(),
            "transcript exceeds the bounded importer limit".into(),
        ));
    }
    let metadata_bytes = read_snapshot_file(snapshot, &metadata_path, MAX_META_BYTES)
        .map_err(|error| source_error(&source_key, &error))?;
    let raw_meta: Value = serde_json::from_slice(&metadata_bytes).map_err(|_| {
        (
            source_key.clone(),
            "invalid_session_metadata".into(),
            "metadata sidecar is not valid JSON".into(),
        )
    })?;
    let meta: LegacyMeta = serde_json::from_value(raw_meta).map_err(|_| {
        (
            source_key.clone(),
            "invalid_session_metadata".into(),
            "metadata sidecar is missing required fields or has invalid field types".into(),
        )
    })?;
    let basename = metadata_path
        .file_name()
        .and_then(|name| name.to_str())
        .and_then(|name| name.strip_suffix(".meta.json"))
        .ok_or_else(|| {
            (
                source_key.clone(),
                "invalid_session_id".into(),
                "sidecar filename is not a valid session ID".into(),
            )
        })?;
    if meta.id != basename || !valid_legacy_id(&meta.id) {
        return Err((
            source_key,
            "session_id_mismatch".into(),
            "sidecar ID must match its transcript basename and use a safe identifier".into(),
        ));
    }
    validate_legacy_meta(&meta).map_err(|category| {
        (
            metadata_path.to_string_lossy().into_owned(),
            category,
            "session metadata lacks a usable directory, provider, or model".into(),
        )
    })?;
    let transcript = read_snapshot_file(snapshot, &transcript_path, MAX_TRANSCRIPT_BYTES)
        .map_err(|error| source_error(&source_key, &error))?;
    let archive_path = transcript_path.with_file_name(format!(
        "{}.archive.jsonl",
        transcript_path
            .file_name()
            .and_then(|name| name.to_str())
            .unwrap_or_default()
    ));
    let archive_file = snapshot.files.get(&archive_path);
    if archive_file.is_some_and(|file| file.length > MAX_TRANSCRIPT_BYTES) {
        return Err((
            archive_path.to_string_lossy().into_owned(),
            "folded_archive_too_large".into(),
            "folded archive exceeds the bounded importer limit; the entire session was left unimported".into(),
        ));
    }
    let archive = archive_file
        .map(|_| read_snapshot_file(snapshot, &archive_path, MAX_TRANSCRIPT_BYTES))
        .transpose()
        .map_err(|error| source_error(&source_key, &error))?;
    let mut pair_hasher = Sha256::new();
    pair_hasher.update(source_key.as_bytes());
    pair_hasher.update(metadata_file.sha256.as_bytes());
    pair_hasher.update(transcript_file.sha256.as_bytes());
    if let Some(archive_file) = archive_file {
        pair_hasher.update(b"fold-merge-v1:");
        pair_hasher.update(archive_file.sha256.as_bytes());
    }
    let fingerprint = format!("sha256:{:x}", pair_hasher.finalize());
    let session_id = SessionId::from_uuid(stable_uuid(
        format!("paseo-legacy-session:{source_key}:{}", meta.id).as_bytes(),
    ));
    let merged = archive
        .as_deref()
        .map(|archive| folded_archives::merge(&transcript, archive, &archive_path, issues))
        .transpose()
        .map_err(|message| {
            (
                archive_path.to_string_lossy().into_owned(),
                "unreadable_folded_archive".into(),
                message,
            )
        })?;
    let (mut events, blobs) = map_transcript(
        merged
            .as_ref()
            .map_or(transcript.as_slice(), |merged| merged.transcript.as_slice()),
        &meta,
        session_id,
        &transcript_path,
        merged
            .as_ref()
            .map(|merged| merged.pairing_groups.as_slice()),
        issues,
    )
    .map_err(|message| {
        (
            transcript_path.to_string_lossy().into_owned(),
            "unreadable_transcript".into(),
            message,
        )
    })?;
    if let Some(merged) = merged {
        if events.len().saturating_add(merged.provenance.len()) > MAX_SESSION_EVENTS {
            return Err((archive_path.to_string_lossy().into_owned(), "folded_archive_event_limit".into(),
                "merged history and raw provenance exceed the event limit; the entire session was left unimported".into()));
        }
        events.extend(merged.provenance);
    }

    let now = meta
        .created_at
        .or(meta.last_active)
        .unwrap_or_else(unknown_time);
    let updated = meta.last_active.unwrap_or(now);
    let provider = ProviderId::new(meta.provider.clone()).map_err(|_| {
        (
            source_key.clone(),
            "invalid_session_metadata".into(),
            "provider identifier cannot be represented safely".into(),
        )
    })?;
    let mut project = generated_project(&meta).ok_or_else(|| {
        (
            source_key.clone(),
            "invalid_session_metadata".into(),
            "project metadata could not be represented safely".into(),
        )
    })?;
    let mut workspace = generated_workspace(&meta, &project).ok_or_else(|| {
        (
            source_key.clone(),
            "invalid_session_metadata".into(),
            "workspace metadata could not be represented safely".into(),
        )
    })?;
    // Generated records are deterministic and detached from all Go worktree
    // ownership markers. Existing exact records are resolved at apply time.
    project.created_at = unknown_time();
    project.updated_at = unknown_time();
    workspace.created_at = unknown_time();
    workspace.updated_at = unknown_time();
    let mut agent = Agent::new(
        workspace.id,
        provider.clone(),
        format!("Historical session {}", meta.id),
    )
    .map_err(|_| {
        (
            source_key.clone(),
            "invalid_session_metadata".into(),
            "session title could not be represented safely".into(),
        )
    })?;
    agent.id = AgentId::from_uuid(stable_uuid(
        format!("paseo-legacy-agent:{source_key}").as_bytes(),
    ));
    agent.model = Some(ModelRef::new(provider, meta.model.clone()).map_err(|_| {
        (
            source_key.clone(),
            "invalid_session_metadata".into(),
            "model identifier cannot be represented safely".into(),
        )
    })?);
    agent.lifecycle = AgentLifecycle::Archived;
    agent.created_at = now;
    agent.updated_at = updated;
    agent.config = json!({
        "legacy_import": {
            "source": "paseo",
            "read_only": true,
            "legacy_session_id": meta.id,
            "metadata": safe_meta(&meta),
            "per_message_timestamps": false,
            "run_ids": false
        }
    });
    let session = Session {
        id: session_id,
        agent_id: agent.id,
        lifecycle: SessionLifecycle::Archived,
        provider_session_id: None,
        created_at: now,
        updated_at: updated,
    };
    let events = events
        .into_iter()
        .enumerate()
        .map(|(index, kind)| {
            let sequence = u64::try_from(index + 1).unwrap_or(u64::MAX);
            TimelineEvent {
                id: talos_domain::EventId::from_uuid(stable_uuid(
                    format!("paseo-legacy-event:{session_id}:{sequence}").as_bytes(),
                )),
                session_id,
                run_id: None,
                sequence,
                timestamp: now,
                kind,
            }
        })
        .collect();
    let hashes = blobs.iter().map(|blob| blob.hash.clone()).collect();
    Ok(Candidate {
        source_key,
        fingerprint,
        session_id,
        batch: HistoricalSessionBatch {
            project: Some(project),
            workspace,
            agent,
            session,
            events,
        },
        blobs,
        hashes,
    })
}

fn push_issue(issues: &mut Vec<Issue>, issue: Issue) {
    if issues.len() < MAX_REPORTED_ISSUES - 1 {
        issues.push(issue);
    } else if issues.len() == MAX_REPORTED_ISSUES - 1 {
        issues.push(Issue {
            path: "sessions-source".into(),
            category: "issue_limit_reached".into(),
            message: "additional session inventory issues were omitted at the bounded report limit"
                .into(),
        });
    }
}

fn source_error(source_key: &str, error: &MigrationError) -> (String, String, String) {
    let category = if matches!(error, MigrationError::SourceChanged) {
        "source_changed"
    } else {
        "unreadable_source_file"
    };
    (
        source_key.into(),
        category.into(),
        "source file could not be read consistently; no session was imported".into(),
    )
}

fn valid_legacy_id(id: &str) -> bool {
    !id.is_empty()
        && id.len() <= 128
        && id
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_'))
}

fn validate_legacy_meta(meta: &LegacyMeta) -> Result<(), String> {
    let usable_path = |value: &str| {
        !value.trim().is_empty()
            && value.len() <= 4096
            && !value.chars().any(char::is_control)
            && Path::new(value).is_absolute()
    };
    if !usable_path(&meta.dir) {
        return Err("unusable_session_directory".into());
    }
    if !meta.project_dir.is_empty() && !usable_path(&meta.project_dir) {
        return Err("unusable_project_directory".into());
    }
    if meta.provider.trim().is_empty()
        || meta.provider.len() > 128
        || meta.provider.chars().any(char::is_control)
        || meta.model.trim().is_empty()
        || meta.model.len() > 512
        || meta.model.chars().any(char::is_control)
    {
        return Err("unusable_provider_or_model".into());
    }
    Ok(())
}

fn safe_meta(meta: &LegacyMeta) -> Value {
    json!({
        "id": meta.id,
        "dir": meta.dir,
        "project_dir": if meta.project_dir.is_empty() { Value::Null } else { json!(meta.project_dir) },
        "isolation": meta.isolation,
        "branch": if meta.branch.is_empty() { Value::Null } else { json!(meta.branch) },
        "default_branch": if meta.default_branch.is_empty() { Value::Null } else { json!(meta.default_branch) },
        "merged": meta.merged,
        "provider": meta.provider,
        "model": meta.model,
        "created_at": meta.created_at,
        "last_active": meta.last_active
    })
}

fn unknown_time() -> DateTime<Utc> {
    DateTime::from_timestamp(0, 0).expect("unix epoch is valid")
}

fn stable_uuid(input: &[u8]) -> Uuid {
    Uuid::from_bytes(stable_id(input))
}

fn bounded_path_name(path: &str, fallback: &str) -> String {
    let name = Path::new(path)
        .file_name()
        .and_then(|name| name.to_str())
        .filter(|name| !name.is_empty());
    match name {
        Some(name) if name.len() <= 64 => name.to_owned(),
        _ => fallback.to_owned(),
    }
}

fn generated_project(meta: &LegacyMeta) -> Option<Project> {
    let path = if meta.project_dir.is_empty() {
        &meta.dir
    } else {
        &meta.project_dir
    };
    let name = bounded_path_name(path, "Legacy project");
    let mut project = Project::new(name, path).ok()?;
    project.id = ProjectId::from_uuid(stable_uuid(
        format!("paseo-legacy-project:{path}").as_bytes(),
    ));
    project.metadata = json!({ "legacy_import": { "source": "paseo", "read_only": true } });
    project.created_at = unknown_time();
    project.updated_at = unknown_time();
    Some(project)
}

fn generated_workspace(meta: &LegacyMeta, project: &Project) -> Option<Workspace> {
    let name = bounded_path_name(&meta.dir, "Legacy workspace");
    let mut workspace = Workspace::new(name, &meta.dir, WorkspaceKind::Directory).ok()?;
    workspace.id = WorkspaceId::from_uuid(stable_uuid(
        format!("paseo-legacy-workspace:{}", meta.dir).as_bytes(),
    ));
    workspace.project_id = Some(project.id);
    workspace.metadata = json!({ "legacy_import": { "source": "paseo", "read_only": true } });
    workspace.created_at = unknown_time();
    workspace.updated_at = unknown_time();
    Some(workspace)
}

fn map_transcript(
    bytes: &[u8],
    meta: &LegacyMeta,
    session_id: SessionId,
    transcript_path: &Path,
    pairing_groups: Option<&[usize]>,
    issues: &mut Vec<Issue>,
) -> Result<(Vec<TimelineEventKind>, Vec<AttachmentBlob>), String> {
    let mut lines = Vec::<(usize, Vec<u8>, Option<Value>)>::new();
    for (index, line) in bytes.split(|byte| *byte == b'\n').enumerate() {
        if line.is_empty() || line == b"\r" {
            continue;
        }
        if line.len() > MAX_JSONL_LINE_BYTES {
            return Err("a JSONL record exceeds the bounded per-line limit".into());
        }
        let line = line.strip_suffix(b"\r").unwrap_or(line).to_vec();
        let value = serde_json::from_slice(&line).ok();
        lines.push((index + 1, line, value));
        if lines.len() > MAX_SESSION_EVENTS {
            return Err("transcript contains more records than the bounded event limit".into());
        }
    }

    let mut uses = HashMap::<String, Vec<ToolUseRef>>::new();
    let mut results = HashMap::<String, Vec<ToolResultRef>>::new();
    let mut latest_stats = None;
    let mut block_count = 0_usize;
    for (line_index, (line_number, _, value)) in lines.iter().enumerate() {
        let Some(value) = value else { continue };
        if value.get("type").and_then(Value::as_str) == Some("stats") {
            latest_stats = Some(line_index);
        }
        let role = value.get("role").and_then(Value::as_str);
        let Some(content) = value.get("content").and_then(Value::as_array) else {
            continue;
        };
        block_count = block_count.saturating_add(content.len());
        if block_count > MAX_SESSION_EVENTS {
            return Err("transcript exceeds the bounded content-block limit".into());
        }
        for (block_index, block) in content.iter().enumerate() {
            match (role, block.get("type").and_then(Value::as_str)) {
                (Some("assistant"), Some("tool_use")) => {
                    if let Some(tool) = parse_tool_use(block, line_index, block_index) {
                        uses.entry(tool.id.clone()).or_default().push(tool);
                    }
                }
                (Some("tool"), Some("tool_result")) => {
                    if let Some(tool) = parse_tool_result(block, line_index, block_index) {
                        results.entry(tool.id.clone()).or_default().push(tool);
                    }
                }
                _ => {}
            }
        }
        let _ = line_number;
    }

    let mut paired_uses = HashMap::<(usize, usize), ToolResultRef>::new();
    let mut paired_results = HashSet::<(usize, usize)>::new();
    for (id, use_group) in &uses {
        if use_group.len() != 1 {
            continue;
        }
        let Some(result_group) = results.get(id).filter(|group| group.len() == 1) else {
            continue;
        };
        let tool_use = &use_group[0];
        let tool_result = &result_group[0];
        if (tool_result.line, tool_result.block) <= (tool_use.line, tool_use.block) {
            continue;
        }
        if pairing_groups.is_some_and(|groups| groups[tool_use.line] != groups[tool_result.line]) {
            continue;
        }
        paired_uses.insert((tool_use.line, tool_use.block), tool_result.clone());
        paired_results.insert((tool_result.line, tool_result.block));
    }

    let event_time = meta
        .created_at
        .or(meta.last_active)
        .unwrap_or_else(unknown_time);
    let mut drafts = Vec::<(usize, usize, TimelineEventKind)>::new();
    let mut blobs = Vec::<AttachmentBlob>::new();
    let mut stored_hashes = HashSet::<String>::new();
    let mut image_bytes_total = 0_usize;
    let mut image_count = 0_usize;
    let latest_stats = latest_stats;
    for (line_index, (line_number, raw_line, value)) in lines.iter().enumerate() {
        macro_rules! add_extension {
            ($block_index:expr, $kind:expr, $raw_bytes:expr, $raw_value:expr) => {{
                let payload = match (std::str::from_utf8($raw_bytes), $raw_value) {
                    (Ok(raw_json), _) => json!({ "line": line_number, "raw_json": raw_json }),
                    (_, Some(value)) => json!({ "line": line_number, "raw_value": value }),
                    _ => json!({ "line": line_number, "raw_base64": BASE64.encode($raw_bytes) }),
                };
                drafts.push((
                    line_index,
                    $block_index,
                    TimelineEventKind::Extension {
                        kind: $kind.into(),
                        payload,
                    },
                ));
            }};
        }
        macro_rules! add_block_extension {
            ($block_index:expr, $kind:expr, $raw_block:expr, $message_role:expr) => {{
                let payload = json!({
                    "line": line_number,
                    "block_index": $block_index,
                    "role": $message_role,
                    "raw_block": $raw_block
                });
                drafts.push((
                    line_index,
                    $block_index,
                    TimelineEventKind::Extension {
                        kind: $kind.into(),
                        payload,
                    },
                ));
            }};
        }
        let Some(value) = value else {
            push_issue(
                issues,
                Issue {
                    path: format!("{}:{line_number}", transcript_path.to_string_lossy()),
                    category: "malformed_jsonl_line".into(),
                    message: "malformed record was retained as a raw historical extension".into(),
                },
            );
            add_extension!(0, "legacy_malformed_line", raw_line, None::<&Value>);
            continue;
        };
        match value.get("type").and_then(Value::as_str) {
            Some("compaction") => {
                add_extension!(0, "legacy_compaction", raw_line, Some(value));
                continue;
            }
            Some("stats") => {
                if latest_stats == Some(line_index) {
                    add_extension!(0, "legacy_cumulative_stats", raw_line, Some(value));
                } else {
                    push_issue(
                        issues,
                        Issue {
                            path: format!("{}:{line_number}", transcript_path.to_string_lossy()),
                            category: "older_cumulative_stats_omitted".into(),
                            message: "only the latest cumulative stats record is retained".into(),
                        },
                    );
                }
                continue;
            }
            Some(_) => {
                add_extension!(0, "legacy_unknown_record", raw_line, Some(value));
                continue;
            }
            None => {}
        }

        let role = value
            .get("role")
            .and_then(Value::as_str)
            .unwrap_or_default();
        let Some(content) = value.get("content").and_then(Value::as_array) else {
            add_extension!(0, "legacy_unmapped_message", raw_line, Some(value));
            continue;
        };
        if !matches!(role, "user" | "assistant" | "tool") {
            add_extension!(0, "legacy_unmapped_message", raw_line, Some(value));
            continue;
        }
        let has_unknown_message_fields = value
            .as_object()
            .is_some_and(|object| object.keys().any(|key| key != "role" && key != "content"));
        if has_unknown_message_fields {
            add_extension!(0, "legacy_message_fields", raw_line, Some(value));
        }

        for (block_index, block) in content.iter().enumerate() {
            match (role, block.get("type").and_then(Value::as_str)) {
                ("user" | "assistant", Some("text")) => {
                    if let Some(text) = block
                        .get("text")
                        .and_then(Value::as_str)
                        .filter(|text| !text.is_empty())
                    {
                        drafts.push((
                            line_index,
                            block_index,
                            if role == "user" {
                                TimelineEventKind::UserText { text: text.into() }
                            } else {
                                TimelineEventKind::AssistantText { text: text.into() }
                            },
                        ));
                    } else {
                        add_block_extension!(block_index, "legacy_unmapped_block", block, role);
                    }
                }
                ("user", Some("image")) => {
                    match decode_image_block(
                        block,
                        session_id,
                        line_index,
                        block_index,
                        image_count,
                    ) {
                        Ok((attachment, blob)) => {
                            image_count += 1;
                            image_bytes_total = image_bytes_total.saturating_add(blob.bytes.len());
                            if image_count > MAX_IMAGES_PER_SESSION
                                || image_bytes_total > MAX_SESSION_IMAGE_BYTES
                            {
                                push_issue(issues, Issue {
                                    path: format!("{}:{line_number}", transcript_path.to_string_lossy()),
                                    category: "inline_image_limit_exceeded".into(),
                                    message: "image block exceeded the bounded session attachment limit and was retained raw".into(),
                                });
                                add_block_extension!(
                                    block_index,
                                    "legacy_unmapped_image",
                                    block,
                                    role
                                );
                                continue;
                            }
                            if stored_hashes.insert(blob.hash.clone()) {
                                blobs.push(blob);
                            }
                            drafts.push((
                                line_index,
                                block_index,
                                TimelineEventKind::Attachment(attachment),
                            ));
                        }
                        Err(category) => {
                            push_issue(issues, Issue {
                                path: format!(
                                    "{}:{line_number}",
                                    transcript_path.to_string_lossy()
                                ),
                                category: category.into(),
                                message:
                                    "inline image could not be safely imported and was retained raw"
                                        .into(),
                            });
                            add_block_extension!(block_index, "legacy_unmapped_image", block, role);
                        }
                    }
                }
                ("assistant", Some("tool_use")) => {
                    let Some(tool_result) = paired_uses.get(&(line_index, block_index)) else {
                        add_block_extension!(block_index, "legacy_unpaired_tool_use", block, role);
                        continue;
                    };
                    let Some(tool_use) = parse_tool_use(block, line_index, block_index) else {
                        add_block_extension!(block_index, "legacy_unpaired_tool_use", block, role);
                        continue;
                    };
                    drafts.push((
                        line_index,
                        block_index,
                        TimelineEventKind::Tool(ToolCall {
                            id: tool_use.id,
                            name: tool_use.name,
                            title: None,
                            input: tool_use.input,
                            output: Some(json!({ "content": tool_result.content, "is_error": tool_result.is_error })),
                            status: if tool_result.is_error { ToolStatus::Failed } else { ToolStatus::Completed },
                        }),
                    ));
                }
                ("tool", Some("tool_result"))
                    if paired_results.contains(&(line_index, block_index)) => {}
                ("tool", Some("tool_result")) => {
                    add_block_extension!(block_index, "legacy_unpaired_tool_result", block, role);
                }
                _ => add_block_extension!(block_index, "legacy_unmapped_block", block, role),
            }
        }
    }
    drafts.sort_by_key(|(line, block, _)| (*line, *block));
    if drafts.len() > MAX_SESSION_EVENTS {
        return Err("mapped timeline exceeds the bounded event limit".into());
    }
    let _ = event_time; // TimelineEvent timestamps use the sidecar's coarse created_at below.
    Ok((
        drafts.into_iter().map(|(_, _, event)| event).collect(),
        blobs,
    ))
}

fn parse_tool_use(block: &Value, line: usize, block_index: usize) -> Option<ToolUseRef> {
    let tool = block.get("tool_use")?;
    let id = tool.get("id")?.as_str()?.to_owned();
    let name = tool.get("name")?.as_str()?.to_owned();
    let input = tool.get("args")?.as_object()?;
    if id.is_empty() || name.is_empty() || id.len() > 512 || name.len() > 512 {
        return None;
    }
    Some(ToolUseRef {
        line,
        block: block_index,
        id,
        name,
        input: Value::Object(input.clone()),
    })
}

fn parse_tool_result(block: &Value, line: usize, block_index: usize) -> Option<ToolResultRef> {
    let tool = block.get("tool_result")?;
    let id = tool.get("tool_use_id")?.as_str()?.to_owned();
    let content = tool.get("content")?.as_str()?.to_owned();
    if id.is_empty() || id.len() > 512 {
        return None;
    }
    Some(ToolResultRef {
        line,
        block: block_index,
        id,
        content,
        is_error: tool
            .get("is_error")
            .and_then(Value::as_bool)
            .unwrap_or(false),
    })
}

fn decode_image_block(
    block: &Value,
    session_id: SessionId,
    line: usize,
    block_index: usize,
    ordinal: usize,
) -> Result<(Attachment, AttachmentBlob), &'static str> {
    let image = block.get("image").ok_or("invalid_inline_image")?;
    let declared_mime = image
        .get("media_type")
        .and_then(Value::as_str)
        .ok_or("invalid_inline_image")?;
    let encoded = image
        .get("data")
        .and_then(Value::as_str)
        .ok_or("invalid_inline_image")?;
    if encoded.len()
        > MAX_IMAGE_BYTES
            .saturating_mul(4)
            .div_ceil(3)
            .saturating_add(4)
    {
        return Err("inline_image_too_large");
    }
    let bytes = BASE64
        .decode(encoded)
        .map_err(|_| "invalid_inline_image_base64")?;
    if bytes.is_empty() {
        return Err("invalid_inline_image");
    }
    if bytes.len() > MAX_IMAGE_BYTES {
        return Err("inline_image_too_large");
    }
    let sniffed = sniff_image_mime(&bytes).ok_or("unsupported_inline_image_format")?;
    if declared_mime != sniffed {
        return Err("inline_image_mime_mismatch");
    }
    let hash = format!("{:x}", Sha256::digest(&bytes));
    let storage_key = hash.clone();
    let mut attachment = Attachment::new(
        format!("legacy-image-{}", ordinal + 1),
        sniffed,
        u64::try_from(bytes.len()).unwrap_or(u64::MAX),
        storage_key,
    )
    .map_err(|_| "invalid_inline_image")?;
    attachment.id = AttachmentId::from_uuid(stable_uuid(
        format!("paseo-legacy-attachment:{session_id}:{line}:{block_index}:{hash}").as_bytes(),
    ));
    attachment.sha256 = Some(hash.clone());
    Ok((attachment, AttachmentBlob { hash, bytes }))
}

fn sniff_image_mime(bytes: &[u8]) -> Option<&'static str> {
    if bytes.starts_with(b"\x89PNG\r\n\x1a\n") {
        Some("image/png")
    } else if bytes.starts_with(b"\xff\xd8\xff") {
        Some("image/jpeg")
    } else if bytes.starts_with(b"GIF87a") || bytes.starts_with(b"GIF89a") {
        Some("image/gif")
    } else if bytes.len() >= 12 && bytes.starts_with(b"RIFF") && &bytes[8..12] == b"WEBP" {
        Some("image/webp")
    } else {
        None
    }
}

fn apply_candidate(
    snapshot: &SourceSnapshot,
    destination: &Path,
    journal_path: &Path,
    journal: &mut Journal,
    store: &mut SqliteStore,
    candidate: Candidate,
    report: &mut Report,
) -> Result<(), MigrationError> {
    let candidate_session_id = candidate.session_id.to_string();
    for entry in &journal.session_entries {
        if (entry.source_key == candidate.source_key || entry.session_id == candidate_session_id)
            && (entry.source_key != candidate.source_key
                || entry.fingerprint != candidate.fingerprint
                || entry.session_id != candidate_session_id)
        {
            return Err(MigrationError::HistoricalSessionConflict);
        }
    }
    if let Some(pending) = &journal.pending_session
        && (pending.source_key != candidate.source_key
            || pending.fingerprint != candidate.fingerprint
            || pending.session_id != candidate.session_id.to_string()
            || pending.attachment_hashes != candidate.hashes)
    {
        return Err(MigrationError::JournalMismatch);
    }

    verify_snapshot(snapshot)?;
    let pending = PendingSessionImport {
        source_key: candidate.source_key.clone(),
        fingerprint: candidate.fingerprint.clone(),
        session_id: candidate.session_id.to_string(),
        attachment_hashes: candidate.hashes.clone(),
    };
    if journal.pending_session.as_ref().is_none_or(|current| {
        current.source_key != pending.source_key
            || current.fingerprint != pending.fingerprint
            || current.session_id != pending.session_id
            || current.attachment_hashes != pending.attachment_hashes
    }) {
        journal.pending_session = Some(pending);
        write_journal(journal_path, journal)?;
    }

    for blob in &candidate.blobs {
        persist_attachment_blob(destination, blob)?;
    }
    let imported = match store.import_historical_session_batch(&candidate.batch) {
        Ok(imported) => imported,
        Err(talos_store::StoreError::HistoricalImportConflict) => {
            journal.pending_session = None;
            write_journal(journal_path, journal)?;
            return Err(MigrationError::HistoricalSessionConflict);
        }
        Err(error) => return Err(MigrationError::Store(error)),
    };

    journal.session_entries.retain(|entry| {
        entry.source_key != candidate.source_key
            && entry.session_id != candidate.session_id.to_string()
    });
    journal.session_entries.push(SessionJournalEntry {
        source_key: candidate.source_key,
        fingerprint: candidate.fingerprint,
        session_id: candidate.session_id.to_string(),
        attachment_hashes: candidate.hashes,
        imported_at: Utc::now(),
    });
    journal.pending_session = None;
    write_journal(journal_path, journal)?;
    if imported {
        report.imported_sessions += 1;
        report
            .imported_session_ids
            .push(candidate.session_id.to_string());
        report.already_imported = false;
    }
    Ok(())
}

fn persist_attachment_blob(
    destination: &Path,
    blob: &AttachmentBlob,
) -> Result<(), MigrationError> {
    if format!("{:x}", Sha256::digest(&blob.bytes)) != blob.hash {
        return Err(MigrationError::SourceChanged);
    }
    let directory = destination.join(ATTACHMENTS_DIR_NAME);
    reject_symlink_components(&directory)?;
    match fs::symlink_metadata(&directory) {
        Ok(metadata) if metadata.file_type().is_symlink() || !metadata.is_dir() => {
            return Err(MigrationError::UnsafePath);
        }
        Ok(_) => {}
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            fs::create_dir(&directory)?;
        }
        Err(error) => return Err(error.into()),
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(&directory, fs::Permissions::from_mode(0o700))?;
    }
    sync_directory(destination)?;
    reject_symlink_components(&directory)?;
    let target = directory.join(&blob.hash);
    validate_destination_file(&target)?;
    if fs::symlink_metadata(&target).is_ok() {
        recover_linked_attachment_temporary(&directory, &target, blob)?;
        return verify_attachment_blob(&target, blob);
    }

    let temporary = directory.join(format!(".{}.tmp.{}", blob.hash, Uuid::new_v4()));
    let result = (|| -> Result<(), MigrationError> {
        let mut options = OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options
                .mode(0o600)
                .custom_flags(libc::O_NOFOLLOW | libc::O_CLOEXEC);
        }
        let mut file = options.open(&temporary)?;
        file.write_all(&blob.bytes)?;
        file.sync_all()?;
        drop(file);
        match fs::hard_link(&temporary, &target) {
            Ok(()) => {}
            Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => {
                verify_attachment_blob(&target, blob)?;
            }
            Err(error) => return Err(error.into()),
        }
        // Persist the final content-addressed name before unlinking the recovery
        // name. A crash before the next directory sync can then safely retry.
        sync_directory(&directory)?;
        fs::remove_file(&temporary)?;
        sync_directory(&directory)?;
        Ok(())
    })();
    if result.is_err() {
        let _ = fs::remove_file(&temporary);
    }
    result?;
    verify_attachment_blob(&target, blob)
}

fn recover_linked_attachment_temporary(
    directory: &Path,
    target: &Path,
    blob: &AttachmentBlob,
) -> Result<(), MigrationError> {
    validate_destination_file(target)?;
    let mut target_file = open_source_file(target)?;
    let target_metadata = target_file.metadata()?;
    if !target_metadata.is_file()
        || target_metadata.len() != u64::try_from(blob.bytes.len()).unwrap_or(u64::MAX)
    {
        return Err(MigrationError::DestinationConflict);
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::MetadataExt;
        if target_metadata.mode() & 0o077 != 0 || !(1..=2).contains(&target_metadata.nlink()) {
            return Err(MigrationError::UnsafePath);
        }
    }
    let mut target_bytes = Vec::with_capacity(blob.bytes.len());
    target_file.read_to_end(&mut target_bytes)?;
    if target_bytes != blob.bytes || format!("{:x}", Sha256::digest(&target_bytes)) != blob.hash {
        return Err(MigrationError::DestinationConflict);
    }

    let prefix = format!(".{}.tmp.", blob.hash);
    for entry in fs::read_dir(directory)? {
        let entry = entry?;
        let name = entry.file_name();
        let Some(suffix) = name
            .to_string_lossy()
            .strip_prefix(&prefix)
            .map(str::to_owned)
        else {
            continue;
        };
        if Uuid::parse_str(&suffix).is_err() {
            continue;
        }
        let temporary = entry.path();
        validate_destination_file(&temporary)?;
        let mut temp_file = open_source_file(&temporary)?;
        let temp_metadata = temp_file.metadata()?;
        let target_metadata = fs::symlink_metadata(target)?;
        #[cfg(unix)]
        let linked_to_target = {
            use std::os::unix::fs::MetadataExt;
            if temp_metadata.mode() & 0o077 != 0 {
                return Err(MigrationError::UnsafePath);
            }
            match temp_metadata.nlink() {
                1 => false,
                2 if temp_metadata.dev() == target_metadata.dev()
                    && temp_metadata.ino() == target_metadata.ino() =>
                {
                    true
                }
                _ => return Err(MigrationError::UnsafePath),
            }
        };
        #[cfg(not(unix))]
        let linked_to_target = false;
        if !temp_metadata.is_file()
            || temp_metadata.len() != u64::try_from(blob.bytes.len()).unwrap_or(u64::MAX)
        {
            return Err(MigrationError::DestinationConflict);
        }
        let mut bytes = Vec::with_capacity(blob.bytes.len());
        temp_file.read_to_end(&mut bytes)?;
        if bytes != blob.bytes || format!("{:x}", Sha256::digest(&bytes)) != blob.hash {
            return Err(MigrationError::DestinationConflict);
        }
        if linked_to_target {
            // A crash after hard_link but before unlink leaves exactly these two names.
            // Validate inode identity above before removing only the temporary name.
            fs::remove_file(&temporary)?;
        }
    }
    sync_directory(directory)?;
    Ok(())
}

fn verify_attachment_blob(path: &Path, blob: &AttachmentBlob) -> Result<(), MigrationError> {
    validate_destination_file(path)?;
    let mut file = open_source_file(path)?;
    let metadata = file.metadata()?;
    if !metadata.is_file() {
        return Err(MigrationError::UnsafePath);
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::MetadataExt;
        if metadata.nlink() != 1 || metadata.mode() & 0o077 != 0 {
            return Err(MigrationError::UnsafePath);
        }
    }
    if metadata.len() != u64::try_from(blob.bytes.len()).unwrap_or(u64::MAX) {
        return Err(MigrationError::DestinationConflict);
    }
    let mut bytes = Vec::with_capacity(blob.bytes.len());
    file.read_to_end(&mut bytes)?;
    if bytes != blob.bytes || format!("{:x}", Sha256::digest(&bytes)) != blob.hash {
        return Err(MigrationError::DestinationConflict);
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    fn write_jsonl(path: &Path, rows: &[Value]) {
        let text = rows
            .iter()
            .map(Value::to_string)
            .collect::<Vec<_>>()
            .join("\n");
        fs::write(path, format!("{text}\n")).unwrap();
    }

    fn write_meta(root: &Path, id: &str, dir: &Path, project_dir: &Path) -> PathBuf {
        fs::create_dir_all(root).unwrap();
        let metadata = root.join(format!("{id}.meta.json"));
        fs::write(
            &metadata,
            serde_json::to_vec(&json!({
                "id": id,
                "dir": dir,
                "project_dir": project_dir,
                "provider": "test-provider",
                "model": "test-model",
                "created_at": "2024-01-02T03:04:05Z",
                "last_active": "2024-01-02T03:04:06Z"
            }))
            .unwrap(),
        )
        .unwrap();
        metadata
    }

    fn write_base_source(root: &Path, projects: &[Value], workspaces: &[Value]) {
        fs::create_dir_all(root.join("projects")).unwrap();
        fs::write(
            root.join("projects/projects.json"),
            Value::Array(projects.to_vec()).to_string(),
        )
        .unwrap();
        fs::write(
            root.join("projects/workspaces.json"),
            Value::Array(workspaces.to_vec()).to_string(),
        )
        .unwrap();
    }

    fn fixture_candidate(
        root: &Path,
        id: &str,
        dir: &Path,
        project_dir: &Path,
        rows: &[Value],
    ) -> Candidate {
        write_meta(root, id, dir, project_dir);
        write_jsonl(&root.join(format!("{id}.jsonl")), rows);
        let snapshot = snapshot_source(root).unwrap();
        prepare_candidate(
            &snapshot,
            (
                PathBuf::from(format!("{id}.meta.json")),
                PathBuf::from(format!("{id}.jsonl")),
            ),
            &mut Vec::new(),
        )
        .unwrap()
    }

    #[test]
    fn imports_text_and_pairs_tool_use_with_result() {
        let temp = tempfile::tempdir().unwrap();
        let sessions = temp.path().join("sessions");
        let dir = temp.path().join("work");
        let project = temp.path().join("repo");
        let candidate = fixture_candidate(
            &sessions,
            "text-tools",
            &dir,
            &project,
            &[
                json!({"role":"user","content":[{"type":"text","text":"hello"}]}),
                json!({"role":"assistant","content":[{"type":"tool_use","tool_use":{"id":"call-1","name":"echo","args":{"x":1}}}]}),
                json!({"role":"tool","content":[{"type":"tool_result","tool_result":{"tool_use_id":"call-1","content":"done","is_error":false}}]}),
                json!({"role":"assistant","content":[{"type":"text","text":"finished"}]}),
            ],
        );
        let events = candidate.batch.events;
        assert!(matches!(&events[0].kind, TimelineEventKind::UserText { text } if text == "hello"));
        assert!(
            matches!(&events[1].kind, TimelineEventKind::Tool(tool) if tool.name == "echo" && tool.output.as_ref().is_some_and(|output| output["content"] == "done"))
        );
        assert!(
            matches!(&events[2].kind, TimelineEventKind::AssistantText { text } if text == "finished")
        );
        assert!(events.iter().all(|event| event.run_id.is_none()));
        assert_eq!(candidate.batch.agent.lifecycle, AgentLifecycle::Archived);
        assert_eq!(
            candidate.batch.session.lifecycle,
            SessionLifecycle::Archived
        );
        assert_eq!(candidate.batch.session.provider_session_id, None);
    }

    #[test]
    fn last_active_supplies_created_timestamps_when_created_at_is_missing() {
        let temp = tempfile::tempdir().unwrap();
        let sessions = temp.path().join("sessions");
        let metadata = write_meta(
            &sessions,
            "last-active-fallback",
            &temp.path().join("work"),
            &temp.path().join("repo"),
        );
        let mut metadata_value: Value =
            serde_json::from_slice(&fs::read(&metadata).unwrap()).unwrap();
        metadata_value.as_object_mut().unwrap().remove("created_at");
        fs::write(&metadata, serde_json::to_vec(&metadata_value).unwrap()).unwrap();
        write_jsonl(
            &sessions.join("last-active-fallback.jsonl"),
            &[json!({"role":"user","content":[{"type":"text","text":"history"}]})],
        );

        let snapshot = snapshot_source(&sessions).unwrap();
        let pair = discover_candidates(&snapshot, &mut Vec::new())
            .unwrap()
            .remove(0);
        let candidate = prepare_candidate(&snapshot, pair, &mut Vec::new()).unwrap();
        let last_active = DateTime::parse_from_rfc3339("2024-01-02T03:04:06Z")
            .unwrap()
            .with_timezone(&Utc);

        assert_eq!(candidate.batch.agent.created_at, last_active);
        assert_eq!(candidate.batch.agent.updated_at, last_active);
        assert_eq!(candidate.batch.session.created_at, last_active);
        assert_eq!(candidate.batch.session.updated_at, last_active);
        assert!(
            candidate
                .batch
                .events
                .iter()
                .all(|event| event.timestamp == last_active)
        );
    }

    #[test]
    fn oversized_path_basenames_use_bounded_generated_names() {
        let temp = tempfile::tempdir().unwrap();
        let sessions = temp.path().join("sessions");
        let long_project = PathBuf::from("/legacy-project").join("p".repeat(3000));
        let long_workspace = PathBuf::from("/legacy-workspace").join("w".repeat(3000));
        let candidate =
            fixture_candidate(&sessions, "long-paths", &long_workspace, &long_project, &[]);

        assert_eq!(
            candidate.batch.project.as_ref().unwrap().name,
            "Legacy project"
        );
        assert_eq!(candidate.batch.workspace.name, "Legacy workspace");
    }

    #[test]
    fn unsupported_blocks_keep_their_raw_data_without_repeating_the_full_line() {
        let temp = tempfile::tempdir().unwrap();
        let sessions = temp.path().join("sessions");
        let blocks = (0..80)
            .map(|index| {
                json!({
                    "type": "unsupported_legacy_block",
                    "index": index,
                    "data": "x".repeat(16_000)
                })
            })
            .collect::<Vec<_>>();
        let row = json!({ "role": "assistant", "content": blocks });
        let line_bytes = serde_json::to_vec(&row).unwrap();
        assert!(line_bytes.len() <= MAX_JSONL_LINE_BYTES);

        let candidate = fixture_candidate(
            &sessions,
            "many-unsupported-blocks",
            &temp.path().join("work"),
            &temp.path().join("repo"),
            &[row],
        );
        let retained_payload_bytes = candidate
            .batch
            .events
            .iter()
            .map(|event| match &event.kind {
                TimelineEventKind::Extension { payload, .. } => {
                    serde_json::to_vec(payload).unwrap().len()
                }
                _ => 0,
            })
            .sum::<usize>();

        assert_eq!(candidate.batch.events.len(), 80);
        assert!(retained_payload_bytes <= line_bytes.len() * 2);
        assert!(candidate.batch.events.iter().all(|event| matches!(
            &event.kind,
            TimelineEventKind::Extension { payload, .. }
                if payload["raw_block"]["type"] == "unsupported_legacy_block"
        )));
    }

    #[test]
    fn tool_result_before_tool_use_does_not_complete_the_tool_call() {
        let temp = tempfile::tempdir().unwrap();
        let sessions = temp.path().join("sessions");
        let candidate = fixture_candidate(
            &sessions,
            "reversed-tool-pair",
            &temp.path().join("work"),
            &temp.path().join("repo"),
            &[
                json!({"role":"tool","content":[{"type":"tool_result","tool_result":{"tool_use_id":"call-1","content":"too early","is_error":false}}]}),
                json!({"role":"assistant","content":[{"type":"tool_use","tool_use":{"id":"call-1","name":"echo","args":{}}}]}),
            ],
        );

        assert!(candidate.batch.events.iter().all(|event| matches!(
            &event.kind,
            TimelineEventKind::Extension {
                kind,
                payload: _
            } if kind == "legacy_unpaired_tool_result" || kind == "legacy_unpaired_tool_use"
        )));
        assert_eq!(candidate.batch.events.len(), 2);
    }

    #[test]
    fn malformed_and_unpaired_records_are_retained_as_extensions() {
        let temp = tempfile::tempdir().unwrap();
        let sessions = temp.path().join("sessions");
        let dir = temp.path().join("work");
        let project = temp.path().join("repo");
        write_meta(&sessions, "raw", &dir, &project);
        fs::write(
            sessions.join("raw.jsonl"),
            b"{malformed\n{\"role\":\"assistant\",\"content\":[{\"type\":\"tool_use\",\"tool_use\":{\"id\":\"orphan\",\"name\":\"unknown\",\"args\":{}}}]}\n",
        )
        .unwrap();
        let snapshot = snapshot_source(&sessions).unwrap();
        let pair = discover_candidates(&snapshot, &mut Vec::new())
            .unwrap()
            .remove(0);
        let candidate = prepare_candidate(&snapshot, pair, &mut Vec::new()).unwrap();
        assert!(matches!(
            &candidate.batch.events[0].kind,
            TimelineEventKind::Extension { kind, payload }
                if kind == "legacy_malformed_line" && payload["raw_json"] == "{malformed"
        ));
        assert!(matches!(
            &candidate.batch.events[1].kind,
            TimelineEventKind::Extension { kind, .. } if kind == "legacy_unpaired_tool_use"
        ));
    }

    #[test]
    fn imports_inline_image_by_verified_content_hash() {
        let temp = tempfile::tempdir().unwrap();
        let sessions = temp.path().join("sessions");
        let destination = temp.path().join("destination");
        fs::create_dir_all(&destination).unwrap();
        let image = b"\x89PNG\r\n\x1a\nfixture-image";
        let candidate = fixture_candidate(
            &sessions,
            "with-image",
            &temp.path().join("work"),
            &temp.path().join("repo"),
            &[
                json!({"role":"user","content":[{"type":"image","image":{"media_type":"image/png","data":BASE64.encode(image)}}]}),
            ],
        );
        let blob = &candidate.blobs[0];
        assert_eq!(blob.hash, format!("{:x}", Sha256::digest(image)));
        persist_attachment_blob(&destination, blob).unwrap();
        assert_eq!(
            fs::read(destination.join(ATTACHMENTS_DIR_NAME).join(&blob.hash)).unwrap(),
            image
        );
        assert!(
            matches!(&candidate.batch.events[0].kind, TimelineEventKind::Attachment(attachment) if attachment.sha256.as_deref() == Some(blob.hash.as_str()) && attachment.storage_key == blob.hash)
        );
        fs::write(
            destination.join(ATTACHMENTS_DIR_NAME).join(&blob.hash),
            b"corrupt",
        )
        .unwrap();
        assert!(matches!(
            persist_attachment_blob(&destination, blob),
            Err(MigrationError::DestinationConflict)
        ));
    }

    #[cfg(unix)]
    #[test]
    fn attachment_hard_link_crash_window_is_cleaned_on_retry() {
        use std::os::unix::fs::{MetadataExt, OpenOptionsExt};

        let temp = tempfile::tempdir().unwrap();
        let sessions = temp.path().join("sessions");
        let destination = temp.path().join("destination");
        fs::create_dir_all(&destination).unwrap();
        let candidate = fixture_candidate(
            &sessions,
            "linked-image",
            &temp.path().join("work"),
            &temp.path().join("repo"),
            &[
                json!({"role":"user","content":[{"type":"image","image":{"media_type":"image/png","data":BASE64.encode(b"\x89PNG\r\n\x1a\nlinked-crash")}}]}),
            ],
        );
        let blob = &candidate.blobs[0];
        let directory = destination.join(ATTACHMENTS_DIR_NAME);
        fs::create_dir(&directory).unwrap();
        let temporary = directory.join(format!(".{}.tmp.{}", blob.hash, Uuid::new_v4()));
        let target = directory.join(&blob.hash);
        let mut file = OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&temporary)
            .unwrap();
        file.write_all(&blob.bytes).unwrap();
        file.sync_all().unwrap();
        drop(file);
        fs::hard_link(&temporary, &target).unwrap();
        assert_eq!(fs::metadata(&target).unwrap().nlink(), 2);

        persist_attachment_blob(&destination, blob).unwrap();

        assert!(!temporary.exists());
        assert_eq!(fs::metadata(&target).unwrap().nlink(), 1);
        verify_attachment_blob(&target, blob).unwrap();
    }

    #[test]
    fn prepared_plan_budget_is_checked_and_never_overcommits() {
        let mut budget = PreparedBudget::default();
        assert!(budget.reserve(MAX_PREPARED_SOURCE_BYTES, 10, 100, 20));
        assert!(!budget.reserve(1, 0, 0, 0));
        assert_eq!(budget.source_bytes, MAX_PREPARED_SOURCE_BYTES);
        assert_eq!(budget.sessions, 1);

        let mut budget = PreparedBudget::default();
        assert!(budget.reserve(1, MAX_PREPARED_EVENTS, 10, 10));
        assert!(!budget.reserve(1, 1, 0, 0));
        assert_eq!(budget.events, MAX_PREPARED_EVENTS);
        assert_eq!(budget.sessions, 1);

        let mut budget = PreparedBudget::default();
        assert!(budget.reserve(1, 0, MAX_PREPARED_EVENT_BYTES, 10));
        assert!(!budget.reserve(1, 0, 1, 0));
        assert_eq!(budget.event_bytes, MAX_PREPARED_EVENT_BYTES);

        let mut budget = PreparedBudget::default();
        assert!(budget.reserve(1, 0, 10, MAX_PREPARED_BLOB_BYTES));
        assert!(!budget.reserve(1, 0, 0, 1));
        assert_eq!(budget.blob_bytes, MAX_PREPARED_BLOB_BYTES);

        let mut budget = PreparedBudget::default();
        for _ in 0..MAX_PREPARED_SESSIONS {
            assert!(budget.reserve(0, 0, 0, 0));
        }
        assert!(!budget.reserve(0, 0, 0, 0));
        assert_eq!(budget.sessions, MAX_PREPARED_SESSIONS);
    }

    #[test]
    fn image_limit_fallback_keeps_the_over_limit_block_as_raw_extension() {
        let temp = tempfile::tempdir().unwrap();
        let sessions = temp.path().join("sessions");
        let image = json!({"type":"image","image":{"media_type":"image/png","data":BASE64.encode(b"\x89PNG\r\n\x1a\nfixture")}});
        let content = vec![image; MAX_IMAGES_PER_SESSION + 1];
        let candidate = fixture_candidate(
            &sessions,
            "many-images",
            &temp.path().join("work"),
            &temp.path().join("repo"),
            &[json!({"role":"user","content":content})],
        );
        assert_eq!(
            candidate
                .batch
                .events
                .iter()
                .filter(|event| matches!(&event.kind, TimelineEventKind::Attachment(_)))
                .count(),
            MAX_IMAGES_PER_SESSION
        );
        assert!(candidate.batch.events.iter().any(|event| {
            matches!(&event.kind, TimelineEventKind::Extension { kind, .. } if kind == "legacy_unmapped_image")
        }));
    }

    #[test]
    fn archive_sidecars_are_not_separate_candidates_and_orphans_are_reported() {
        let temp = tempfile::tempdir().unwrap();
        let sessions = temp.path().join("sessions");
        let dir = temp.path().join("work");
        let project = temp.path().join("repo");
        write_meta(&sessions, "one", &dir, &project);
        write_jsonl(
            &sessions.join("one.jsonl"),
            &[json!({"role":"user","content":[{"type":"text","text":"current"}]})],
        );
        fs::write(sessions.join("one_archive.jsonl"), b"duplicate fold\n").unwrap();
        fs::write(
            sessions.join("orphan.jsonl.archive.jsonl"),
            b"folded archive data\n",
        )
        .unwrap();
        let snapshot = snapshot_source(&sessions).unwrap();
        let mut issues = Vec::new();
        let candidates = discover_candidates(&snapshot, &mut issues).unwrap();
        assert_eq!(candidates.len(), 1);
        assert_eq!(candidates[0].1, PathBuf::from("one.jsonl"));
        assert_eq!(issues.len(), 1);
        assert_eq!(issues[0].category, "orphan_folded_archive");
        assert_eq!(issues[0].path, "orphan.jsonl.archive.jsonl");
    }

    #[test]
    fn source_symlinks_and_mutation_are_rejected_without_repairing_bytes() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path().join("sessions");
        fs::create_dir_all(&root).unwrap();
        let real = temp.path().join("real.jsonl");
        fs::write(&real, b"original\n").unwrap();
        #[cfg(unix)]
        {
            std::os::unix::fs::symlink(&real, root.join("linked.jsonl")).unwrap();
            assert!(matches!(
                snapshot_source(&root),
                Err(MigrationError::UnsafePath)
            ));
            fs::remove_file(root.join("linked.jsonl")).unwrap();
        }
        fs::copy(&real, root.join("ordinary.jsonl")).unwrap();
        let snapshot = snapshot_source(&root).unwrap();
        fs::write(root.join("ordinary.jsonl"), b"changed!\n").unwrap();
        assert!(matches!(
            verify_snapshot(&snapshot),
            Err(MigrationError::SourceChanged)
        ));
        assert_eq!(
            fs::read(root.join("ordinary.jsonl")).unwrap(),
            b"changed!\n"
        );
    }

    #[test]
    fn sessions_rerun_is_idempotent_and_reads_an_old_journal() {
        let temp = tempfile::tempdir().unwrap();
        let base = temp.path().join("legacy-home");
        let sessions = temp.path().join("legacy-sessions");
        let destination = temp.path().join("new-home");
        write_base_source(&base, &[], &[]);
        write_meta(
            &sessions,
            "one",
            &temp.path().join("work"),
            &temp.path().join("repo"),
        );
        write_jsonl(
            &sessions.join("one.jsonl"),
            &[json!({"role":"user","content":[{"type":"text","text":"history"}]})],
        );
        fs::create_dir_all(&destination).unwrap();
        fs::write(destination.join(JOURNAL_FILE_NAME), r#"{"entries":[]}"#).unwrap();

        let first = super::super::migrate_with_sessions(&base, &destination, true, Some(&sessions))
            .unwrap();
        assert_eq!(first.imported_sessions, 1);
        let second =
            super::super::migrate_with_sessions(&base, &destination, true, Some(&sessions))
                .unwrap();
        assert_eq!(second.imported_sessions, 0);
        assert!(second.already_imported);
        let journal = load_journal(&destination.join(JOURNAL_FILE_NAME)).unwrap();
        assert_eq!(journal.version, crate::JOURNAL_VERSION);
        assert!(journal.pending_session.is_none());
        assert_eq!(journal.session_entries.len(), 1);
        let store = SqliteStore::open(destination.join(DATABASE_FILE_NAME)).unwrap();
        assert_eq!(
            store
                .replay_events(
                    journal.session_entries[0].session_id.parse().unwrap(),
                    None,
                    20
                )
                .unwrap()
                .len(),
            1
        );
    }

    #[test]
    fn pending_session_journal_recovers_after_the_database_commit() {
        let temp = tempfile::tempdir().unwrap();
        let base = temp.path().join("legacy-home");
        let sessions = temp.path().join("legacy-sessions");
        let destination = temp.path().join("new-home");
        write_base_source(&base, &[], &[]);
        write_meta(
            &sessions,
            "one",
            &temp.path().join("work"),
            &temp.path().join("repo"),
        );
        write_jsonl(
            &sessions.join("one.jsonl"),
            &[json!({"role":"user","content":[{"type":"text","text":"committed"}]})],
        );
        super::super::migrate(&base, &destination, true).unwrap();

        let snapshot = snapshot_source(&sessions).unwrap();
        let pair = discover_candidates(&snapshot, &mut Vec::new())
            .unwrap()
            .remove(0);
        let candidate = prepare_candidate(&snapshot, pair, &mut Vec::new()).unwrap();
        {
            let mut store = SqliteStore::open(destination.join(DATABASE_FILE_NAME)).unwrap();
            assert!(
                store
                    .import_historical_session_batch(&candidate.batch)
                    .unwrap()
            );
        }
        let mut journal = load_journal(&destination.join(JOURNAL_FILE_NAME)).unwrap();
        journal.pending_session = Some(PendingSessionImport {
            source_key: candidate.source_key,
            fingerprint: candidate.fingerprint,
            session_id: candidate.session_id.to_string(),
            attachment_hashes: candidate.hashes,
        });
        write_journal(&destination.join(JOURNAL_FILE_NAME), &journal).unwrap();

        let report =
            super::super::migrate_with_sessions(&base, &destination, true, Some(&sessions))
                .unwrap();
        assert_eq!(report.imported_sessions, 0);
        let journal = load_journal(&destination.join(JOURNAL_FILE_NAME)).unwrap();
        assert!(journal.pending_session.is_none());
        assert_eq!(journal.session_entries.len(), 1);
        let store = SqliteStore::open(destination.join(DATABASE_FILE_NAME)).unwrap();
        let session_id = journal.session_entries[0].session_id.parse().unwrap();
        assert_eq!(store.replay_events(session_id, None, 20).unwrap().len(), 1);
    }

    #[test]
    fn base_import_rows_are_reused_by_exact_compatible_path() {
        let temp = tempfile::tempdir().unwrap();
        let base = temp.path().join("legacy-home");
        let sessions = temp.path().join("legacy-sessions");
        let destination = temp.path().join("new-home");
        let project_path = temp.path().join("repo");
        let workspace_path = project_path.join("work");
        let project_string = project_path.to_string_lossy().to_string();
        let workspace_string = workspace_path.to_string_lossy().to_string();
        write_base_source(
            &base,
            &[json!({"id":"old-project","name":"repo","path":project_string})],
            &[
                json!({"id":"old-workspace","name":"work","cwd":workspace_string,"kind":"directory","projectId":"old-project"}),
            ],
        );
        write_meta(&sessions, "one", &workspace_path, &project_path);
        write_jsonl(
            &sessions.join("one.jsonl"),
            &[json!({"role":"user","content":[{"type":"text","text":"history"}]})],
        );

        let report =
            super::super::migrate_with_sessions(&base, &destination, true, Some(&sessions))
                .unwrap();
        assert_eq!(report.imported_sessions, 1);
        let store = SqliteStore::open(destination.join(DATABASE_FILE_NAME)).unwrap();
        assert_eq!(store.list_projects().unwrap().len(), 1);
        assert_eq!(store.list_workspaces().unwrap().len(), 1);
        let session_id = report.imported_session_ids[0].parse().unwrap();
        let snapshot = store.snapshot(session_id, 20).unwrap();
        let agent = store.agent(snapshot.session.agent_id).unwrap().unwrap();
        let workspace = store.workspace(agent.workspace_id).unwrap().unwrap();
        assert_eq!(workspace.path, workspace_string);
        assert_eq!(
            workspace.project_id,
            Some(store.list_projects().unwrap()[0].id)
        );
    }

    #[test]
    fn historical_import_associates_matching_unassociated_base_workspace() {
        let temp = tempfile::tempdir().unwrap();
        let base = temp.path().join("legacy-home");
        let sessions = temp.path().join("legacy-sessions");
        let destination = temp.path().join("new-home");
        let project_path = temp.path().join("repo");
        let workspace_path = project_path.join("work");
        let project_string = project_path.to_string_lossy().to_string();
        let workspace_string = workspace_path.to_string_lossy().to_string();
        write_base_source(
            &base,
            &[json!({"id":"old-project","name":"repo","path":project_string})],
            &[json!({
                "id":"old-workspace",
                "name":"work",
                "cwd":workspace_string,
                "kind":"directory",
                "archived":true,
                "pinnedAt":"2024-01-02T03:04:05Z",
                "createdAt":"2024-01-01T00:00:00Z",
                "updatedAt":"2024-01-03T00:00:00Z"
            })],
        );
        super::super::migrate(&base, &destination, true).unwrap();
        let before_store = SqliteStore::open(destination.join(DATABASE_FILE_NAME)).unwrap();
        let before_workspace = before_store.list_workspaces().unwrap().remove(0);
        let project = before_store.list_projects().unwrap().remove(0);
        assert_eq!(before_workspace.project_id, None);
        drop(before_store);

        write_meta(&sessions, "one", &workspace_path, &project_path);
        write_jsonl(
            &sessions.join("one.jsonl"),
            &[json!({"role":"user","content":[{"type":"text","text":"history"}]})],
        );
        let report =
            super::super::migrate_with_sessions(&base, &destination, true, Some(&sessions))
                .unwrap();
        assert_eq!(report.imported_sessions, 1);

        let store = SqliteStore::open(destination.join(DATABASE_FILE_NAME)).unwrap();
        let after_workspace = store.workspace(before_workspace.id).unwrap().unwrap();
        assert_eq!(after_workspace.project_id, Some(project.id));
        assert_eq!(after_workspace.name, before_workspace.name);
        assert_eq!(after_workspace.path, before_workspace.path);
        assert_eq!(after_workspace.kind, before_workspace.kind);
        assert_eq!(after_workspace.lifecycle, before_workspace.lifecycle);
        assert_eq!(after_workspace.metadata, before_workspace.metadata);
        assert_eq!(after_workspace.created_at, before_workspace.created_at);
        assert_eq!(after_workspace.updated_at, before_workspace.updated_at);
    }

    #[test]
    fn incompatible_base_path_collision_fails_before_session_pending_journal() {
        let temp = tempfile::tempdir().unwrap();
        let base = temp.path().join("legacy-home");
        let sessions = temp.path().join("legacy-sessions");
        let destination = temp.path().join("new-home");
        let project_path = temp.path().join("repo");
        let workspace_path = project_path.join("work");
        write_base_source(
            &base,
            &[json!({"id":"old-project","name":"different name","path":project_path})],
            &[
                json!({"id":"old-workspace","name":"work","cwd":workspace_path,"kind":"directory","projectId":"old-project"}),
            ],
        );
        write_meta(&sessions, "one", &workspace_path, &project_path);
        write_jsonl(
            &sessions.join("one.jsonl"),
            &[json!({"role":"user","content":[{"type":"text","text":"history"}]})],
        );

        assert!(matches!(
            super::super::migrate_with_sessions(&base, &destination, true, Some(&sessions)),
            Err(MigrationError::DestinationConflict)
        ));
        let journal = load_journal(&destination.join(JOURNAL_FILE_NAME)).unwrap();
        assert!(journal.pending_session.is_none());
        assert!(journal.session_entries.is_empty());
        let store = SqliteStore::open(destination.join(DATABASE_FILE_NAME)).unwrap();
        let workspace_id = store.list_workspaces().unwrap()[0].id;
        assert_eq!(
            store.list_agents(workspace_id).unwrap(),
            Vec::<Agent>::new()
        );
    }

    #[test]
    fn session_apply_obeys_daemon_importer_lock() {
        let temp = tempfile::tempdir().unwrap();
        let sessions = temp.path().join("sessions");
        let destination = temp.path().join("destination");
        write_meta(
            &sessions,
            "one",
            &temp.path().join("work"),
            &temp.path().join("repo"),
        );
        write_jsonl(
            &sessions.join("one.jsonl"),
            &[json!({"role":"user","content":[{"type":"text","text":"history"}]})],
        );
        fs::create_dir_all(&destination).unwrap();
        fs::write(destination.join(crate::LOCK_FILE_NAME), b"held").unwrap();
        let mut report = Report {
            source: temp.path().join("base"),
            destination: destination.clone(),
            dry_run: false,
            source_fingerprint: String::new(),
            project_records: 0,
            imported_projects: 0,
            workspace_records: 0,
            imported_workspaces: 0,
            already_imported: false,
            destination_backup: None,
            issues: Vec::new(),
            unsupported: Vec::new(),
            report_version: 2,
            sessions_source: None,
            sessions_source_fingerprint: None,
            session_candidates: 0,
            session_ids: Vec::new(),
            imported_sessions: 0,
            imported_session_ids: Vec::new(),
            attachment_hashes: Vec::new(),
        };
        assert!(matches!(
            import_sessions(&sessions, &destination, true, &mut report),
            Err(MigrationError::DestinationLocked)
        ));
        assert_eq!(
            fs::read(destination.join(crate::LOCK_FILE_NAME)).unwrap(),
            b"held"
        );
    }

    #[test]
    fn changed_session_conflicts_without_overwriting_or_pending_journal() {
        let temp = tempfile::tempdir().unwrap();
        let base = temp.path().join("legacy-home");
        let sessions = temp.path().join("legacy-sessions");
        let destination = temp.path().join("new-home");
        write_base_source(&base, &[], &[]);
        write_meta(
            &sessions,
            "one",
            &temp.path().join("work"),
            &temp.path().join("repo"),
        );
        let transcript = sessions.join("one.jsonl");
        write_jsonl(
            &transcript,
            &[json!({"role":"user","content":[{"type":"text","text":"before"}]})],
        );
        super::super::migrate_with_sessions(&base, &destination, true, Some(&sessions)).unwrap();
        write_jsonl(
            &transcript,
            &[json!({"role":"user","content":[{"type":"text","text":"after"}]})],
        );
        assert!(matches!(
            super::super::migrate_with_sessions(&base, &destination, true, Some(&sessions)),
            Err(MigrationError::HistoricalSessionConflict)
        ));
        let journal = load_journal(&destination.join(JOURNAL_FILE_NAME)).unwrap();
        assert!(journal.pending_session.is_none());
        assert_eq!(journal.session_entries.len(), 1);
    }

    fn text(role: &str, value: &str) -> Value {
        json!({"role":role,"content":[{"type":"text","text":value}]})
    }

    fn fold(messages: &[Value]) -> Value {
        json!({"type":"fold_archive","timestamp":"2024-01-02T03:04:05Z","messages":messages})
    }

    fn prepare_folded(root: &Path) -> Result<(Candidate, Vec<Issue>), (String, String, String)> {
        let snapshot = snapshot_source(root).unwrap();
        let mut issues = Vec::new();
        let pairs = discover_candidates(&snapshot, &mut issues).unwrap();
        let candidate = prepare_candidate(&snapshot, pairs[0].clone(), &mut issues)?;
        Ok((candidate, issues))
    }

    fn folded_fixture(root: &Path, current: &[Value], archives: &[Value]) {
        write_meta(root, "one", &root.join("work"), &root.join("repo"));
        write_jsonl(&root.join("one.jsonl"), current);
        write_jsonl(&root.join("one.jsonl.archive.jsonl"), archives);
    }

    fn mapped_text(candidate: &Candidate) -> Vec<&str> {
        candidate
            .batch
            .events
            .iter()
            .filter_map(|event| match &event.kind {
                TimelineEventKind::UserText { text }
                | TimelineEventKind::AssistantText { text } => Some(text.as_str()),
                _ => None,
            })
            .collect()
    }

    fn retained_bytes(candidate: &Candidate, extension_kind: &str) -> Vec<u8> {
        candidate
            .batch
            .events
            .iter()
            .filter_map(|event| match &event.kind {
                TimelineEventKind::Extension { kind, payload } if kind == extension_kind => Some(
                    BASE64
                        .decode(payload["raw_base64"].as_str().unwrap())
                        .unwrap(),
                ),
                _ => None,
            })
            .flatten()
            .collect()
    }

    #[test]
    fn multiple_folds_merge_pinned_messages_once_with_lossless_order_provenance() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path().join("sessions");
        let pinned = text("user", "pinned");
        folded_fixture(
            &root,
            &[pinned.clone(), text("assistant", "current")],
            &[
                fold(&[pinned.clone(), text("assistant", "old-1")]),
                fold(&[pinned, text("assistant", "old-2")]),
            ],
        );
        let snapshot = snapshot_source(&root).unwrap();
        let (candidate, issues) = prepare_folded(&root).unwrap();
        assert_eq!(
            mapped_text(&candidate),
            ["pinned", "old-1", "old-2", "current"]
        );
        assert_eq!(
            retained_bytes(&candidate, "legacy_fold_archive_record"),
            fs::read(root.join("one.jsonl.archive.jsonl")).unwrap()
        );
        assert_eq!(
            retained_bytes(&candidate, "legacy_fold_current_record"),
            fs::read(root.join("one.jsonl")).unwrap()
        );
        assert!(
            candidate
                .batch
                .events
                .iter()
                .any(|event| matches!(&event.kind,
            TimelineEventKind::Extension {kind, payload} if kind == "legacy_fold_merge"
                && payload["reused_occurrences"] == 2 && payload["exact_chronology"] == false))
        );
        assert!(
            issues
                .iter()
                .any(|issue| issue.category == "folded_archive_merged")
        );
        assert!(
            candidate
                .batch
                .events
                .iter()
                .enumerate()
                .all(
                    |(index, event)| event.sequence == u64::try_from(index + 1).unwrap()
                        && event.run_id.is_none()
                )
        );
        assert_eq!(candidate.batch.agent.lifecycle, AgentLifecycle::Archived);
        assert_eq!(
            candidate.batch.session.lifecycle,
            SessionLifecycle::Archived
        );
        assert_eq!(candidate.batch.session.provider_session_id, None);
        verify_snapshot(&snapshot).unwrap();
    }

    #[test]
    fn identical_turns_preserve_maximum_segment_multiplicity_and_record_all_occurrences() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path().join("sessions");
        let repeated = text("user", "repeat");
        folded_fixture(
            &root,
            &[repeated.clone(), repeated.clone(), repeated.clone()],
            &[
                fold(&[repeated.clone(), repeated.clone()]),
                fold(&[repeated]),
            ],
        );
        let (candidate, _) = prepare_folded(&root).unwrap();
        assert_eq!(mapped_text(&candidate), ["repeat", "repeat", "repeat"]);
        let archive_refs = candidate
            .batch
            .events
            .iter()
            .filter_map(|event| match &event.kind {
                TimelineEventKind::Extension { kind, payload }
                    if kind == "legacy_fold_archive_record" =>
                {
                    Some(payload["details"]["messages"].as_array().unwrap().len())
                }
                _ => None,
            })
            .sum::<usize>();
        assert_eq!(archive_refs, 3);
        assert!(candidate.batch.events.iter().any(|event| matches!(&event.kind,
            TimelineEventKind::Extension {kind, payload} if kind == "legacy_fold_merge" && payload["reused_occurrences"] == 3)));
    }

    #[test]
    fn interrupted_fold_and_duplicate_archive_record_do_not_replay_current_messages() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path().join("sessions");
        let messages = [text("user", "u"), text("assistant", "a")];
        folded_fixture(&root, &messages, &[fold(&messages), fold(&messages)]);
        let (candidate, _) = prepare_folded(&root).unwrap();
        assert_eq!(mapped_text(&candidate), ["u", "a"]);
        assert!(candidate.batch.events.iter().any(|event| matches!(&event.kind,
            TimelineEventKind::Extension {kind, payload} if kind == "legacy_fold_merge" && payload["reused_occurrences"] == 4)));
    }

    #[test]
    fn canonical_identity_ignores_json_formatting_but_preserves_message_fields() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path().join("sessions");
        let message = text("user", "same");
        let archive = fold(std::slice::from_ref(&message));
        folded_fixture(
            &root,
            std::slice::from_ref(&message),
            std::slice::from_ref(&archive),
        );
        let current =
            b"{ \"content\" : [ { \"text\":\"same\",\"type\":\"text\" } ], \"role\" : \"user\" }\r\n";
        fs::write(root.join("one.jsonl"), current).unwrap();
        let (candidate, _) = prepare_folded(&root).unwrap();
        assert_eq!(mapped_text(&candidate), ["same"]);
        assert_eq!(
            retained_bytes(&candidate, "legacy_fold_current_record"),
            current
        );

        write_jsonl(
            &root.join("one.jsonl.archive.jsonl"),
            &[fold(&[json!({
                "role":"user",
                "content":[{"type":"text","text":"same"}],
                "extra":"keep"
            })])],
        );
        let (candidate, _) = prepare_folded(&root).unwrap();
        assert_eq!(mapped_text(&candidate), ["same", "same"]);
    }

    #[test]
    fn malformed_unknown_and_non_utf8_archive_records_are_retained_exactly() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path().join("sessions");
        folded_fixture(&root, &[text("user", "current")], &[]);
        let mut archive = b"\r\n{broken\n\xff\xfe\n{\"type\":\"future\",\"messages\":[]}\n{\"type\":\"fold_archive\",\"messages\":null}\n".to_vec();
        archive.extend_from_slice(
            serde_json::to_string(&fold(&[text("assistant", "history"), Value::Null]))
                .unwrap()
                .as_bytes(),
        );
        fs::write(root.join("one.jsonl.archive.jsonl"), &archive).unwrap();
        let (candidate, issues) = prepare_folded(&root).unwrap();
        assert_eq!(mapped_text(&candidate), ["history", "current"]);
        assert_eq!(
            retained_bytes(&candidate, "legacy_fold_archive_record"),
            archive
        );
        assert!(
            issues
                .iter()
                .any(|issue| issue.category == "folded_archive_raw_records")
        );
        assert!(
            candidate
                .batch
                .events
                .iter()
                .any(|event| matches!(&event.kind,
            TimelineEventKind::Extension {kind, ..} if kind == "legacy_unmapped_message"))
        );
    }

    #[test]
    fn tool_pairs_require_unique_ids_and_a_shared_source_segment() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path().join("sessions");
        let tool_use = json!({"role":"assistant","content":[{"type":"tool_use","tool_use":{"id":"call","name":"echo","args":{}}}]});
        let result = json!({"role":"tool","content":[{"type":"tool_result","tool_result":{"tool_use_id":"call","content":"done","is_error":false}}]});
        folded_fixture(
            &root,
            &[text("user", "current")],
            &[fold(&[tool_use.clone(), result.clone()])],
        );
        let (candidate, _) = prepare_folded(&root).unwrap();
        assert_eq!(
            candidate
                .batch
                .events
                .iter()
                .filter(|event| matches!(event.kind, TimelineEventKind::Tool(_)))
                .count(),
            1
        );
        write_jsonl(
            &root.join("one.jsonl.archive.jsonl"),
            &[fold(&[tool_use]), fold(&[result])],
        );
        let (candidate, _) = prepare_folded(&root).unwrap();
        assert!(
            candidate
                .batch
                .events
                .iter()
                .all(|event| !matches!(event.kind, TimelineEventKind::Tool(_)))
        );
        assert!(
            candidate
                .batch
                .events
                .iter()
                .any(|event| matches!(&event.kind,
            TimelineEventKind::Extension {kind, ..} if kind == "legacy_unpaired_tool_result"))
        );
    }

    #[test]
    fn archive_file_line_record_and_combined_event_limits_reject_whole_session() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path().join("sessions");
        folded_fixture(&root, &[text("user", "current")], &[]);
        let archive = root.join("one.jsonl.archive.jsonl");
        File::create(&archive)
            .unwrap()
            .set_len(MAX_TRANSCRIPT_BYTES + 1)
            .unwrap();
        assert_eq!(
            prepare_folded(&root).err().unwrap().1,
            "folded_archive_too_large"
        );
        fs::write(&archive, vec![b'x'; MAX_JSONL_LINE_BYTES + 1]).unwrap();
        assert_eq!(
            prepare_folded(&root).err().unwrap().1,
            "unreadable_folded_archive"
        );
        write_jsonl(&archive, &[fold(&vec![Value::Null; MAX_SESSION_EVENTS])]);
        assert_eq!(
            prepare_folded(&root).err().unwrap().1,
            "unreadable_folded_archive"
        );
        fs::write(&archive, b"x\n".repeat(MAX_SESSION_EVENTS)).unwrap();
        assert_eq!(
            prepare_folded(&root).err().unwrap().1,
            "unreadable_folded_archive"
        );
        // Each block maps to an event; retained records must fit the same budget.
        let blocks = vec![json!({"type":"text","text":"x"}); MAX_SESSION_EVENTS / 2];
        let row = json!({"role":"user","content":blocks});
        let archive_row = fold(std::slice::from_ref(&row));
        folded_fixture(
            &root,
            std::slice::from_ref(&row),
            std::slice::from_ref(&archive_row),
        );
        // Current bytes differ only by source, so this is within the limit.
        assert!(prepare_folded(&root).is_ok());
        let row = json!({"role":"user","content":vec![json!({}); MAX_SESSION_EVENTS - 2]});
        write_jsonl(&root.join("one.jsonl"), &[text("user", "current")]);
        write_jsonl(&archive, &[fold(&[row])]);
        assert_eq!(
            prepare_folded(&root).err().unwrap().1,
            "folded_archive_event_limit"
        );
        let row = json!({"role":"user","content":vec![json!({}); MAX_SESSION_EVENTS + 1]});
        write_jsonl(&archive, &[fold(&[row])]);
        assert_eq!(
            prepare_folded(&root).err().unwrap().1,
            "unreadable_transcript"
        );
    }

    #[test]
    fn archive_mutation_is_snapshotted_and_changes_session_fingerprint() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path().join("sessions");
        folded_fixture(
            &root,
            &[text("user", "current")],
            &[fold(&[text("assistant", "old")])],
        );
        let before = snapshot_source(&root).unwrap();
        let (first, _) = prepare_folded(&root).unwrap();
        write_jsonl(
            &root.join("one.jsonl.archive.jsonl"),
            &[fold(&[text("assistant", "new")])],
        );
        assert!(matches!(
            verify_snapshot(&before),
            Err(MigrationError::SourceChanged)
        ));
        let (second, _) = prepare_folded(&root).unwrap();
        assert_eq!(first.session_id, second.session_id);
        assert_ne!(first.fingerprint, second.fingerprint);
    }

    #[test]
    fn folded_images_reruns_and_each_durable_crash_window_recover_exactly() {
        for stage in 0..3 {
            let temp = tempfile::tempdir().unwrap();
            let base = temp.path().join("base");
            let root = temp.path().join("sessions");
            let destination = temp.path().join("destination");
            write_base_source(&base, &[], &[]);
            let image_bytes = b"\x89PNG\r\n\x1a\narchived-image";
            let image = json!({"role":"user","content":[{"type":"image","image":{"media_type":"image/png","data":BASE64.encode(image_bytes)}}]});
            folded_fixture(
                &root,
                &[image.clone(), text("assistant", "current")],
                &[
                    fold(&[image.clone(), text("assistant", "old")]),
                    fold(&[image]),
                ],
            );
            let source = snapshot_source(&root).unwrap();
            super::super::migrate(&base, &destination, true).unwrap();
            let (candidate, _) = prepare_folded(&root).unwrap();
            assert_eq!(candidate.blobs.len(), 1);
            assert_eq!(candidate.blobs[0].bytes, image_bytes);
            assert_eq!(
                candidate
                    .batch
                    .events
                    .iter()
                    .filter(|event| matches!(event.kind, TimelineEventKind::Attachment(_)))
                    .count(),
                1
            );
            let expected_events = candidate.batch.events.clone();
            let session_id = candidate.session_id;
            let mut journal = load_journal(&destination.join(JOURNAL_FILE_NAME)).unwrap();
            journal.pending_session = Some(PendingSessionImport {
                source_key: candidate.source_key.clone(),
                fingerprint: candidate.fingerprint.clone(),
                session_id: session_id.to_string(),
                attachment_hashes: candidate.hashes.clone(),
            });
            write_journal(&destination.join(JOURNAL_FILE_NAME), &journal).unwrap();
            if stage >= 1 {
                for blob in &candidate.blobs {
                    persist_attachment_blob(&destination, blob).unwrap();
                }
            }
            if stage == 2 {
                SqliteStore::open(destination.join(DATABASE_FILE_NAME))
                    .unwrap()
                    .import_historical_session_batch(&candidate.batch)
                    .unwrap();
            }
            let applied =
                super::super::migrate_with_sessions(&base, &destination, true, Some(&root))
                    .unwrap();
            assert_eq!(applied.imported_sessions, usize::from(stage != 2));
            let rerun = super::super::migrate_with_sessions(&base, &destination, true, Some(&root))
                .unwrap();
            assert_eq!(rerun.imported_sessions, 0);
            let journal = load_journal(&destination.join(JOURNAL_FILE_NAME)).unwrap();
            assert_eq!(journal.session_entries.len(), 1);
            assert!(journal.pending_session.is_none());
            let store = SqliteStore::open(destination.join(DATABASE_FILE_NAME)).unwrap();
            assert_eq!(
                store.replay_events(session_id, None, 100).unwrap(),
                expected_events
            );
            for blob in &candidate.blobs {
                let path = destination.join(ATTACHMENTS_DIR_NAME).join(&blob.hash);
                verify_attachment_blob(&path, blob).unwrap();
                #[cfg(unix)]
                {
                    use std::os::unix::fs::PermissionsExt;
                    assert_eq!(
                        fs::metadata(&path).unwrap().permissions().mode() & 0o777,
                        0o600
                    );
                }
                fs::write(path, b"tampered").unwrap();
            }
            assert!(
                super::super::migrate_with_sessions(&base, &destination, true, Some(&root))
                    .is_err()
            );
            verify_snapshot(&source).unwrap();
        }
    }

    #[test]
    fn oversize_archive_never_imports_current_only_and_cannot_hide_a_pending_session() {
        let temp = tempfile::tempdir().unwrap();
        let base = temp.path().join("base");
        let root = temp.path().join("sessions");
        let destination = temp.path().join("destination");
        write_base_source(&base, &[], &[]);
        folded_fixture(
            &root,
            &[text("user", "current")],
            &[fold(&[text("assistant", "old")])],
        );
        let (candidate, _) = prepare_folded(&root).unwrap();
        File::create(root.join("one.jsonl.archive.jsonl"))
            .unwrap()
            .set_len(MAX_TRANSCRIPT_BYTES + 1)
            .unwrap();
        let report =
            super::super::migrate_with_sessions(&base, &destination, true, Some(&root)).unwrap();
        assert_eq!(report.imported_sessions, 0);
        assert_eq!(report.session_ids.len(), 0);
        assert!(
            report
                .issues
                .iter()
                .any(|issue| issue.category == "prepared_plan_limit")
        );
        let mut journal = load_journal(&destination.join(JOURNAL_FILE_NAME)).unwrap();
        journal.pending_session = Some(PendingSessionImport {
            source_key: candidate.source_key,
            fingerprint: candidate.fingerprint,
            session_id: candidate.session_id.to_string(),
            attachment_hashes: candidate.hashes,
        });
        write_journal(&destination.join(JOURNAL_FILE_NAME), &journal).unwrap();
        assert!(matches!(
            super::super::migrate_with_sessions(&base, &destination, true, Some(&root)),
            Err(MigrationError::JournalMismatch)
        ));
        assert!(
            load_journal(&destination.join(JOURNAL_FILE_NAME))
                .unwrap()
                .pending_session
                .is_some()
        );
    }

    #[test]
    fn changing_or_removing_imported_archive_conflicts_without_overwriting_history() {
        let temp = tempfile::tempdir().unwrap();
        let base = temp.path().join("base");
        let root = temp.path().join("sessions");
        let destination = temp.path().join("destination");
        write_base_source(&base, &[], &[]);
        folded_fixture(
            &root,
            &[text("user", "current")],
            &[fold(&[text("assistant", "old")])],
        );
        let first =
            super::super::migrate_with_sessions(&base, &destination, true, Some(&root)).unwrap();
        let session_id = first.imported_session_ids[0].parse().unwrap();
        let before = SqliteStore::open(destination.join(DATABASE_FILE_NAME))
            .unwrap()
            .replay_events(session_id, None, 100)
            .unwrap();
        fs::write(root.join("one.jsonl.archive.jsonl"), b"changed\n").unwrap();
        assert!(matches!(
            super::super::migrate_with_sessions(&base, &destination, true, Some(&root)),
            Err(MigrationError::HistoricalSessionConflict)
        ));
        fs::remove_file(root.join("one.jsonl.archive.jsonl")).unwrap();
        assert!(matches!(
            super::super::migrate_with_sessions(&base, &destination, true, Some(&root)),
            Err(MigrationError::HistoricalSessionConflict)
        ));
        assert_eq!(
            SqliteStore::open(destination.join(DATABASE_FILE_NAME))
                .unwrap()
                .replay_events(session_id, None, 100)
                .unwrap(),
            before
        );
        assert!(
            load_journal(&destination.join(JOURNAL_FILE_NAME))
                .unwrap()
                .pending_session
                .is_none()
        );
    }
}
