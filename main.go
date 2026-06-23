package main

import (
	"fmt"
	"os"

	"kiln/internal/provider"
	anthropicprovider "kiln/internal/provider/anthropic"
	geminiprovider "kiln/internal/provider/gemini"
	openaiprovider "kiln/internal/provider/openai"
	"kiln/internal/tui"
)

func main() {
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

	if err := t.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
