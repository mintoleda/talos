//! Read-only inventory and explicit import of the portable subset of legacy Paseo data.
#![forbid(unsafe_code)]

use chrono::{DateTime, Utc};
use rusqlite::{Connection, OpenFlags, backup::Backup};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use sha2::{Digest, Sha256};
use std::{
    fs::{self, OpenOptions},
    io::Write,
    path::{Component, Path, PathBuf},
    time::Duration,
};
use talos_domain::{Workspace, WorkspaceId, WorkspaceKind, WorkspaceLifecycle};
use talos_store::SqliteStore;
use thiserror::Error;
use uuid::Uuid;

/// Database filename used by `talos-core::HomePaths`.
pub const DATABASE_FILE_NAME: &str = "state.sqlite3";
/// Filename used by the first importer revision. It is never opened or modified.
const LEGACY_DATABASE_FILE_NAME: &str = "state.sqlite";
const JOURNAL_FILE_NAME: &str = "import-journal.json";
const BACKUP_FILE_PREFIX: &str = "state.sqlite3.pre-import-";
/// Shared with `talosd`; `create_new` is the exclusion primitive on both sides.
const LOCK_FILE_NAME: &str = "talosd.lock";
const PROJECTS_METADATA_KEY: &str = "legacy_import_projects";

#[derive(Debug, Error)]
pub enum MigrationError {
    #[error("I/O error while accessing migration data")]
    Io(#[from] std::io::Error),
    #[error("destination store error")]
    Store(#[from] talos_store::StoreError),
    #[error("migration journal is invalid; refusing to modify the destination")]
    JournalCorrupt,
    #[error("migration journal could not be written")]
    JournalWrite(#[source] serde_json::Error),
    #[error("source and destination paths overlap")]
    PathOverlap,
    #[error("migration path contains a symbolic link or an unexpected file type")]
    UnsafePath,
    #[error("source data is not available")]
    SourceMissing,
    #[error("source data changed during migration; no further changes were made")]
    SourceChanged,
    #[error("destination contains conflicting workspace records; no records were imported")]
    DestinationConflict,
    #[error(
        "destination is locked by a running or stale daemon/importer; stop it and remove the lock only after confirming it is safe"
    )]
    DestinationLocked,
    #[error("an unfinished migration does not match this source; refusing to continue")]
    JournalMismatch,
    #[error("destination database failed its integrity check")]
    DestinationUnhealthy,
    #[error("destination database backup could not be created or verified")]
    DestinationBackup(#[source] rusqlite::Error),
    #[error("destination database backup failed its integrity check")]
    DestinationBackupUnhealthy,
    #[error("destination database could not be restored from its verified backup")]
    DestinationRestoreFailed,
    #[error("destination records could not be verified after import")]
    IncompleteImport,
    #[error("legacy importer database has active sidecars; close the old importer and retry")]
    LegacyDatabaseBusy,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Issue {
    pub path: String,
    pub category: String,
    pub message: String,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Report {
    pub source: PathBuf,
    pub destination: PathBuf,
    pub dry_run: bool,
    pub source_fingerprint: String,
    pub project_records: usize,
    pub workspace_records: usize,
    pub imported_workspaces: usize,
    pub already_imported: bool,
    /// Verified snapshot of the destination database from before apply, when one existed.
    #[serde(default)]
    pub destination_backup: Option<PathBuf>,
    pub issues: Vec<Issue>,
    pub unsupported: Vec<String>,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
struct JournalEntry {
    fingerprint: String,
    imported_at: DateTime<Utc>,
    /// Missing in journal entries written by the first importer revision.
    #[serde(default = "legacy_database_name")]
    database: String,
    #[serde(default)]
    workspace_ids: Vec<String>,
    #[serde(default)]
    destination_backup: Option<String>,
}
impl JournalEntry {
    fn matches(&self, fingerprint: &str) -> bool {
        self.fingerprint == fingerprint && self.database == DATABASE_FILE_NAME
    }
}
#[derive(Clone, Debug, Serialize, Deserialize)]
struct PendingImport {
    fingerprint: String,
    database: String,
    #[serde(default)]
    workspace_ids: Vec<String>,
    #[serde(default)]
    destination_backup: Option<String>,
}
#[derive(Clone, Debug, Default, Serialize, Deserialize)]
struct Journal {
    #[serde(default)]
    entries: Vec<JournalEntry>,
    #[serde(default)]
    pending: Option<PendingImport>,
}
fn legacy_database_name() -> String {
    LEGACY_DATABASE_FILE_NAME.into()
}

/// Inventory or import the conservative portable subset. Scanning never follows symlinks.
// Keep the safety checks and ordered apply steps together for auditability.
#[allow(clippy::too_many_lines)]
pub fn migrate(source: &Path, destination: &Path, apply: bool) -> Result<Report, MigrationError> {
    let (source_root, destination_root) = validate_paths(source, destination, apply)?;
    let mut issues = Vec::new();
    if !source_root.exists() {
        issues.push(Issue {
            path: "source".into(),
            category: "source_missing".into(),
            message: "source directory does not exist; no records were read".into(),
        });
    }
    validate_source_tree(&source_root)?;

    let source_files = [
        (
            "projects/projects.json",
            source_root.join("projects/projects.json"),
        ),
        (
            "projects/workspaces.json",
            source_root.join("projects/workspaces.json"),
        ),
    ];
    let fingerprint = fingerprint(&source_files);
    let projects = read_records(&source_files[0].1, source_files[0].0, &mut issues);
    let workspaces = read_records(&source_files[1].1, source_files[1].0, &mut issues);
    ensure_source_unchanged(&source_files, &fingerprint)?;
    let journal_path = destination_root.join(JOURNAL_FILE_NAME);
    let journal = load_journal(&journal_path)?;
    let converted = prepare_workspaces(&workspaces, &mut issues);
    let expected_ids = converted
        .iter()
        .map(|workspace| workspace.id.to_string())
        .collect::<Vec<_>>();

    if let Some(pending) = &journal.pending {
        let expected_backup = destination_backup_name(&fingerprint);
        if pending.database != DATABASE_FILE_NAME || pending.fingerprint != fingerprint {
            return Err(MigrationError::JournalMismatch);
        }
        if !pending.workspace_ids.is_empty() && pending.workspace_ids != expected_ids {
            return Err(MigrationError::JournalMismatch);
        }
        if pending
            .destination_backup
            .as_deref()
            .is_some_and(|name| Some(name) != expected_backup.as_deref())
        {
            return Err(MigrationError::JournalMismatch);
        }
    }

    let db_path = destination_root.join(DATABASE_FILE_NAME);
    let legacy_db_path = destination_root.join(LEGACY_DATABASE_FILE_NAME);
    validate_database_file(&db_path)?;
    validate_destination_file(&legacy_db_path)?;
    let target_present = db_path.exists();
    let legacy_present = fs::symlink_metadata(&legacy_db_path).is_ok();
    if legacy_present {
        issues.push(Issue {
            path: LEGACY_DATABASE_FILE_NAME.into(),
            category: if target_present {
                "legacy_database_ignored"
            } else {
                "legacy_database_pending"
            }
            .into(),
            message: if target_present {
                "existing state.sqlite is left untouched because state.sqlite3 already exists".into()
            } else {
                "legacy state.sqlite will be copied to state.sqlite3 during apply; the original is preserved".into()
            },
        });
    }
    let already_imported = journal
        .entries
        .iter()
        .any(|entry| entry.matches(&fingerprint))
        && target_present;
    let journal_backup =
        journal_backup_name(&journal, &fingerprint).map(|name| destination_root.join(name));
    let safe_projects = Value::Array(projects.iter().map(safe_project_metadata).collect());
    let mut report = Report {
        source: source.into(),
        destination: destination.into(),
        dry_run: !apply,
        source_fingerprint: fingerprint.clone(),
        project_records: projects.len(),
        workspace_records: workspaces.len(),
        imported_workspaces: 0,
        already_imported,
        destination_backup: journal_backup,
        issues,
        unsupported: unsupported_features(),
    };

    if apply {
        let plan = ImportPlan {
            source_files: &source_files,
            fingerprint: &fingerprint,
            converted: &converted,
            expected_ids: &expected_ids,
            safe_projects: &safe_projects,
            journal: &journal,
            journal_path: &journal_path,
            already_imported,
            legacy_db_path: &legacy_db_path,
            legacy_present,
        };
        let outcome = apply_import(&destination_root, &plan)?;
        report.imported_workspaces = outcome.imported_workspaces;
        report.already_imported = outcome.already_imported;
        report.destination_backup = outcome.destination_backup;
        report.dry_run = false;
    }

    ensure_source_unchanged(&source_files, &fingerprint)?;
    Ok(report)
}

fn unsupported_features() -> Vec<String> {
    vec![
        "credentials and identity/token files".into(),
        "agents, sessions, prompts, timeline and runtime/provider state".into(),
        "permission requests and stale claims".into(),
        "attachments, worktrees, plugins, model caches, schedules and desktop state".into(),
    ]
}

struct ImportPlan<'a> {
    source_files: &'a [(&'a str, PathBuf)],
    fingerprint: &'a str,
    converted: &'a [Workspace],
    expected_ids: &'a [String],
    safe_projects: &'a Value,
    journal: &'a Journal,
    journal_path: &'a Path,
    already_imported: bool,
    legacy_db_path: &'a Path,
    legacy_present: bool,
}

struct ApplyOutcome {
    imported_workspaces: usize,
    already_imported: bool,
    destination_backup: Option<PathBuf>,
}

fn apply_import(
    destination_root: &Path,
    plan: &ImportPlan<'_>,
) -> Result<ApplyOutcome, MigrationError> {
    create_destination(destination_root)?;
    validate_destination_file(plan.journal_path)?;
    let db_path = destination_root.join(DATABASE_FILE_NAME);
    validate_database_file(&db_path)?;
    let _import_lock = acquire_import_lock(destination_root)?;
    validate_database_file(&db_path)?;

    let created_database = !db_path.exists();
    if created_database
        && plan.legacy_present
        && let Err(error) = ensure_source_unchanged(plan.source_files, plan.fingerprint)
            .and_then(|()| seed_from_legacy_database(plan.legacy_db_path, &db_path))
    {
        cleanup_failed_import(true, &db_path, plan.journal_path, plan.journal);
        return Err(error);
    }

    let known_backup_name = journal_backup_name(plan.journal, plan.fingerprint);
    let backup = if created_database || (plan.already_imported && plan.journal.pending.is_none()) {
        None
    } else {
        Some(create_destination_backup(
            &db_path,
            plan.fingerprint,
            plan.journal,
        )?)
    };
    let backup_name = backup
        .as_ref()
        .and_then(|backup| backup.path.file_name())
        .and_then(|name| name.to_str())
        .map(str::to_owned)
        .or(known_backup_name);
    let mut pending_written = false;
    let result =
        apply_existing_destination(plan, &db_path, backup_name.as_deref(), &mut pending_written);
    match result {
        Ok(mut outcome) => {
            outcome.destination_backup = backup
                .map(|backup| backup.path)
                .or_else(|| backup_name.map(|name| destination_root.join(name)));
            Ok(outcome)
        }
        Err(error) => {
            if created_database {
                cleanup_failed_import(true, &db_path, plan.journal_path, plan.journal);
            } else if let Some(backup) = &backup {
                if restore_destination_backup(&backup.path, &db_path).is_err() {
                    return Err(MigrationError::DestinationRestoreFailed);
                }
                // A pending record points to this snapshot and makes a retry safe. Before
                // that record is durable, discard only a new, unreferenced backup.
                if !pending_written
                    && plan.journal.pending.is_none()
                    && backup.created
                    && !plan.already_imported
                {
                    remove_database_files(&backup.path);
                }
            }
            Err(error)
        }
    }
}

fn apply_existing_destination(
    plan: &ImportPlan<'_>,
    db_path: &Path,
    backup_name: Option<&str>,
    pending_written: &mut bool,
) -> Result<ApplyOutcome, MigrationError> {
    // `SqliteStore::open` can apply schema migrations before conflicts are known.
    // The caller has made a verified backup and restores it if this operation fails.
    let DestinationState {
        mut store,
        existing,
        metadata: existing_metadata,
    } = open_destination_state(db_path)?;
    validate_conflicts(plan.converted, &existing)?;
    if existing_metadata
        .as_ref()
        .is_some_and(|metadata| metadata != plan.safe_projects)
    {
        return Err(MigrationError::DestinationConflict);
    }

    if plan.already_imported
        && import_is_complete(
            plan.converted,
            &existing,
            existing_metadata.as_ref(),
            plan.safe_projects,
        )
    {
        ensure_source_unchanged(plan.source_files, plan.fingerprint)?;
        let mut updated = plan.journal.clone();
        updated.pending = None;
        if let Some(entry) = updated
            .entries
            .iter_mut()
            .find(|entry| entry.matches(plan.fingerprint))
        {
            entry.destination_backup = backup_name.map(str::to_owned);
        }
        write_journal(plan.journal_path, &updated)?;
        return Ok(ApplyOutcome {
            imported_workspaces: 0,
            already_imported: true,
            destination_backup: None,
        });
    }

    ensure_source_unchanged(plan.source_files, plan.fingerprint)?;
    let pending = PendingImport {
        fingerprint: plan.fingerprint.into(),
        database: DATABASE_FILE_NAME.into(),
        workspace_ids: plan.expected_ids.to_vec(),
        destination_backup: backup_name.map(str::to_owned),
    };
    let mut pending_journal = plan.journal.clone();
    pending_journal.pending = Some(pending);
    write_journal(plan.journal_path, &pending_journal)?;
    *pending_written = true;

    let imported = apply_to_store(
        &mut store,
        db_path,
        plan.converted,
        plan.safe_projects,
        &existing,
        existing_metadata.as_ref(),
    )?;
    let imported_workspaces = finalize_import(plan, imported, backup_name)?;
    Ok(ApplyOutcome {
        imported_workspaces,
        already_imported: false,
        destination_backup: None,
    })
}

fn finalize_import(
    plan: &ImportPlan<'_>,
    imported: usize,
    backup_name: Option<&str>,
) -> Result<usize, MigrationError> {
    ensure_source_unchanged(plan.source_files, plan.fingerprint)?;
    let mut updated = plan.journal.clone();
    updated.pending = None;
    updated
        .entries
        .retain(|entry| !entry.matches(plan.fingerprint));
    updated.entries.push(JournalEntry {
        fingerprint: plan.fingerprint.into(),
        imported_at: Utc::now(),
        database: DATABASE_FILE_NAME.into(),
        workspace_ids: plan.expected_ids.to_vec(),
        destination_backup: backup_name.map(str::to_owned),
    });
    write_journal(plan.journal_path, &updated)?;
    Ok(imported)
}

fn validate_paths(
    source: &Path,
    destination: &Path,
    apply: bool,
) -> Result<(PathBuf, PathBuf), MigrationError> {
    let source_absolute = absolute_path(source)?;
    let destination_absolute = absolute_path(destination)?;
    // Check the unresolved components first so `link/..` cannot hide a symlink.
    reject_symlink_components(&source_absolute)?;
    reject_symlink_components(&destination_absolute)?;
    let source_root = normalize_lexical(&source_absolute);
    let destination_root = normalize_lexical(&destination_absolute);
    // A missing component before `..` can hide the real target above, so check the
    // normalized path as well.
    reject_symlink_components(&source_root)?;
    reject_symlink_components(&destination_root)?;
    if source_root == destination_root
        || source_root.starts_with(&destination_root)
        || destination_root.starts_with(&source_root)
    {
        return Err(MigrationError::PathOverlap);
    }
    if !source_root.exists() {
        if apply {
            return Err(MigrationError::SourceMissing);
        }
    } else if !source_root.is_dir() {
        return Err(MigrationError::UnsafePath);
    }
    if destination_root.exists() && !destination_root.is_dir() {
        return Err(MigrationError::UnsafePath);
    }
    Ok((source_root, destination_root))
}

fn validate_source_tree(source: &Path) -> Result<(), MigrationError> {
    // This rejects a symlinked `projects` directory before any file below it is read.
    reject_symlink_components(&source.join("projects"))
}

fn validate_destination_file(path: &Path) -> Result<(), MigrationError> {
    match fs::symlink_metadata(path) {
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(error) => Err(error.into()),
        Ok(metadata) if metadata.file_type().is_symlink() || !metadata.is_file() => {
            Err(MigrationError::UnsafePath)
        }
        Ok(_) => Ok(()),
    }
}

fn validate_database_file(path: &Path) -> Result<(), MigrationError> {
    validate_destination_file(path)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::MetadataExt;
        if fs::symlink_metadata(path).is_ok_and(|metadata| metadata.nlink() > 1) {
            return Err(MigrationError::UnsafePath);
        }
    }
    let main_present = path.exists();
    for suffix in ["-wal", "-shm", "-journal"] {
        let mut sidecar = path.as_os_str().to_os_string();
        sidecar.push(suffix);
        let sidecar = PathBuf::from(sidecar);
        match fs::symlink_metadata(&sidecar) {
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
            Err(error) => return Err(error.into()),
            Ok(metadata) if metadata.file_type().is_symlink() || !metadata.is_file() => {
                return Err(MigrationError::UnsafePath);
            }
            Ok(_) if !main_present => return Err(MigrationError::UnsafePath),
            Ok(metadata) => {
                #[cfg(unix)]
                {
                    use std::os::unix::fs::MetadataExt;
                    if metadata.nlink() > 1 {
                        return Err(MigrationError::UnsafePath);
                    }
                }
                #[cfg(not(unix))]
                let _ = metadata;
            }
        }
    }
    Ok(())
}

fn destination_backup_name(fingerprint: &str) -> Option<String> {
    let digest = fingerprint.strip_prefix("sha256:")?;
    if digest.len() != 64 || !digest.bytes().all(|byte| byte.is_ascii_hexdigit()) {
        return None;
    }
    Some(format!("{BACKUP_FILE_PREFIX}{digest}.bak"))
}

fn journal_backup_name(journal: &Journal, fingerprint: &str) -> Option<String> {
    let expected = destination_backup_name(fingerprint)?;
    journal
        .pending
        .as_ref()
        .filter(|pending| {
            pending.fingerprint == fingerprint && pending.database == DATABASE_FILE_NAME
        })
        .and_then(|pending| pending.destination_backup.clone())
        .or_else(|| {
            journal
                .entries
                .iter()
                .find(|entry| entry.matches(fingerprint))
                .and_then(|entry| entry.destination_backup.clone())
        })
        .filter(|name| *name == expected)
}

fn pending_references_backup(journal: &Journal, fingerprint: &str, name: &str) -> bool {
    journal.pending.as_ref().is_some_and(|pending| {
        pending.fingerprint == fingerprint
            && pending.database == DATABASE_FILE_NAME
            && pending.destination_backup.as_deref() == Some(name)
    })
}

struct BackupArtifact {
    path: PathBuf,
    created: bool,
}

/// Make a consistent online snapshot, including committed pages in a SQLite WAL.
/// An unjournaled file at the deterministic name is stale or orphaned and is refreshed;
/// only a pending journal entry authorizes reuse after an interrupted import.
fn create_destination_backup(
    database: &Path,
    fingerprint: &str,
    journal: &Journal,
) -> Result<BackupArtifact, MigrationError> {
    let parent = database.parent().ok_or(MigrationError::UnsafePath)?;
    let name = destination_backup_name(fingerprint).ok_or(MigrationError::UnsafePath)?;
    let path = parent.join(&name);
    validate_database_file(&path)?;
    if path.exists() {
        if pending_references_backup(journal, fingerprint, &name) {
            verify_backup(&path)?;
            return Ok(BackupArtifact {
                path,
                created: false,
            });
        }
        // A deterministic name alone does not prove that an artifact is the
        // snapshot for this attempt. Refresh unjournaled or completed-import files.
        remove_database_files(&path);
    }

    let temporary = parent.join(format!(
        ".{}.tmp.{}",
        path.file_name().unwrap().to_string_lossy(),
        Uuid::new_v4()
    ));
    let mut created = false;
    let result = (|| -> Result<(), MigrationError> {
        let mut options = OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(0o600);
        }
        drop(options.open(&temporary)?);

        sqlite_backup(database, &temporary).map_err(MigrationError::DestinationBackup)?;
        verify_backup(&temporary)?;
        OpenOptions::new().read(true).open(&temporary)?.sync_all()?;
        match fs::hard_link(&temporary, &path) {
            Ok(()) => created = true,
            Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => {
                validate_database_file(&path)?;
                if !pending_references_backup(journal, fingerprint, &name) {
                    return Err(MigrationError::DestinationBackup(
                        rusqlite::Error::InvalidQuery,
                    ));
                }
                verify_backup(&path)?;
            }
            Err(error) => return Err(error.into()),
        }
        fs::remove_file(&temporary)?;
        sync_directory(parent)?;
        Ok(())
    })();
    if let Err(error) = result {
        remove_database_files(&temporary);
        if created {
            remove_database_files(&path);
        }
        return Err(error);
    }

