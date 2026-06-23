package main

import (
	"fmt"
	"strings"
)

const ansiBgCode = "\033[48;5;234m" // very dark background for code blocks

// mdSegment is a parsed piece of assistant response.
type mdSegment struct {
	isCode bool
	lang   string
	text   string
}

// parseMarkdown splits markdown text into prose and fenced code block segments.
func parseMarkdown(text string) []mdSegment {
	var segs []mdSegment
	rem := text
	for {
		idx := strings.Index(rem, "```")
		if idx == -1 {
			if rem != "" {
				segs = append(segs, mdSegment{text: rem})
			}
			break
		}
		if idx > 0 {
			segs = append(segs, mdSegment{text: rem[:idx]})
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
		segs = append(segs, mdSegment{isCode: true, lang: lang, text: strings.TrimRight(code, "\n")})
	}
	return segs
}

// renderCodeBlock renders a fenced code block with line numbers and a dark background.
func renderCodeBlock(code, lang string, width int) []string {
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
	out = append(out, "  "+ansiBgCode+ansiDim+langLabel+strings.Repeat(" ", headerPad)+ansiReset)

	for i, line := range codeLines {
		numStr := fmt.Sprintf("%*d", numW, i+1)
		line = strings.ReplaceAll(line, "\t", "    ")
		runes := []rune(line)
		if len(runes) > textW {
			line = string(runes[:textW-1]) + "…"
		}
		out = append(out, fmt.Sprintf("  %s%s%s │ %-*s%s",
			ansiBgCode, ansiFgLineNum, numStr,
			textW, line,
			ansiReset))
	}

	// footer
	out = append(out, "  "+ansiDim+strings.Repeat("─", width-4)+ansiReset)

	return out
}
