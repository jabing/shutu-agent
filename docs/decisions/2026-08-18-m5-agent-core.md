# ADR: M5 核心能力四段——后台任务 / 子代理 / 上下文压缩 / 技能（参照 dsh 四个能力族）

- 状态：**已定**（2026-08-18，用户拍板 M5 四项"是必须的，先实现"；本 ADR 是 M5 主 ADR，四段决策依序落地）
- 关联：design.md §10 D1–D10（重点 D3/D4/D5）；Agent.md 路线图 M5（拆四段）；参考实现 `../deepseek-harness/packages/{jobs,subagent,compaction,skill}/`
- 分阶段：M5a 后台任务（jobs）→ M5b 子代理（subagent）→ M5c 上下文压缩（compaction）→ M5d 技能（skill），每段独立验收，全部达标才算 M5 完成。M4 的"KB 最小可用集 ①②"（文档摄入/条目管理/导出投影）与"完整 dsh-knowledge 功能"整体后置于 M5 之后，不再并排。

## 背景

M4 知识库已三段验收完成（`kb` 接缝：Service/Provider/Tool + 召回 + 提取回写，FTS5 中文检索零新依赖）。用户拍板：**M5 四项（子代理/后台任务/上下文压缩/技能）是必须的，先实现**，替代原"远期按需"定位。

M5 立项前必须回答三个基线问题：

1. D5（循环串行同步；并发/后台任务推迟）的"有明确用例"条件是否已满足、如何落地并发而不破坏现有串行循环？
2. D4（薄核心，无插件系统）在引入子代理/压缩/技能后是否仍成立？dsh 的插件微内核与 scope 分层机制要裁掉哪些？
3. 四段各自引入的**新的模型可见输入**（job 状态、子代理结果、压缩摘要、技能正文/目录）如何满足 D3（模型可见 ⇒ 已落日志）而不改 loop 的 turn/step 结构（D4）？

## 总体决策：参照 dsh 四个能力族，Go 裁剪实现，仍走接缝三件套

**四个能力都以"Service Definition + Provider（可换）+ Consumer（工具/命令）"三件套落地（D2），挂在扩展点上，不改 loop 的 turn/step 结构（D4）。** 参考 `../deepseek-harness/packages/` 下 `jobs/`、`subagent/`、`compaction/`、`skill/` 四个能力族的 Service 定义与事件词汇（`docs/subsystems/{jobs,subagent,compaction,skills}.md`），Go 重写、按"个人单进程、无插件系统、无 scope 分层"裁剪。

**基线变更（本 ADR 一并锁定）**：

- **D5 重评**：明确用例已出现（长耗时工具/子代理后台续跑）。落地方式：新增 `internal/jobs` 后台任务注册表（owner 隔离、owner 上限并发），**主循环仍严格串行**，后台工作独立 goroutine 运行、通过事件回写日志，不进入现有 turn/step 路径。D5 原意（不提前上 goroutine 编排）在此转向"受控并发"。
- **D4 维持**：仍不引入插件系统/事件总线/scope 分层；四段全用 Go 接口 + 注册表。dsh 的 `ctx.effect`/fiber 生命周期机制裁掉，改为 Go 惯用的 `Close() error` / context 取消 / 显式 disposer。
- **loop 扩展点升级**（M4 ADR 决策 ⑤ 已预留）：现有单钩子 `loop.Config.Recall`（服务 kb 主动召回）在 M5b 升级为**统一的 pre-step 上下文注入扩展点**（`Config.PreStep`，可注册多个注入器，kb 召回是其首个消费者，M5b 子代理目录/技能目录随 M5d 接入）。turn/step 结构仍零改动。

**四段共同的新事件类型**（D3，均 log-only、`DeriveHistory` 视为不透明数据，不进入模型消息派生；模型实际看到的载荷由组合根/工具直接注入并同步落日志）：

