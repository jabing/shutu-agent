# 个人 Agent 整体设计方案

> 状态：**已定稿 v1**（2026-08-18） · 本文是设计基线，任何偏离必须先改本文 + 决策记录，再改代码。
> 参考原型：DeepSeek Harness（`../deepseek-harness/docs/architecture.md`），Go 重写，裁剪掉插件微内核等重机制。

---

## 0. 目标与边界

**目标**：用 Go 实现一个个人 Agent，借鉴 dsh 架构三原则——薄核心、日志即事实、能力即接缝（seam）。后期无痛加入个人知识库（RAG）。

**明确不做（v1）**：

| 不做 | 原因 |
|---|---|
| Cordis 式插件微内核 / 动态加载 | 单人项目，Go 接口 + 注册表足够；M5 再评估 |
| 多用户 / 云端部署 / 编辑集成 | 个人本地工具 |
| 事件总线 / 消息中间件 | Go 接口调用即可，不需要运行时解耦 |
| 第一版就做 Web UI | REPL 先把循环磨利（M3 再做） |

---

## 1. 从 dsh 继承的架构原则（不可动摇）

1. **核心极薄**：主干只有 5 件事——会话日志（session）、LLM 适配（llm）、工具注册表（tools）、提示词组装（prompt）、循环（loop）。其余全是模块。
2. **会话日志是唯一事实来源**：模型看到的一切必须能从日志重构（model-visible ⟺ logged）。历史是日志的**派生值**，永不另存。
3. **能力 = 接缝（capability seam）**：任何新能力 = 接口定义（Service）+ 后端实现（Provider，可换）+ 消费工具（Tool）三件套。消费方只依赖接口。
4. **新功能挂扩展点，不改循环**：加知识库 = 注册工具 + 注册服务，循环代码零改动。
5. **工具参数在入口统一校验**：模型生成的参数一定是脏的，Execute 前用 JSON Schema 校验。

---

## 2. 总体结构（模块地图）

```
personal-agent/
├── Agent.md                  # 全局规划 + 开发纪律（工作入口）
├── cmd/pa/main.go            # 入口：REPL（M1）→ CLI（M2）→ Web 可选（M3）
├── internal/
│   ├── config/               # config.yaml + 环境变量；模型、密钥、数据目录、工具白名单
│   ├── llm/                  # LLM 接口 + deepseek（OpenAI 兼容 / SSE 流式）实现
│   ├── session/              # 追加式事件日志 + 派生历史（模型可见即日志）
│   ├── tools/                # 工具注册表 + JSON Schema 校验 + 白名单
│   ├── prompt/               # 系统提示词分节组装（persona / skills / 能力声明）
│   ├── loop/                 # agent 循环（turn = 0..N step）
│   ├── store/                # 持久化抽象 + sqlite 实现（M2）；事件追加、版本号字段
│   └── kb/                   # 知识库能力（M4）：service + provider + kb_* 工具
├── docs/
│   ├── design.md             # 本文件（设计基线）
│   └── decisions/            # 决策记录 ADR：YYYY-MM-DD-<slug>.md
└── data/                     # 运行时数据（gitignore）：会话日志、知识库索引、配置
```

**包依赖方向（单向）**：`loop → session/llm/tools/prompt`，`kb → llm(嵌入) + store`。禁止反向依赖，禁止 `kb` 依赖 `loop`。

---

## 3. 数据模型（会话日志）

```go
// internal/session
type Event struct {
    Seq   uint64          // 单调递增，持久化后为跨重启主键
    Type  string          // 判别字符串，见下
    At    time.Time
    Data  json.RawMessage // 该类型事件的结构化载荷
}
```

- **v1 事件类型**：`user/message`、`assistant/chunk`（流式保真）、`assistant/message`、`tool/result`、`tool/error`。
- **M4 增加**：`kb/retrieval`（检索行为对模型可见 ⇒ 必须落日志）、`kb/ingest`。
- **新输入 ⇒ 新事件类型**，绝不在内存里拼 prompt 而不记录。
- `DeriveHistory() []llm.Message` 是纯函数：从日志折叠出模型历史；未来加过滤（如截断/压缩）只改折叠规则。
- 持久化 = 追加写入（SQLite 单表或 JSONL），启动时重放重建内存日志。事件类型带 `Version` 字段预留迁移。

---

## 4. Agent 循环（turn / step 结构，照抄 dsh 的 flow）

- **step** = 一次模型请求 + 其发起的工具调用；**turn** = 0..N 个 step，直到模型不再请求工具。
- v1 严格串行同步；取消通过 `context.Context`（Ctrl+C 即取消当前 step）。

