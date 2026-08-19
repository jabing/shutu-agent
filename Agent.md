# Agent.md — 个人 Agent 项目全局规划

> 本文是项目工作入口：状态、路线图、开发纪律都在这里。
> 设计基线在 [`docs/design.md`](docs/design.md)——**改设计先改那里，再改代码**。

---

## 1. 项目定位

Go 实现、借鉴 DeepSeek Harness 架构的个人 Agent：薄核心（会话日志 + LLM 适配 + 工具注册表 + 提示词组装 + 循环），后期以"能力接缝"方式接入个人知识库（RAG）。参考实现：`../deepseek-harness`（重点读 `docs/architecture.md`、`docs/subsystems/core.md`、`docs/subsystems/session.md`）。

## 2. 设计基线（防漂移摘要，细节见 design.md）

- **D1** 会话 = 追加式事件日志，历史是派生值，永不另存。
- **D2** 新能力 = Service / Provider / Tool 三件套，消费方只依赖接口。
- **D3** 模型可见 ⇒ 已落日志；新模型可见输入 ⇒ 新事件类型。
- **D4** 薄核心；v1 用 Go 接口 + 注册表，不引入插件系统/事件总线。
- **D5** 循环串行同步；并发、后台任务推迟到 M5（有明确用例才做）。
- **D6** LLM 适配器第一天就支持 SSE 流式。
- **D7** 工具参数在 Execute 入口统一 JSON Schema 校验。
- **D8** 持久化走 store 接口（SQLite 后端，CGO-free），事件带版本号。
- **D9** 知识库是能力接缝（kb service + 可换 Provider + kb_* 工具）；检索与对话模型解耦（M4 用 FTS5 全文检索 + 提取回写，向量/embedding 仅作 M5 可选 Provider 预留）。
- **D10** 安全白名单先行；执行类工具（bash 等）M3 才上。

## 3. 当前状态

**2026-08-18**：M3 完成并通过验收（提交 `1dda2ed`，ADR `2026-08-18-m3-sandbox-scope`）。M4 知识库**三段全部完成并通过验收**：M4a 内核（`682f07e`）、M4b 工具与召回（`bdd903d`）、M4c 提取回写（`5e98fa7`）。方向：**参照 dsh-knowledge（已下载 `../dsh-knowledge`，FTS5 全文检索 + 提取回写，非向量 RAG，方案已实测）**，调研见 `docs/research-m4-kb.md`，派发见 `docs/dispatch-m4a/b/c.md`，ADR `2026-08-18-m4-kb-architecture.md` 完整定稿七项决策。**下一步：M5 核心能力四段**（用户拍板"必须、先实现"；ADR `2026-08-18-m5-agent-core.md` 已定稿）：M5a 后台任务 → M5b 子代理 → M5c 上下文压缩 → M5d 技能，逐段派发验收。

## 4. 路线图

