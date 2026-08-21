# M5a-2 实施派发消息（控制会话 → 实施会话）——jobs 事件、工具与组合根接线

> 状态：已派发 2026-08-19（M5a 拆两半：M5a-1 内核已验收 `34bf1e8`+`5f3abd4`；本文件为 M5a-2 接线）· 用法：把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\shutu-agent` 的 **M5a-2：jobs 事件类型 + job_* 工具 + config + cmd/pa 组合根接线**。M5a-1 已验收（`internal/jobs` 的 Registry 接口 + Local Provider + 21 个测试全绿，提交 `34bf1e8`+`5f3abd4`），本任务在它之上补齐 Consumer 面。

**必读（先读这些）**：
1. `D:\dev-projects\Agent\shutu-agent\docs\dispatch-m5a.md` 的「M5a 范围」第 3、4、5 条和「自测」段 —— 本任务的契约。
2. `D:\dev-projects\Agent\shutu-agent\docs\decisions\2026-08-18-m5-agent-core.md` 的「决策 ①」（背景与取舍）。
3. **`D:\dev-projects\Agent\shutu-agent\internal\jobs\service.go`（已验收的接口，签名以代码为准）** 和 `internal/jobs/local.go`（Provider 行为）。
4. 现有代码形态（照抄模式）：
   - `internal/session/session.go`（事件词汇表：EventKBRecall/NewKBRecall 等 kb 事件的声明与构造模式）
   - `internal/kb/tools.go`（kb_* 工具如何结构化实现 `tools.Tool`：Name/Description/Schema/Execute，不 import tools 包）
   - `internal/config/config.go`（KBConfig 模式：Enabled 门 + 指针字段 + applyDefaults 白名单追加，第 207–240 行）
   - `cmd/pa/kb.go`（组合根如何注册工具 + 回调落事件）+ `cmd/pa/main.go`（app 结构、registerKB 调用点、repl 循环）
5. `D:\dev-projects\Agent\shutu-agent\Agent.md` 第 10 节 D1–D10 纪律。

**实现内容**：

1. **事件类型（D3，`internal/session/session.go` 扩展）**：
   - 常量：`EventJobStart = "job/start"`、`EventJobStatus = "job/status"`、`EventJobDone = "job/done"`。
   - 载荷构造（log-only，`DeriveHistory` 视为不透明数据——参照 kb 事件构造模式，载荷是纯数据投影，session 不依赖 jobs 包）：
     - `NewJobStart(id string, kind, label, ownerSession string) any`
     - `NewJobStatus(id string, status, detail string) any`
     - `NewJobDone(id string, status, detail string, outputSummary string) any`（output 只记摘要，有界）
   - 追加路径测试：`internal/session/session_test.go` 新增"job/* 事件可追加 + JSON 往返 + 重放"测试（参照 `TestKBRecallEventAppendsAndReplays` 模式）。

2. **Consumer 工具（`internal/jobs/tools.go`，默认关 D10）**：
   - `job_start`（args: kind/label/owner_session 可选/command 字符串；注册一个执行 `command` 的后台工作。命令执行用现有 `internal/tools` 的 run_command 机制或简单 os/exec 均可——**建议**：本段用"运行外部命令"作为默认 Run 实现（os/exec + context 取消），`owner_session` 缺省用当前会话 id。返回 job id）。
   - `job_status`（args: id）→ 快照文本。
   - `job_cancel`（args: id）→ requested/already-finished。
   - `job_wait`（args: id, timeout_seconds 默认 30）→ 终态快照或超时提示。
   - `job_read`（args: id）→ 输出文本 + 状态。
   - 结构化实现 `tools.Tool` 接口（Go 结构类型，**不 import tools 包**，seam 解耦——参照 `internal/kb/tools.go`）。D7：schema 在 Execute 入口统一校验（参照 kb 工具 schema 写法）；D10：注册由组合根按 `jobs.enabled` 决定。
   - 工具需要拿到当前会话 id（owner 绑定）：构造时注入 `OwnerSession func() string` 或由组合根传入（参照 `internal/kb` 的 onAdded 回调模式）。

3. **config（`internal/config/config.go` 扩展）**：
   ```yaml
   jobs:
     enabled: false
     max_concurrent_jobs_per_owner: 10
   ```
   - `JobsConfig{Enabled bool; MaxConcurrentJobsPerOwner int}`。
   - `applyDefaults`：`jobs.enabled: true` 时把 `job_start/job_status/job_cancel/job_wait/job_read` 追加进 `tools.enabled`（参照 kb 第 225–229 行模式）；`MaxConcurrentJobsPerOwner <= 0` 时落默认 10。
   - config 测试：`internal/config/config_test.go` 新增"jobs.enabled 追加白名单"、"缺省 max 落默认 10"。

4. **组合根接线（`cmd/pa/`）**：
   - 新增 `cmd/pa/jobs.go`：`registerJobs()`——`jobs.enabled` 时创建 `jobs.NewLocal(jobs.LocalOpts{MaxConcurrentJobsPerOwner: cfg.Jobs.MaxConcurrentJobsPerOwner})`、注册 5 个工具（注入当前会话 id 回调）、`defer Close()`（在 main.go 调用点）。`jobs.enabled=false` 时什么都不做（D10，参照 registerKB）。
   - `cmd/pa/main.go`：调用 `app.registerJobs()`（kb 之后）；app 结构加 `jobs *jobs.Local` 字段。
   - **事件落日志（D3）**：job 状态迁移由组合根监听落事件——`job/start`（注册成功时）、`job/status`（running→stopping 等迁移）、`job/done`（终态）。实现方式：Local 注册表无回调接口，组合根可在工具层落事件（job_start 工具执行成功落 `job/start`；job_status/job_cancel/job_wait/job_read 工具在观察到状态变化时落 `job/status`；终态落 `job/done`），或给 Local 增加一个可选的 `OnTransition func(snapshot JobSnapshot)` 回调字段（组合根设置后自动落事件）。**任选其一，选更简洁的**；若改 Local，更新 `internal/jobs/service.go` 注释说明（不改变接口签名）。
   - 工具结果走 `tool/result` 已满足 D3（模型实际看到）。

**纪律**：不改 loop 的 turn/step 结构（D4）；主循环保持串行（D5，job 后台 goroutine 不进 turn/step）；不新增任何第三方依赖；CGO-free；原有测试（尤其 M5a-1 的 21 个）保持绿色；`jobs.enabled` 默认关闭（D10）。**本任务不做**：子代理/压缩/技能、job 持久化、spill 落盘（M5a-1 已截断，spill 后续评估）。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）；每次 Go 命令设 `$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\shutu-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\shutu-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\shutu-agent\.gocache'`。用 pwsh 执行命令。git 提交用 `git -C D:\dev-projects\Agent\shutu-agent -c user.name='Personal Agent' -c user.email='dev@shutu-agent.local' commit -m "..."`。不要提交 `pa.exe`、`data/`、缓存目录。

**上下文管理**：不要通读参考源码，按需精读片段；分阶段提交（事件 → 工具 → config → 接线，每阶段 commit 一次，信息含 "M5a-2"）；报告只列文件名 + 一句话。

**自测**（全部通过再报告）：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试至少覆盖：`job/*` 事件可落日志（追加 + 重放）、工具 schema 校验（D7：坏参数拒绝）、job_start 真实执行一个命令并返回 id、job_status/job_read 反射快照、job_cancel 请求取消、job_wait 超时、**jobs.enabled=false 时工具不注册**（组合根测试或 config 测试）、白名单追加生效。

**完成报告**：改动文件清单、实现决策（事件落日志方案选哪种）、测试结果、提交 hash 列表。提交后报告，不要等待控制会话确认——报告即交接。
