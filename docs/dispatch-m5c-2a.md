# M5c-2a 实施派发消息（控制会话 → 实施会话）——compaction 事件 + config

> 状态：已派发 2026-08-19（M5c-2 因任务过大拆为 2a/2b 顺序派发；本文件为 2a：事件 + config）· 用法：把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\shutu-agent` 的 **M5c-2a：session `compaction/*` 事件 + config `CompactionConfig` + 单元测试**。这是 M5c-2 的一个**小**子任务（M5c-2b 另一个会话做 /compact 命令 + PreStep 接线，依赖你的事件与 config）。你是实施会话。

**必读（先读这些，不要通读参考源码）**：
1. `D:\dev-projects\Agent\shutu-agent\docs\dispatch-m5c-2.md` —— 背景契约，重点读「实现内容」第 1（事件）、2（config）条。**第 3、4、5 条是 M5c-2b 的事，只读参考。**
2. `D:\dev-projects\Agent\shutu-agent\docs\decisions\2026-08-18-m5-agent-core.md` 的「决策 ③」（事件三连锁语义）。
3. 现有代码（按需精读片段）：
   - `internal/session/session.go` —— **job/subagent 事件的 log-only 模式是你要复制的模板**（`EventJobStart/Status/Done`、`EventSubagentStart/End/Report` + `New*` 构造 + 200-rune 有界 head + DeriveHistory 不派生）。
   - `internal/config/config.go` —— **JobsConfig/SubagentConfig 段模式是模板**（Enabled + applyDefaults 校验 + 白名单追加）。
4. `Agent.md` 第 10 节 D1–D10 纪律。

**实现内容**（严格按 dispatch-m5c-2.md）：
1. **事件（`internal/session/session.go` + 测试）**：
   - 新增常量 `EventCompactionStart/EventCompactionSummary/EventCompactionEnd/EventCompactionPrune`（值 `compaction/start|summary|end|prune`）。
   - 新增构造 `NewCompactionStart(reason string, trigger string) any`、`NewCompactionSummary(compactionID, summary string) any`（**输出摘要有界 200 rune**）、`NewCompactionEnd(compactionID string, shadowedRange [2]int64, shadowedTokens int) any`、`NewCompactionPrune(compactionID string, replaced int, savedBytes int) any`。
   - **log-only**：DeriveHistory 不派生 compaction/* 事件（与 job/subagent 一致）。
   - 测试：追加 + JSON 往返 + Restore 重放 + 不派生（`DeriveHistory` 长度不含 compaction/* 事件）。
2. **config（`internal/config/config.go` + config_test.go + config.yaml）**：
   - `CompactionConfig{Enabled bool, TokenThreshold int, RetainTurns int, MaxChars int}`（yaml: `enabled/token_threshold/retain_turns/max_chars`）。
   - applyDefaults：`enabled` 缺省 false；`token_threshold<=0` → 32000；`retain_turns<=0` → 8；`max_chars<=0` → 0（=引擎默认，由接线传 BasicEngine 默认或 0 由引擎兜底）。校验非负。
   - **enabled 时不自动白名单任何工具**（compaction 无工具——自动触发走 PreStep，手动走 /compact 命令）。
   - config.yaml 加 compaction 段文档（默认 enabled:false）。
   - 测试：缺省值、applyDefaults、enabled 不白名单工具。

**纪律**：零新依赖；CGO-free；原有测试全绿。**不要动**：`internal/compaction`（已验收，只读）、loop、cmd/pa、jobs、subagent、kb、store、tools 包（只读参考）。**不要做**：/compact 命令、PreStep 接线、事件落日志接线（M5c-2b）。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）；每次 Go 命令设 `$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\shutu-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\shutu-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\shutu-agent\.gocache'`。用 pwsh 执行命令。git 提交用 `git -C D:\dev-projects\Agent\shutu-agent -c user.name='Personal Agent' -c user.email='dev@shutu-agent.local' commit -m "..."`。不要提交 `pa.exe`、`data/`、缓存目录。

**上下文管理（关键）**：这是**小任务**——只复制 session 的 job/subagent 事件模式与 config 的 Jobs/Subagent 段模式，不要通读整个文件或参考库。完成即一次性提交（信息含 "M5c-2a"）。

**自测**（全部通过再报告）：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试覆盖：事件追加/重放/不派生、200-rune 摘要有界、config 缺省与 applyDefaults、enabled 不白名单工具。

**完成报告**：改动文件清单、实现决策、测试结果、提交 hash。提交后报告，不要等待确认——报告即交接。
