package permissions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// newTestStore creates an in-memory PermStore backed by a temp file.
func newTestStore(t *testing.T) *PermStore {
	t.Helper()
	return &PermStore{
		path:  filepath.Join(t.TempDir(), "permissions.json"),
		perms: make(map[string]RepoPermission),
	}
}

func TestSetAndGet(t *testing.T) {
	ps := newTestStore(t)
	if err := ps.Set("/repo", "read-write"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	p, ok := ps.Get("/repo")
	if !ok {
		t.Fatal("Get: expected to find /repo")
	}
	if p.Mode != "read-write" {
		t.Errorf("mode: want read-write, got %q", p.Mode)
	}
	if p.UpdatedAt == "" {
		t.Error("UpdatedAt should be set")
	}
}

func TestSet_invalidMode(t *testing.T) {
	ps := newTestStore(t)
	if err := ps.Set("/repo", "superuser"); err == nil {
		t.Error("expected error for invalid mode")
	}
	_, ok := ps.Get("/repo")
	if ok {
		t.Error("invalid-mode Set should not store anything")
	}
}

func TestGet_unknown(t *testing.T) {
	ps := newTestStore(t)
	_, ok := ps.Get("/unknown/path")
	if ok {
		t.Error("Get on unknown path should return ok=false")
	}
}

func TestCanRead(t *testing.T) {
	cases := []struct {
		mode string
		want bool
	}{
		{"read-write", true},
		{"read-only", true},
		{"none", false},
	}
	for _, tc := range cases {
		ps := newTestStore(t)
		ps.Set("/r", tc.mode)
		if got := ps.CanRead("/r"); got != tc.want {
			t.Errorf("CanRead(%q): want %v, got %v", tc.mode, tc.want, got)
		}
	}
}

func TestCanWrite(t *testing.T) {
	cases := []struct {
		mode string
		want bool
	}{
		{"read-write", true},
		{"read-only", false},
		{"none", false},
	}
	for _, tc := range cases {
		ps := newTestStore(t)
		ps.Set("/w", tc.mode)
		if got := ps.CanWrite("/w"); got != tc.want {
			t.Errorf("CanWrite(%q): want %v, got %v", tc.mode, tc.want, got)
		}
	}
}

func TestCanRead_unknownPath(t *testing.T) {
	ps := newTestStore(t)
	if ps.CanRead("/no/such/path") {
		t.Error("CanRead should return false for unknown path")
	}
}

func TestAll_returnsCopy(t *testing.T) {
	ps := newTestStore(t)
	ps.Set("/a", "read-only")
	ps.Set("/b", "read-write")

	all := ps.All()
	if len(all) != 2 {
		t.Errorf("All: want 2 entries, got %d", len(all))
	}
	// mutating the returned map should not affect the store
	delete(all, "/a")
	if _, ok := ps.Get("/a"); !ok {
		t.Error("All returned a live reference, not a copy")
	}
}

func TestPersistence(t *testing.T) {
	ps := newTestStore(t)
	ps.Set("/persist", "read-write")

	// load a new PermStore from the same file
	data, err := os.ReadFile(ps.path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	ps2 := &PermStore{path: ps.path, perms: make(map[string]RepoPermission)}
	if err := json.Unmarshal(data, &ps2.perms); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	p, ok := ps2.Get("/persist")
	if !ok {
		t.Fatal("expected /persist in reloaded store")
	}
	if p.Mode != "read-write" {
		t.Errorf("mode: want read-write, got %q", p.Mode)
	}
}

func TestModeShort(t *testing.T) {
	cases := map[string]string{
		"read-write": "rw",
		"read-only":  "ro",
		"none":       "--",
		"???":        "??",
	}
	for mode, want := range cases {
		if got := ModeShort(mode); got != want {
			t.Errorf("ModeShort(%q): want %q, got %q", mode, want, got)
		}
	}
}
