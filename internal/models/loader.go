package models

import "strings"

// Entry is a selectable model with its provider.
type Entry struct {
	Provider string
	ID       string
}

// Full returns "provider/id".
func (e Entry) Full() string { return e.Provider + "/" + e.ID }

// Filter returns entries matching all whitespace-separated words in query
// (case-insensitive). Matches against the full "provider/id" string.
func Filter(entries []Entry, query string) []Entry {
	if query == "" {
		return entries
	}
	words := strings.Fields(strings.ToLower(query))
	var out []Entry
	for _, e := range entries {
		haystack := strings.ToLower(e.Full())
		match := true
		for _, w := range words {
			if !strings.Contains(haystack, w) {
				match = false
				break
			}
		}
		if match {
			out = append(out, e)
		}
	}
	return out
}