```text
turn/start
  user/message 追加到日志
  step:
    history := log.DeriveHistory()
    组装提示词分节 + 工具 schema
    llm.Stream(...) → assistant/chunk* → assistant/message
    无工具调用 → turn/end
    有工具调用 → tools.Validate + tools.Execute → tool/result* → 下一 step
turn/end
```

循环只做这一件事。**任何产品功能都不得修改此结构**（防漂移 D4）。

---

## 5. 工具系统

```go
// internal/tools
type Tool interface {
    Name() string
    Schema() map[string]any   // JSON Schema，进入模型请求
    Execute(ctx context.Context, args json.RawMessage) (string, error)
}
type Registry struct{ ... }  // Register / Specs / Execute（入口统一校验）
```

- 校验库：`github.com/santhosh-tekuri/jsonschema/v5`（纯 Go）。
- **v1 工具**：`get_time`、`read_file`（只读）。
- 白名单在 `config.yaml` 按名称启用/禁用；未注册或未启用 ⇒ 拒绝执行。
- 超时与输出上限（截断/spill）是 Execute 管道的固定环节，M3 落地。

### M3 安全策略（2026-08-18 落地，决策：docs/decisions/2026-08-18-m3-sandbox-scope.md）

全部属于 `tools` 包的策略层 / Execute 管道，循环零改动（D4）：

- **白名单 `tools.enabled`**：按名称启用/禁用；默认只含只读工具 `get_time`、`read_file`。未启用 ⇒ Execute 门拒绝（`tool "x" is not enabled`）。
- **超时 `tools.timeout`**：每次工具 Execute 用 `context.WithTimeout` 包裹，默认 30s；`run_command` 可用 `tools.run_command.timeout` 单独覆盖。超时作为 `tool/error` 事件落日志（D3）。
- **输出截断 / spill `tools.output_limit`**：输出超过默认 64KB 时截断，全文落盘 `data/spill/<session>-<seq>.txt`；`tool/result` 事件记录截断文本 + 定位符（模型可见 ⇒ 落日志 D3）。spill 失败是 best-effort：保留内联输出，不把成功调用变成错误。
- **执行类工具 `run_command`**：唯一的执行类工具（D10 落地），仅当 `tools.run_command.enabled: true` 时注册/可用，**默认关闭**。单行命令经 `cmd /C`（Windows）或 `/bin/sh -c`（其他）执行，固定工作目录（`tools.run_command.workdir`），不暴露交互式 shell；执行前从环境变量中移除含 `KEY`/`SECRET`/`TOKEN`/`PASSWORD`/`API` 的条目；非零退出码以 `[exit code: N]` 内联报告（结果仍为 `tool/result`）。超时/取消通过进程终止生效：Windows 杀直系进程（输出走临时文件，孙进程不占管道），Unix 用进程组 `kill(-pgid)`。
- **取消（Ctrl+C）**：`signal.NotifyContext` 取消当前 step——流式中断（HTTP 请求上下文）与工具执行中断即时生效；事件追加即持久化（D8），内存日志与磁盘一致。

---

## 6. LLM 适配

- 适配器接口 `llm.LLM`：`Stream(ctx, ChatRequest) (StreamReader, error)`，**SSE 流式是第一天就支持的硬要求**（D6）。
- 默认实现：DeepSeek（OpenAI 兼容，`base_url=https://api.deepseek.com`）。可加 OpenAI/本地 Ollama，均实现同一接口。
- `ChatRequest` 携带工具 schema；tool 消息（`assistant` 带 tool_calls、`tool` 带结果）纳入历史。
- 重试/退避：M2 加入，策略放适配器内（provider 自有权责）。

---

## 7. 系统提示词组装（M2 落地，M1 用单段）

`prompt.Builder` 按配置分节拼装 system prompt：`persona`（人设）→ `skills`（技能说明）→ `knowledge`（M4 注入检索上下文）→ 工具 schema（自动）。分节来自 `config/prompts/*.md`，可独立增删而不改循环。

---

## 8. 知识库能力设计（M4 前瞻设计，接缝三件套）

```
kb 能力 = 三部分（严格对应 seam 结构）：
├── Service（接口，internal/kb/service）:
│     Ingest(source, chunks) / Retrieve(query, topK) → []Chunk{Text, Source, Score}
├── Provider（后端，可换）:
│     local: SQLite + 向量扩展（sqlite-vec，M4 评估 CGO 需求；
│             若不便利则纯 Go 向量索引，接口不变）
│     remote: pgvector / Qdrant + API 嵌入（备选）
└── Consumer（工具，注册进 tools）:
      kb_search(query, topK)   → 片段 + 来源
      kb_add(source, text)     → 分块入库
```

