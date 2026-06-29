package panes

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/tui/markdown"
	"github.com/mintoleda/talos/internal/tui/styles"
)

// segment is one styled block of the transcript. text is stored unstyled and
// without surrounding newlines so it can be re-wrapped to the pane width on
// resize; before/after carry the raw (unstyled) newlines that separate blocks.
type segment struct {
	style            lipgloss.Style
	text             string
	before           string
	after            string
	renderAsMarkdown bool // if true, text is rendered through the markdown renderer

	// Rendered markdown cache. Pre-computed when the segment is created
	// and invalidated only on pane resize (mdCacheVersion mismatch).
	renderedMarkdown string
	mdVersion        int

	// Tool-call segments are rendered lazily in body() so they reflow to the
	// current pane width (a pre-styled string could not).
	isTool   bool
	toolName string
	toolArgs map[string]any
	toolOK   bool
}

// ChatModel renders the scrollback transcript for a single agent.
type ChatModel struct {
	vp          viewport.Model
	segments    []segment // finalized turns
	streaming   string    // current in-progress assistant response
	activeTools map[string]string // id -> name of currently-running tools
	sp          spinner.Model
	width       int
	height      int
	md          *markdown.Renderer
	autoscroll  bool // pin viewport to bottom unless user has scrolled up
	dirty       bool // viewport content needs recomputation (body() + SetContent)
	mdCacheVersion int // incremented on SetSize; invalidates per-segment markdown cache
}

func NewChat() ChatModel {
	vp := viewport.New(0, 0)
	vp.KeyMap = viewport.KeyMap{} // disable built-in key bindings; we manage nav ourselves
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return ChatModel{
		vp:             vp,
		md:             markdown.New(80),
		sp:             sp,
		activeTools:    make(map[string]string),
		autoscroll:     true,
		dirty:          true, // first frame needs to set content
		mdCacheVersion: 1,
	}
}

func (c ChatModel) Init() tea.Cmd { return c.sp.Tick }

func (c ChatModel) Update(msg tea.Msg) (ChatModel, tea.Cmd) {
	var cmds []tea.Cmd
	var vpCmd tea.Cmd
	c.vp, vpCmd = c.vp.Update(msg)
	cmds = append(cmds, vpCmd)
	if len(c.activeTools) > 0 {
		var spCmd tea.Cmd
		c.sp, spCmd = c.sp.Update(msg)
		cmds = append(cmds, spCmd)
		// Spinner animation updates every tick; the viewport content must be
		// re-rendered to show the new spinner frame.
		c.dirty = true
	}
	return c, tea.Batch(cmds...)
}

func (c *ChatModel) SetSize(w, h int) {
	c.width, c.height = w, h
	c.vp.Width = w
	c.vp.Height = h
	c.md.SetWidth(w)
	// Invalidate all per-segment markdown caches so body() re-renders at
	// the new width. View() will call body() once, not on every frame.
	c.mdCacheVersion++
	c.dirty = true
}

func (c ChatModel) Width() int  { return c.width }
func (c ChatModel) Height() int { return c.height }

func (c ChatModel) Len() int { return len(c.segments) }

// wrapText soft-wraps plain text to the pane width so long lines reflow instead
// of being truncated by the viewport. Wrapping is done on unstyled text to keep
// reflow's column tracking correct.
func (c ChatModel) wrapText(s string) string {
	if c.width <= 0 {
		return s
	}
	return ansi.Wordwrap(s, c.width, "")
}

