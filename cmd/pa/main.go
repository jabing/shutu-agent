// Command pa is the personal agent REPL (M1). It wires the thin core — llm,
// session, tools, prompt, loop — and drives turns from stdin. The DeepSeek
// API key is read from the DEEPSEEK_API_KEY environment variable only.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"personal-agent/internal/llm/deepseek"
	"personal-agent/internal/loop"
	"personal-agent/internal/prompt"
	"personal-agent/internal/session"
	"personal-agent/internal/tools"
)

const persona = `You are a personal assistant. You are helpful, concise, and grounded: when an answer depends on current facts or files, use the available tools instead of guessing.`

func main() {
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "pa: DEEPSEEK_API_KEY is not set (API keys only ever come from the environment)")
		os.Exit(1)
	}

	reg := tools.New()
	if err := reg.Register(tools.GetTime{}); err != nil {
		fmt.Fprintln(os.Stderr, "pa:", err)
		os.Exit(1)
	}
	if err := reg.Register(tools.ReadFile{}); err != nil {
		fmt.Fprintln(os.Stderr, "pa:", err)
		os.Exit(1)
	}

	log := session.New()
	client := deepseek.New(deepseek.Config{APIKey: apiKey})

	agent := loop.New(loop.Config{
		LLM:    client,
		Log:    log,
		Tools:  reg,
		Prompt: prompt.New(persona),
		Model:  "deepseek-chat",
		OnText: func(delta string) { fmt.Print(delta) },
		OnError: func(err error) {
			fmt.Fprintln(os.Stderr, "\n[stream error]", err)
		},
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("pa — personal agent REPL (M1). /exit to quit.")
	for {
		fmt.Print("\n> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "/exit" || line == "/quit" {
			break
		}
		if line == "/help" {
			fmt.Println("commands: /exit /quit /help")
			continue
		}
		if err := agent.Run(ctx, line); err != nil {
			fmt.Fprintln(os.Stderr, "\npa:", err)
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "pa:", err)
		os.Exit(1)
	}
}
