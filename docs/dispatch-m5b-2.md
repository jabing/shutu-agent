# M5b-2 实施派发消息（控制会话 → 实施会话）——subagent 事件、工具与组合根接线

> 状态：已派发 2026-08-19（M5 拆四段：M5a 后台任务 ✅ → M5b 子代理 → M5c 上下文压缩 → M5d 技能；本文件为 M5b 第二半）· 用法：把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\personal-agent` 的 **M5b-2：subagent 事件类型 + subagent_* 工具 + config + cmd/pa 组合根接线**。M5b-1 已验收（`internal/subagent` 运行时接口 + spawn Provider + loop PreStep 升级，提交 `55f1b63`+`34c302c`+`8a3f648`），本任务在它之上补齐 Consumer 面。

**必读（先读这些）**：
1. `D:\dev-projects\Agent\personal-agent\docs\dispatch-m5b.md` —— 背景契约，重点读「M5b 范围」第 2（工具）、4（事件）、5（config）条和「约束」「自测」段。**本任务做第 2、4、5 条 + 组合根接线**；第 1、3 条（运行时 + PreStep）已由 M5b-1 完成，不要重做。
2. `D:\dev-projects\Agent\personal-agent\docs\decisions\2026-08-18-m5-agent-core.md` 的「决策 ②」和「总体决策」（PreStep）+「实施说明（M5b-1）」（委托深度/血缘存 SpawnProvider 注册表，M5b-2 事件接线将其表面化）。
3. **现有代码（签名以代码为准，照抄模式）**：
   - `internal/subagent/service.go` + `spawn.go`（M5b-1 已验收：Runtime/Provider/StartRequest/Result/Run 接口、`ChildLog(id)` 取子会话日志、`ListChildren`）
   - `internal/session/session.go`（事件词汇表 + kb/job 事件构造模式，`EventJobStart`/`NewJobStart` 等是最近的参照）
   - `internal/jobs/tools.go`（job_* 工具如何结构化实现 tools.Tool + JobTools 共享束 + transition tracker + owner 回调 + D3 事件发射——subagent 工具照此模式）
   - `internal/jobs/exec.go` 等（命令执行体，非本任务重点）
   - `internal/config/config.go`（KBConfig/JobsConfig 模式 + applyDefaults 白名单追加）
   - `cmd/pa/kb.go` + `cmd/pa/jobs.go` + `cmd/pa/main.go`（组合根接线模式：registerKB/registerJobs、app 结构、main 调用点）
   - `internal/loop/loop.go`（PreStep 注入器容器，M5b-1 已升级——**本任务可能把 subagent 目录注入接进 PreStep，选做，非必须**）
4. `Agent.md` 第 10 节 D1–D10 纪律。

**实现内容**：

1. **事件类型（D3，`internal/session/session.go` 扩展）**：
   - 常量：`EventSubagentStart = "subagent/start"`、`EventSubagentEnd = "subagent/end"`、`EventSubagentReport = "subagent/report"`。
   - 载荷构造（log-only，`DeriveHistory` 视为不透明数据，参照 kb/job 事件构造模式；session 不依赖 subagent 包）：
     - `NewSubagentStart(childID, provider, parentSessionID, label string) any`
     - `NewSubagentEnd(childID, provider, stopReason, outputSummary string) any`（输出只记摘要，有界 200 rune）
     - `NewSubagentReport(childID, parentSessionID, content string) any`
   - 追加路径测试：`internal/session/session_test.go` 新增"subagent/* 事件可追加 + JSON 往返 + 重放 + 不派生消息"（参照 job 事件测试模式）。

2. **Consumer 工具（`internal/subagent/tools.go`，默认关 D10）**：
   - `subagent_spawn`（args: prompt 必填/label/owner 可选/max_depth 可选）→ 调 `Runtime.Start("spawn", ...)`，返回 child id；执行成功落 `subagent/start` 事件。子代理后台跑（`Run.Result` 不阻塞——工具只启动，观察用后续工具）。
   - `subagent_status`（args: id）→ child summary（running?）或结果（settled 后含 output + stop_reason）。已结算时落 `subagent/end` 事件（transition tracker 恰好一次）。
   - `subagent_cancel`（args: id）→ 调 `Run.Cancel(reason)`，返回 requested/already-finished。
   - `subagent_list`（args: parent_session 可选）→ `Runtime.ListChildren` 投影。
   - 结构化实现 `tools.Tool` 接口（**不 import tools 包**，seam 解耦——参照 `internal/jobs/tools.go` 的 JobTools 模式）。D7：schema 在 Execute 入口统一校验（`additionalProperties: false`）；D10：注册由组合根按 `subagent.enabled` 决定。
   - 工具需要持有 `subagent.Runtime` 与当前会话 id 回调（owner）与事件 sink（onEvent），照 `JobTools` 的 `NewSubagentTools(r Runtime, owner func() string, onEvent func(typ string, data any))` 模式。
   - **事件落日志的并发安全（必须遵循 M5a-2 决策，ADR 已记录）**：所有 `a.log.Append` 都在工具 `Execute` 内（主循环串行路径）完成，**绝不在子代理后台 goroutine 里直接追加 session.Log**。子代理结算（后台）如何落 `subagent/end`？——由观察类工具（`subagent_status`）在串行路径读取 `Run.Result` 后落事件；或组合根用轮询/回调在串行路径落。选更简洁且不碰后台 goroutine 追加的（建议：观察工具落事件 + transition tracker 恰好一次）。

3. **config（`internal/config/config.go` 扩展）**：
   ```yaml
   subagent:
     enabled: false
     max_depth: 8
     default_provider: spawn
   ```
   - `SubagentConfig{Enabled bool; MaxDepth int; DefaultProvider string}`；`DefaultProvider` 空时落 `"spawn"`；`MaxDepth <= 0` 时落 8。
   - `applyDefaults`：`subagent.enabled: true` 时把 `subagent_spawn/subagent_status/subagent_cancel/subagent_list` 追加进 `tools.enabled`（参照 jobs/kb 模式）。
   - config 测试：缺省关/缺省 max_depth/缺省 provider、enabled 追加白名单。

4. **组合根接线（`cmd/pa/`）**：
   - 新增 `cmd/pa/subagent.go`：`registerSubagent()`——`subagent.enabled` 时创建 `subagent.NewSpawnProvider(subagent.Deps{Log, LLM, Tools, Prompt, Model})` + `subagent.NewRuntime()` + 注册 spawn provider + 注册 4 个工具（注入 owner/onEvent）+ `defer Close()`；disabled 零操作（D10，参照 registerJobs）。
   - `cmd/pa/main.go`：调用 `app.registerSubagent()`（jobs 之后）；app 结构加 `subagents subagent.Runtime` 字段（nil 时 disabled）。
   - 事件 sink：onEvent 把 subagent 载荷追加到 `a.log`（参照 `cmd/pa/jobs.go` 的 onEvent）。
   - **（选做，非必须）** 子代理/技能目录注入 PreStep：若实现简单，可在 loop.PreStep 注册一个注入器把当前活子代理列表作为轻量上下文；不做也完全可接受（验收不强求）。若做，保持有界（`maxInjectorChars` 由 loop 兜底）。

**纪律**：不改 loop 的 turn/step 结构（D4；PreStep 已在 M5b-1 升级，本任务只可能往 `loop.New` 的 Config.PreStep 传注入器，不改 loop 源码）；主循环串行（D5；后台子代理 goroutine 绝不碰 session.Log）；零新依赖；CGO-free；原有测试全绿（M5b-1 的 subagent 测试、loop 测试、job 测试）；`subagent.enabled` 默认关（D10）。**本任务不做**：压缩（M5c）、技能（M5d）、fork  Provider、远程 Provider、outputSchema、continuable 冷恢复、job 持久化。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）；每次 Go 命令设 `$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\personal-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\personal-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\personal-agent\.gocache'`。用 pwsh 执行命令。git 提交用 `git -C D:\dev-projects\Agent\personal-agent -c user.name='Personal Agent' -c user.email='dev@personal-agent.local' commit -m "..."`。不要提交 `pa.exe`、`data/`、缓存目录。

**上下文管理**：不要通读参考源码，按需精读片段；**分阶段提交**（事件 → 工具 → config → 接线，每阶段 commit 一次，信息含 "M5b-2"）；报告只列文件名 + 一句话。

**自测**（全部通过再报告）：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试至少覆盖 dispatch-m5b.md「自测」段中属于本任务的：`subagent/*` 事件可落日志（追加+重放）、工具 schema 校验（D7）、subagent_spawn 返回 child id 且后台跑、subagent_status 反射结果 + 终态落 `subagent/end` 恰好一次、subagent_cancel 生效、subagent_list 投影、subagent.enabled=false 不注册、白名单追加、**组合根测试**（enabled 时注册 + 事件落日志；disabled 时零操作）。

**完成报告**：改动文件清单、实现决策（subagent/end 事件落日志方案选哪种）、测试结果、提交 hash 列表、对 M5 主 ADR 的更新说明（如有）。提交后报告，不要等待控制会话确认——报告即交接。
