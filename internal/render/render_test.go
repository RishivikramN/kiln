package render_test

import (
	"strings"
	"testing"

	"kiln/internal/render"
)

func TestParseMarkdown_plainText(t *testing.T) {
	segs := render.ParseMarkdown("hello world")
	if len(segs) != 1 {
		t.Fatalf("want 1 segment, got %d", len(segs))
	}
	if segs[0].IsCode {
		t.Error("expected prose segment")
	}
	if segs[0].Text != "hello world" {
		t.Errorf("text: want %q, got %q", "hello world", segs[0].Text)
	}
}

func TestParseMarkdown_singleCodeBlock(t *testing.T) {
	input := "```go\nfunc main() {}\n```"
	segs := render.ParseMarkdown(input)
	if len(segs) != 1 {
		t.Fatalf("want 1 segment, got %d", len(segs))
	}
	if !segs[0].IsCode {
		t.Error("expected code segment")
	}
	if segs[0].Lang != "go" {
		t.Errorf("lang: want go, got %q", segs[0].Lang)
	}
	if segs[0].Text != "func main() {}" {
		t.Errorf("text: got %q", segs[0].Text)
	}
}

func TestParseMarkdown_proseCodeProse(t *testing.T) {
	input := "Before\n```python\nprint('hi')\n```\nAfter"
	segs := render.ParseMarkdown(input)
	if len(segs) != 3 {
		t.Fatalf("want 3 segments, got %d", len(segs))
	}
	if segs[0].IsCode || !strings.HasPrefix(segs[0].Text, "Before") {
		t.Errorf("seg[0]: want prose starting with 'Before', got %+v", segs[0])
	}
	if !segs[1].IsCode || segs[1].Lang != "python" {
		t.Errorf("seg[1]: want code lang=python, got %+v", segs[1])
	}
	if segs[2].IsCode || !strings.Contains(segs[2].Text, "After") {
		t.Errorf("seg[2]: want prose containing 'After', got %+v", segs[2])
	}
}

func TestParseMarkdown_unclosedFence(t *testing.T) {
	input := "intro\n```go\ncode here\nno closing fence"
	segs := render.ParseMarkdown(input)
	// should produce a prose segment then a code segment with everything after the fence open
	hasCode := false
	for _, s := range segs {
		if s.IsCode {
			hasCode = true
			if !strings.Contains(s.Text, "code here") {
				t.Errorf("unclosed fence code segment missing content: %q", s.Text)
			}
		}
	}
	if !hasCode {
		t.Error("expected at least one code segment for unclosed fence")
	}
}

func TestParseMarkdown_noLanguageTag(t *testing.T) {
	input := "```\nsome code\n```"
	segs := render.ParseMarkdown(input)
	if len(segs) != 1 || !segs[0].IsCode {
		t.Fatalf("want 1 code segment, got %+v", segs)
	}
	if segs[0].Lang != "" {
		t.Errorf("lang: want empty, got %q", segs[0].Lang)
	}
	if segs[0].Text != "some code" {
		t.Errorf("text: got %q", segs[0].Text)
	}
}

func TestParseMarkdown_empty(t *testing.T) {
	segs := render.ParseMarkdown("")
	if len(segs) != 0 {
		t.Errorf("empty input: want 0 segments, got %d", len(segs))
	}
}

func TestParseMarkdown_multipleCodeBlocks(t *testing.T) {
	input := "```sh\necho a\n```\n```sh\necho b\n```"
	segs := render.ParseMarkdown(input)
	codeCt := 0
	for _, s := range segs {
		if s.IsCode {
			codeCt++
		}
	}
	if codeCt != 2 {
		t.Errorf("want 2 code segments, got %d", codeCt)
	}
}
