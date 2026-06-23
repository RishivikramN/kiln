package diff

import (
	"sync"
)

// LineKind indicates whether a diff line is context, added, or deleted.
type LineKind byte

const (
	LkCtx LineKind = ' '
	LkAdd LineKind = '+'
	LkDel LineKind = '-'
)

// Line is a single line in a computed diff.
type Line struct {
	K      LineKind
	OldNum int
	NewNum int
	Text   string
}

// Result holds a computed file diff ready for display.
type Result struct {
	Filename string
	Added    int
	Removed  int
	Lines    []Line // nil when file was too large for LCS
}

// pendingDiffs carries diffs from write_file Execute → onTool callback.
var pendingDiffs sync.Map // key: relative path → Result

// StorePending stashes a diff result for later retrieval by TakePending.
func StorePending(relPath string, r Result) {
	pendingDiffs.Store(relPath, r)
}

// TakePending retrieves and removes a stashed diff result.
func TakePending(relPath string) (Result, bool) {
	v, ok := pendingDiffs.LoadAndDelete(relPath)
	if !ok {
		return Result{}, false
	}
	return v.(Result), true
}

// Compute computes a line-level LCS diff between oldText and newText.
func Compute(oldText, newText, filename string) Result {
	oldL := splitLines(oldText)
	newL := splitLines(newText)
	m, n := len(oldL), len(newL)

	// skip LCS for large files — report counts only
	if m > 800 || n > 800 {
		return Result{Filename: filename, Added: n, Removed: m}
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
	var lines []Line
	i, j := m, n
	for i > 0 || j > 0 {
		switch {
		case i > 0 && j > 0 && oldL[i-1] == newL[j-1]:
			lines = append(lines, Line{K: LkCtx, OldNum: i, NewNum: j, Text: newL[j-1]})
			i--
			j--
		case j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]):
			lines = append(lines, Line{K: LkAdd, NewNum: j, Text: newL[j-1]})
			j--
		default:
			lines = append(lines, Line{K: LkDel, OldNum: i, Text: oldL[i-1]})
			i--
		}
	}
	for l, r := 0, len(lines)-1; l < r; l, r = l+1, r-1 {
		lines[l], lines[r] = lines[r], lines[l]
	}

	var added, removed int
	for _, dl := range lines {
		switch dl.K {
		case LkAdd:
			added++
		case LkDel:
			removed++
		}
	}
	return Result{Filename: filename, Added: added, Removed: removed, Lines: lines}
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	// normalize line endings
	out := make([]string, 0)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
			out = append(out, s[start:i])
			i++
			start = i + 1
		} else if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	// trailing content (no trailing newline)
	if start < len(s) {
		out = append(out, s[start:])
	}
	// drop trailing empty string caused by a final newline
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		return nil
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

// Imax and Imin exported for use in render package.
func Imax(a, b int) int { return imax(a, b) }
func Imin(a, b int) int { return imin(a, b) }
