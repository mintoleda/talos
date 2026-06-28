// Package jsonutil provides JSON helpers that guarantee deterministic output.
package jsonutil

import (
	"bytes"
	"encoding/json"
	"sort"
)

// MarshalDeterministic returns the JSON encoding of v with map keys sorted
// lexicographically at every level. This guarantees bit-identical output
// across runs, which is critical for prefix-cache hit rates on providers
// that match prompts byte-for-byte.
func MarshalDeterministic(v any) ([]byte, error) {
	normalized := normalize(v)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "")
	if err := enc.Encode(normalized); err != nil {
		return nil, err
	}
	// json.Encoder adds a trailing newline; strip it.
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// MustMarshalDeterministic is like MarshalDeterministic but panics on error.
func MustMarshalDeterministic(v any) []byte {
	b, err := MarshalDeterministic(v)
	if err != nil {
		panic(err)
	}
	return b
}

func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(x))
		for _, k := range keys {
			out[k] = normalize(x[k])
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e)
		}
		return out
	case []map[string]any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e)
		}
		return out
	default:
		return x
	}
}
