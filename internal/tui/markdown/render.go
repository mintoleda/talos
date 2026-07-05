package markdown

import (
	"strings"

	"github.com/charmbracelet/glamour"
)

type Renderer struct {
	width int
	r     *glamour.TermRenderer
}

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

func (ren *Renderer) SetWidth(width int) {
	// Rendering a new width requires a new TermRenderer (glamour limitation).
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
