package panes

import (
	"testing"

	"github.com/mintoleda/talos/internal/protocol"
)

func TestNewChatIsEmpty(t *testing.T) {
	c := NewChat()
	if c.Len() != 0 {
		t.Fatal("expected empty chat")
	}
}

func TestChatAppendUserInput(t *testing.T) {
	c := NewChat()
	c = c.AppendUserInput("hello world")

	if c.Len() != 1 {
		t.Fatalf("expected 1 segment, got %d", c.Len())
	}
}

func TestChatAppendUserBlocksText(t *testing.T) {
	c := NewChat()
	c = c.AppendUserBlocks([]protocol.ContentBlock{
		{Type: protocol.BlockText, Text: "hello"},
	})

	if c.Len() != 1 {
		t.Fatalf("expected 1 segment, got %d", c.Len())
	}
}

func TestChatAppendUserBlocksImage(t *testing.T) {
	c := NewChat()
	c = c.AppendUserBlocks([]protocol.ContentBlock{
		{Type: protocol.BlockText, Text: "what is this?"},
		{Type: protocol.BlockImage, Image: &protocol.ImageBlock{MediaType: "image/png", Data: "fake"}},
	})

	if c.Len() != 1 {
		t.Fatalf("expected 1 segment, got %d", c.Len())
	}
	// Should contain [image] marker in the text.
	if !contains(c.segments[0].text, "[image]") {
		t.Fatal("expected [image] marker in segment text")
	}
}

func TestChatAppendDelta(t *testing.T) {
	c := NewChat()
	c = c.AppendDelta("hello")
	if c.streaming != "hello" {
		t.Fatalf("expected streaming='hello', got %q", c.streaming)
	}
	// Len should still be 0 — streaming isn't a segment.
	if c.Len() != 0 {
		t.Fatal("streaming should not add a segment")
	}
}

func TestChatAppendDeltaCombines(t *testing.T) {
	c := NewChat()
	c = c.AppendDelta("hello ")
	c = c.AppendDelta("world")
	if c.streaming != "hello world" {
		t.Fatalf("expected streaming='hello world', got %q", c.streaming)
	}
}

func TestChatAppendThinkDelta(t *testing.T) {
	c := NewChat()
	c = c.AppendThinkDelta("thinking...")
	if c.streamingThink != "thinking..." {
		t.Fatalf("expected streamingThink='thinking...', got %q", c.streamingThink)
	}
	if c.Len() != 0 {
		t.Fatal("streaming think should not add a segment")
	}
}

func TestChatAppendThinkDeltaCombines(t *testing.T) {
	c := NewChat()
	c = c.AppendThinkDelta("part1 ")
	c = c.AppendThinkDelta("part2")
	if c.streamingThink != "part1 part2" {
		t.Fatalf("expected streamingThink='part1 part2', got %q", c.streamingThink)
	}
}

func TestChatFlushThinkStreaming(t *testing.T) {
	c := NewChat()
	c = c.AppendThinkDelta("thinking text")
	c = c.FlushThinkStreaming()
	if c.streamingThink != "" {
		t.Fatal("streamingThink should be empty after flush")
	}
	if c.Len() != 1 {
		t.Fatalf("expected 1 segment after flush, got %d", c.Len())
	}
	if !c.segments[0].isThinking {
		t.Fatal("flushed think should be a thinking segment")
	}
}

func TestChatFlushThinkStreamingEmpty(t *testing.T) {
	c := NewChat()
	c = c.FlushThinkStreaming()
	if c.Len() != 0 {
		t.Fatal("flush on empty think should be no-op")
	}
}

func TestChatAppendThinkingBlockClearsStreaming(t *testing.T) {
	c := NewChat()
	c = c.AppendThinkDelta("streamed")
	c = c.AppendThinkingBlock("final")
	if c.streamingThink != "" {
		t.Fatal("streamingThink should be cleared by AppendThinkingBlock")
	}
	if c.Len() != 1 {
		t.Fatalf("expected 1 segment, got %d", c.Len())
	}
	if c.segments[0].thinkText != "final" {
		t.Fatalf("expected thinkText='final', got %q", c.segments[0].thinkText)
	}
}

func TestChatFlushStreamingAlsoFlushesThink(t *testing.T) {
	c := NewChat()
	c = c.AppendThinkDelta("think")
	c = c.AppendDelta("text")
	c = c.FlushStreaming()
	if c.streamingThink != "" {
		t.Fatal("streamingThink should be empty after FlushStreaming")
	}
	if c.streaming != "" {
		t.Fatal("streaming should be empty after FlushStreaming")
	}
	if c.Len() != 2 {
		t.Fatalf("expected 2 segments, got %d", c.Len())
	}
	if !c.segments[0].isThinking {
		t.Fatal("first segment should be thinking")
	}
	if !c.segments[1].renderAsMarkdown {
		t.Fatal("second segment should be text")
	}
}

