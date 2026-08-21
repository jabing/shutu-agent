# M6f-2 实施派发消息（控制会话 → 实施会话）——mcp 工具 + 事件 + config + 组合根桥接

> 状态：待 M6f-1 验收后派发 2026-08-19（M6 能力补全六段，ADR `2026-08-19-m6-agent-full.md`；本文件为 M6f 第二派发：MCP 工具 + 接线）· 用法：M6f-1 验收通过后，把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\shutu-agent` 的 **M6f-2：`mcp_*` 工具 + `mcp/*` 事件 + config + 组合根接线（把外部 MCP server 的工具桥接进注册表）+ 单元测试**。这是 M6f 的第二派发（第一派发 M6f-1 已做 `internal/mcp` 接缝 + 自实现 JSON-RPC over stdio 客户端，你**依赖它们**）。零新依赖。你是实施会话。

**直接开工，不要做任何前置检查**。你的主要输入是 `D:\dev-projects\Agent\shutu-agent\docs\dispatch-m6f-2.md`（本文件即主契约），**先完整读它**，然后立即用 write 工具创建/修改文件。

**读这些（按需精读片段，不要通读）**：
1. `D:\dev-projects\Agent\shutu-agent\docs\dispatch-m6f-1.md` —— 接缝契约（Client/Factory/Tool/CallResult 签名）。
2. `D:\dev-projects\Agent\shutu-agent\docs\decisions\2026-08-19-m6-agent-full.md` —— M6 主 ADR（M6f 行）。
3. 现有代码（按需精读）：
   - `internal/mcp/service.go` + `stdio.go`（M6f-1 已做）。
   - `internal/session/session.go` —— 各能力事件的 log-only 模式（模板）。
   - `internal/config/config.go` —— 各段模式 + applyDefaults 白名单（job_*/subagent_*/schedule_*/plan_*/spill_*/interact_*/code_*）。
   - `cmd/pa/*.go` —— register* 组合根模式、工具注册、onEvent sink、命令注册、/help 状态行。
   - `internal/tools/` —— tools.Tool + D7 + 注册表（桥接目标）。
4. 参考（只借鉴思路，不精读）：`D:\dev-projects\Agent\deepseek-harness\packages\mcp\mcp-client\src\tools.ts`。

**实现内容**：
1. **事件（`internal/session/session.go` + 测试）**：新增 `EventMcpList/Call`（`mcp/list|call`）+ `NewMcpList(count int) any`、`NewMcpCall(name string, isError bool) any`。**log-only**：DeriveHistory 不派生。
2. **config（`internal/config` + config.yaml）**：`McpConfig{Enabled bool, Servers []McpServer}`（yaml: `enabled/servers`；`McpServer{Name, Cmd string, Args []string}`）。默认 `enabled:false / servers:[]`。**enabled 时自动白名单 `mcp_*` 工具**。
3. **mcp_* 工具（`internal/mcp/tools.go` + 测试）**：`NewMcpTools(f Factory, servers []McpServer, onEvent func(typ string, data any))` 返回结构化 tools.Tool 集合（不 import tools 包，D2）：
   - `mcp_list(server)`：列出某 server 的工具；D7 schema（additionalProperties:false）；落 `mcp/list`。
   - `mcp_call(server, tool, args map[string]any)`：调用某 server 的工具；落 `mcp/call`。
   - 事件经 onEvent sink（串行工具路径）；未知 server/协议错误返回错误消息（非 panic）。
4. **工具桥接（cmd/pa 接线）**：`mcp.enabled && servers 非空` 时，为每个配置的 server 建立 stdio 客户端（Factory.New + Start + ListTools），把 server 工具动态注册进工具注册表（名称加前缀 `mcp.<server>.<tool>` 防冲突；参数 schema 透传）；调用时转 `tools/call`。**接线位置在工具注册层，不改 loop（D4）**；Client 调用是前台串行（D5）。
5. **组合根（cmd/pa `registerMcps()`）**：`mcp.enabled` 时建 Factory + 注册 mcp_* 工具 + 桥接工具 + 事件 sink；disabled 零操作（D10）。`main.go` 调用 + `app.mcp` 字段（持客户端列表）+ deferred Close + /help 状态行。

**纪律**：**日志仍追加式（D1）**；不改 loop turn/step（D4）；串行路径（D5）；零新依赖（不引 MCP SDK）；CGO-free；原有测试全绿。**不要动**：`internal/mcp/service.go`/`stdio.go`（M6f-1 已验收，只读；tools.go 新建）、loop.go（只读，不要改）、compaction、subagent、skill、kb、store、schedule、plan、spill、interact、code 包（只读参考）。**不要做**：fs（M6f-3）、M6e 之外的代码能力、KB 补全。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）。每次 Go 命令这样跑（用 pwsh）：
`$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\shutu-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\shutu-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\shutu-agent\.gocache'; & 'C:\Program Files\Go\bin\go.exe' test ./...`
git 提交：`git -C D:\dev-projects\Agent\shutu-agent -c user.name='Personal Agent' -c user.email='dev@shutu-agent.local' commit -m "M6f-2: <what>"`。不要提交 pa.exe/data/缓存。

**上下文管理（关键）**：**分阶段提交**——① session 事件 → ② config → ③ tools → ④ 桥接 + 组合根，每阶段一次 commit（信息含 "M6f-2"）。不要通读任何参考库。报告只列文件名+一句话。

**自测**（全部通过再报告）：vet/test/build 三命令全绿。新增测试至少覆盖：事件追加/重放/不派生、config 缺省 + 白名单、mcp_list/mcp_call（D7 + 事件 + 未知 server 报错）、工具桥接（注册前缀/schema 透传/调用转交，用伪 server）、enabled=false 不注册。

**完成报告**：改动文件清单、实现决策（桥接命名、schema 透传、伪 server 测试方案）、测试结果、提交 hash 列表、对 M6 主 ADR 的更新说明（如有）。提交后报告即交接，不要等待确认。