    validate_database_file(&path)?;
    verify_backup(&path)?;
    Ok(BackupArtifact { path, created })
}

fn sqlite_backup(source: &Path, destination: &Path) -> rusqlite::Result<()> {
    let source = Connection::open_with_flags(source, OpenFlags::SQLITE_OPEN_READ_ONLY)?;
    let mut destination = Connection::open(destination)?;
    source.busy_timeout(Duration::from_secs(5))?;
    destination.busy_timeout(Duration::from_secs(5))?;
    let backup = Backup::new(&source, &mut destination)?;
    backup.run_to_completion(128, Duration::from_millis(10), None)?;
    drop(backup);
    drop(destination);
    drop(source);
    Ok(())
}

fn verify_backup(path: &Path) -> Result<(), MigrationError> {
    let connection = Connection::open_with_flags(path, OpenFlags::SQLITE_OPEN_READ_ONLY)
        .map_err(MigrationError::DestinationBackup)?;
    let result = connection
        .query_row("PRAGMA integrity_check", [], |row| row.get::<_, String>(0))
        .map_err(MigrationError::DestinationBackup)?;
    if result == "ok" {
        Ok(())
    } else {
        Err(MigrationError::DestinationBackupUnhealthy)
    }
}

fn restore_destination_backup(backup: &Path, database: &Path) -> rusqlite::Result<()> {
    sqlite_backup(backup, database)?;
    let connection = Connection::open_with_flags(database, OpenFlags::SQLITE_OPEN_READ_ONLY)?;
    let result =
        connection.query_row("PRAGMA integrity_check", [], |row| row.get::<_, String>(0))?;
    if result == "ok" {
        Ok(())
    } else {
        Err(rusqlite::Error::InvalidQuery)
    }
}

