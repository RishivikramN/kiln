package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsDestructiveTool(t *testing.T) {
	for _, name := range []string{"write_file", "run_command"} {
		if !isDestructiveTool(name) {
			t.Errorf("expected %s to be destructive", name)
		}
	}
	for _, name := range []string{"read_file", "list_files", "grep", ""} {
		if isDestructiveTool(name) {
			t.Errorf("expected %s to not be destructive", name)
		}
	}
}

func TestDescribeToolCall(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"write_file", map[string]any{"path": "src/main.go", "content": "x"}, "write src/main.go"},
		{"run_command", map[string]any{"command": "go test ./..."}, "run: go test ./..."},
		{"write_file", map[string]any{}, "write_file"},      // missing path falls back
		{"run_command", map[string]any{}, "run_command"},    // missing command falls back
		{"list_files", map[string]any{}, "list_files"},      // unknown tool returns name
	}
	for _, tc := range tests {
		got := describeToolCall(tc.name, tc.input)
		if got != tc.want {
			t.Errorf("describeToolCall(%q, %v) = %q, want %q", tc.name, tc.input, got, tc.want)
		}
	}
}

func TestComputePreviewDiff_newFile(t *testing.T) {
	dir := t.TempDir()
	input := map[string]any{
		"path":    "hello.go",
		"content": "package main\n",
	}
	d := computePreviewDiff(dir, input)
	if d == nil {
		t.Fatal("expected non-nil diff for new file")
	}
	if d.Added == 0 {
		t.Errorf("expected added lines > 0 for new file, got %d", d.Added)
	}
	if d.Removed != 0 {
		t.Errorf("expected no removed lines for new file, got %d", d.Removed)
	}
}

func TestComputePreviewDiff_existingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.go")
	os.WriteFile(path, []byte("package main\n\nfunc old() {}\n"), 0644)

	input := map[string]any{
		"path":    "file.go",
		"content": "package main\n\nfunc new() {}\n",
	}
	d := computePreviewDiff(dir, input)
	if d == nil {
		t.Fatal("expected non-nil diff")
	}
	if d.Added == 0 || d.Removed == 0 {
		t.Errorf("expected both added and removed lines; got +%d -%d", d.Added, d.Removed)
	}
	if d.Filename != "file.go" {
		t.Errorf("filename = %q, want %q", d.Filename, "file.go")
	}
}

func TestComputePreviewDiff_noChange(t *testing.T) {
	dir := t.TempDir()
	content := "package main\n"
	path := filepath.Join(dir, "same.go")
	os.WriteFile(path, []byte(content), 0644)

	input := map[string]any{
		"path":    "same.go",
		"content": content,
	}
	d := computePreviewDiff(dir, input)
	if d == nil {
		t.Fatal("expected non-nil diff")
	}
	if d.Added != 0 || d.Removed != 0 {
		t.Errorf("expected no changes; got +%d -%d", d.Added, d.Removed)
	}
}

func TestComputePreviewDiff_missingPath(t *testing.T) {
	d := computePreviewDiff("/any/dir", map[string]any{"content": "x"})
	if d != nil {
		t.Errorf("expected nil diff when path is missing, got %+v", d)
	}
}
