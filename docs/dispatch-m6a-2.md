# M6a-2 实施派发消息（控制会话 → 实施会话）——schedule 工具 + 事件 + config + 接线

> 状态：待 M6a-1 验收后派发 2026-08-19（M6 能力补全六段，ADR `2026-08-19-m6-agent-full.md`；本文件为 M6a 第二半：工具 + 事件 + config + 接线）· 用法：M6a-1 验收通过后，把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\personal-agent` 的 **M6a-2：`schedule_*` 工具 + `schedule/*` 事件 + config + 组合根接线（含 D5 触发路径）+ 单元测试**。这是 M6a 的第二半（第一半 M6a-1 已做 `internal/schedule` 接缝 + 内核，你**依赖它们**）。你是实施会话。

**必读（先读这些，不要通读参考源码）**：
1. `D:\dev-projects\Agent\personal-agent\docs\dispatch-m6a-1.md` —— 接缝契约（Engine/Schedule/Provider 签名、Tick 语义），你的工具/接线调用它。
2. `D:\dev-projects\Agent\personal-agent\docs\decisions\2026-08-19-m6-agent-full.md` —— M6 主 ADR，重点读 M6a 行与 D5 触发语义。
3. 现有代码（按需精读片段）：
   - `internal/schedule/service.go` + `engine.go`（M6a-1 已做：Engine/Add/Remove/List/Tick）。
   - `internal/session/session.go` —— job/subagent/compaction/skill 事件的 log-only 模式（模板：`Event*` 常量 + `New*` 构造 + 200-rune 有界 + DeriveHistory 不派生）。
   - `internal/loop/loop.go` —— `Config.PreStep []PreStepInjector{Name, Inject}`（若触发路径走 pre-step 则用它）。
   - `internal/config/config.go` —— Jobs/Subagent/Compaction/Skill 段模式 + applyDefaults 白名单。
   - `cmd/pa/*.go` —— registerJobs/registerSubagent/registerCompaction/registerSkills 组合根模式、工具注册、onEvent sink、preStepInjectors()、命令注册。
   - `internal/tools/` —— tools.Tool 结构化接口 + D7。
4. 参考源码（**只借鉴思路，不精读**）：`D:\dev-projects\Agent\deepseek-harness\packages\schedule\` 的 `src/tools.ts`、`runtime.ts`。

**实现内容**：
1. **事件（`internal/session/session.go` + 测试）**：新增 `EventScheduleCreate/List/Delete/Fire`（`schedule/create|list|delete|fire`）+ `NewScheduleCreate(id, kind, spec string) any`、`NewScheduleList(count int) any`、`NewScheduleDelete(id string) any`、`NewScheduleFire(id string, payload string) any`（payload 摘要 200-rune 有界）。**log-only**：DeriveHistory 不派生（与 M5 事件一致）。
2. **config（`internal/config` + config.yaml）**：`ScheduleConfig{Enabled bool, TickInterval time.Duration}`（yaml: `enabled/tick_interval`）；默认 `enabled:false / tick_interval:1m`（≤0 → 1m）。**enabled 时自动白名单 `schedule_*` 工具**（与 job_*/subagent_* 同模式）。
3. **schedule_* 工具（`internal/schedule/tools.go` + 测试）**：`NewScheduleTools(e Engine, onEvent func(typ string, data any))` 返回结构化 tools.Tool 集合（不 import tools 包，D2）：
   - `schedule_create(kind, spec, payload)`：D7 schema（additionalProperties:false）；kind ∈ {interval, cron} 校验；落 `schedule/create`。
   - `schedule_list()`：返回当前调度清单；落 `schedule/list`。
   - `schedule_delete(id)`：未知 id 报错；落 `schedule/delete`。
   - 事件经 onEvent sink（串行工具路径）。
4. **D5 触发路径（cmd/pa 接线）**：**不引入后台 ticker**——调度推进走**串行路径**：每 turn 的 PreStep 注入器（Name "schedule"，在 skill 之后）调 `Engine.Tick(now)`，把返回到期的调度：落 `schedule/fire` 事件 + **入队 job**（复用 `internal/jobs`——`job_start` 一个执行 payload 的后台任务，owner=当前会话）；无 job 引擎时仅落事件。所有副作用都在串行 PreStep 路径（D5）。
5. **组合根（cmd/pa `registerSchedules()`）**：`schedule.enabled` 时创建内存 Provider + Engine + 注册 schedule_* 工具（白名单）+ PreStep 注入器 + 事件 sink；disabled 零操作（D10）。`main.go` 调用 + `app.schedules` 字段 + deferred Close。

**纪律**：**日志仍追加式（D1）**；不改 loop turn/step（D4）——触发走 PreStep + job 入队，无后台 goroutine（D5）；零新依赖；CGO-free；原有测试全绿。**不要动**：`internal/schedule/service.go`/`engine.go`（M6a-1 已验收，只读）、loop.go（只读）、compaction、subagent、skill、kb、store 包（只读参考；jobs 可调用）。**不要做**：M6b–M6f、KB 补全。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）；每次 Go 命令设 `$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\personal-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\personal-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\personal-agent\.gocache'`。用 pwsh 执行命令。git 提交用 `git -C D:\dev-projects\Agent\personal-agent -c user.name='Personal Agent' -c user.email='dev@personal-agent.local' commit -m "..."`。不要提交 `pa.exe`、`data/`、缓存目录。

**上下文管理（关键）**：**分阶段提交**（session 事件一次 → config 一次 → tools 一次 → PreStep + 组合根一次，信息含 "M6a-2"）；只按需精读片段，不要通读参考库；报告只列文件名 + 一句话。

**自测**（全部通过再报告）：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试至少覆盖：事件追加/重放/不派生、config 缺省 + 白名单、schedule_create/list/delete（D7 + 事件）、PreStep 触发（Tick 返回到期 → fire 事件 + job 入队，D5 无后台 goroutine）、enabled=false 不注册。

**完成报告**：改动文件清单、实现决策（触发路径如何保 D5 串行、job 复用）、测试结果、提交 hash 列表、对 M6 主 ADR 的更新说明（如有）。提交后报告，不要等待确认——报告即交接。
