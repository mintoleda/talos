//! One disclosure per assistant turn, independent of the desktop renderer.
use std::collections::{HashMap, HashSet};

use chrono::{DateTime, FixedOffset};
use serde_json::Value;

use crate::state::{Entry, EntryKind, ToolState};

#[derive(Debug)]
pub enum TimelineItem<'a> {
    Message(&'a Entry),
    Activity(ActivityGroup<'a>),
}

#[derive(Debug)]
pub struct ActivityGroup<'a> {
    /// The first step's durable ID keeps the disclosure stable as work arrives.
    pub id: &'a str,
    pub steps: Vec<&'a Entry>,
    pub active: bool,
}

impl ActivityGroup<'_> {
    #[must_use]
    pub fn current(&self) -> &Entry {
        // A parallel tool completing must not hide another still-running tool.
        let live = self
            .steps
            .iter()
            .copied()
            .filter(|entry| self.active && is_running(entry));
        live.max_by_key(|entry| timestamp(entry))
            .unwrap_or_else(|| {
                self.steps
                    .iter()
                    .copied()
                    .max_by_key(|entry| timestamp(entry))
                    .expect("activity has steps")
            })
    }

    #[must_use]
    pub fn label(&self) -> String {
        if self.active {
            step_label(self.current(), true)
        } else {
            self.summary()
        }
    }

    /// Completed turns summarize the whole chain, not whichever tool ran last.
    /// File counts are distinct successful targets; failed/interrupted calls
    /// are reported separately rather than claimed as completed work.
    #[must_use]
    pub fn summary(&self) -> String {
        let mut thoughts = 0;
        let mut read = HashSet::new();
        let mut edited = HashSet::new();
        let mut written = HashSet::new();
        let mut commands = 0;
        let mut other = 0;
        let mut failed = 0;
        let mut stopped = 0;
        for entry in &self.steps {
            match &entry.kind {
                EntryKind::Thought { .. } => thoughts += 1,
                EntryKind::Tool {
                    state: ToolState::Failed,
                    ..
                } => failed += 1,
                EntryKind::Tool {
                    state: ToolState::Running,
                    ..
                } => stopped += 1,
                EntryKind::Tool {
                    name,
                    target,
                    input,
                    state: ToolState::Completed,
                    ..
                } => {
                    let (action, subject) = tool_action(name, target, input);
                    match ToolAction::from_name(&action) {
                        ToolAction::Read => {
                            read.insert(subject);
                        }
                        ToolAction::Edit => {
                            edited.insert(subject);
                        }
                        ToolAction::Write => {
                            written.insert(subject);
                        }
                        ToolAction::Command => commands += 1,
                        _ => other += 1,
                    }
                }
                _ => {}
            }
        }
        let mut parts = Vec::new();
        for (count, verb, singular, plural) in [
            (thoughts, "thought", "time", "times"),
            (edited.len(), "edited", "file", "files"),
            (written.len(), "wrote", "file", "files"),
            (commands, "ran", "command", "commands"),
            (read.len(), "read", "file", "files"),
            (other, "used", "other tool", "other tools"),
            (failed, "failed", "tool call", "tool calls"),
            (stopped, "stopped", "tool call", "tool calls"),
        ] {
            if count > 0 {
                parts.push(format!(
                    "{verb} {count} {}",
                    if count == 1 { singular } else { plural }
                ));
            }
        }
        let mut summary = match parts.as_slice() {
            [] => "no activity".into(),
            [only] => only.clone(),
            [first, last] => format!("{first} and {last}"),
            _ => format!(
                "{}, and {}",
                parts[..parts.len() - 1].join(", "),
                parts.last().expect("summary has clauses")
            ),
        };
        summary[..1].make_ascii_uppercase();
        summary
    }

    #[must_use]
    pub fn running(&self) -> bool {
        self.active && is_running(self.current())
    }
}

fn timestamp(entry: &Entry) -> Option<DateTime<FixedOffset>> {
    DateTime::parse_from_rfc3339(&entry.at).ok()
}

fn is_running(entry: &Entry) -> bool {
    matches!(
        entry.kind,
        EntryKind::Thought {
            streaming: true,
            ..
        } | EntryKind::Tool {
            state: ToolState::Running,
            ..
        }
    )
}

