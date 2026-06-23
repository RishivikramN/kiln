package provider

import (
	"context"

	"kiln/internal/diff"
	"kiln/internal/permissions"
)

// History role constants used across all providers and the TUI.
const (
	RoleHistAst       = "hist_ast"
	RoleHistUsr       = "hist_usr"
	RoleHistAstOAI    = "hist_ast_oai"
	RoleHistUsrOAI    = "hist_usr_oai"
	RoleHistAstClaude = "hist_ast_claude"
	RoleHistUsrClaude = "hist_usr_claude"
	RoleHistAstGemini = "hist_ast_gemini"
	RoleHistUsrGemini = "hist_usr_gemini"
)

// Tool defines a function the model can call.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON Schema object
	Execute     func(repoPath string, perms *permissions.PermStore, input map[string]any) (string, error)
}

// Message is a single turn in the conversation history.
type Message struct {
	Role    string
	Content string
	Tokens  int          // estimated tokens for tool messages (input+output / 4)
	Diff    *diff.Result // set for "diff" role messages
}

// Provider is the common interface all LLM backends implement.
type Provider interface {
	Name() string
	ActiveModel() string
	SetModel(model string) error
	Models() []string
	// Chat runs a conversation turn. tools may be nil. The provider owns the
	// tool-calling loop: it keeps calling the model until no more tool calls
	// are requested, executes each tool via onTool, and streams text via onToken.
	// onHistory is called for each message the provider adds to its internal
	// conversation (tool-call and tool-result turns) so they can be persisted
	// for subsequent user turns. Roles: hist_ast/hist_usr for text-mode Ollama,
	// hist_ast_oai/hist_usr_oai for OpenAI structured, hist_ast_claude/hist_usr_claude
	// for Claude, hist_ast_gemini/hist_usr_gemini for Gemini.
	Chat(ctx context.Context, systemPrompt string, messages []Message, tools []Tool, onToken func(string), onTool func(name, input, result string), onHistory func(role, content string)) error
}
