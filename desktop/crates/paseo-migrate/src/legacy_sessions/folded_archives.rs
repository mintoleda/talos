//! Conservative union of fold snapshots. Canonical JSON identifies supported
//! message copies; repeated occurrences are references, with all source
//! bytes/order retained.
use super::{
    BASE64, Issue, MAX_JSONL_LINE_BYTES, MAX_SESSION_EVENTS, TimelineEventKind, push_issue,
};
use base64::Engine as _;
use serde::Deserialize;
use serde_json::{Value, json, value::RawValue};
use sha2::{Digest, Sha256};
use std::{collections::HashMap, path::Path};

pub(super) struct Merged {
    pub transcript: Vec<u8>,
    pub pairing_groups: Vec<usize>,
    pub provenance: Vec<TimelineEventKind>,
}

#[derive(Deserialize)]
struct Fold {
    #[serde(rename = "type")]
    kind: String,
    messages: Vec<Box<RawValue>>,
}

#[derive(Default)]
struct Merger {
    transcript: Vec<u8>,
    pairing_groups: Vec<usize>,
    provenance: Vec<TimelineEventKind>,
    // The nth identical message in a source segment references the nth canonical
    // occurrence. This preserves repeats within a fold/current transcript while
    // avoiding replay of pinned messages and retries of an interrupted fold.
    occurrences: HashMap<String, Vec<usize>>,
    reused: usize,
    records: usize,
}

impl Merger {
    fn message(
        &mut self,
        raw: &[u8],
        segment: usize,
        counts: &mut HashMap<String, usize>,
    ) -> Result<Value, String> {
        self.budget()?;
        let raw_hash = sha256(raw);
        let message: Option<Value> = serde_json::from_slice(raw).ok();
        let deduplicate = message.as_ref().is_some_and(|value| {
            value.get("type").is_none()
                && matches!(
                    value.get("role").and_then(Value::as_str),
                    Some("user" | "assistant" | "tool")
                )
                && value.get("content").is_some_and(Value::is_array)
        });
        let identity_hash = if deduplicate {
            Some(canonical_json_hash(message.as_ref().ok_or_else(|| {
                "deduplicated message did not parse".to_owned()
            })?)?)
        } else {
            None
        };
        let count_key = identity_hash.as_deref().unwrap_or(&raw_hash).to_owned();
        let ordinal = counts.entry(count_key).or_default();
        let previous = identity_hash
            .as_ref()
            .and_then(|identity| self.occurrences.get(identity))
            .and_then(|rows| rows.get(*ordinal))
            .copied();
        let reused = previous.is_some();
        let record = if let Some(previous) = previous {
            self.reused += 1;
            previous
        } else {
            let record = self.pairing_groups.len() + 1;
            self.transcript.extend_from_slice(raw);
            self.transcript.push(b'\n');
            self.pairing_groups.push(segment);
            if let Some(identity_hash) = &identity_hash {
                self.occurrences
                    .entry(identity_hash.clone())
                    .or_default()
                    .push(record);
            }
            record
        };
        *ordinal += 1;
        Ok(json!({
            "raw_sha256": raw_hash,
            "identity_sha256": identity_hash,
            "occurrence": *ordinal,
            "merged_record": record,
            "reused": reused
        }))
    }

    fn budget(&mut self) -> Result<(), String> {
        self.records += 1;
        if self.records > MAX_SESSION_EVENTS {
            return Err("folded archive/current records exceed the bounded record limit; the entire session was left unimported".into());
        }
        Ok(())
    }

    fn retain(&mut self, kind: &str, raw: &[u8], line: usize, offset: usize, details: &Value) {
        self.provenance.push(TimelineEventKind::Extension {
            kind: kind.into(),
            payload: json!({
                "line": line, "byte_offset": offset,
                // Includes CR/LF, whitespace and malformed/non-UTF8 bytes. Concatenate
                // these in line order to recover the exact selected source file.
                "raw_base64": BASE64.encode(raw), "raw_sha256": sha256(raw),
                "details": details
            }),
        });
    }
}

fn sha256(raw: &[u8]) -> String {
    format!("{:x}", Sha256::digest(raw))
}

fn canonical_json_hash(value: &Value) -> Result<String, String> {
    let mut canonical = Vec::new();
    write_canonical_json(value, &mut canonical)?;
    Ok(sha256(&canonical))
}

fn write_canonical_json(value: &Value, output: &mut Vec<u8>) -> Result<(), String> {
    match value {
        Value::Array(values) => {
            output.push(b'[');
            for (index, item) in values.iter().enumerate() {
                if index > 0 {
                    output.push(b',');
                }
                write_canonical_json(item, output)?;
            }
            output.push(b']');
        }
        Value::Object(object) => {
            let mut entries = object.iter().collect::<Vec<_>>();
            entries.sort_unstable_by_key(|(key, _)| *key);
            output.push(b'{');
            for (index, (key, item)) in entries.into_iter().enumerate() {
                if index > 0 {
                    output.push(b',');
                }
                serde_json::to_writer(&mut *output, key)
                    .map_err(|error| format!("cannot canonicalize message key: {error}"))?;
                output.push(b':');
                write_canonical_json(item, output)?;
            }
            output.push(b'}');
        }
        _ => serde_json::to_writer(&mut *output, value)
            .map_err(|error| format!("cannot canonicalize message value: {error}"))?,
    }
    Ok(())
}

