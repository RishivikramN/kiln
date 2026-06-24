package session_test

import (
	"errors"
	"os"
	"testing"

	"kiln/internal/provider"
	"kiln/internal/session"
)

var testMessages = []provider.Message{
	{Role: "user", Content: "hello"},
	{Role: "assistant", Content: "hi there"},
	{Role: "tool", Content: "read_file", Tokens: 42},
	{Role: "diff", Content: "visual only"},  // must be skipped
	{Role: "system", Content: "welcome msg"}, // must be skipped
	{Role: "hist_usr", Content: "tool result data"},
}

func TestSaveLoad_roundtrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := "/home/user/myproject"
	model := "ollama/qwen3:30b"
	sysp := "be concise"

	if err := session.Save(repo, model, sysp, testMessages); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s, err := session.Load(repo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Model != model {
		t.Errorf("model: want %q got %q", model, s.Model)
	}
	if s.SystemPrompt != sysp {
		t.Errorf("system_prompt: want %q got %q", sysp, s.SystemPrompt)
	}
	// diff and system roles are excluded; 4 roles remain
	if len(s.Messages) != 4 {
		t.Errorf("want 4 messages (diff+system excluded), got %d", len(s.Messages))
	}
	if s.Messages[0].Role != "user" || s.Messages[0].Content != "hello" {
		t.Errorf("messages[0] = %+v", s.Messages[0])
	}
	if s.Messages[2].Tokens != 42 {
		t.Errorf("tokens not preserved: want 42, got %d", s.Messages[2].Tokens)
	}
	if s.SavedAt.IsZero() {
		t.Error("SavedAt should be set")
	}
}

func TestExists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := "/repo/path"

	if session.Exists(repo) {
		t.Error("session should not exist before Save")
	}
	session.Save(repo, "m", "", []provider.Message{{Role: "user", Content: "x"}})
	if !session.Exists(repo) {
		t.Error("session should exist after Save")
	}
}

func TestDelete(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := "/repo/delete-test"

	// Delete on non-existent session must not error.
	if err := session.Delete(repo); err != nil {
		t.Fatalf("Delete on non-existent session: %v", err)
	}

	session.Save(repo, "m", "", []provider.Message{{Role: "user", Content: "x"}})
	if err := session.Delete(repo); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if session.Exists(repo) {
		t.Error("session should not exist after Delete")
	}
}

func TestLoad_notFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := session.Load("/no/such/repo/ever")
	if err == nil {
		t.Fatal("expected error for missing session")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("want os.ErrNotExist, got %v", err)
	}
}

func TestToProviderMessages(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := "/repo/convert"
	session.Save(repo, "m", "", []provider.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello", Tokens: 5},
	})
	s, _ := session.Load(repo)
	msgs := s.ToProviderMessages()
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hi" {
		t.Errorf("msgs[0] = %+v", msgs[0])
	}
	if msgs[1].Tokens != 5 {
		t.Errorf("Tokens not preserved: want 5, got %d", msgs[1].Tokens)
	}
}

func TestDifferentReposDifferentPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p1 := session.Path("/repo/a")
	p2 := session.Path("/repo/b")
	if p1 == p2 {
		t.Error("different repos should produce different session paths")
	}
}
