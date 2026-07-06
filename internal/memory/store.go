package memory

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Entry struct {
	ID         string    `json:"id"`
	Category   string    `json:"category"`
	Text       string    `json:"text"`
	Tags       []string  `json:"tags,omitempty"`
	Importance float64   `json:"importance"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Source     string    `json:"source"`
	Uses       int       `json:"uses,omitempty"`
}

type record struct {
	Entry
	Deleted bool `json:"deleted,omitempty"`
}

type Store struct {
	path    string
	mu      sync.Mutex
	entries map[string]Entry
	order   []string
}

func StorePath(baseDir, projectID string) string {
	return filepath.Join(baseDir, "memory", projectID, "store.jsonl")
}

func Open(baseDir, projectID string) (*Store, error) {
	path := StorePath(baseDir, projectID)
	if err := migrateLegacy(baseDir, path); err != nil {
		return nil, err
	}
	s := &Store{path: path, entries: make(map[string]Entry)}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil || r.ID == "" {
			continue
		}
		if r.Deleted {
			delete(s.entries, r.ID)
			continue
		}
		if _, ok := s.entries[r.ID]; !ok {
			s.order = append(s.order, r.ID)
		}
		s.entries[r.ID] = r.Entry
	}
	return s, sc.Err()
}

func (s *Store) Add(e Entry) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if e.ID == "" {
		e.ID = randomID()
	}
	if e.Category == "" {
		e.Category = "context"
	}
	if e.Importance <= 0 {
		e.Importance = 0.5
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	e.UpdatedAt = now
	if e.Source == "" {
		e.Source = "agent"
	}
	if err := s.appendRecord(record{Entry: e}); err != nil {
		return Entry{}, err
	}
	if _, ok := s.entries[e.ID]; !ok {
		s.order = append(s.order, e.ID)
	}
	s.entries[e.ID] = e
	return e, nil
}

func (s *Store) Update(id string, fn func(*Entry)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return os.ErrNotExist
	}
	fn(&e)
	e.UpdatedAt = time.Now().UTC()
	if err := s.appendRecord(record{Entry: e}); err != nil {
		return err
	}
	s.entries[id] = e
	return nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.appendRecord(record{Entry: Entry{ID: id}, Deleted: true}); err != nil {
		return err
	}
	delete(s.entries, id)
	return nil
}

func (s *Store) All() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, 0, len(s.entries))
	for _, id := range s.order {
		if e, ok := s.entries[id]; ok {
			out = append(out, e)
		}
	}
	return out
}

func (s *Store) Search(query string, limit int) []Entry {
	q := tokens(query)
	if limit <= 0 {
		limit = 10
	}
	type scored struct {
		e     Entry
		score float64
	}
	var rows []scored
	for _, e := range s.All() {
		score := scoreEntry(e, q)
		if score > 0 {
			rows = append(rows, scored{e: e, score: score})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].score > rows[j].score })
	if len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]Entry, len(rows))
	for i, r := range rows {
		out[i] = r.e
		_ = s.Update(r.e.ID, func(e *Entry) { e.Uses++ })
	}
	return out
}

// effectiveImportance applies a time-based decay so older entries lose
// visibility over time. The half-life is 30 days — an entry's effective
// importance halves every 30 days since its last update.
func effectiveImportance(e Entry, now time.Time) float64 {
	age := now.Sub(e.UpdatedAt)
	halfLife := 30 * 24 * time.Hour
	return e.Importance * math.Pow(0.5, age.Hours()/halfLife.Hours())
}

func (s *Store) TopN(n int, budgetBytes int) []Entry {
	all := s.All()
	now := time.Now()
	sort.Slice(all, func(i, j int) bool {
		ei := effectiveImportance(all[i], now)
		ej := effectiveImportance(all[j], now)
		if ei != ej {
			return ei > ej
		}
		return all[i].UpdatedAt.After(all[j].UpdatedAt)
	})
	var out []Entry
	var used int
	for _, e := range all {
		if n > 0 && len(out) >= n {
			break
		}
		used += len(e.Text) + len(e.ID) + len(e.Category) + 8
		if budgetBytes > 0 && used > budgetBytes {
			break
		}
		out = append(out, e)
	}
	return out
}

func (s *Store) Compact() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, id := range s.order {
		if e, ok := s.entries[id]; ok {
			if err := enc.Encode(record{Entry: e}); err != nil {
				f.Close()
				return err
			}
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) appendRecord(r record) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(r)
}

// compactionFilePath returns the path to the file tracking compaction counts.
func compactionFilePath(storePath string) string {
	return storePath + ".compactions"
}

// CompactCount reads the persistent compaction count from disk.
func (s *Store) CompactCount() int {
	data, err := os.ReadFile(compactionFilePath(s.path))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return n
}

// IncrementCompactions bumps the compaction count by 1.
func (s *Store) IncrementCompactions() error {
	n := s.CompactCount() + 1
	return os.WriteFile(compactionFilePath(s.path), []byte(strconv.Itoa(n)+"\n"), 0o600)
}

// ResetCompactions resets the counter to zero (called after dream).
func (s *Store) ResetCompactions() error {
	return os.WriteFile(compactionFilePath(s.path), []byte("0\n"), 0o600)
}

// CompactionNudgeNeeded returns true if enough compactions have accumulated
// since the last dream run to warrant a nudge. Threshold is 50 by default.
func (s *Store) CompactionNudgeNeeded(threshold int) bool {
	if threshold <= 0 {
		threshold = 50
	}
	return s.CompactCount() >= threshold
}

func migrateLegacy(baseDir, storePath string) error {
	legacy := Path(baseDir)
	if _, err := os.Stat(storePath); err == nil {
		return nil
	}
	data, err := os.ReadFile(legacy)
	if err != nil {
		return nil
	}
	s := &Store{path: storePath, entries: make(map[string]Entry)}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, err := s.Add(Entry{Category: "context", Text: line, Importance: 0.5, Source: "user"}); err != nil {
			return err
		}
	}
	return os.Rename(legacy, legacy+".imported")
}

func randomID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		h := fnv.New64a()
		_, _ = h.Write([]byte(time.Now().String()))
		return hex.EncodeToString(h.Sum(nil))
	}
	return hex.EncodeToString(b[:])
}

func tokens(s string) map[string]bool {
	out := make(map[string]bool)
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return r < 'a' || r > 'z'
	}) {
		if len(f) > 1 {
			out[f] = true
		}
	}
	return out
}

func scoreEntry(e Entry, q map[string]bool) float64 {
	if len(q) == 0 {
		return e.Importance
	}
	hay := tokens(e.Text + " " + strings.Join(e.Tags, " "))
	var hits int
	for tok := range q {
		if hay[tok] {
			hits++
		}
	}
	if hits == 0 {
		return 0
	}
	recency := 1.0
	if !e.UpdatedAt.IsZero() {
		recency = 1 / (1 + time.Since(e.UpdatedAt).Hours()/24/365)
	}
	return float64(hits) * (0.5 + e.Importance) * recency * (1 + float64(e.Uses)/10)
}

// polarityWords maps words to a rough semantic valence. Used by
// HasContradictingEntries to detect likely contradictions between
// two entries in the same category sharing overlapping tags.
var polarityWords = map[string]float64{
	"uses": 1, "is": 1, "does": 1, "are": 1, "was": 1, "were": 1,
	"will": 1, "should": 1, "must": 1, "required": 1, "requires": 1,
	"supports": 1, "enabled": 1, "preferred": 1, "recommended": 1,
	"always": 1, "default": 1, "yes": 1,

	"doesn't": -1, "isn't": -1, "aren't": -1, "won't": -1,
	"shouldn't": -1, "mustn't": -1, "cannot": -1, "can't": -1,
	"not": -1, "no": -1, "never": -1, "avoid": -1, "deprecated": -1,
	"discouraged": -1, "disabled": -1, "removed": -1, "unsupported": -1,
}

// entryPolarity scores the semantic valence of an entry's text.
// Returns a number in [-1, 1].
func entryPolarity(text string) float64 {
	var score float64
	var count int
	for _, f := range strings.Fields(strings.ToLower(text)) {
		if v, ok := polarityWords[f]; ok {
			score += v
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return score / float64(count)
}

// HasContradictingEntries checks whether two entries in the same category
// have overlapping tags and opposite semantic polarity, which suggests
// a contradiction. Returns true if they likely conflict.
func HasContradictingEntries(a, b Entry) bool {
	if a.Category != b.Category || a.ID == b.ID {
		return false
	}
	// Require at least one overlapping tag.
	overlap := false
	for _, ta := range a.Tags {
		for _, tb := range b.Tags {
			if ta == tb {
				overlap = true
				break
			}
		}
		if overlap {
			break
		}
	}
	if !overlap {
		return false
	}
	pa := entryPolarity(a.Text)
	pb := entryPolarity(b.Text)
	// Both must have detectable polarity, and they must oppose.
	if pa == 0 || pb == 0 {
		return false
	}
	return (pa > 0 && pb < 0) || (pa < 0 && pb > 0)
}
