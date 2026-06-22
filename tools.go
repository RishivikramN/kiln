package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Tool defines a function the model can call.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON Schema object
	Execute     func(repoPath string, perms *PermStore, input map[string]any) (string, error)
}

// readTools returns only the read-only subset of built-in tools.
func readTools() []Tool {
	all := defaultTools()
	return all[:2] // list_files, read_file
}

// defaultTools returns the built-in file system and shell tools.
func defaultTools() []Tool {
	return []Tool{
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
			Execute: func(repoPath string, perms *PermStore, input map[string]any) (string, error) {
				if perms != nil && !perms.CanRead(repoPath) {
					return "", fmt.Errorf("read permission denied — use /permissions allow")
				}
				rel, _ := input["path"].(string)
				if rel == "" {
					rel = "."
				}
				target, err := safeJoin(repoPath, rel)
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
			Description: "Read the full contents of a file in the repository.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Relative path to the file",
					},
				},
				"required": []string{"path"},
			},
			Execute: func(repoPath string, perms *PermStore, input map[string]any) (string, error) {
				if perms != nil && !perms.CanRead(repoPath) {
					return "", fmt.Errorf("read permission denied — use /permissions allow")
				}
				rel, _ := input["path"].(string)
				target, err := safeJoin(repoPath, rel)
				if err != nil {
					return "", err
				}
				data, err := os.ReadFile(target)
				if err != nil {
					return "", err
				}
				const maxBytes = 40000
				if len(data) > maxBytes {
					return string(data[:maxBytes]) + "\n\n... (file truncated at 40 KB)", nil
				}
				return string(data), nil
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
			Execute: func(repoPath string, perms *PermStore, input map[string]any) (string, error) {
				if perms != nil && !perms.CanWrite(repoPath) {
					return "", fmt.Errorf("write permission denied — use /permissions allow")
				}
				rel, _ := input["path"].(string)
				content, _ := input["content"].(string)
				target, err := safeJoin(repoPath, rel)
				if err != nil {
					return "", err
				}
				if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
					return "", err
				}
				if err := os.WriteFile(target, []byte(content), 0644); err != nil {
					return "", err
				}
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
			Execute: func(repoPath string, perms *PermStore, input map[string]any) (string, error) {
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

// safeJoin joins repoPath and rel, rejecting path traversal attempts.
func safeJoin(repoPath, rel string) (string, error) {
	target := filepath.Join(repoPath, filepath.Clean(rel))
	if !strings.HasPrefix(target+string(filepath.Separator), repoPath+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the repository", rel)
	}
	return target, nil
}

// runTool finds the named tool and executes it with the given JSON input.
func runTool(tools []Tool, name, inputJSON, repoPath string, perms *PermStore) (string, error) {
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
