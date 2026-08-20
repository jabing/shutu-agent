# M8-2a 派发：LLM provider 注册表 + config 选择 + deepseek/openai 适配 + 接线

> 里程碑 M8 第二段（ADR `docs/decisions/2026-08-20-m8-message-model.md`）。本文件是 **M8-2a（前半）** 契约：Provider 接口 + 注册表 + config 多 provider 选择 + deepseek/openai 适配 + 组合根接线。**M8-2b（anthropic provider，新 wire）** 在后续派发。前置：M8-1 已交付 content parts Message / reasoning / `StreamEvent`（只读）。

## 0. 纪律

- **不改 `internal/loop/loop.go` 的 turn/step 结构**（D4）；主循环串行（D5）；**零新第三方依赖**；CGO-free；原有测试全绿。
- 凭证 env-only（纪律 6）：`DEEPSEEK_API_KEY` / `OPENAI_API_KEY`。
- 每模块阶段提交（commit message 前缀 `M8-2`）。

## 1. 范围

**做**：
1. `internal/llm/provider.go`：`Provider` 接口 + `Registry`（D2 三件套）。
2. `ChatRequest` 增加 `Provider string` 字段（选择 provider 路由；空 = 默认）。
3. `internal/llm/deepseek`：`Client` 实现 `Provider`（`ID()`/`Available()`）。
4. **openai provider 复用 deepseek.Client**（DeepSeek API 即 OpenAI 兼容 SSE；用 `deepseek.New(Config{BaseURL: <openai base>, APIKey: <OPENAI_API_KEY>, Model: ...})` 构造，`ID()="openai"`）——**零新 wire 代码**。`Available()` 检查 `OPENAI_API_KEY` 非空 + base_url 可解析。
5. config：`LLMConfig`（`provider` 默认 `deepseek` + `providers.openai` / `providers.anthropic` 参数）+ applyDefaults；顶层 `model`/`base_url` 保留为 deepseek 默认配置（兼容现有 config.yaml）。
6. `cmd/pa`：创建 `Registry`，注册 deepseek + openai（按 config 可用性），按 `cfg.LLM.Provider` 取 provider 传给 loop；`/llm-status` 状态行（照 `/kb-status` 风格）。
7. 默认 provider=deepseek 回归（行为与 M8-1 前一致）。

**不做（本段）**：anthropic provider（M8-2b）；图片/多模态（M8-3）；reasoning 跨 anthropic 回传（M8-2b）。

## 2. Provider 接口 + Registry 契约（internal/llm）

```go
// provider.go（新文件）
// Provider 是一个 LLM 后端（D2）。消费方（loop/组合根）只依赖本接口。
type Provider interface {
    ID() string              // 稳定 id（"deepseek" / "openai" / "anthropic"）
    Available() bool         // 廉价本地检查（key/endpoint 可解析），绝不做网络调用
    Stream(ctx context.Context, req ChatRequest) (StreamReader, error)
}

// Registry 是多 provider 注册表（D2）。
type Registry struct{ ... }
func NewRegistry() *Registry
func (r *Registry) Register(p Provider) error          // id 重复报错
func (r *Registry) Get(id string) (Provider, error)    // 不存在报错
func (r *Registry) List() []Provider                   // 注册顺序
```

**ChatRequest 变更**（llm.go）：
```go
type ChatRequest struct {
    Provider string      // 选择 provider 路由；空 = 默认（组合根决定）
    Model    string
    Messages []Message
    Tools    []ToolSchema
}
```

> loop 不感知 Provider 字段（仍调 `l.llm.Stream`，组合根把已选中的 provider 注入 loop）。`ChatRequest.Provider` 保留为面向未来的显式路由字段（M8-2b 或工具层直接调 provider 时用）。

## 3. deepseek 适配契约

`internal/llm/deepseek`：
- `Client` 增加 `func (c *Client) ID() string { return "deepseek" }`。
- `Available()`：`apiKey != ""` 且 baseURL 可解析（照 web.DeepSeekSearchProvider.Available 同款）。
- 现有 `Stream`/`New` 不动。

## 4. openai provider 契约（internal/llm/openai）

```go
// package openai — OpenAI 兼容 SSE provider，复用 deepseek 的 OpenAI 兼容
// 客户端（DeepSeek API 即 OpenAI 兼容，wire 相同），零新序列化/解析代码。
// ID="openai"；凭证 OPENAI_API_KEY（env-only）。
func New(cfg Config) *openaiProvider   // Config{BaseURL, APIKey, Model}
// 内部持 deepseek.Client（New(deepseek.Config{BaseURL, APIKey, Model})）
// ID() = "openai"
// Available() = APIKey 非空 且 BaseURL 可解析（或默认 https://api.openai.com/v1）
// Stream = 委托内部 deepseek.Client.Stream
```

