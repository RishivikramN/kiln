package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"kiln/internal/provider"
	"kiln/internal/tools"
)

const (
	claudeAPIURL = "https://api.anthropic.com/v1/messages"
	claudeAPIVer = "2023-06-01"
)

var claudeModels = []string{
	"claude-sonnet-4-6",
	"claude-opus-4-8",
	"claude-haiku-4-5-20251001",
}

// ClaudeProvider implements provider.Provider using the Anthropic REST API.
type ClaudeProvider struct {
	apiKey string
	model  string
}

// NewClaudeProvider creates a ClaudeProvider from the ANTHROPIC_API_KEY environment variable.
func NewClaudeProvider() (*ClaudeProvider, error) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY not set")
	}
	return &ClaudeProvider{apiKey: key, model: "claude-sonnet-4-6"}, nil
}

func (p *ClaudeProvider) Name() string        { return "claude" }
func (p *ClaudeProvider) ActiveModel() string { return p.model }
func (p *ClaudeProvider) Models() []string    { return claudeModels }
func (p *ClaudeProvider) ContextWindow() int  { return 200000 }

func (p *ClaudeProvider) SetModel(model string) error {
	for _, m := range claudeModels {
		if m == model {
			p.model = model
			return nil
		}
	}
	return fmt.Errorf("unknown claude model: %s", model)
}

// --- wire types for Claude REST API ---

type cMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string | []cBlock
}

type cBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type cTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type cRequest struct {
	Model     string     `json:"model"`
	MaxTokens int        `json:"max_tokens"`
	Stream    bool       `json:"stream"`
	System    string     `json:"system,omitempty"`
	Messages  []cMessage `json:"messages"`
	Tools     []cTool    `json:"tools,omitempty"`
}

type cSSEData struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block,omitempty"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta,omitempty"`
}

func (p *ClaudeProvider) Chat(ctx context.Context, systemPrompt string, messages []provider.Message, providerTools []provider.Tool, onToken func(string), onTool func(name, input, result string), onHistory func(role, content string)) error {
	msgs := toCMessages(messages)
	var cTools []cTool
	for _, t := range providerTools {
		cTools = append(cTools, cTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Parameters,
		})
	}

	type blockState struct {
		kind     string
		id, name string
		inputBuf strings.Builder
	}

	for {
		req := cRequest{
			Model:     p.model,
			MaxTokens: 8192,
			Stream:    true,
			System:    systemPrompt,
			Messages:  msgs,
			Tools:     cTools,
		}

		blocks := map[int]*blockState{}
		var stopReason string

		if err := p.stream(ctx, req, func(ev cSSEData) {
			switch ev.Type {
			case "content_block_start":
				if ev.ContentBlock != nil {
					blocks[ev.Index] = &blockState{
						kind: ev.ContentBlock.Type,
						id:   ev.ContentBlock.ID,
						name: ev.ContentBlock.Name,
					}
				}
			case "content_block_delta":
				b := blocks[ev.Index]
				if b == nil || ev.Delta == nil {
					return
				}
				switch ev.Delta.Type {
				case "text_delta":
					onToken(ev.Delta.Text)
				case "input_json_delta":
					b.inputBuf.WriteString(ev.Delta.PartialJSON)
				}
			case "message_delta":
				if ev.Delta != nil {
					stopReason = ev.Delta.StopReason
				}
			}
		}); err != nil {
			return err
		}

		if stopReason != "tool_use" {
			return nil
		}

		// build assistant content blocks for history
		var assistantBlocks []cBlock
		for idx := 0; ; idx++ {
			b, ok := blocks[idx]
			if !ok {
				break
			}
			if b.kind != "tool_use" {
				continue
			}
			inputRaw := json.RawMessage(b.inputBuf.String())
			if len(inputRaw) == 0 {
				inputRaw = json.RawMessage("{}")
			}
			assistantBlocks = append(assistantBlocks, cBlock{
				Type:  "tool_use",
				ID:    b.id,
				Name:  b.name,
				Input: inputRaw,
			})
		}

		// persist the assistant turn (tool_use blocks) for multi-turn history
		if astJSON, err2 := json.Marshal(assistantBlocks); err2 == nil {
			onHistory(provider.RoleHistAstClaude, string(astJSON))
		}
		msgs = append(msgs, cMessage{Role: "assistant", Content: assistantBlocks})

		// execute tools and collect results
		var resultBlocks []cBlock
		for _, ab := range assistantBlocks {
			result, err := tools.RunTool(providerTools, ab.Name, string(ab.Input), "", nil)
			if err != nil {
				result = err.Error()
			}
			onTool(ab.Name, string(ab.Input), result)
			resultBlocks = append(resultBlocks, cBlock{
				Type:      "tool_result",
				ToolUseID: ab.ID,
				Content:   result,
			})
		}
		// persist the user turn (tool_result blocks) for multi-turn history
		if resJSON, err2 := json.Marshal(resultBlocks); err2 == nil {
			onHistory(provider.RoleHistUsrClaude, string(resJSON))
		}
		msgs = append(msgs, cMessage{Role: "user", Content: resultBlocks})
	}
}

func (p *ClaudeProvider) stream(ctx context.Context, req cRequest, onEvent func(cSSEData)) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeAPIURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", claudeAPIVer)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody map[string]any
		json.NewDecoder(resp.Body).Decode(&errBody)
		return fmt.Errorf("claude API error %d: %v", resp.StatusCode, errBody)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var ev cSSEData
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		onEvent(ev)
	}
	return scanner.Err()
}

func toCMessages(messages []provider.Message) []cMessage {
	var out []cMessage
	for _, m := range messages {
		switch m.Role {
		case "user", "assistant":
			out = append(out, cMessage{Role: m.Role, Content: m.Content})
		case provider.RoleHistAst:
			out = append(out, cMessage{Role: "assistant", Content: m.Content})
		case provider.RoleHistUsr:
			out = append(out, cMessage{Role: "user", Content: m.Content})
		case provider.RoleHistAstClaude:
			var blocks []cBlock
			if json.Unmarshal([]byte(m.Content), &blocks) == nil {
				out = append(out, cMessage{Role: "assistant", Content: blocks})
			}
		case provider.RoleHistUsrClaude:
			var blocks []cBlock
			if json.Unmarshal([]byte(m.Content), &blocks) == nil {
				out = append(out, cMessage{Role: "user", Content: blocks})
			}
		}
	}
	return out
}
