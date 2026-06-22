package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type RepoPermission struct {
	Mode      string `json:"mode"`       // "read-write", "read-only", "none"
	UpdatedAt string `json:"updated_at"`
}

type PermStore struct {
	path  string
	perms map[string]RepoPermission
}

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

func (s *PermStore) Get(repoPath string) (RepoPermission, bool) {
	p, ok := s.perms[repoPath]
	return p, ok
}

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

func (s *PermStore) CanRead(repoPath string) bool {
	p, ok := s.perms[repoPath]
	return ok && (p.Mode == "read-only" || p.Mode == "read-write")
}

func (s *PermStore) CanWrite(repoPath string) bool {
	p, ok := s.perms[repoPath]
	return ok && p.Mode == "read-write"
}

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

func modeShort(mode string) string {
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
