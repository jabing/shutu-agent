# M5b 实施派发消息（控制会话 → 实施会话）——子代理 subagent

> 状态：已派发 2026-08-19（M5 拆四段：M5a 后台任务 ✅ → M5b 子代理 → M5c 上下文压缩 → M5d 技能；本文件为第二段）· 用法：把下文整段粘贴给新开的实施会话。

---

请阅读 `D:\dev-projects\Agent\shutu-agent\Agent.md`、`docs/design.md`（§4 pre-step 扩展点、§10 D4/D5）**和 `docs/decisions/2026-08-18-m5-agent-core.md`（M5 主 ADR，本段对应"决策 ②"）**，并通读参考源码 `D:\dev-projects\Agent\deepseek-harness\packages\subagent\`（重点：`subagent/`（Provider 注册表、`src/types.ts`、`src/index.ts`）、`subagent-spawn-in-process/`（进程内新会话子代理）、`tool-subagent/`（委托工具）、`tool-subagent-control/`（send_message/interrupt/list）、`tool-subagent-report/`（报告通道））以及 `docs/subsystems/subagent.md`（Provider 契约、深度、seed、结果、事件），按设计基线实现 **M5b 子代理**（M5 第二段；本段验收标准见下，M5 完整验收标准见 Agent.md 第 4 节）。

**前置依赖**：本段依赖 **M5a 的 `internal/jobs.Registry`**（子代理后台续跑挂在 job 上）。若 M5a 验收后 `jobs` 接口有调整，以**当前代码**为准（`internal/jobs/service.go`），本文档中的 `jobs` 引用仅为契约意图。

**M5b 范围（只做子代理，不碰压缩/技能）**：

1. **`internal/subagent` 包——子代理运行时（seam 的 Service 定义，多 Provider 注册表）**：
   ```go
   type Capabilities struct {
       OutputSchema bool
       DepthLimit   bool
       ToolFilter   bool
       Persona      bool
   }

   type StartRequest struct {
       Label         string
       Prompt        string
       ParentSessionID string
       MaxDepth      int      // 0 = 不设上限
       ToolFilter    []string // 子代理可见工具白名单（可选）
       Persona       string   // 可选子代理人格
   }

   type Result struct {
       Output     string
       StopReason string   // completed | aborted | error | max-tokens | refusal
   }

   type Run struct {
       ID     string                       // 子代理会话 id（本地 provider 即子会话）
       Result func(ctx context.Context) (Result, error)
       Cancel func(reason string) error
   }

   type Provider interface {
       Name() string
       Capabilities() Capabilities
       Start(ctx context.Context, req StartRequest) (*Run, error)
   }

   type Runtime interface {
       RegisterProvider(p Provider) error
       GetProvider(name string) (Provider, bool)
       ListProviders() []string
       Start(ctx context.Context, name string, req StartRequest) (*Run, error)   // 校验 capabilities 后委托
       ListChildren(ctx context.Context, parentSessionID string) ([]ChildSummary, error)
       Close() error
   }
   ```
   - **子代理 = 完整独立 Agent**：子会话走同一 `internal/session` + `internal/loop`（loop 是库，可实例化多个），独立 `user/message`/`assistant/message` 序列；父会话记录 `parent_session`。**loop 代码零改动**（D4）——子代理只是"另一个会话 + 另一个 loop 实例"，由组合根驱动。参考 `../deepseek-harness/packages/subagent/subagent-spawn-in-process/`。
   - **深度**：子会话记录委托深度（父深度 + 1，存会话 header）；`MaxDepth > 0` 时超深拒绝（`SubagentError("depth exceeded")`）。`depthLimit` capability 才支持。
   - **Provider 注册表**：多 Provider 按名共存（`list/getProvider/registerProvider`）；本地默认 `spawn`（全新子会话）。**`fork`（继承父会话已完成回合前缀，seed 语义）不在本段实现**（验收余量允许时再上，或 M5c 后评估）。
   - **后台续跑（continuable 最小形态）**：子代理以 job 形式后台运行（M5a 的 `jobs.Registry`）：`Start` 后不阻塞，拿 child session id 继续；子代理完成后经 `job/done` 通知 + 终态结果入 `subagent/end` 事件。父代理可 `send_message`（续跑消息，对**进程内活着的**子代理）、`interrupt`（取消当前回合）。
   - **不做（dsh 裁剪，理由见主 ADR 决策 ②）**：ACP/Codex/Claude Code/SDK 远程 Provider、`outputSchema` 结构化返回、scope 分层/fiber 生命周期、continuable 的冷恢复/激活状态机（子代理会话可持久化 resume，但无激活管理；`send`/`interrupt` 只针对进程内活着的子代理）。

2. **Consumer（工具，`internal/subagent/tools.go`）**：`subagent_spawn`（委托，返回 child id）、`subagent_send`（续跑消息）、`subagent_interrupt`、`subagent_list`——结构化实现 `tools.Tool` 接口（不 import tools 包，seam 解耦），组合根注册。D7 校验；D10 默认关。

3. **loop pre-step 扩展点升级（本段落）**：现有 `loop.Config.Recall`（M4b，kb 主动召回）升级为**统一 pre-step 注入机制**（主 ADR"总体决策"）：
   ```go
   type Config struct {
       // ...
       Recall func(ctx context.Context, userText string) []llm.Message // 保留（首个消费者，向后兼容）
       PreStep []PreStepInjector // 可注册多个注入器
   }
   type PreStepInjector struct {
       Name   string
       Inject func(ctx context.Context, userText string) []llm.Message
   }
   ```
   - `Run` 在 `user/message` 追加后、首个 step 请求前，依注册顺序收集各注入器返回的上下文消息注入首个请求（`step===1` 门保持不变）；工具调用后续 step 不重复注入。
   - **预算有界 + fail-open**：每个注入器返回的上下文有长度上限（config `pre_step.max_chars_per_injector`，默认 4000，超出截断）；单个注入器 panic/fail 不阻断回答（组合根兜底，fail-open）。kb 召回改为注册进 PreStep（替代直接 `Recall` 字段）——`recallContext` 逻辑复用，`loop_test` 现有断言（首个请求注入、后续不注入、nil-safe）必须保持绿色。
   - turn/step 结构零改动（D4）；`PreStep` 是注入器的容器，`Recall` 字段保留作为兼容别名（实现为 PreStep 的首项，二选一）。

4. **事件（D3，`internal/session` 新增，log-only）**：`EventSubagentStart = "subagent/start"`、`EventSubagentEnd = "subagent/end"`、`EventSubagentReport = "subagent/report"` + 载荷构造 `NewSubagentStart/NewSubagentEnd/NewSubagentReport`（child session id/provider/父会话/stop reason/输出摘要）。`DeriveHistory` 视为不透明数据。子代理委托工具结果经 `tool/result` 落日志（D3 满足）；子代理自身会话是独立 session，其日志本就完整。

5. **config（`internal/config` 扩展）**：
   ```yaml
   subagent:
     enabled: false
     max_depth: 8
     default_provider: spawn
   pre_step:
     max_chars_per_injector: 4000
   ```
   `subagent.enabled` 单一开关（D10）：false ⇒ 子代理工具不注册、不进白名单、组合根不初始化运行时。`pre_step` 属 loop 配置，独立开关 `loop.pre_step`（默认 true，false 时不收集任何注入器——保留纯串行行为）。

**决策记录（必交）**：M5 主 ADR `docs/decisions/2026-08-18-m5-agent-core.md` 决策 ② 已写好（本段）＋"总体决策"中的 PreStep 升级。实施中若有偏离，**更新该 ADR 对应小节**并说明。

**约束**（严格遵守 design.md 第 10 节 D1–D10）：

- 不改 loop 的 turn/step 结构（D4）；`PreStep` 只是注入器容器，`step()` 内消息组装逻辑不变（注入点位置不变）。
- **主循环保持串行**（D5）：子代理后台续跑走 `jobs`，不进主循环 turn/step 路径。
- **明确不做（本段）**：压缩（M5c）、技能（M5d）、远程 Provider、`outputSchema`、`fork` seed、continuable 冷恢复状态机、job 持久化。只实现运行时 + spawn Provider + 工具 + PreStep 升级 + 事件 + config。
- `subagent.enabled` 默认关闭（D10）。
- 保持 CGO-free；**不新增任何第三方依赖**；Go 沙箱绕行沿用项目内缓存。
- 原有测试必须保持绿色（尤其 `loop_test` 的 Recall 断言、`cmd/pa` 的 kb recall 测试）。

**参考源码**：`D:\dev-projects\Agent\deepseek-harness\packages\subagent\`（Provider 注册表、spawn-in-process、三个工具；**只借鉴思路与契约，不照搬 TS 代码**）。`docs/subsystems/subagent.md` 的 Provider 契约与深度/seed 语义。

**自测（全部通过后提交，提交信息含 M5b）**：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试至少覆盖：**子代理完整回合可回放且父会话日志不受污染**（独立 session）、子代理终结回传结果（completed/error）、深度上限拒绝、后台续跑走 job（start 后不阻塞、job/done 通知、终态入 `subagent/end` 事件）、`send_message`/`interrupt` 对活子代理生效、工具 schema 校验（D7）、`subagent/*` 事件类型可落日志、**PreStep 升级后 kb 召回行为不变**（原有 Recall 测试绿色 + 新 PreStep 注入器测试）、subagent 默认关闭（enabled=false 不初始化）、Close 无泄漏。

**完成报告**：改动文件清单、实现决策、测试结果、提交 hash、对 M5 主 ADR 的更新说明（如有）。提交后报告，不要等待控制会话确认——报告即交接。
