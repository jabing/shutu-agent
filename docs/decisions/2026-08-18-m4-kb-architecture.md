# ADR: M4 知识库架构——FTS5 + 中文二元组 LIKE 兜底检索，Provider 抽象，kb/recall 事件落日志

- 状态：**已定**（2026-08-18，M4a 内核落地；M4b 工具消费面与召回注入落地；M4c 提取回写落地）
- 关联：design.md §8、§10 D1–D10（重点 D2/D3/D9/D10）；Agent.md 路线图 M4；调研 `docs/research-m4-kb.md`；参考实现 `../dsh-knowledge/`
- 分阶段：本 ADR 是 M4 主 ADR。M4a 定稿三件事：① 检索方案 ② Provider 抽象与接口边界 ③ 事件落日志机制；M4b 补充：④ 工具消费面 ⑤ 召回注入机制 ⑥ CLI 与 config 扩展；M4c 补充：⑦ 提取回写机制（本 ADR 的 M4 最后一段决策）。

## 背景

M4 要给个人 Agent 接入知识库能力（设计基线 design.md §8，参照 dsh-knowledge：FTS5 全文检索 + 提取回写，非向量 RAG）。M4a 是第一段——只做内核（Service 接口 + SQLite Provider + config + 事件类型声明），不碰工具/提取/召回编排（M4b/M4c）。

实现前必须回答三个问题：

1. 检索怎么做？个人规模、本地离线、中文友好的全文检索；
2. 怎么让"换后端不换消费方"成立（design.md D2/D9 的能力接缝）？
3. 模型主动召回注入是**新的模型可见输入**，怎么满足 D3（模型可见 ⇒ 已落日志）而不改 loop 结构（D4）？

## 决策 ① 检索方案：SQLite FTS5 + 中文二元组 LIKE 兜底

**用 modernc.org/sqlite v1.38.0 自带 FTS5 虚拟表做 BM25 全文检索，中文二元组 LIKE 兜底补充，不引入向量/embedding。**

具体（对应 `internal/kb/`）：

- 表：`knowledge_entries`（条目 + source/type/tags/scope/confidence/version/created_at/updated_at）+ `knowledge_fts`（FTS5，`tokenize = 'unicode61 remove_diacritics 2'`，列为 knowledge_id UNINDEXED / title / body / tags）。WAL、外键、busy_timeout、`BEGIN` 事务（`Add` 条目 + FTS 同步同事务）。
- 检索（`Search`）：
  1. `toFtsQuery`：空格切词、逐词加引号（内嵌引号双写）、OR 连接、上限 20 词；
  2. FTS5 BM25 排序 `bm25(knowledge_fts, 0.0, 4.0, 1.0, 0.5)`（四列权重依序：knowledge_id/title/body/tags，标题权重最高）；MATCH 语法错误按"无 FTS 命中"处理，交给兜底；
  3. FTS 结果不足 topK 时，`fallbackTerms` 兜底：英文词原样（小写）、连续中文切相邻二元组（单字串保留单字），对 title/body/tags 做 `LIKE '%xx%'`（转义 `\ % _`）OR 连接，`updated_at DESC` 排序，去重补足 topK；
  4. 命中转分数 `1/(1+max(0,rank))`；兜底行沿用 dsh-knowledge 的固定 rank=3.0（分数恒低于真实 FTS 命中）。
- `Recall` = 有界检索（limit 即界），注入编排在 M4b。
- `Add`：同 source 更新走 `version+1`（更新历史表留 M4c/M5 评估，M4a 只记当前版本 + updated 时间）；否则新条目 version=1。
- 数据落 `<data_dir>/kb/knowledge.sqlite`（`data/` 已在 .gitignore，不入库）。

**理由（为何不用向量/embedding）：**