fn record_body(raw: &[u8]) -> Result<&[u8], String> {
    let body = raw.strip_suffix(b"\n").unwrap_or(raw);
    if body.len() > MAX_JSONL_LINE_BYTES {
        return Err("a folded archive/current record exceeds the bounded per-line limit; the entire session was left unimported".into());
    }
    Ok(body.strip_suffix(b"\r").unwrap_or(body))
}

/// Archive append order followed by unmatched current records is a deterministic
/// presentation order, not an assertion of original conversation chronology.
/// The multiset maximum per canonical message hash is a lower bound: distinct identical turns in
/// different folds cannot be distinguished from pinned/failed-fold duplicates.
/// Raw records plus occurrence references preserve that uncertainty losslessly.
pub(super) fn merge(
    current: &[u8],
    archive: &[u8],
    archive_path: &Path,
    issues: &mut Vec<Issue>,
) -> Result<Merged, String> {
    let mut merger = Merger::default();
    let mut offset = 0;
    let mut folds = 0;
    let mut raw_records = 0;
    for (index, raw) in archive.split_inclusive(|byte| *byte == b'\n').enumerate() {
        merger.budget()?;
        let body = record_body(raw)?;
        let fold = serde_json::from_slice::<Fold>(body)
            .ok()
            .filter(|fold| fold.kind == "fold_archive");
        let details = if let Some(fold) = fold {
            folds += 1;
            let mut counts = HashMap::new();
            let references = fold
                .messages
                .iter()
                .map(|message| merger.message(message.get().as_bytes(), index + 1, &mut counts))
                .collect::<Result<Vec<_>, _>>()?;
            json!({"status": "fold_archive", "messages": references})
        } else {
            raw_records += 1;
            json!({"status": "raw_only"})
        };
        merger.retain(
            "legacy_fold_archive_record",
            raw,
            index + 1,
            offset,
            &details,
        );
        offset += raw.len();
    }

    let mut counts = HashMap::new();
    offset = 0;
    for (index, raw) in current.split_inclusive(|byte| *byte == b'\n').enumerate() {
        let body = record_body(raw)?;
        // Match RawValue's range (no surrounding JSON whitespace), while the
        // provenance below preserves the entire physical source record.
        let parsed = serde_json::from_slice::<Box<RawValue>>(body).ok();
        let reference = if body.iter().all(u8::is_ascii_whitespace) {
            merger.budget()?;
            Value::Null
        } else {
            merger.message(
                parsed.as_ref().map_or(body, |raw| raw.get().as_bytes()),
                0,
                &mut counts,
            )?
        };
        merger.retain(
            "legacy_fold_current_record",
            raw,
            index + 1,
            offset,
            &reference,
        );
        offset += raw.len();
    }
    merger.provenance.push(TimelineEventKind::Extension {
        kind: "legacy_fold_merge".into(),
        payload: json!({
            "version": 2, "archive_sha256": sha256(archive), "current_sha256": sha256(current),
            "archive_bytes": archive.len(), "current_bytes": current.len(),
            "fold_records": folds, "raw_only_records": raw_records, "reused_occurrences": merger.reused,
            "presentation_order": "archive_file_then_unmatched_current",
            "exact_chronology": false, "message_identity": "canonical_json_sha256_and_segment_occurrence",
            "raw_record_identity": "raw_sha256",
            "multiplicity": "maximum_per_source_segment; cross_segment_identity_is_ambiguous",
            "tool_pairing": "unique_ids_within_first_source_segment_only",
            "record_reference": "one_based_merged_record_before_timeline_mapping"
        }),
    });
    push_issue(
        issues,
        Issue {
            path: archive_path.to_string_lossy().into_owned(),
            category: "folded_archive_merged".into(),
            message: format!(
                "{folds} fold records merged; {raw_records} records retained raw; {} repeated occurrences retained as provenance references; exact chronology and cross-segment identity are not asserted",
                merger.reused
            ),
        },
    );
    if raw_records > 0 {
        push_issue(
            issues,
            Issue {
                path: archive_path.to_string_lossy().into_owned(),
                category: "folded_archive_raw_records".into(),
                message: "unrecognized, blank or malformed archive records were retained byte-for-byte as raw historical extensions".into(),
            },
        );
    }
    Ok(Merged {
        transcript: merger.transcript,
        pairing_groups: merger.pairing_groups,
        provenance: merger.provenance,
    })
}
