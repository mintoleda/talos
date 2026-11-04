//! Typed configuration and paths for the Rust-owned Paseo home.

use std::{
    collections::{BTreeMap, HashSet},
    path::{Path, PathBuf},
};

use paseo_domain::ProviderId;
use serde::{Deserialize, Serialize};
use thiserror::Error;

const CONFIG_VERSION: u32 = 1;
const SUPPORTED_PROVIDER_IDS: [&str; 2] = ["talos", "claude"];
const CLAUDE_AGENT_ACP_PACKAGE: &str = "@agentclientprotocol/claude-agent-acp@0.100.0";

#[derive(Debug, Error)]
pub enum ConfigError {
    #[error("configuration I/O failed: {0}")]
    Io(#[from] std::io::Error),
    #[error("invalid TOML configuration: {0}")]
    Toml(#[from] toml::de::Error),
    #[error("failed to serialize configuration: {0}")]
    Serialize(#[from] toml::ser::Error),
    #[error("unsupported configuration version {0}")]
    Version(u32),
    #[error("invalid configuration: {0}")]
    Invalid(String),
    #[error("cannot determine a user home directory; set TALOSD_HOME")]
    MissingHome,
    #[error("refusing to use the legacy Paseo home or a path inside it: {0}")]
    LegacyHomePath(String),
    #[error(
        "{0} is locked by a pre-rename paseod; stop it (or remove paseod.lock once no daemon is running) so its data can move"
    )]
    PreRenameHomeLocked(String),
}

/// Default home directory name under the user's home.
const HOME_DIR_NAME: &str = ".talosd";
/// Home used before the Talos rename; adopted once by [`HomePaths::adopt_pre_rename_home`].
const PRE_RENAME_HOME_DIR_NAME: &str = ".paseo-rs";

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ProviderKind {
    Acp,
    /// Accepted only while loading old configuration so it can be removed.
    #[serde(rename = "pi")]
    LegacyPi,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ProviderConfig {
    pub id: String,
    pub display_name: String,
    pub kind: ProviderKind,
    #[serde(default = "default_true")]
    pub enabled: bool,
    pub command: PathBuf,
    #[serde(default)]
    pub args: Vec<String>,
    #[serde(default)]
    pub env: BTreeMap<String, String>,
    #[serde(default)]
    pub cwd: Option<PathBuf>,
}

impl ProviderConfig {
    #[must_use]
    pub fn talos(executable: impl Into<PathBuf>) -> Self {
        Self {
            id: "talos".into(),
            display_name: "Talos".into(),
            kind: ProviderKind::Acp,
            enabled: true,
            command: executable.into(),
            args: vec!["acp".into()],
            env: BTreeMap::new(),
            cwd: None,
        }
    }

    #[must_use]
    pub fn claude_code(executable: impl Into<PathBuf>) -> Self {
        Self {
            id: "claude".into(),
            display_name: "Claude Code".into(),
            kind: ProviderKind::Acp,
            enabled: true,
            command: executable.into(),
            args: vec!["--yes".into(), CLAUDE_AGENT_ACP_PACKAGE.into()],
            env: BTreeMap::new(),
            cwd: None,
        }
    }
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct AppConfig {
    #[serde(default = "config_version")]
    pub version: u32,
    /// The default applies only to new agents. Existing agent providers never change.
    #[serde(default = "default_provider")]
    pub default_provider: String,
    #[serde(default)]
    pub providers: Vec<ProviderConfig>,
    #[serde(default = "default_event_capacity")]
    pub event_capacity: usize,
    #[serde(default = "default_shutdown_grace_ms")]
    pub shutdown_grace_ms: u64,
}

impl Default for AppConfig {
    fn default() -> Self {
        Self {
            version: CONFIG_VERSION,
            default_provider: default_provider(),
            providers: vec![
                ProviderConfig::talos("talos"),
                ProviderConfig::claude_code("npx"),
            ],
            event_capacity: default_event_capacity(),
            shutdown_grace_ms: default_shutdown_grace_ms(),
        }
    }
}

impl AppConfig {
    pub fn validate(&self) -> Result<(), ConfigError> {
        if self.version != CONFIG_VERSION {
            return Err(ConfigError::Version(self.version));
        }
        if self.event_capacity == 0 || self.event_capacity > 65_536 {
            return Err(ConfigError::Invalid(
                "event_capacity must be between 1 and 65536".into(),
            ));
        }
        if self.shutdown_grace_ms > 120_000 {
            return Err(ConfigError::Invalid(
                "shutdown_grace_ms must not exceed 120000".into(),
            ));
        }
        let mut ids = HashSet::new();
        for provider in &self.providers {
            ProviderId::new(provider.id.clone())
                .map_err(|error| ConfigError::Invalid(error.to_string()))?;
            if provider.kind != ProviderKind::Acp
                || !SUPPORTED_PROVIDER_IDS.contains(&provider.id.as_str())
            {
                return Err(ConfigError::Invalid(format!(
                    "provider {} is not supported; only talos and claude are available",
                    provider.id
                )));
            }
            if provider.display_name.trim().is_empty() {
                return Err(ConfigError::Invalid(format!(
                    "provider {} has an empty display name",
                    provider.id
                )));
            }
            if !ids.insert(provider.id.as_str()) {
                return Err(ConfigError::Invalid(format!(
                    "duplicate provider id {}",
                    provider.id
                )));
            }
            if provider.command.as_os_str().is_empty() {
                return Err(ConfigError::Invalid(format!(
                    "provider {} has an empty command",
                    provider.id
                )));
            }
            if provider
                .env
                .keys()
                .any(|key| key.is_empty() || key.contains('='))
            {
                return Err(ConfigError::Invalid(format!(
                    "provider {} has an invalid environment variable name",
                    provider.id
                )));
            }
        }
        for required in SUPPORTED_PROVIDER_IDS {
            if !ids.contains(required) {
                return Err(ConfigError::Invalid(format!(
                    "required provider {required:?} is not registered"
                )));
            }
        }
        if !ids.contains(self.default_provider.as_str()) {
            return Err(ConfigError::Invalid(format!(
                "default provider {:?} is not registered",
                self.default_provider
            )));
        }
        if !self
            .providers
            .iter()
            .any(|provider| provider.id == self.default_provider && provider.enabled)
        {
            return Err(ConfigError::Invalid(format!(
                "default provider {:?} is disabled",
                self.default_provider
            )));
        }
        Ok(())
    }

    fn normalize_supported_providers(&mut self) -> bool {
        let before = self.providers.clone();
        let before_default = self.default_provider.clone();
        self.providers.retain(|provider| {
            provider.kind == ProviderKind::Acp
                && SUPPORTED_PROVIDER_IDS.contains(&provider.id.as_str())
        });
        if !self.providers.iter().any(|provider| provider.id == "talos") {
            self.providers.push(ProviderConfig::talos("talos"));
        }
        if !self
            .providers
            .iter()
            .any(|provider| provider.id == "claude")
        {
            self.providers.push(ProviderConfig::claude_code("npx"));
        }
        if !SUPPORTED_PROVIDER_IDS.contains(&self.default_provider.as_str()) {
            self.default_provider = default_provider();
        }
        self.providers != before || self.default_provider != before_default
    }
}

#[derive(Clone, Debug)]
pub struct HomePaths {
    pub home: PathBuf,
    pub config: PathBuf,
    pub database: PathBuf,
}

impl HomePaths {
    pub fn discover() -> Result<Self, ConfigError> {
        let home = if let Some(path) = std::env::var_os("TALOSD_HOME") {
            PathBuf::from(path)
        } else {
            directories::BaseDirs::new()
                .ok_or(ConfigError::MissingHome)?
                .home_dir()
                .join(HOME_DIR_NAME)
        };
        Ok(Self::at(home))
    }

    #[must_use]
    pub fn at(home: impl Into<PathBuf>) -> Self {
        let home = home.into();
        Self {
            config: home.join("config.toml"),
            database: home.join("state.sqlite3"),
            home,
        }
    }

    /// Reject the installed legacy home and all paths that resolve inside it.
    /// Call this before creating the selected home or any parent-owned files.
    pub fn validate(&self) -> Result<(), ConfigError> {
        let Some(user_home) =
            directories::BaseDirs::new().map(|dirs| dirs.home_dir().to_path_buf())
        else {
            return Err(ConfigError::MissingHome);
        };
        self.validate_not_within(&user_home.join(".paseo"))
    }

    fn validate_not_within(&self, legacy_home: &Path) -> Result<(), ConfigError> {
        let legacy = canonicalize_future_path(legacy_home)?;
        let selected = canonicalize_future_path(&self.home)?;
        if selected == legacy || selected.starts_with(&legacy) {
            return Err(ConfigError::LegacyHomePath(self.home.display().to_string()));
        }
        Ok(())
    }

    /// One-time move of the pre-rename `~/.paseo-rs` home into the default
    /// `~/.talosd`. Only the default home is ever filled this way; an explicit
    /// `TALOSD_HOME` elsewhere is left alone. Returns whether anything moved.
    /// Call after [`Self::validate`] and before the instance lock is taken.
    pub fn adopt_pre_rename_home(&self) -> Result<bool, ConfigError> {
        let Some(user_home) =
            directories::BaseDirs::new().map(|dirs| dirs.home_dir().to_path_buf())
        else {
            return Err(ConfigError::MissingHome);
        };
        if canonicalize_future_path(&self.home)?
            != canonicalize_future_path(&user_home.join(HOME_DIR_NAME))?
        {
            return Ok(false);
        }
        self.adopt_from(&user_home.join(PRE_RENAME_HOME_DIR_NAME))
    }

    /// Move entries from `old` into this home. The desktop app may already have
    /// created the new home for its daemon log, so existence alone does not
    /// mean "already migrated"; an existing database does.
    fn adopt_from(&self, old: &Path) -> Result<bool, ConfigError> {
        match std::fs::symlink_metadata(old) {
            Ok(metadata) if metadata.is_dir() => {}
            Ok(_) => return Ok(false),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(false),
            Err(error) => return Err(error.into()),
        }
        if std::fs::symlink_metadata(&self.database).is_ok() {
            return Ok(false);
        }
        if std::fs::symlink_metadata(old.join("paseod.lock")).is_ok() {
            return Err(ConfigError::PreRenameHomeLocked(old.display().to_string()));
        }
        std::fs::create_dir_all(&self.home)?;
        for entry in std::fs::read_dir(old)? {
            let entry = entry?;
            let name = entry.file_name();
            let target = match name.to_str() {
                // No lock means no daemon is listening; the socket is dead.
                Some("paseod.sock") => {
                    std::fs::remove_file(entry.path())?;
                    continue;
                }
                Some("paseod.log") => self.home.join("talosd.log"),
                _ => self.home.join(&name),
            };
            if std::fs::symlink_metadata(&target).is_err() {
                std::fs::rename(entry.path(), target)?;
            }
        }
        // Anything that collided stays behind for the user to inspect.
        let _ = std::fs::remove_dir(old);
        Ok(true)
    }
}

fn canonicalize_future_path(path: &Path) -> Result<PathBuf, ConfigError> {
    let absolute = if path.is_absolute() {
        path.to_path_buf()
    } else {
        std::env::current_dir()?.join(path)
    };
    let mut resolved = PathBuf::new();
    for component in absolute.components() {
        match component {
            std::path::Component::Prefix(prefix) => resolved.push(prefix.as_os_str()),
            std::path::Component::RootDir => resolved.push(component.as_os_str()),
            std::path::Component::CurDir => {}
            std::path::Component::ParentDir => {
                resolved.pop();
            }
            std::path::Component::Normal(part) => {
                resolved.push(part);
                match std::fs::canonicalize(&resolved) {
                    Ok(canonical) => resolved = canonical,
                    Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
                    Err(error) => return Err(error.into()),
                }
            }
        }
    }
    Ok(resolved)
}

pub(crate) async fn load_or_create(path: &Path) -> Result<AppConfig, ConfigError> {
    match tokio::fs::read_to_string(path).await {
        Ok(contents) => {
            let mut config: AppConfig = toml::from_str(&contents)?;
            let normalized = config.normalize_supported_providers();
            config.validate()?;
            if normalized {
                save(path, &config).await?;
            }
            if let Some(default_provider) = std::env::var_os("TALOSD_DEFAULT_PROVIDER") {
                config.default_provider = default_provider.to_string_lossy().into_owned();
                config.validate()?;
            }
            Ok(config)
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            let mut config = AppConfig::default();
            if let Some(default_provider) = std::env::var_os("TALOSD_DEFAULT_PROVIDER") {
                config.default_provider = default_provider.to_string_lossy().into_owned();
            }
            config.validate()?;
            save(path, &config).await?;
            Ok(config)
        }
        Err(error) => Err(error.into()),
    }
}

pub(crate) async fn save(path: &Path, config: &AppConfig) -> Result<(), ConfigError> {
    config.validate()?;
    let content = toml::to_string_pretty(config)?;
    let parent = path
        .parent()
        .ok_or_else(|| ConfigError::Invalid("configuration path has no parent".into()))?;
    tokio::fs::create_dir_all(parent).await?;
    let temporary = path.with_extension(format!("toml.tmp.{}", std::process::id()));
    tokio::fs::write(&temporary, content).await?;
    set_private_permissions(&temporary).await?;
    tokio::fs::rename(temporary, path).await?;
    Ok(())
}

#[cfg(unix)]
async fn set_private_permissions(path: &Path) -> Result<(), std::io::Error> {
    use std::os::unix::fs::PermissionsExt;
    tokio::fs::set_permissions(path, std::fs::Permissions::from_mode(0o600)).await
}

#[cfg(not(unix))]
async fn set_private_permissions(_path: &Path) -> Result<(), std::io::Error> {
    Ok(())
}

fn config_version() -> u32 {
    CONFIG_VERSION
}
fn default_provider() -> String {
    "talos".into()
}
fn default_event_capacity() -> usize {
    1024
}
fn default_shutdown_grace_ms() -> u64 {
    1500
}
fn default_true() -> bool {
    true
}

#[cfg(test)]
mod tests {
    use super::*;
    use uuid::Uuid;

    struct TestPaths(PathBuf);

    impl TestPaths {
        fn new() -> Self {
            Self(std::env::temp_dir().join(format!("talosd-config-test-{}", Uuid::now_v7())))
        }
    }

    impl Drop for TestPaths {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.0);
        }
    }

    #[test]
    fn defaults_to_only_talos_and_claude_code() {
        let config = AppConfig::default();
        assert_eq!(
            config
                .providers
                .iter()
                .map(|provider| provider.id.as_str())
                .collect::<Vec<_>>(),
            ["talos", "claude"]
        );
        assert_eq!(config.providers[1].command, PathBuf::from("npx"));
        assert_eq!(
            config.providers[1].args,
            ["--yes", CLAUDE_AGENT_ACP_PACKAGE]
        );
        config.validate().unwrap();
    }

    #[tokio::test]
    async fn old_provider_entries_are_replaced_with_claude_code() {
        let temp = TestPaths::new();
        let path = temp.0.join("config.toml");
        std::fs::create_dir_all(&temp.0).unwrap();
        std::fs::write(
            &path,
            r#"version = 1
default_provider = "pi"

[[providers]]
id = "talos"
display_name = "Talos"
kind = "acp"
enabled = true
command = "talos"
args = ["acp"]

[[providers]]
id = "pi"
display_name = "Pi"
kind = "pi"
enabled = true
command = "pi"

[[providers]]
id = "codex"
display_name = "Codex"
kind = "acp"
enabled = true
command = "codex-acp"
"#,
        )
        .unwrap();

        let config = load_or_create(&path).await.unwrap();
        assert_eq!(config.default_provider, "talos");
        assert_eq!(
            config
                .providers
                .iter()
                .map(|provider| provider.id.as_str())
                .collect::<Vec<_>>(),
            ["talos", "claude"]
        );
        let saved = std::fs::read_to_string(path).unwrap();
        assert!(!saved.contains("id = \"pi\""));
        assert!(!saved.contains("id = \"codex\""));
        assert!(saved.contains("id = \"claude\""));
    }

    #[test]
    fn refuses_legacy_root_and_descendants_before_creation() {
        let temp = TestPaths::new();
        let legacy = temp.0.join("legacy-home");
        std::fs::create_dir_all(&legacy).unwrap();

        assert!(HomePaths::at(&legacy).validate_not_within(&legacy).is_err());
        let descendant = HomePaths::at(legacy.join("nested").join("rust-home"));
        assert!(descendant.validate_not_within(&legacy).is_err());
        assert!(!descendant.home.exists());

        let unrelated = HomePaths::at(temp.0.join("separate-home"));
        unrelated.validate_not_within(&legacy).unwrap();
    }

    #[cfg(unix)]
    #[test]
    fn refuses_symlink_aliases_of_legacy_root() {
        use std::os::unix::fs::symlink;

        let temp = TestPaths::new();
        let legacy = temp.0.join("legacy-home");
        let alias = temp.0.join("alias");
        std::fs::create_dir_all(&legacy).unwrap();
        symlink(&legacy, &alias).unwrap();

        let selected = HomePaths::at(alias.join("nested"));
        assert!(selected.validate_not_within(&legacy).is_err());
    }

    #[test]
    fn adopts_pre_rename_home_once_alongside_an_existing_log() {
        let temp = TestPaths::new();
        let old = temp.0.join("old");
        std::fs::create_dir_all(&old).unwrap();
        std::fs::write(old.join("state.sqlite3"), "db").unwrap();
        std::fs::write(old.join("auth.token"), "token").unwrap();
        std::fs::write(old.join("paseod.log"), "old log").unwrap();
        std::fs::write(old.join("paseod.sock"), "").unwrap();
        let home = HomePaths::at(temp.0.join("new"));
        std::fs::create_dir_all(&home.home).unwrap();
        std::fs::write(home.home.join("talosd.log"), "new log").unwrap();

        assert!(home.adopt_from(&old).unwrap());
        assert_eq!(std::fs::read_to_string(&home.database).unwrap(), "db");
        assert!(home.home.join("auth.token").exists());
        assert!(!home.home.join("paseod.sock").exists());
        assert_eq!(
            std::fs::read_to_string(home.home.join("talosd.log")).unwrap(),
            "new log"
        );
        // The colliding log stays behind, so the old directory is kept.
        assert!(old.join("paseod.log").exists());
        assert!(!old.join("state.sqlite3").exists());

        assert!(!home.adopt_from(&old).unwrap());
    }

    #[test]
    fn refuses_to_adopt_a_locked_pre_rename_home() {
        let temp = TestPaths::new();
        let old = temp.0.join("old");
        std::fs::create_dir_all(&old).unwrap();
        std::fs::write(old.join("state.sqlite3"), "db").unwrap();
        std::fs::write(old.join("paseod.lock"), "123").unwrap();
        let home = HomePaths::at(temp.0.join("new"));

        assert!(matches!(
            home.adopt_from(&old),
            Err(ConfigError::PreRenameHomeLocked(_))
        ));
        assert!(old.join("state.sqlite3").exists());
    }
}