1. **零新依赖 + CGO-free**：项目硬约束（design.md §9），向量库/嵌入模型客户端全是新依赖；FTS5 在已钉死的 v1.38.0 内即内置启用（`SQLITE_ENABLE_FTS5=1`），本次实现**零依赖升级、零新依赖**。
2. **实测可用**：`docs/research-m4-kb.md` §Go 实测——toFtsQuery + fallbackTerms 完整移植后，中文"架构/中文/知识库"、英文"sqlite"、混合"FTS5 提取"全部正确命中；M4a 的 `TestProviderSwapConsumerUnchanged` 中文/英文/混合断言全绿（走的就是这一条路径）。
3. **个人规模**：本地个人知识条目数量级远不需要语义向量索引；dsh-knowledge 本身就不用向量，与"薄核心 + 日志即事实"同频。
4. **FTS5 中文缺陷有既定兜底**：unicode61 把连续中文当单个 token（"架构决策记录"整体一词），搜"架构"不中；trigram 需 ≥3 字（两字失效）。二元组 LIKE 兜底精准补齐（单字/词/短语/混合全过）。
5. **接口已预留演进**：向量/embedding 仅作 M5 可选 Provider（design.md §8 明确推迟），由 Provider 抽象兜底，切换成本≈0。

## 决策 ② Provider 抽象与接口边界（换 Provider 不改消费方）

**知识库是能力接缝（D2/D9）：`internal/kb` 的 `KB` 接口是 Service，消费方（工具、召回编排）只依赖接口；后端实现（Provider，可换）满足同一接口。**

- 接口（`internal/kb/service.go`）：

  ```go
  type KB interface {
      Search(ctx context.Context, query string, opts SearchOpts) ([]Hit, error)
      Add(ctx context.Context, draft Entry) error
      Recall(ctx context.Context, query string, limit int) ([]Hit, error)
      Close() error
      // Extract 在 M4c 加（接口预留，勿实现）
  }
  type Entry struct{ ID, Title, Body, Type string; Tags []string; Scope, Source string; Confidence float64; Version int }
  type Hit struct{ Entry Entry; Score float64 }
  type SearchOpts struct{ TopK int; Scope string }
  ```

  `Close` 属于接口：Provider 持有外部资源（DB 句柄），随 Provider 生命周期释放，避免换后端泄漏。`Extract` 按派发要求**不在本段声明**（声明即强迫所有 Provider 出桩实现，留 M4c）。
- 本段两个 Provider：**默认 SQLite**（`internal/kb/sqlite.go`，FTS5 + 二元组兜底）+ **内存**（`internal/kb/mem.go`，简化词包含匹配）。
- 共享语义集中在 seam 包：`normalizeDraft`（标题 1–200、正文 1–50000、type ∈ {preference,fact,decision,procedure,lesson}、confidence∈[0,1]、tags 规范化）、`toFtsQuery`/`fallbackTerms`/`rankToScore`/`escapeLike`/`normalizeTopK`。所有 Provider 复用同一套检索与校验，跨后端行为一致。
- **边界验收**（`TestProviderSwapConsumerUnchanged`）：同一份消费方代码（增删查 + 版本 + scope + topK 断言）对 SQLite 与内存 Provider 全绿——证明消费方只依赖接口，换后端零改动。
- 数据目录归 config：`kb.db_path` 空值默认 `<data_dir>/kb/knowledge.sqlite`（跟随 data_dir），显式值原样使用。
- **M4b 接口扩展**（由工具消费面驱动，见决策 ④）：新增 `Get(ctx,id)`（kb_read 按 id 取整条）、`Stats(ctx)`（/kb-status）；`Add` 由 `error` 改为 `(Entry, error)` 返回被赋值的条目（kb_add 输出 id/version，kb/add 事件摘要需要）。两个 Provider 同步实现，原 M4a 消费方测试机械更新后保持绿色。

## 决策 ③ 事件落日志机制（kb/recall 满足 D3，不改 loop）

**主动召回把知识注入模型上下文是新的"模型可见输入"，必须在注入前落日志（D3）。机制：在会话日志词汇表新增 `kb/recall` 事件类型，由组合根（M4b 的召回编排）在循环外调用 `session.NewKBRecall` 追加，loop 的 turn/step 结构零改动（D4）。**

- `internal/session/session.go`：新增常量 `EventKBRecall = "kb/recall"` + 载荷构造 `NewKBRecall(query string, hits []session.RecallHit)`（`RecallHit`：id/title/snippet/type/tags/scope/source/score——"有界摘要"的纯数据投影，`session` 不依赖 `kb` 包）。
- **D3 在 M4a 就位**：类型声明 + 追加路径测试（`session.TestKBRecallEventAppendsAndReplays`：sink 追加 → JSON 往返 → 重启重放；`store.TestKBRecallEventPersistsAndReplays`：log sink → SQLiteStore → 回放）。
- `DeriveHistory` 对 `kb/recall` 视为不透明数据（不回放成消息）：召回由编排方直接注入上下文，日志只负责"已注入"的事实记录（设计基线 §8：检索失败 fail-open，不阻断回答）。
- 其余 kb 事件（`kb/extract`、`kb/add`）随 M4b/M4c 依序加入，同一机制，事件带 `Version` 字段（D8）兼容演进。

