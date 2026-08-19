# M5c-1b 实施派发消息（控制会话 → 实施会话）——compaction 接缝 + 基础 Provider + 剪枝

> 状态：待 M5c-1a 验收后派发 2026-08-19（M5c 第一半因任务过大拆为 1a/1b 顺序派发；本文件为 1b：接缝 + Provider + 剪枝）· 用法：M5c-1a 验收通过后，把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\personal-agent` 的 **M5c-1b：`internal/compaction` 接缝（Service 定义）+ 基础 Provider + tool-result 剪枝 + 单元测试**。这是 M5c 的一个子任务（M5c-1a 另一个会话已做 session 折叠规则改造，你**依赖它**——`NewUserMessageReplace` 已存在于 session 包）。你是实施会话。

**必读（先读这些，不要通读参考源码）**：
1. `D:\dev-projects\Agent\personal-agent\docs\dispatch-m5c-1.md` —— 你的主契约，重点读「实现内容」第 2（接缝）、3（基础 Provider）、4（剪枝）条。**第 1 条（折叠规则）已由 M5c-1a 完成，跳过。**
2. `D:\dev-projects\Agent\personal-agent\docs\decisions\2026-08-18-m5-agent-core.md` 的「决策 ③」（压缩语义、遮蔽、配对边界）。
3. 现有代码：
   - `internal/session/session.go` —— **`NewUserMessageReplace(text string, start, end int64)`（M5c-1a 已加）**、`NewUserMessage`、`Event`/`EventUserMessage`、`Append`、`DeriveHistory`、`Events()`。事件载荷形状：`userMessageData`（含可选 `surfaceOp:{op:"replace",start,end}`，私有类型，用 `New*` 构造）。
   - `internal/llm/`（复用摘要模型：LLM 接口与调用方式，读 `internal/llm/deepseek` 的接口定义即可）。
   - `internal/jobs/local.go`（truncateUTF8 / Unicode 安全截断模式）。
   - `internal/session/session_test.go`（测试模式）。
4. `Agent.md` 第 10 节 D1–D10 纪律。

**实现内容**（严格按契约）：
1. **接缝（`service.go`）**：
   ```go
   type Trigger string // pressure | context-overflow
   type Result struct {
       CompactionID   string
       Summary        string
       ShadowedRange  [2]int64  // 被遮蔽 surface 位置跨度（首/尾 seq）
       ShadowedSeqs   []int64   // 被遮蔽的 surface 节点 seq（权威集合）
       ShadowedTokens int
   }
   type SessionLike interface {
       Events() []session.Event
       Append(typ string, data any) (session.Event, error)
       DeriveHistory() []llm.Message
   }
   type Engine interface {
       CompactIfNeeded(ctx context.Context, sess SessionLike, trigger Trigger) (*Result, error)
       CompactNow(ctx context.Context, sess SessionLike) (*Result, error)
       CompactRegion(ctx context.Context, sess SessionLike, start, end int64) (*Result, error)
   }
   ```
   - 工具函数：`ToolPairingBalancedBefore(sess SessionLike, seq int64) bool` / `ToolPairingBalancedAfter(sess SessionLike, seq int64) bool`——检查某 surface 位置两侧 assistant tool_calls 与其 tool/result 配对是否完整（不切断配对中间）。
2. **基础 Provider（`basic.go`）**：`BasicEngine`（`NewBasic(BasicOpts{Tokenizer, LLM, Model, TokenThreshold, RetainTurns})`）：
   - token 压力检测：零依赖简单估算（如 rune/4 或 len(bytes)/4，注释说明）。
   - `CompactIfNeeded`：估算超 `TokenThreshold` 触发；`context-overflow` 比 `pressure` 更激进（强制一次有效平衡缩减）。
   - 保留尾部：保留最近 `RetainTurns` 个 `user/message` 回合；被遮蔽范围 = 前缀（用 `ToolPairingBalancedBefore/After` 校正到配对边界）。
   - 摘要生成：复用 `internal/llm` 把被遮蔽范围历史折叠成摘要；失败返回错误（fail-open 由接线处理）。摘要上下文 = `DeriveHistory()` 中被遮蔽部分。
   - **落摘要**：压缩成功时用 `sess.Append(session.EventUserMessage, session.NewUserMessageReplace(summary, start, end))` 落一条带 replace 标记的 user/message（旧事件物理保留——D1，折叠靠 M5c-1a 的 derive 规则）。返回 `Result`（CompactionID 自生成，如时间戳/计数）。
   - `CompactNow`：低于压力也执行一次有效压缩（选最近 RetainTurns 之前的最大平衡前缀）。`CompactRegion`：对给定范围做配对校正后压缩。
3. **剪枝（`pruner.go`）**：`PruneToolResults(sess SessionLike, maxBytes int) (PruneResult, error)`——纯确定性（无模型），对超预算的 `tool/result` 做 head/middle/tail 截断替换（Unicode code point 边界，不切 rune），返回 `PruneResult{Replaced []seq, SavedBytes int}`。**本任务只实现函数 + 测试**；`compaction/prune` 事件由接线落。
4. 测试：`internal/compaction/basic_test.go` + `pruner_test.go`——token 压力触发/不触发、retain_turns 尾部保留、被遮蔽范围配对校正（不平衡范围校正到平衡）、摘要生成（fake LLM）、CompactNow/CompactRegion、剪枝 head/middle/tail、Unicode 边界。

**纪律**：**日志仍追加式（D1）**——压缩绝不物理删除旧事件，被遮蔽事件保留，只落新摘要事件；不改 loop turn/step（D4）；主循环串行（D5）；零新依赖；CGO-free；原有测试全绿。**不要动**：loop、cmd/pa、config、jobs、subagent、tools、kb、store 包（只读参考）。**不要做**：/compact 命令、compaction/* 事件类型、config、PreStep 接线（接线）。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）；每次 Go 命令设 `$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\personal-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\personal-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\personal-agent\.gocache'`。用 pwsh 执行命令。git 提交用 `git -C D:\dev-projects\Agent\personal-agent -c user.name='Personal Agent' -c user.email='dev@personal-agent.local' commit -m "..."`。不要提交 `pa.exe`、`data/`、缓存目录。

**上下文管理（关键）**：**分阶段提交**（接缝 service.go 一次 → basic.go + pruner.go 一次 → 测试一次，信息含 "M5c-1b"）；只按需精读片段，不要通读参考库；报告只列文件名 + 一句话。

**自测**（全部通过再报告）：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试覆盖契约「自测」段：配对边界校正、token 压力、retain_turns、摘要（fake LLM）、CompactNow/CompactRegion、剪枝 head/middle/tail + Unicode 边界。

**完成报告**：改动文件清单、实现决策（token 估算方式、配对校正、摘要落法）、测试结果、提交 hash 列表、对 M5 主 ADR 的更新说明（如有）。提交后报告，不要等待确认——报告即交接。
