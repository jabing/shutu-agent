package llm

import "testing"

func TestTextBuildsTextBlock(t *testing.T) {
	b := Text("hello")
	if b.Kind != BlockText || b.Text != "hello" {
		t.Fatalf("Text() = %+v, want {kind:text text:hello}", b)
	}
}

func TestMessageTextConcatenatesAndExcludesReasoning(t *testing.T) {
	m := Message{Role: RoleAssistant, Content: []ContentBlock{
		{Kind: BlockReasoning, Text: "reasoning here"},
		Text("Hello "),
		Text("world"),
	}}
	if got := m.Text(); got != "Hello world" {
		t.Fatalf("Text() = %q, want %q (reasoning excluded)", got, "Hello world")
	}
	if got := m.Reasoning(); got != "reasoning here" {
		t.Fatalf("Reasoning() = %q, want %q", got, "reasoning here")
	}
}

func TestMessageTextEmpty(t *testing.T) {
	if got := (Message{}).Text(); got != "" {
		t.Fatalf("empty message Text() = %q, want empty", got)
	}
}

func TestMessageSetTextPreservesToolFields(t *testing.T) {
	m := Message{
		Role:       RoleAssistant,
		Content:    []ContentBlock{{Kind: BlockReasoning, Text: "r"}, Text("old")},
		ToolCallID: "ignored",
		ToolCalls:  []ToolCall{{ID: "c1", Name: "get_time", Arguments: "{}"}},
	}
	m.SetText("new")
	if len(m.Content) != 1 || m.Content[0].Kind != BlockText || m.Content[0].Text != "new" {
		t.Fatalf("SetText content = %+v, want a single text block", m.Content)
	}
	if m.Text() != "new" {
		t.Fatalf("Text() = %q, want new", m.Text())
	}
	// Tool fields must be untouched.
	if len(m.ToolCalls) != 1 || m.ToolCalls[0].ID != "c1" || m.ToolCallID != "ignored" {
		t.Fatalf("SetText clobbered tool fields: %+v", m)
	}
}

func TestMessageHasImage(t *testing.T) {
	if (Message{Content: []ContentBlock{Text("x")}}).HasImage() {
		t.Fatal("plain text message must not have an image")
	}
	if !(Message{Content: []ContentBlock{{Kind: BlockImage}}}).HasImage() {
		t.Fatal("message with a top-level image block must have an image")
	}
	// Nested tool-result block (M8-3 recursion).
	nested := Message{Content: []ContentBlock{{
		Kind:   BlockToolResult,
		Blocks: []ContentBlock{{Kind: BlockImage}},
	}}}
	if !nested.HasImage() {
		t.Fatal("message with a nested image block must have an image")
	}
}

func TestStreamEventReasoningField(t *testing.T) {
	ev := StreamEvent{Kind: StreamFinish, Reasoning: "accumulated", FinishReason: "stop"}
	if ev.Reasoning != "accumulated" || ev.FinishReason != "stop" {
		t.Fatalf("finish event = %+v", ev)
	}
	// The reasoning delta kind exists and is distinct from the text delta kind.
	if StreamReasoningDelta == StreamTextDelta {
		t.Fatal("StreamReasoningDelta must be a distinct kind")
	}
	d := StreamEvent{Kind: StreamReasoningDelta, Text: "t"}
	if d.Kind != StreamReasoningDelta {
		t.Fatalf("delta kind = %v, want StreamReasoningDelta", d.Kind)
	}
}