func TestChatFinalizeTurnFlushesThink(t *testing.T) {
	c := NewChat()
	c = c.AppendThinkDelta("think")
	c = c.FinalizeTurn(protocol.Usage{})
	if c.streamingThink != "" {
		t.Fatal("streamingThink should be empty after FinalizeTurn")
	}
	if c.Len() != 1 {
		t.Fatalf("expected 1 segment, got %d", c.Len())
	}
	if !c.segments[0].isThinking {
		t.Fatal("segment should be thinking")
	}
}

func TestChatAppendAssistantText(t *testing.T) {
	c := NewChat()
	c = c.AppendAssistantText("**bold** text")

	if c.Len() != 1 {
		t.Fatalf("expected 1 segment, got %d", c.Len())
	}
	if !c.segments[0].renderAsMarkdown {
		t.Fatal("assistant text should be markdown-rendered")
	}
}

func TestChatAppendNoticeInfo(t *testing.T) {
	c := NewChat()
	c = c.AppendNotice("info", "something happened")

	if c.Len() != 1 {
		t.Fatalf("expected 1 segment, got %d", c.Len())
	}
}

func TestChatAppendNoticeError(t *testing.T) {
	c := NewChat()
	c = c.AppendNotice("error", "something broke")

	if c.Len() != 1 {
		t.Fatalf("expected 1 segment, got %d", c.Len())
	}
}

func TestChatAppendNoticeWarn(t *testing.T) {
	c := NewChat()
	c = c.AppendNotice("warn", "be careful")

	if c.Len() != 1 {
		t.Fatalf("expected 1 segment, got %d", c.Len())
	}
}

func TestChatAppendNoticeThink(t *testing.T) {
	c := NewChat()
	c = c.AppendNotice("think", "hmm...")

	if c.Len() != 1 {
		t.Fatalf("expected 1 segment, got %d", c.Len())
	}
}

func TestChatAppendBatchHeading(t *testing.T) {
	c := NewChat()
	c = c.AppendBatchHeading(1)

	if c.Len() != 1 {
		t.Fatalf("expected 1 segment, got %d", c.Len())
	}
	if !contains(c.segments[0].text, "batch") {
		t.Fatal("batch heading should contain 'batch'")
	}
}

func TestChatAppendToolUse(t *testing.T) {
	c := NewChat()
	c = c.AppendToolUse("read", map[string]any{"path": "main.go"}, true, "")

	if c.Len() != 1 {
		t.Fatalf("expected 1 segment, got %d", c.Len())
	}
	if !c.segments[0].isTool {
		t.Fatal("tool-use segment should be marked as tool")
	}
}

func TestChatAppendToolUseConsecutive(t *testing.T) {
	c := NewChat()
	c = c.AppendToolUse("read", map[string]any{"path": "a.go"}, true, "")
	c = c.AppendToolUse("read", map[string]any{"path": "b.go"}, true, "")

	if c.Len() != 2 {
		t.Fatalf("expected 2 segments, got %d", c.Len())
	}
	// Second consecutive tool should have no 'before' separator.
	if c.segments[1].before != "" {
		t.Fatal("consecutive tool should have empty before")
	}
}

func TestChatFlushStreaming(t *testing.T) {
	c := NewChat()
	c = c.AppendDelta("streaming text")
	c = c.FlushStreaming()

	if c.streaming != "" {
		t.Fatal("streaming should be empty after flush")
	}
	if c.Len() != 1 {
		t.Fatalf("expected 1 segment after flush, got %d", c.Len())
	}
}

func TestChatFlushStreamingWithNoContent(t *testing.T) {
	c := NewChat()
	c = c.FlushStreaming() // no-op

	if c.Len() != 0 {
		t.Fatal("flush on empty streaming should be no-op")
	}
}

func TestChatPopLastSegment(t *testing.T) {
	c := NewChat()
	c = c.AppendUserInput("hello")
	c = c.AppendUserInput("world")

	if c.Len() != 2 {
		t.Fatalf("expected 2 segments, got %d", c.Len())
	}

	c = c.PopLastSegment()
	if c.Len() != 1 {
		t.Fatalf("expected 1 segment after pop, got %d", c.Len())
	}
}

func TestChatPopLastSegmentEmpty(t *testing.T) {
	c := NewChat()
	c = c.PopLastSegment() // no-op

	if c.Len() != 0 {
		t.Fatal("pop on empty chat should be no-op")
	}
}

func TestChatFinalizeTurn(t *testing.T) {
	c := NewChat()
	c = c.AppendDelta("final text")
	c = c.FinalizeTurn(protocol.Usage{})

	if c.streaming != "" {
		t.Fatal("streaming should be empty after finalize")
	}
	if c.Len() != 1 {
		t.Fatalf("expected 1 segment, got %d", c.Len())
	}
}

