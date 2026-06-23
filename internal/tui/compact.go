package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"kiln/internal/provider"
)

const compactSystemPrompt = `You are a helpful assistant. When asked to summarise a conversation, produce a clear, specific summary that covers the main task, key decisions and their rationale, every file that was read or changed (with what changed), and the current state of work. Be concise but specific — omit filler, keep file names and function names exact.`

const compactRequest = `Summarise the conversation above. Include:
- The main task or goal
- Key decisions and why they were made
- Every file that was read or modified, and what changed
- Current state of work and any open issues

Be specific about file names and code. This summary will replace the full conversation history.`

func (t *TUI) runCompact(ctx context.Context) {
	// Collect conversation history to summarise.
	var history []provider.Message
	t.mu.Lock()
	for _, m := range t.messages {
		switch m.Role {
		case "user", "assistant",
			provider.RoleHistAst, provider.RoleHistUsr,
			provider.RoleHistAstOAI, provider.RoleHistUsrOAI,
			provider.RoleHistAstClaude, provider.RoleHistUsrClaude,
			provider.RoleHistAstGemini, provider.RoleHistUsrGemini:
			history = append(history, m)
		}
	}
	t.mu.Unlock()

	if len(history) == 0 {
		t.mu.Lock()
		t.addSystem("nothing to compact")
		t.mu.Unlock()
		return
	}

	// Append the summarisation request as a final user turn.
	history = append(history, provider.Message{Role: "user", Content: compactRequest})

	// Show spinner on a placeholder assistant message.
	t.mu.Lock()
	t.spinnerStart = time.Now()
	t.messages = append(t.messages, provider.Message{Role: "assistant", Content: ""})
	placeholderIdx := len(t.messages) - 1
	t.mu.Unlock()

	atomic.StoreInt32(&t.spinning, 1)
	atomic.StoreInt32(&t.spinnerIdx, 0)
	spinnerDone := make(chan struct{})
	go func() {
		defer close(spinnerDone)
		for atomic.LoadInt32(&t.spinning) == 1 {
			time.Sleep(100 * time.Millisecond)
			atomic.AddInt32(&t.spinnerIdx, 1)
			t.render()
		}
	}()
	t.render()

	var buf strings.Builder
	firstToken := true
	err := t.activeProvider.Chat(ctx, compactSystemPrompt, history, nil,
		func(token string) {
			if firstToken {
				atomic.StoreInt32(&t.spinning, 0)
				<-spinnerDone
				firstToken = false
			}
			t.mu.Lock()
			t.messages[placeholderIdx].Content += token
			t.mu.Unlock()
			buf.WriteString(token)
			t.render()
		},
		func(_, _, _ string) {}, // ignore any tool calls during compact
		func(_, _ string) {},    // ignore history callbacks
	)

	atomic.StoreInt32(&t.spinning, 0)
	<-spinnerDone

	if err != nil {
		t.mu.Lock()
		t.messages[placeholderIdx].Content = "compact failed: " + err.Error()
		t.mu.Unlock()
		t.render()
		return
	}

	summary := buf.String()

	// Replace all messages with a clean slate. The hist_usr/assistant pair seeds
	// context for the next turn without rendering as a visible user prompt bar.
	t.mu.Lock()
	t.messages = []provider.Message{
		{Role: "system", Content: "conversation compacted"},
		{Role: provider.RoleHistUsr, Content: "[conversation summary]"},
		{Role: "assistant", Content: summary},
	}
	t.scrollOffset = 0
	t.mu.Unlock()
}
