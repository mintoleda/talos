package memory

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
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

func (s *Store) TopN(n int, budgetBytes int) []Entry {
	all := s.All()
	sort.Slice(all, func(i, j int) bool {
		if all[i].Importance != all[j].Importance {
			return all[i].Importance > all[j].Importance
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
