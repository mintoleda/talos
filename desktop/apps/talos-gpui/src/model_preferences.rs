//! Global model frecency and model-provider rail ordering for the GPUI picker.
use serde::{Deserialize, Serialize};
use std::{
    fs, io,
    path::{Path, PathBuf},
    time::{SystemTime, UNIX_EPOCH},
};

const MAX_USE_TIMESTAMPS: usize = 128;
const MAX_TRACKED_MODELS: usize = 512;
const FOUR_HOURS: i64 = 4 * 60 * 60;
const ONE_DAY: i64 = 24 * 60 * 60;
const ONE_WEEK: i64 = 7 * ONE_DAY;
const ONE_MONTH: i64 = 30 * ONE_DAY;

#[derive(Clone, Debug, Default, Deserialize, Serialize)]
#[serde(default)]
pub(super) struct ModelPreferences {
    provider_order: Vec<String>,
    /// Successful model selections and accepted prompts, global across sessions/workspaces.
    model_uses: std::collections::HashMap<String, Vec<i64>>,
}

impl ModelPreferences {
    pub fn load() -> Self {
        preference_path()
            .map(|path| Self::load_from(&path))
            .unwrap_or_default()
    }

    fn load_from(path: &Path) -> Self {
        fs::read(path)
            .ok()
            .and_then(|contents| serde_json::from_slice(&contents).ok())
            .unwrap_or_default()
    }

    pub fn save(&self) -> io::Result<()> {
        if let Some(path) = preference_path() {
            self.save_to(&path)?;
        }
        Ok(())
    }

    fn save_to(&self, path: &Path) -> io::Result<()> {
        let parent = path
            .parent()
            .ok_or_else(|| io::Error::other("preferences path has no parent directory"))?;
        fs::create_dir_all(parent)?;
        let contents = serde_json::to_vec_pretty(self)
            .map_err(|error| io::Error::new(io::ErrorKind::InvalidData, error))?;
        let file_name = path
            .file_name()
            .and_then(|name| name.to_str())
            .unwrap_or("model-preferences.json");
        let temporary = path.with_file_name(format!(".{file_name}.{}.tmp", std::process::id()));
        fs::write(&temporary, contents)?;
        if let Err(error) = fs::rename(&temporary, path) {
            let _ = fs::remove_file(&temporary);
            return Err(error);
        }
        Ok(())
    }

    pub fn ordered_provider_keys(&self, mut keys: Vec<String>) -> Vec<String> {
        keys.sort_by(|left, right| {
            let left_rank = self.provider_order.iter().position(|key| key == left);
            let right_rank = self.provider_order.iter().position(|key| key == right);
            match (left_rank, right_rank) {
                (Some(left), Some(right)) => left.cmp(&right),
                (Some(_), None) => std::cmp::Ordering::Less,
                (None, Some(_)) => std::cmp::Ordering::Greater,
                (None, None) => left.cmp(right),
            }
        });
        keys
    }

    /// Move `source` immediately before `target` in the visible rail order.
    pub fn move_provider(&mut self, available: &[String], source: &str, target: &str) -> bool {
        if source == target
            || !available.iter().any(|key| key == source)
            || !available.iter().any(|key| key == target)
        {
            return false;
        }

        let mut order = self.provider_order.clone();
        for key in available {
            if !order.contains(key) {
                order.push(key.clone());
            }
        }
        let Some(source_index) = order.iter().position(|key| key == source) else {
            return false;
        };
        order.remove(source_index);
        let Some(target_index) = order.iter().position(|key| key == target) else {
            return false;
        };
        order.insert(target_index, source.to_owned());
        self.provider_order = order;
        true
    }

    pub fn record_model_use(&mut self, model_id: &str, now: i64) {
        let timestamps = self.model_uses.entry(model_id.to_owned()).or_default();
        timestamps.push(now);
        timestamps.sort_unstable_by(|left, right| right.cmp(left));
        timestamps.truncate(MAX_USE_TIMESTAMPS);
        if self.model_uses.len() > MAX_TRACKED_MODELS {
            let least_recent = self
                .model_uses
                .iter()
                .filter(|(id, _)| id.as_str() != model_id)
                .min_by_key(|(_, timestamps)| timestamps.first().copied().unwrap_or(i64::MIN))
                .map(|(id, _)| id.clone());
            if let Some(least_recent) = least_recent {
                self.model_uses.remove(&least_recent);
            }
        }
    }

    #[must_use]
    pub fn frecency_score(&self, model_id: &str, now: i64) -> u64 {
        self.model_uses
            .get(model_id)
            .into_iter()
            .flatten()
            .map(|used| {
                let age = now.saturating_sub(*used).max(0);
                match age {
                    0..FOUR_HOURS => 100,
                    FOUR_HOURS..ONE_DAY => 70,
                    ONE_DAY..ONE_WEEK => 50,
                    ONE_WEEK..ONE_MONTH => 30,
                    _ => 5,
                }
            })
            .sum()
    }
}

fn preference_path() -> Option<PathBuf> {
    directories::BaseDirs::new().map(|dirs| {
        dirs.config_dir()
            .join("talos-gpui")
            .join("model-preferences.json")
    })
}

pub(super) fn unix_now() -> i64 {
    i64::try_from(
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs(),
    )
    .unwrap_or(i64::MAX)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn provider_order_is_customized_and_new_providers_are_appended_alphabetically() {
        let mut preferences = ModelPreferences::default();
        let keys = vec!["zeta".into(), "alpha".into(), "beta".into()];
        let visible = preferences.ordered_provider_keys(keys);
        assert_eq!(visible, ["alpha", "beta", "zeta"]);
        assert!(preferences.move_provider(&visible, "zeta", "alpha"));
        assert_eq!(
            preferences.ordered_provider_keys(visible),
            ["zeta", "alpha", "beta"]
        );
        assert_eq!(
            preferences.ordered_provider_keys(vec!["omega".into(), "beta".into()]),
            ["beta", "omega"]
        );
    }

    #[test]
    fn frecency_weights_recent_events_more_and_accumulates_frequency() {
        let now = 2_000_000_000;
        let mut preferences = ModelPreferences::default();
        preferences.record_model_use("recent", now - 60);
        preferences.record_model_use("frequent", now - 8 * ONE_DAY);
        preferences.record_model_use("frequent", now - 9 * ONE_DAY);
        assert!(
            preferences.frecency_score("recent", now) > preferences.frecency_score("frequent", now)
        );
        preferences.record_model_use("frequent", now - 60);
        assert!(
            preferences.frecency_score("frequent", now) > preferences.frecency_score("recent", now)
        );
    }

    #[test]
    fn model_usage_and_provider_order_round_trip_atomically() {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let directory = std::env::temp_dir().join(format!(
            "talos-gpui-model-preferences-{}-{unique}",
            std::process::id()
        ));
        let path = directory.join("preferences.json");
        let mut preferences = ModelPreferences {
            provider_order: vec!["deepseek".into(), "openai-codex".into()],
            ..Default::default()
        };
        preferences.record_model_use("deepseek/flash", 1234);
        preferences.save_to(&path).unwrap();
        let restored = ModelPreferences::load_from(&path);
        assert_eq!(
            restored.ordered_provider_keys(vec!["openai-codex".into(), "deepseek".into()]),
            ["deepseek", "openai-codex"]
        );
        assert_eq!(restored.frecency_score("deepseek/flash", 1234), 100);
        fs::remove_dir_all(directory).unwrap();
    }
}