| 里程碑 | 交付物 | 验收标准（达标才算完成） | 状态 |
|---|---|---|---|
| **M1 最小循环** | `cmd/pa` REPL；`llm`（DeepSeek 流式）；`session` 内存日志；`tools` 注册表 + `get_time`/`read_file`；`loop` 串行 turn/step | 命令行提问可流式回答；工具可被调用并回写日志；`go vet` + `go test` 干净 | ✅ 2026-08-18 验收通过（`6380163`） |
| **M2 持久化与会话** | `store`（SQLite）+ 多会话（/new /list /resume）；`prompt` 分节组装；`config.yaml`；重试策略 | 重启恢复会话且历史完整回放；新事件类型不改历史结构 | ✅ 2026-08-18 验收通过（`e865aca`） |
| **M3 安全与完善** | 工具白名单/权限；超时与输出截断；取消（Ctrl+C）；CLI 完善（Web 可选） | 未白名单工具拒绝执行；取消即时生效；长输出不爆上下文 | ✅ 2026-08-18 验收通过（`1dda2ed`，ADR `2026-08-18-m3-sandbox-scope`） |
| **M4 知识库**（三段） | 拆为 M4a/b/c 依次验收 | 全部达标才算 M4 完成 | ⬜ |
| **M4a 内核** | `kb` 接口（Search/Add/Recall）+ SQLite FTS5 Provider（BM25 + 中文二元组 LIKE 兜底）+ `kb/recall` 事件类型 + config；主 ADR 定稿检索方案 | 中文/英文/混合检索正确；`Add` 后能检索；换 Provider 不改消费方；零新依赖 | ✅ 2026-08-18 验收通过（`682f07e`，ADR `2026-08-18-m4-kb-architecture.md`） |
| **M4b 工具与召回** | `kb_search`/`kb_read`/`kb_add` 工具（默认关）+ `cmd/pa` 召回注入（catalog + 有界 recall）+ `/kb-status` `/kb-reindex` + `kb/add` 事件 | 工具默认关闭且参数校验；注入走 `kb/recall` 落日志；fail-open | ✅ 2026-08-18 验收通过（`bdd903d`） |
| **M4c 提取回写** | `KB.Extract`（幂等 `session:turn`、严格 JSON fail-closed、不阻断回答）+ `kb/extract` 事件 + config；补 ADR | 对话产生可复用知识能被提取写入并被后续检索；坏输出 fail-closed | ✅ 2026-08-18 验收通过（`5e98fa7`） |
| **M4 知识库**（三段） | 拆为 M4a/b/c 依次验收 | 全部达标才算 M4 完成 | ✅ 三段全部完成 |
| **M5 核心能力**（四段，ADR `2026-08-18-m5-agent-core.md`） | 拆为 M5a/b/c/d 依次验收 | 全部达标才算 M5 完成 | ⬜ 已定稿 ADR，派发中 |
| **M5a 后台任务** | `jobs` 接口（owner-fenced 注册表）+ 本地实现 + `job_*` 工具 + `job/*` 事件 + config | 后台工作可观察/取消/等待/通知；owner 隔离；主循环保持串行；默认关闭 | ⬜ |
| **M5b 子代理** | `subagent` 接口（多 Provider 注册表）+ spawn 实现 + 委托/控制/报告工具 + `subagent/*` 事件 + config | 子代理独立会话日志可回放；结果回传父会话；后台续跑走 job；默认关闭 | ⬜ |
| **M5c 上下文压缩** | `compaction` 接缝 + 摘要 provider + tool-result 剪枝 + `/compact` + `compaction/*` 事件 | 超预算触发压缩；摘要遮蔽旧范围且日志仍追加式；tool-call/result 配对不被切断；默认关闭 | ⬜ |
| **M5d 技能** | `skill` 接口（多 Provider 注册表）+ 文件系统发现 + 目录注入 + `skill` 加载工具 + `skill/*` 事件 + config | 目录注入有界；按需加载完整正文；默认关闭 | ⬜ |

## 5. 开发纪律（每轮工作前过一遍）

1. **新功能不改循环**（D4）：能力一律走接缝（接口 + 后端 + 工具）。
2. **模型可见必落日志**（D3）：先加事件类型，再实现。
3. **工具参数入口校验**（D7）：Execute 之前统一 JSON Schema 校验。
4. **先文档后代码**：涉及核心数据模型、循环结构、包依赖方向的变更，先写 `docs/decisions/` 决策记录并更新 design.md。
5. **保持 CGO-free**（Windows 可无工具链构建）；新依赖必须纯 Go 或可无 CGO 使用。
6. **API Key 只走环境变量**，绝不写入代码、配置或日志。
7. **双向同步**：design.md 与本文状态/决策变更必须同步更新。
8. **一里程碑一 PR/提交**：按验收标准检查后才算完成，不达标不进入下一里程碑。

## 6. 决策记录（ADR）

路径：`docs/decisions/YYYY-MM-DD-<slug>.md`。模板：状态（提案/已定/废弃）→ 背景 → 决策 → 理由 → 后果（含放弃的方案）。已有决策见 design.md 第 10 节 D1–D10，ADR 只记录其后的增量变更。

## 7. 常用命令

```sh
go build ./...        # 构建
go test ./...         # 单元测试
go vet ./...          # 静态检查
go run ./cmd/pa       # 启动 REPL（M1 后可用，需 DEEPSEEK_API_KEY）
```

## 8. 会话交接协议（控制面 / 实施面）

**分工**：本会话（控制面）定契约、验收、更新状态；实施会话（实施面）读契约、写代码、自测。会话间唯一可靠通信渠道是磁盘文件——新会话看不到控制会话的对话历史，只依赖本文档与 design.md。

**流程**：

1. **交接**：控制会话把开场白模板（见下）发给实施会话，指定里程碑；各里程碑的完整派发消息存于 `docs/dispatch-*.md`（最新：`docs/dispatch-m5a.md` → `dispatch-m5b.md` → `dispatch-m5c.md` → `dispatch-m5d.md` 依序派发；历史：`docs/dispatch-m4a/b/c.md`、`docs/dispatch-m3.md`、`docs/dispatch-m2.md`）。
2. **实施**：实施会话按 design.md 实现，自测通过后提交，并报告：改动文件清单、实现决策、跑过的命令、测试结果。
3. **验收**：控制会话亲自跑 `go build` / `go test` / `go vet`，审查 `git diff`，对照 D1–D10 逐条检查（日志先行、工具入口校验、接口隔离、无循环改动、无越界功能）。
4. **收尾**：通过 → 更新第 3/4 节状态 → 准备下一里程碑交接；不通过 → 把问题清单发回实施会话修订。

