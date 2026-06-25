package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"kiln/internal/config"
)

func TestLoad_missingFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error for missing file: %v", err)
	}
	if cfg.MaxToolCalls != 50 {
		t.Errorf("want MaxToolCalls=50, got %d", cfg.MaxToolCalls)
	}
	if cfg.ChatTimeoutSecs != 300 {
		t.Errorf("want ChatTimeoutSecs=300, got %d", cfg.ChatTimeoutSecs)
	}
	if !cfg.ConfirmWrites {
		t.Error("want ConfirmWrites=true by default")
	}
	if !cfg.AutoSaveSession {
		t.Error("want AutoSaveSession=true by default")
	}
}

func TestLoad_fromFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	os.MkdirAll(filepath.Join(tmp, ".kiln"), 0700)
	data := []byte(`{"model":"claude/claude-opus-4-8","max_tool_calls":25,"confirm_writes":true}`)
	os.WriteFile(filepath.Join(tmp, ".kiln", "config.json"), data, 0600)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Model != "claude/claude-opus-4-8" {
		t.Errorf("want model=claude/claude-opus-4-8, got %q", cfg.Model)
	}
	if cfg.MaxToolCalls != 25 {
		t.Errorf("want MaxToolCalls=25, got %d", cfg.MaxToolCalls)
	}
	if !cfg.ConfirmWrites {
		t.Error("want ConfirmWrites=true")
	}
	// Unset fields inherit defaults.
	if cfg.ChatTimeoutSecs != 300 {
		t.Errorf("want ChatTimeoutSecs=300 (default), got %d", cfg.ChatTimeoutSecs)
	}
}

func TestLoad_invalidJSON(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	os.MkdirAll(filepath.Join(tmp, ".kiln"), 0700)
	os.WriteFile(filepath.Join(tmp, ".kiln", "config.json"), []byte("{bad json"), 0600)

	_, err := config.Load()
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestLoad_zeroMaxToolCallsKeepsDefault(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	os.MkdirAll(filepath.Join(tmp, ".kiln"), 0700)
	os.WriteFile(filepath.Join(tmp, ".kiln", "config.json"), []byte(`{"max_tool_calls":0}`), 0600)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MaxToolCalls != 50 {
		t.Errorf("zero max_tool_calls should use default (50), got %d", cfg.MaxToolCalls)
	}
}
