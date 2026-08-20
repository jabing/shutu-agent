# M8-1 派发：llm.Message content parts + reasoning 落库回传 + 全使用方迁移

> 里程碑 M8 消息模型升级（ADR `docs/decisions/2026-08-20-m8-message-model.md`）。本文件是 **M8-1（第一段）** 的自包含契约：`internal/llm` 消息模型升级为 content parts、reasoning 落库并随历史回传、全部使用方一次性迁移、旧会话回放兼容（D8）。M8-2（多 provider 注册表）与 M8-3（多模态 `/attach` 图片 + data URL）在后续派发，**本段不做**。

## 0. 纪律

- **不改 `internal/loop/loop.go` 的 turn/step 结构**（D4）；主循环串行（D5）；**零新第三方依赖**；CGO-free。
- **一次改完不留双轨**：`Message.Content` 迁移到 `[]ContentBlock`，不留 string 字段共存。
- 每个模块完成后**阶段提交**（commit message 前缀 `M8-1`）。
- 迁移判据：`go build ./...` + `go vet ./...` + `go test -count=1 ./...` 全绿，且 grep 不到残留的 `llm.Message` string-Content 路径。

## 1. 范围

**做**：
1. `internal/llm` 类型升级：`ContentBlock`/`ContentBlockKind`/`Message.Content []ContentBlock` + helper（`Text(s)`/`Message.Text()`/`Message.SetText()`/`HasImage()`）。
2. `StreamEvent` 增加 reasoning：`StreamReasoningDelta` kind + `StreamFinish.Reasoning` 累积字段。
3. deepseek wire：assistant 消息序列化带 `reasoning_content`；SSE 流式解析 `reasoning_content` delta → `StreamReasoningDelta`。
4. 事件字段演进（D3）：`assistant/message` 增加 `reasoning` 载荷；`user/message` 增加可选 `content` blocks（**M8-3 预留**，本段不写入，仅加字段 + 折叠读取）；`DeriveHistory` 折叠：Text→text block、Reasoning→reasoning block。
5. **全使用方迁移**（见 §4 清单）。
6. **D8 旧事件回放**：旧日志（纯字符串 user/assistant message）折叠为单个 text block，历史不回归。

**不做（本段）**：图片 `/attach`、data URL、附件存储（M8-3）；provider 注册表（M8-2）；Anthropic/OpenAI 兼容 provider（M8-2）。

## 2. 类型契约（internal/llm）

```go
// content.go（新文件）
type ContentBlockKind string

const (
    BlockText       ContentBlockKind = "text"
    BlockReasoning  ContentBlockKind = "reasoning"
    BlockImage      ContentBlockKind = "image"      // M8-3 使用；本段仅定义
    BlockToolCall   ContentBlockKind = "tool-call"  // 本段不使用（ToolCalls 走 Message 层）
    BlockToolResult ContentBlockKind = "tool-result"
)

type ImageRef struct { // M8-3 使用；本段仅定义
    ID        string
    MediaType string // image/png|jpeg|webp|gif
    Bytes     int64
    Width     int
    Height    int
    Path      string
}

type ContentBlock struct {
    Kind      ContentBlockKind
    Text      string        // BlockText / BlockReasoning
    Image     ImageRef      // BlockImage（M8-3）
    CallID    string        // 预留
    Name      string        // 预留
    Arguments string        // 预留
    IsError   bool          // 预留
    Blocks    []ContentBlock // 预留（嵌套 tool-result）
}

// Text 构造一个文本 block。
func Text(s string) ContentBlock
```

**Message 变更**（`message.go`，原文件改造）：
```go
type Message struct {
    Role       Role
    Content    []ContentBlock   // 原 string
    ToolCallID string
    ToolCalls  []ToolCall
}

// Text 拼接所有 BlockText 的 Text（reasoning 不含在内），兼容旧读取方。
func (m Message) Text() string
// SetText 把 Content 替换为单个 text block（保留 ToolCalls/ToolCallID 不动）。
// 用于 M8-1 的截断/注入路径（这些消息都是纯文本）。
func (m *Message) SetText(s string)
// HasImage 递归判断 content 是否含 image block（M8-3 使用；本段实现便于测试）。
func (m Message) HasImage() bool
```

**StreamEvent 变更**（`llm.go`）：
```go
const (
    StreamTextDelta StreamEventKind = iota
    StreamReasoningDelta   // 新增：推理文本增量
    StreamFinish
)
type StreamEvent struct {
    Kind         StreamEventKind
    Text         string     // StreamTextDelta / StreamReasoningDelta
    Reasoning    string     // StreamFinish：累积推理文本
    FinishReason string
    ToolCalls    []ToolCall
}
```

## 3. deepseek wire 契约（internal/llm/deepseek）

1. `toWireMessage`：assistant 消息（`m.Role == RoleAssistant`）且 `m` 的 content 含 reasoning 文本时，wire 层加 `reasoning_content` 字段（OpenAI 兼容字段，dsh 同款）。
   - **取数**：遍历 `m.Content` 收集 `BlockReasoning` 的 Text 拼起来（helper `m.Reasoning()`，message.go 提供，与 `Text()` 对称）；非 assistant 忽略。
   - **wire 结构**：`wireMessage` 增加 `ReasoningContent string \`json:"reasoning_content,omitempty"\``。
   - content 序列化：本段仍是单文本——取 `m.Text()` 作为 `Content`（`Content: m.Text()`；不实现 parts array，那是 M8-3）。注意 `toWireMessage` 现写 `Content: m.Content`（string），改为 `Content: m.Text()`。
