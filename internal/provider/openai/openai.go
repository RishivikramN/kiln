package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	oai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"

	"kiln/internal/provider"
	"kiln/internal/tools"
)

var openaiModels = []string{
	"gpt-4.1",
	"gpt-4.1-mini",
	"gpt-4o",
	"o3",
	"o4-mini",
}

// OpenAIProvider implements provider.Provider using the OpenAI API.
type OpenAIProvider struct {
	client        oai.Client
	model         string
	name          string
	maxToolCalls  int
	lastInputTok  int
	lastOutputTok int
}

// NewOpenAIProvider creates an OpenAIProvider from the OPENAI_API_KEY environment variable.
func NewOpenAIProvider() (*OpenAIProvider, error) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY not set")
	}
	return &OpenAIProvider{
		client:       oai.NewClient(option.WithAPIKey(key)),
		model:        "gpt-4.1",
		name:         "openai",
		maxToolCalls: provider.DefaultMaxToolCalls,
	}, nil
}

func (p *OpenAIProvider) Name() string        { return p.name }
func (p *OpenAIProvider) ActiveModel() string { return p.model }
func (p *OpenAIProvider) Models() []string    { return openaiModels }

func (p *OpenAIProvider) ContextWindow() int {
	switch p.model {
	case "gpt-4.1", "gpt-4.1-mini":
		return 1000000
	case "gpt-4o":
		return 128000
	default: // o3, o4-mini
		return 200000
	}
}

func (p *OpenAIProvider) Usage() (int, int)     { return p.lastInputTok, p.lastOutputTok }
func (p *OpenAIProvider) SetMaxToolCalls(n int) { p.maxToolCalls = n }

func (p *OpenAIProvider) SetModel(model string) error {
	for _, m := range openaiModels {
		if m == model {
			p.model = model
			return nil
		}
	}
	return fmt.Errorf("unknown openai model: %s", model)
}

func (p *OpenAIProvider) Chat(ctx context.Context, systemPrompt string, messages []provider.Message, providerTools []provider.Tool, onToken func(string), onTool func(name, input, result string), onHistory func(role, content string)) error {
	return openaiChat(ctx, &p.client, p.model, systemPrompt, messages, providerTools, p.maxToolCalls, &p.lastInputTok, &p.lastOutputTok, onToken, onTool, onHistory, false)
}

// OllamaProvider reuses the OpenAI-compatible Ollama endpoint.
type OllamaProvider struct {
	client        oai.Client
	model         string
	models        []string
	maxToolCalls  int
	lastInputTok  int
	lastOutputTok int
}

// NewOllamaProvider creates an OllamaProvider connecting to the local Ollama server.
func NewOllamaProvider() (*OllamaProvider, error) {
	baseURL := os.Getenv("OLLAMA_HOST")
	if baseURL == "" {
		baseURL = "http://localhost:11434/v1"
	} else {
		baseURL = strings.TrimRight(baseURL, "/") + "/v1"
	}
	p := &OllamaProvider{
		client: oai.NewClient(
			option.WithBaseURL(baseURL),
			option.WithAPIKey("ollama"),
		),
		maxToolCalls: provider.DefaultMaxToolCalls,
	}
	models, err := p.fetchModels(baseURL)
	if err != nil {
		return nil, fmt.Errorf("ollama not reachable at %s: %w", baseURL, err)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("ollama has no models pulled — run: ollama pull <model>")
	}
	p.models = models
	p.model = models[0]
	return p, nil
}

func (p *OllamaProvider) fetchModels(baseURL string) ([]string, error) {
	tagsURL := strings.TrimSuffix(baseURL, "/v1") + "/api/tags"
	resp, err := http.Get(tagsURL) //nolint:gosec
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(result.Models))
	for _, m := range result.Models {
		out = append(out, m.Name)
	}
	return out, nil
}

func (p *OllamaProvider) Name() string        { return "ollama" }
func (p *OllamaProvider) ActiveModel() string { return p.model }
func (p *OllamaProvider) Models() []string    { return p.models }
func (p *OllamaProvider) ContextWindow() int  { return 8192 }

func (p *OllamaProvider) Usage() (int, int)     { return p.lastInputTok, p.lastOutputTok }
func (p *OllamaProvider) SetMaxToolCalls(n int) { p.maxToolCalls = n }

func (p *OllamaProvider) SetModel(model string) error {
	for _, m := range p.models {
		if m == model {
			p.model = model
			return nil
		}
	}
	return fmt.Errorf("unknown ollama model: %s", model)
}