**实施会话开场白模板**（直接粘贴）：

> 请阅读 `D:\dev-projects\Agent\personal-agent\Agent.md` 和 `docs/design.md`，按设计基线实现 **M1 最小循环骨架**（里程碑验收标准见 Agent.md 第 4 节）。参考原型 dsh 的源码与文档在 `D:\dev-projects\Agent\deepseek-harness`——实现每个模块前先读 Agent.md 第 9 节对应的 dsh 源码与文档，借鉴其结构与接口设计（注意 dsh 是 TypeScript + 插件框架，只需借鉴思路，不照搬代码，Go 实现按 design.md 的模块地图落地）。完成后运行 `go vet ./...`、`go test ./...`、`go build ./...` 并全部通过，然后报告：改动文件清单、实现决策、测试结果。严格遵守 design.md 第 10 节 D1–D10，不要引入任何超出 M1 范围的功能。

**并行原则**：同一里程碑只派一个实施会话；需要并行时按包目录划分所有权（如 `session`/`store` 与 `kb` 分属不同会话），各会话只写自己负责的目录。

**防跑偏红线**：实施会话的报告不作为验收依据；越界功能（超出里程碑范围）一律退回，不合并。

## 9. 参考链接

### 文档

- 设计基线：[`docs/design.md`](docs/design.md)
- 原型架构：[`../deepseek-harness/docs/architecture.md`](../deepseek-harness/docs/architecture.md)
- dsh 循环细节：[`../deepseek-harness/docs/subsystems/core.md`](../deepseek-harness/docs/subsystems/core.md)
- dsh 会话日志：[`../deepseek-harness/docs/subsystems/session.md`](../deepseek-harness/docs/subsystems/session.md)
- dsh 能力接缝：[`../deepseek-harness/docs/capability-seams.md`](../deepseek-harness/docs/capability-seams.md)
- M4 参照插件（知识库，FTS5 + 提取回写）：[`../dsh-knowledge/`](../dsh-knowledge/)（[GitHub](https://github.com/lemoncat7/dsh-knowledge)）+ 调研 [`docs/research-m4-kb.md`](docs/research-m4-kb.md)
- M5 参照四个能力族：[`../deepseek-harness/packages/jobs/`](../deepseek-harness/packages/jobs/)、[`../deepseek-harness/packages/subagent/`](../deepseek-harness/packages/subagent/)、[`../deepseek-harness/packages/compaction/`](../deepseek-harness/packages/compaction/)、[`../deepseek-harness/packages/skill/`](../deepseek-harness/packages/skill/) + 子系统文档 [`docs/subsystems/{jobs,subagent,compaction,skills}.md`](../deepseek-harness/docs/subsystems/jobs.md)；M5 主 ADR `docs/decisions/2026-08-18-m5-agent-core.md`

### 源码参考（`../deepseek-harness/packages/`）

实现每个模块前先读对应源码，借鉴结构、接口划分与边界设计；dsh 是 TypeScript + Cordis 插件框架，**只借鉴思路，不照搬代码**。

| 本模块 | dsh 参考源码 | 重点看什么 |
|---|---|---|
| `loop` | `core/agent-loop/` | 循环驱动、turn/step 状态机 |
| `session` | `core/session/` | 事件日志、历史派生（deriveMessages） |
| `tools` | `core/tools/` | 工具注册表、参数校验、执行管道 |
| `prompt` | `core/system-prompt/` | 提示词分节组装 |
| `llm` | `llm/llm/` + `llm/llm-deepseek/` | 适配器接口、流式、DeepSeek 实现 |
| `store`（M2） | `session/session-persistence*` | 持久化与重放 |
| `kb`（M4） | `../dsh-knowledge/src/`（domain/local-provider/retrieval/extraction/tools/recall）+ `web/`（seam 三件套模板） | 知识条目模型、FTS5 检索 + 中文二元组兜底、提取回写、能力接缝的包划分 |
| `jobs`（M5a） | `../deepseek-harness/packages/jobs/{jobs,jobs-local,tool-jobs}/` | owner-fenced 后台任务注册表、生命周期契约、模型侧控制工具 |
| `subagent`（M5b） | `../deepseek-harness/packages/subagent/{subagent,subagent-spawn-in-process,tool-subagent,tool-subagent-control,tool-subagent-report}/` | Provider 注册表、委托/控制/报告、子代理会话 |
| `compaction`（M5c） | `../deepseek-harness/packages/compaction/{compaction,compaction-basic,compaction-tool-result-pruner,command-compact}/` | 压缩接缝、摘要 provider、tool-result 剪枝、人工命令 |
| `skill`（M5d） | `../deepseek-harness/packages/skill/{skill,skill-filesystem,tool-skill}/` | 技能 provider 注册表、文件系统发现、目录/加载工具 |
