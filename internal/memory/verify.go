package memory

import (
	"encoding/json"
	"os"
	"sync"
)

// strikeRecord tracks consecutive flagged-flag counts per entry.
// Stored alongside the store so strikes survive restarts.
type strikeRecord struct {
	Strikes map[string]int `json:"strikes"`
}

// StrikePath returns the path to the strikes file for a given store path.
func StrikePath(storePath string) string {
	return storePath + ".strikes.json"
}

func loadStrikes(path string) map[string]int {
	out := make(map[string]int)
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var sr strikeRecord
	if err := json.Unmarshal(data, &sr); err != nil {
		return out
	}
	return sr.Strikes
}

func saveStrikes(path string, strikes map[string]int) error {
	sr := strikeRecord{Strikes: strikes}
	data, err := json.MarshalIndent(sr, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// Verifier holds state for periodically re-checking high-importance
// agent-written memories for staleness across process restarts.
type Verifier struct {
	storePath  string
	strikes    map[string]int
	mu         sync.Mutex
}

// NewVerifier creates a verifier for the given store path.
func NewVerifier(storePath string) *Verifier {
	return &Verifier{
		storePath: storePath,
		strikes:   loadStrikes(StrikePath(storePath)),
	}
}

// Flag marks an entry as suspect, incrementing its strike count.
// It returns the new strike count. When the count reaches 3, Flag
// also resets the strike (so the cycle can repeat if needed) and
// returns true to indicate the entry should be permanently downgraded.
func (v *Verifier) Flag(id string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.strikes[id]++
	n := v.strikes[id]
	if n >= 3 {
		delete(v.strikes, id)
		_ = saveStrikes(StrikePath(v.storePath), v.strikes)
		return n
	}
	_ = saveStrikes(StrikePath(v.storePath), v.strikes)
	return n
}

// Clear resets the strike count for an entry (e.g. after manual update).
func (v *Verifier) Clear(id string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.strikes, id)
	_ = saveStrikes(StrikePath(v.storePath), v.strikes)
}

// Strikes returns the current strike count for an entry.
func (v *Verifier) Strikes(id string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.strikes[id]
}