| 段 | 新事件类型 | 载荷要点 |
|---|---|---|
| M5a | `job/start`、`job/status`、`job/done` | job id/kind/label/ownerSession/status/detail/output 摘要（有界） |
| M5b | `subagent/start`、`subagent/end`、`subagent/report` | child session id/provider/父会话/结果摘要或报告 |
| M5c | `compaction/start`、`compaction/summary`、`compaction/end`、`compaction/prune` | 锁/范围/被遮蔽 seq/token 数/摘要投影/model 调用（压缩摘要**本身**作为带 `surfaceOp: replace` 的 `user/message` 遮蔽旧范围） |
| M5d | `skill/load`（可选）、`skills/change`（可选） | 技能名/来源/被加载正文摘要；目录变更由组合根重读 |

**保留的硬约束**：CGO-free、零新第三方依赖（全部在现有 Go 标准库 + 已钉依赖内实现）、API Key 只走环境变量、默认关闭（D10）——每个新 Consumer 工具都走白名单。

---

## 决策 ① M5a 后台任务（jobs）—— owner 隔离的后台工作注册表

**新增 `internal/jobs` 包：Job 注册表（Service）+ 进程内本地实现（Provider）+ 模型侧控制工具（Consumer），让长耗时工作以"后台 job"形式运行、观察、取消、等待、通知，同时保持主循环串行（D5 重评的受控并发）。**

- **注册表接口**（`internal/jobs/service.go`）：
  ```go
  type Status string   // running | stopping | completed | killed | failed
  type Kind    string  // 不透明 id 命名空间（如 "bash"、"subagent"、"extract"），注册表不解释
  type JobSnapshot struct {
      ID, Kind, Label string
      OwnerSession    string   // 授权边界：owner 会话 id，非保密性（id 可预测，授权是关键）
      Status          Status
      Detail          string   // kind 特定状态细节（"exit code: 3"、"max-tokens"）
      StartedAt, FinishedAt time.Time
      OutputLimitBytes int
  }
  type JobStart struct {
      Kind, Label string
      OwnerSession string
      OutputLimitBytes int
      Run func(ctx context.Context) (JobOutcome, error)  // 前台可取消执行体
      Cancel func(reason string) error                    // 同步、幂等
  }
  type JobOutcome struct{ Status Status; Detail, Output string }
  ```
  - 生命周期：`start`（preflight owner/上限 → 原子注册 → 后台 goroutine 跑 `Run`）→ `list/get`（owner 过滤快照）→ `read`（流式增量或终态幂等输出，标记 reported）→ `kill`（请求取消，返回 requested/already-finished）→ `wait`（有界等待，超时返回当前快照）。
  - **owner 隔离**：job 属于某会话；跨 owner 的 `get/read/kill/wait` 拒绝（授权基于 owner 匹配，id 可预测不保密）。无 owner 的 job 任何人可观察，用于守护工作。
  - **并发上限**：本地实现 `max_concurrent_jobs_per_owner`（默认 10，配置可改），计数 running+stopping；上限由 owner 放行，终端结算释放配额。**主循环不被 job 阻塞**：job 后台执行，主循环继续。
  - **生命周期可逆**（D 系列"副作用必须可逆"）：`Close()` 取消并等待所有活 job；owner 会话销毁时同 owner job 被取消。job 表存内存（个人单进程），不持久化跨重启（重启即清空，重启后遗留 job 无法继续——`jobs` 只服务当前进程，M5b 的"子代理会话"持久化另论）。
  - **owner 存在性 preflight（M5a-1 实现说明）**：`internal/jobs` 包内无会话注册表可查（会话注册表属组合根/M5a-2 接线），故 `Start` 的 preflight 落为 spec 字段校验（kind/label 非空、Run 非 nil、OutputLimitBytes≥0）+ 并发上限检查；owner 授权在访问时（`get/read/kill/wait` 的 lookup）强制。组合根在工具层注入当前会话 id 作为 owner，保证 job 归属于真实存在的会话。
  - **事件落日志的并发安全（M5a-2 实现决策）**：job 状态迁移发生在后台 goroutine，而 `session.Log` 非并发安全——若组合根在后台 goroutine 里直接 `a.log.Append`，会与主循环的串行追加竞态（D5）。因此 **`job/*` 事件全部在 job_* 工具的 `Execute` 内（主循环串行路径）落日志**：`job_start` 落 `job/start`，观察类工具（status/cancel/wait/read）通过共享 transition tracker 对每个 `(id,status)` 恰好落一次 `job/status`/`job/done`。M5b/c/d 的接线遵循同一模式：后台完成的模型可见输入，一律经串行工具路径或组合根回调落日志，绝不从后台 goroutine 直接追加会话日志。
