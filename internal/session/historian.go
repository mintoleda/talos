package session

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/mintoleda/talos/internal/memory"
	"github.com/mintoleda/talos/internal/protocol"
)

type Historian struct {
	Provider provider
	Model    string
	Store    *memory.Store
}

func (h *Historian) Extract(ctx context.Context, msgs []protocol.Message) error {
	if h == nil || h.Provider == nil || h.Store == nil || len(msgs) == 0 {
		return nil
	}
	frozen := make([]protocol.FrozenMessage, len(msgs))
	for i, m := range msgs {
		raw, err := json.Marshal(m)
		if err != nil {
			return err
		}
		frozen[i] = protocol.FrozenMessage{Msg: m, Raw: raw}
	}
	req := protocol.Request{
		System:   "Extract durable project memories from this conversation chunk. Return only a JSON array of objects with category, text, tags, and importance. Categories: architecture, convention, config, preference, context. Return [] if there are no durable facts. Exclude session-local details.",
		Messages: frozen,
		Model:    h.Model,
	}
	stream, err := h.Provider.StreamTurn(ctx, req)
	if err != nil {
		return err
	}
	var raw string
	for ev := range stream {
		switch e := ev.(type) {
		case protocol.PEText:
			raw += e.Text
		case protocol.PEError:
			return e.Err
		}
	}
	var rows []struct {
		Category   string   `json:"category"`
		Text       string   `json:"text"`
		Tags       []string `json:"tags"`
		Importance float64  `json:"importance"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &rows); err != nil {
		return err
	}
	for _, r := range rows {
		text := strings.TrimSpace(r.Text)
		if text == "" || h.duplicate(text) {
			continue
		}
		_, _ = h.Store.Add(memory.Entry{
			Category:   r.Category,
			Text:       text,
			Tags:       r.Tags,
			Importance: r.Importance,
			Source:     "historian",
		})
	}
	return nil
}

func (h *Historian) duplicate(text string) bool {
	want := tokenSet(text)
	for _, e := range h.Store.All() {
		got := tokenSet(e.Text)
		if overlap(want, got) >= 0.8 {
			return true
		}
	}
	return false
}

func (h *Historian) ExtractAsync(msgs []protocol.Message) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		_ = h.Extract(ctx, msgs)
	}()
}

func tokenSet(s string) map[string]bool {
	out := make(map[string]bool)
	for _, f := range strings.Fields(strings.ToLower(s)) {
		if len(f) > 2 {
			out[f] = true
		}
	}
	return out
}

func overlap(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	var hits int
	for k := range a {
		if b[k] {
			hits++
		}
	}
	if len(a) < len(b) {
		return float64(hits) / float64(len(a))
	}
	return float64(hits) / float64(len(b))
}
