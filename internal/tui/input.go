package tui

import (
	"strings"

	"kiln/internal/provider"
)

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
		case 12: // Ctrl+L — redraw
			return true
		case 127: // Backspace
			if len(t.input) > 0 {
				t.input = t.input[:len(t.input)-1]
			}
			t.historyIdx = -1
			return true
		case 13: // Enter
			t.submit()
			return !t.quit
		}
	}

	// escape sequences
	if len(b) >= 3 && b[0] == 27 && b[1] == '[' {
		switch {
		case b[2] == 'A': // ↑ — history prev
			t.historyPrev()
		case b[2] == 'B': // ↓ — history next
			t.historyNext()
		case b[2] == '5' && len(b) >= 4 && b[3] == '~': // PgUp — scroll up
			t.scrollOffset += t.height / 2
		case b[2] == '6' && len(b) >= 4 && b[3] == '~': // PgDn — scroll down
			t.scrollOffset -= t.height / 2
			if t.scrollOffset < 0 {
				t.scrollOffset = 0
			}
		}
		return !t.quit
	}

	// printable chars (ASCII + UTF-8)
	if b[0] >= 32 {
		for _, r := range string(b) {
			t.input = append(t.input, r)
		}
		t.historyIdx = -1
	}
	return !t.quit
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
}

func (t *TUI) submit() {
	text := strings.TrimSpace(string(t.input))
	t.input = t.input[:0]
	t.historyIdx = -1
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
