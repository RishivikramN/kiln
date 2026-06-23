package tools_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kiln/internal/diff"
	"kiln/internal/tools"
)

// toolExec looks up a tool by name and runs its Execute function.
func toolExec(t *testing.T, name, repoPath string, input map[string]any) (string, error) {
	t.Helper()
	for _, tool := range tools.DefaultTools() {
		if tool.Name == name {
			return tool.Execute(repoPath, nil, input)
		}
	}
	t.Fatalf("tool %q not found", name)
	return "", nil
}

// ---- SafeJoin ----

func TestSafeJoin_normal(t *testing.T) {
	got, err := tools.SafeJoin("/repo", "src/main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/repo/src/main.go" {
		t.Errorf("want /repo/src/main.go, got %q", got)
	}
}

func TestSafeJoin_dotRelative(t *testing.T) {
	got, err := tools.SafeJoin("/repo", "./subdir/../file.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/repo/file.go" {
		t.Errorf("want /repo/file.go, got %q", got)
	}
}

func TestSafeJoin_rootDot(t *testing.T) {
	got, err := tools.SafeJoin("/repo", ".")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/repo" {
		t.Errorf("want /repo, got %q", got)
	}
}

func TestSafeJoin_traversal(t *testing.T) {
	_, err := tools.SafeJoin("/repo", "../secret")
	if err == nil {
		t.Error("expected error for path traversal, got nil")
	}
}

func TestSafeJoin_deepTraversal(t *testing.T) {
	_, err := tools.SafeJoin("/repo", "foo/../../etc/passwd")
	if err == nil {
		t.Error("expected error for deep path traversal, got nil")
	}
}

// ---- list_files ----

func TestListFiles_normal(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0755)

	out, err := toolExec(t, "list_files", dir, map[string]any{"path": "."})
	if err != nil {
		t.Fatalf("list_files error: %v", err)
	}
	if !strings.Contains(out, "a.txt") {
		t.Errorf("expected a.txt in output: %q", out)
	}
	if !strings.Contains(out, "sub/") {
		t.Errorf("expected sub/ in output: %q", out)
	}
}

func TestListFiles_emptyDir(t *testing.T) {
	dir := t.TempDir()
	out, err := toolExec(t, "list_files", dir, map[string]any{"path": "."})
	if err != nil {
		t.Fatalf("list_files error: %v", err)
	}
	if out != "(empty directory)" {
		t.Errorf("want '(empty directory)', got %q", out)
	}
}

func TestListFiles_traversalRejected(t *testing.T) {
	dir := t.TempDir()
	_, err := toolExec(t, "list_files", dir, map[string]any{"path": "../.."})
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

// ---- read_file ----

func TestReadFile_normal(t *testing.T) {
	dir := t.TempDir()
	want := "hello kiln"
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte(want), 0644)

	out, err := toolExec(t, "read_file", dir, map[string]any{"path": "test.txt"})
	if err != nil {
		t.Fatalf("read_file error: %v", err)
	}
	if out != want {
		t.Errorf("want %q, got %q", want, out)
	}
}

func TestReadFile_notFound(t *testing.T) {
	dir := t.TempDir()
	_, err := toolExec(t, "read_file", dir, map[string]any{"path": "missing.txt"})
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestReadFile_truncatesLargeFile(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", 41000)
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(big), 0644)

	out, err := toolExec(t, "read_file", dir, map[string]any{"path": "big.txt"})
	if err != nil {
		t.Fatalf("read_file error: %v", err)
	}
	if !strings.Contains(out, "truncated") {
		t.Error("expected truncation notice for large file")
	}
	if len(out) > 42000 {
		t.Errorf("output too large after truncation: %d bytes", len(out))
	}
}

