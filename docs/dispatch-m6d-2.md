# M6d-2 实施派发消息（控制会话 → 实施会话）——interact 工具 + 事件 + config + 接线 + 敏感工具门

> 状态：待 M6d-1 验收后派发 2026-08-19（M6 能力补全六段，ADR `2026-08-19-m6-agent-full.md`；本文件为 M6d 第二半：工具 + 事件 + config + 接线 + 敏感工具门）· 用法：M6d-1 验收通过后，把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\personal-agent` 的 **M6d-2：`interact_*` 工具 + `interact/*` 事件 + config + 组合根接线 + 敏感工具门 + 单元测试**。这是 M6d 的第二半（第一半 M6d-1 已做 `internal/interact` 接缝 + 审批内核，你**依赖它们**）。你是实施会话。

**直接开工，不要做任何前置检查**。你的主要输入是 `D:\dev-projects\Agent\personal-agent\docs\dispatch-m6d-2.md`（本文件即主契约），**先完整读它**，然后立即用 write 工具创建/修改文件。

**读这些（按需精读片段，不要通读）**：
1. `D:\dev-projects\Agent\personal-agent\docs\dispatch-m6d-1.md` —— 接缝契约（Engine/Request/ApprovalStatus 签名、Await 轮询语义）。
2. `D:\dev-projects\Agent\personal-agent\docs\decisions\2026-08-19-m6-agent-full.md` —— M6 主 ADR（M6d 行：敏感工具执行前经 interact 门，CLI 侧）。
3. 现有代码（按需精读）：
   - `internal/interact/service.go` + `mem.go`（M6d-1 已做）。
   - `internal/session/session.go` —— 各能力事件的 log-only 模式（模板）。
   - `internal/config/config.go` —— 各段模式 + applyDefaults 白名单。
   - `cmd/pa/*.go` —— register* 组合根模式、工具注册、onEvent sink、命令注册、repl() 串行流（M4c extractTurn / M6c spillAutoSpill 收尾钩子所在）。
   - `internal/tools/` —— tools.Tool + D7 + 工具注册/白名单；`internal/jobs/tools.go`（工具层事件模式）。
4. 参考（只借鉴思路，不精读）：`D:\dev-projects\Agent\deepseek-harness\packages\interaction\user-approval\src\index.ts`、`tool-ask-user\src\index.ts`。

**实现内容**：
1. **事件（`internal/session/session.go` + 测试）**：新增 `EventInteractRequest/Resolve/Deny`（`interact/request|resolve|deny`）+ `NewInteractRequest(id, toolName string) any`、`NewInteractResolve(id string, approved bool) any`、`NewInteractDeny(id string) any`。**log-only**：DeriveHistory 不派生。
2. **config（`internal/config` + config.yaml）**：`InteractConfig{Enabled bool, SensitiveTools []string}`（yaml: `enabled/sensitive_tools`）；默认 `enabled:false`（SensitiveTools 空 = 无门控）。**enabled 时自动白名单 `interact_*` 工具**。
3. **interact_* 工具（`internal/interact/tools.go` + 测试）**：`NewInteractTools(e Engine, onEvent func(typ string, data any))` 返回结构化 tools.Tool 集合（不 import tools 包，D2）：
   - `interact_ask(prompt)`：向用户发起问题/审批请求；D7 schema（additionalProperties:false）；返回请求 ID + 当前状态（CLI 交互由用户终端处理，工具返回后模型继续）；落 `interact/request`。
   - `interact_status(id)`：查审批状态；落 `interact/status`（若需补充事件词汇则加）。
   - 事件经 onEvent sink（串行工具路径）。
4. **敏感工具门（cmd/pa 接线）**：`interact.enabled && sensitive_tools 非空` 时，对白名单工具执行前拦截：若工具名 ∈ SensitiveTools，先 `Engine.Request(...)` 发起审批 → **CLI 串行路径等待用户 y/n**（复用 Engine.Await，读终端）→ approved 才放行执行，rejected 返回"被用户拒绝"给模型。**接线位置在工具执行包装层（cmd/pa 的 Execute 包装或注册拦截），不改 loop turn/step（D4），串行路径（D5）**。
5. **组合根（cmd/pa `registerInteracts()`）**：`interact.enabled` 时创建内存 Provider + Engine + 注册 interact_* 工具（白名单）+ 敏感工具门包装 + 事件 sink；disabled 零操作（D10）。`main.go` 调用 + `app.interacts` 字段 + deferred Close + /help 状态行。

**纪律**：**日志仍追加式（D1）**；不改 loop turn/step（D4）——敏感工具门在工具执行包装层；串行路径（D5）——Await 是调用方驱动的轮询，无后台 goroutine；零新依赖；CGO-free；原有测试全绿。**不要动**：`internal/interact/service.go`/`mem.go`（M6d-1 已验收，只读；tools.go 新建）、loop.go（只读，不要改）、compaction、subagent、skill、kb、store、schedule、plan、spill 包（只读参考）。**不要做**：M6e–M6f、KB 补全。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）。每次 Go 命令这样跑（用 pwsh）：
`$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\personal-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\personal-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\personal-agent\.gocache'; & 'C:\Program Files\Go\bin\go.exe' test ./...`
git 提交：`git -C D:\dev-projects\Agent\personal-agent -c user.name='Personal Agent' -c user.email='dev@personal-agent.local' commit -m "M6d-2: <what>"`。不要提交 pa.exe/data/缓存。

**上下文管理（关键）**：**分阶段提交**——① session 事件 → ② config → ③ tools → ④ 敏感工具门 + 组合根，每阶段一次 commit（信息含 "M6d-2"）。不要通读任何参考库。报告只列文件名+一句话。

**自测**（全部通过再报告）：vet/test/build 三命令全绿。新增测试至少覆盖：事件追加/重放/不派生、config 缺省 + 白名单、interact_ask/status（D7 + 事件）、敏感工具门（SensitiveTools 命中 → 审批 approved 放行/rejected 拒绝返回、未命中不拦截、enabled=false 不注册）。

**完成报告**：改动文件清单、实现决策（敏感工具门接线位置、Await 在 CLI 如何驱动）、测试结果、提交 hash 列表、对 M6 主 ADR 的更新说明（如有）。提交后报告即交接，不要等待确认。