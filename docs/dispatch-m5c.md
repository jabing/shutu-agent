# M5c 实施派发消息（控制会话 → 实施会话）——上下文压缩 compaction

> 状态：待 M5a/M5b 验收后派发 2026-08-18（M5 拆四段：M5a 后台任务 → M5b 子代理 → M5c 上下文压缩 → M5d 技能；本文件为第三段）· 用法：M5b 验收通过后，把下文整段粘贴给新开的实施会话。

---

请阅读 `D:\dev-projects\Agent\personal-agent\Agent.md`、`docs/design.md`（§4 pre-step 扩展点、§3 事件、§10 D1/D4/D5）**和 `docs/decisions/2026-08-18-m5-agent-core.md`（M5 主 ADR，本段对应"决策 ③"）**，并通读参考源码 `D:\dev-projects\Agent\deepseek-harness\packages\compaction\`（重点：`compaction/`（接缝 `src/types.ts`、`src/index.ts`）、`compaction-basic/`（token 压力 + 摘要）、`compaction-tool-result-pruner/`（tool-result 剪枝）、`command-compact/`（人工命令））以及 `docs/subsystems/compaction.md`（`compaction/*` 事件、`CompactionResult`、service 语义），按设计基线实现 **M5c 上下文压缩**（M5 第三段；本段验收标准见下，M5 完整验收标准见 Agent.md 第 4 节）。

**前置依赖**：本段依赖 **M5b 的 pre-step 扩展点**（自动压缩挂在 pre-step 注入器上）与现有 `internal/llm`（摘要模型）。若 M5b 验收后 `PreStep` 接口有调整，以**当前代码**为准。

**M5c 范围（只做压缩，不碰技能）**：

1. **`internal/compaction` 包——压缩接缝（Service 定义）+ 基础 Provider + tool-result 剪枝**：
   ```go
   type Trigger string   // pressure | context-overflow

   type Result struct {
       CompactionID  string
       Summary       string
       ShadowedRange [2]int64   // 被遮蔽 surface 位置跨度（首/尾 seq）
       ShadowedSeqs  []int64    // 被遮蔽的 surface 节点 seq（权威集合）
       ShadowedTokens int
   }

   type Engine interface {
       CompactIfNeeded(ctx context.Context, session *session.Log, trigger Trigger) (*Result, error)
       CompactNow(ctx context.Context, session *session.Log) (*Result, error)
       CompactRegion(ctx context.Context, session *session.Log, start, end int64) (*Result, error)
   }
   ```
   - **压缩语义**（对照 `docs/subsystems/compaction.md`，Go 简化）：
     1. **事件三连锁**（D3/D1）：`compaction/start`（占锁，标识本次尝试）→ 摘要生成 → `compaction/summary`（记录摘要投影 + 被遮蔽范围/seq/token + model 调用）→ `compaction/end`（释放锁）。崩溃中断产生"无配对的 start"即可探测（孤儿锁）。
     2. **摘要本身落成新 `user/message`**，载荷带 `surfaceOp: {op: "replace", start, end}`；`DeriveHistory` 折叠规则遇到该标记时跳过被遮蔽 seq 的旧事件、以摘要消息替代（**日志仍追加式，D1 不变**——旧事件物理保留，只是派生时被遮蔽）。这需要**修改 `DeriveHistory` 的折叠规则**（M2 预留的"只改折叠规则"位）。
     3. **tool-call/result 配对边界**：压缩范围边界必须保持 assistant 的 tool_calls 与其 tool/result 配对完整（不能截断配对中间）。提供 `ToolPairingBalancedBefore/After` 检查（seq 前后各检查配对是否完整）。
   - **基础 Provider（默认 `basic`）**：token 压力检测（估算当前 surface token 数——字符/词估算，零依赖；超 `token_threshold` 触发 `pressure`）、保留尾部策略（保留最近 `retain_turns` 个回合）、调用当前模型生成摘要（复用 `internal/llm`，`CompactIfNeeded` 的 `context-overflow` 比 `pressure` 更激进——可强制做一次有效平衡缩减）。`CompactNow` 低于压力也执行一次有效压缩（人工 `/compact`）。
   - **tool-result 剪枝（可选 Consumer）**：`internal/compaction/pruner.go`——纯确定性（无模型），对超预算的 `tool/result` 做 head/middle/tail 截断替换（Unicode code point 边界，不切 surrogates），记 `compaction/prune` 事件（定价被遮蔽节点）。
   - **人工命令**：`/compact`（REPL 命令，走 `CompactNow`；busy/无可用范围时给出明确提示）。
   - 摘要模型跟随会话模型（复用 `internal/llm` + `cfg.model`，零新配置；参照 M4c 提取的模型选择取舍）。

2. **Consumer（pre-step 注入器）**：自动压缩挂在 M5b 的 pre-step 扩展点上（`trigger = pressure`，token 估算超阈值才真正压缩；压缩后本次 turn 的注入器集合照常）。**压缩不是注入"上下文消息"**，而是改写 surface——pre-step 里先执行压缩检查，再走其他注入器。loop 的 turn/step 结构零改动（D4），`DeriveHistory` 折叠规则变更属于"折叠规则只改派生"（M2 预留）。

3. **事件（D3，`internal/session` 新增，log-only）**：`EventCompactionStart = "compaction/start"`、`EventCompactionSummary = "compaction/summary"`、`EventCompactionEnd = "compaction/end"`、`EventCompactionPrune = "compaction/prune"` + 载荷构造 `NewCompactionStart/NewCompactionSummary/NewCompactionEnd/NewCompactionPrune`（compaction id/turn/摘要投影/被遮蔽范围与 seq/token 数/model 调用）。`DeriveHistory` 对 `compaction/*` 事件**作为派生规则输入**（跳过被遮蔽 seq），不当作模型消息。

4. **config（`internal/config` 扩展）**：
   ```yaml
   compaction:
     enabled: false
     token_threshold: 32000
     retain_turns: 8
   ```
   `compaction.enabled` 单一开关（D10）：false ⇒ 不注册 pre-step 压缩注入器、不注册 `/compact`。

**决策记录（必交）**：M5 主 ADR `docs/decisions/2026-08-18-m5-agent-core.md` 决策 ③ 已写好（本段）。实施中若有偏离（如折叠规则实现、token 估算方式），**更新该 ADR 对应小节**并说明。

**约束**（严格遵守 design.md 第 10 节 D1–D10）：

- 不改 loop 的 turn/step 结构（D4）；`DeriveHistory` 折叠规则变更属"派生规则只改折叠"（M2 预留，设计基线 §3）。
- **日志仍追加式（D1）**：压缩绝不物理删除旧事件；被遮蔽事件保留在日志，只是派生时跳过。
- **明确不做（本段）**：技能（M5d）、token 计费服务抽象、多 Provider 并发压缩、压缩审计队列、跨会话压缩。只实现接缝 + basic Provider + 剪枝 + `/compact` + 事件 + config。
- `compaction.enabled` 默认关闭（D10）。
- 保持 CGO-free；**不新增任何第三方依赖**；Go 沙箱绕行沿用项目内缓存。
- 原有测试必须保持绿色（尤其 session `DeriveHistory` 相关测试）。

**参考源码**：`D:\dev-projects\Agent\deepseek-harness\packages\compaction\`（接缝、basic、pruner、command；**只借鉴思路与契约，不照搬 TS 代码**）。`docs/subsystems/compaction.md` 的 `compaction/*` 事件与 `CompactionResult`。

**自测（全部通过后提交，提交信息含 M5c）**：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试至少覆盖：**折叠规则**（压缩后 `DeriveHistory` = 摘要 + 未遮蔽尾部；被遮蔽 seq 不出现）、**日志仍追加式**（旧事件物理存在，只是派生被遮蔽）、**tool-call/result 配对边界**（不平衡范围拒绝压缩）、token 压力触发与 retain_turns 尾部保留、`CompactNow` 低于压力也有效、tool-result 剪枝（head/middle/tail、Unicode 边界、`compaction/prune` 事件）、`/compact` 命令（含 busy/无可用范围提示）、`compaction/*` 事件类型可落日志、compaction 默认关闭（enabled=false 不注册）。

**完成报告**：改动文件清单、实现决策、测试结果、提交 hash、对 M5 主 ADR 的更新说明（如有）。提交后报告，不要等待控制会话确认——报告即交接。