- **Consumer（工具）**：`job_start`（注册一个后台工作，返回 job id）、`job_status`、`job_cancel`、`job_wait`（有界）、`job_read`（读输出）。默认关（D10），白名单门同 kb 工具模式。组合根注册工具 + 事件落日志。
- **config**：`jobs.enabled`（默认 false）、`jobs.max_concurrent_jobs_per_owner`。
- **事件（D3）**：`job/start`（注册成功，含 id/kind/label/owner）、`job/status`（running→stopping 等状态迁移，含 detail）、`job/done`（终态，含输出摘要）。载荷有界（输出只记摘要 + spill 定位符，全文落 `data/spill/`）。
- **本段明确不做**：job 持久化/跨重启续跑、分布式/远程注册表、job 优先级队列、自动重试（M5 后续按需）。

**理由**：M5b 子代理的"后台续跑"与"长耗时工具"都需要统一的后台执行/观察/取消协议；dsh 把 job registry 做成 owner-fenced 的独立能力族而非 loop 一部分，正是为了"后台运行不影响串行主循环"（D5 语义）。个人单进程用内存注册表即可，持久化对单机无意义。

## 决策 ② M5b 子代理（subagent）—— 委托给子代理（独立会话 + 独立 agent），spawn 优先，continuable 后置

**新增 `internal/subagent` 包：子代理运行时（Service，多 Provider 共存注册表）+ `spawn-in-process` Provider（进程内新会话子代理，默认）+ 委托/控制/报告工具（Consumer）。让主代理把任务委托给拥有独立会话日志的子代理，子代理结果回传父会话，可后台续跑（挂在 M5a job 上）。**

- **服务接口**（`internal/subagent/service.go`）：
  ```go
  type Provider interface {
      Name() string
      Capabilities() Capabilities   // 声明支持：outputSchema/depthLimit/toolFilter/persona
      Start(ctx context.Context, req StartRequest) (*Run, error)
  }
  type StartRequest struct {
      Label string
      Prompt string              // 子代理首条 user 消息
      ParentSessionID string
      MaxDepth int               // 委托深度上限
      ToolFilter []string        // 子代理可见/可执行工具白名单（可选）
      Persona string             // 可选子代理人格
      Signal <-chan struct{}     // 取消通道
  }
  type Run struct {
      ID string                   // 子代理会话 id（本地 provider 下即子会话）
      Result func(ctx) (Result, error)  // 终态：输出 + stopReason
      Cancel func(reason) error
  }
  type Result struct { Output string; StopReason string }  // completed|aborted|error|max-tokens|refusal
  ```
  - **Provider 注册表**：多 Provider 按名共存（dsh 同款，`list/getProvider/registerProvider`）；本地默认 `spawn`（全新子会话），`fork`（继承父会话已完成回合前缀，即 dsh `subagent-fork-in-process` 的 seed 语义）视 M5b 验收余量再上。
  - **子代理 = 完整独立 Agent**：子会话走同一 `internal/session` + `internal/loop`（复用核心，loop 是库，可实例化多个），有自己独立的 `user/message` 序列与 `assistant/message` 终结；父会话 `parent_session` 记录委托来源。**loop 代码零改动**（D4）——子代理只是"另一个会话 + 另一个 loop 实例"，由组合根驱动。
  - **深度与上下文**：子代理会话记录委托深度（父深度 + 1，持久化在会话 header）；`fork` 的 seed 是父会话到最后一个 `turn/end` 的完整回合前缀（平衡前缀，确保 replay 合法），与 dsh `CreateAgentOptions.seed` 同语义。
  - **后台续跑（continuable，M5b 核心价值之一）**：子代理以 job 形式后台运行（M5a）：父代理调用委托工具后不阻塞等待，拿到 child session id 继续；子代理完成后经 `job/done` 通知 + 终态结果入 `subagent/end` 事件。父代理可 `send_message`（续跑消息）、`interrupt`（取消当前回合）、`list`（子代理枚举）。
  - **报告通道**：子代理可显式 `report` 给父会话（`subagent/report` 事件 + 作为父会话的 `user/message` 注入，D3 双向满足）。