- 默认 base_url：`https://api.openai.com/v1`；默认 model：`gpt-4o-mini`（可配）。
- **说明注释**：openai 与 deepseek 共享 OpenAI 兼容 SSE 实现（含 `reasoning_content` 透传——OpenAI 兼容推理模型同样用该字段，M8 语义自然成立）。

## 5. config 契约（internal/config）

```go
type LLMConfig struct {
    // Provider 选择路由：deepseek（默认）| openai | anthropic（M8-2b 接入）。
    // 未知值 fail-closed（启动报错，不静默回落）。
    Provider string                 `yaml:"provider"`
    // Providers 是各 provider 的参数（非选中 provider 的参数仍解析为默认值，
    // 使 config 可切换；凭证只走 env）。
    OpenAI   OpenAIProviderConfig   `yaml:"openai"`
    Anthropic AnthropicProviderConfig `yaml:"anthropic"` // M8-2b 使用；本段仅解析
}

type OpenAIProviderConfig struct {
    BaseURL string `yaml:"base_url"` // 默认 https://api.openai.com/v1
    Model   string `yaml:"model"`    // 默认 gpt-4o-mini
    // APIKey 走 OPENAI_API_KEY env（纪律 6）
}
type AnthropicProviderConfig struct { // M8-2b 占位，本段仅解析不消费
    BaseURL string `yaml:"base_url"`
    Model   string `yaml:"model"`
}
```

- `Config` 增加 `LLM LLMConfig \`yaml:"llm"\``。
- applyDefaults：`provider` 空 → `deepseek`；openai/anthropic 各字段缺省回落默认。
- **顶层 `model`/`base_url` 保留**为 deepseek 的默认配置（`cfg.Model`/`cfg.BaseURL`，兼容现有 config.yaml，不迁移）。
- config.yaml 增加 `llm:` 段文档（provider + openai/anthropic 子段，含"凭证 env-only"注释）。

## 6. cmd/pa 接线契约

- `app` 增加 `llmReg *llm.Registry` 字段。
- `registerLLM()`（新文件 `cmd/pa/llm.go`）：
  1. 注册 deepseek（`deepseek.New(deepseek.Config{APIKey: os.Getenv("DEEPSEEK_API_KEY"), BaseURL: cfg.BaseURL, Model: cfg.Model, MaxRetries: 2})`，`ID()`="deepseek"）。
  2. 若 `os.Getenv("OPENAI_API_KEY")` 非空 → 注册 openai provider。
  3. 按 `cfg.LLM.Provider` 从注册表取 provider；不存在 → 启动报错（fail-closed）。
  4. 选中的 provider 注入 `app.llm`（loop 用）。
- `main.go`：`deepseek.New(...)` 改为 `registerLLM()`；loop 构造用 `app.llm`。
- `/llm-status`：显示 provider/model/modalities（`text`）；未配置的 provider 显示 unavailable（照 /kb-status 风格）。
- `printHelp` 增 `/llm-status` 命令行。

## 7. 测试要求

- `internal/llm`：Registry 注册/重复/Get/List。
- `internal/llm/deepseek`：`ID()`/`Available()`（key 空、base_url 非法）。
- `internal/llm/openai`：构造委托（Stream 行为回归——用 httptest 假 OpenAI 兼容 SSE 服务验证 text + tool-call 流式走通）；`Available()`。
- `internal/config`：LLMConfig 默认值（provider→deepseek、openai base/model 默认）；`llm.provider` 解析；config.yaml 段解析。
- `cmd/pa`：`registerLLM` 默认 deepseek 回归（无 OPENAI_API_KEY 时只注册 deepseek，provider 选中正确）；`/llm-status` 输出；未知 provider fail-closed。
- 全项目门禁绿；loop.go 无改动（D4）。

## 8. 提交与报告

- 每模块阶段提交（`M8-2: ...`）：Provider+Registry → deepseek 适配 → openai → config → wiring → 测试。
- 完成后 `go vet ./...` / `go test -count=1 ./...` / `go build ./...` 全绿再报告。
- 报告：改动文件清单、实现决策（对照本契约的偏离）、跑过的命令、测试结果。