## 决策 ④ 工具消费面（M4b）：kb_search / kb_read / kb_add + D10 白名单门 + D7 校验

**三个工具是 kb 接缝的 Consumer，落在 `internal/kb/tools.go`（design.md §8 Consumer / D2/D9），结构化实现 `tools.Tool` 接口（Go 结构类型，不 import tools 包，seam 保持解耦），由组合根注册进 `tools.Registry`。M4a 的"kb 默认关闭（D10）"在此扩展为完整的消费面门：`kb.enabled` 同时决定 provider 初始化、工具注册与白名单。**

- **工具边界**：
  - `kb_search(query, limit)`（只读）：`Search` + 条目片段 + 来源 + score + id；`limit` 缺省取 `kb.top_k`（默认 5，schema 上限 100）。结果让模型能凭 id 调 `kb_read`。
  - `kb_read(id)`（只读）：`Get` 返回完整条目（含元数据与正文）；未知 id 报错（模型不会把过期 id 当活条目）。
  - `kb_add(title, body, type, tags)`（写）：显式写入，`source="manual:<随机后缀>"`。随机后缀是关键：`Add` 的"同 source 更新 version+1"语义会把共享字面量 `"manual"` 的多次显式写入折叠成一条覆盖，随机后缀让每次 `kb_add` 都是独立条目（与 seed 数据 `manual:6`/`manual:new` 的既有约定一致），模型拿到 `id + version` 后可 `kb_read`。
- **白名单开关**（沿用 M3 run_command 单一开关模式）：`config.applyDefaults` 在 `kb.enabled: true` 时自动把 `kb_search/kb_read/kb_add` 追加进 `tools.enabled`（`TestLoadKBEnabledAppendsToolsToWhitelist`）；组合根同时注册工具 + 打开 provider。默认关闭：`kb.enabled=false` ⇒ 工具不注册、不进白名单、`Execute` 拒绝为 unknown tool（`TestKBToolsNotRegisteredByDefault`、`TestKBToolsRejectedWhenNotWhitelisted`）。`internal/kb.NewFromConfig` 仍是唯一构造入口，`enabled=false` 返回 `(nil, nil)` 且**不打开任何数据库文件**（M4a 既有 `TestNewFromConfigDisabled`）。
- **D7 校验**：三个工具 schema 在 `Execute` 入口由 `jsonschema/v5` 统一编译校验（`TestKBAddRejectsBadArgs` 等：缺 title/坏 type/未知字段/非对象全部拒绝，且不触达 provider）。
- **接口扩展（消费面驱动，M4a 接口的 M4b 增量）**：见决策 ② 尾部——`Get`、`Stats`、`Add` 返回 `(Entry, error)`；SQLite 与内存 Provider 同步实现。
- **kb/add 事件（D3）**：`kb_add` 工具带可选 `onAdded func(Entry)` 回调；组合根把它接到 `session.NewKBAdd`（条目摘要）追加 `kb/add` 事件。`kb_search`/`kb_read` 结果走 `tool/result`（模型实际看到 ⇒ 已满足 D3），无需额外事件。

## 决策 ⑤ 召回注入机制（M4b）：catalog + 有界 recall，不改 loop 的 turn/step 结构

**知识目录与主动召回由组合根（cmd/pa）编排；loop 只新增一个可选扩展点 `Config.Recall`，turn/step 结构零改动（D4"新功能挂扩展点"）。**