func TestChatFinalizeTurnNoStreaming(t *testing.T) {
	c := NewChat()
	c = c.FinalizeTurn(protocol.Usage{}) // no-op

	if c.Len() != 0 {
		t.Fatal("finalize with no streaming should be no-op")
	}
}

func TestChatAddActiveTool(t *testing.T) {
	c := NewChat()
	c = c.AddActiveTool("1", "read")

	if len(c.activeTools) != 1 {
		t.Fatalf("expected 1 active tool, got %d", len(c.activeTools))
	}
}

func TestChatRemoveActiveTool(t *testing.T) {
	c := NewChat()
	c = c.AddActiveTool("1", "read")
	c = c.AddActiveTool("2", "bash")
	c = c.RemoveActiveTool("1")

	if len(c.activeTools) != 1 {
		t.Fatalf("expected 1 active tool, got %d", len(c.activeTools))
	}
	if _, ok := c.activeTools["2"]; !ok {
		t.Fatal("expected tool 2 to remain")
	}
}

func TestChatRemoveActiveToolUnknown(t *testing.T) {
	c := NewChat()
	c = c.RemoveActiveTool("nonexistent") // no-op
}

func TestChatSetSize(t *testing.T) {
	c := NewChat()
	c.SetSize(80, 24)

	if c.width != 80 || c.height != 24 {
		t.Fatalf("expected width=80 height=24, got %d/%d", c.width, c.height)
	}
}

func TestChatWidth(t *testing.T) {
	c := NewChat()
	c.SetSize(100, 30)

	if c.Width() != 100 {
		t.Fatalf("expected width=100, got %d", c.Width())
	}
}

func TestChatHeight(t *testing.T) {
	c := NewChat()
	c.SetSize(100, 30)

	if c.Height() != 30 {
		t.Fatalf("expected height=30, got %d", c.Height())
	}
}

func TestChatScrollDown(t *testing.T) {
	c := NewChat()
	// Should not panic.
	c = c.ScrollDown(1)
	if !c.autoscroll {
		t.Fatal("scroll down should set autoscroll true")
	}
}

func TestChatScrollUp(t *testing.T) {
	c := NewChat()
	c = c.ScrollUp(1)
	if c.autoscroll {
		t.Fatal("scroll up should set autoscroll false")
	}
}

func TestChatScrollTop(t *testing.T) {
	c := NewChat()
	c = c.ScrollTop()
	if c.autoscroll {
		t.Fatal("scroll top should set autoscroll false")
	}
}

func TestChatScrollBottom(t *testing.T) {
	c := NewChat()
	c = c.ScrollBottom()
	if !c.autoscroll {
		t.Fatal("scroll bottom should set autoscroll true")
	}
}

func TestChatBody(t *testing.T) {
	c := NewChat()
	c = c.AppendUserInput("hello")
	c = c.AppendAssistantText("world")

	body := c.body()
	if body == "" {
		t.Fatal("expected non-empty body")
	}
}

func TestChatBodyWithStreaming(t *testing.T) {
	c := NewChat()
	c = c.AppendUserInput("hello")
	c = c.AppendDelta("streaming...")

	body := c.body()
	if body == "" {
		t.Fatal("expected non-empty body with streaming")
	}
}

func TestChatBodyWithActiveTools(t *testing.T) {
	c := NewChat()
	c = c.AddActiveTool("1", "read")

	body := c.body()
	if body == "" {
		t.Fatal("expected non-empty body with active tools")
	}
}

func TestChatView(t *testing.T) {
	c := NewChat()
	c.SetSize(80, 24)
	c = c.AppendUserInput("hello")
	c = c.AppendAssistantText("world")

	view := c.View()
	if view == "" {
		t.Fatal("expected non-empty view")
	}
}

func TestChatMultipleSegments(t *testing.T) {
	c := NewChat()
	for i := 0; i < 10; i++ {
		c = c.AppendUserInput("message")
	}
	if c.Len() != 10 {
		t.Fatalf("expected 10 segments, got %d", c.Len())
	}
}

func TestChatWrapText(t *testing.T) {
	c := NewChat()
	c.SetSize(20, 10)

	longText := "this is a very long line that should be wrapped at the configured width"
	wrapped := c.wrapText(longText)
	if wrapped == "" {
		t.Fatal("expected non-empty wrapped text")
	}
}

func TestChatDirtyFlag(t *testing.T) {
	c := NewChat()
	if !c.dirty {
		t.Fatal("expected dirty=true after construction")
	}
	c = c.AppendUserInput("test")
	if !c.dirty {
		t.Fatal("expected dirty=true after append")
	}
}