/// Messages retain their order and stay outside the disclosure, including
/// commentary between tools. Missing run IDs (imported history) use user turns
/// as boundaries; distinct durable run IDs are never merged.
#[must_use]
pub fn timeline(entries: &[Entry], running: bool) -> Vec<TimelineItem<'_>> {
    let mut items = Vec::new();
    let mut groups = HashMap::new();
    let mut turn = 0;
    let current_turn = entries
        .iter()
        .filter(|entry| matches!(entry.kind, EntryKind::User { .. }))
        .count();
    let current_run = entries.last().and_then(|entry| entry.run.as_deref());
    for entry in entries {
        if matches!(entry.kind, EntryKind::User { .. }) {
            turn += 1;
        }
        if !matches!(
            entry.kind,
            EntryKind::Thought { .. } | EntryKind::Tool { .. }
        ) {
            items.push(TimelineItem::Message(entry));
            continue;
        }
        let key = (turn, entry.run.as_deref());
        if let Some(&index) = groups.get(&key) {
            if let TimelineItem::Activity(group) = &mut items[index] {
                group.steps.push(entry);
            }
        } else {
            groups.insert(key, items.len());
            items.push(TimelineItem::Activity(ActivityGroup {
                id: &entry.id,
                steps: vec![entry],
                active: running && turn == current_turn && entry.run.as_deref() == current_run,
            }));
        }
    }
    items
}

#[must_use]
pub fn step_label(entry: &Entry, active: bool) -> String {
    match &entry.kind {
        EntryKind::Thought { streaming, .. } => if active && *streaming {
            "Thinking"
        } else {
            "Thought"
        }
        .into(),
        EntryKind::Tool {
            name,
            target,
            input,
            state,
            ..
        } => {
            let (action, subject) = tool_action(name, target, input);
            let (present, past, infinitive) = ToolAction::from_name(&action).verbs();
            let verb = match state {
                ToolState::Running if active => present.to_owned(),
                ToolState::Running => format!("Stopped {}", present.to_lowercase()),
                ToolState::Completed => past.to_owned(),
                ToolState::Failed => format!("Failed to {infinitive}"),
            };
            if subject.is_empty() {
                verb
            } else {
                format!("{verb} {subject}")
            }
        }
        _ => String::new(),
    }
}

#[derive(Clone, Copy)]
enum ToolAction {
    Read,
    Edit,
    Write,
    List,
    Search,
    Fetch,
    Command,
    Other,
}

impl ToolAction {
    fn from_name(name: &str) -> Self {
        match name {
            "read" | "read_file" | "readfile" => Self::Read,
            "edit" | "edit_file" | "editfile" | "apply_patch" | "patch" => Self::Edit,
            "write" | "write_file" | "writefile" => Self::Write,
            "ls" | "list" | "list_files" | "glob" | "find" => Self::List,
            "grep" | "search" | "web_search" => Self::Search,
            "fetch" | "web_fetch" => Self::Fetch,
            "bash" | "bash_background" | "execute" | "shell" | "run" | "command" => Self::Command,
            _ => Self::Other,
        }
    }

    fn verbs(self) -> (&'static str, &'static str, &'static str) {
        match self {
            Self::Read => ("Reading", "Read", "read"),
            Self::Edit => ("Editing", "Edited", "edit"),
            Self::Write => ("Writing", "Wrote", "write"),
            Self::List => ("Listing", "Listed", "list"),
            Self::Search => ("Searching", "Searched", "search"),
            Self::Fetch => ("Fetching", "Fetched", "fetch"),
            Self::Command => ("Running", "Ran", "run"),
            Self::Other => ("Calling", "Called", "call"),
        }
    }
}

