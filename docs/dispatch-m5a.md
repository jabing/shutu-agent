# M5a 实施派发消息（控制会话 → 实施会话）——后台任务 jobs

> 状态：已派发 2026-08-18（M5 拆四段：M5a 后台任务 → M5b 子代理 → M5c 上下文压缩 → M5d 技能；本文件为第一段）· 用法：把下文整段粘贴给新开的实施会话。

---

请阅读 `D:\dev-projects\Agent\shutu-agent\Agent.md`、`docs/design.md`（§10 已更新 D5/D4）**和 `docs/decisions/2026-08-18-m5-agent-core.md`（M5 主 ADR，本段对应"决策 ①"）**，并通读参考源码 `D:\dev-projects\Agent\deepseek-harness\packages\jobs\`（重点：`jobs/`（Service 定义 `src/types.ts`、`src/index.ts`）、`jobs-local/`（本地注册表）、`tool-jobs/`（模型侧工具））以及 `docs/subsystems/jobs.md`（job 生命周期、owner 隔离、快照契约），按设计基线实现 **M5a 后台任务**（M5 第一段；本段验收标准见下，M5 完整验收标准见 Agent.md 第 4 节）。

**M5a 范围（只做后台任务，不碰子代理/压缩/技能）**：

1. **`internal/jobs` 包——job 注册表（seam 的 Service 定义）**：
   ```go
   type Status string  // running | stopping | completed | killed | failed
   type Kind    string  // 不透明 id 命名空间（"bash"/"subagent"/"extract"…），注册表不解释

   type JobSnapshot struct {
       ID         string
       Kind       Kind
       Label      string
       OwnerSession string
       Status     Status
       Detail     string
       StartedAt  time.Time
       FinishedAt *time.Time
       OutputLimitBytes int
   }

   type JobStart struct {
       Kind       Kind
       Label      string
       OwnerSession string
       OutputLimitBytes int
       Run    func(ctx context.Context) (JobOutcome, error)  // 前台可取消执行体
       Cancel func(reason string) error                      // 同步、幂等
   }

   type JobOutcome struct {
       Status Status  // completed | killed | failed
       Detail string  // "exit code: 3"、"max-tokens" 等
       Output string  // 终态输出（final-output 型）；流式 job 可忽略
   }

   type Registry interface {
       Start(ctx context.Context, spec JobStart) (string, error)      // 返回注册表签发的 id
       List(ctx context.Context, callerSession string) ([]JobSnapshot, error)
       Get(ctx context.Context, id, callerSession string) (JobSnapshot, error)
       Read(ctx context.Context, id, callerSession string) (string, JobSnapshot, error)
       Kill(ctx context.Context, id, callerSession, reason string) (string, error)  // "requested"|"already-finished"
       Wait(ctx context.Context, id, callerSession string, timeout time.Duration) (JobSnapshot, error)
       Close() error
   }
   ```
   - **owner 隔离**：job 属于 `OwnerSession`；`Get/Read/Kill/Wait` 按 callerSession 匹配 owner，跨 owner 拒绝（授权基于 owner 匹配，id 可预测不保密）。无 owner（空串）的 job 任何人可观察。
   - **生命周期语义**（对照 `docs/subsystems/jobs.md`）：`Start` 先做 preflight（owner/并发上限）再启动 goroutine 跑 `Run`；`Read` 返回流式增量或终态幂等输出并标记 reported；`Kill` 同步幂等请求取消，返回 `requested`/`already-finished`；`Wait` 有界等待，超时返回当前快照；终端结算（completed/killed/failed）释放并发配额。
   - **并发上限**：本地实现 `maxConcurrentJobsPerOwner`（默认 10），计数 running+stopping，按 exact owner 一个桶，无 owner 共享一个桶。
   - **生命周期可逆**：`Close()` 取消并等待所有活 job；注册表持有 goroutine 引用，close 后无泄漏。
   - 内存实现即可，**不持久化**（跨重启不续跑，个人单进程接受）。

2. **本地 Provider（默认，`internal/jobs` 内实现）**：内存注册表 + goroutine 池；取消走 context；输出有界（`OutputLimitBytes`，超出截断 + spill 落 `data/spill/`，沿用 M3 的 spill 机制，参考 `internal/tools` 的 spill 实现）。消费方只依赖 `Registry` 接口（D2）。

3. **Consumer（工具，`internal/jobs/tools.go`）**：`job_start`、`job_status`、`job_cancel`、`job_wait`、`job_read`——结构化实现 `tools.Tool` 接口（Go 结构类型，不 import tools 包，seam 保持解耦），由组合根注册进 `tools.Registry`。D7 校验（schema 在 Execute 入口统一校验）；D10 默认关。

4. **事件（D3，`internal/session` 新增，log-only）**：`EventJobStart = "job/start"`、`EventJobStatus = "job/status"`、`EventJobDone = "job/done"` + 载荷构造 `NewJobStart/NewJobStatus/NewJobDone`（id/kind/label/ownerSession/status/detail/output 摘要）。`DeriveHistory` 视为不透明数据。模型实际看到的 job 状态/输出经工具 `tool/result` 落入日志（D3 满足）。

5. **config（`internal/config` 扩展）**：
   ```yaml
   jobs:
     enabled: false
     max_concurrent_jobs_per_owner: 10
   ```
   `jobs.enabled` 单一开关（D10）：false ⇒ job 工具不注册、不进白名单、组合根不初始化注册表（参照 kb 的 `NewFromConfig` 模式：disabled 返回 nil 不打开资源）。

**决策记录（必交）**：M5 主 ADR `docs/decisions/2026-08-18-m5-agent-core.md` 已由控制会话写好（决策 ① 即本段）。本段实施中若有偏离（如接口签名、并发模型调整），**更新该 ADR 对应小节**并说明，不要另开新 ADR。

**约束**（严格遵守 design.md 第 10 节 D1–D10）：

- 不改 loop 的 turn/step 结构（D4）；jobs 是能力接缝，Service/Provider/Consumer 三件套齐全（D2）。
- **主循环保持串行**（D5 重评的"受控并发"）：job 后台 goroutine 独立运行，**不进入现有 turn/step 路径**；组合根在 REPL 循环外维护 job 状态并落事件。
- **明确不做（本段）**：job 持久化/跨重启续跑、分布式/远程注册表、优先级队列、自动重试、子代理（M5b）、压缩（M5c）、技能（M5d）。只实现注册表 + 本地 Provider + 工具 + 事件 + config。
- `jobs.enabled` 默认关闭（D10）。
- 保持 CGO-free；**不新增任何第三方依赖**；Go 沙箱绕行沿用项目内缓存（`.gomodcache` / `.gocache` / `.gopath`）。
- 原有测试必须保持绿色。

**参考源码**：`D:\dev-projects\Agent\deepseek-harness\packages\jobs\`（`jobs/` Service 定义、`jobs-local/` 本地实现、`tool-jobs/` 工具；**只借鉴思路与契约，不照搬 TS 代码**）。架构原则参考 `D:\dev-projects\Agent\deepseek-harness\docs\architecture.md`、`docs\capability-seams.md`。

**自测（全部通过后提交，提交信息含 M5a）**：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试至少覆盖：start 后可 list/get、状态迁移（running→completed/killed/failed）、kill 幂等且终态正确、wait 超时返回当前快照、**owner 隔离（跨会话 get/read/kill/wait 拒绝）**、**并发上限（超过 maxConcurrentJobsPerOwner 拒绝新 job、终态释放配额）**、输出截断 + spill、`job/*` 事件类型可落日志（类型声明 + 追加路径测试）、jobs 默认关闭（enabled=false 不初始化）、Close 无泄漏（活 job 被取消）。

**完成报告**：改动文件清单、实现决策、测试结果、提交 hash、对 M5 主 ADR 的更新说明（如有）。

**上下文管理（关键，务必遵守）**：
- 本派发文档已自包含实现所需的一切契约与验收标准。**不要通读** `deepseek-harness/packages/jobs/` 全部源码——只在某个语义不确定时精读对应文件（如 `jobs/src/types.ts` 的 JobSnapshot 字段、`jobs-local/src/index.ts` 的并发上限逻辑）。读文件用 read 的 offset/limit 只读需要的片段，不要整文件大段贴入。
- **分阶段提交**：每完成一个模块（接口 → Provider → 事件 → 工具 → config → 测试）就 `git add` + commit 一次（提交信息含 M5a + 模块名），不要攒到最后一次性提交。这样即使中途上下文耗尽，已完成的模块也已入库。
- 不要粘贴大段源码到报告里；报告只列文件名 + 一句话说明。
- **环境注意**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）；每次 Go 命令设置 `$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\shutu-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\shutu-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\shutu-agent\.gocache'`。用 pwsh 执行；git 提交用 `-c user.name='Personal Agent' -c user.email='dev@shutu-agent.local'`。不要提交 `pa.exe`、`data/`、缓存目录。