#[cfg(unix)]
fn sync_directory(path: &Path) -> Result<(), MigrationError> {
    OpenOptions::new().read(true).open(path)?.sync_all()?;
    Ok(())
}

#[cfg(not(unix))]
fn sync_directory(_path: &Path) -> Result<(), MigrationError> {
    Ok(())
}

fn absolute_path(path: &Path) -> Result<PathBuf, MigrationError> {
    if path.is_absolute() {
        Ok(path.to_path_buf())
    } else {
        Ok(std::env::current_dir()?.join(path))
    }
}

/// Safe only after `reject_symlink_components` has rejected every existing symlink.
fn normalize_lexical(path: &Path) -> PathBuf {
    let mut normalized = PathBuf::new();
    for component in path.components() {
        match component {
            Component::Prefix(prefix) => normalized.push(prefix.as_os_str()),
            Component::RootDir => normalized.push(component.as_os_str()),
            Component::CurDir => {}
            Component::ParentDir => {
                normalized.pop();
            }
            Component::Normal(part) => normalized.push(part),
        }
    }
    normalized
}

/// Reject every existing symlink component, including ancestors. Missing tails are allowed
/// so a destination can be created, but no existing path component may be a symlink.
fn reject_symlink_components(path: &Path) -> Result<(), MigrationError> {
    let mut current = PathBuf::new();
    let mut missing_tail = false;
    for component in path.components() {
        match component {
            Component::Prefix(prefix) => current.push(prefix.as_os_str()),
            Component::RootDir => current.push(component.as_os_str()),
            Component::CurDir => {}
            Component::ParentDir => {
                if missing_tail {
                    return Err(MigrationError::UnsafePath);
                }
                current.pop();
            }
            Component::Normal(part) => {
                current.push(part);
                if missing_tail {
                    continue;
                }
                match fs::symlink_metadata(&current) {
                    Ok(metadata) if metadata.file_type().is_symlink() => {
                        return Err(MigrationError::UnsafePath);
                    }
                    Ok(_) => {}
                    Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
                        missing_tail = true;
                    }
                    Err(error) => return Err(error.into()),
                }
            }
        }
    }
    Ok(())
}

fn create_destination(path: &Path) -> Result<(), MigrationError> {
    match fs::symlink_metadata(path) {
        Ok(metadata) if metadata.file_type().is_symlink() || !metadata.is_dir() => {
            return Err(MigrationError::UnsafePath);
        }
        Ok(_) => {}
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            fs::create_dir_all(path)?;
            let metadata = fs::symlink_metadata(path)?;
            if metadata.file_type().is_symlink() || !metadata.is_dir() {
                return Err(MigrationError::UnsafePath);
            }
        }
        Err(error) => return Err(error.into()),
    }
    reject_symlink_components(path)
}

