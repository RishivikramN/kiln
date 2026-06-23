package main

import (
	"fmt"
	"strings"
	"sync"
)

const (
	ansiBgDiffAdd = "\033[48;5;22m"  // dark green bg for added lines
	ansiBgDiffDel = "\033[48;5;52m"  // dark red bg for removed lines
	ansiFgDiffAdd = "\033[38;5;114m" // bright green fg
	ansiFgDiffDel = "\033[38;5;203m" // salmon/red fg
	ansiFgLineNum = "\033[38;5;244m" // gray for line numbers
)

type lineKind byte

const (
	lkCtx lineKind = ' '
	lkAdd lineKind = '+'
	lkDel lineKind = '-'
)

type dline struct {
	k      lineKind
	oldNum int
	newNum int
	text   string
}

// DiffResult holds a computed file diff ready for display.
type DiffResult struct {
	Filename string
	Added    int
	Removed  int
	Lines    []dline // nil when file was too large for LCS
}

// pendingDiffs carries diffs from write_file Execute → onTool callback.
var pendingDiffs sync.Map // key: relative path → DiffResult

func storePendingDiff(relPath string, d DiffResult) {
	pendingDiffs.Store(relPath, d)
}

func takePendingDiff(relPath string) (DiffResult, bool) {
	v, ok := pendingDiffs.LoadAndDelete(relPath)
	if !ok {
		return DiffResult{}, false
	}
	return v.(DiffResult), true
}

// diffFiles computes a line-level LCS diff between oldText and newText.
func diffFiles(oldText, newText, filename string) DiffResult {
	oldL := splitLines(oldText)
	newL := splitLines(newText)
	m, n := len(oldL), len(newL)

	// skip LCS for large files — report counts only
	if m > 800 || n > 800 {
		return DiffResult{Filename: filename, Added: n, Removed: m}
	}

	// LCS DP table
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if oldL[i-1] == newL[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	// traceback (builds in reverse, then flip)
	var lines []dline
	i, j := m, n
	for i > 0 || j > 0 {
		switch {
		case i > 0 && j > 0 && oldL[i-1] == newL[j-1]:
			lines = append(lines, dline{k: lkCtx, oldNum: i, newNum: j, text: newL[j-1]})
			i--
			j--
		case j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]):
			lines = append(lines, dline{k: lkAdd, newNum: j, text: newL[j-1]})
			j--
		default:
			lines = append(lines, dline{k: lkDel, oldNum: i, text: oldL[i-1]})
			i--
		}
	}
	for l, r := 0, len(lines)-1; l < r; l, r = l+1, r-1 {
		lines[l], lines[r] = lines[r], lines[l]
	}

	var added, removed int
	for _, dl := range lines {
		switch dl.k {
		case lkAdd:
			added++
		case lkDel:
			removed++
		}
	}
	return DiffResult{Filename: filename, Added: added, Removed: removed, Lines: lines}
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// renderDiff renders a DiffResult as ANSI-colored terminal lines.
// Shows 3 lines of context around each changed hunk.
func renderDiff(d DiffResult, width int) []string {
	var out []string

	// header: ● Update(filename)  +N  -N
	addStr, delStr := "", ""
	if d.Added > 0 {
		addStr = fmt.Sprintf("  %s+%d%s", ansiFgDiffAdd, d.Added, ansiReset)
	}
	if d.Removed > 0 {
		delStr = fmt.Sprintf("  %s-%d%s", ansiFgDiffDel, d.Removed, ansiReset)
	}
	out = append(out, "  "+ansiGreen+ansiBold+"●"+ansiReset+" "+
		ansiBold+"Update("+d.Filename+")"+ansiReset+addStr+delStr)

	if len(d.Lines) == 0 {
		return out
	}
	out = append(out, "  "+ansiDim+"└"+strings.Repeat("─", width-5)+ansiReset)

	// mark interesting lines (changed ±3 context)
	show := make([]bool, len(d.Lines))
	for i, dl := range d.Lines {
		if dl.k != lkCtx {
			for j := imax(0, i-3); j <= imin(len(d.Lines)-1, i+3); j++ {
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
				out = append(out, "  "+ansiDim+"    ···"+ansiReset)
			}
			continue
		}
		inHunk = true

		lineNum := dl.newNum
		if dl.k == lkDel {
			lineNum = dl.oldNum
		}
		numStr := fmt.Sprintf("%*d", numW, lineNum)

		text := strings.ReplaceAll(dl.text, "\t", "    ")
		runes := []rune(text)
		if len(runes) > textW {
			text = string(runes[:textW-1]) + "…"
		}

		switch dl.k {
		case lkAdd:
			out = append(out, fmt.Sprintf("%s  %s+%s %s%s  %s%-*s%s",
				ansiBgDiffAdd, ansiFgDiffAdd, ansiReset+ansiBgDiffAdd,
				ansiFgLineNum, numStr, ansiFgDiffAdd,
				textW, text, ansiReset))
		case lkDel:
			out = append(out, fmt.Sprintf("%s  %s-%s %s%s  %s%-*s%s",
				ansiBgDiffDel, ansiFgDiffDel, ansiReset+ansiBgDiffDel,
				ansiFgLineNum, numStr, ansiFgDiffDel,
				textW, text, ansiReset))
		default:
			out = append(out, fmt.Sprintf("    %s%s%s  %s",
				ansiFgLineNum, numStr, ansiReset, text))
		}
	}

	return out
}

func imax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func imin(a, b int) int {
	if a < b {
		return a
	}
	return b
}