- **catalog（会话开始时）**：`kb.enabled && kb.catalog`（默认 true）时，组合根把轻量目录（`kb.CatalogText()`：库名/描述 + 何时用 kb_search/kb_read/kb_add 的指引，**不塞正文**）注入系统提示词的 `knowledge` 分节（`prompt.Builder.Add`，Order 30 与 `30-knowledge.md` 同槽，design.md §7）。对应 dsh-knowledge `system-prompt/assemble` 的挂载目录注入；我们只有一个全局库，故目录是能力指引而非库列表。
- **有界 recall（每轮开始时）**：组合根的 `recallContext` 按用户输入调 `kb.Recall(limit=recall_limit, 默认 3；0=关闭)`，命中转有界摘要（`kb.Snippet` 截断 + 元数据），**先追加 `kb/recall` 事件（D3：模型可见 ⇒ 已落日志），再把摘要作为 user 角色上下文消息**注入本轮首个 step 的请求。
- **不改 loop 结构**：`loop.Config.Recall func(ctx, userText) []llm.Message` 在 `Run` 追加 `user/message` 之后、首个 step 请求之前调用一次，返回值仅注入首个请求（工具调用后续 step 不重复注入，与 dsh-knowledge 的 `step === 1` 门一致）。这是 Go 对 dsh `agent/pre-step` 钩子的最小扩展点模拟，完全符合 D4；编排（查询构造、KB.Recall、fail-open、事件落日志）全部在 cmd/pa。
- **fail-open 依据**：Recall 检索失败、无命中、`kb/recall` 事件追加失败都**不阻断回答**——编排返回 nil 上下文，REPL 仅向 stderr 打一条告警（参考 dsh recall.ts 的 `try/catch → return modelDecision`）。召回是增强，不是依赖。
- 载荷只记录"已注入"那一刻的有界摘要，避免日志膨胀（决策 ③ 已述）。

## 决策 ⑥ CLI 与 config 扩展（M4b）

- **`/kb-status`**：条目数 / 库文件大小 / 最近写入（走 `KB.Stats`）；kb 关闭时提示 `kb: disabled`。
- **`/kb-reindex`**：重建 FTS 索引（SQLite Provider 的 `Reindex`：清空 `knowledge_fts` 并从 `knowledge_entries` 重灌，单事务、先读后写避开单连接竞争）；非 SQLite Provider 时报不支持。
- **config 扩展**（`internal/config`）：`kb.recall_limit`（`*int`，缺省 ⇒ 3，显式 `0` ⇒ 关闭主动召回）、`kb.catalog`（`*bool`，缺省 ⇒ true，显式 `false` ⇒ 不注入目录）。用指针是因为 **0/false 是有意义的显式取值，必须与"缺省"区分**（缺省要落到默认值，显式 0/false 要保留语义）；经 `KBConfig.RecallLimitValue()/CatalogValue()` 读取。`enabled/db_path/top_k` 沿用 M4a。kb 数据仍落 `data/kb/`（不入库）。`kb.extraction` 开关在 M4c 加入（见决策 ⑦）。

## 决策 ⑦ 提取回写机制（M4c）：幂等认领 + 严格 JSON fail-closed + fail-open + 提取模型跟随会话

**每轮回答结束后，由组合根（cmd/pa）在循环外调用 `kb.Extract(ctx, ExtractOpts)`（KB 接口的 M4a 预留位）做回答后提取回写：先幂等认领 `session:turn`，检索既有条目作上下文，调当前模型输出严格 JSON 候选，运行时校验 fail-closed，仅直写。loop 的 turn/step 结构零改动（D4）。**

具体（对应 `internal/kb/extract.go` + `cmd/pa` 编排）：