/// Offline seed for the first importer layout: it wrote a store database to `state.sqlite`.
/// The original file is never modified. Raw copying is only accepted when there are no WAL
/// sidecars, and the copy must pass the store's own integrity check before it is used.
fn seed_from_legacy_database(legacy: &Path, target: &Path) -> Result<(), MigrationError> {
    if target.exists() {
        return Err(MigrationError::DestinationConflict);
    }
    let metadata = fs::symlink_metadata(legacy)?;
    if metadata.file_type().is_symlink() || !metadata.is_file() {
        return Err(MigrationError::UnsafePath);
    }
    for suffix in ["-wal", "-shm", "-journal"] {
        let mut sidecar = legacy.as_os_str().to_os_string();
        sidecar.push(suffix);
        match fs::symlink_metadata(PathBuf::from(sidecar)) {
            Ok(_) => return Err(MigrationError::LegacyDatabaseBusy),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
            Err(error) => return Err(error.into()),
        }
    }
    if let Err(error) = fs::copy(legacy, target) {
        remove_database_files(target);
        return Err(error.into());
    }
    let verified = (|| -> Result<(), MigrationError> {
        let store = SqliteStore::open(target)?;
        if !store.integrity_check()? {
            return Err(MigrationError::DestinationUnhealthy);
        }
        Ok(())
    })();
    if let Err(error) = verified {
        remove_database_files(target);
        return Err(error);
    }
    Ok(())
}

fn load_journal(path: &Path) -> Result<Journal, MigrationError> {
    match fs::symlink_metadata(path) {
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(Journal::default()),
        Err(error) => Err(error.into()),
        Ok(metadata) if metadata.file_type().is_symlink() => Err(MigrationError::UnsafePath),
        Ok(metadata) if !metadata.is_file() => Err(MigrationError::JournalCorrupt),
        Ok(_) => {
            let bytes = fs::read(path)?;
            serde_json::from_slice(&bytes).map_err(|_| MigrationError::JournalCorrupt)
        }
    }
}

fn write_journal(path: &Path, journal: &Journal) -> Result<(), MigrationError> {
    let parent = path.parent().ok_or(MigrationError::UnsafePath)?;
    reject_symlink_components(parent)?;
    let bytes = serde_json::to_vec_pretty(journal).map_err(MigrationError::JournalWrite)?;
    let temporary = parent.join(format!(".{JOURNAL_FILE_NAME}.tmp.{}", Uuid::new_v4()));
    let result = (|| -> Result<(), MigrationError> {
        let mut file = OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&temporary)?;
        file.write_all(&bytes)?;
        file.sync_all()?;
        drop(file);
        fs::rename(&temporary, path)?;
        Ok(())
    })();
    if result.is_err() {
        let _ = fs::remove_file(&temporary);
    }
    result
}

struct ImportLock {
    path: PathBuf,
    token: String,
}

impl Drop for ImportLock {
    fn drop(&mut self) {
        if fs::read_to_string(&self.path).is_ok_and(|contents| contents == self.token) {
            let _ = fs::remove_file(&self.path);
        }
    }
}

fn acquire_import_lock(destination: &Path) -> Result<ImportLock, MigrationError> {
    let path = destination.join(LOCK_FILE_NAME);
    let token = format!(
        "paseo-migrate pid={} id={}",
        std::process::id(),
        Uuid::new_v4()
    );
    let mut file = match OpenOptions::new().write(true).create_new(true).open(&path) {
        Ok(file) => file,
        Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => {
            return Err(MigrationError::DestinationLocked);
        }
        Err(error) => return Err(error.into()),
    };
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        if let Err(error) = file.set_permissions(fs::Permissions::from_mode(0o600)) {
            let _ = fs::remove_file(&path);
            return Err(error.into());
        }
    }
    let written = (|| -> Result<(), MigrationError> {
        file.write_all(token.as_bytes())?;
        file.sync_all()?;
        Ok(())
    })();
    drop(file);
    if let Err(error) = written {
        let _ = fs::remove_file(&path);
        return Err(error);
    }
    Ok(ImportLock { path, token })
}

fn cleanup_failed_import(
    created_database: bool,
    db_path: &Path,
    journal_path: &Path,
    previous_journal: &Journal,
) {
    if created_database {
        remove_database_files(db_path);
        if previous_journal.entries.is_empty() && previous_journal.pending.is_none() {
            let _ = fs::remove_file(journal_path);
        } else {
            let _ = write_journal(journal_path, previous_journal);
        }
    }
}

fn remove_database_files(path: &Path) {
    for suffix in ["", "-wal", "-shm", "-journal"] {
        let mut candidate = path.as_os_str().to_os_string();
        candidate.push(suffix);
        let _ = fs::remove_file(PathBuf::from(candidate));
    }
}

fn prepare_workspaces(records: &[Value], issues: &mut Vec<Issue>) -> Vec<Workspace> {
    let mut converted = Vec::new();
    for record in records {
        match convert_workspace(record) {
            Ok(workspace) => {
                if converted
                    .iter()
                    .any(|existing: &Workspace| existing.id == workspace.id)
                {
                    let equivalent = converted
                        .iter()
                        .any(|existing: &Workspace| workspace_equivalent(existing, &workspace));
                    if !equivalent {
                        issues.push(Issue {
                            path: "projects/workspaces.json".into(),
                            category: "duplicate_workspace_id".into(),
                            message: "a conflicting duplicate record was ignored; source was left unchanged".into(),
                        });
                    }
                    continue;
                }
                converted.push(workspace);
            }
            Err(category) => issues.push(Issue {
                path: "projects/workspaces.json".into(),
                category,
                message: "record could not be safely represented; source was left unchanged".into(),
            }),
        }
    }
    converted
}

fn validate_conflicts(
    candidates: &[Workspace],
    existing: &[Workspace],
) -> Result<(), MigrationError> {
    for candidate in candidates {
        for current in existing {
            if current.id == candidate.id {
                if !workspace_equivalent(current, candidate) {
                    return Err(MigrationError::DestinationConflict);
                }
            } else if current.path == candidate.path
                || legacy_id(current).is_some_and(|value| Some(value) == legacy_id(candidate))
            {
                return Err(MigrationError::DestinationConflict);
            }
        }
    }
    Ok(())
}

fn import_is_complete(
    candidates: &[Workspace],
    existing: &[Workspace],
    existing_metadata: Option<&Value>,
    expected_metadata: &Value,
) -> bool {
    candidates.iter().all(|candidate| {
        existing
            .iter()
            .any(|current| workspace_equivalent(current, candidate))
    }) && existing_metadata == Some(expected_metadata)
}

fn workspace_equivalent(left: &Workspace, right: &Workspace) -> bool {
    left.id == right.id
        && left.name == right.name
        && left.path == right.path
        && left.kind == right.kind
        && left.lifecycle == right.lifecycle
        && left.metadata == right.metadata
}
fn legacy_id(workspace: &Workspace) -> Option<&str> {
    workspace.metadata.get("legacy_id").and_then(Value::as_str)
}

struct DestinationState {
    store: Option<SqliteStore>,
    existing: Vec<Workspace>,
    metadata: Option<Value>,
}

fn open_destination_state(db_path: &Path) -> Result<DestinationState, MigrationError> {
    let store = if db_path.exists() {
        Some(SqliteStore::open(db_path)?)
    } else {
        None
    };
    if let Some(store) = &store
        && !store.integrity_check()?
    {
        return Err(MigrationError::DestinationUnhealthy);
    }
    let existing = match &store {
        Some(store) => store.list_workspaces()?,
        None => Vec::new(),
    };
    let metadata = match &store {
        Some(store) => store.config_metadata(PROJECTS_METADATA_KEY)?,
        None => None,
    };
    Ok(DestinationState {
        store,
        existing,
        metadata,
    })
}

fn apply_to_store(
    store: &mut Option<SqliteStore>,
    db_path: &Path,
    converted: &[Workspace],
    safe_projects: &Value,
    existing: &[Workspace],
    existing_metadata: Option<&Value>,
) -> Result<usize, MigrationError> {
    if store.is_none() {
        *store = Some(SqliteStore::open(db_path)?);
    }
    let store = store.as_mut().ok_or(MigrationError::IncompleteImport)?;
    if !store.integrity_check()? {
        return Err(MigrationError::DestinationUnhealthy);
    }
    let mut imported = 0;
    for workspace in converted {
        if existing
            .iter()
            .any(|current| workspace_equivalent(current, workspace))
        {
            continue;
        }
        store.save_workspace(workspace)?;
        imported += 1;
    }
    if existing_metadata != Some(safe_projects) {
        store.set_config_metadata(PROJECTS_METADATA_KEY, safe_projects)?;
    }
    if !store.integrity_check()? {
        return Err(MigrationError::DestinationUnhealthy);
    }
    let written = store.list_workspaces()?;
    if converted.iter().any(|candidate| {
        !written
            .iter()
            .any(|current| workspace_equivalent(current, candidate))
    }) {
        return Err(MigrationError::IncompleteImport);
    }
    if store.config_metadata(PROJECTS_METADATA_KEY)?.as_ref() != Some(safe_projects) {
        return Err(MigrationError::IncompleteImport);
    }
    Ok(imported)
}

