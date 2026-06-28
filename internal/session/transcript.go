package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mintoleda/talos/internal/protocol"
)

// CompactionRecord is appended to the JSONL when a chunk of old messages is
// summarized and folded into the immutable prefix. It lets Resume reconstruct
// the same in-memory prompt deterministically.
type CompactionRecord struct {
	Type       string    `json:"type"`
	ChunkIDs   []int     `json:"chunk_ids"`
	Summary    string    `json:"summary"`
	SummaryAt  time.Time `json:"summary_at"`
	MessageRaw []byte    `json:"message_raw"` // canonical assistant/user summary message
}

// StatsRecord is a type-tagged line in the JSONL that snapshots aggregate
// token usage. It is always the last line (appended on session close). When
// a transcript is loaded, the most recent stats record initializes the loop's
// accumulator so /stats carries across restarts.
type StatsRecord struct {
	Type        string `json:"type"`
	Calls       int    `json:"calls"`
	InputTokens int    `json:"input_tokens"`
	CachedTokens int   `json:"cached_tokens"`
	OutputTokens int   `json:"output_tokens"`
}

type Transcript struct {
	mu        sync.Mutex
	f         *os.File
	w         *bufio.Writer
	frozen    []protocol.FrozenMessage
	summaries []protocol.FrozenMessage
	path      string
	lastStats StatsRecord // most recent stats found during Load
}

// Create returns a new Transcript for the given path. The file is not created
// on disk until the first message is appended, so empty sessions never leave
// a trace. Use Load to open an existing transcript.
func Create(path string) (*Transcript, error) {
	return &Transcript{path: path}, nil
}

// lazyOpen is idempotent.
func (t *Transcript) lazyOpen() error {
	if t.f != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(t.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	t.f = f
	t.w = bufio.NewWriter(f)
	return nil
}

func Load(path string) (*Transcript, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	t := &Transcript{f: f, w: bufio.NewWriter(f)}

	readF, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer readF.Close()

	scanner := bufio.NewScanner(readF)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		// Compaction records are distinguished by a `type` field at the top
		// level. They are not protocol.Message values.
		var discriminator struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &discriminator); err == nil && discriminator.Type == "compaction" {
			var rec CompactionRecord
			if err := json.Unmarshal(line, &rec); err != nil {
				return nil, fmt.Errorf("transcript compaction parse: %w", err)
			}
			var m protocol.Message
			if err := json.Unmarshal(rec.MessageRaw, &m); err != nil {
				return nil, fmt.Errorf("transcript compaction message parse: %w", err)
			}
			t.summaries = append(t.summaries, protocol.FrozenMessage{Msg: m, Raw: rec.MessageRaw})
			// Drop the summarized chunk from the in-memory frozen list so resume
			// reconstructs the compacted prompt.
			if len(rec.ChunkIDs) > 0 {
				t.DropOldest(len(rec.ChunkIDs))
			}
			continue
		}
		if err := json.Unmarshal(line, &discriminator); err == nil && discriminator.Type == "stats" {
			var sr StatsRecord
			if err := json.Unmarshal(line, &sr); err != nil {
				return nil, fmt.Errorf("transcript stats parse: %w", err)
			}
			t.lastStats = sr
			continue
		}
		var m protocol.Message
		if err := json.Unmarshal(line, &m); err != nil {
			return nil, fmt.Errorf("transcript parse: %w", err)
		}
		t.frozen = append(t.frozen, protocol.FrozenMessage{Msg: m, Raw: append([]byte(nil), line...)})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *Transcript) Append(m protocol.Message) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.lazyOpen(); err != nil {
		return err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if _, err := t.w.Write(raw); err != nil {
		return err
	}
	if err := t.w.WriteByte('\n'); err != nil {
		return err
	}
	if err := t.w.Flush(); err != nil {
		return err
	}
	t.frozen = append(t.frozen, protocol.FrozenMessage{Msg: m, Raw: raw})
	return nil
}

func (t *Transcript) Frozen() []protocol.FrozenMessage {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]protocol.FrozenMessage, len(t.frozen))
	copy(out, t.frozen)
	return out
}

// Summaries returns the folded summary messages that should appear before the
// live Zone B history in prompt construction.
func (t *Transcript) Summaries() []protocol.FrozenMessage {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]protocol.FrozenMessage, len(t.summaries))
	copy(out, t.summaries)
	return out
}

// AppendCompaction writes a compaction record to disk and folds the summary
// into the in-memory prefix.
func (t *Transcript) AppendCompaction(rec CompactionRecord) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.lazyOpen(); err != nil {
		return err
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := t.w.Write(raw); err != nil {
		return err
	}
	if err := t.w.WriteByte('\n'); err != nil {
		return err
	}
	if err := t.w.Flush(); err != nil {
		return err
	}
	var m protocol.Message
	if err := json.Unmarshal(rec.MessageRaw, &m); err != nil {
		return err
	}
	t.summaries = append(t.summaries, protocol.FrozenMessage{Msg: m, Raw: rec.MessageRaw})
	return nil
}

// WriteStats appends a stats snapshot to the JSONL. It is intended to be
// called before the transcript is closed or swapped out so the next Load
// can restore the accumulator.
func (t *Transcript) WriteStats(sr StatsRecord) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.lazyOpen(); err != nil {
		return err
	}
	sr.Type = "stats"
	raw, err := json.Marshal(sr)
	if err != nil {
		return err
	}
	if _, err := t.w.Write(raw); err != nil {
		return err
	}
	if err := t.w.WriteByte('\n'); err != nil {
		return err
	}
	if err := t.w.Flush(); err != nil {
		return err
	}
	t.lastStats = sr
	return nil
}

// RestoreStats returns the last stats snapshot found when the transcript was
// loaded (zero value if none).
func (t *Transcript) RestoreStats() StatsRecord {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastStats
}

// DropOldest removes the first n messages from the in-memory frozen list. The
// on-disk JSONL remains the complete record.
func (t *Transcript) DropOldest(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n > len(t.frozen) {
		n = len(t.frozen)
	}
	t.frozen = t.frozen[n:]
}

// PrependSummary builds a canonical assistant/user summary pair and adds it to
// the in-memory prefix. It does not touch the on-disk JSONL.
func (t *Transcript) PrependSummary(summary string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := protocol.TextMessage(protocol.RoleAssistant, summary)
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	t.summaries = append(t.summaries, protocol.FrozenMessage{Msg: m, Raw: raw})
	return nil
}

func (t *Transcript) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.w != nil {
		if err := t.w.Flush(); err != nil {
			return err
		}
	}
	if t.f != nil {
		path := t.f.Name()
		if err := t.f.Close(); err != nil {
			return err
		}
		// Remove the file if no messages were ever recorded — empty sessions
		// should not persist on disk. This covers both brand-new sessions where
		// the user exited before typing anything, and sessions that were loaded
		// but had zero messages (unlikely, but safe).
		if len(t.frozen) == 0 && len(t.summaries) == 0 {
			_ = os.Remove(path)
			// Also remove the parent directory if it becomes empty.
			if parent := filepath.Dir(path); parent != "." {
				entries, _ := os.ReadDir(parent)
				if len(entries) == 0 {
					_ = os.Remove(parent)
				}
			}
		}
	}
	return nil
}
