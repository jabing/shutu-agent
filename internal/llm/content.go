// Content parts for llm.Message (M8, ADR 2026-08-20-m8-message-model.md). A
// message's Content is a tagged union of blocks instead of a bare string, so
// reasoning (BlockReasoning), multimodal attachments (BlockImage, M8-3) and
// tool blocks can coexist in one message without double-track types.
package llm

// ContentBlockKind discriminates ContentBlock values (the tagged-union style
// mirrors WebFetchBody's Kind discriminator, design.md §10 D2).
type ContentBlockKind string

const (
	// BlockText is a plain text block (system/user/assistant/tool content).
	BlockText ContentBlockKind = "text"
	// BlockReasoning is the assistant's reasoning text (DeepSeek
	// reasoning_content). It is provider-neutral in the log (D3) and is
	// re-encoded per provider wire rules when replayed (M8-2).
	BlockReasoning ContentBlockKind = "reasoning"
	// BlockImage is an attachment reference (M8-3 uses it; this milestone only
	// defines the type). The log stores the ImageRef, never base64 bytes.
	BlockImage ContentBlockKind = "image"
	// BlockToolCall and BlockToolResult are reserved vocabulary. Tool calls
	// still travel on the Message layer (ToolCalls) in this milestone.
	BlockToolCall   ContentBlockKind = "tool-call"
	BlockToolResult ContentBlockKind = "tool-result"
)

// ImageRef is a reference to an image attachment (M8-3 uses it; this milestone
// only defines the type). Only the reference is logged or carried in memory —
// the bytes are read from Path at request time and turned into a data URL.
type ImageRef struct {
	ID        string
	MediaType string // image/png|jpeg|webp|gif
	Bytes     int64
	Width     int
	Height    int
	Path      string
}

// ContentBlock is one tagged content part of a Message.
type ContentBlock struct {
	Kind ContentBlockKind
	Text string // BlockText / BlockReasoning

	Image ImageRef // BlockImage (M8-3)

	// Reserved for tool blocks (not used this milestone; ToolCalls travel on
	// the Message layer).
	CallID    string
	Name      string
	Arguments string
	IsError   bool
	Blocks    []ContentBlock // nested tool-result
}

// Text builds a plain text block.
func Text(s string) ContentBlock {
	return ContentBlock{Kind: BlockText, Text: s}
}
