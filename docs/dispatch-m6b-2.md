# M6b-2 实施派发消息（控制会话 → 实施会话）——plan 工具 + 事件 + config + 接线

> 状态：待 M6b-1 验收后派发 2026-08-19（M6 能力补全六段，ADR `2026-08-19-m6-agent-full.md`；本文件为 M6b 第二半：工具 + 事件 + config + 接线）· 用法：M6b-1 验收通过后，把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\personal-agent` 的 **M6b-2：`plan_*` 工具 + `plan/*` 事件 + config + 组合根接线 + 单元测试**。这是 M6b 的第二半（第一半 M6b-1 已做 `internal/plan` 接缝 + 域模型，你**依赖它们**）。你是实施会话。

**直接开工，不要做任何前置检查**。你的主要输入是 `D:\dev-projects\Agent\personal-agent\docs\dispatch-m6b-2.md`（本文件即主契约），**先完整读它**，然后立即用 write 工具创建/修改文件。

**读这些（按需精读片段，不要通读）**：
1. `D:\dev-projects\Agent\personal-agent\docs\dispatch-m6b-1.md` —— 接缝契约（Engine/Goal/Plan/Todo/Status/Provider 签名）。
2. `D:\dev-projects\Agent\personal-agent\docs\decisions\2026-08-19-m6-agent-full.md` —— M6 主 ADR（M6b 行）。
3. 现有代码（按需精读）：
   - `internal/plan/service.go` + `mem.go`（M6b-1 已做）。
   - `internal/session/session.go` —— job/subagent/compaction/skill/schedule 事件的 log-only 模式（模板）。
   - `internal/config/config.go` —— 各段模式 + applyDefaults 白名单。
   - `cmd/pa/*.go` —— registerJobs/registerSubagent/registerCompaction/registerSkills/registerSchedules 组合根模式、工具注册、onEvent sink、preStepInjectors()、命令注册。
   - `internal/tools/` —— tools.Tool + D7；`internal/jobs/tools.go`、`internal/schedule/tools.go`（工具层事件模式）。
4. 参考（只借鉴思路，不精读）：`D:\dev-projects\Agent\deepseek-harness\packages\goal\goal\src\client.ts`、`tool-goal\src\index.ts`、`todo\tool-todo\src\index.ts`。

**实现内容**：
1. **事件（`internal/session/session.go` + 测试）**：新增 `EventPlanCreate/Update/Delete/Status`（`plan/create|update|delete|status`）+ `NewPlanCreate(scope, id, title string) any`、`NewPlanUpdate(scope, id string) any`、`NewPlanDelete(scope, id string) any`、`NewPlanStatus(scope, id string, st string) any`。**log-only**：DeriveHistory 不派生。
2. **config（`internal/config` + config.yaml）**：`PlanConfig{Enabled bool}`（yaml: `enabled`）；默认 `enabled:false`。**enabled 时自动白名单 `plan_*` 工具**。
3. **plan_* 工具（`internal/plan/tools.go` + 测试）**：`NewPlanTools(e Engine, onEvent func(typ string, data any))` 返回结构化 tools.Tool 集合（不 import tools 包，D2）：
   - `plan_goal(title, objective)`：创建 goal；D7 schema（additionalProperties:false）；落 `plan/create`（scope=goal）。
   - `plan_plan(goal_id, title, steps []string)`：为 goal 建 plan；落 `plan/create`（scope=plan）。
   - `plan_todo(plan_id, title)`：加步骤；落 `plan/create`（scope=todo）。
   - `plan_status(scope, id, status)`：改状态；落 `plan/status`。
   - `plan_list()`：返回聚合树；落 `plan/list`。
   - `plan_remove(scope, id)`：删除（级联）；落 `plan/delete`。
   - 事件经 onEvent sink（串行工具路径）；未知 id/非法状态返回错误消息（非 panic）。
4. **组合根（cmd/pa `registerPlans()`）**：`plan.enabled` 时创建内存 Provider + Engine + 注册 plan_* 工具（白名单）+ 事件 sink；disabled 零操作（D10）。`main.go` 调用 + `app.plans` 字段 + deferred Close + /help 状态行。

**纪律**：**日志仍追加式（D1）**；不改 loop turn/step（D4）；串行工具路径（D5）；零新依赖；CGO-free；原有测试全绿。**不要动**：`internal/plan/service.go`/`mem.go`（M6b-1 已验收，只读；tools.go 新建）、loop.go（只读）、compaction、subagent、skill、kb、store、schedule 包（只读参考）。**不要做**：M6c–M6f、KB 补全、子代理执行接线（M6b 只提供规划模型，执行委托留到 M6c 之后可选）。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）。每次 Go 命令这样跑（用 pwsh）：
`$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\personal-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\personal-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\personal-agent\.gocache'; & 'C:\Program Files\Go\bin\go.exe' test ./...`
git 提交：`git -C D:\dev-projects\Agent\personal-agent -c user.name='Personal Agent' -c user.email='dev@personal-agent.local' commit -m "M6b-2: <what>"`。不要提交 pa.exe/data/缓存。

**上下文管理（关键）**：**分阶段提交**——① session 事件 → ② config → ③ tools → ④ 组合根，每阶段一次 commit（信息含 "M6b-2"）。不要通读任何参考库。报告只列文件名+一句话。

**自测**（全部通过再报告）：vet/test/build 三命令全绿。新增测试至少覆盖：事件追加/重放/不派生、config 缺省 + 白名单、plan_goal/plan_plan/plan_todo/plan_status/plan_list/plan_remove（D7 + 事件 + 未知 id 报错）、enabled=false 不注册。

**完成报告**：改动文件清单、实现决策、测试结果、提交 hash 列表、对 M6 主 ADR 的更新说明（如有）。提交后报告即交接，不要等待确认。