package render

import (
	"fmt"
	"strings"

	"kiln/internal/diff"
)

// ANSI escape constants for content rendering (exported so tui/display.go can use them).
const (
	Reset = "\033[0m"
	Bold  = "\033[1m"
	Dim   = "\033[2m"
	Green = "\033[32m"
	Cyan  = "\033[36m"
	Blue  = "\033[34m"

	// Code block background.
	BgCode = "\033[48;5;234m" // very dark background for code blocks

	// Diff colors.
	BgDiffAdd = "\033[48;5;22m"  // dark green bg for added lines
	BgDiffDel = "\033[48;5;52m"  // dark red bg for removed lines
	FgDiffAdd = "\033[38;5;114m" // bright green fg
	FgDiffDel = "\033[38;5;203m" // salmon/red fg
	FgLineNum = "\033[38;5;244m" // gray for line numbers
)

// Segment is a parsed piece of assistant response.
type Segment struct {
	IsCode bool
	Lang   string
	Text   string
}

// ParseMarkdown splits markdown text into prose and fenced code block segments.
func ParseMarkdown(text string) []Segment {
	var segs []Segment
	rem := text
	for {
		idx := strings.Index(rem, "```")
		if idx == -1 {
			if rem != "" {
				segs = append(segs, Segment{Text: rem})
			}
			break
		}
		if idx > 0 {
			segs = append(segs, Segment{Text: rem[:idx]})
		}
		after := rem[idx+3:]
		nl := strings.IndexByte(after, '\n')
		lang := ""
		if nl != -1 {
			lang = strings.TrimSpace(after[:nl])
			after = after[nl+1:]
		}
		end := strings.Index(after, "```")
		code := after
		if end != -1 {
			code = after[:end]
			rem = after[end+3:]
		} else {
			rem = ""
		}
		segs = append(segs, Segment{IsCode: true, Lang: lang, Text: strings.TrimRight(code, "\n")})
	}
	return segs
}

// RenderCodeBlock renders a fenced code block with line numbers and a dark background.
func RenderCodeBlock(code, lang string, width int) []string {
	codeLines := strings.Split(code, "\n")
	numW := len(fmt.Sprintf("%d", len(codeLines)))
	textW := width - numW - 7
	if textW < 10 {
		textW = 10
	}

	var out []string

	// header bar with language label
	langLabel := ""
	if lang != "" {
		langLabel = " " + lang
	}
	headerPad := width - len(langLabel) - 4
	if headerPad < 0 {
		headerPad = 0
	}
	out = append(out, "  "+BgCode+Dim+langLabel+strings.Repeat(" ", headerPad)+Reset)

	for i, line := range codeLines {
		numStr := fmt.Sprintf("%*d", numW, i+1)
		line = strings.ReplaceAll(line, "\t", "    ")
		runes := []rune(line)
		if len(runes) > textW {
			line = string(runes[:textW-1]) + "…"
		}
		out = append(out, fmt.Sprintf("  %s%s%s │ %-*s%s",
			BgCode, FgLineNum, numStr,
			textW, line,
			Reset))
	}

	// footer
	out = append(out, "  "+Dim+strings.Repeat("─", width-4)+Reset)

	return out
}

// RenderDiff renders a diff.Result as ANSI-colored terminal lines.
// Shows 3 lines of context around each changed hunk.
func RenderDiff(d diff.Result, width int) []string {
	var out []string

	// header: ● Update(filename)  +N  -N
	addStr, delStr := "", ""
	if d.Added > 0 {
		addStr = fmt.Sprintf("  %s+%d%s", FgDiffAdd, d.Added, Reset)
	}
	if d.Removed > 0 {
		delStr = fmt.Sprintf("  %s-%d%s", FgDiffDel, d.Removed, Reset)
	}
	out = append(out, "  "+Green+Bold+"●"+Reset+" "+
		Bold+"Update("+d.Filename+")"+Reset+addStr+delStr)

	if len(d.Lines) == 0 {
		return out
	}
	out = append(out, "  "+Dim+"└"+strings.Repeat("─", width-5)+Reset)

	// mark interesting lines (changed ±3 context)
	show := make([]bool, len(d.Lines))
	for i, dl := range d.Lines {
		if dl.K != diff.LkCtx {
			for j := diff.Imax(0, i-3); j <= diff.Imin(len(d.Lines)-1, i+3); j++ {
				show[j] = true
			}
		}
	}

	numW := len(fmt.Sprintf("%d", len(d.Lines)+1))
	textW := width - numW - 8
	if textW < 10 {
		textW = 10
	}

	inHunk := false
	for i, dl := range d.Lines {
		if !show[i] {
			if inHunk {
				inHunk = false
				out = append(out, "  "+Dim+"    ···"+Reset)
			}
			continue
		}
		inHunk = true

		lineNum := dl.NewNum
		if dl.K == diff.LkDel {
			lineNum = dl.OldNum
		}
		numStr := fmt.Sprintf("%*d", numW, lineNum)

		text := strings.ReplaceAll(dl.Text, "\t", "    ")
		runes := []rune(text)
		if len(runes) > textW {
			text = string(runes[:textW-1]) + "…"
		}

		switch dl.K {
		case diff.LkAdd:
			out = append(out, fmt.Sprintf("%s  %s+%s %s%s  %s%-*s%s",
				BgDiffAdd, FgDiffAdd, Reset+BgDiffAdd,
				FgLineNum, numStr, FgDiffAdd,
				textW, text, Reset))
		case diff.LkDel:
			out = append(out, fmt.Sprintf("%s  %s-%s %s%s  %s%-*s%s",
				BgDiffDel, FgDiffDel, Reset+BgDiffDel,
				FgLineNum, numStr, FgDiffDel,
				textW, text, Reset))
		default:
			out = append(out, fmt.Sprintf("    %s%s%s  %s",
				FgLineNum, numStr, Reset, text))
		}
	}

	return out
}
