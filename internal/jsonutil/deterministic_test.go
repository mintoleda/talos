package jsonutil

import (
	"testing"
)

func TestMarshalDeterministic(t *testing.T) {
	v := map[string]any{
		"z": "last",
		"a": "first",
		"m": map[string]any{
			"c": 3,
			"a": 1,
			"b": 2,
		},
		"arr": []any{
			map[string]any{"y": 2, "x": 1},
			"plain",
		},
	}

	got, err := MarshalDeterministic(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	want := `{"a":"first","arr":[{"x":1,"y":2},"plain"],"m":{"a":1,"b":2,"c":3},"z":"last"}`
	if string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}

	got2, err := MarshalDeterministic(v)
	if err != nil {
		t.Fatalf("marshal 2: %v", err)
	}
	if string(got) != string(got2) {
		t.Errorf("non-deterministic: %s vs %s", got, got2)
	}
}

func TestMarshalDeterministic_NilAndEmpty(t *testing.T) {
	empty, err := MarshalDeterministic(map[string]any{})
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if string(empty) != `{}` {
		t.Errorf("empty map: got %s", empty)
	}
}
