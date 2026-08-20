// attachments.go — the M8-3 multimodal image-attachment wiring
// (dispatch-m8-3 §4 / ADR 2026-08-20-m8-message-model.md 决策 M8-3). registerAttachments
// creates the attachment store under <data_dir>/attachments/ only when
// llm.multimodal.enabled (默认关 D10); /attach validates a local image file
// (extension in SupportedMediaTypes, bytes ≤ max_image_bytes — fail-closed),
// stores it via Store.SaveImage, and logs a user/message event whose content
// carries the image block — only the ImageRef is logged, never the bytes (dsh
// 7078918: 落库只存引用，请求时才转 data URL — M8-3b serializes). Provider
// serialization, offload limits and image fail-closed are M8-3b. The loop's
// turn/step structure is untouched (D4): /attach runs on the serial command
// path (D5).
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"personal-agent/internal/attachment"
	"personal-agent/internal/config"
	"personal-agent/internal/llm"
	"personal-agent/internal/session"
)

// registerAttachments wires the image-attachment store when multimodal is
// enabled (dispatch-m8-3 §4): llm.multimodal.enabled=false ⇒ no store is
// created and /attach is unavailable (D10). The store lives at
// <data_dir>/attachments/ (the empty-dir default is not reachable here — the
// config always carries a DataDir default, but NewStore keeps its own default
// for robustness).
func (a *app) registerAttachments() error {
	if !a.cfg.LLM.Multimodal.Enabled {
		return nil // 默认关（D10）：不创建 store，/attach 不可用
	}
	st, err := attachment.NewStore(filepath.Join(a.cfg.DataDir, "attachments"))
	if err != nil {
		return fmt.Errorf("pa: register attachments: %w", err)
	}
	a.attachStore = st
	return nil
}

// attachCommand implements /attach <path> (dispatch-m8-3 §4): validate the
// file (extension in SupportedMediaTypes, bytes ≤ max_image_bytes — fail-closed
// errors), SaveImage into the attachment store, log a user/message event whose
// content carries the image block (only the ImageRef is logged, never the
// bytes), and print the attachment hint. Multimodal disabled ⇒ error (D10).
// The file is read via os.ReadFile on the serial command path (D5) — the path
// is user-provided and stays a local read, no bytes are ever sent out (凭证/数据
// 安全: env 不外泄).
func (a *app) attachCommand(ctx context.Context, args []string) error {
	if !a.cfg.LLM.Multimodal.Enabled || a.attachStore == nil {
		return fmt.Errorf("multimodal disabled (llm.multimodal.enabled=false)")
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: /attach <path>")
	}
	path := args[0]
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("attach: read %s: %w", path, err)
	}
	ext := strings.ToLower(filepath.Ext(path))
	mediaType, ok := attachment.SupportedMediaTypes[ext]
	if !ok {
		return fmt.Errorf("attach: unsupported image type %q (png/jpg/jpeg/webp/gif)", ext)
	}
	maxBytes := a.cfg.LLM.Multimodal.MaxImageBytes
	if maxBytes <= 0 {
		maxBytes = config.DefaultMultimodalMaxImageBytes
	}
	ref, err := a.attachStore.SaveImage(mediaType, data, maxBytes)
	if err != nil {
		return fmt.Errorf("attach: %w", err)
	}
	if a.log == nil {
		return fmt.Errorf("attach: no active session")
	}
	// 落 user/message 事件：content 含 image block，只存 ImageRef（无字节）。折叠后
	// 成为带 image block 的 user 消息（D3：模型可见 ⇒ 已落日志）。
	if _, err := a.log.Append(session.EventUserMessage,
		session.NewUserMessageWithBlocks("", []llm.ContentBlock{{Kind: llm.BlockImage, Image: ref}})); err != nil {
		return fmt.Errorf("attach: log user message: %w", err)
	}
	// 输出提示（只显示引用元数据，不输出图片字节）。
	fmt.Printf("attached %s as image %s (%s, %d bytes)\n", path, ref.ID, strings.TrimPrefix(ext, "."), ref.Bytes)
	return nil
}