- **Consumer（工具）**：`subagent_spawn`（委托，返回 child id）、`subagent_send`（续跑消息）、`subagent_interrupt`、`subagent_list`。默认关（D10）。工具 schema 在 Execute 入口统一校验（D7）。
- **事件（D3）**：`subagent/start`（child session id/provider/父会话）、`subagent/end`（终态 + 输出摘要）、`subagent/report`（报告载荷）。`DeriveHistory` 视为不透明数据。
- **config**：`subagent.enabled`（默认 false）、`subagent.max_depth`（默认 8）、`subagent.default_provider`（默认 `spawn`）。
- **本段明确不做（dsh 裁剪，理由同 M4 决策 ⑦ 的"裁剪取舍"精神）**：ACP/Codex/Claude Code/SDK 远程 Provider（个人单进程无外部子进程协作）、`outputSchema` 结构化结果（v1 只回文本）、scope 分层与 fiber 生命周期（无插件系统）、continuable 的冷恢复/持久化激活管理（子代理会话可持久化，但"激活/恢复"状态机裁剪——`fork`/`send`/`interrupt` 都只针对**当前进程内活着的**子代理；重启后子代理会话可 resume 但不再有激活管理）。**何时可重评**：出现需要跨重启续跑子代理或结构化返回的明确用例时。

**理由**：子代理是"把大任务拆给独立上下文的子 agent"的标准能力（dsh 也是独立能力族而非 loop 一部分）。独立会话 + 独立 loop 实例 + 后台 job，三个基础都在 M5a 及核心已备，接缝成本低；多 Provider 注册表为未来外部子进程 Provider 预留（D2）。

## 决策 ③ M5c 上下文压缩（compaction）—— 摘要遮蔽旧范围，纯事件落地，loop 零改动

**新增 `internal/compaction` 包：压缩接缝（Service Definition）+ 基础 Provider（token 压力 + LLM 摘要）+ 可选 tool-result 剪枝 + 人工 `/compact` 命令（Consumer）。长会话超预算时，把一段 surface 范围摘要成一条带 `surfaceOp: replace` 的 `user/message` 并遮蔽原事件，日志仍追加式（D1），DeriveHistory 折叠规则据此排除被遮蔽事件（D4）。**

- **服务接口**（`internal/compaction/service.go`）：
  ```go
  type Trigger string   // pressure | context-overflow
  type Result struct {
      CompactionID string
      Summary string          // 摘要正文
      ShadowedRange [2]int64  // 被遮蔽 surface 位置跨度（首/尾 seq）
      ShadowedSeqs []int64    // 被遮蔽的 surface 节点 seq（权威集合）
      ShadowedTokens int
  }
  type Engine interface {
      CompactIfNeeded(ctx, trigger Trigger, session SessionLike) (*Result, error)
      CompactNow(ctx, session SessionLike) (*Result, error)   // 低于压力也执行一次有效压缩
      CompactRegion(ctx, start, end int64, session SessionLike) (*Result, error)
  }
  ```
  - **压缩语义**（dsh 同款，Go 简化）：
    1. **事件三连锁**：`compaction/start`（占锁，标识本次尝试）→ 摘要生成 → `compaction/summary`（记录摘要投影 + 被遮蔽范围/seq/token + model 调用）→ `compaction/end`（释放锁）。崩溃中断产生"无配对的 start"即可探测（孤儿锁）。
    2. **摘要本身落成新 `user/message`**，载荷带 `surfaceOp: {op: "replace", start, end}`；`DeriveHistory` 折叠规则遇到该标记时，跳过被遮蔽 seq 的旧事件、以摘要消息替代（**日志仍追加式，D1 不变**——旧事件物理保留，只是派生时被遮蔽）。
    3. **tool-call/result 配对边界**：压缩范围边界必须保持 assistant 的 tool_calls 与其 tool/result 配对完整（不能截断配对中间）。提供 `ToolPairingBalancedBefore/After` 检查。
  - **Provider**：默认 `basic`——token 压力检测（估算当前 surface token 数，超阈值触发；压力以 `pressure` 触发，overflow 以更激进策略）、保留尾部策略（保留最近 N 个回合）、调用当前模型生成摘要（复用 `internal/llm`，零新配置）。
  - **可选 tool-result 剪枝**：`internal/compaction/pruner.go`——纯确定性（无模型），对超预算的 `tool/result` 做 head/middle/tail 截断替换，记 `compaction/prune` 事件（定价被遮蔽节点，便于纯消费者扣除）。
  - **触发编排**：自动触发挂在 pre-step 扩展点（M5b 升级后的 `PreStep` 注入器之一，优先于消息派生执行）；`/compact` 人工命令走 `CompactNow`。
  - **事件（D3）**：`compaction/start`、`compaction/summary`、`compaction/end`、`compaction/prune`，均 log-only，`DeriveHistory` 处理 `compaction/*` 为"派生规则输入"而非模型消息。
  - **config**：`compaction.enabled`（默认 false）、`compaction.token_threshold`（默认 32000）、`compaction.retain_turns`（默认 8）。
  - **本段明确不做**：token 计费服务抽象（个人单进程直接按字符/词估算）、多 Provider 并发压缩、压缩审计队列。

