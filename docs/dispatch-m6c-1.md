# M6c-1 实施派发消息（控制会话 → 实施会话）——spill 接缝 + 沉淀策略内核

> 状态：已派发 2026-08-19（M6 能力补全六段，ADR `2026-08-19-m6-agent-full.md`；本文件为 M6c 第一半：接缝 + 策略内核）· 用法：把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\personal-agent` 的 **M6c-1：`internal/spill` 长期记忆接缝（Service 定义 + 多 Provider）+ 自动沉淀策略内核 + 单元测试**。这是 M6c 的第一半（第二半 M6c-2 做 spill_* 工具 + spill/* 事件 + config + 组合根接线，依赖你的接缝）。你是实施会话。

**直接开工，不要做任何前置检查**：不要跑 baseline check、不要验证环境。你的主要输入是 `D:\dev-projects\Agent\personal-agent\docs\dispatch-m6c-1.md`（本文件即主契约），**先完整读它**，然后立即用 write 工具创建文件。

**读这些（按需精读片段，不要通读）**：
1. `D:\dev-projects\Agent\personal-agent\docs\decisions\2026-08-19-m6-agent-full.md` —— M6 主 ADR，重点读 M6c 行（长期记忆，spill 与 kb 边界）。
2. `internal/schedule/service.go` + `internal/plan/service.go` —— 接缝模板（Provider/Engine + 哨兵错误 + Close 幂等）。
3. `internal/session/session.go` —— **只读**：看 Event/事件类型/DeriveHistory（本任务不落事件，但策略需要读历史）。
4. 参考（只借鉴思路，不精读）：`D:\dev-projects\Agent\deepseek-harness\packages\spill\spill\src\types.ts`、`spill-local\src\store.ts`、`spill-policy\src\types.ts`（记忆条目模型、沉淀策略）。

**实现内容**：
1. **`internal/spill` 接缝（`service.go`）**：
   ```go
   type Memo struct {
       ID        string
       Content   string    // 记忆正文（对话衍生的持久事实）
       Source    string    // 来源标识（如 "session:<id>:<turn>" 或 "auto"）
       CreatedAt time.Time
   }
   type Provider interface {
       Name() string
       List(ctx context.Context) ([]Memo, error)
       Add(ctx context.Context, m Memo) (Memo, error)      // 幂等 upsert by ID
       Get(ctx context.Context, id string) (Memo, error)
       Delete(ctx context.Context, id string) error
       // Search 供召回：按内容匹配（v1 用包含式匹配，零依赖；后续可换 FTS/向量 Provider）
       Search(ctx context.Context, query string, limit int) ([]Memo, error)
   }
   type Engine interface {
       // Spill 把一条对话衍生记忆写入长期记忆（去重：同 ID 幂等）。
       Spill(ctx context.Context, content, source string) (Memo, error)
       // Recall 按查询召回相关记忆（limit<=0 → 默认 5）。
       Recall(ctx context.Context, query string, limit int) ([]Memo, error)
       // AutoSpill 是自动沉淀策略：读会话历史（events），把"值得记住"的片段自动写入。
       // v1 策略（policy.go）：每个 turn 的 assistant 最终消息 + 工具结果摘要，
       // 按启发式（长度阈值、新信息标记）决定是否沉淀；由接线方在串行路径调用（D5）。
       AutoSpill(ctx context.Context, events []session.Event) (int, error)  // 返回新增条数
       List(ctx context.Context) ([]Memo, error)
       Remove(ctx context.Context, id string) error
       Close() error
   }
   ```
   - 默认 Provider：内存（`memProvider`，重启丢失；store 持久化留接口注释说明后续可加）。
   - 去重：`Spill` 按内容哈希生成 ID（幂等——同内容不重复写）。
   - Search v1：包含式子串匹配（大小写不敏感）+ limit 截断，零依赖。
2. **策略内核（`policy.go`）**：`AutoSpill` 读事件流，提取每个 `assistant/message` 的最终文本（不含工具调用帧）与 `tool/result` 摘要，按启发式判断"值得沉淀"（如文本 ≥ 某 rune 阈值、含明确结论性语句、非纯寒暄），写入去重后的记忆；返回新增条数。策略是纯函数（无副作用），可测。
3. **测试（`internal/spill/spill_test.go` + `policy_test.go`）**：Spill/Recall/去重（同内容幂等）、Search 包含式匹配 + limit、AutoSpill（对构造的事件流提取/去重/阈值过滤）、List/Remove、Close 幂等。

**纪律**：**本任务不落任何日志事件、不加任何工具、不加 config**（M6c-2 的事）；不改 loop turn/step（D4）；AutoSpill 是纯函数、无副作用（D5 由接线方保证在串行路径调用）；**spill 与 kb 边界**：kb=显式知识库（用户可检索），spill=对话自动记忆，本包不碰 kb 包（D9 保持）；零新依赖；CGO-free；原有测试全绿。**不要动**：loop、cmd/pa、config、jobs、subagent、compaction、skill、kb、store、tools、session（只读参考）、schedule、plan 包（只读参考）。**不要做**：spill_* 工具、spill/* 事件、config、组合根接线（M6c-2）。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）。每次 Go 命令这样跑（用 pwsh）：
`$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\personal-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\personal-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\personal-agent\.gocache'; & 'C:\Program Files\Go\bin\go.exe' test ./...`
git 提交：`git -C D:\dev-projects\Agent\personal-agent -c user.name='Personal Agent' -c user.email='dev@personal-agent.local' commit -m "M6c-1: <what>"`。不要提交 pa.exe/data/缓存。

**上下文管理（关键）**：**分阶段提交**——① service.go 接缝 + mem.go → ② policy.go 策略 → ③ 测试，每阶段一次 commit（信息含 "M6c-1"）。不要通读任何参考库。报告只列文件名+一句话。

**自测**（全部通过再报告）：vet/test/build 三命令全绿。新增测试覆盖：Spill/Recall/去重幂等、Search 包含式匹配 + limit、AutoSpill（提取/去重/阈值过滤）、List/Remove、Close 幂等。

**完成报告**：改动文件清单、实现决策（去重 ID 策略、AutoSpill 启发式、Search v1 方式）、测试结果、提交 hash 列表、对 M6 主 ADR 的更新说明（如有）。提交后报告即交接，不要等待确认。