func TestReadFile_traversalRejected(t *testing.T) {
	dir := t.TempDir()
	_, err := toolExec(t, "read_file", dir, map[string]any{"path": "../../etc/passwd"})
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

// ---- write_file ----

func TestWriteFile_createsFile(t *testing.T) {
	dir := t.TempDir()
	content := "package main\n"
	out, err := toolExec(t, "write_file", dir, map[string]any{
		"path":    "main.go",
		"content": content,
	})
	if err != nil {
		t.Fatalf("write_file error: %v", err)
	}
	if !strings.Contains(out, "wrote") {
		t.Errorf("unexpected output: %q", out)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "main.go"))
	if string(got) != content {
		t.Errorf("file content mismatch: got %q", got)
	}
	// clean up pending diff side-channel
	diff.TakePending("main.go")
}

func TestWriteFile_createsParentDirs(t *testing.T) {
	dir := t.TempDir()
	_, err := toolExec(t, "write_file", dir, map[string]any{
		"path":    "pkg/sub/file.go",
		"content": "package sub\n",
	})
	if err != nil {
		t.Fatalf("write_file error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "pkg/sub/file.go")); statErr != nil {
		t.Errorf("expected file to exist: %v", statErr)
	}
	diff.TakePending("pkg/sub/file.go")
}

func TestWriteFile_storesPendingDiff(t *testing.T) {
	dir := t.TempDir()
	rel := "difftest.go"
	toolExec(t, "write_file", dir, map[string]any{"path": rel, "content": "v1\n"})
	diff.TakePending(rel) // discard first

	toolExec(t, "write_file", dir, map[string]any{"path": rel, "content": "v2\n"})
	d, ok := diff.TakePending(rel)
	if !ok {
		t.Fatal("expected pending diff after write_file")
	}
	if d.Added == 0 && d.Removed == 0 {
		t.Error("expected non-zero diff counts")
	}
}

func TestWriteFile_traversalRejected(t *testing.T) {
	dir := t.TempDir()
	_, err := toolExec(t, "write_file", dir, map[string]any{
		"path":    "../../evil.sh",
		"content": "rm -rf /",
	})
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

// ---- read_file line range ----

func TestReadFile_lineRange(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "lines.txt"), []byte("one\ntwo\nthree\nfour\nfive\n"), 0644)

	out, err := toolExec(t, "read_file", dir, map[string]any{"path": "lines.txt", "start_line": float64(2), "end_line": float64(4)})
	if err != nil {
		t.Fatalf("read_file line range error: %v", err)
	}
	if !strings.Contains(out, "two") || !strings.Contains(out, "four") {
		t.Errorf("expected lines 2-4 in output: %q", out)
	}
	if strings.Contains(out, "one") || strings.Contains(out, "five") {
		t.Errorf("output should not include lines outside range: %q", out)
	}
}

func TestReadFile_lineRange_includesNumbers(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("a\nb\nc\n"), 0644)

	out, _ := toolExec(t, "read_file", dir, map[string]any{"path": "f.txt", "start_line": float64(2), "end_line": float64(2)})
	if !strings.Contains(out, "2:") {
		t.Errorf("expected line number prefix in output: %q", out)
	}
}

func TestReadFile_lineRange_startBeyondEOF(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "short.txt"), []byte("only one line\n"), 0644)

	out, err := toolExec(t, "read_file", dir, map[string]any{"path": "short.txt", "start_line": float64(99)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "beyond") {
		t.Errorf("expected beyond-EOF message: %q", out)
	}
}

// ---- grep ----

func TestGrep_match(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n\nfunc Hello() {}\n"), 0644)

	out, err := toolExec(t, "grep", dir, map[string]any{"pattern": "func Hello"})
	if err != nil {
		t.Fatalf("grep error: %v", err)
	}
	if !strings.Contains(out, "hello.go") || !strings.Contains(out, "func Hello") {
		t.Errorf("expected match in output: %q", out)
	}
}

func TestGrep_noMatch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "f.go"), []byte("package main\n"), 0644)

	out, err := toolExec(t, "grep", dir, map[string]any{"pattern": "xyz_not_here"})
	if err != nil {
		t.Fatalf("grep error: %v", err)
	}
	if out != "(no matches)" {
		t.Errorf("want '(no matches)', got %q", out)
	}
}