fn ensure_source_unchanged(
    files: &[(&str, PathBuf)],
    expected: &str,
) -> Result<(), MigrationError> {
    if fingerprint(files) == expected {
        Ok(())
    } else {
        Err(MigrationError::SourceChanged)
    }
}

fn read_records(path: &Path, label: &str, issues: &mut Vec<Issue>) -> Vec<Value> {
    let bytes = match fs::symlink_metadata(path) {
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Vec::new(),
        Err(_) => {
            issues.push(Issue {
                path: label.into(),
                category: "unreadable".into(),
                message: "file metadata could not be read; contents omitted".into(),
            });
            return Vec::new();
        }
        Ok(metadata) if metadata.file_type().is_symlink() => {
            issues.push(Issue {
                path: label.into(),
                category: "symlink_skipped".into(),
                message: "symbolic links are not read".into(),
            });
            return Vec::new();
        }
        Ok(metadata) if !metadata.is_file() => {
            issues.push(Issue {
                path: label.into(),
                category: "unexpected_file_type".into(),
                message: "expected a regular file; contents omitted".into(),
            });
            return Vec::new();
        }
        Ok(_) => {
            let Ok(bytes) = fs::read(path) else {
                issues.push(Issue {
                    path: label.into(),
                    category: "unreadable".into(),
                    message: "file could not be read; contents omitted".into(),
                });
                return Vec::new();
            };
            bytes
        }
    };
    let result = serde_json::from_slice::<Value>(&bytes).ok();
    match result {
        Some(Value::Array(rows)) => rows,
        Some(Value::Object(mut object)) => object
            .remove("projects")
            .or_else(|| object.remove("workspaces"))
            .and_then(|value| value.as_array().cloned())
            .unwrap_or_default(),
        Some(_) => {
            issues.push(Issue {
                path: label.into(),
                category: "unexpected_shape".into(),
                message: "expected a JSON array or records object".into(),
            });
            Vec::new()
        }
        None => {
            issues.push(Issue {
                path: label.into(),
                category: "unreadable_or_invalid_json".into(),
                message: "file could not be parsed; contents omitted".into(),
            });
            Vec::new()
        }
    }
}
fn safe_project_metadata(record: &Value) -> Value {
    let Some(object) = record.as_object() else {
        return Value::Null;
    };
    let mut safe = serde_json::Map::new();
    for key in [
        "id",
        "name",
        "path",
        "cwd",
        "kind",
        "branch",
        "createdAt",
        "updatedAt",
    ] {
        if let Some(value) = object.get(key) {
            safe.insert(key.into(), value.clone());
        }
    }
    Value::Object(safe)
}

