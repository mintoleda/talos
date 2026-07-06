package jsonutil

import (
	"encoding/json"
	"fmt"
	"strings"
)

// UnmarshalArrayFromText decodes a JSON array from model output. It accepts a
// raw array or a Markdown fenced json block, and rejects non-array payloads.
func UnmarshalArrayFromText[T any](text string, out *[]T) error {
	payload, err := ExtractJSONArray(text)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(payload), out)
}

func ExtractJSONArray(text string) (string, error) {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSpace(s)
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	if strings.HasPrefix(s, "[") {
		return s, nil
	}
	start := strings.IndexByte(s, '[')
	end := strings.LastIndexByte(s, ']')
	if start >= 0 && end > start {
		return strings.TrimSpace(s[start : end+1]), nil
	}
	return "", fmt.Errorf("no JSON array in model output")
}
