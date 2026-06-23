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
