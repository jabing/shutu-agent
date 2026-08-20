# ADR 2026-08-20：M8 消息模型升级——多 provider + reasoning 回传 + 多模态

> 状态：已定 · 背景 → 决策 → 理由 → 后果（含裁剪与放弃）

## 背景

M7（web 搜索）完成后，Agent 部分进入第二阶段。用户 2026-08-20 拍板四项能力中三项列入 **M8**：**多 LLM provider**（必做）、**DeepSeek reasoning 回传**（依附多 provider：跨 provider 重编码会话时需要）、**多模态**（必要）。三者同改 `llm.Message` 消息模型与 wire 层，**打包一次设计，避免改三次**。参照源：dsh `packages/llm/`（`llm` 核心 + `llm-deepseek` 适配器，提交 `7078918` 多模态范式 / `583894f` reasoning 回传）。

**当前 `internal/llm` 局限**（M1 定稿，M1–M7 未动）：`Message.Content` 是 `string`（单文本）；`StreamEvent` 只有 `StreamTextDelta`/`StreamFinish`；`LLM` 接口单一 `Stream`；仅 DeepSeek（OpenAI 兼容 SSE）一个 provider；无图片、无 reasoning。

## 决策

`internal/llm` 消息模型升级为 **content parts + provider 注册表**，三件（多 provider / reasoning / 多模态）一次设计、分三段实施（M8-1 消息模型 → M8-2 多 provider → M8-3 多模态），逐段验收。

### M8-1 消息模型（content parts + reasoning + 事件迁移）

`llm.Message.Content` 从 `string` 升级为 `[]ContentBlock`（tagged struct，照 `WebFetchBody` 的 Kind 判别风格）：

```go
// internal/llm
type ContentBlockKind string
const (
    BlockText     ContentBlockKind = "text"
    BlockReasoning ContentBlockKind = "reasoning"
    BlockImage    ContentBlockKind = "image"
    BlockToolCall ContentBlockKind = "tool-call"
    BlockToolResult ContentBlockKind = "tool-result"
)

type ContentBlock struct {
    Kind      ContentBlockKind
    Text      string       // text / reasoning
    Image     ImageRef     // image：附件引用（见 M8-3）
    CallID    string       // tool-call / tool-result
    Name      string       // tool-call
    Arguments string       // tool-call（raw JSON 字符串）
    IsError   bool         // tool-result
    Blocks    []ContentBlock // tool-result 嵌套
}

type Message struct {
    Role       Role
    Content    []ContentBlock
    ToolCallID string
    ToolCalls  []ToolCall
}
```

- 提供便捷构造/读取：`Text(s string) ContentBlock`、`Message.Text() string`（拼接所有 text block，兼容旧调用）、`HasImage()`。
- `StreamEvent` 增加 `StreamReasoningDelta`（reasoning 增量，与 text delta 平行）+ `StreamFinish` 携带累积 reasoning 文本；`reasoningTokens` 进 `TokenUsage`。
- **wire 层（deepseek）**：assistant 消息序列化时带 `reasoning_content`（OpenAI 兼容字段，dsh 同款）；流式接收解析 `reasoning_content` delta。
- **D3 事件迁移**：`assistant/message` 载荷增加 `content`（blocks 数组，含 reasoning）；新事件类型不需要（assistant/message 已存在，字段演进）；**旧事件回放兼容（D8）**：`assistant/message` 旧数据是纯字符串 → 折叠时检测（有 `content` 字段用 blocks，否则包成单个 text block）；`user/message` 同理（字符串 → text block）。
- 全部使用方同步迁移：`session.DeriveHistory`（折叠）、`loop`（透传）、`deepseek.go`（序列化）、`compaction`（摘要/遮蔽）、`subagent`、`prompt`、`kb` 召回注入（text block 构造）。**一次改完**，不留双轨。

### M8-2 多 provider 注册表

```go
// internal/llm
type Provider interface {
    ID() string
    Available() bool       // 廉价本地检查（key/endpoint 可解析）
    Stream(ctx context.Context, req ChatRequest) (StreamReader, error)
}
type Registry struct{ ... }  // Register / Get / List（D2 三件套：Provider 接口 + 注册表）
```

- `ChatRequest` 增加 `Provider string`（选择 provider 路由，空=默认）。
- config：`llm.provider`（选择路由）+ `llm.providers.*`（各 provider 参数：deepseek / openai / anthropic）。
- 三个 provider：
  - **deepseek**（现有，SSE OpenAI 兼容）
  - **openai**（OpenAI 兼容通用，SSE，复用 deepseek 的序列化/解析思路抽象出共享部分）
  - **anthropic**（Anthropic Messages 流式，**复用 M7 的 Anthropic 兼容 HTTP 客户端心智与 key/headers 构造**；工具调用/block 解析按其 wire 格式）
- **reasoning 跨 provider 保留**：会话日志中 assistant 消息的 reasoning block 是 provider-neutral 的（D3 已落库），换 provider 重编码时按目标 provider wire 规则回传（deepseek/openai → `reasoning_content`；anthropic → `thinking` blocks）。这是"回传"的确切语义。
- 凭证：各 provider key 从各自环境变量读（`DEEPSEEK_API_KEY` / `OPENAI_API_KEY` / `ANTHROPIC_API_KEY`），env-only（纪律 6）。

### M8-3 多模态（图片）

