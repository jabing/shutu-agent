# 个人 Agent 整体设计方案

> 状态：**已定稿 v1**（2026-08-18） · 本文是设计基线，任何偏离必须先改本文 + 决策记录，再改代码。
> 参考原型：DeepSeek Harness（`../deepseek-harness/docs/architecture.md`），Go 重写，裁剪掉插件微内核等重机制。

---

## 0. 目标与边界

**目标**：用 Go 实现一个个人 Agent，借鉴 dsh 架构三原则——薄核心、日志即事实、能力即接缝（seam）。后期以"能力接缝"方式接入个人知识库（M4，参照 dsh-knowledge：FTS5 全文检索 + 提取回写，不用向量 RAG）。

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
│   ├── kb/                   # 知识库能力（M4）：service + sqlite provider + kb_* 工具 + 提取回写
│   ├── jobs/                 # 后台任务（M5a）：owner-fenced job 注册表 + 本地实现 + job_* 工具
│   ├── subagent/             # 子代理（M5b）：provider 注册表 + spawn 实现 + 委托/控制/报告工具
│   ├── compaction/           # 上下文压缩（M5c）：压缩接缝 + 摘要 provider + /compact
│   ├── skill/                # 技能（M5d）：provider 注册表 + 文件系统发现 + skill 加载工具
│   ├── schedule/             # 定时调度（M6a）：provider 注册表 + 触发/入队 job
│   ├── plan/                 # 任务规划（M6b）：goal→plan→todo + 规划/推进工具
│   ├── spill/                # 长期记忆（M6c）：跨会话记忆 provider + 自动沉淀/召回
│   ├── interact/             # 人工审批（M6d）：审批请求/响应接缝（CLI 侧）
│   ├── code/                 # 代码沙箱（M6e）：沙箱 provider + 本地子进程隔离实现
│   ├── mcp/                  # 外部工具生态（M6f）：MCP 客户端接缝（JSON-RPC 自实现优先）
│   ├── fs/                   # 文件/工作区统一封装（M6f）
│   └── web/                  # web 搜索（M7）：service + deepseek/fetch provider + web_* 工具
├── docs/
│   ├── design.md             # 本文件（设计基线）
│   └── decisions/            # 决策记录 ADR：YYYY-MM-DD-<slug>.md
└── data/                     # 运行时数据（gitignore）：会话日志、知识库索引、配置
```

**包依赖方向（单向）**：`loop → session/llm/tools/prompt`，`kb → llm(嵌入) + store`，`web → store(事件 sink)`。禁止反向依赖，禁止 `kb` 依赖 `loop`。

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
- **M4 增加**：`kb/recall`（主动召回注入）、`kb/extract`（提取回写结果）、`kb/add`（显式写入）。
- **M5 增加**（参照 dsh 四个能力族，ADR `2026-08-18-m5-agent-core.md`）：`job/start|status|done`（后台任务）、`subagent/start|end|report`（子代理）、`compaction/start|summary|end|prune`（上下文压缩，摘要本身作为带 `surfaceOp: replace` 的 `user/message` 遮蔽旧范围）、`skill/catalog|load`（技能目录与加载）。全部 log-only，`DeriveHistory` 视为不透明数据（compaction 除外：其为派生规则输入，折叠时跳过被遮蔽 seq）。
- **M6 增加**（能力补全，ADR `2026-08-19-m6-agent-full.md`）：`schedule/*`（定时调度）、`plan/*`（任务规划）、`spill/*`（长期记忆）、`interact/*`（人工审批）、`code/*`（代码沙箱）、`mcp/*`（外部工具生态）。同样全部 log-only，逐段派发时细化各事件类型。
- **M7 增加**（web 搜索，ADR `2026-08-20-m7-web-search.md`）：`web/search-request`（搜索请求快照，secret-free：query/endpoint/model/body，派发前落库）。`web_search`/`web_fetch` 工具结果走通用 `tool/result`（模型实际看到 ⇒ 已满足 D3）。log-only，`DeriveHistory` 视为不透明数据。
- **M8 事件字段演进**（消息模型升级，ADR `2026-08-20-m8-message-model.md`）：`user/message` 与 `assistant/message` 载荷从纯字符串演进为 `content` blocks 数组（text / reasoning / image-ref / tool-call / tool-result）；assistant 消息带 `reasoning_content`（D3 落库并随 `DeriveHistory` 跨 provider 回传）；image block 只落 `ImageRef` 引用（不含 base64，D8 兼容：旧纯字符串回放时包成单个 text block）。
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

**pre-step 扩展点（M5 起）**：`loop.Config.PreStep` 是可注册多个注入器的钩子，在 `user/message` 追加后、首个 step 请求前调用，返回值（如召回上下文、子代理/技能目录）仅注入首个请求。M4b 的 `Config.Recall`（kb 主动召回）是它的首个消费者，M5b/M5d 的子代理目录与技能目录随后接入。turn/step 结构零改动（D4），扩展点预算有界、fail-open。

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

`prompt.Builder` 按配置分节拼装 system prompt：`persona`（人设）→ `skills`（技能说明）→ `knowledge`（M4：知识库轻量目录注入，只含库名/描述不塞正文）→ 工具 schema（自动）。分节来自 `config/prompts/*.md`，可独立增删而不改循环。

---

## 8. 知识库能力设计（M4 落地设计，参照 dsh-knowledge，接缝三件套）

> 方向定稿 2026-08-18：**全盘参照 [dsh-knowledge](https://github.com/lemoncat7/dsh-knowledge)（参考源码 `../dsh-knowledge/`）**。放弃 embedding/向量检索（dsh-knowledge 本身就不用向量，明确推迟），采用 **SQLite FTS5 全文检索 + 中文二元组 LIKE 兜底 + 回答后模型提取回写**。该方案已由控制会话在 Go/modernc 栈实测验证（`docs/research-m4-kb.md` §Go 实测），**零新依赖、完全离线、CGO-free、中文友好**。

```
kb 能力 = 三部分（严格对应 seam 结构）：
├── Service（接口，internal/kb/service）:
│     Search(ctx, query, opts)   → []Hit{Entry, Score}   // FTS5 + 二元组 LIKE 兜底
│     Add(ctx, draft Entry)      → error                 // 显式写入一条知识条目
│     Recall(ctx, query, limit)  → []Hit                 // 主动召回（有界摘要）
│     Extract(ctx, session, turn) → error                // 回答后提取回写
├── Provider（后端，可换）:
│     sqlite: knowledge_entries + knowledge_fts(FTS5) + extraction_jobs
│             （modernc.org/sqlite v1.38.0 已内置 FTS5，实测可用，零新依赖）
│     remote: 预留接口（M5 如需分布式/共享再评估）
└── Consumer（工具 + 注入，注册进 tools / prompt）:
      kb_search(query, limit) → 条目片段 + 来源     （只读）
      kb_read(id)            → 完整条目             （只读）
      kb_add(title, body, type, tags, scope) → 显式写入（写）
      主动召回：会话开始时轻量目录注入 + 每轮有界摘要注入（kb/recall 事件）
```

- **知识条目模型**（dsh-knowledge 同款）：`{title, body, type, tags, scope, confidence, version, source}`。`type ∈ {preference, fact, decision, procedure, lesson}`；`source` 记录条目来源（会话/轮次/显式添加）；`version` 支持更新历史。
- **知识来源两条路**：① **回答后提取回写**——每轮回答结束后，用当前模型判断是否产生可复用知识（严格模式：只收明确陈述或已验证的长期知识），写入条目（幂等，`session:turn` 为 job key）；② **显式 `kb_add` 工具**——用户/模型主动写入（也用于收录笔记正文）。
- **检索**：FTS5（`unicode61 remove_diacritics 2`）BM25 排序 + **中文二元组 LIKE 兜底**（`fallbackTerms`：中文切相邻二元组做 `LIKE '%xx%'`，英文词直接匹配）——解决 FTS5 默认分词把连续中文当整 token 的缺陷。中文/英文/混合查询均实测可用。
- **召回与注入**：轻量目录（知识库名+描述，不塞正文）注入系统提示词；每轮开始时按用户输入主动召回有界条数（默认 3）作为 `kb/recall` 上下文消息注入（模型可见 ⇒ 落日志 D3）。检索失败 fail-open，不阻断回答。
- **不修改循环**（D4）：kb 是能力接缝，loop 的 turn/step 结构零改动。提取/召回由 `cmd/pa`（组合根）在循环外编排，工具走 `tools` 注册表。
- **落日志（D3）**：`kb/recall`（主动召回注入）、`kb/extract`（提取回写结果）、`kb/add`（显式写入）；`kb_search`/`kb_read` 结果走 `tool/result`（模型实际看到 ⇒ 已满足 D3）。历史仍是日志派生值（D1）。

---

## 9. 技术选型（锁定）

| 项 | 选择 | 备注 |
|---|---|---|
| 语言 | Go（1.23+） | 编译型、单二进制、跨平台 |
| LLM 客户端 | `sashabaranov/go-openai`（自定义 BaseURL）或手写 SSE；M8 起 provider 注册表（deepseek / openai 兼容 / anthropic Messages），多模态图片请求时转 data URL | DeepSeek 为默认；Anthropic 复用 M7 的 HTTP 客户端心智 |
| 参数校验 | `santhosh-tekuri/jsonschema/v5` | 纯 Go |
| 配置 | `gopkg.in/yaml.v3` + 环境变量 | API Key 只走环境变量，绝不入库 |
| 持久化 | `modernc.org/sqlite`（纯 Go，无 CGO） | Windows 友好；JSONL 仅作开发模式 |
| 向量存储 | **不用向量库**：modernc sqlite v1.38.0 内置 FTS5（实测可用），中文检索用二元组 LIKE 兜底；embedding/向量仅作 M5 预留 Provider 接口 | 由 Provider 抽象兜底，切换成本≈0 |
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
| D4 | 薄核心；v1 用 Go 接口+注册表，无插件系统；新功能挂扩展点（M5 起：`Config.PreStep` 统一 pre-step 注入器） | 引入插件框架/事件总线 | 永不允许 |
| D5 | 循环串行同步；后台任务/子代理走 `internal/jobs` owner-fenced 注册表（受控并发，M5a 落地） | 把并发直接编排进主循环 turn/step | M5 已落地（ADR `2026-08-18-m5-agent-core.md` 决策 ①），持续保持主循环串行 |
| D6 | LLM 适配器第一天支持 SSE 流式 | 先整块响应后补流式 | 永不允许（返工成本极高） |
| D7 | 工具参数 Execute 前统一 JSON Schema 校验 | 各工具自行解析裸 JSON | 永不允许 |
| D8 | store 接口抽象，SQLite 后端；事件带版本号 | 代码里直接写死文件格式 | 无，接口已预留 |
| D9 | 知识库是能力（seam）；检索与对话模型解耦（M4 用 FTS5，embedding/向量仅作可选 Provider 预留） | 检索逻辑写进 loop / 检索后端写死 | 永不允许 |
| D10 | 安全白名单先行；执行类工具 M3 才上 | 第一版就开放 bash | M3 随沙箱一起评估 |

---

## 11. 里程碑（详见 `../Agent.md`）

| 里程碑 | 内容 | 周期 | 验收标准 |
|---|---|---|---|
| M1 | 最小循环骨架（REPL + 流式 + 日志 + 工具） | 1–2 天 | 命令行提问可流式回答；`get_time`/`read_file` 可调用；`go vet`/`go test` 干净 |
| M2 | 持久化 + 多会话 + 提示词组装 + 配置 | 3–5 天 | 重启可恢复会话；新增事件类型不改历史结构 |
| M3 | 安全白名单 + 超时/输出截断 + CLI 完善（Web 可选） | ~1 周 | 工具仅白名单内可执行；取消即时生效 |
| M4 | 知识库能力（拆三段：M4a 内核 → M4b 工具与召回 → M4c 提取回写） | 1–2 周 | 对话产生可复用知识能被提取并检索引用（含中文）；显式 `kb_add` 可写；`kb_search`/`kb_read`/`kb/recall`/`kb/extract`/`kb/add` 落日志；换 Provider 不改消费方 |
| M5 | 核心能力四段（ADR `2026-08-18-m5-agent-core.md`）：M5a 后台任务 → M5b 子代理 → M5c 上下文压缩 → M5d 技能 | 按四段逐段验收 | 四段各自验收标准（见各 dispatch 文档）全部达标才算 M5 完成 |
| M6 | 能力补全六段（ADR `2026-08-19-m6-agent-full.md`）：M6a 定时调度 → M6b 任务规划 → M6c 长期记忆 → M6d 人工审批 → M6e 代码沙箱 → M6f 工具生态 | 按六段逐段验收 | 六段各自验收标准（见各 dispatch 文档）全部达标才算 M6 完成；默认关、零新依赖（M6f MCP 优先自实现，SDK 仅当协议超限才评估）—— **✅ 2026-08-19 六段全部验收通过（见 `../Agent.md` §4）** |
| M7 | web 搜索（ADR `2026-08-20-m7-web-search.md`）：`internal/web` 接缝（service + deepseek 官方搜索 provider + http fetch provider + `web_search`/`web_fetch` 工具 + `web/search-request` 事件）+ config | 按 half 逐段验收 | M7 验收标准（见 dispatch-m7 文档与 ADR）：真实搜索返回结构化来源；D3/D7/D10 合规；零新依赖；不改 loop—— **✅ 2026-08-20 验收通过（见 `../Agent.md` §4；真实 key 冒烟待 rotate 后补）** |
| M8 | 消息模型升级（ADR `2026-08-20-m8-message-model.md`，Agent 部分第二阶段）：M8-1 `llm.Message` content parts（text/reasoning/image-ref/tool-call/tool-result）+ reasoning 落库回传 + 全部使用方迁移（D8 旧事件回放兼容）→ M8-2 多 provider 注册表（deepseek/openai/anthropic，config 切换，reasoning 跨 provider 保留，anthropic 复用 M7 HTTP 客户端）→ M8-3 多模态（`/attach` 图片，落库只存 `ImageRef`、请求时 data URL、20MiB 上限最老替换、inputModalities 声明、默认关） | 三段逐段验收 | M8 验收标准（见 ADR）：`go vet/test/build` 全绿且迁移无残留；旧会话回放不回归；provider 切换历史重编码正确（reasoning 保留）；图片/多模态按范式工作；不改 loop；零新依赖—— **✅ 2026-08-20 完成（三段全验收）** |

---

## 12. 演进规则（如何改这份设计）

1. **先文档后代码**：任何涉及核心数据模型、循环结构、包依赖方向的变更，先写决策记录 `docs/decisions/YYYY-MM-DD-<slug>.md`（状态/背景/决策/理由/后果），再更新本文件，最后改代码。
2. 新增模型可见输入 ⇒ 先加事件类型（D3），再实现。
3. 新增能力 ⇒ 先定义接口（D2），再实现 Provider 与 Tool。
4. 里程碑验收标准是"完成"的定义；未达标不进入下一里程碑。
5. 本文件与 `Agent.md` 双份基线，改一处必须同步另一处。
