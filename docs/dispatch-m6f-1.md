# M6f-1 实施派发消息（控制会话 → 实施会话）——mcp 接缝 + 自实现 JSON-RPC over stdio 客户端

> 状态：已派发 2026-08-19（M6 能力补全六段，ADR `2026-08-19-m6-agent-full.md`；本文件为 M6f 第一派发：MCP 客户端内核）· 用法：把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\shutu-agent` 的 **M6f-1：`internal/mcp` 接缝（Service 定义 + 多 Provider）+ 自实现 JSON-RPC 2.0 over stdio 的 MCP 客户端内核 + 单元测试**。这是 M6f 的第一派发（M6f-2 做 mcp 工具 + mcp/* 事件 + config + 组合根接线把 MCP server 的工具桥接进注册表，依赖你的接缝）。**零新依赖**：MCP 客户端自实现 JSON-RPC（标准库 encoding/json + bufio + os/exec 即可，这是 ADR 声明的首个零新依赖例外——优先自实现，不引 SDK）。你是实施会话。

**直接开工，不要做任何前置检查**：不要跑 baseline check、不要验证环境。你的主要输入是 `D:\dev-projects\Agent\shutu-agent\docs\dispatch-m6f-1.md`（本文件即主契约），**先完整读它**，然后立即用 write 工具创建文件。

**读这些（按需精读片段，不要通读）**：
1. `D:\dev-projects\Agent\shutu-agent\docs\decisions\2026-08-19-m6-agent-full.md` —— M6 主 ADR，重点读 M6f 行（工具生态：MCP 优先自实现 JSON-RPC；SDK 仅在协议复杂度超过时考虑；fs 安全封装）。
2. `internal/schedule/service.go` + `internal/code/service.go` —— 接缝模板（Provider/Engine + 哨兵错误 + Close 幂等；code 的子进程/超时/输出边界可参考）。
3. `internal/tools/` —— tools.Tool 结构化接口（M6f-2 要桥接，但本派发只读参考其工具形状）。
4. 参考（只借鉴思路，不精读）：`D:\dev-projects\Agent\deepseek-harness\packages\mcp\mcp-client\src\transport.ts`、`connection.ts`、`tools.ts`。

**实现内容**：
1. **`internal/mcp` 接缝（`service.go`）**：
   ```go
   type Tool struct {
       Name        string
       Description string
       InputSchema map[string]any   // JSON Schema（来自 server 的 tools/list）
   }
   type CallResult struct {
       Content []any  // server 返回的 content 列表（文本等）
       IsError bool
   }
   type Client interface {
       // Start 启动 stdio 子进程并完成 MCP initialize 握手；Idle 幂等。
       Start(ctx context.Context) error
       // ListTools 返回 server 的可用工具（tools/list）。
       ListTools(ctx context.Context) ([]Tool, error)
       // Call 调用 server 工具（tools/call）；结果返回给模型。
       Call(ctx context.Context, name string, args map[string]any) (CallResult, error)
       // Close 关闭子进程与连接；幂等。
       Close() error
   }
   type Factory interface {
       // New 由命令 + 参数构造一个 stdio MCP 客户端。
       New(ctx context.Context, cmd string, args []string) (Client, error)
   }
   ```
   - 默认实现：`stdioClient`（`NewStdioClient(cmd string, args []string)`，零依赖）——os/exec 启动子进程（stdin/stdout 管道），JSON-RPC 2.0 帧（每请求一行的新行定界 JSON），顺序 ID，请求/响应按 ID 匹配，`initialize` 握手 + `tools/list` + `tools/call` 方法封装。
   - 超时：每请求 ctx 超时（默认 30s）。
   - 错误：启动失败/握手失败/协议错误/未知方法 → 哨兵错误；server 返回 error 帧 → 封装错误。
   - Close 幂等：终止子进程（kill）、关闭管道、无泄漏（无后台 goroutine 泄漏；如需读取循环，用同步读 + ctx 取消）。
2. **测试（`internal/mcp/mcp_test.go`）**：用**伪 server 子进程**（测试内起一个 `cmd /C` 或 Go 测试辅助进程按 JSON-RPC 协议应答）覆盖：Start 握手、ListTools、Call 成功、Call 错误帧、超时、Close 幂等。**注意 Windows cmd /C 引号边界**（参考 M6e-1 的 cmd /C 注释）：伪 server 用 `os.Args` 检测的辅助模式更稳（test binary 以 -test.run=TestHelper 子进程方式）。

**纪律**：**本任务不落任何日志事件、不加任何工具、不加 config**（M6f-2 的事）；不改 loop turn/step（D4）；Client 调用是前台串行（D5，无后台 goroutine 泄漏）；**零新依赖**（标准库即可，绝不引 MCP SDK）；CGO-free；原有测试全绿。**不要动**：loop、cmd/pa、config、jobs、subagent、compaction、skill、kb、store、tools、session（只读参考）、schedule、plan、spill、interact、code 包（只读参考）。**不要做**：mcp 工具/事件/config/组合根桥接（M6f-2）、fs（M6f-3）。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）。每次 Go 命令这样跑（用 pwsh）：
`$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\shutu-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\shutu-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\shutu-agent\.gocache'; & 'C:\Program Files\Go\bin\go.exe' test ./...`
git 提交：`git -C D:\dev-projects\Agent\shutu-agent -c user.name='Personal Agent' -c user.email='dev@shutu-agent.local' commit -m "M6f-1: <what>"`。不要提交 pa.exe/data/缓存。

**上下文管理（关键）**：**分阶段提交**——① service.go 接缝 + stdio.go（JSON-RPC 传输+客户端）→ ② 测试（伪 server），每阶段一次 commit（信息含 "M6f-1"）。不要通读任何参考库。报告只列文件名+一句话。

**自测**（全部通过再报告）：vet/test/build 三命令全绿。新增测试覆盖：Start 握手、ListTools、Call 成功/错误帧/超时、Close 幂等（含子进程终止、无泄漏）。

**完成报告**：改动文件清单、实现决策（JSON-RPC 帧定界、ID 匹配、伪 server 测试方案）、测试结果、提交 hash 列表、对 M6 主 ADR 的更新说明（如有）。提交后报告即交接，不要等待确认。