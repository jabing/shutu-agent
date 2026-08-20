// Request-level image offload (M8-3b, dispatch-m8-3b §2 / ADR
// 2026-08-20-m8-message-model.md 决策 M8-3). Images are only ever referenced by
// ImageRef in the log and memory (dsh 7078918 范式); the byte budget is
// enforced on a whole request at serialize time: when the cumulative image
// bytes exceed the budget, the images that pushed it over (oldest first) are
// replaced in place by an OffloadedImageText placeholder so the model still
// sees the conversation shape. The providers call OffloadRequestImages in
// Stream — after the HasImage fail-closed check, before serialization.
package llm

// OffloadedImageText is the text block that replaces an image whose bytes push
// the request image budget over its limit (dispatch-m8-3b §2, dsh
// OFFLOADED_IMAGE_TEXT 同款). It becomes a plain text block so the model sees
// a readable placeholder instead of a dropped image.
const OffloadedImageText = "[image omitted]"

// OffloadRequestImages enforces the request image-byte budget (maxBytes; the
// providers' New applies the 20MiB default, dispatch-m8-3b §4) on a chat
// request's messages. The image bytes accumulate in message-history order; an
// image whose addition exceeds the budget is replaced in place by a text block
// carrying OffloadedImageText (oldest first, its position among the message's
// content blocks preserved — like truncateInjectorContext). It recurses into
// nested tool-result blocks, so a nested in-budget image is never dropped while
// an over-budget one is offloaded (ADR M8-3: tool-result 嵌套图片一并 offload).
// When the budget is not exceeded the messages are returned untouched (no copy,
// no side effect). maxBytes <= 0 means no budget (nothing is offloaded).
func OffloadRequestImages(msgs []Message, maxBytes int) []Message {
	if maxBytes <= 0 {
		return msgs
	}
	var acc int64
	for i := range msgs {
		offloadBlocks(&msgs[i].Content, int64(maxBytes), &acc)
	}
	return msgs
}

// offloadBlocks walks one message's content block list, accumulating every
// image block's Bytes into acc (shared across the whole request) and replacing
// any image whose addition pushes the total over maxBytes with an
// OffloadedImageText text block. Nested blocks (tool results) are recursed
// into. The replacement preserves the block's position in the list.
func offloadBlocks(blocks *[]ContentBlock, maxBytes int64, acc *int64) {
	for i := range *blocks {
		b := &(*blocks)[i]
		if b.Kind == BlockImage {
			*acc += b.Image.Bytes
			if *acc > maxBytes {
				(*blocks)[i] = Text(OffloadedImageText)
			}
			continue
		}
		if len(b.Blocks) > 0 {
			offloadBlocks(&b.Blocks, maxBytes, acc)
		}
	}
}