- **附件存储**：`internal/attachment`（或并入 llm）：图片文件存 `<data_dir>/attachments/<id>.<ext>`，元数据 `ImageRef{ID, MediaType, Bytes, Width, Height, Path}`。
- **落库只存引用（dsh `7078918` 范式）**：`user/message` / `assistant/message` 的 `image` block 只落 `ImageRef`（不含 base64/字节）；`DeriveHistory` 还原 `ImageRef`。
- **请求时才转 data URL**：provider 序列化时按 `ImageRef.Path` 读文件 → `data:<mime>;base64,<bytes>`。
- **20MiB 上限 + 最老替换**：`llm.max_request_image_bytes`（默认 20MiB）→ `offloadRequestImages`（照 dsh content.ts：base64 累计超限，最老图片替换为占位符文本"（图片因超限省略；如仍需要请重新附加）"）。
- **inputModalities 能力声明**：config `llm.model_input_modalities`（exact-model：`text` / `text,image`）；收到模型不支持模态（如纯文本模型遇图片）→ fail-closed 报错（照 dsh `UNSUPPORTED_CONTENT` 语义，不静默丢弃）。
- **图片来源**：REPL `/attach <path>`（或后续 web 端）；`/attach` 校验文件（存在、扩展名 png/jpeg/webp/gif、大小上限）、落 `user/message` 图片 block、返回附件 id 提示。默认关（D10，`llm.multimodal.enabled`）。
- **图内嵌工具结果**：tool-result 嵌套图片 block 一并处理（offload 递归）。

### 统一接线

- `cmd/pa`：注册 provider 注册表（按 config 选择）+ `/attach` 命令 + `/llm-status`（provider/model/modalities 状态行，照 `/kb-status` 风格）。
- **不改 `internal/loop/loop.go`**（D4）；主循环串行（D5，`/attach` 与图片读取在工具/命令路径，无后台 goroutine）；零新第三方依赖；CGO-free。
- D3：图片附加与 reasoning 都随 `user/message`/`assistant/message` 落库（模型可见 ⇒ 已落日志）。

## 理由

1. **一次设计避免改三次**（用户定序）：三件都动 `Message`/`StreamEvent`/wire 层，拆开做会反复迁移。
2. **照 dsh 范式**（`7078918`/`583894f`）：content parts + 落库只存引用 + 请求时 data URL + 最老替换 + `reasoning_content`——已被 dsh 生产验证。
3. **provider 注册表走 D2 三件套**：Provider 接口 + 注册表，换 provider 不改消费方（loop/工具只依赖 `LLM` 抽象）。
4. **复用 M7**：Anthropic 兼容 HTTP 客户端（headers/key 构造）直接复用，避免重写。

## 后果

- **迁移成本已估**：`Message.Content` 类型变化波及约 6 个包（session/loop/llm-deepseek/compaction/subagent/prompt）；M8-1 一次改完，`go build ./...` + 全测试是迁移完成判据。
- **事件兼容（D8）**：旧会话日志 `assistant/message`/`user/message` 为纯字符串，折叠时包成 text block；新数据带 `content` 数组。事件 `Version` 字段配合（不 bump 主版本，向下兼容读）。
- **裁剪**：图片编码仅支持 PNG/JPEG/WebP/GIF（dsh 同款）；解码不做（宽高在 `ImageRef` 中仅作元数据，可不解析，未知记 0）；不做图片输出（assistant 图片仅占位，M8 不实现生成式图像）。
- **裁剪**：多模态默认关（D10）；`/attach` 是 REPL 命令，文件经 fs 读入（在权限范围内）。
- **Anthropic provider 的范围**：M8-2 实现 text + tool-call + reasoning（thinking）流式；图片输入（`image_url` 同款 base64）随 M8-3 一起（Anthropic 也支持 base64 image，复用 offload）。
- **token 计量**：`TokenUsage` 增加 `reasoningTokens`（deepseek 提供时）；不引入第三方 tokenizer（估算裁剪，不实现精确计量）。
- **与评测候选的关系**：M8 落定的 `llm.Message`/子代理域是任务评测接缝的引用基础；评测接缝排在 M9 之后、KB 全量之前。
- 本 ADR 与 design.md §1/§3/§4/§9/§11、Agent.md §4 同步更新（双向同步）。

## 验收标准（M8 达标才算完成，三段各自 + 总验收）

**M8-1（消息模型）**：
1. `go vet ./...` + `go test -count=1 ./...` + `go build ./...` 全绿；`Message.Content` 迁移全部使用方，无残留 string-Content 路径。
2. 旧会话日志回放：纯字符串 `assistant/message`/`user/message` 折叠为 text block，历史对话不回归（有测试）。
3. reasoning：assistant 消息 `reasoning_content` 落库（D3）并随 `DeriveHistory` 回传（deepseek wire 正确）。
4. 不改 loop（D4）；零新依赖；CGO-free。

**M8-2（多 provider）**：
5. provider 注册表可注册多 provider；config 切换 provider 后会话历史跨 provider 重编码正确（reasoning 签名保留，有测试）。
6. deepseek / openai / anthropic 三 provider 均可流式出文本与工具调用；anthropic 复用 M7 HTTP 客户端；凭证 env-only。
7. 默认 provider = deepseek，行为与 M7 前一致（回归测试）。

**M8-3（多模态）**：
8. `/attach` 图片：文件校验、落 `user/message` 图片 block（只存引用，不含 base64），请求时转 data URL；有测试。
9. 20MiB 上限最老替换正确；纯文本模型遇图片 fail-closed 报错；tool-result 嵌套图片 offload 正确。
10. 多模态默认关（D10）；`/llm-status` 显示 provider/model/modalities。

**总验收**：三段各自验收标准全部达标才算 M8 完成；主循环保持串行（D5）；`internal/loop/loop.go` 无改动（D4）。
