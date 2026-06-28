package markdown

import (
	"strings"

	"github.com/charmbracelet/glamour"
)

// Renderer renders markdown text to ANSI-styled terminal output.
type Renderer struct {
	width int
	r     *glamour.TermRenderer
}

// New creates a Renderer that word-wraps to the given width.
// If width <= 0 a reasonable default (80) is used.
func New(width int) *Renderer {
	w := width
	if w <= 0 {
		w = 80
	}
	// Use the "dark" built-in style which looks good on dark terminals.
	// Glamour ships with "dark", "light", and "notty" (no TTY codes).
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(w),
	)
	if err != nil {
		// Fallback – return a no-op renderer.
		return &Renderer{width: w}
	}
	return &Renderer{width: w, r: r}
}

// Render renders markdown text to an ANSI-styled string.
func (ren *Renderer) Render(markdown string) string {
	if ren.r == nil {
		return markdown
	}
	out, err := ren.r.Render(markdown)
	if err != nil {
		return markdown
	}
	// Glamour adds a trailing newline; strip it so callers can compose.
	out = strings.TrimSuffix(out, "\n")
	return out
}

// RenderInline renders a single line of markdown (e.g. for streaming).
// Unlike Render, it does not wrap the output in a paragraph block so it
// can be appended to incrementally.
func (ren *Renderer) RenderInline(markdown string) string {
	if ren.r == nil {
		return markdown
	}
	out, err := ren.r.Render(markdown)
	if err != nil {
		return markdown
	}
	// Glamour wraps in <paragraph>…</paragraph> by default; for inline
	// streaming we strip trailing newline.
	out = strings.TrimSuffix(out, "\n")
	return out
}

// SetWidth updates the word-wrap width. Rendering a new width requires
// creating a new TermRenderer, so this is a full rebuild.
func (ren *Renderer) SetWidth(width int) {
	if width == ren.width || width <= 0 {
		return
	}
	ren.width = width
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return
	}
	ren.r = r
}
