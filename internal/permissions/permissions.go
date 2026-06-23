package permissions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RepoPermission stores the access mode for a repository path.
type RepoPermission struct {
	Mode      string `json:"mode"`       // "read-write", "read-only", "none"
	UpdatedAt string `json:"updated_at"`
}

// PermStore persists per-repo permission modes to ~/.kiln/permissions.json.
type PermStore struct {
	path  string
	perms map[string]RepoPermission
}

// LoadPermStore loads (or creates) the permission store from ~/.kiln/permissions.json.
func LoadPermStore() (*PermStore, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".kiln")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, "permissions.json")
	s := &PermStore{path: p, perms: make(map[string]RepoPermission)}

	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.perms); err != nil {
		return nil, fmt.Errorf("corrupt permissions file: %w", err)
	}
	return s, nil
}

// Get returns the permission for the given repo path.
func (s *PermStore) Get(repoPath string) (RepoPermission, bool) {
	p, ok := s.perms[repoPath]
	return p, ok
}

// Set updates the permission mode for the given repo path and persists it.
func (s *PermStore) Set(repoPath, mode string) error {
	switch mode {
	case "read-write", "read-only", "none":
	default:
		return fmt.Errorf("invalid mode %q — use: read-write, read-only, none", mode)
	}
	s.perms[repoPath] = RepoPermission{
		Mode:      mode,
		UpdatedAt: time.Now().Format(time.RFC3339),
	}
	return s.save()
}

// CanRead returns true if the repo has read-only or read-write permission.
func (s *PermStore) CanRead(repoPath string) bool {
	p, ok := s.perms[repoPath]
	return ok && (p.Mode == "read-only" || p.Mode == "read-write")
}

// CanWrite returns true if the repo has read-write permission.
func (s *PermStore) CanWrite(repoPath string) bool {
	p, ok := s.perms[repoPath]
	return ok && p.Mode == "read-write"
}

// All returns a copy of all stored permissions.
func (s *PermStore) All() map[string]RepoPermission {
	out := make(map[string]RepoPermission, len(s.perms))
	for k, v := range s.perms {
		out[k] = v
	}
	return out
}

func (s *PermStore) save() error {
	data, err := json.MarshalIndent(s.perms, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}

// ModeShort returns a short label for a permission mode ("rw", "ro", "--").
func ModeShort(mode string) string {
	switch mode {
	case "read-write":
		return "rw"
	case "read-only":
		return "ro"
	case "none":
		return "--"
	default:
		return "??"
	}
}