// body renders the full transcript (finalized segments plus any in-progress
// streaming response) wrapped to the current width.
//
// Markdown segments are pre-rendered through glamour at creation time; body()
// reuses the cached output. The cache is invalidated only when SetSize changes
// the pane width (via mdCacheVersion). This avoids repeated glamour parse/
// render passes — the primary source of CPU usage during streaming.
func (c ChatModel) body() string {
	var b strings.Builder
	for i := range c.segments {
		s := &c.segments[i]
		b.WriteString(s.before)
		switch {
		case s.isTool:
			b.WriteString(c.renderToolLine(*s))
		case s.renderAsMarkdown:
			if s.renderedMarkdown != "" && s.mdVersion == c.mdCacheVersion {
				// Cache hit — skip the expensive glamour render.
				b.WriteString(s.renderedMarkdown)
			} else {
				// Cache miss (width changed or segment was created
				// without pre-render). Render now and cache for next time.
				rendered := c.md.Render(s.text)
				s.renderedMarkdown = rendered
				s.mdVersion = c.mdCacheVersion
				b.WriteString(rendered)
			}
		default:
			b.WriteString(s.style.Render(c.wrapText(s.text)))
		}
		b.WriteString(s.after)
	}
	if c.streaming != "" {
		// Streaming text changes on every delta; there is no cache.
		b.WriteString(c.md.Render(c.streaming))
	}
	if len(c.activeTools) > 0 {
		b.WriteString("  ")
		b.WriteString(styles.ToolRunningStyle.Render(strings.TrimRight(c.sp.View(), " ")))
		b.WriteString(" ")
		if len(c.activeTools) == 1 {
			for _, name := range c.activeTools {
				b.WriteString(styles.ToolNameStyle.Render(name + "…"))
			}
		} else {
			b.WriteString(styles.ToolNameStyle.Render(fmt.Sprintf("%d tools…", len(c.activeTools))))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (c ChatModel) AddActiveTool(id, name string) ChatModel {
	if c.activeTools == nil {
		c.activeTools = make(map[string]string)
	}
	c.activeTools[id] = name
	c.dirty = true
	return c
}

func (c ChatModel) RemoveActiveTool(id string) ChatModel {
	delete(c.activeTools, id)
	c.dirty = true
	return c
}

// markdownSegment pre-renders a segment's text through glamour so body() can
// use the cached string instead of re-parsing markdown on every frame.
func (c ChatModel) markdownSegment(s segment) segment {
	if s.renderAsMarkdown && s.text != "" && s.renderedMarkdown == "" {
		s.renderedMarkdown = c.md.Render(s.text)
		s.mdVersion = c.mdCacheVersion
	}
	return s
}

func (c ChatModel) append(s segment) ChatModel {
	c.segments = append(c.segments, c.markdownSegment(s))
	c.dirty = true
	return c
}

func (c ChatModel) AppendUserInput(text string) ChatModel {
	return c.append(segment{style: styles.UserStyle, text: "› " + text, before: "\n", after: "\n"})
}

// AppendUserBlocks renders a user message that may contain image blocks.
// Text blocks are shown inline; image blocks appear as "[image]" tags.
func (c ChatModel) AppendUserBlocks(blocks []protocol.ContentBlock) ChatModel {
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case protocol.BlockText:
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		case protocol.BlockImage:
			parts = append(parts, "[image]")
		}
	}
	return c.append(segment{style: styles.UserStyle, text: "› " + strings.Join(parts, " "), before: "\n", after: "\n"})
}

// AppendDelta accumulates streaming text without touching the viewport.
// View() will render it combined with finalized content each frame.
func (c ChatModel) AppendDelta(text string) ChatModel {
	c.streaming += text
	c.dirty = true
	return c
}

// AppendToolUse adds a completed tool-call entry inline in the chat transcript.
// The call descriptor (path/command/query) is derived from the arguments and
// rendered lazily so it reflows on resize; full output lives in the tools pane.
func (c ChatModel) AppendToolUse(name string, args map[string]any, ok bool) ChatModel {
	// Group a run of consecutive tool calls into a compact block: only the
	// first one (preceded by text) gets a blank separator line above it.
	before := "\n"
	if n := len(c.segments); n > 0 && c.segments[n-1].isTool {
		before = ""
	}
	return c.append(segment{
		isTool:   true,
		toolName: name,
		toolArgs: args,
		toolOK:   ok,
		before:   before,
		after:    "\n",
	})
}

// renderToolLine styles a single inline tool entry, indented under the assistant
// text and truncated to the pane width.
func (c ChatModel) renderToolLine(s segment) string {
	icon := "✓"
	style := styles.ToolOKStyle
	if !s.toolOK {
		icon = "✗"
		style = styles.ToolErrorStyle
	}
	width := c.width
	if width < 1 {
		width = 80
	}
	desc := formatToolCall(s.toolName, s.toolArgs)
	return "  " + toolLine(icon, style, s.toolName, desc, width-2, lipgloss.Width(s.toolName))
}

func (c ChatModel) AppendNotice(level, text string) ChatModel {
	style := styles.DimStyle
	switch level {
	case "error":
		style = styles.ErrorStyle
	case "warn":
		style = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	case "think":
		style = styles.ThinkStyle
	}
	return c.append(segment{style: style, text: fmt.Sprintf("[%s] %s", level, text), before: "\n", after: "\n"})
}

func (c ChatModel) AppendAssistantText(text string) ChatModel {
	return c.append(segment{renderAsMarkdown: true, text: text, after: "\n"})
}

// FlushStreaming moves the in-progress streaming text into a finalized segment.
// Call this before inserting a tool-call entry so text before the tool is
// separated from text that follows it.
func (c ChatModel) FlushStreaming() ChatModel {
	if c.streaming == "" {
		return c
	}
	seg := c.markdownSegment(segment{renderAsMarkdown: true, text: c.streaming, after: "\n"})
	c.segments = append(c.segments, seg)
	c.streaming = ""
	c.dirty = true
	return c
}

// PopLastSegment removes the most recently added finalized segment from the
// transcript. Used when a pending steer message is withdrawn (up-arrow) so the
// chat pane doesn't show a message the agent never saw. Returns the updated
// model. No-op if there are no segments.
func (c ChatModel) PopLastSegment() ChatModel {
	if len(c.segments) == 0 {
		return c
	}
	c.segments = c.segments[:len(c.segments)-1]
	c.dirty = true
	return c
}

func (c ChatModel) FinalizeTurn(usage protocol.Usage) ChatModel {
	if c.streaming != "" {
		seg := c.markdownSegment(segment{
			style:            styles.AssistantStyle,
			text:             c.streaming,
			after:            "\n",
			renderAsMarkdown: true,
		})
		c.segments = append(c.segments, seg)
		c.streaming = ""
		c.dirty = true
	}
	return c
}

func (c ChatModel) ScrollDown(n int) ChatModel {
	c.vp.LineDown(n)
	if c.vp.AtBottom() {
		c.autoscroll = true
	}
	return c
}

func (c ChatModel) ScrollUp(n int) ChatModel {
	c.autoscroll = false
	c.vp.LineUp(n)
	return c
}

func (c ChatModel) ScrollTop() ChatModel {
	c.autoscroll = false
	c.vp.GotoTop()
	return c
}

func (c ChatModel) ScrollBottom() ChatModel {
	c.autoscroll = true
	c.vp.GotoBottom()
	return c
}

func (c ChatModel) View() string {
	if c.dirty {
		content := c.body()
		if c.autoscroll {
			c.vp.SetContent(content)
			c.vp.GotoBottom()
		} else {
			// User has scrolled up — preserve their scroll position.
			// Use SetYOffset (which clamps) rather than direct YOffset
			// assignment, because SetContent may change the line count
			// (e.g. resize, or streaming text re-rendering at a different
			// height). An unclamped YOffset produces invalid slice bounds
			// in visibleLines(), causing visual corruption.
			savedOffset := c.vp.YOffset
			c.vp.SetContent(content)
			c.vp.SetYOffset(savedOffset)
		}
		c.dirty = false
	} else if c.autoscroll {
		// No content change, but pin to bottom (e.g. after user hit ScrollBottom).
		c.vp.GotoBottom()
	}
	return c.vp.View()
}
