package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

type fileStamp struct {
	hash  string
	mtime time.Time
}

// ReadSet tracks which files have been read in this session and whether the
// on-disk version is still the one we saw. The edit and write tools gate on
// SeenAndFresh, so a model cannot mutate a file it has not actually opened.
//
// ReadSet is concurrency-safe. The optional savePath enables on-disk
// persistence: every Update flushes the in-memory state to JSON, and Load
// rehydrates it. Order is tracked so RecentPaths can return the most-recently
// read files for the system-reminder footer.
type ReadSet struct {
	mu       sync.Mutex
	seen     map[string]fileStamp
	order    []string         // read order, oldest first; re-reads move to end
	indexOf  map[string]int   // path -> index in order, or -1
	limit    int64
	savePath string           // optional; if set, Update persists
}

// NewReadSet returns an empty ReadSet. The hash limit (256KB) is the largest
// chunk of a file we hash for staleness detection; files larger than that
// use a partial hash plus a "-partial" suffix to disambiguate.
func NewReadSet() *ReadSet {
	return &ReadSet{
		seen:    make(map[string]fileStamp),
		indexOf: make(map[string]int),
		limit:   256 * 1024,
	}
}

type readSetSnapshot struct {
	Version int      `json:"version"`
	Paths   []string `json:"paths"`
	Stamps  map[string]fileStampJSON `json:"stamps"`
}

type fileStampJSON struct {
	Hash  string `json:"hash"`
	MTime int64  `json:"mtime_unix_nano"`
}

const readSetVersion = 1

// LoadReadSet reads a previously-saved ReadSet from path. A missing file
// returns an empty set, nil — fresh sessions have nothing to restore. A
// corrupt file returns the error so the caller can decide whether to abort
// or fall back to empty.
func LoadReadSet(path string) (*ReadSet, error) {
	rs := NewReadSet()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			rs.savePath = path
			return rs, nil
		}
		return nil, fmt.Errorf("read readset %s: %w", path, err)
	}
	var snap readSetSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("parse readset %s: %w", path, err)
	}
	if snap.Version != readSetVersion {
		return nil, fmt.Errorf("readset %s: unsupported version %d", path, snap.Version)
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, p := range snap.Paths {
		js, ok := snap.Stamps[p]
		if !ok {
			continue
		}
		rs.seen[p] = fileStamp{
			hash:  js.Hash,
			mtime: time.Unix(0, js.MTime),
		}
		rs.indexOf[p] = len(rs.order)
		rs.order = append(rs.order, p)
	}
	rs.savePath = path
	return rs, nil
}

// SetSavePath enables on-disk persistence. Subsequent Updates will rewrite
// the file at path. Pass "" to disable persistence without losing state.
func (r *ReadSet) SetSavePath(path string) {
	r.mu.Lock()
	r.savePath = path
	r.mu.Unlock()
}

// Save flushes the current set to disk atomically (write-temp + rename) if a
// save path has been set. Safe to call from any goroutine; serialised on r.mu.
func (r *ReadSet) Save() error {
	r.mu.Lock()
	path := r.savePath
	if path == "" {
		r.mu.Unlock()
		return nil
	}
	snap := readSetSnapshot{
		Version: readSetVersion,
		Paths:   append([]string(nil), r.order...),
		Stamps:  make(map[string]fileStampJSON, len(r.seen)),
	}
	for p, s := range r.seen {
		snap.Stamps[p] = fileStampJSON{
			Hash:  s.hash,
			MTime: s.mtime.UnixNano(),
		}
	}
	r.mu.Unlock()

	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal readset: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write readset tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename readset: %w", err)
	}
	return nil
}

func (r *ReadSet) hashFile(path string) (string, time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", time.Time{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", time.Time{}, err
	}
	if int64(len(data)) > r.limit {
		sum := sha256.Sum256(data[:r.limit])
		return hex.EncodeToString(sum[:]) + "-partial", info.ModTime(), nil
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), info.ModTime(), nil
}

// SeenAndFresh reports whether path was read in this session and the on-disk
// version is still byte-identical (or, for large files, partial-hash + mtime
// identical) to the version we saw.
func (r *ReadSet) SeenAndFresh(path string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	fs, ok := r.seen[path]
	if !ok {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.ModTime() != fs.mtime {
		return false
	}
	hash, mtime, err := r.hashFile(path)
	if err != nil {
		return false
	}
	return hash == fs.hash && mtime == fs.mtime
}

// Update records a fresh read of path. If the path was already in the set,
// it is moved to the end of the read order so RecentPaths surfaces it as
// most-recently-read.
func (r *ReadSet) Update(path string) error {
	r.mu.Lock()
	hash, mtime, err := r.hashFile(path)
	if err != nil {
		r.mu.Unlock()
		return fmt.Errorf("hash %s: %w", path, err)
	}
	r.seen[path] = fileStamp{hash: hash, mtime: mtime}
	if idx, ok := r.indexOf[path]; ok {
		// Already present: remove from current position.
		r.order = append(r.order[:idx], r.order[idx+1:]...)
		// Reindex everything after the removed element.
		for i := idx; i < len(r.order); i++ {
			r.indexOf[r.order[i]] = i
		}
	}
	r.indexOf[path] = len(r.order)
	r.order = append(r.order, path)
	savePath := r.savePath
	r.mu.Unlock()

	if savePath != "" {
		if err := r.Save(); err != nil {
			return err
		}
	}
	return nil
}

func (r *ReadSet) Record(path string) error {
	return r.Update(path)
}

func (r *ReadSet) MarkStale(path string) {
	r.mu.Lock()
	delete(r.seen, path)
	if idx, ok := r.indexOf[path]; ok {
		r.order = append(r.order[:idx], r.order[idx+1:]...)
		for i := idx; i < len(r.order); i++ {
			r.indexOf[r.order[i]] = i
		}
		delete(r.indexOf, path)
	}
	r.mu.Unlock()
}

func (r *ReadSet) MarkStaleBatch(paths []string) {
	r.mu.Lock()
	for _, path := range paths {
		delete(r.seen, path)
		if idx, ok := r.indexOf[path]; ok {
			r.order = append(r.order[:idx], r.order[idx+1:]...)
			for i := idx; i < len(r.order); i++ {
				r.indexOf[r.order[i]] = i
			}
			delete(r.indexOf, path)
		}
	}
	r.mu.Unlock()
}

func (r *ReadSet) WasSeen(path string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.seen[path]
	return ok
}

func (r *ReadSet) RecentPaths(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || len(r.order) == 0 {
		return nil
	}
	if n > len(r.order) {
		n = len(r.order)
	}
	out := make([]string, n)
	// order is oldest-first; reverse the last n entries.
	for i := 0; i < n; i++ {
		out[i] = r.order[len(r.order)-1-i]
	}
	return out
}

func (r *ReadSet) AllPaths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.order))
	out = append(out, r.order...)
	sort.Strings(out)
	return out
}

func (r *ReadSet) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.order)
}