fn tool_action(name: &str, target: &str, input: &str) -> (String, String) {
    // ACP updates often omit title/kind on completion. The projection retains
    // the original title, which also recovers names from older saved timelines.
    let title =
        if target.contains(':') && matches!(name.to_ascii_lowercase().as_str(), "tool" | "other") {
            target
        } else {
            name
        };
    let (source_action, title_target) = title.split_once(": ").unwrap_or((title, ""));
    let action = source_action.to_ascii_lowercase().replace(' ', "_");
    let value = serde_json::from_str::<Value>(input).unwrap_or(Value::Null);
    let subject = [
        "path",
        "file_path",
        "filePath",
        "command",
        "query",
        "pattern",
        "url",
    ]
    .into_iter()
    .find_map(|key| value.get(key).and_then(Value::as_str))
    .unwrap_or_else(|| {
        if !title_target.is_empty() {
            title_target
        } else if target == name {
            ""
        } else {
            target
        }
    });
    let subject = subject.lines().next().unwrap_or_default().trim();
    let subject = if subject.is_empty() && !matches!(action.as_str(), "tool" | "other") {
        source_action
    } else {
        subject
    };
    (action, subject.to_owned())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn entry(id: &str, run: Option<&str>, kind: EntryKind) -> Entry {
        Entry {
            id: id.into(),
            run: run.map(str::to_owned),
            at: format!("2026-01-01T00:00:{id}Z"),
            kind,
        }
    }
    fn tool(id: &str, state: ToolState) -> Entry {
        entry(
            id,
            Some("r1"),
            EntryKind::Tool {
                call_id: id.into(),
                name: "read: file.rs".into(),
                target: "read: file.rs".into(),
                state,
                input: r#"{"path":"file.rs"}"#.into(),
                output: "saved output".into(),
            },
        )
    }
    fn group<'a>(items: &'a [TimelineItem<'a>]) -> &'a ActivityGroup<'a> {
        items
            .iter()
            .find_map(|item| {
                if let TimelineItem::Activity(group) = item {
                    Some(group)
                } else {
                    None
                }
            })
            .unwrap()
    }

    #[test]
    fn one_row_per_turn_keeps_commentary_and_answers_outside() {
        let entries = vec![
            entry(
                "01",
                Some("r1"),
                EntryKind::User {
                    text: "hello".into(),
                },
            ),
            entry(
                "02",
                Some("r1"),
                EntryKind::Thought {
                    text: "plan".into(),
                    streaming: false,
                },
            ),
            tool("03", ToolState::Completed),
            entry(
                "04",
                Some("r1"),
                EntryKind::Assistant {
                    text: "checking more".into(),
                    streaming: false,
                },
            ),
            tool("05", ToolState::Completed),
            entry(
                "06",
                Some("r1"),
                EntryKind::Assistant {
                    text: "answer".into(),
                    streaming: false,
                },
            ),
            entry(
                "07",
                Some("r2"),
                EntryKind::User {
                    text: "next".into(),
                },
            ),
            entry(
                "08",
                Some("r2"),
                EntryKind::Thought {
                    text: "next plan".into(),
                    streaming: false,
                },
            ),
        ];
        let items = timeline(&entries, false);
        assert_eq!(items.len(), 6);
        assert_eq!(group(&items).steps.len(), 3);
        assert_eq!(group(&items).id, "02");
        assert_eq!(group(&items).label(), "Thought 1 time and read 1 file");
        assert!(
            matches!(&items[2], TimelineItem::Message(Entry { kind: EntryKind::Assistant { text, .. }, .. }) if text == "checking more")
        );
        assert!(
            matches!(&items[5], TimelineItem::Activity(group) if group.steps.len() == 1 && group.label() == "Thought 1 time")
        );
    }

    #[test]
    fn imported_history_is_split_at_users_even_without_run_ids() {
        let mut entries = vec![
            tool("01", ToolState::Completed),
            entry(
                "02",
                None,
                EntryKind::User {
                    text: "next".into(),
                },
            ),
            tool("03", ToolState::Completed),
        ];
        for entry in &mut entries {
            entry.run = None;
        }
        assert_eq!(timeline(&entries, false).len(), 3);
    }

    #[test]
    fn thinking_and_tools_use_live_and_completed_verbs_without_a_timer() {
        let mut entries = vec![entry(
            "01",
            Some("r1"),
            EntryKind::Thought {
                text: "plan".into(),
                streaming: true,
            },
        )];
        assert_eq!(group(&timeline(&entries, true)).label(), "Thinking");
        if let EntryKind::Thought { streaming, .. } = &mut entries[0].kind {
            *streaming = false;
        }
        assert_eq!(group(&timeline(&entries, true)).label(), "Thought");
        entries.push(tool("02", ToolState::Running));
        assert_eq!(group(&timeline(&entries, true)).label(), "Reading file.rs");
        entries[1] = tool("02", ToolState::Completed);
        assert_eq!(group(&timeline(&entries, true)).label(), "Read file.rs");
        assert_eq!(
            group(&timeline(&entries, false)).label(),
            "Thought 1 time and read 1 file"
        );
    }

    fn completed_tool(id: &str, name: &str, target: &str) -> Entry {
        let mut step = tool(id, ToolState::Completed);
        if let EntryKind::Tool {
            name: known_name,
            target: known_target,
            input,
            ..
        } = &mut step.kind
        {
            *known_name = name.into();
            *known_target = target.into();
            input.clear();
        }
        step
    }

    #[test]
    fn finished_summary_matches_the_reference_and_uses_grammatical_counts() {
        let mut entries = vec![
            entry(
                "01",
                Some("r1"),
                EntryKind::Thought {
                    text: "first".into(),
                    streaming: false,
                },
            ),
            entry(
                "02",
                Some("r1"),
                EntryKind::Thought {
                    text: "second".into(),
                    streaming: false,
                },
            ),
            completed_tool("03", "read", "one.rs"),
            completed_tool("04", "ReadFile", "two.rs"),
        ];
        for id in 5..14 {
            entries.push(completed_tool(&format!("{id:02}"), "Codemode", ""));
        }
        let items = timeline(&entries, false);
        assert_eq!(
            group(&items).label(),
            "Thought 2 times, read 2 files, and used 9 other tools"
        );
        assert_eq!(group(&items).steps.len(), 13);
        assert_eq!(
            step_label(entries.last().unwrap(), false),
            "Called Codemode"
        );
        assert_eq!(
            group(&timeline(&entries[2..3], false)).label(),
            "Read 1 file"
        );
        assert_eq!(
            group(&timeline(&entries[4..5], false)).label(),
            "Used 1 other tool"
        );
    }

    #[test]
    fn summaries_count_files_once_and_commands_per_call_without_claiming_failed_work() {
        let mut entries = vec![
            completed_tool("01", "read", "file.rs"),
            completed_tool("02", "read_file", "file.rs"),
            completed_tool("03", "edit", "file.rs"),
            completed_tool("04", "apply_patch", "file.rs"),
            completed_tool("05", "write", "new.rs"),
            completed_tool("06", "bash", "echo same command"),
            completed_tool("07", "execute", "echo same command"),
        ];
        assert_eq!(
            group(&timeline(&entries, false)).label(),
            "Edited 1 file, wrote 1 file, ran 2 commands, and read 1 file"
        );
        entries.push(tool("08", ToolState::Failed));
        entries.push(tool("09", ToolState::Running));
        assert_eq!(
            group(&timeline(&entries, false)).label(),
            "Edited 1 file, wrote 1 file, ran 2 commands, read 1 file, failed 1 tool call, and stopped 1 tool call"
        );
    }

    #[test]
    fn completed_summary_remains_when_a_new_turn_starts() {
        let mut entries = vec![tool("01", ToolState::Completed)];
        entries.push(entry(
            "02",
            Some("r2"),
            EntryKind::User {
                text: "next".into(),
            },
        ));
        assert_eq!(group(&timeline(&entries, true)).label(), "Read 1 file");
        entries.push(entry(
            "03",
            Some("r2"),
            EntryKind::Thought {
                text: "next plan".into(),
                streaming: true,
            },
        ));
        let items = timeline(&entries, true);
        assert_eq!(group(&items).label(), "Read 1 file");
        assert!(
            matches!(items.last().unwrap(), TimelineItem::Activity(group) if group.label() == "Thinking")
        );
    }

    #[test]
    fn parallel_updates_show_current_work_then_the_most_recent_completion() {
        let mut entries = vec![
            tool("01", ToolState::Running),
            tool("02", ToolState::Completed),
        ];
        entries[1].at = "2026-01-01T00:00:03Z".into();
        assert_eq!(group(&timeline(&entries, true)).current().id, "01");
        entries[0] = tool("01", ToolState::Completed);
        entries[0].at = "2026-01-01T00:00:04Z".into();
        assert_eq!(group(&timeline(&entries, true)).current().id, "01");
        assert_eq!(group(&timeline(&entries, true)).steps.len(), 2);
    }

    #[test]
    fn failures_interruption_and_edit_titles_are_honest() {
        let mut step = tool("01", ToolState::Failed);
        assert_eq!(step_label(&step, true), "Failed to read file.rs");
        step = tool("01", ToolState::Running);
        assert_eq!(step_label(&step, false), "Stopped reading file.rs");
        if let EntryKind::Tool {
            name,
            target,
            input,
            ..
        } = &mut step.kind
        {
            *name = "tool".into();
            *target = "edit: workflow.yml".into();
            *input = String::new();
        }
        assert_eq!(step_label(&step, true), "Editing workflow.yml");
        if let EntryKind::Tool { state, .. } = &mut step.kind {
            *state = ToolState::Completed;
        }
        assert_eq!(step_label(&step, true), "Edited workflow.yml");
    }
}
