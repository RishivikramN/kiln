package main

import "context"

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
