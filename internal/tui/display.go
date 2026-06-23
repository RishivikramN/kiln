package tui

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"kiln/internal/render"
)

// TUI-chrome ANSI constants (unexported — only used for layout/chrome).
const (
	ansiClearScreen = "\033[2J\033[H"
	ansiHideCursor  = "\033[?25l"
	ansiShowCursor  = "\033[?25h"
	ansiClearLine   = "\033[2K"
	ansiBgDark      = "\033[48;5;236m"
)

// Content ANSI constants — re-exported from render for use within this package.
const (
	ansiReset = render.Reset
	ansiBold  = render.Bold
	ansiDim   = render.Dim
	ansiGreen = render.Green
	ansiCyan  = render.Cyan
)

func moveTo(row, col int) string {
	return fmt.Sprintf("\033[%d;%dH", row, col)
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (t *TUI) renderLocked() {
	var sb strings.Builder
	sb.WriteString(ansiHideCursor)
	sb.WriteString(ansiClearScreen)

	// Status bar
	permLabel := "no permissions"
	if t.permStore != nil {
		if p, ok := t.permStore.Get(t.repoPath); ok {
			switch p.Mode {
			case "read-write":
				permLabel = "read and write"
			case "read-only":
				permLabel = "read only"
			case "none":
				permLabel = "no permissions"
			}
		}
	}
	sysIndicator := ""
	if t.systemPrompt != "" {
		sysIndicator = "  [sys]"
	}
	left := fmt.Sprintf(" kiln │  %s  │  permissions: %s%s  ", t.model, permLabel, sysIndicator)
	right := "  Ctrl+C to exit "
	pad := t.width - len(left) - len(right)
	if pad < 0 {
		pad = 0
	}
	sb.WriteString(moveTo(1, 1))
	sb.WriteString(ansiCyan + ansiBold + left + strings.Repeat(" ", pad) + right + ansiReset)

	// Top divider
	sb.WriteString(moveTo(2, 1))
	sb.WriteString(ansiDim + strings.Repeat("─", t.width) + ansiReset)

	// Chat area
	chatHeight := t.height - 4
	if chatHeight < 1 {
		chatHeight = 1
	}
	lines := t.chatLines()

	maxScroll := len(lines) - chatHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	if t.scrollOffset > maxScroll {
		t.scrollOffset = maxScroll
	}

	start := len(lines) - chatHeight - t.scrollOffset
	if start < 0 {
		start = 0
	}

	for i := 0; i < chatHeight; i++ {
		sb.WriteString(moveTo(3+i, 1))
		sb.WriteString(ansiClearLine)
		if idx := start + i; idx < len(lines) {
			sb.WriteString(lines[idx])
		}
	}

	// Bottom divider
	sb.WriteString(moveTo(t.height-1, 1))
	sb.WriteString(ansiDim + strings.Repeat("─", t.width) + ansiReset)

	// Input line — render block cursor at t.cursorPos
	sb.WriteString(moveTo(t.height, 1))
	sb.WriteString(ansiClearLine)
	sb.WriteString(ansiGreen + ansiBold + "  ❯ " + ansiReset)
	cp := t.cursorPos
	if cp > len(t.input) {
		cp = len(t.input)
	}
	sb.WriteString(string(t.input[:cp]))
	if cp < len(t.input) {
		sb.WriteString("\033[7m" + string(t.input[cp:cp+1]) + ansiReset)
		sb.WriteString(string(t.input[cp+1:]))
	} else {
		sb.WriteString("█")
	}

	t.renderCompletionsLocked(&sb)

	fmt.Print(sb.String())
}

// renderCompletionsLocked draws the autocomplete popup above the bottom divider.
// Called with t.mu already held.
func (t *TUI) renderCompletionsLocked(sb *strings.Builder) {
	if len(t.completions) == 0 {
		return
	}

	const maxVisible = 6
	const cmdCol = 26 // fixed width for the command column
	const ansiHighlight = "\033[48;5;237m"

	n := len(t.completions)

	// scroll window so the selected item stays visible
	start := t.completionIdx - maxVisible/2
	if start < 0 {
		start = 0
	}
	if start+maxVisible > n {
		start = n - maxVisible
	}
	if start < 0 {
		start = 0
	}
	end := start + maxVisible
	if end > n {
		end = n
	}
	visible := t.completions[start:end]

	// items sit above the bottom divider (height-1), not overlaying it
	for i, cmd := range visible {
		row := t.height - 1 - len(visible) + i
		if row < 3 {
			continue
		}
		desc := slashDescriptions[cmd]
		absIdx := start + i

		sb.WriteString(moveTo(row, 1))
		sb.WriteString(ansiClearLine)

		if absIdx == t.completionIdx {
			// selected: subtle background, bright command, dim description
			cmdPart := fmt.Sprintf("%-*s", cmdCol, cmd)
			sb.WriteString(ansiHighlight + "  " + ansiCyan + ansiBold + cmdPart + ansiReset)
			sb.WriteString(ansiHighlight + ansiDim + desc + ansiReset)
			// fill rest of line with highlight bg so the row feels full-width
			used := 2 + cmdCol + len(desc)
			if pad := t.width - used; pad > 0 {
				sb.WriteString(ansiHighlight + strings.Repeat(" ", pad) + ansiReset)
			}
		} else {
			// non-selected: dim command in cyan, dim description
			cmdPart := fmt.Sprintf("%-*s", cmdCol, cmd)
			sb.WriteString("  " + ansiDim + ansiCyan + cmdPart + ansiReset)
			sb.WriteString(ansiDim + desc + ansiReset)
		}
	}
}

func (t *TUI) chatLines() []string {
	var lines []string
	w := t.width
	textWidth := w - 4
	if textWidth < 10 {
		textWidth = 10
	}

	for i, msg := range t.messages {
		switch msg.Role {

		case "user":
			// full-width dark bar, like Claude Code prompt
			first := true
			for _, rawLine := range strings.Split(msg.Content, "\n") {
				pfx := "    "
				if first {
					pfx = "  ❯ "
					first = false
				}
				for _, line := range wrap(rawLine, w-len(pfx)) {
					raw := pfx + line
					padded := fmt.Sprintf("%-*s", w, raw)
					lines = append(lines, ansiBgDark+ansiDim+padded+ansiReset)
				}
			}
			lines = append(lines, "")

		case "tool":
			tok := ""
			if msg.Tokens > 0 {
				tok = fmt.Sprintf(" · ~%d tokens", msg.Tokens)
			}
			lines = append(lines, ansiDim+"  ⚙ "+toolSummary(msg.Content)+tok+ansiReset)

		case "diff":
			if msg.Diff != nil {
				lines = append(lines, render.RenderDiff(*msg.Diff, w)...)
				lines = append(lines, "")
			}

		case "assistant":
			content := msg.Content
			if content == "" && i == len(t.messages)-1 && atomic.LoadInt32(&t.spinning) == 1 {
				frame := spinnerFrames[int(atomic.LoadInt32(&t.spinnerIdx))%len(spinnerFrames)]
				// t.spinnerStart is safe to read directly — renderLocked already holds t.mu
				elapsed := formatElapsed(time.Since(t.spinnerStart))
				lines = append(lines, ansiDim+"  "+frame+" thinking… "+elapsed+ansiReset)
				lines = append(lines, "")
				continue
			}
			segs := render.ParseMarkdown(content)
			first := true
			for _, seg := range segs {
				if seg.IsCode {
					if first {
						lines = append(lines, "  "+ansiGreen+ansiBold+"●"+ansiReset)
						first = false
					}
					lines = append(lines, render.RenderCodeBlock(seg.Text, seg.Lang, textWidth)...)
				} else {
					for _, rawLine := range strings.Split(seg.Text, "\n") {
						for _, line := range wrap(rawLine, textWidth) {
							if first {
								lines = append(lines, "  "+ansiGreen+ansiBold+"●"+ansiReset+" "+line)
								first = false
							} else {
								lines = append(lines, "    "+line)
							}
						}
					}
				}
			}
			lines = append(lines, "")

		case "system":
			// slash command output (not tool calls)
			first := true
			for _, rawLine := range strings.Split(msg.Content, "\n") {
				for _, line := range wrap(rawLine, textWidth) {
					if first {
						lines = append(lines, "  "+ansiCyan+ansiBold+"●"+ansiReset+" "+line)
						first = false
					} else {
						lines = append(lines, "    "+line)
					}
				}
			}
			lines = append(lines, "")
		}
	}
	return lines
}

func formatElapsed(d time.Duration) string {
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm %ds", s/60, s%60)
}

func toolSummary(name string) string {
	switch name {
	case "list_files":
		return "Listed files"
	case "read_file":
		return "Read file"
	case "grep":
		return "Searched files"
	case "write_file":
		return "Wrote file"
	case "run_command":
		return "Ran shell command"
	default:
		return name
	}
}

func wrap(text string, width int) []string {
	if len(text) <= width {
		return []string{text}
	}
	var lines []string
	cur := ""
	for _, w := range strings.Fields(text) {
		switch {
		case cur == "":
			cur = w
		case len(cur)+1+len(w) <= width:
			cur += " " + w
		default:
			lines = append(lines, cur)
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}
