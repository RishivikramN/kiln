package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"kiln/internal/config"
	"kiln/internal/provider"
	anthropicprovider "kiln/internal/provider/anthropic"
	geminiprovider "kiln/internal/provider/gemini"
	openaiprovider "kiln/internal/provider/openai"
	"kiln/internal/tools"
	"kiln/internal/tui"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not load config:", err)
	}

	// Headless mode: kiln "prompt" — run one turn and exit.
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		runHeadless(args[0], cfg)
		return
	}

	t := tui.NewTUI()

	for _, init := range []func() (provider.Provider, error){
		func() (provider.Provider, error) { return anthropicprovider.NewClaudeProvider() },
		func() (provider.Provider, error) { return openaiprovider.NewOpenAIProvider() },
		func() (provider.Provider, error) { return geminiprovider.NewGeminiProvider() },
		func() (provider.Provider, error) { return openaiprovider.NewOllamaProvider() },
	} {
		if p, err := init(); err == nil {
			t.AddProvider(p)
		}
	}

	t.Configure(tui.Options{
		MaxToolCalls:    cfg.MaxToolCalls,
		ChatTimeout:     time.Duration(cfg.ChatTimeoutSecs) * time.Second,
		ConfirmWrites:   cfg.ConfirmWrites,
		AutoSaveSession: cfg.AutoSaveSession,
		SystemPrompt:    cfg.SystemPrompt,
		DefaultModel:    cfg.Model,
	})

	if err := t.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runHeadless executes a single prompt and writes the response to stdout.
// Uses read-only tools; exits non-zero on error.
func runHeadless(prompt string, cfg config.Config) {
	// Initialise the first available provider.
	var p provider.Provider
	for _, init := range []func() (provider.Provider, error){
		func() (provider.Provider, error) { return anthropicprovider.NewClaudeProvider() },
		func() (provider.Provider, error) { return openaiprovider.NewOpenAIProvider() },
		func() (provider.Provider, error) { return geminiprovider.NewGeminiProvider() },
		func() (provider.Provider, error) { return openaiprovider.NewOllamaProvider() },
	} {
		if pr, err := init(); err == nil {
			p = pr
			break
		}
	}
	if p == nil {
		fmt.Fprintln(os.Stderr, "kiln: no provider available — set an API key or start Ollama")
		os.Exit(1)
	}
	if cfg.MaxToolCalls > 0 {
		p.SetMaxToolCalls(cfg.MaxToolCalls)
	}
	if cfg.Model != "" {
		parts := strings.SplitN(cfg.Model, "/", 2)
		if len(parts) == 2 {
			p.SetModel(parts[1]) //nolint:errcheck — best-effort
		}
	}

	cwd, _ := os.Getwd()
	sysPrompt := tui.BasePrompt()
	sysPrompt += fmt.Sprintf(
		"\n\nCURRENT SESSION:\nWorking directory: %s\nPermission level: read-only\nTools you have RIGHT NOW: list_files, read_file, grep",
		cwd,
	)
	if cfg.SystemPrompt != "" {
		sysPrompt += "\n\nSession instructions: " + cfg.SystemPrompt
	}

	providerTools := tools.ReadTools()

	timeout := time.Duration(cfg.ChatTimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	messages := []provider.Message{{Role: "user", Content: prompt}}
	err := p.Chat(ctx, sysPrompt, messages, providerTools,
		func(token string) { fmt.Print(token) },
		func(name, _, _ string) { fmt.Fprintf(os.Stderr, "  [%s]\n", name) },
		func(_, _ string) {},
	)
	fmt.Println()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kiln:", err)
		os.Exit(1)
	}
}