**理由**：长会话 token 爆掉是个人 agent 必然遇到的实际问题（上下文压缩是"让会话活得久"的关键）。dsh 把压缩做成独立能力族（不碰 loop），其"事件三连锁 + 摘要以 replace 遮蔽 + 日志追加式"方案与我们 D1/D3/D4 完全同构，是裁剪移植的最佳模板。

## 决策 ④ M5d 技能（skill）—— 可发现、可加载的复用指令，provider 注册表 + 文件系统 Provider + 目录/加载工具

**新增 `internal/skill` 包：技能注册表（Service，多 Provider 共存）+ 文件系统 Provider（默认，从项目/用户目录发现 SKILL.md）+ 目录注入 + `skill` 加载工具（Consumer）。让主代理能发现并加载可复用指令（类似本会话的技能目录），供模型按需阅读后执行。**

- **服务接口**（`internal/skill/service.go`）：
  ```go
  type Provider interface {
      Name() string
      List(ctx) ([]Candidate, error)      // 发现候选（name/description/rank/locator）
      Get(ctx, c Candidate) (*Definition, error)  // 加载完整正文
  }
  type Candidate struct { Name, Description, Source string; Rank int }
  type Definition struct {
      Name, Description, Content string
      Source, Path string
      ModelInvocable, UserInvocable bool
  }
  ```
  - **注册表**：多 Provider 按名共存；同名按 rank 优先后 provider 序、注册序裁决（dsh 同款）。默认文件系统 Provider 扫描根（按 rank）：`<projectRoot>/.dsh/skills`（100）、`<projectRoot>/.agents/skills`（200）、用户目录 `<userHome>/.dsh/skills`（300，config 可加自定义目录）。技能身份：kebab-case 名，`<name>/SKILL.md` 目录束或 `<name>.md` 平铺文件（不递归发现）。frontmatter 支持 `disable-model-invocation` / `user-invocable`。
  - **目录注入（D3）**：会话开始时（pre-step 扩展点注入器，M5b 统一机制的消费者），把**技能目录**（排序后的 `name + description`，不塞正文/路径/来源）注入为 `user/message`（或 system-reminder），并在 `skill/catalog` 事件落日志。目录变更重读（组合根轮询或变更回调）。
  - **Consumer（工具）**：`skill_load(name)`——校验 kebab-case → 查目录 → 加载完整正文返回给模型（`<skill_content>`）。默认关（D10）。模型按需调用。
  - **事件（D3）**：`skill/catalog`（目录注入，含条目数/版本）、`skill/load`（加载记录，含技能名/来源/正文摘要）。`DeriveHistory` 视为不透明数据。
  - **config**：`skill.enabled`（默认 false）、`skill.dirs`（自定义技能目录列表，默认空）、`skill.catalog_max_chars`（目录描述上限，默认 500）。
  - **本段明确不做（dsh 裁剪）**：scope 分层/宿主+按 scope 分层（无插件系统）、chokidar 文件监视自动失效（v1 用组合根按需重读，变更后下一次 pre-step 重取）、远程技能 Provider、打包 badge 技能（bundled 技能可后续用 `skill.dirs` 指向项目内目录实现）。