func TestGrep_caseInsensitive(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("Hello World\n"), 0644)

	out, err := toolExec(t, "grep", dir, map[string]any{"pattern": "hello", "case_insensitive": true})
	if err != nil {
		t.Fatalf("grep error: %v", err)
	}
	if !strings.Contains(out, "Hello World") {
		t.Errorf("expected case-insensitive match: %q", out)
	}
}

func TestGrep_caseInsensitive_noMatchWhenFalse(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("Hello World\n"), 0644)

	out, _ := toolExec(t, "grep", dir, map[string]any{"pattern": "hello"})
	if out != "(no matches)" {
		t.Errorf("expected no match for case-sensitive search: %q", out)
	}
}

func TestGrep_specificFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("needle\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("other\n"), 0644)

	out, err := toolExec(t, "grep", dir, map[string]any{"pattern": "needle", "path": "a.go"})
	if err != nil {
		t.Fatalf("grep error: %v", err)
	}
	if strings.Contains(out, "b.go") {
		t.Error("grep on specific file should not search other files")
	}
}

func TestGrep_skipsGitDir(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)
	os.WriteFile(filepath.Join(dir, ".git", "match.txt"), []byte("needle\n"), 0644)
	os.WriteFile(filepath.Join(dir, "code.go"), []byte("nothing\n"), 0644)

	out, _ := toolExec(t, "grep", dir, map[string]any{"pattern": "needle"})
	if strings.Contains(out, ".git") {
		t.Error("grep should skip .git directory")
	}
}

func TestGrep_skipsBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "binary.bin"), []byte("text\x00binary\x00data"), 0644)

	out, _ := toolExec(t, "grep", dir, map[string]any{"pattern": "text"})
	if strings.Contains(out, "binary.bin") {
		t.Error("grep should skip binary files")
	}
}

func TestGrep_invalidPattern(t *testing.T) {
	dir := t.TempDir()
	_, err := toolExec(t, "grep", dir, map[string]any{"pattern": "["})
	if err == nil {
		t.Error("expected error for invalid regex pattern")
	}
}

func TestGrep_traversalRejected(t *testing.T) {
	dir := t.TempDir()
	_, err := toolExec(t, "grep", dir, map[string]any{"pattern": "x", "path": "../../etc"})
	if err == nil {
		t.Error("expected error for path traversal in grep")
	}
}

// ---- run_command ----

func TestRunCommand_success(t *testing.T) {
	dir := t.TempDir()
	out, err := toolExec(t, "run_command", dir, map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("run_command error: %v", err)
	}
	if strings.TrimSpace(out) != "hello" {
		t.Errorf("want 'hello', got %q", out)
	}
}

func TestRunCommand_runsInRepoDir(t *testing.T) {
	dir := t.TempDir()
	out, err := toolExec(t, "run_command", dir, map[string]any{"command": "pwd"})
	if err != nil {
		t.Fatalf("run_command error: %v", err)
	}
	// on macOS, /tmp may be symlinked to /private/tmp — compare base names
	if !strings.HasSuffix(strings.TrimSpace(out), filepath.Base(dir)) {
		t.Errorf("expected output to end with %q, got %q", filepath.Base(dir), out)
	}
}

func TestRunCommand_stderrCaptured(t *testing.T) {
	dir := t.TempDir()
	out, _ := toolExec(t, "run_command", dir, map[string]any{"command": "echo err >&2"})
	if !strings.Contains(out, "err") {
		t.Errorf("expected stderr in output, got %q", out)
	}
}

func TestRunCommand_emptyCommand(t *testing.T) {
	dir := t.TempDir()
	_, err := toolExec(t, "run_command", dir, map[string]any{"command": ""})
	if err == nil {
		t.Error("expected error for empty command")
	}
}
