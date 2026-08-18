// Package llm defines the LLM adapter contract and the streaming event
// protocol shared by every provider. SSE streaming is a hard requirement from
// day one (D6): Stream returns an incremental reader, never a whole-response
// blob.
package llm

import "context"

// Role is a provider-neutral conversation role, mirroring the OpenAI wire
// vocabulary used by the DeepSeek chat completions API.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is one assistant-emitted tool invocation.
type ToolCall struct {
	ID        string // call correlation id, echoed by tool messages
	Name      string // registered tool name
	Arguments string // raw JSON string of the arguments
}

// Message is one entry of a chat history. Exactly one of Content/ToolCallID/
// ToolCalls is meaningful depending on Role (see field comments).
type Message struct {
	Role       Role
	Content    string     // system/user/assistant/tool content
	ToolCallID string     // role=tool only: the call id this result answers
	ToolCalls  []ToolCall // role=assistant only: tool calls this message emitted
}

// ToolSchema declares one callable tool to the model.
type ToolSchema struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON Schema of the arguments
}

// ChatRequest is a single model request: conversation history plus the tools
// the model may call on this step.
type ChatRequest struct {
	Model    string
	Messages []Message
	Tools    []ToolSchema
}

// StreamEventKind discriminates StreamEvent values.
type StreamEventKind int

const (
	// StreamTextDelta carries an incremental piece of assistant text.
	StreamTextDelta StreamEventKind = iota
	// StreamFinish marks the end of the stream with the final finish reason
	// and the complete accumulated tool calls (arguments already joined).
	StreamFinish
)

// StreamEvent is one element read from a model stream.
type StreamEvent struct {
	Kind         StreamEventKind
	Text         string     // StreamTextDelta
	FinishReason string     // StreamFinish: stop | tool_calls | ...
	ToolCalls    []ToolCall // StreamFinish: complete calls in model order
}

// StreamReader yields StreamEvents until io.EOF.
type StreamReader interface {
	Next() (StreamEvent, error)
}

// LLM is the adapter interface every provider implements.
type LLM interface {
	// Stream starts a chat request and returns an incremental reader. The
	// returned reader must honor ctx cancellation.
	Stream(ctx context.Context, req ChatRequest) (StreamReader, error)
}
