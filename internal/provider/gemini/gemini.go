package gemini

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

const geminiBaseURL = "https://generativelanguage.googleapis.com/v1beta/models"

var geminiModels = []string{
	"gemini-2.5-flash",
	"gemini-2.5-pro",
	"gemini-2.0-flash",
}

// GeminiProvider implements provider.Provider using the Google Gemini REST API.
type GeminiProvider struct {
	apiKey string
	model  string
}

// NewGeminiProvider creates a GeminiProvider from the GEMINI_API_KEY or GOOGLE_API_KEY environment variable.
func NewGeminiProvider() (*GeminiProvider, error) {
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		key = os.Getenv("GOOGLE_API_KEY")
	}
	if key == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY not set")
	}
	return &GeminiProvider{apiKey: key, model: "gemini-2.5-flash"}, nil
}

func (p *GeminiProvider) Name() string        { return "gemini" }
func (p *GeminiProvider) ActiveModel() string { return p.model }
func (p *GeminiProvider) Models() []string    { return geminiModels }

func (p *GeminiProvider) SetModel(model string) error {
	for _, m := range geminiModels {
		if m == model {
			p.model = model
			return nil
		}
	}
	return fmt.Errorf("unknown gemini model: %s", model)
}

// --- wire types for Gemini REST API ---

type gContent struct {
	Role  string  `json:"role,omitempty"`
	Parts []gPart `json:"parts"`
}

type gPart struct {
	Text             string     `json:"text,omitempty"`
	FunctionCall     *gFuncCall `json:"functionCall,omitempty"`
	FunctionResponse *gFuncResp `json:"functionResponse,omitempty"`
}

type gFuncCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type gFuncResp struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type gSchema struct {
	Type        string              `json:"type,omitempty"`
	Description string              `json:"description,omitempty"`
	Properties  map[string]*gSchema `json:"properties,omitempty"`
	Required    []string            `json:"required,omitempty"`
}

type gFuncDecl struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Parameters  *gSchema `json:"parameters,omitempty"`
}

type gTool struct {
	FunctionDeclarations []gFuncDecl `json:"functionDeclarations"`
}

type gRequest struct {
	Contents          []gContent `json:"contents"`
	SystemInstruction *gContent  `json:"systemInstruction,omitempty"`
	Tools             []gTool    `json:"tools,omitempty"`
}

type gResponse struct {
	Candidates []struct {
		Content struct {
			Parts []gPart `json:"parts"`
			Role  string  `json:"role"`
		} `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (p *GeminiProvider) Chat(ctx context.Context, systemPrompt string, messages []provider.Message, providerTools []provider.Tool, onToken func(string), onTool func(name, input, result string), onHistory func(role, content string)) error {
	req := gRequest{Contents: toGContents(messages)}
	if systemPrompt != "" {
		req.SystemInstruction = &gContent{Parts: []gPart{{Text: systemPrompt}}}
	}
	if len(providerTools) > 0 {
		req.Tools = []gTool{{FunctionDeclarations: toGDecls(providerTools)}}
	}

	for {
		var textParts []string
		var calls []gFuncCall

		if err := p.stream(ctx, req, func(resp gResponse) {
			for _, c := range resp.Candidates {
				for _, part := range c.Content.Parts {
					if part.Text != "" {
						textParts = append(textParts, part.Text)
						onToken(part.Text)
					}
					if part.FunctionCall != nil {
						calls = append(calls, *part.FunctionCall)
					}
				}
			}
		}); err != nil {
			return err
		}

		if len(calls) == 0 {
			return nil
		}

		// append model turn (text + function calls)
		modelParts := make([]gPart, 0, len(textParts)+len(calls))
		for _, t := range textParts {
			modelParts = append(modelParts, gPart{Text: t})
		}
		for i := range calls {
			modelParts = append(modelParts, gPart{FunctionCall: &calls[i]})
		}
		if astJSON, err2 := json.Marshal(modelParts); err2 == nil {
			onHistory(provider.RoleHistAstGemini, string(astJSON))
		}
		req.Contents = append(req.Contents, gContent{Role: "model", Parts: modelParts})

		// execute tools and append results
		resultParts := make([]gPart, 0, len(calls))
		for _, fc := range calls {
			inputJSON := argsToJSON(fc.Args)
			result, err := tools.RunTool(providerTools, fc.Name, inputJSON, "", nil)
			if err != nil {
				result = err.Error()
			}
			onTool(fc.Name, inputJSON, result)
			resultParts = append(resultParts, gPart{
				FunctionResponse: &gFuncResp{
					Name:     fc.Name,
					Response: map[string]any{"output": result},
				},
			})
		}
		if resJSON, err2 := json.Marshal(resultParts); err2 == nil {
			onHistory(provider.RoleHistUsrGemini, string(resJSON))
		}
		req.Contents = append(req.Contents, gContent{Role: "user", Parts: resultParts})
	}
}

func (p *GeminiProvider) stream(ctx context.Context, req gRequest, onChunk func(gResponse)) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/%s:streamGenerateContent?key=%s&alt=sse", geminiBaseURL, p.model, p.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&errBody)
		if errBody.Error.Message != "" {
			return fmt.Errorf("gemini: %s", errBody.Error.Message)
		}
		return fmt.Errorf("gemini: HTTP %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024) // 4 MB — thinking models send large chunks
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var chunk gResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil {
			return fmt.Errorf("gemini: %s", chunk.Error.Message)
		}
		onChunk(chunk)
	}
	return scanner.Err()
}

func toGContents(messages []provider.Message) []gContent {
	var out []gContent
	for _, m := range messages {
		switch m.Role {
		case "user":
			out = append(out, gContent{Role: "user", Parts: []gPart{{Text: m.Content}}})
		case "assistant":
			out = append(out, gContent{Role: "model", Parts: []gPart{{Text: m.Content}}})
		case provider.RoleHistAst:
			out = append(out, gContent{Role: "model", Parts: []gPart{{Text: m.Content}}})
		case provider.RoleHistUsr:
			out = append(out, gContent{Role: "user", Parts: []gPart{{Text: m.Content}}})
		case provider.RoleHistAstGemini:
			var parts []gPart
			if json.Unmarshal([]byte(m.Content), &parts) == nil {
				out = append(out, gContent{Role: "model", Parts: parts})
			}
		case provider.RoleHistUsrGemini:
			var parts []gPart
			if json.Unmarshal([]byte(m.Content), &parts) == nil {
				out = append(out, gContent{Role: "user", Parts: parts})
			}
		}
	}
	return out
}

func toGDecls(providerTools []provider.Tool) []gFuncDecl {
	decls := make([]gFuncDecl, 0, len(providerTools))
	for _, t := range providerTools {
		decls = append(decls, gFuncDecl{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  toGSchema(t.Parameters),
		})
	}
	return decls
}

func toGSchema(params map[string]any) *gSchema {
	s := &gSchema{Type: "OBJECT"}
	if props, ok := params["properties"].(map[string]any); ok {
		s.Properties = make(map[string]*gSchema, len(props))
		for name, v := range props {
			if prop, ok := v.(map[string]any); ok {
				ps := &gSchema{}
				if typ, ok := prop["type"].(string); ok {
					ps.Type = strings.ToUpper(typ)
				}
				if desc, ok := prop["description"].(string); ok {
					ps.Description = desc
				}
				s.Properties[name] = ps
			}
		}
	}
	if req, ok := params["required"].([]string); ok {
		s.Required = req
	}
	return s
}

func argsToJSON(args map[string]any) string {
	b, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	return string(b)
}
