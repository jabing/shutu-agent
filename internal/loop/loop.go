// Package loop implements the agent loop (design.md §4): a turn is 0..N
// steps, each step being one model request plus the tool calls it initiates.
// The loop is strictly serial and synchronous (D5) and only appends to the
// session log (D1/D3). No product feature may change this structure.
package loop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"personal-agent/internal/llm"
	"personal-agent/internal/prompt"
	"personal-agent/internal/session"
	"personal-agent/internal/tools"
)

// maxSteps bounds the number of tool-call steps in one turn, so a misbehaving
// model cannot loop forever.
const maxSteps = 10

// Loop drives one conversation turn against the session log.
type Loop struct {
	llm     llm.LLM
	log     *session.Log
	tools   *tools.Registry
	prompt  *prompt.Builder
	model   string
	recall  func(context.Context, string) []llm.Message
	onText  func(string) // optional sink for streamed assistant text (REPL)
	onError func(error)  // optional sink for stream errors (REPL)
}

// Config wires the loop's dependencies. All fields are required except the
// optional hooks.
type Config struct {
	LLM    llm.LLM
	Log    *session.Log
	Tools  *tools.Registry
	Prompt *prompt.Builder
	Model  string
	// Recall, if set, is the proactive knowledge recall extension point
	// (design.md §8, D4: new features hang on extension points). It is called
	// once at the start of each turn — after user/message is appended, before
	// the first step's model request — and returns extra context messages
	// injected into that first request only. The recall orchestration (query,
	// KB.Recall, fail-open, kb/recall logging) lives entirely in cmd/pa; the
	// loop just injects what it returns. The turn/step structure is unchanged.
	Recall func(ctx context.Context, userText string) []llm.Message
	// OnText, if set, is called with each streamed assistant text delta.
	OnText func(string)
	// OnError, if set, is called when a step's stream fails after start.
	OnError func(error)
}

// New returns a Loop.
func New(cfg Config) *Loop {
	return &Loop{
		llm:     cfg.LLM,
		log:     cfg.Log,
		tools:   cfg.Tools,
		prompt:  cfg.Prompt,
		model:   cfg.Model,
		recall:  cfg.Recall,
		onText:  cfg.OnText,
		onError: cfg.OnError,
	}
}

// Run executes one turn for the given user input. It appends user/message,
// then runs steps until the model stops requesting tools or maxSteps is hit.
// The supplied context cancels the current step (design.md §4).
func (l *Loop) Run(ctx context.Context, userText string) error {
	if _, err := l.log.Append(session.EventUserMessage, session.NewUserMessage(userText)); err != nil {
		return err
	}
	// The recall context is computed once per turn and applied to the first
	// request only (dsh-knowledge gates its pre-step recall on step === 1).
	var contextMsgs []llm.Message
	if l.recall != nil {
		contextMsgs = l.recall(ctx, userText)
	}
	for step := 0; step < maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("loop: cancelled: %w", err)
		}
		done, err := l.step(ctx, contextMsgs)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		contextMsgs = nil // only the turn's first request carries the recall
	}
	return fmt.Errorf("loop: exceeded %d steps per turn", maxSteps)
}

// step performs one model request and its tool executions. It returns
// (true, nil) when the turn is complete (no tool calls requested). contextMsgs
// are prepended to the request (after the system prompt, before the derived
// history).
func (l *Loop) step(ctx context.Context, contextMsgs []llm.Message) (bool, error) {
	history := l.log.DeriveHistory()
	specs := l.tools.Specs()
	messages := make([]llm.Message, 0, len(history)+1+len(contextMsgs))
	messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: l.prompt.Build()})
	messages = append(messages, contextMsgs...)
	messages = append(messages, history...)

	reader, err := l.llm.Stream(ctx, llm.ChatRequest{Model: l.model, Messages: messages, Tools: specs})
	if err != nil {
		return false, err
	}

	var text strings.Builder
	var calls []llm.ToolCall
	var finishReason string
	for {
		ev, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if l.onError != nil {
				l.onError(err)
			}
			return false, err
		}
		switch ev.Kind {
		case llm.StreamTextDelta:
			text.WriteString(ev.Text)
			if l.onText != nil {
				l.onText(ev.Text)
			}
			if _, err := l.log.Append(session.EventAssistantChunk, session.NewAssistantChunk(ev.Text)); err != nil {
				return false, err
			}
		case llm.StreamFinish:
			calls = ev.ToolCalls
			finishReason = ev.FinishReason
		}
	}

	if _, err := l.log.Append(session.EventAssistantMessage, session.NewAssistantMessage(text.String(), calls, finishReason)); err != nil {
		return false, err
	}
	if len(calls) == 0 {
		return true, nil
	}
	for _, call := range calls {
		if err := ctx.Err(); err != nil {
			return false, fmt.Errorf("loop: cancelled: %w", err)
		}
		res, err := l.tools.Execute(ctx, call.Name, []byte(call.Arguments))
		if err != nil {
			if _, aerr := l.log.Append(session.EventToolError, session.NewToolError(call.ID, call.Name, err.Error())); aerr != nil {
				return false, aerr
			}
		} else {
			var spill *session.SpillRef
			if res.SpillPath != "" {
				spill = &session.SpillRef{Locator: res.SpillPath, Bytes: res.SpillBytes}
			}
			if _, aerr := l.log.Append(session.EventToolResult, session.NewToolResult(call.ID, call.Name, res.Output, spill)); aerr != nil {
				return false, aerr
			}
		}
	}
	return false, nil
}