**理由**：技能 = "可复用指令的发现与按需加载"，与个人 agent"把常用流程沉淀成可复用能力"直接同构（本项目自身的 `skills/` 就是活例）。dsh 的 provider 注册表 + 文件系统发现 + 目录注入 + `skill` 加载工具四件套是干净模板，Go 裁剪后零新依赖。

---

## 后果

### 基线变更汇总

- **D5 重评**：从"并发/后台任务推迟"改为"受控并发：后台任务/子代理走 `internal/jobs` owner-fenced 注册表，主循环仍串行"。design.md §10 同步。
- **loop 扩展点**：`Config.Recall`（M4b）升级为 `Config.PreStep`（可注册多个 pre-step 注入器，kb 召回为首个消费者，M5b 子代理目录、M5d 技能目录随后接入）；turn/step 结构零改动（D4）。
- **事件词汇表扩充**：`job/*`、`subagent/*`、`compaction/*`、`skill/*` 加入 `internal/session`，全部 log-only，`DeriveHistory` 不透明处理（compaction 除外：其为派生规则的输入）。
- **config 扩充**：`jobs.*`、`subagent.*`、`compaction.*`、`skill.*` 四块，全部 `enabled` 默认 false（D10）。

### 放弃的方案

- **插件系统/事件总线/scope 分层/fiber 生命周期**（dsh 核心机制）：单进程个人 agent 不需要；Go 接口 + 注册表 + `Close()`/disposer 足够（D4 维持）。
- **job 持久化/跨重启续跑**：单机内存注册表即可，重启后遗留 job 无法继续，接受。
- **远程子代理 Provider（ACP/Codex/Claude Code/SDK）**：个人单进程无外部子进程协作场景；Provider 注册表已预留，未来按需。
- **continuable 子代理的冷恢复/激活状态机**：子代理会话可持久化 resume，但"激活管理"（激活权、冷恢复、子代继承、FIFO 队列）状态机裁剪；`send`/`interrupt` 只针对进程内活着的子代理。
- **outputSchema 结构化子代理返回**：v1 只回文本，结构化返回按需评估。
- **token 计费服务抽象**：压缩用简单估算（字符/词），不建计费服务。
- **技能文件监视自动失效**：组合根按需重读（下一次 pre-step 重取），不引入 chokidar。

### 残余风险与后果

- **子代理深度/循环安全**：子代理复用同一 loop 库，必须保证"子代理的 `user/message` 序列独立、终结可靠、不串扰父会话日志"；由测试断言（子代理完整回合可重放、父会话不受污染）。
- **压缩遮蔽的正确性**：`surfaceOp: replace` 的派生折叠是**派生规则**的变更，必须与事件类型一起落测试（折叠后历史= 摘要 + 未遮蔽尾部；tool-call/result 配对边界不被切断）。
- **后台 job 与取消**：Windows 下 job 取消沿用 M3 的进程终止策略（Windows 杀直系进程、Unix 进程组 `kill(-pgid)`）；job 输出 spill 同 M3。
- **技能正文安全**：技能是本地可信文件，加载后作为模型指令输入，不执行；`skill_load` 返回正文有长度上限（防超长注入）。
- **目录注入体积**：pre-step 注入器必须对注入内容有界（目录只含 name+description，压缩/召回有界），避免四段注入互相挤爆上下文——统一由 `PreStep` 注入器的"预算"控制（每个注入器有分配上限，超出 fail-open）。
- **M4 后置项**：KB 最小可用集 ①②（文档摄入/条目管理/导出投影）与完整 dsh-knowledge 功能整体后置到 M5 之后，M4 验收标准不变（已达标）。

### 何时可重评

M5 四段验收后，若出现以下任一需求再评估增量（均为一次接缝扩展，消费方不变）：job 持久化/跨重启续跑；子代理外部 Provider / 结构化返回 / continuable 冷恢复；压缩 token 计费服务 / 审计队列；技能远程 Provider / 文件监视自动失效。知识库语义检索（向量 Provider，M4 ADR 预留位）仍在 M5 之后的待评估列表，与"完整 dsh-knowledge 功能"一同后置。
