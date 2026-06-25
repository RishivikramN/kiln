package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config holds user-configurable settings loaded from ~/.kiln/config.json.
// Missing fields keep their default values.
type Config struct {
	Model           string `json:"model,omitempty"`
	SystemPrompt    string `json:"system_prompt,omitempty"`
	MaxToolCalls    int    `json:"max_tool_calls,omitempty"`
	ChatTimeoutSecs int    `json:"chat_timeout_secs,omitempty"`
	ConfirmWrites   bool   `json:"confirm_writes,omitempty"`
	AutoSaveSession bool   `json:"auto_save_session,omitempty"`
}

func defaults() Config {
	return Config{
		MaxToolCalls:    50,
		ChatTimeoutSecs: 300,
		ConfirmWrites:   true,
		AutoSaveSession: true,
	}
}

// Dir returns the kiln home directory (~/.kiln).
func Dir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".kiln")
}

// Path returns the config file path (~/.kiln/config.json).
func Path() string { return filepath.Join(Dir(), "config.json") }

// Load reads the config file, filling missing fields with defaults.
// Returns defaults without an error if the file does not exist.
func Load() (Config, error) {
	cfg := defaults()
	data, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	// Re-apply defaults for zero-valued numeric fields so that setting
	// "max_tool_calls": 0 in the file doesn't silently disable the guard.
	if cfg.MaxToolCalls == 0 {
		cfg.MaxToolCalls = defaults().MaxToolCalls
	}
	if cfg.ChatTimeoutSecs == 0 {
		cfg.ChatTimeoutSecs = defaults().ChatTimeoutSecs
	}
	return cfg, nil
}
