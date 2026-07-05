package tools

import (
	"encoding/json"
	"time"

	"github.com/mintoleda/talos/internal/protocol"
)

type Registry struct {
	tools []Tool
	bg    *BackgroundRegistry
}

// BashConfig carries the bash tool's tunables so they flow from config rather
// than being hardcoded. Zero values fall back to NewBash's defaults.
type BashConfig struct {
	DefaultTimeout time.Duration
	MaxTimeout     time.Duration
	MaxOutput      int
}

func DefaultRegistry(cwd string, reads *ReadSet, bash BashConfig, searchURL string) *Registry {
	bg := NewBackgroundRegistry(cwd)
	idx := ensureFFFIndex(cwd)
	return &Registry{
		tools: []Tool{
			NewRead(reads, idx),
			NewWrite(reads),
			NewEdit(reads),
			NewBash(cwd, bash.DefaultTimeout, bash.MaxTimeout, bash.MaxOutput, reads),
			NewSearch(cwd),
			NewFind(cwd),
			NewLs(),
			NewBashBackground(cwd, bg),
			NewBashReadOutput(bg),
			NewBashKill(bg),
			NewWebSearch(WebSearchConfig{SearchURL: searchURL}),
			NewWebFetch(WebFetchConfig{}),
		},
		bg: bg,
	}
}

func (r *Registry) KillBg() {
	if r.bg != nil {
		r.bg.KillAll()
	}
}

func (r *Registry) Close() {
	r.KillBg()
}

func EmptyRegistry() *Registry {
	return &Registry{tools: nil}
}

// Add appends tools to the registry. Used to inject tools constructed outside
// the tools package (e.g. subagent spawn tools) without import cycles.
func (r *Registry) Add(extra ...Tool) {
	r.tools = append(r.tools, extra...)
}

// Filter returns a new registry containing only the tools whose names appear in
// allow. The special entries "*" and "all" keep every tool. The returned
// registry shares the background-process registry so Close still cleans up.
func (r *Registry) Filter(allow []string) *Registry {
	for _, a := range allow {
		if a == "*" || a == "all" {
			return &Registry{tools: append([]Tool(nil), r.tools...), bg: r.bg}
		}
	}
	want := make(map[string]bool, len(allow))
	for _, a := range allow {
		want[a] = true
	}
	var kept []Tool
	for _, t := range r.tools {
		if want[t.Name()] {
			kept = append(kept, t)
		}
	}
	return &Registry{tools: kept, bg: r.bg}
}

func (r *Registry) Get(name string) (Tool, bool) {
	for _, t := range r.tools {
		if t.Name() == name {
			return t, true
		}
	}
	return nil, false
}

func (r *Registry) Schemas() []protocol.ToolSchema {
	out := make([]protocol.ToolSchema, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, protocol.ToolSchema{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Schema(),
		})
	}
	return out
}

func (r *Registry) All() []Tool {
	return r.tools
}

func EmptySchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
