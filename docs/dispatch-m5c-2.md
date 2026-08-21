# M5c-2 实施派发消息（控制会话 → 实施会话）——compaction 接线（/compact + 事件 + config + PreStep）

> 状态：已派发 2026-08-19（M5 拆四段：M5a ✅ → M5b ✅ → M5c 上下文压缩 → M5d 技能；M5c 第一半 1a ✅ / 1b ✅ 已验收；本文件为 M5c 第二半接线）· 用法：把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\shutu-agent` 的 **M5c-2：compaction 接线**——`/compact` 命令 + `compaction/*` 事件 + config 配置 + PreStep 自动压缩。这是 M5c 的第二半（第一半 1a/1b 已完成：session 折叠规则 + `internal/compaction` 接缝 + BasicEngine + 剪枝）。你是实施会话。

**必读（先读这些，不要通读参考源码）**：
1. `D:\dev-projects\Agent\shutu-agent\docs\dispatch-m5c.md` —— 你的主契约，重点读「M5c 范围」第 2（PreStep 接线）、3（事件三连锁）、4（config）条与「M5c 细节」。
2. `D:\dev-projects\Agent\shutu-agent\docs\decisions\2026-08-18-m5-agent-core.md` 的「决策 ③」。
3. 现有代码（按需精读片段）：
   - `internal/compaction/service.go`（Engine 接口 + Trigger + Result + SessionLike）、`basic.go`（BasicEngine/NewBasic）、`pruner.go`（PruneToolResults）。
   - `internal/session/session.go` —— `EventUserMessage`、`NewUserMessageReplace`、`NewUserMessage`、`Append`、`DeriveHistory`、`Events()`。
   - `internal/loop/loop.go` —— **`Config.PreStep []PreStepInjector`（M5b-1 已加）** 与 `Config.Recall`（已保留为兼容首个注入器 "recall"）。看 PreStep 注入器如何注册/调用。
   - `internal/config/config.go` —— 现有各 Config 段（Jobs/Subagent）模式：Enabled + applyDefaults 自动白名单。
   - `cmd/pa/*.go` —— registerKB / registerJobs / registerSubagent 组合根模式；commands（/kb、/job 等）如何注册。
   - `internal/prompt/`、`internal/tools/`、`internal/jobs/tools.go`（tool-layer 事件落日志模式）。
4. `Agent.md` 第 10 节 D1–D10 纪律 + M5c 事件词汇表（design.md §3）。

**实现内容**（严格按 dispatch-m5c.md）：

1. **事件类型（`internal/session`）**：新增 `EventCompactionStart/Summary/End/Prune` 常量 + `NewCompactionStart/NewCompactionSummary/NewCompactionEnd/NewCompactionPrune` 载荷构造（log-only——**DeriveHistory 不派生这些事件**，与 job/subagent 事件一致；summary 输出有界 200 rune）。
   - 事件语义（ADR 决策 ③）：compaction/start（开始压缩，带原因/触发）→ compaction/summary（摘要落日志）→ compaction/end（压缩完成，带遮蔽范围/省下 token）；compaction/prune（剪枝完成）。**注意**：压缩摘要本体是 `user/message`（surfaceOp.replace，M5c-1a），compaction/* 是它的**观测事件**（log-only 记录），两者都在串行路径落。
2. **config（`internal/config` + config.yaml）**：`CompactionConfig{Enabled bool, TokenThreshold int, RetainTurns int, MaxChars int}`；默认 `enabled:false / token_threshold:32000 / retain_turns:8`（MaxChars 默认与 BasicEngine 默认一致或 0=由引擎默认）。**enabled 时不自动白名单任何工具**（compaction 无工具——自动触发走 PreStep，手动走 /compact 命令）。applyDefaults 校验非负。
3. **/compact 命令（cmd/pa）**：`/compact` 手动触发 `CompactNow`（或带参数 `/compact region <start> <end>` 触发 `CompactRegion`）；结果打印摘要 + 遮蔽范围 + 省下 token；enabled=false 时提示不可用（D10）。参照现有 /job、/kb 命令注册方式。**命令本身不落日志**（压缩的观测事件由引擎/接线落）。
4. **PreStep 自动压缩（cmd/pa 接线）**：当 `compaction.enabled` 时，向 loop `Config.PreStep` 追加一个注入器（注入器名 "compaction"，在 recall 之后）：
   - 每次 step 前估算当前 surface token（复用 BasicEngine 估算或 `estimateTokens`）；超 `token_threshold` 则 `CompactIfNeeded(ctx, sess, TriggerPressure)`；在摘要注入后（replace 标记），模型看到的下一步历史已是折叠后的（derive 规则，M5c-1a）。
   - **注入器预算**：遵循 PreStep 注入器约定（per-injector 4000 rune 上限，超出截断）；注入内容为「已压缩的提示」（简短说明），不是把摘要全文注入——折叠后的历史已含摘要 user/message。
   - 接线点在组合根（cmd/pa），把 `*session.Log` 适配为 `compaction.SessionLike`（Log 已满足接口）。
   - **串行路径保证（D5）**：PreStep 在串行主循环内调用，所有 Append 都在这里发生，无后台 goroutine。
5. **事件落日志（cmd/pa 接线）**：compaction/* 观测事件在串行路径（/compact 命令处理或 PreStep）经 `a.log.Append` 落（与 job/subagent 的 onEvent sink 模式一致）。

**纪律**：**日志仍追加式（D1）**——压缩只追加摘要 user/message + compaction/* 观测事件，绝不物理删除旧事件；**不改 loop 的 turn/step 结构**（D4——只通过已存在的 PreStep 扩展点接线，不动 loop.go 本身；若 PreStep 现有 API 不足，允许最小扩展 PreStep 注入器签名，但**必须保持向后兼容**且记录）；主循环串行（D5）；零新依赖；CGO-free；原有测试全绿。**不要动**：`internal/compaction`（1a/1b 已验收，只读）、jobs、subagent、kb、store、tools 包（只读参考）。**不要做**：M5d 技能、KB 补全。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）；每次 Go 命令设 `$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\shutu-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\shutu-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\shutu-agent\.gocache'`。用 pwsh 执行命令。git 提交用 `git -C D:\dev-projects\Agent\shutu-agent -c user.name='Personal Agent' -c user.email='dev@shutu-agent.local' commit -m "..."`。不要提交 `pa.exe`、`data/`、缓存目录。

**上下文管理（关键）**：**分阶段提交**（session 事件一次 → config 一次 → /compact 命令一次 → PreStep + 事件接线一次，信息含 "M5c-2"）；只按需精读片段，不要通读参考库；报告只列文件名 + 一句话。

**自测**（全部通过再报告）：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试至少覆盖：事件追加/重放/不派生、config 缺省与 applyDefaults、/compact 命令（enabled/disabled）、PreStep 注入器（压力触发调用 CompactIfNeeded、enabled=false 不注册）、事件落日志恰好一次。

**完成报告**：改动文件清单、实现决策（PreStep 注入器如何接、事件三连锁如何落）、测试结果、提交 hash 列表、对 M5 主 ADR 的更新说明（如有）。提交后报告，不要等待确认——报告即交接。
