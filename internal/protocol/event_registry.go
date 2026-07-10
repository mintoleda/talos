package protocol

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// EventRegistry maps wire names (snake_case) to Go event types.
// It is used by both encode and decode so they cannot drift.
var (
	// eventNameByGoType maps Go type name → wire event type name.
	eventNameByGoType = map[string]string{
		"UserInput":             "user_input",
		"ModelChanged":           "model_changed",
		"PermissionModeChanged": "permission_mode_changed",
		"TextDelta":              "text_delta",
		"ThinkingDelta":          "thinking_delta",
		"ThinkingBlock":          "thinking_block",
		"ToolStarted":            "tool_started",
		"ToolFinished":           "tool_finished",
		"ToolOutputDelta":        "tool_output_delta",
		"Notice":                 "notice",
		"TurnEnded":              "turn_ended",
		"PermissionRequested":    "permission_requested",
		"ApprovalResolved":       "approval_resolved",
		"EngineSnapshot":         "engine_snapshot",
		"SessionStatus":          "session_status",
		"BatchStarted":           "batch_started",
		"BatchFinished":          "batch_finished",
		"PromptEstimate":         "prompt_estimate",
		"SubagentStarted":        "subagent_started",
		"SubagentEvent":          "subagent_event",
		"SubagentFinished":       "subagent_finished",
		"BgStarted":              "bg_started",
		"BgOutput":               "bg_output",
		"BgExited":               "bg_exited",
	}

	// eventTypeByWireName maps wire name → zero-value instance for decode.
	// Built from eventNameByGoType via reflection at init.
	eventTypeByWireName map[string]Event

	// eventTypeToWire is the reverse: reflect.Type → wire name.
	eventTypeToWire map[reflect.Type]string
)

func init() {
	eventTypeByWireName = make(map[string]Event, len(eventNameByGoType))
	eventTypeToWire = make(map[reflect.Type]string, len(eventNameByGoType))

	// All event types that implement isEvent().
	all := []Event{
		UserInput{},
		ModelChanged{},
		PermissionModeChanged{},
		TextDelta{},
		ThinkingDelta{},
		ThinkingBlock{},
		ToolStarted{},
		ToolFinished{},
		ToolOutputDelta{},
		Notice{},
		TurnEnded{},
		PermissionRequested{},
		ApprovalResolved{},
		EngineSnapshot{},
		SessionStatus{},
		BatchStarted{},
		BatchFinished{},
		PromptEstimate{},
		SubagentStarted{},
		SubagentEvent{},
		SubagentFinished{},
		BgStarted{},
		BgOutput{},
		BgExited{},
	}

	for _, ev := range all {
		t := reflect.TypeOf(ev)
		name := t.Name()
		wire, ok := eventNameByGoType[name]
		if !ok {
			panic(fmt.Sprintf("protocol: missing wire name for event type %s", name))
		}
		eventTypeByWireName[wire] = ev
		eventTypeToWire[t] = wire
	}
}

// EventName returns the wire name for an event type.
// Returns "" for unknown types.
func EventName(e Event) string {
	t := reflect.TypeOf(e)
	// Pointer receivers (e.g. &SubagentEvent{}) also work.
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return eventTypeToWire[t]
}

// NewEvent creates a zero-value Event of the type identified by wireName.
// Returns nil if wireName is unknown.
func NewEvent(wireName string) Event {
	ev, ok := eventTypeByWireName[wireName]
	if !ok {
		return nil
	}
	// Create a fresh copy via reflection (ev is a value, not a pointer).
	return reflect.New(reflect.TypeOf(ev)).Elem().Interface().(Event)
}

// MarshalEvent serializes an event to its wire representation.
// Returns the wire type name and the JSON-encoded bytes.
// For SubagentEvent, the Inner field is encoded recursively with the
// {"etype": "...", "event": {...}} pattern so the receiver can decode it.
func MarshalEvent(e Event) (etype string, raw []byte, err error) {
	etype = EventName(e)
	if etype == "" {
		return "", nil, fmt.Errorf("protocol: unknown event type %T", e)
	}

	// Special handling for SubagentEvent: encode Inner recursively.
	if se, ok := e.(SubagentEvent); ok {
		return marshalSubagentEvent(se)
	}

	raw, err = json.Marshal(e)
	return
}

// marshalSubagentEvent handles the recursive encoding of SubagentEvent.Inner.
func marshalSubagentEvent(se SubagentEvent) (etype string, raw []byte, err error) {
	etype = "subagent_event"

	// Encode the inner event recursively.
	innerType, innerRaw, err := MarshalEvent(se.Inner)
	if err != nil {
		return "", nil, fmt.Errorf("subagent inner event: %w", err)
	}

	// Build the outer JSON with the inner event as a tagged object.
	outer := struct {
		ID    string          `json:"id"`
		Agent string          `json:"agent"`
		Inner json.RawMessage `json:"inner"`
	}{
		ID:    se.ID,
		Agent: se.Agent,
		Inner: mustTagEvent(innerType, innerRaw),
	}

	raw, err = json.Marshal(outer)
	return
}

// UnmarshalEvent reconstructs an Event from its wire representation.
// For SubagentEvent, the Inner field is decoded recursively.
func UnmarshalEvent(etype string, raw json.RawMessage) (Event, error) {
	switch etype {
	case "subagent_event":
		return unmarshalSubagentEvent(raw)
	default:
		ev := NewEvent(etype)
		if ev == nil {
			return nil, fmt.Errorf("unknown event type %q", etype)
		}
		ptr := reflect.New(reflect.TypeOf(ev))
		if err := json.Unmarshal(raw, ptr.Interface()); err != nil {
			return nil, err
		}
		return ptr.Elem().Interface().(Event), nil
	}
}

// unmarshalSubagentEvent decodes a SubagentEvent with its nested Inner event.
func unmarshalSubagentEvent(raw json.RawMessage) (Event, error) {
	var outer struct {
		ID    string          `json:"id"`
		Agent string          `json:"agent"`
		Inner json.RawMessage `json:"inner"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		return nil, err
	}

	// Decode the inner tagged event.
	var tagged struct {
		EType string          `json:"etype"`
		Event json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal(outer.Inner, &tagged); err != nil {
		return nil, fmt.Errorf("subagent inner tag: %w", err)
	}

	inner, err := UnmarshalEvent(tagged.EType, tagged.Event)
	if err != nil {
		return nil, fmt.Errorf("subagent inner: %w", err)
	}

	return SubagentEvent{
		ID:    outer.ID,
		Agent: outer.Agent,
		Inner: inner,
	}, nil
}

// mustTagEvent wraps raw bytes as {"etype": name, "event": raw}.
func mustTagEvent(etype string, raw []byte) json.RawMessage {
	tagged, err := json.Marshal(struct {
		EType string          `json:"etype"`
		Event json.RawMessage `json:"event"`
	}{EType: etype, Event: raw})
	if err != nil {
		// This should never fail for these simple types.
		panic(fmt.Sprintf("protocol: tag event: %v", err))
	}
	return json.RawMessage(tagged)
}
