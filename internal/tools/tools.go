package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"kiln/internal/diff"
	"kiln/internal/permissions"
	"kiln/internal/provider"
)

// ReadTools returns only the read-only subset of built-in tools.
func ReadTools() []provider.Tool {
	all := DefaultTools()
	return all[:3] // list_files, read_file, grep
}

// DefaultTools returns the built-in file system and shell tools.
func DefaultTools() []provider.Tool {
	return []provider.Tool{
		{
			Name:        "list_files",
			Description: "List files and directories at a path in the repository. Use '.' for the root.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Relative path to list (use '.' for root)",
					},
				},
				"required": []string{"path"},
			},
			Execute: func(repoPath string, perms *permissions.PermStore, input map[string]any) (string, error) {
				if perms != nil && !perms.CanRead(repoPath) {
					return "", fmt.Errorf("read permission denied — use /permissions allow")
				}
				rel, _ := input["path"].(string)
				if rel == "" {
					rel = "."
				}
				target, err := SafeJoin(repoPath, rel)
				if err != nil {
					return "", err
				}
				entries, err := os.ReadDir(target)
				if err != nil {
					return "", err
				}
				var lines []string
				for _, e := range entries {
					name := e.Name()
					if e.IsDir() {
						name += "/"
					}
					lines = append(lines, name)
				}
				if len(lines) == 0 {
					return "(empty directory)", nil
				}
				return strings.Join(lines, "\n"), nil
			},
		},
		{
			Name:        "read_file",
			Description: "Read a file in the repository. Reads the full file by default (capped at 40 KB). Use start_line and end_line (1-indexed, inclusive) to read a specific range of lines — no cap applies when a range is given.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Relative path to the file",
					},
					"start_line": map[string]any{
						"type":        "integer",
						"description": "First line to read, 1-indexed (optional)",
					},
					"end_line": map[string]any{
						"type":        "integer",
						"description": "Last line to read, inclusive (optional)",
					},
				},
				"required": []string{"path"},
			},
			Execute: func(repoPath string, perms *permissions.PermStore, input map[string]any) (string, error) {
				if perms != nil && !perms.CanRead(repoPath) {
					return "", fmt.Errorf("read permission denied — use /permissions allow")
				}
				rel, _ := input["path"].(string)
				target, err := SafeJoin(repoPath, rel)
				if err != nil {
					return "", err
				}
				data, err := os.ReadFile(target)
				if err != nil {
					return "", err
				}
				startRaw, hasStart := input["start_line"]
				endRaw, hasEnd := input["end_line"]
				if hasStart || hasEnd {
					lines := strings.Split(string(data), "\n")
					total := len(lines)
					start, end := 1, total
					if hasStart {
						if n, ok := jsonInt(startRaw); ok && n >= 1 {
							start = n
						}
					}
					if hasEnd {
						if n, ok := jsonInt(endRaw); ok && n >= 1 {
							end = n
						}
					}
					if start > total {
						return fmt.Sprintf("(start_line %d is beyond the end of the file — %d lines total)", start, total), nil
					}
					if end > total {
						end = total
					}
					if end < start {
						end = start
					}
					var sb strings.Builder
					for i, line := range lines[start-1 : end] {
						fmt.Fprintf(&sb, "%d: %s\n", start+i, line)
					}
					return sb.String(), nil
				}
				const maxBytes = 40000
				if len(data) > maxBytes {
					return string(data[:maxBytes]) + "\n\n... (file truncated at 40 KB)", nil
				}
				return string(data), nil
			},
		},
		{
			Name:        "grep",
			Description: "Search for a pattern across files in the repository. Returns matches as file:line:content. Supports regular expressions. Searches recursively when given a directory.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Regular expression or literal string to search for",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "File or directory to search (default: '.' for repo root)",
					},
					"case_insensitive": map[string]any{
						"type":        "boolean",
						"description": "Match case-insensitively (default: false)",
					},
				},
				"required": []string{"pattern"},
			},
			Execute: func(repoPath string, perms *permissions.PermStore, input map[string]any) (string, error) {
				if perms != nil && !perms.CanRead(repoPath) {
					return "", fmt.Errorf("read permission denied — use /permissions allow")
				}
				pattern, _ := input["pattern"].(string)
				if pattern == "" {
					return "", fmt.Errorf("pattern is required")
				}
				rel, _ := input["path"].(string)
				if rel == "" {
					rel = "."
				}
				if ci, _ := input["case_insensitive"].(bool); ci {
					pattern = "(?i)" + pattern
				}
				re, err := regexp.Compile(pattern)
				if err != nil {
					return "", fmt.Errorf("invalid pattern: %w", err)
				}
				target, err := SafeJoin(repoPath, rel)
				if err != nil {
					return "", err
				}
				const maxMatches = 100
				var results []string
				truncated := false
				walkErr := filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
					if err != nil {
						return nil
					}
					if d.IsDir() {
						switch d.Name() {
						case ".git", "node_modules", "vendor", ".cache", "dist", "build":
							return filepath.SkipDir
						}
						return nil
					}
					data, readErr := os.ReadFile(path)
					if readErr != nil {
						return nil
					}
					// skip binary files
					check := data
					if len(check) > 512 {
						check = check[:512]
					}
					if bytes.IndexByte(check, 0) >= 0 {
						return nil
					}
					relPath, _ := filepath.Rel(repoPath, path)
					for i, line := range strings.Split(string(data), "\n") {
						if len(results) >= maxMatches {
							truncated = true
							return filepath.SkipAll
						}
						if re.MatchString(line) {
							if len(line) > 200 {
								line = line[:200] + "…"
							}
							results = append(results, fmt.Sprintf("%s:%d:%s", relPath, i+1, line))
						}
					}
					return nil
				})
				if walkErr != nil {
					return "", walkErr
				}
				if len(results) == 0 {
					return "(no matches)", nil
				}
				out := strings.Join(results, "\n")
				if truncated {
					out += fmt.Sprintf("\n… (capped at %d matches)", maxMatches)
				}
				return out, nil
			},
		},
		{
			Name:        "write_file",
			Description: "Write content to a file in the repository. Creates the file if it does not exist.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Relative path to the file",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "Full content to write to the file",
					},
				},
				"required": []string{"path", "content"},
			},
			Execute: func(repoPath string, perms *permissions.PermStore, input map[string]any) (string, error) {
				if perms != nil && !perms.CanWrite(repoPath) {
					return "", fmt.Errorf("write permission denied — use /permissions allow")
				}
				rel, _ := input["path"].(string)
				content, _ := input["content"].(string)
				target, err := SafeJoin(repoPath, rel)
				if err != nil {
					return "", err
				}
				// read old content for diff (empty string if file is new)
				oldBytes, _ := os.ReadFile(target)
				if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
					return "", err
				}
				if err := os.WriteFile(target, []byte(content), 0644); err != nil {
					return "", err
				}
				// compute and stash the diff for the TUI to pick up in onTool
				d := diff.Compute(string(oldBytes), content, rel)
				diff.StorePending(rel, d)
				return fmt.Sprintf("wrote %d bytes to %s", len(content), rel), nil
			},
		},
		{
			Name:        "run_command",
			Description: "Run a shell command in the repository root. Use for building, testing, grepping, git operations, etc. Timeout: 30s.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "Shell command to run (executed via sh -c)",
					},
				},
				"required": []string{"command"},
			},
			Execute: func(repoPath string, perms *permissions.PermStore, input map[string]any) (string, error) {
				if perms != nil && !perms.CanWrite(repoPath) {
					return "", fmt.Errorf("run_command requires read-write permission — use /permissions allow")
				}
				command, _ := input["command"].(string)
				if command == "" {
					return "", fmt.Errorf("command is required")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "sh", "-c", command)
				cmd.Dir = repoPath
				var out bytes.Buffer
				cmd.Stdout = &out
				cmd.Stderr = &out
				err := cmd.Run()
				result := strings.TrimSpace(out.String())
				if len(result) > 8000 {
					result = result[:8000] + "\n... (truncated)"
				}
				if err != nil && result == "" {
					return "", fmt.Errorf("command failed: %w", err)
				}
				return result, nil
			},
		},
	}
}

// jsonInt converts a JSON-decoded number (float64) or int to int.
func jsonInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}

// SafeJoin joins repoPath and rel, rejecting path traversal attempts.
func SafeJoin(repoPath, rel string) (string, error) {
	target := filepath.Join(repoPath, filepath.Clean(rel))
	if !strings.HasPrefix(target+string(filepath.Separator), repoPath+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the repository", rel)
	}
	return target, nil
}

// RunTool finds the named tool and executes it with the given JSON input.
func RunTool(tools []provider.Tool, name, inputJSON, repoPath string, perms *permissions.PermStore) (string, error) {
	var input map[string]any
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return "", fmt.Errorf("invalid tool input: %w", err)
	}
	for _, t := range tools {
		if t.Name == name {
			return t.Execute(repoPath, perms, input)
		}
	}
	return "", fmt.Errorf("unknown tool: %s", name)
}
