package session

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"kiln/internal/provider"
)

// Message is a saved conversation turn. Excludes display-only roles (diff, system).
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Tokens  int    `json:"tokens,omitempty"`
}

// Session is the persisted state of one conversation.
type Session struct {
	Model        string    `json:"model"`
	SystemPrompt string    `json:"system_prompt,omitempty"`
	Messages     []Message `json:"messages"`
	SavedAt      time.Time `json:"saved_at"`
}

func sessionsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".kiln", "sessions")
}

// Path returns the session file path for a given repo directory.
// Uses the first 6 bytes of SHA-256(repoPath) to keep filenames short.
func Path(repoPath string) string {
	h := sha256.Sum256([]byte(repoPath))
	return filepath.Join(sessionsDir(), fmt.Sprintf("%x.json", h[:6]))
}

// Save writes the session to disk. It skips diff and system role messages,
// which are display-only and re-generated on startup.
func Save(repoPath, model, systemPrompt string, messages []provider.Message) error {
	var sm []Message
	for _, m := range messages {
		if m.Role == "diff" || m.Role == "system" {
			continue
		}
		sm = append(sm, Message{Role: m.Role, Content: m.Content, Tokens: m.Tokens})
	}
	s := Session{
		Model:        model,
		SystemPrompt: systemPrompt,
		Messages:     sm,
		SavedAt:      time.Now(),
	}
	if err := os.MkdirAll(sessionsDir(), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(repoPath), data, 0600)
}

// Load reads the saved session for the given repo.
// Returns os.ErrNotExist (wrapped) if no session file exists.
func Load(repoPath string) (*Session, error) {
	data, err := os.ReadFile(Path(repoPath))
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Exists reports whether a saved session exists for repoPath.
func Exists(repoPath string) bool {
	_, err := os.Stat(Path(repoPath))
	return err == nil
}

// Delete removes the saved session for repoPath.
// Returns nil if no session file exists.
func Delete(repoPath string) error {
	err := os.Remove(Path(repoPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ToProviderMessages converts the saved messages back to provider.Message slice.
func (s *Session) ToProviderMessages() []provider.Message {
	out := make([]provider.Message, 0, len(s.Messages))
	for _, m := range s.Messages {
		out = append(out, provider.Message{Role: m.Role, Content: m.Content, Tokens: m.Tokens})
	}
	return out
}