- **接口（M4a 预留位落地）**：`KB.Extract(ctx, ExtractOpts{LLM, Model, SessionID, Turn, UserText, AssistantText}) (ExtractResult, error)`。`ExtractResult{Status ∈ created|skipped|duplicate|failed, Reason, Created[]}`；两个 Provider（SQLite + 内存）都实现，共享同一 `runExtraction` 管道（`extractStore` 小接口 = Search/Add + claim/complete），跨后端行为一致（延续决策 ② 的接口边界）。
- **幂等认领**：`extraction_jobs` 表（`PRIMARY KEY(session_id, turn)`，M4a 建或不建均可，M4c 用 `CREATE TABLE IF NOT EXISTS` 补建）`INSERT OR IGNORE` 原子认领 `session:turn`；认领失败 = duplicate 结果，重放/重启同 key 不重复写、不重复调模型。认领后 `completeExtraction` 写 status/reason（审计轨迹，best-effort 不阻断）。
- **流程（对照 dsh-knowledge extraction.ts + architecture.md §Extraction flow，裁剪到单全局库）**：① 认领 job → ② 取本轮用户输入 + 最终回答（由 cmd/pa 从日志派生：turn = 日志中 `user/message` 数，D1；最终回答 = 最后一条非空 `assistant/message`）→ ③ `Search`（user+assistant 拼接截断 4000 字符，top 10 条，正文片段截断 1200 字符）作上下文 → ④ 调当前模型输出**严格 JSON 候选** `{"candidates":[{action,title,body,type,tags,confidence,reason}]}`（或 `{"skip":true}`）→ ⑤ 运行时校验 → ⑥ 仅直写（每条候选独立 source `session:<id>:turn:<n>:<i>`，避免 Add 的"同 source 版本+1"把一轮多条事实折叠成一行）。
- **严格 JSON fail-closed**：拒绝非 JSON（含围栏剥离后仍不可解析）、未知 `type`（∉ {preference,fact,decision,procedure,lesson}）、越权字段（candidate 字段白名单仅 `action/title/body/type/tags/confidence/reason`，出现 `scope/id/source/knowledgeBaseId` 等一律拒绝）、未知 `action`、超界/缺字段的 title/body/confidence——以上任何一条 ⇒ 该候选不写入；全部候选被拒 ⇒ `failed`（reason 记被拒数）；部分被拒 ⇒ 只写合法候选并在 reason 记录。系统提示词为**保守策略**（只收明确陈述或已验证的长期知识，拒绝秘密与临时输出，与 dsh-knowledge 的 CONSERVATIVE 对齐，见决策 ① 的"只收明确陈述"）。
- **fail-open（不阻断回答）**：模型调用失败、输出非法、检索失败全部归类为 `failed/skipped` 结果（reason 记录），**不向组合根返回错误**；组合根的 `extractTurn` 把它们记成 `kb/extract` 事件后静默继续。只有 Provider 级致命故障（认领/写入的存储错误）才返回 error，组合根同样转成 failed 事件继续。**提取永远不影响下一轮回答。**
- **`kb/extract` 事件（D3）**：`session` 词汇表新增 `EventKBExtract = "kb/extract"` + `NewKBExtract(status, session, turn, reason, ids)`；组合根每次提取结束都追加（created/skipped/failed + reason + 写入条目 id），载荷有界（不落模型原文/正文），`DeriveHistory` 视为不透明数据（不改 loop 结构，D4）。`kb/recall`/`kb/add`/`kb/extract` 三个 kb 事件至此齐备（design.md §3）。
- **提取模型选择（不引入新配置）**：复用现有 `internal/llm` 适配器，提取请求携带 `ExtractOpts.Model`（= 会话模型 `cfg.model`）；deepseek 适配器本来就以自身配置的 Model 为准（`ChatRequest.Model` 是 advisory），故"提取模型默认跟随当前会话模型"零配置成立。**不新增 `kb.extraction_model` 之类配置**（取舍见下）。
- **config**：新增 `kb.extraction`（`*bool`，缺省 ⇒ true，显式 `false` ⇒ 组合根跳过提取；`KBConfig.ExtractionValue()` 读取）。`kb.enabled=false`（D10 默认）时 kb 为 nil，提取与工具、召回一样完全不初始化。

**理由 / 对照 dsh-knowledge 的裁剪取舍：**

1. **为什么提取是 dsh-knowledge 的"灵魂"**：对话产生可复用知识是知识库的主要来源，没有提取回写就只有手动 `kb_add` 一条路，知识库不会自己生长（M4 验收"对话产生可复用知识能被提取并检索引用"）。
2. **为什么不引入提取专用模型配置**：提取质量依赖与对话相同的模型即可（dsh 默认也是跟随会话模型，`extractionProvider/Model` 是可选的覆盖）；增加第二套模型配置违背"不引入新配置"派发约束，且 M5 如需可再评估（一次 config 字段 + 一次 `ExtractOpts` 字段，消费方不变）。
3. **裁剪取舍（与 dsh-knowledge 的差异，M4 明确不做）**：
   - **无挂载/多知识库**：dsh 按 mount 解析可写目标并做 `routingDescription` 路由；我们单全局库，候选不需要 `knowledgeBaseId` 字段，也去掉挂载层 → 越权字段白名单更小、更严。
   - **无候选审核（audit）**：dsh 的 `audit`/`conflict` 模式留人工审核队列；M4 仅直写（`direct`），冲突/合并策略裁剪 → `action` 只保留 `create|skip`（出现 `update/conflict/audit` 即拒绝，fail-closed）。
   - **无远程**：dsh 支持 remote provider 与中央服务；M4 无远程 API，提取只走本地 SQLite/内存 Provider。
   - **无 `retention`/scope 输出**：dsh 的 `retention.{durable,evidence}` 保守门与 `scope` 字段裁剪掉——保守性由系统提示词"只收明确陈述或已验证的长期知识"承接（提示词层表达，不需要结构化字段），避免模型输出我们无法校验的字段。
   - **保留的精神**：幂等 job、严格 JSON fail-closed、fail-open 不阻断回答、拒绝秘密与临时输出、条目长度上限（title 1–200 / body 1–50000，由 `normalizeDraft` 兜底）。
