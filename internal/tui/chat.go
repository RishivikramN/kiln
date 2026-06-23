package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"kiln/internal/diff"
	"kiln/internal/permissions"
	"kiln/internal/provider"
	"kiln/internal/tools"
)

func (t *TUI) runChat(ctx context.Context) {
	t.responding = false

	var history []provider.Message
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
	history = pruneOldToolResults(history)

	// add empty assistant placeholder — spinner will animate it
	t.messages = append(t.messages, provider.Message{Role: "assistant", Content: ""})
	idx := len(t.messages) - 1

	// start spinner
	t.mu.Lock()
	t.spinnerStart = time.Now()
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

	// select tools based on permission level
	var providerTools []provider.Tool
	if t.permStore != nil {
		rp, ps := t.repoPath, t.permStore
		var base []provider.Tool
		switch {
		case ps.CanWrite(rp):
			base = tools.DefaultTools() // all 4
		case ps.CanRead(rp):
			base = tools.ReadTools() // list_files + read_file only
		}
		for i := range base {
			orig := base[i].Execute
			base[i].Execute = func(_ string, _ *permissions.PermStore, input map[string]any) (string, error) {
				return orig(rp, ps, input)
			}
		}
		providerTools = base
	}

	sysPrompt := systemPrompt

	// build explicit tool list so the model never has to guess what it has
	var toolNames []string
	for _, tool := range providerTools {
		toolNames = append(toolNames, tool.Name)
	}
	toolList := "none"
	if len(toolNames) > 0 {
		toolList = strings.Join(toolNames, ", ")
	}

	permState := "none — you have no tools. Tell the user to run /permissions rw or /permissions ro before doing anything."
	if t.permStore != nil {
		if p, ok := t.permStore.Get(t.repoPath); ok {
			switch p.Mode {
			case "read-write":
				permState = "read-write"
			case "read-only":
				permState = "read-only"
			}
		}
	}

	sysPrompt += fmt.Sprintf(
		"\n\nCURRENT SESSION:\nWorking directory: %s\nPermission level: %s\nTools you have RIGHT NOW (call these yourself, do not mention them to the user): %s",
		t.repoPath, permState, toolList,
	)

	if t.systemPrompt != "" {
		sysPrompt += "\n\nSession instructions: " + t.systemPrompt
	}

	// Disable Qwen3 extended thinking — it can stall for 15+ minutes per call.
	if t.activeProvider.Name() == "ollama" {
		sysPrompt += "\n/no_think"
	}

	firstToken := true
	err := t.activeProvider.Chat(ctx, sysPrompt, history, providerTools,
		func(token string) {
			if firstToken {
				// stop spinner before first write
				atomic.StoreInt32(&t.spinning, 0)
				<-spinnerDone
				firstToken = false
			}
			t.mu.Lock()
			t.messages[idx].Content += token
			t.mu.Unlock()
			t.render()
		},
		func(name, input, result string) {
			approxTokens := (len(input) + len(result)) / 4
			t.mu.Lock()
			t.messages = append(t.messages, provider.Message{
				Role:    "tool",
				Content: name,
				Tokens:  approxTokens,
			})
			// if write_file ran, retrieve and display the diff
			if name == "write_file" {
				var args struct {
					Path string `json:"path"`
				}
				if json.Unmarshal([]byte(input), &args) == nil && args.Path != "" {
					if d, ok := diff.TakePending(args.Path); ok {
						dr := d
						t.messages = append(t.messages, provider.Message{Role: "diff", Diff: &dr})
					}
				}
			}
			// keep assistant placeholder at end
			last := t.messages[idx]
			t.messages = append(t.messages[:idx], t.messages[idx+1:]...)
			t.messages = append(t.messages, last)
			idx = len(t.messages) - 1
			t.mu.Unlock()
			t.render()
		},
		func(role, content string) {
			// Insert the history message immediately before the assistant placeholder
			// so that tool call context is preserved across conversation turns.
			t.mu.Lock()
			entry := provider.Message{Role: role, Content: content}
			t.messages = append(t.messages[:idx], append([]provider.Message{entry}, t.messages[idx:]...)...)
			idx++ // placeholder shifted one position forward
			t.mu.Unlock()
		},
	)

	// ensure spinner is stopped on error / no tokens
	atomic.StoreInt32(&t.spinning, 0)
	<-spinnerDone

	if err != nil {
		t.mu.Lock()
		t.messages[idx].Content = "error: " + err.Error()
		t.mu.Unlock()
		t.render()
	}
}

// pruneOldToolResults replaces the content of tool-result messages that are
// older than keepTurns user turns with a short stub. This keeps the provider's
// context window from filling up with large file reads from earlier in the
// conversation while preserving the overall conversation shape.
const keepTurns = 3

func pruneOldToolResults(history []provider.Message) []provider.Message {
	turns := 0
	for _, m := range history {
		if m.Role == "user" {
			turns++
		}
	}
	cutoff := turns - keepTurns
	if cutoff <= 0 {
		return history
	}

	result := make([]provider.Message, len(history))
	copy(result, history)

	turn := 0
	for i, m := range result {
		if m.Role == "user" {
			turn++
		}
		if turn > cutoff {
			continue
		}
		switch m.Role {
		case provider.RoleHistUsr:
			result[i].Content = pruneHistUsr(m.Content)
		case provider.RoleHistUsrOAI:
			result[i].Content = pruneHistUsrOAI(m.Content)
		case provider.RoleHistUsrClaude:
			result[i].Content = pruneHistUsrClaude(m.Content)
		case provider.RoleHistUsrGemini:
			result[i].Content = pruneHistUsrGemini(m.Content)
		}
	}
	return result
}

func pruneHistUsr(content string) string {
	// "Tool result for read_file: <file contents>"
	if i := strings.Index(content, ": "); i >= 0 {
		return content[:i+2] + "[pruned]"
	}
	return "[pruned]"
}

func pruneHistUsrOAI(content string) string {
	var r struct {
		ToolCallID string `json:"tool_call_id"`
		Content    string `json:"content"`
	}
	if err := json.Unmarshal([]byte(content), &r); err != nil {
		return content
	}
	r.Content = "[pruned]"
	b, _ := json.Marshal(r)
	return string(b)
}

func pruneHistUsrClaude(content string) string {
	var blocks []json.RawMessage
	if err := json.Unmarshal([]byte(content), &blocks); err != nil {
		return content
	}
	for i, raw := range blocks {
		var block map[string]json.RawMessage
		if err := json.Unmarshal(raw, &block); err != nil {
			continue
		}
		var typ string
		if err := json.Unmarshal(block["type"], &typ); err == nil && typ == "tool_result" {
			block["content"] = json.RawMessage(`"[pruned]"`)
			b, err := json.Marshal(block)
			if err == nil {
				blocks[i] = json.RawMessage(b)
			}
		}
	}
	b, _ := json.Marshal(blocks)
	return string(b)
}

func pruneHistUsrGemini(content string) string {
	var parts []map[string]any
	if err := json.Unmarshal([]byte(content), &parts); err != nil {
		return content
	}
	for i, part := range parts {
		if fr, ok := part["functionResponse"].(map[string]any); ok {
			if resp, ok := fr["response"].(map[string]any); ok {
				resp["output"] = "[pruned]"
				fr["response"] = resp
				parts[i]["functionResponse"] = fr
			}
		}
	}
	b, _ := json.Marshal(parts)
	return string(b)
}