func (p *OllamaProvider) Chat(ctx context.Context, systemPrompt string, messages []provider.Message, providerTools []provider.Tool, onToken func(string), onTool func(name, input, result string), onHistory func(role, content string)) error {
	// Ollama models handle tool results better as plain user messages than as
	// structured tool_result blocks (many local models ignore the latter).
	return openaiChat(ctx, &p.client, p.model, systemPrompt, messages, providerTools, p.maxToolCalls, &p.lastInputTok, &p.lastOutputTok, onToken, onTool, onHistory, true)
}

// openaiChat is the shared tool-calling loop for OpenAI and Ollama.
// textToolResults=true sends tool results as plain UserMessages instead of
// structured ToolMessages — required for Ollama models that ignore the latter.
// inTok/outTok are updated with the token counts from each API call (the last
// call's values represent the full-context size and output respectively).
func openaiChat(ctx context.Context, client *oai.Client, model, systemPrompt string, messages []provider.Message, providerTools []provider.Tool, maxToolCalls int, inTok, outTok *int, onToken func(string), onTool func(name, input, result string), onHistory func(role, content string), textToolResults bool) error {
	msgs := toOpenAIMessages(messages)
	if systemPrompt != "" {
		msgs = append([]oai.ChatCompletionMessageParamUnion{oai.SystemMessage(systemPrompt)}, msgs...)
	}

	var oaiTools []oai.ChatCompletionToolParam
	if len(providerTools) > 0 {
		oaiTools = toOpenAITools(providerTools)
	}

	callCount := 0
	for {
		params := oai.ChatCompletionNewParams{
			Model:    model,
			Messages: msgs,
		}
		if len(oaiTools) > 0 {
			params.Tools = oaiTools
		}

		resp, err := client.Chat.Completions.New(ctx, params)
		if err != nil {
			// if the model rejected tool definitions, retry without them
			if len(oaiTools) > 0 && isToolUnsupportedErr(err) {
				oaiTools = nil
				params.Tools = nil
				onTool("notice", "", "model does not support tool calling — retrying without tools")
				resp, err = client.Chat.Completions.New(ctx, params)
				if err != nil {
					return err
				}
			} else {
				return err
			}
		}
		// Capture token usage; updated on every iteration so the last write
		// reflects the largest context (input) and cumulative output.
		if inTok != nil {
			*inTok = int(resp.Usage.PromptTokens)
		}
		if outTok != nil {
			*outTok += int(resp.Usage.CompletionTokens)
		}
		if len(resp.Choices) == 0 {
			return nil
		}

		choice := resp.Choices[0]

		if choice.FinishReason != "tool_calls" || len(choice.Message.ToolCalls) == 0 {
			content := strings.TrimSpace(choice.Message.Content)
			// Some Ollama models emit tool calls as plain JSON text instead of
			// using the structured tool_calls field. Detect and execute them.
			if len(oaiTools) > 0 {
				if name, argsJSON, ok := extractTextToolCall(content); ok {
					result, execErr := tools.RunTool(providerTools, name, argsJSON, "", nil)
					if execErr != nil {
						result = execErr.Error()
					}
					// onTool first (display), then history entries.
					onTool(name, argsJSON, result)
					resultText := "Tool result for " + name + ": " + result
					onHistory(provider.RoleHistAst, content)
					onHistory(provider.RoleHistUsr, resultText)
					msgs = append(msgs, oai.AssistantMessage(content))
					msgs = append(msgs, oai.UserMessage(resultText))
					callCount++
					if callCount > maxToolCalls {
						return fmt.Errorf("tool call limit reached (%d) — model may be looping; use /compact to reduce context", maxToolCalls)
					}
					continue
				}
			}
			for _, r := range content {
				onToken(string(r))
			}
			return nil
		}

		if textToolResults {
			// Ollama structured path: store as text-mode history so future context
			// uses the same plain-text format the model was trained on.
			for _, tc := range choice.Message.ToolCalls {
				synthContent := fmt.Sprintf(`{"name":%q,"arguments":%s}`, tc.Function.Name, tc.Function.Arguments)
				resultText := "Tool result for " + tc.Function.Name + ": "
				result, execErr := tools.RunTool(providerTools, tc.Function.Name, tc.Function.Arguments, "", nil)
				if execErr != nil {
					result = execErr.Error()
				}
				resultText += result
				onHistory(provider.RoleHistAst, synthContent)
				onTool(tc.Function.Name, tc.Function.Arguments, result)
				onHistory(provider.RoleHistUsr, resultText)
				msgs = append(msgs, oai.AssistantMessage(synthContent))
				msgs = append(msgs, oai.UserMessage(resultText))
			}
		} else {
			// OpenAI structured path: serialize the full assistant message so it can
			// be reconstructed with intact tool_call IDs on the next turn.
			msgJSON, _ := json.Marshal(choice.Message)
			onHistory(provider.RoleHistAstOAI, string(msgJSON))
			msgs = append(msgs, choice.Message.ToParam())

			for _, tc := range choice.Message.ToolCalls {
				result, execErr := tools.RunTool(providerTools, tc.Function.Name, tc.Function.Arguments, "", nil)
				if execErr != nil {
					result = execErr.Error()
				}
				onTool(tc.Function.Name, tc.Function.Arguments, result)
				type oaiResult struct {
					ToolCallID string `json:"tool_call_id"`
					Content    string `json:"content"`
				}
				resJSON, _ := json.Marshal(oaiResult{tc.ID, result})
				onHistory(provider.RoleHistUsrOAI, string(resJSON))
				msgs = append(msgs, oai.ToolMessage(result, tc.ID))
			}
		}
		callCount += len(choice.Message.ToolCalls)
		if callCount > maxToolCalls {
			return fmt.Errorf("tool call limit reached (%d) — model may be looping; use /compact to reduce context", maxToolCalls)
		}
	}
}

