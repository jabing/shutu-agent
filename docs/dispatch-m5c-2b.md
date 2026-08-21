# M5c-2b 实施派发消息（控制会话 → 实施会话）——/compact 命令 + PreStep 接线

> 状态：待 M5c-2a 验收后派发 2026-08-19（M5c-2 因任务过大拆为 2a/2b 顺序派发；本文件为 2b：/compact 命令 + PreStep 自动压缩 + 事件落日志）· 用法：M5c-2a 验收通过后，把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\shutu-agent` 的 **M5c-2b：/compact 命令 + PreStep 自动压缩接线 + compaction/* 事件落日志 + 单元测试**。这是 M5c-2 的子任务（M5c-2a 另一个会话已做 session `compaction/*` 事件 + config `CompactionConfig`，你**依赖它们**）。你是实施会话。

**必读（先读这些，不要通读参考源码）**：
1. `D:\dev-projects\Agent\shutu-agent\docs\dispatch-m5c-2.md` —— 你的主契约，重点读「实现内容」第 3（/compact）、4（PreStep）、5（事件落日志）条。**第 1、2 条（事件 + config）已由 M5c-2a 完成，跳过。**
2. `D:\dev-projects\Agent\shutu-agent\docs\decisions\2026-08-18-m5-agent-core.md` 的「决策 ③」。
3. 现有代码（按需精读片段）：
   - `internal/compaction/service.go`（Engine/Trigger/Result/SessionLike）、`basic.go`（BasicEngine/NewBasic）。
   - `internal/session/session.go` —— **`EventCompactionStart/Summary/End/Prune` + `New*` 构造（M5c-2a 已加）**、`NewUserMessageReplace`、`Append`、`DeriveHistory`。
   - `internal/loop/loop.go` —— `Config.PreStep []PreStepInjector{Name, Inject func(ctx, userText string) []llm.Message}`（逐 turn 调用、per-injector 4000 rune 上限、panic fail-open）。**接线方不修改 loop.go**。
   - `internal/config/config.go` —— `CompactionConfig{Enabled, TokenThreshold, RetainTurns, MaxChars}`（M5c-2a 已加）。
   - `cmd/pa/*.go` —— registerKB/registerJobs/registerSubagent 组合根模式、现有命令（/kb、/job、/subagent 等）注册方式、onEvent sink 模式。
   - `internal/tools/`、`internal/jobs/tools.go`（tool-layer 事件落日志模式，参考）。
4. `Agent.md` 第 10 节 D1–D10 纪律。

**实现内容**（严格按 dispatch-m5c-2.md）：
1. **/compact 命令（cmd/pa）**：`/compact` 手动 `CompactNow`（或 `/compact region <start> <end>` → CompactRegion）；打印摘要 + 遮蔽范围 + 省下 token；enabled=false 提示不可用（D10）。参照现有命令注册方式。命令处理在串行路径。
2. **PreStep 自动压缩（cmd/pa 接线）**：`compaction.enabled` 时向 loop `Config.PreStep` 追加注入器（Name "compaction"）：每 turn 前估算 surface token（复用 BasicEngine 估算），超 `token_threshold` 则 `CompactIfNeeded(ctx, sess, TriggerPressure)`；注入内容为简短「已压缩」说明（不是摘要全文——折叠后历史已含摘要 user/message）。`*session.Log` 已满足 `compaction.SessionLike`。**所有 Append 都在 PreStep/命令的串行路径发生，无后台 goroutine（D5）**。
3. **事件落日志（cmd/pa 接线）**：compaction/* 观测事件在串行路径（PreStep 注入器内或命令处理内）经 `a.log.Append` 落：start（触发原因）→ summary（摘要）→ end（遮蔽范围/省下 token）；prune（如接线调用 PruneToolResults 则落，否则可留空——M5c 最小集不含主动剪枝，**不强制接线 pruner**）。模式与 job/subagent 的 onEvent sink 一致。

**纪律**：**日志仍追加式（D1）**——压缩只追加摘要 user/message + compaction/* 观测事件，绝不物理删除旧事件；**不改 loop 的 turn/step 结构（D4）**——只通过已存在的 PreStep 扩展点接线，不动 loop.go；主循环串行（D5）；零新依赖；CGO-free；原有测试全绿。**不要动**：`internal/compaction`、`internal/loop/loop.go`（只读）、jobs、subagent、kb、store、tools 包（只读参考）。**不要做**：M5d 技能、KB 补全、pruner 接线（可选，非强制）。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）；每次 Go 命令设 `$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\shutu-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\shutu-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\shutu-agent\.gocache'`。用 pwsh 执行命令。git 提交用 `git -C D:\dev-projects\Agent\shutu-agent -c user.name='Personal Agent' -c user.email='dev@shutu-agent.local' commit -m "..."`。不要提交 `pa.exe`、`data/`、缓存目录。

**上下文管理（关键）**：**分阶段提交**（/compact 命令一次 → PreStep + 事件接线一次，信息含 "M5c-2b"）；只按需精读片段，不要通读参考库；报告只列文件名 + 一句话。

**自测**（全部通过再报告）：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试至少覆盖：/compact 命令（enabled/disabled）、PreStep 注入器（压力触发调用 CompactIfNeeded、enabled=false 不注册）、事件落日志恰好一次。

**完成报告**：改动文件清单、实现决策（PreStep 注入器如何接、事件如何落）、测试结果、提交 hash 列表、对 M5 主 ADR 的更新说明（如有）。提交后报告，不要等待确认——报告即交接。
