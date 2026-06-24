package tui

import (
	"fmt"
	"os/exec"
	"strings"

	"kiln/internal/permissions"
	"kiln/internal/session"
)

func (t *TUI) handleCommand(cmd string) {
	parts := strings.Fields(cmd)
	switch parts[0] {
	case "/help":
		t.addSystem(strings.Join([]string{
			"slash commands:",
			"  /models                — list all available models",
			"  /models <name>         — switch model (e.g. openai/gpt-4o)",
			"  /permissions           — show repo permissions",
			"  /permissions allow     — grant read+write access",
			"  /permissions read-only — grant read-only access",
			"  /permissions deny      — revoke access",
			"  /system <prompt>       — set system prompt",
			"  /system                — clear system prompt",
			"  /compact               — summarise conversation to free context",
			"  /undo                  — remove last exchange",
			"  /copy                  — copy last response to clipboard",
			"  /clear                 — clear chat history",
			"  /exit                  — quit",
			"",
			"keyboard:",
			"  ↑ / ↓      — browse input history",
			"  PgUp / PgDn — scroll chat",
			"  Ctrl+L     — redraw",
			"  Ctrl+C     — exit",
		}, "\n"))
	case "/clear":
		t.messages = nil
		t.scrollOffset = 0
	case "/exit":
		t.quit = true
	case "/models":
		t.handleModels(parts[1:])
	case "/permissions":
		t.handlePermissions(parts[1:])
	case "/system":
		t.handleSystem(parts[1:])
	case "/sessions":
		t.handleSessions(parts[1:])
	case "/compact":
		t.handleCompact()
		return // handleCompact launches a goroutine; don't reset scrollOffset yet
	case "/undo":
		t.handleUndo()
	case "/copy":
		t.handleCopy()
	default:
		t.addSystem("unknown command: " + parts[0] + " — type /help for commands")
	}
	if !t.quit {
		t.scrollOffset = 0
	}
}

func (t *TUI) handleModels(args []string) {
	if len(t.providers) == 0 {
		t.addSystem("no providers connected\nset ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY, or start Ollama")
		return
	}
	if len(args) == 0 {
		var lines []string
		for _, pname := range []string{"claude", "openai", "gemini", "ollama"} {
			p, ok := t.providers[pname]
			if !ok {
				continue
			}
			lines = append(lines, fmt.Sprintf("  [%s]", pname))
			for _, m := range p.Models() {
				full := pname + "/" + m
				if t.activeProvider == p && m == p.ActiveModel() {
					lines = append(lines, "    ● "+full)
				} else {
					lines = append(lines, "    ○ "+full)
				}
			}
		}
		t.addSystem("available models — /models <provider/model> to switch:\n" + strings.Join(lines, "\n"))
		return
	}

	target := args[0]
	providerName, modelName, hasSep := strings.Cut(target, "/")
	if !hasSep {
		modelName = providerName
		providerName = ""
	}

	for _, pname := range []string{"claude", "openai", "gemini", "ollama"} {
		if providerName != "" && pname != providerName {
			continue
		}
		p, ok := t.providers[pname]
		if !ok {
			continue
		}
		if err := p.SetModel(modelName); err == nil {
			t.activeProvider = p
			t.model = pname + "/" + p.ActiveModel()
			t.addSystem("switched to " + t.model)
			return
		}
	}
	t.addSystem(fmt.Sprintf("model %q not found — use /models to list available models", target))
}

func (t *TUI) handlePermissions(args []string) {
	if t.permStore == nil {
		t.addSystem("permission store unavailable")
		return
	}
	if len(args) == 0 {
		all := t.permStore.All()
		if len(all) == 0 {
			t.addSystem("no repos registered yet\nuse /permissions allow, /permissions read-only, or /permissions deny")
			return
		}
		var lines []string
		for path, perm := range all {
			marker := "  "
			if path == t.repoPath {
				marker = "● "
			}
			lines = append(lines, fmt.Sprintf("  %s[%s] %s", marker, permissions.ModeShort(perm.Mode), path))
		}
		t.addSystem("repo permissions (● = current):\n" + strings.Join(lines, "\n") +
			"\n\nuse /permissions allow · /permissions read-only · /permissions deny")
		return
	}

	var mode string
	switch args[0] {
	case "allow", "read-write":
		mode = "read-write"
	case "read-only":
		mode = "read-only"
	case "deny", "none":
		mode = "none"
	default:
		t.addSystem("usage: /permissions allow · /permissions read-only · /permissions deny")
		return
	}
	if err := t.permStore.Set(t.repoPath, mode); err != nil {
		t.addSystem("error: " + err.Error())
		return
	}
	t.addSystem(fmt.Sprintf("set %s → %s", t.repo, mode))
}

func (t *TUI) handleSystem(args []string) {
	if len(args) == 0 {
		if t.systemPrompt == "" {
			t.addSystem("no system prompt set\nuse /system <text> to set one")
		} else {
			t.systemPrompt = ""
			t.addSystem("system prompt cleared")
		}
		return
	}
	t.systemPrompt = strings.Join(args, " ")
	t.addSystem(fmt.Sprintf("system prompt set:\n  %s", t.systemPrompt))
}

func (t *TUI) handleUndo() {
	// remove trailing assistant message if present
	if len(t.messages) > 0 && t.messages[len(t.messages)-1].Role == "assistant" {
		t.messages = t.messages[:len(t.messages)-1]
	}
	// remove the user message before it
	if len(t.messages) > 0 && t.messages[len(t.messages)-1].Role == "user" {
		t.messages = t.messages[:len(t.messages)-1]
		t.addSystem("last exchange removed")
	} else {
		t.addSystem("nothing to undo")
	}
}

func (t *TUI) handleSessions(args []string) {
	if len(args) > 0 && args[0] == "clear" {
		if err := session.Delete(t.repoPath); err != nil {
			t.addSystem("sessions: " + err.Error())
		} else {
			t.addSystem("session cleared")
		}
		return
	}
	if session.Exists(t.repoPath) {
		t.addSystem(fmt.Sprintf("session saved for %s\nuse /sessions clear to delete", t.repo))
	} else {
		t.addSystem("no session saved for this repo")
	}
}

func (t *TUI) handleCompact() {
	if t.activeProvider == nil {
		t.addSystem("no provider connected")
		return
	}
	t.mu.Lock()
	if t.chatCancel != nil {
		t.mu.Unlock()
		t.addSystem("wait for current response to finish before compacting")
		return
	}
	ctx, cancel := t.newChatContext()
	t.chatCancel = cancel
	t.mu.Unlock()

	go func() {
		t.runCompact(ctx)
		cancel()
		t.mu.Lock()
		t.chatCancel = nil
		t.mu.Unlock()
		t.render()
	}()
}

func (t *TUI) handleCopy() {
	// find last assistant message
	for i := len(t.messages) - 1; i >= 0; i-- {
		if t.messages[i].Role == "assistant" {
			content := t.messages[i].Content
			cmd := exec.Command("pbcopy")
			cmd.Stdin = strings.NewReader(content)
			if err := cmd.Run(); err != nil {
				t.addSystem("copy failed: " + err.Error())
			} else {
				t.addSystem("copied to clipboard")
			}
			return
		}
	}
	t.addSystem("nothing to copy")
}