fn convert_workspace(record: &Value) -> Result<Workspace, String> {
    let object = record.as_object().ok_or("not_an_object")?;
    let legacy_id = object
        .get("id")
        .and_then(Value::as_str)
        .ok_or("missing_id")?;
    let id = Uuid::parse_str(legacy_id).map_or_else(
        |_| WorkspaceId::from_uuid(Uuid::from_bytes(stable_id(legacy_id.as_bytes()))),
        WorkspaceId::from_uuid,
    );
    let path = object
        .get("cwd")
        .or_else(|| object.get("path"))
        .and_then(Value::as_str)
        .filter(|p| !p.is_empty())
        .ok_or("missing_path")?;
    let name = object
        .get("name")
        .and_then(Value::as_str)
        .filter(|n| !n.is_empty())
        .unwrap_or_else(|| {
            Path::new(path)
                .file_name()
                .and_then(|s| s.to_str())
                .unwrap_or("Imported workspace")
        });
    let kind = match object
        .get("kind")
        .and_then(Value::as_str)
        .unwrap_or("directory")
    {
        "directory" => WorkspaceKind::Directory,
        "local_checkout" => WorkspaceKind::LocalCheckout,
        "worktree" => WorkspaceKind::Worktree,
        _ => return Err("unknown_workspace_kind".into()),
    };
    let lifecycle = if object.get("archivedAt").is_some_and(|v| !v.is_null())
        || object.get("archived").and_then(Value::as_bool) == Some(true)
    {
        WorkspaceLifecycle::Archived
    } else {
        WorkspaceLifecycle::Active
    };
    let now = Utc::now();
    let created_at = timestamp(object.get("createdAt")).unwrap_or(now);
    let updated_at = timestamp(object.get("updatedAt")).unwrap_or(created_at);
    let mut metadata = serde_json::Map::new();
    for key in [
        "projectId",
        "branch",
        "worktreeRoot",
        "baseBranch",
        "isPaseoOwnedWorktree",
        "mainRepoRoot",
        "pinnedAt",
        "background",
        "archivedAt",
        "archiveReason",
    ] {
        if let Some(value) = object.get(key) {
            metadata.insert(key.into(), value.clone());
        }
    }
    metadata.insert("legacy_id".into(), Value::String(legacy_id.into()));
    Ok(Workspace {
        id,
        name: name.into(),
        path: path.into(),
        kind,
        lifecycle,
        metadata: Value::Object(metadata),
        created_at,
        updated_at,
    })
}
fn timestamp(value: Option<&Value>) -> Option<DateTime<Utc>> {
    let text = value?.as_str()?;
    DateTime::parse_from_rfc3339(text)
        .ok()
        .map(|time| time.with_timezone(&Utc))
}
fn stable_id(input: &[u8]) -> [u8; 16] {
    let digest = Sha256::digest(input);
    let mut output = [0; 16];
    output.copy_from_slice(&digest[..16]);
    output
}
fn fingerprint(files: &[(&str, PathBuf)]) -> String {
    let mut hash = Sha256::new();
    for (name, path) in files {
        hash.update(name.as_bytes());
        match fs::symlink_metadata(path) {
            Ok(metadata) if metadata.file_type().is_symlink() => hash.update([2]),
            Ok(metadata) if metadata.is_file() => match fs::read(path) {
                Ok(bytes) => {
                    hash.update([1]);
                    hash.update(bytes);
                }
                Err(_) => hash.update([3]),
            },
            Ok(_) => hash.update([4]),
            Err(_) => hash.update([0]),
        }
    }
    format!("sha256:{:x}", hash.finalize())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn write_source(source: &Path, workspaces: &str, projects: &str) {
        fs::create_dir_all(source.join("projects")).unwrap();
        fs::write(source.join("projects/workspaces.json"), workspaces).unwrap();
        fs::write(source.join("projects/projects.json"), projects).unwrap();
    }

    fn source_fingerprint(source: &Path) -> String {
        fingerprint(&[
            (
                "projects/projects.json",
                source.join("projects/projects.json"),
            ),
            (
                "projects/workspaces.json",
                source.join("projects/workspaces.json"),
            ),
        ])
    }

    fn records(source: &Path) -> (Vec<u8>, Vec<u8>) {
        (
            fs::read(source.join("projects/projects.json")).unwrap(),
            fs::read(source.join("projects/workspaces.json")).unwrap(),
        )
    }

    #[test]
    fn dry_run_is_read_only_and_apply_is_idempotent() {
        let temp = tempfile::tempdir().unwrap();
        let source = temp.path().join("legacy");
        let dest = temp.path().join("new");
        write_source(
            &source,
            r#"[{"id":"old-id","name":"repo","cwd":"/repo","kind":"worktree","branch":"main","archived":true}]"#,
            "[]",
        );
        let original = records(&source);
        let dry = migrate(&source, &dest, false).unwrap();
        assert!(dry.dry_run);
        assert!(!dest.exists());
        assert_eq!(records(&source), original);
        let applied = migrate(&source, &dest, true).unwrap();
        assert_eq!(applied.imported_workspaces, 1);
        assert_eq!(records(&source), original);
        let again = migrate(&source, &dest, true).unwrap();
        assert!(again.already_imported);
        assert_eq!(again.imported_workspaces, 0);
        assert_eq!(records(&source), original);
        assert!(!dest.join(LEGACY_DATABASE_FILE_NAME).exists());
        let db = SqliteStore::open(dest.join(DATABASE_FILE_NAME)).unwrap();
        assert_eq!(
            db.list_workspaces().unwrap()[0].lifecycle,
            WorkspaceLifecycle::Archived
        );
    }

    #[test]
    fn preexisting_destination_is_backed_up_and_backup_is_journaled() {
        let temp = tempfile::tempdir().unwrap();
        let source = temp.path().join("legacy");
        let dest = temp.path().join("new");
        write_source(
            &source,
            r#"[{"id":"imported","name":"imported","cwd":"/imported","kind":"directory"}]"#,
            "[]",
        );
        fs::create_dir_all(&dest).unwrap();
        let existing = convert_workspace(
            &json!({"id":"existing","name":"existing","cwd":"/existing","kind":"directory"}),
        )
        .unwrap();
        create_database(&dest.join(DATABASE_FILE_NAME), &existing);

        let report = migrate(&source, &dest, true).unwrap();
        let backup_path = report.destination_backup.expect("backup is reported");
        assert!(backup_path.is_file());
        let backup = SqliteStore::open(&backup_path).unwrap();
        let backup_workspaces = backup.list_workspaces().unwrap();
        assert_eq!(backup_workspaces.len(), 1);
        assert_eq!(backup_workspaces[0].id, existing.id);

        let journal = load_journal(&dest.join(JOURNAL_FILE_NAME)).unwrap();
        assert!(journal.pending.is_none());
        assert_eq!(journal.entries.len(), 1);
        assert_eq!(
            journal.entries[0].destination_backup.as_deref(),
            backup_path.file_name().and_then(|name| name.to_str())
        );
        let rerun = migrate(&source, &dest, true).unwrap();
        assert!(rerun.already_imported);
        assert_eq!(rerun.destination_backup, Some(backup_path));
    }

    #[test]
    fn orphan_backup_is_refreshed_before_rollback() {
        let temp = tempfile::tempdir().unwrap();
        let source = temp.path().join("legacy");
        let dest = temp.path().join("new");
        let imported = convert_workspace(
            &json!({"id":"collision","name":"source-collision","cwd":"/source","kind":"directory"}),
        )
        .unwrap();
        write_source(
            &source,
            r#"[{"id":"collision","name":"source-collision","cwd":"/source","kind":"directory"}]"#,
            "[]",
        );
        let source_before = records(&source);
        fs::create_dir_all(&dest).unwrap();
        let existing = convert_workspace(
            &json!({"id":"collision","name":"destination-collision","cwd":"/destination","kind":"directory"}),
        )
        .unwrap();
        let added_later = convert_workspace(
            &json!({"id":"added-later","name":"added-later","cwd":"/later","kind":"directory"}),
        )
        .unwrap();
        create_database(&dest.join(DATABASE_FILE_NAME), &existing);

        let fingerprint = source_fingerprint(&source);
        let orphan = create_destination_backup(
            &dest.join(DATABASE_FILE_NAME),
            &fingerprint,
            &Journal::default(),
        )
        .unwrap();
        {
            let mut db = SqliteStore::open(dest.join(DATABASE_FILE_NAME)).unwrap();
            db.save_workspace(&added_later).unwrap();
        }

        assert!(matches!(
            migrate(&source, &dest, true),
            Err(MigrationError::DestinationConflict)
        ));
        let restored = SqliteStore::open(dest.join(DATABASE_FILE_NAME)).unwrap();
        let restored_workspaces = restored.list_workspaces().unwrap();
        assert_eq!(restored_workspaces.len(), 2);
        assert!(
            restored_workspaces
                .iter()
                .any(|workspace| workspace_equivalent(workspace, &existing))
        );
        assert!(
            restored_workspaces
                .iter()
                .any(|workspace| workspace_equivalent(workspace, &added_later))
        );
        assert!(
            !restored_workspaces
                .iter()
                .any(|workspace| workspace_equivalent(workspace, &imported))
        );
        assert!(!orphan.path.exists());
        assert_eq!(records(&source), source_before);
    }

    #[test]
    fn online_backup_includes_wal_and_can_restore_the_database() {
        let temp = tempfile::tempdir().unwrap();
        let database = temp.path().join(DATABASE_FILE_NAME);
        let source = Connection::open(&database).unwrap();
        source.pragma_update(None, "journal_mode", "WAL").unwrap();
        source
            .execute_batch("CREATE TABLE sample(value TEXT); INSERT INTO sample VALUES('before');")
            .unwrap();

        let fingerprint = format!("sha256:{}", "a".repeat(64));
        let backup =
            create_destination_backup(&database, &fingerprint, &Journal::default()).unwrap();
        assert!(backup.created);
        source
            .execute("INSERT INTO sample VALUES('after')", [])
            .unwrap();
        drop(source);

        restore_destination_backup(&backup.path, &database).unwrap();
        let restored =
            Connection::open_with_flags(&database, OpenFlags::SQLITE_OPEN_READ_ONLY).unwrap();
        let values = restored
            .prepare("SELECT value FROM sample ORDER BY rowid")
            .unwrap()
            .query_map([], |row| row.get::<_, String>(0))
            .unwrap()
            .collect::<Result<Vec<_>, _>>()
            .unwrap();
        assert_eq!(values, vec!["before"]);
    }

    #[test]
    fn malformed_records_are_redacted() {
        let temp = tempfile::tempdir().unwrap();
        let source = temp.path().join("src");
        fs::create_dir_all(source.join("projects")).unwrap();
        fs::write(source.join("projects/workspaces.json"), "SECRET malformed").unwrap();
        let report = migrate(&source, &temp.path().join("dest"), false).unwrap();
        assert!(
            report
                .issues
                .iter()
                .all(|issue| !issue.message.contains("SECRET"))
        );
    }

    #[test]
    fn rejects_overlapping_source_and_destination_before_writing() {
        let temp = tempfile::tempdir().unwrap();
        let source = temp.path().join("legacy");
        write_source(&source, "[]", "[]");
        let inside = source.join("nested/rust-home");
        assert!(matches!(
            migrate(&source, &inside, true),
            Err(MigrationError::PathOverlap)
        ));
        assert!(!inside.exists());
        assert!(matches!(
            migrate(&source, &source, true),
            Err(MigrationError::PathOverlap)
        ));
        assert!(matches!(
            migrate(&source, temp.path(), true),
            Err(MigrationError::PathOverlap)
        ));
        assert!(!temp.path().join(DATABASE_FILE_NAME).exists());
        assert!(!temp.path().join(JOURNAL_FILE_NAME).exists());
    }

    #[cfg(unix)]
    #[test]
    fn rejects_source_symlink_ancestors_and_destination_aliases() {
        use std::os::unix::fs::symlink;

        let temp = tempfile::tempdir().unwrap();
        let real = temp.path().join("real");
        let source = real.join("legacy");
        write_source(&source, "[]", "[]");
        let alias = temp.path().join("alias");
        symlink(&real, &alias).unwrap();
        assert!(matches!(
            migrate(&alias.join("legacy"), &temp.path().join("dest"), false),
            Err(MigrationError::UnsafePath)
        ));
        assert!(matches!(
            migrate(
                &temp.path().join("alias/../real/legacy"),
                &temp.path().join("dest"),
                false
            ),
            Err(MigrationError::UnsafePath)
        ));
        assert!(matches!(
            migrate(&source, &temp.path().join("missing/../alias/legacy"), false),
            Err(MigrationError::UnsafePath)
        ));
        assert!(!temp.path().join("dest").exists());

        let destination = temp.path().join("new-home");
        fs::create_dir_all(&destination).unwrap();
        let target = source.join("projects/workspaces.json");
        let before = fs::read(&target).unwrap();
        symlink(&target, destination.join(DATABASE_FILE_NAME)).unwrap();
        assert!(matches!(
            migrate(&source, &destination, true),
            Err(MigrationError::UnsafePath)
        ));
        assert_eq!(fs::read(&target).unwrap(), before);

        let journal_destination = temp.path().join("journal-home");
        fs::create_dir_all(&journal_destination).unwrap();
        symlink(&target, journal_destination.join(JOURNAL_FILE_NAME)).unwrap();
        assert!(matches!(
            migrate(&source, &journal_destination, false),
            Err(MigrationError::UnsafePath)
        ));
        assert_eq!(fs::read(&target).unwrap(), before);
    }

    #[cfg(unix)]
    #[test]
    fn hard_linked_destination_database_is_rejected_before_open() {
        let temp = tempfile::tempdir().unwrap();
        let source = temp.path().join("legacy");
        let dest = temp.path().join("new");
        write_source(&source, "[]", "[]");
        fs::create_dir_all(&dest).unwrap();
        let source_file = source.join("projects/workspaces.json");
        let before = fs::read(&source_file).unwrap();
        fs::hard_link(&source_file, dest.join(DATABASE_FILE_NAME)).unwrap();
        assert!(matches!(
            migrate(&source, &dest, true),
            Err(MigrationError::UnsafePath)
        ));
        assert_eq!(fs::read(&source_file).unwrap(), before);
    }

    #[test]
    fn apply_lock_is_exclusive_and_uses_the_daemon_primitive() {
        let temp = tempfile::tempdir().unwrap();
        let dest = temp.path().join("new");
        fs::create_dir_all(&dest).unwrap();
        let lock_path = dest.join(LOCK_FILE_NAME);
        let lock = acquire_import_lock(&dest).unwrap();
        assert!(lock_path.exists());
        assert!(matches!(
            acquire_import_lock(&dest),
            Err(MigrationError::DestinationLocked)
        ));
        assert!(
            fs::OpenOptions::new()
                .write(true)
                .create_new(true)
                .open(&lock_path)
                .is_err()
        );
        drop(lock);
        assert!(!lock_path.exists());
    }

    #[test]
    fn preexisting_lock_blocks_apply_and_is_not_removed() {
        let temp = tempfile::tempdir().unwrap();
        let source = temp.path().join("legacy");
        let dest = temp.path().join("new");
        write_source(
            &source,
            r#"[{"id":"one","name":"one","cwd":"/one","kind":"directory"}]"#,
            "[]",
        );
        fs::create_dir_all(&dest).unwrap();
        let lock_path = dest.join(LOCK_FILE_NAME);
        fs::write(&lock_path, "stale daemon pid").unwrap();
        let before = fs::read(&lock_path).unwrap();
        let source_before = records(&source);
        assert!(matches!(
            migrate(&source, &dest, true),
            Err(MigrationError::DestinationLocked)
        ));
        assert_eq!(fs::read(&lock_path).unwrap(), before);
        assert_eq!(records(&source), source_before);
        assert!(!dest.join(DATABASE_FILE_NAME).exists());
        assert!(!dest.join(JOURNAL_FILE_NAME).exists());
    }

    #[test]
    fn owned_lock_is_removed_after_success_and_conflict() {
        let temp = tempfile::tempdir().unwrap();
        let success_source = temp.path().join("success-source");
        let success_dest = temp.path().join("success-dest");
        write_source(
            &success_source,
            r#"[{"id":"one","name":"one","cwd":"/one","kind":"directory"}]"#,
            "[]",
        );
        migrate(&success_source, &success_dest, true).unwrap();
        assert!(!success_dest.join(LOCK_FILE_NAME).exists());

        let conflict_source = temp.path().join("conflict-source");
        let conflict_dest = temp.path().join("conflict-dest");
        write_source(
            &conflict_source,
            r#"[{"id":"same-id","name":"new-name","cwd":"/repo","kind":"directory"}]"#,
            "[]",
        );
        fs::create_dir_all(&conflict_dest).unwrap();
        let existing = convert_workspace(
            &json!({"id":"same-id","name":"old-name","cwd":"/repo","kind":"directory"}),
        )
        .unwrap();
        create_database(&conflict_dest.join(DATABASE_FILE_NAME), &existing);
        assert!(matches!(
            migrate(&conflict_source, &conflict_dest, true),
            Err(MigrationError::DestinationConflict)
        ));
        assert!(!conflict_dest.join(LOCK_FILE_NAME).exists());
    }

    #[test]
    fn lock_drop_does_not_remove_a_replaced_lock() {
        let temp = tempfile::tempdir().unwrap();
        let dest = temp.path().join("new");
        fs::create_dir_all(&dest).unwrap();
        let lock_path = dest.join(LOCK_FILE_NAME);
        let lock = acquire_import_lock(&dest).unwrap();
        fs::write(&lock_path, "replacement owner").unwrap();
        drop(lock);
        assert_eq!(fs::read_to_string(&lock_path).unwrap(), "replacement owner");
    }

    #[cfg(unix)]
    #[test]
    fn destination_wal_symlink_is_rejected_before_open() {
        use std::os::unix::fs::symlink;

        let temp = tempfile::tempdir().unwrap();
        let source = temp.path().join("legacy");
        let dest = temp.path().join("new");
        write_source(&source, "[]", "[]");
        fs::create_dir_all(&dest).unwrap();
        let source_file = source.join("projects/workspaces.json");
        let before = fs::read(&source_file).unwrap();
        symlink(&source_file, dest.join("state.sqlite3-wal")).unwrap();
        assert!(matches!(
            migrate(&source, &dest, true),
            Err(MigrationError::UnsafePath)
        ));
        assert_eq!(fs::read(&source_file).unwrap(), before);
        assert!(!dest.join(DATABASE_FILE_NAME).exists());
    }

    #[cfg(unix)]
    #[test]
    fn final_source_symlink_is_skipped_without_following_it() {
        use std::os::unix::fs::symlink;

        let temp = tempfile::tempdir().unwrap();
        let source = temp.path().join("legacy");
        let target = temp.path().join("outside.json");
        fs::create_dir_all(source.join("projects")).unwrap();
        fs::write(
            &target,
            r#"[{"id":"outside","name":"outside","cwd":"/outside"}]"#,
        )
        .unwrap();
        fs::write(source.join("projects/projects.json"), "[]").unwrap();
        symlink(&target, source.join("projects/workspaces.json")).unwrap();
        let report = migrate(&source, &temp.path().join("dest"), false).unwrap();
        assert_eq!(report.workspace_records, 0);
        assert!(
            report
                .issues
                .iter()
                .any(|issue| issue.category == "symlink_skipped")
        );
    }

    #[test]
    fn conflicting_destination_record_blocks_record_writes() {
        let temp = tempfile::tempdir().unwrap();
        let source = temp.path().join("legacy");
        let dest = temp.path().join("new");
        write_source(
            &source,
            r#"[{"id":"same-id","name":"new-name","cwd":"/repo","kind":"directory"}]"#,
            "[]",
        );
        fs::create_dir_all(&dest).unwrap();
        let existing = convert_workspace(
            &json!({"id":"same-id","name":"old-name","cwd":"/repo","kind":"directory"}),
        )
        .unwrap();
        {
            let mut db = SqliteStore::open(dest.join(DATABASE_FILE_NAME)).unwrap();
            db.save_workspace(&existing).unwrap();
            db.set_config_metadata(PROJECTS_METADATA_KEY, &json!([]))
                .unwrap();
        }
        assert!(matches!(
            migrate(&source, &dest, true),
            Err(MigrationError::DestinationConflict)
        ));
        assert!(!dest.join(JOURNAL_FILE_NAME).exists());
        let db = SqliteStore::open(dest.join(DATABASE_FILE_NAME)).unwrap();
        let workspaces = db.list_workspaces().unwrap();
        assert_eq!(workspaces.len(), 1);
        assert_eq!(workspaces[0].name, "old-name");
    }

    #[test]
    fn conflicting_projects_metadata_blocks_workspace_writes() {
        let temp = tempfile::tempdir().unwrap();
        let source = temp.path().join("legacy");
        let dest = temp.path().join("new");
        write_source(
            &source,
            r#"[{"id":"one","name":"one","cwd":"/one","kind":"directory"}]"#,
            r#"[{"id":"project","name":"project"}]"#,
        );
        fs::create_dir_all(&dest).unwrap();
        {
            let mut db = SqliteStore::open(dest.join(DATABASE_FILE_NAME)).unwrap();
            db.set_config_metadata(PROJECTS_METADATA_KEY, &json!({"different":true}))
                .unwrap();
        }
        assert!(matches!(
            migrate(&source, &dest, true),
            Err(MigrationError::DestinationConflict)
        ));
        assert!(!dest.join(JOURNAL_FILE_NAME).exists());
        let db = SqliteStore::open(dest.join(DATABASE_FILE_NAME)).unwrap();
        assert_eq!(db.list_workspaces().unwrap().len(), 0);
    }

    #[test]
    fn corrupt_journal_is_rejected_without_leaking_contents_or_writing() {
        let temp = tempfile::tempdir().unwrap();
        let source = temp.path().join("legacy");
        let dest = temp.path().join("new");
        write_source(&source, "[]", "[]");
        fs::create_dir_all(&dest).unwrap();
        fs::write(dest.join(JOURNAL_FILE_NAME), "SECRET not json").unwrap();
        for apply in [false, true] {
            let error = migrate(&source, &dest, apply).unwrap_err();
            assert!(matches!(error, MigrationError::JournalCorrupt));
            assert!(!format!("{error}").contains("SECRET"));
        }
        assert!(!dest.join(DATABASE_FILE_NAME).exists());
        assert!(!dest.join(LEGACY_DATABASE_FILE_NAME).exists());
    }

    #[test]
    fn pending_journal_resumes_a_partial_import() {
        let temp = tempfile::tempdir().unwrap();
        let source = temp.path().join("legacy");
        let dest = temp.path().join("new");
        write_source(
            &source,
            r#"[{"id":"one","name":"one","cwd":"/one","kind":"directory"},{"id":"two","name":"two","cwd":"/two","kind":"directory"}]"#,
            "[]",
        );
        fs::create_dir_all(&dest).unwrap();
        let first =
            convert_workspace(&json!({"id":"one","name":"one","cwd":"/one","kind":"directory"}))
                .unwrap();
        let second =
            convert_workspace(&json!({"id":"two","name":"two","cwd":"/two","kind":"directory"}))
                .unwrap();
        {
            let mut db = SqliteStore::open(dest.join(DATABASE_FILE_NAME)).unwrap();
            db.save_workspace(&first).unwrap();
        }
        let fingerprint = source_fingerprint(&source);
        let backup = create_destination_backup(
            &dest.join(DATABASE_FILE_NAME),
            &fingerprint,
            &Journal::default(),
        )
        .unwrap();
        let backup_name = backup
            .path
            .file_name()
            .unwrap()
            .to_string_lossy()
            .into_owned();
        let journal = Journal {
            entries: Vec::new(),
            pending: Some(PendingImport {
                fingerprint,
                database: DATABASE_FILE_NAME.into(),
                workspace_ids: vec![first.id.to_string(), second.id.to_string()],
                destination_backup: Some(backup_name),
            }),
        };
        write_journal(&dest.join(JOURNAL_FILE_NAME), &journal).unwrap();
        let report = migrate(&source, &dest, true).unwrap();
        assert_eq!(report.imported_workspaces, 1);
        assert_eq!(report.destination_backup, Some(backup.path));
        let db = SqliteStore::open(dest.join(DATABASE_FILE_NAME)).unwrap();
        assert_eq!(db.list_workspaces().unwrap().len(), 2);
        let journal = load_journal(&dest.join(JOURNAL_FILE_NAME)).unwrap();
        assert!(journal.pending.is_none());
        assert_eq!(journal.entries.len(), 1);
        assert_eq!(journal.entries[0].workspace_ids.len(), 2);
    }

    fn create_database(path: &Path, workspace: &Workspace) {
        let mut db = SqliteStore::open(path).unwrap();
        db.save_workspace(workspace).unwrap();
        db.set_config_metadata(PROJECTS_METADATA_KEY, &json!([]))
            .unwrap();
    }

    fn remove_wal_sidecars(path: &Path) {
        for suffix in ["-wal", "-shm"] {
            let mut sidecar = path.as_os_str().to_os_string();
            sidecar.push(suffix);
            let _ = fs::remove_file(PathBuf::from(sidecar));
        }
    }

    #[test]
    fn legacy_layout_is_seeded_offline_without_certifying_a_different_database() {
        let temp = tempfile::tempdir().unwrap();
        let source = temp.path().join("legacy");
        let dest = temp.path().join("new");
        write_source(
            &source,
            r#"[{"id":"one","name":"one","cwd":"/one","kind":"directory"}]"#,
            "[]",
        );
        fs::create_dir_all(&dest).unwrap();
        let legacy_db = dest.join(LEGACY_DATABASE_FILE_NAME);
        let legacy_only = convert_workspace(
            &json!({"id":"legacy-only","name":"legacy-only","cwd":"/legacy","kind":"directory"}),
        )
        .unwrap();
        create_database(&legacy_db, &legacy_only);
        remove_wal_sidecars(&legacy_db);
        let legacy_bytes = fs::read(&legacy_db).unwrap();
        let journal = format!(
            r#"{{"entries":[{{"fingerprint":"{}","imported_at":"2024-01-01T00:00:00Z"}}]}}"#,
            source_fingerprint(&source)
        );
        fs::write(dest.join(JOURNAL_FILE_NAME), journal).unwrap();

        let dry = migrate(&source, &dest, false).unwrap();
        assert!(!dry.already_imported);
        assert!(
            dry.issues
                .iter()
                .any(|issue| issue.category == "legacy_database_pending")
        );
        assert!(!dest.join(DATABASE_FILE_NAME).exists());

        let report = migrate(&source, &dest, true).unwrap();
        assert!(!report.already_imported);
        assert_eq!(report.imported_workspaces, 1);
        assert_eq!(fs::read(&legacy_db).unwrap(), legacy_bytes);
        let db = SqliteStore::open(dest.join(DATABASE_FILE_NAME)).unwrap();
        let names = db
            .list_workspaces()
            .unwrap()
            .into_iter()
            .map(|workspace| workspace.name)
            .collect::<Vec<_>>();
        assert_eq!(names.len(), 2);
        assert!(names.contains(&"legacy-only".into()));
        assert!(names.contains(&"one".into()));
        let journal = load_journal(&dest.join(JOURNAL_FILE_NAME)).unwrap();
        assert_eq!(journal.entries.len(), 2);
        assert_eq!(journal.entries[1].database, DATABASE_FILE_NAME);
    }

    #[test]
    fn legacy_database_sidecars_refuse_offline_copy() {
        let temp = tempfile::tempdir().unwrap();
        let source = temp.path().join("legacy");
        let dest = temp.path().join("new");
        write_source(&source, "[]", "[]");
        fs::create_dir_all(&dest).unwrap();
        let legacy_db = dest.join(LEGACY_DATABASE_FILE_NAME);
        fs::write(&legacy_db, b"sentinel").unwrap();
        fs::write(dest.join("state.sqlite-wal"), b"uncheckpointed").unwrap();
        let legacy_bytes = fs::read(&legacy_db).unwrap();
        assert!(matches!(
            migrate(&source, &dest, true),
            Err(MigrationError::LegacyDatabaseBusy)
        ));
        assert_eq!(fs::read(&legacy_db).unwrap(), legacy_bytes);
        assert!(!dest.join(DATABASE_FILE_NAME).exists());
        assert!(!dest.join(JOURNAL_FILE_NAME).exists());
    }

    #[test]
    fn legacy_seed_conflict_rolls_back_the_new_target() {
        let temp = tempfile::tempdir().unwrap();
        let source = temp.path().join("legacy");
        let dest = temp.path().join("new");
        write_source(
            &source,
            r#"[{"id":"same-id","name":"new-name","cwd":"/repo","kind":"directory"}]"#,
            "[]",
        );
        fs::create_dir_all(&dest).unwrap();
        let legacy_db = dest.join(LEGACY_DATABASE_FILE_NAME);
        let legacy_only = convert_workspace(
            &json!({"id":"same-id","name":"old-name","cwd":"/repo","kind":"directory"}),
        )
        .unwrap();
        create_database(&legacy_db, &legacy_only);
        remove_wal_sidecars(&legacy_db);
        let legacy_bytes = fs::read(&legacy_db).unwrap();
        assert!(matches!(
            migrate(&source, &dest, true),
            Err(MigrationError::DestinationConflict)
        ));
        assert_eq!(fs::read(&legacy_db).unwrap(), legacy_bytes);
        assert!(!dest.join(DATABASE_FILE_NAME).exists());
        assert!(!dest.join(JOURNAL_FILE_NAME).exists());
    }

    #[test]
    fn existing_rust_database_is_never_replaced_by_legacy_database() {
        let temp = tempfile::tempdir().unwrap();
        let source = temp.path().join("legacy");
        let dest = temp.path().join("new");
        write_source(
            &source,
            r#"[{"id":"one","name":"one","cwd":"/one","kind":"directory"}]"#,
            "[]",
        );
        fs::create_dir_all(&dest).unwrap();
        let target_db = dest.join(DATABASE_FILE_NAME);
        let target_only = convert_workspace(
            &json!({"id":"target-only","name":"target-only","cwd":"/target","kind":"directory"}),
        )
        .unwrap();
        create_database(&target_db, &target_only);
        let legacy_db = dest.join(LEGACY_DATABASE_FILE_NAME);
        let legacy_only = convert_workspace(
            &json!({"id":"legacy-only","name":"legacy-only","cwd":"/legacy","kind":"directory"}),
        )
        .unwrap();
        create_database(&legacy_db, &legacy_only);

        let report = migrate(&source, &dest, true).unwrap();
        assert!(
            report
                .issues
                .iter()
                .any(|issue| issue.category == "legacy_database_ignored")
        );
        let db = SqliteStore::open(&target_db).unwrap();
        let names = db
            .list_workspaces()
            .unwrap()
            .into_iter()
            .map(|workspace| workspace.name)
            .collect::<Vec<_>>();
        assert!(names.contains(&"target-only".into()));
        assert!(names.contains(&"one".into()));
        assert!(!names.contains(&"legacy-only".into()));
    }
}