- **嵌入模型与对话模型解耦**：嵌入走独立 provider 配置（本地 Ollama 优先，API 备选）。
- 检索工作流：用户提问 → 循环（不变）→ 模型调 `kb_search` → 片段+来源经 `tool/result` 落日志 → 模型引用作答。
- **检索结果必须落日志**（`kb/retrieval` 事件）——这是"模型可见即日志"在知识库上的落地。
- 索引目标：个人笔记目录（Markdown），增量索引，来源标注文件路径。

---

## 9. 技术选型（锁定）

| 项 | 选择 | 备注 |
|---|---|---|
| 语言 | Go（1.23+） | 编译型、单二进制、跨平台 |
| LLM 客户端 | `sashabaranov/go-openai`（自定义 BaseURL）或手写 SSE | DeepSeek 为默认 |
| 参数校验 | `santhosh-tekuri/jsonschema/v5` | 纯 Go |
| 配置 | `gopkg.in/yaml.v3` + 环境变量 | API Key 只走环境变量，绝不入库 |
| 持久化 | `modernc.org/sqlite`（纯 Go，无 CGO） | Windows 友好；JSONL 仅作开发模式 |
| 向量存储 | M4 评估 sqlite-vec / 纯 Go 方案 | 由 Provider 抽象兜底，切换成本≈0 |
| 日志 | `slog`（标准库） | 够用 |
| 测试 | 标准库 `testing` + `httptest` | 适配器用录制回放测试 |

**硬约束：全程 CGO-free**（Windows 个人机可无工具链直接构建）。

---

## 10. 固定设计决策（D1–D10，防漂移基线）

| # | 决策 | 明确拒绝（反例） | 何时可重评 |
|---|---|---|---|
| D1 | 会话 = 追加式事件日志；历史是派生值 | 直接持久化 messages 数组 | 出现性能瓶颈且测得为日志折叠时 |
| D2 | 新能力 = Service/Provider/Tool 三件套 | 在循环里 `if kb {...}` | 永不允许 |
| D3 | 模型可见 ⇒ 已落日志；新输入 ⇒ 新事件类型 | 内存拼 prompt 不记录 | 永不允许 |
| D4 | 薄核心；v1 用 Go 接口+注册表，无插件系统 | 引入插件框架/事件总线 | M5 有明确需求时 |
| D5 | 循环串行同步；并发/后台任务推迟 | 提前上 goroutine 编排 | M5，且有明确用例（子代理/任务） |
| D6 | LLM 适配器第一天支持 SSE 流式 | 先整块响应后补流式 | 永不允许（返工成本极高） |
| D7 | 工具参数 Execute 前统一 JSON Schema 校验 | 各工具自行解析裸 JSON | 永不允许 |
| D8 | store 接口抽象，SQLite 后端；事件带版本号 | 代码里直接写死文件格式 | 无，接口已预留 |
| D9 | 知识库是能力（seam），嵌入与对话模型解耦 | 检索逻辑写进 loop / 嵌入写死 | 永不允许 |
| D10 | 安全白名单先行；执行类工具 M3 才上 | 第一版就开放 bash | M3 随沙箱一起评估 |

---

## 11. 里程碑（详见 `../Agent.md`）

| 里程碑 | 内容 | 周期 | 验收标准 |
|---|---|---|---|
| M1 | 最小循环骨架（REPL + 流式 + 日志 + 工具） | 1–2 天 | 命令行提问可流式回答；`get_time`/`read_file` 可调用；`go vet`/`go test` 干净 |
| M2 | 持久化 + 多会话 + 提示词组装 + 配置 | 3–5 天 | 重启可恢复会话；新增事件类型不改历史结构 |
| M3 | 安全白名单 + 超时/输出截断 + CLI 完善（Web 可选） | ~1 周 | 工具仅白名单内可执行；取消即时生效 |
| M4 | 知识库能力（kb service + provider + kb_* 工具） | 1–2 周 | 索引笔记后提问能引用正确片段+来源；换 Provider 不改消费方 |
| M5 | 远期可选：子代理、后台任务、压缩、技能、插件评估 | 按需 | 每个都有独立决策记录 |

---

## 12. 演进规则（如何改这份设计）

1. **先文档后代码**：任何涉及核心数据模型、循环结构、包依赖方向的变更，先写决策记录 `docs/decisions/YYYY-MM-DD-<slug>.md`（状态/背景/决策/理由/后果），再更新本文件，最后改代码。
2. 新增模型可见输入 ⇒ 先加事件类型（D3），再实现。
3. 新增能力 ⇒ 先定义接口（D2），再实现 Provider 与 Tool。
4. 里程碑验收标准是"完成"的定义；未达标不进入下一里程碑。
5. 本文件与 `Agent.md` 双份基线，改一处必须同步另一处。
