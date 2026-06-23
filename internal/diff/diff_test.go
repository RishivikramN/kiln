package diff_test

import (
	"strings"
	"testing"

	"kiln/internal/diff"
)

func TestCompute_identical(t *testing.T) {
	text := "line1\nline2\nline3\n"
	r := diff.Compute(text, text, "file.txt")
	if r.Added != 0 || r.Removed != 0 {
		t.Errorf("identical: want 0/0, got %d/%d", r.Added, r.Removed)
	}
	for _, l := range r.Lines {
		if l.K != diff.LkCtx {
			t.Errorf("identical: expected all context lines, got kind %q", l.K)
		}
	}
}

func TestCompute_allAdded(t *testing.T) {
	r := diff.Compute("", "a\nb\nc\n", "new.txt")
	if r.Added != 3 || r.Removed != 0 {
		t.Errorf("allAdded: want +3/-0, got +%d/-%d", r.Added, r.Removed)
	}
	for _, l := range r.Lines {
		if l.K != diff.LkAdd {
			t.Errorf("allAdded: expected all LkAdd, got %q", l.K)
		}
	}
}

func TestCompute_allRemoved(t *testing.T) {
	r := diff.Compute("a\nb\nc\n", "", "del.txt")
	if r.Added != 0 || r.Removed != 3 {
		t.Errorf("allRemoved: want +0/-3, got +%d/-%d", r.Added, r.Removed)
	}
	for _, l := range r.Lines {
		if l.K != diff.LkDel {
			t.Errorf("allRemoved: expected all LkDel, got %q", l.K)
		}
	}
}

func TestCompute_mixedChange(t *testing.T) {
	old := "alpha\nbeta\ngamma\n"
	new := "alpha\nBETA\ngamma\n"
	r := diff.Compute(old, new, "mix.txt")
	if r.Added != 1 || r.Removed != 1 {
		t.Errorf("mixed: want +1/-1, got +%d/-%d", r.Added, r.Removed)
	}
}

func TestCompute_largeFileSkipsLCS(t *testing.T) {
	// > 800 lines should return counts only, no line-level diff
	big := strings.Repeat("x\n", 801)
	r := diff.Compute(big, big+"extra\n", "large.txt")
	if r.Lines != nil {
		t.Errorf("large file: expected nil Lines, got %d entries", len(r.Lines))
	}
	// counts are set based on line counts, not LCS
	if r.Added == 0 && r.Removed == 0 {
		t.Error("large file: expected non-zero counts")
	}
}

func TestCompute_filename(t *testing.T) {
	r := diff.Compute("a\n", "b\n", "src/main.go")
	if r.Filename != "src/main.go" {
		t.Errorf("filename: want src/main.go, got %q", r.Filename)
	}
}

func TestCompute_lineNumbers(t *testing.T) {
	r := diff.Compute("a\nb\n", "a\nc\n", "f.txt")
	// line 1 (a) is context, line 2 changes
	for _, l := range r.Lines {
		if l.K == diff.LkCtx && l.OldNum == 0 && l.NewNum == 0 {
			t.Errorf("context line missing line numbers: %+v", l)
		}
	}
}

func TestStorePendingTakePending(t *testing.T) {
	r := diff.Compute("old\n", "new\n", "stored.txt")
	diff.StorePending("stored.txt", r)

	got, ok := diff.TakePending("stored.txt")
	if !ok {
		t.Fatal("TakePending: expected to find stored diff")
	}
	if got.Filename != "stored.txt" {
		t.Errorf("TakePending: filename mismatch: %q", got.Filename)
	}

	// second take should find nothing
	_, ok2 := diff.TakePending("stored.txt")
	if ok2 {
		t.Error("TakePending: expected empty after first take")
	}
}
