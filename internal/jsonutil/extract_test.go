package jsonutil

import "testing"

func TestExtractJSONArrayRaw(t *testing.T) {
	got, err := ExtractJSONArray(`[{"id":"1"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if got != `[{"id":"1"}]` {
		t.Fatalf("got %q", got)
	}
}

func TestExtractJSONArrayFenced(t *testing.T) {
	got, err := ExtractJSONArray("```json\n[{\"id\":\"1\"}]\n```")
	if err != nil {
		t.Fatal(err)
	}
	if got != `[{"id":"1"}]` {
		t.Fatalf("got %q", got)
	}
}

func TestExtractJSONArrayWithPreamble(t *testing.T) {
	got, err := ExtractJSONArray("Here is the JSON:\n[{\"id\":\"1\"}]\n")
	if err != nil {
		t.Fatal(err)
	}
	if got != `[{"id":"1"}]` {
		t.Fatalf("got %q", got)
	}
}

func TestUnmarshalArrayFromText(t *testing.T) {
	var rows []struct {
		ID string `json:"id"`
	}
	if err := UnmarshalArrayFromText("```json\n[{\"id\":\"1\"}]\n```", &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "1" {
		t.Fatalf("rows=%#v", rows)
	}
}
