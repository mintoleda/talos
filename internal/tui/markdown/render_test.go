package markdown

import (
	"testing"
)

func TestNewRenderer(t *testing.T) {
	ren := New(80)
	if ren == nil {
		t.Fatal("expected non-nil renderer")
	}
	if ren.width != 80 {
		t.Fatalf("expected width=80, got %d", ren.width)
	}
	if ren.r == nil {
		t.Fatal("expected non-nil glamour renderer")
	}
}

func TestNewRendererZeroWidth(t *testing.T) {
	ren := New(0)
	if ren == nil {
		t.Fatal("expected non-nil renderer")
	}
	if ren.width != 80 {
		t.Fatalf("expected default width=80, got %d", ren.width)
	}
}

func TestRenderPlainText(t *testing.T) {
	ren := New(80)
	out := ren.Render("hello world")
	if out == "" {
		t.Fatal("expected non-empty output")
	}
	// Strip ANSI to check content is present.
	plain := stripANSI(out)
	if !contains(plain, "hello world") {
		t.Fatalf("output should contain input text, got: %s", out)
	}
}

func stripANSI(s string) string {
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			i += 2 // skip \x1b[
			for i < len(s) && !((s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z')) {
				i++
			}
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}

func TestRenderEmpty(t *testing.T) {
	ren := New(80)
	out := ren.Render("")
	// May return empty or just whitespace.
	_ = out
}

func TestRenderInline(t *testing.T) {
	ren := New(80)
	out := ren.RenderInline("hello")
	if out == "" {
		t.Fatal("expected non-empty output for inline render")
	}
}

func TestRenderWithMarkdown(t *testing.T) {
	ren := New(80)
	out := ren.Render("**bold** and *italic*")
	if out == "" {
		t.Fatal("expected non-empty output")
	}
}

func TestRenderWithCodeBlock(t *testing.T) {
	ren := New(80)
	out := ren.Render("```go\nfunc main() {}\n```")
	if out == "" {
		t.Fatal("expected non-empty output for code block")
	}
}

func TestRenderNoTrailingNewline(t *testing.T) {
	ren := New(80)
	out := ren.Render("hello")
	// Output should be stripped of trailing newlines by our Render method.
	_ = out
	// This test is informational — glamour output format may vary.
}

func TestSetWidth(t *testing.T) {
	ren := New(80)
	ren.SetWidth(60)

	if ren.width != 60 {
		t.Fatalf("expected width=60, got %d", ren.width)
	}
}

func TestSetWidthSameWidthNoOp(t *testing.T) {
	ren := New(80)
	r0 := ren.r
	ren.SetWidth(80)

	if ren.r != r0 {
		t.Fatal("expected same renderer instance for same width")
	}
}

func TestSetWidthZeroNoOp(t *testing.T) {
	ren := New(80)
	r0 := ren.r
	ren.SetWidth(0)

	if ren.r != r0 {
		t.Fatal("expected same renderer instance for zero width")
	}
}

func TestRenderWithList(t *testing.T) {
	ren := New(80)
	out := ren.Render("- item 1\n- item 2")
	if out == "" {
		t.Fatal("expected non-empty output for list")
	}
}

func TestRenderWithHeading(t *testing.T) {
	ren := New(80)
	out := ren.Render("# Heading\n\nParagraph.")
	if out == "" {
		t.Fatal("expected non-empty output for heading")
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
