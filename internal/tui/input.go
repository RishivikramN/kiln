package tui

import (
	"strings"

	"kiln/internal/provider"
)

// slashCompletions is the master list of completable slash commands, sorted alphabetically.
var slashCompletions = []string{
	"/clear",
	"/copy",
	"/exit",
	"/help",
	"/models",
	"/permissions",
	"/permissions allow",
	"/permissions deny",
	"/permissions read-only",
	"/system",
	"/undo",
}

// slashDescriptions maps each slash command to a short description shown in the popup.
var slashDescriptions = map[string]string{
	"/clear":                 "clear chat history",
	"/copy":                  "copy last response to clipboard",
	"/exit":                  "quit",
	"/help":                  "show all commands and keyboard shortcuts",
	"/models":                "list or switch models",
	"/permissions":           "show repo permissions",
	"/permissions allow":     "grant read+write access to this repo",
	"/permissions deny":      "revoke access to this repo",
	"/permissions read-only": "grant read-only access to this repo",
	"/system":                "set or clear a custom system prompt",
	"/undo":                  "remove the last exchange",
}

func (t *TUI) handleKey(b []byte) bool {
	if len(b) == 0 {
		return true
	}

	// single-byte control codes
	if len(b) == 1 {
		switch b[0] {
		case 3, 4: // Ctrl+C, Ctrl+D — cancel active request, or exit if none
			t.mu.Lock()
			cancel := t.chatCancel
			t.chatCancel = nil
			t.mu.Unlock()
			if cancel != nil {
				cancel()
				return true
			}
			return false
		case 9: // Tab — accept selected completion (fill without submitting)
			t.acceptCompletion()
			return true
		case 27: // Escape — dismiss autocomplete popup
			if len(t.completions) > 0 {
				t.completions = nil
				t.completionIdx = -1
				return true
			}
			return true
		case 12: // Ctrl+L — redraw
			return true
		case 1: // Ctrl+A — move to beginning
			t.cursorPos = 0
			return true
		case 5: // Ctrl+E — move to end
			t.cursorPos = len(t.input)
			return true
		case 11: // Ctrl+K — delete to end of line
			t.input = t.input[:t.cursorPos]
			t.recomputeCompletions()
			return true
		case 127: // Backspace — delete char before cursor
			if t.cursorPos > 0 {
				t.input = append(t.input[:t.cursorPos-1], t.input[t.cursorPos:]...)
				t.cursorPos--
			}
			t.historyIdx = -1
			t.recomputeCompletions()
			return true
		case 13: // Enter — accept selected completion and submit, or submit as-is
			if len(t.completions) > 0 && t.completionIdx >= 0 {
				t.acceptCompletion()
			}
			t.submit()
			return !t.quit
		}
	}

	// escape sequences — covers ESC+letter, SS3, and CSI variants
	if b[0] == 27 && len(b) >= 2 {
		switch b[1] {
		case 'b': // ESC b — Option+Left (Terminal.app meta key / Natural Text Editing)
			t.cursorWordLeft()
		case 'f': // ESC f — Option+Right
			t.cursorWordRight()
		case 'O': // SS3 (application-mode Home/End sent by some terminals)
			if len(b) >= 3 {
				switch b[2] {
				case 'H':
					t.cursorPos = 0
				case 'F':
					t.cursorPos = len(t.input)
				}
			}
		case '[': // CSI sequences
			if len(b) < 3 {
				break
			}
			// Modifier arrow: ESC [ 1 ; <mod> [CD]
			// mod 3 = Option, mod 5 = Ctrl, mod 9 = Cmd (iTerm2)
			if len(b) >= 6 && b[2] == '1' && b[3] == ';' {
				switch b[5] {
				case 'C':
					switch b[4] {
					case '3', '5': // Option+Right or Ctrl+Right — word right
						t.cursorWordRight()
					case '9': // Cmd+Right — end of line
						t.cursorPos = len(t.input)
					}
				case 'D':
					switch b[4] {
					case '3', '5': // Option+Left or Ctrl+Left — word left
						t.cursorWordLeft()
					case '9': // Cmd+Left — beginning of line
						t.cursorPos = 0
					}
				}
				break
			}
			// Simple and tilde sequences
			switch {
			case b[2] == 'A': // ↑ — completion up, or history prev
				if len(t.completions) > 0 {
					if t.completionIdx > 0 {
						t.completionIdx--
					}
				} else {
					t.historyPrev()
				}
			case b[2] == 'B': // ↓ — completion down, or history next
				if len(t.completions) > 0 {
					if t.completionIdx < len(t.completions)-1 {
						t.completionIdx++
					}
				} else {
					t.historyNext()
				}
			case b[2] == 'C': // → — char right
				if t.cursorPos < len(t.input) {
					t.cursorPos++
				}
			case b[2] == 'D': // ← — char left
				if t.cursorPos > 0 {
					t.cursorPos--
				}
			case b[2] == 'H': // Home
				t.cursorPos = 0
			case b[2] == 'F': // End
				t.cursorPos = len(t.input)
			case b[2] == '1' && len(b) >= 4 && b[3] == '~': // Home (alt)
				t.cursorPos = 0
			case b[2] == '3' && len(b) >= 4 && b[3] == '~': // Delete
				if t.cursorPos < len(t.input) {
					t.input = append(t.input[:t.cursorPos], t.input[t.cursorPos+1:]...)
					t.recomputeCompletions()
				}
			case b[2] == '4' && len(b) >= 4 && b[3] == '~': // End (alt)
				t.cursorPos = len(t.input)
			case b[2] == '5' && len(b) >= 4 && b[3] == '~': // PgUp
				t.scrollOffset += t.height / 2
			case b[2] == '6' && len(b) >= 4 && b[3] == '~': // PgDn
				t.scrollOffset -= t.height / 2
				if t.scrollOffset < 0 {
					t.scrollOffset = 0
				}
			}
		}
		return !t.quit
	}

	// printable chars (ASCII + UTF-8) — insert at cursor position
	if b[0] >= 32 {
		for _, r := range string(b) {
			t.input = append(t.input, 0)
			copy(t.input[t.cursorPos+1:], t.input[t.cursorPos:])
			t.input[t.cursorPos] = r
			t.cursorPos++
		}
		t.historyIdx = -1
		t.recomputeCompletions()
	}
	return !t.quit
}

