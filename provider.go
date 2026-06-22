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
	Chat(ctx context.Context, systemPrompt string, messages []Message, tools []Tool, onToken func(string), onTool func(name, input, result string)) error
}
