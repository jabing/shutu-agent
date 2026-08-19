# M6c-2 实施派发消息（控制会话 → 实施会话）——spill 工具 + 事件 + config + 接线

> 状态：待 M6c-1 验收后派发 2026-08-19（M6 能力补全六段，ADR `2026-08-19-m6-agent-full.md`；本文件为 M6c 第二半：工具 + 事件 + config + 接线）· 用法：M6c-1 验收通过后，把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\personal-agent` 的 **M6c-2：`spill_*` 工具 + `spill/*` 事件 + config + 组合根接线（含自动沉淀 D5 路径）+ 单元测试**。这是 M6c 的第二半（第一半 M6c-1 已做 `internal/spill` 接缝 + 策略内核，你**依赖它们**）。你是实施会话。

**直接开工，不要做任何前置检查**。你的主要输入是 `D:\dev-projects\Agent\personal-agent\docs\dispatch-m6c-2.md`（本文件即主契约），**先完整读它**，然后立即用 write 工具创建/修改文件。

**读这些（按需精读片段，不要通读）**：
1. `D:\dev-projects\Agent\personal-agent\docs\dispatch-m6c-1.md` —— 接缝契约（Engine/Memo/Provider 签名、AutoSpill 语义）。
2. `D:\dev-projects\Agent\personal-agent\docs\decisions\2026-08-19-m6-agent-full.md` —— M6 主 ADR（M6c 行 + spill/kb 边界）。
3. 现有代码（按需精读）：
   - `internal/spill/service.go` + `mem.go` + `policy.go`（M6c-1 已做）。
   - `internal/session/session.go` —— 各能力事件的 log-only 模式（模板）。
   - `internal/config/config.go` —— 各段模式 + applyDefaults 白名单。
   - `cmd/pa/*.go` —— register* 组合根模式、工具注册、onEvent sink、preStepInjectors()（recall→compaction→skill→schedule 现有顺序）、命令注册。
   - `internal/loop/loop.go` —— `Config.PreStep`（自动沉淀挂 pre-step，若接线需要）。
   - `internal/tools/` —— tools.Tool + D7；`internal/plan/tools.go`、`internal/schedule/tools.go`（工具层事件模式）。
4. 参考（只借鉴思路，不精读）：`D:\dev-projects\Agent\deepseek-harness\packages\spill\spill\src\index.ts`、`spill-policy\src\index.ts`。

**实现内容**：
1. **事件（`internal/session/session.go` + 测试）**：新增 `EventSpillWrite/Recall/List/Delete`（`spill/write|recall|list|delete`）+ `NewSpillWrite(id, content string) any`（content 摘要 200-rune 有界）、`NewSpillRecall(query string, count int) any`、`NewSpillList(count int) any`、`NewSpillDelete(id string) any`。**log-only**：DeriveHistory 不派生。
2. **config（`internal/config` + config.yaml）**：`SpillConfig{Enabled bool, AutoSpill bool}`（yaml: `enabled/auto_spill`）；默认 `enabled:false / auto_spill:true`（auto_spill 在 enabled 时生效）。**enabled 时自动白名单 `spill_*` 工具**。
3. **spill_* 工具（`internal/spill/tools.go` + 测试）**：`NewSpillTools(e Engine, onEvent func(typ string, data any))` 返回结构化 tools.Tool 集合（不 import tools 包，D2）：
   - `spill_write(content, source)`：显式写入记忆；D7 schema（additionalProperties:false）；落 `spill/write`。
   - `spill_recall(query, limit)`：召回；落 `spill/recall`。
   - `spill_list()`：列出记忆；落 `spill/list`。
   - `spill_delete(id)`：删除；未知 id 报错；落 `spill/delete`。
   - 事件经 onEvent sink（串行工具路径）。
4. **自动沉淀路径（cmd/pa 接线）**：`spill.enabled && spill.auto_spill` 时，在每 turn 结束后（turn 完成时，不是 step 内）调 `Engine.AutoSpill(ctx, log.Events())` 自动沉淀新记忆——**接线点在串行路径**（如 loop 的 step 完成后、或 turn 收尾钩子；若用 PreStep 则注意它跑在 user 输入前，更适合"对上一轮结果沉淀"——请选串行且不重复沉淀的时机，落 `spill/write` 事件）。**AutoSpill 本身是纯函数（M6c-1）**，接线方保证只在串行路径调用（D5），后台无 goroutine。
5. **组合根（cmd/pa `registerSpills()`）**：`spill.enabled` 时创建内存 Provider + Engine + 注册 spill_* 工具（白名单）+ 自动沉淀接线 + 事件 sink；disabled 零操作（D10）。`main.go` 调用 + `app.spills` 字段 + deferred Close + /help 状态行。

**纪律**：**日志仍追加式（D1）**；不改 loop turn/step 结构（D4）——自动沉淀走已有扩展点（PreStep 或 turn 收尾），**若必须改 loop 则不允许**，改接线位置（如命令处理后）实现；串行路径（D5）；零新依赖；CGO-free；原有测试全绿。**不要动**：`internal/spill/service.go`/`mem.go`/`policy.go`（M6c-1 已验收，只读；tools.go 新建）、loop.go（只读，不要改）、compaction、subagent、skill、kb、store、schedule、plan 包（只读参考）。**不要做**：M6d–M6f、KB 补全、把 spill 并入 kb。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）。每次 Go 命令这样跑（用 pwsh）：
`$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\personal-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\personal-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\personal-agent\.gocache'; & 'C:\Program Files\Go\bin\go.exe' test ./...`
git 提交：`git -C D:\dev-projects\Agent\personal-agent -c user.name='Personal Agent' -c user.email='dev@personal-agent.local' commit -m "M6c-2: <what>"`。不要提交 pa.exe/data/缓存。

**上下文管理（关键）**：**分阶段提交**——① session 事件 → ② config → ③ tools → ④ 自动沉淀 + 组合根，每阶段一次 commit（信息含 "M6c-2"）。不要通读任何参考库。报告只列文件名+一句话。

**自测**（全部通过再报告）：vet/test/build 三命令全绿。新增测试至少覆盖：事件追加/重放/不派生、config 缺省 + 白名单、spill_write/recall/list/delete（D7 + 事件 + 未知 id 报错）、自动沉淀路径（串行、不重复、落事件）、enabled=false 不注册。

**完成报告**：改动文件清单、实现决策（自动沉淀接线时机如何选、如何保 D5 串行不重复）、测试结果、提交 hash 列表、对 M6 主 ADR 的更新说明（如有）。提交后报告即交接，不要等待确认。