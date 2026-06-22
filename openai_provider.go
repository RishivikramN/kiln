package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

var openaiModels = []string{
	"gpt-4.1",
	"gpt-4.1-mini",
	"gpt-4o",
	"o3",
	"o4-mini",
}

type OpenAIProvider struct {
	client openai.Client
	model  string
	name   string
}

func NewOpenAIProvider() (*OpenAIProvider, error) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY not set")
	}
	return &OpenAIProvider{
		client: openai.NewClient(option.WithAPIKey(key)),
		model:  "gpt-4.1",
		name:   "openai",
	}, nil
}

func (p *OpenAIProvider) Name() string        { return p.name }
func (p *OpenAIProvider) ActiveModel() string { return p.model }
func (p *OpenAIProvider) Models() []string    { return openaiModels }

func (p *OpenAIProvider) SetModel(model string) error {
	for _, m := range openaiModels {
		if m == model {
			p.model = model
			return nil
		}
	}
	return fmt.Errorf("unknown openai model: %s", model)
}

func (p *OpenAIProvider) Chat(ctx context.Context, systemPrompt string, messages []Message, tools []Tool, onToken func(string), onTool func(name, input, result string)) error {
	return openaiChat(ctx, &p.client, p.model, systemPrompt, messages, tools, onToken, onTool, false)
}

// OllamaProvider reuses the OpenAI-compatible Ollama endpoint.
type OllamaProvider struct {
	client openai.Client
	model  string
	models []string
}

func NewOllamaProvider() (*OllamaProvider, error) {
	baseURL := os.Getenv("OLLAMA_HOST")
	if baseURL == "" {
		baseURL = "http://localhost:11434/v1"
	} else {
		baseURL = strings.TrimRight(baseURL, "/") + "/v1"
	}
	p := &OllamaProvider{
		client: openai.NewClient(
			option.WithBaseURL(baseURL),
			option.WithAPIKey("ollama"),
		),
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

func (p *OllamaProvider) SetModel(model string) error {
	for _, m := range p.models {
		if m == model {
			p.model = model
			return nil
		}
	}
	return fmt.Errorf("unknown ollama model: %s", model)
}

func (p *OllamaProvider) Chat(ctx context.Context, systemPrompt string, messages []Message, tools []Tool, onToken func(string), onTool func(name, input, result string)) error {
	// Ollama models handle tool results better as plain user messages than as
	// structured tool_result blocks (many local models ignore the latter).
	return openaiChat(ctx, &p.client, p.model, systemPrompt, messages, tools, onToken, onTool, true)
}

// openaiChat is the shared tool-calling loop for OpenAI and Ollama.
// textToolResults=true sends tool results as plain UserMessages instead of
// structured ToolMessages — required for Ollama models that ignore the latter.
func openaiChat(ctx context.Context, client *openai.Client, model, systemPrompt string, messages []Message, tools []Tool, onToken func(string), onTool func(name, input, result string), textToolResults bool) error {
	msgs := toOpenAIMessages(messages)
	if systemPrompt != "" {
		msgs = append([]openai.ChatCompletionMessageParamUnion{openai.SystemMessage(systemPrompt)}, msgs...)
	}

	var oaiTools []openai.ChatCompletionToolParam
	if len(tools) > 0 {
		oaiTools = toOpenAITools(tools)
	}

	for {
		params := openai.ChatCompletionNewParams{
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
					result, execErr := runTool(tools, name, argsJSON, "", nil)
					if execErr != nil {
						result = execErr.Error()
					}
					onTool(name, argsJSON, result)
					msgs = append(msgs, openai.AssistantMessage(content))
					msgs = append(msgs, openai.UserMessage("Tool result for "+name+": "+result))
					continue
				}
			}
			for _, r := range content {
				onToken(string(r))
			}
			return nil
		}

		// Add assistant message with tool calls to history.
		msgs = append(msgs, choice.Message.ToParam())

		// Execute each tool call and add results.
		for _, tc := range choice.Message.ToolCalls {
			result, execErr := runTool(tools, tc.Function.Name, tc.Function.Arguments, "", nil)
			if execErr != nil {
				result = execErr.Error()
			}
			onTool(tc.Function.Name, tc.Function.Arguments, result)
			if textToolResults {
				msgs = append(msgs, openai.UserMessage("Tool result for "+tc.Function.Name+": "+result))
			} else {
				msgs = append(msgs, openai.ToolMessage(result, tc.ID))
			}
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

func toOpenAITools(tools []Tool) []openai.ChatCompletionToolParam {
	out := make([]openai.ChatCompletionToolParam, 0, len(tools))
	for _, t := range tools {
		out = append(out, openai.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        t.Name,
				Description: openai.String(t.Description),
				Parameters:  shared.FunctionParameters(t.Parameters),
			},
		})
	}
	return out
}

// extractTextToolCall detects when a model emits a tool call as plain JSON text,
// optionally wrapped in a markdown code fence. Returns the parsed name + args.
func extractTextToolCall(content string) (name, argsJSON string, ok bool) {
	// strip markdown code fence: ```json ... ``` or ``` ... ```
	s := content
	if idx := strings.Index(s, "```"); idx != -1 {
		s = s[idx+3:]
		if nl := strings.IndexByte(s, '\n'); nl != -1 {
			s = s[nl+1:] // skip optional language tag line
		}
		if end := strings.Index(s, "```"); end != -1 {
			s = s[:end]
		}
		s = strings.TrimSpace(s)
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return "", "", false
	}
	n, ok := m["name"].(string)
	if !ok {
		return "", "", false
	}
	args := m["arguments"]
	if args == nil {
		args = m["parameters"]
	}
	b, err := json.Marshal(args)
	if err != nil {
		return "", "", false
	}
	return n, string(b), true
}

func toOpenAIMessages(messages []Message) []openai.ChatCompletionMessageParamUnion {
	var out []openai.ChatCompletionMessageParamUnion
	for _, m := range messages {
		switch m.Role {
		case "user":
			out = append(out, openai.UserMessage(m.Content))
		case "assistant":
			out = append(out, openai.AssistantMessage(m.Content))
		}
	}
	return out
}