2. `sse.go` 解析：SSE `delta` 除 `content` 外解析 `reasoning_content`（DeepSeek 流式在 delta 里下发 `reasoning_content`）；非空 → 返回 `llm.StreamEvent{Kind: llm.StreamReasoningDelta, Text: <值>}`（与现有 `Delta.Content` 的 text-delta 分支平行）。
3. `StreamFinish` 时：`Reasoning` 累积（reader 内部 `strings.Builder` 收集 reasoning delta，与 text 平行）；tool-call 累积逻辑不变。

## 4. 使用方迁移清单（一次改完）

> 原则：**只动 `llm.Message` 的 `.Content`**。fs/spill/skill/web/mcp 等各自独立的 `Content string` 字段**一律不动**。构造点把 string 包成 `llm.Text(...)`；读取点用 `m.Text()`；截断点用 `SetText`。测试断言 `m.Content != "..."` → `m.Text() != "..."`。

**构造点**（`llm.Message{... Content: <string> ...}` → `Content: []llm.ContentBlock{llm.Text(<string>)}`，可提供 helper 简化）：
- `internal/session/session.go`：335（summary）、353（user）、359-363（assistant：Text→text block + Reasoning→reasoning block 前置）、369-373（tool result）、379-383（tool error）
- `internal/loop/loop.go`：208（system prompt → text block）
- `internal/compaction/service.go`：247/253/259/265；`basic.go`：282
- `cmd/pa/compact.go`：139
- `cmd/pa/kb.go`：112（recall 注入）
- `cmd/pa/skills.go`：107（skill 注入）
- `internal/kb/extract.go`：212（提取帧）
- `internal/subagent/spawn.go`（如构造历史消息处）
- 各测试的构造（`[]llm.Message{{...Content: "..."}}` → 同改）

**读取/截断点**：
- `internal/loop/loop.go`：172/178（`truncateInjectorContext`：`msgs[i].Text()` 算 rune 预算，溢出 `msgs[i].SetText(truncateRunes(...))`）——injector 消息本段全为纯文本，`SetText` 语义成立
- `internal/compaction/basic.go`：255（估算 `e.est(m.Text())`）
- `cmd/pa/compact.go`：46（`len(m.Text())/4`）
- `internal/session/session.go` 折叠逻辑本身（见 §5）

**事件结构**（`internal/session/session.go`）：
- `userMessageData` 增加 `Content []llm.ContentBlock \`json:"content,omitempty"\``（M8-3 预留；本段 DeriveHistory 读到非空则用 blocks，否则 Text 包 text block）
- `assistantMessageData` 增加 `Reasoning string \`json:"reasoning,omitempty"\``
- `NewUserMessage` / `NewAssistantMessage` helper 相应扩参（新增可选参数或用 With* 方式，保持向后兼容现有调用）

## 5. DeriveHistory 折叠契约（D8 兼容）

`internal/session/session.go` `derive`：
- `EventUserMessage`：优先用 `d.Content`（非空时 blocks 原样）；否则 `d.Text` → `[Text(d.Text)]`。`SurfaceOp` 遮蔽逻辑不变（summary 也是 text block）。
- `EventAssistantMessage`：`Text→text block`；`d.Reasoning` 非空 → reasoning block **前置**于 text block（dsh 顺序：reasoning 先、text 后）。`ToolCalls` 不变。
- `EventToolResult` / `EventToolError`：`Output` / `"Error: "+Error` → text block。
- **旧会话回放测试**：构造旧格式事件（只有 text 无 content/reasoning 字段）→ 折叠出单 text block，`Text()` 与旧行为一致。

## 6. 测试要求

- `internal/llm`：`ContentBlock` 构造；`Message.Text()` 拼接多 text block、不含 reasoning；`SetText` 替换；`HasImage`；`StreamEvent` reasoning 字段。
- `internal/llm/deepseek`：assistant 消息带 reasoning → wire `reasoning_content`（httptest 假服务断言请求体）；非 assistant 不带；SSE 假流含 `reasoning_content` delta → reader 产出 `StreamReasoningDelta` + `StreamFinish.Reasoning` 累积；无 reasoning 时旧行为不变（回归）。
- `internal/session`：新格式 user/assistant 事件折叠正确（reasoning 前置）；**旧格式（纯字符串）事件回放不回归**（现有测试改造后用 `Text()` 断言 + 新增旧格式 fixture 测试）。
- `internal/loop`：`truncateInjectorContext` 用 blocks 后行为不变（现有测试改 `Text()`）。
- 全项目：所有 `m.Content` 断言 → `m.Text()`；`go vet/test/build` 绿。

## 7. 提交与报告

- 每模块阶段提交（`M8-1: ...`）：类型+helper → 事件结构+折叠 → deepseek wire → 使用方迁移 → 测试。
- 完成后 `go vet ./...`、`go test -count=1 ./...`、`go build ./...` 全绿；grep 确认无残留 string-Content 的 llm.Message 路径。
- 报告：改动文件清单、实现决策（对照本契约的偏离）、跑过的命令、测试结果。