4. **为什么 `extractTurn` 在 cmd/pa 而非 loop**：延续 M4b"新功能挂扩展点、不改循环"（D4）；loop 只负责 turn/step，提取是回答结束后的产品编排，属于组合根（与 `recallContext` 同层）。

## 后果

### 放弃的方案

- **向量检索 / embedding**（向量库、HNSW、嵌入模型客户端）：违背零新依赖 + CGO-free；个人规模无需语义索引；M5 如需再评估（design.md §8 已留接口）。
- **FTS5 trigram tokenizer**：需 ≥3 字查询，"架构"等两字词失效，中文不友好。
- **纯 LIKE / 纯 FTS5**：纯 LIKE 无 BM25 排序、英文无词干化；纯 FTS5 中文单 token 缺陷命中不了。二者互补才满足中文/英文/混合。

### 残余风险与后果

- **二元组兜底是启发式**：过短的查询（如"知识"）因二元组命中多条属预期；匹配无权重、无语义（"来源"会把不相干条目带进来）。个人规模可接受，M5 若引入向量可替换 Provider 解决。
- **同 source 更新只记当前版本**：版本历史表（dsh-knowledge `knowledge_versions`）留 M5 评估；M4a 满足"只记当前版本 + updated 时间"。
- **内存 Provider 是简化实现**（词包含匹配），仅用于验证接口边界与作参考实现，非生产后端；生产默认 SQLite。
- **`kb/recall` 载荷含 snippet**：正文摘要的有界性由 M4b 的注入格式化保证；事件只记录注入前的那一刻，避免日志膨胀。
- **`kb_add` 每次写独立条目（M4b 取舍）**：随机 `manual:` source 使重复记录同一事实会生成重复条目，而不是更新旧条目；v1 接受（显式写入天然低频、重复可容忍），合并/去重策略留 M5。
- **loop 的 `Recall` 是单一扩展点（M4b）**：只服务 kb 主动召回；若未来多个能力都要在 turn 前注入上下文（子代理、技能等），需把单钩子升级为统一 pre-step 扩展机制（M5 评估），届时 `Recall` 是其首个消费者。
- **提取无去重/合并（M4c 取舍）**：同一事实的重复陈述（不同 session:turn）会生成多条独立条目；候选模型提示已要求"对照 existing 只 create 新知识"，但引擎层不做正文相似度去重。M4 接受（个人规模、提取低频、`kb_search` 可按需检索），去重/合并策略留 M5（与 `kb_add` 同一取舍）。
- **提取质量依赖当前模型（M4c 取舍）**：坏输出走 fail-closed（不写、记 failed），但"该提取的没提取、不该写的写了但格式合法"这类语义误判无法被引擎拦截——保守系统提示词缓解，人工 `kb_add`/`kb_search` 可纠偏；M5 若引入独立审核队列（dsh 的 audit 模式）再评估。
- **`turn` 号从日志派生（M4c）**：`countTurns` = 日志中 `user/message` 事件数。会话命令（/new /list 等）不产生 `user/message`，故 turn 号只反映真实对话轮次；若未来 loop 增加显式 turn 标记事件（turn/start），派生规则同步升级即可（不改结构，D4）。

### 何时可重评

M5 出现子代理、多租户或共享知识库需求时，评估向量/远程 Provider（一次接口新增，消费方不变）；届时一并评估：提取去重/合并、候选审核队列、独立提取模型配置（各为一次 config/接口增量，消费方不变）。