// isToolUnsupportedErr returns true when the error indicates the model
// doesn't support tool/function calling (common with small local models).
func isToolUnsupportedErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "tool") ||
		strings.Contains(msg, "function") ||
		strings.Contains(msg, "not support") ||
		strings.Contains(msg, "unsupported")
}

func toOpenAITools(providerTools []provider.Tool) []oai.ChatCompletionToolParam {
	out := make([]oai.ChatCompletionToolParam, 0, len(providerTools))
	for _, t := range providerTools {
		out = append(out, oai.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        t.Name,
				Description: oai.String(t.Description),
				Parameters:  shared.FunctionParameters(t.Parameters),
			},
		})
	}
	return out
}

// extractTextToolCall detects when a model emits a tool call as plain JSON text,
// either embedded in prose or wrapped in a markdown code fence.
// Uses json.Decoder so trailing garbage (e.g. extra `}`) is tolerated.
func extractTextToolCall(content string) (name, argsJSON string, ok bool) {
	s := content

	// unwrap markdown code fence if present
	if idx := strings.Index(s, "```"); idx != -1 {
		s = s[idx+3:]
		if nl := strings.IndexByte(s, '\n'); nl != -1 {
			s = s[nl+1:] // skip optional language tag line (e.g. "json")
		}
		if end := strings.Index(s, "```"); end != -1 {
			s = s[:end]
		}
		s = strings.TrimSpace(s)
	}

	// find the start of a tool-call JSON object within prose
	// models often emit: "Please run:\n{\"name\":\"list_files\",...}"
	if start := strings.Index(s, `{"name"`); start != -1 {
		s = s[start:]
	} else if !strings.HasPrefix(strings.TrimSpace(s), "{") {
		return "", "", false
	}

	// decode exactly one JSON object — Decoder ignores trailing bytes/garbage
	var m map[string]any
	if err := json.NewDecoder(strings.NewReader(s)).Decode(&m); err != nil {
		return "", "", false
	}
	n, hasName := m["name"].(string)
	if !hasName {
		return "", "", false
	}
	args := m["arguments"]
	if args == nil {
		args = m["parameters"]
	}
	if args == nil {
		args = map[string]any{}
	}
	b, err := json.Marshal(args)
	if err != nil {
		return "", "", false
	}
	return n, string(b), true
}

func toOpenAIMessages(messages []provider.Message) []oai.ChatCompletionMessageParamUnion {
	var out []oai.ChatCompletionMessageParamUnion
	for _, m := range messages {
		switch m.Role {
		case "user":
			out = append(out, oai.UserMessage(m.Content))
		case "assistant":
			out = append(out, oai.AssistantMessage(m.Content))
		case provider.RoleHistAst:
			out = append(out, oai.AssistantMessage(m.Content))
		case provider.RoleHistUsr:
			out = append(out, oai.UserMessage(m.Content))
		case provider.RoleHistAstOAI:
			// Reconstruct the original assistant message with tool_calls intact.
			var msg oai.ChatCompletionMessage
			if json.Unmarshal([]byte(m.Content), &msg) == nil {
				out = append(out, msg.ToParam())
			}
		case provider.RoleHistUsrOAI:
			var r struct {
				ToolCallID string `json:"tool_call_id"`
				Content    string `json:"content"`
			}
			if json.Unmarshal([]byte(m.Content), &r) == nil {
				out = append(out, oai.ToolMessage(r.Content, r.ToolCallID))
			}
		}
	}
	return out
}