func (t *TUI) recomputeCompletions() {
	text := string(t.input)
	if !strings.HasPrefix(text, "/") {
		t.completions = nil
		t.completionIdx = -1
		return
	}
	var matches []string
	for _, cmd := range slashCompletions {
		if strings.HasPrefix(cmd, text) {
			matches = append(matches, cmd)
		}
	}
	// hide if the only match is exactly what's already typed
	if len(matches) == 1 && matches[0] == text {
		t.completions = nil
		t.completionIdx = -1
		return
	}
	t.completions = matches
	if len(matches) == 0 {
		t.completionIdx = -1
	} else if t.completionIdx < 0 {
		t.completionIdx = 0
	} else if t.completionIdx >= len(matches) {
		t.completionIdx = len(matches) - 1
	}
}

func (t *TUI) acceptCompletion() {
	if t.completionIdx < 0 || t.completionIdx >= len(t.completions) {
		return
	}
	t.input = []rune(t.completions[t.completionIdx])
	t.cursorPos = len(t.input)
	t.completions = nil
	t.completionIdx = -1
}

func (t *TUI) cursorWordLeft() {
	p := t.cursorPos
	for p > 0 && t.input[p-1] == ' ' {
		p--
	}
	for p > 0 && t.input[p-1] != ' ' {
		p--
	}
	t.cursorPos = p
}

func (t *TUI) cursorWordRight() {
	p := t.cursorPos
	n := len(t.input)
	for p < n && t.input[p] != ' ' {
		p++
	}
	for p < n && t.input[p] == ' ' {
		p++
	}
	t.cursorPos = p
}

func (t *TUI) historyPrev() {
	if len(t.history) == 0 {
		return
	}
	if t.historyIdx == -1 {
		t.historyTmp = string(t.input)
		t.historyIdx = len(t.history) - 1
	} else if t.historyIdx > 0 {
		t.historyIdx--
	}
	t.input = []rune(t.history[t.historyIdx])
	t.cursorPos = len(t.input)
}

func (t *TUI) historyNext() {
	if t.historyIdx == -1 {
		return
	}
	if t.historyIdx < len(t.history)-1 {
		t.historyIdx++
		t.input = []rune(t.history[t.historyIdx])
	} else {
		t.historyIdx = -1
		t.input = []rune(t.historyTmp)
	}
	t.cursorPos = len(t.input)
}

func (t *TUI) submit() {
	text := strings.TrimSpace(string(t.input))
	t.input = t.input[:0]
	t.cursorPos = 0
	t.historyIdx = -1
	t.completions = nil
	t.completionIdx = -1
	if text == "" {
		return
	}

	// record in history (deduplicate consecutive identical entries)
	if len(t.history) == 0 || t.history[len(t.history)-1] != text {
		t.history = append(t.history, text)
	}

	if strings.HasPrefix(text, "/") {
		t.handleCommand(text)
		return
	}
	if t.activeProvider == nil {
		t.addSystem("no provider connected — set an API key and restart")
		return
	}
	t.messages = append(t.messages, provider.Message{Role: "user", Content: text})
	t.responding = true
	t.scrollOffset = 0
}
