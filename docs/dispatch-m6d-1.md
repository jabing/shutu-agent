# M6d-1 实施派发消息（控制会话 → 实施会话）——interact 接缝 + 审批内核

> 状态：已派发 2026-08-19（M6 能力补全六段，ADR `2026-08-19-m6-agent-full.md`；本文件为 M6d 第一半：接缝 + 审批内核）· 用法：把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\personal-agent` 的 **M6d-1：`internal/interact` 人工审批接缝（Service 定义 + 多 Provider）+ 审批请求/响应内核 + 单元测试**。这是 M6d 的第一半（第二半 M6d-2 做 interact_* 工具 + interact/* 事件 + config + 组合根接线 + 敏感工具门，依赖你的接缝）。你是实施会话。

**直接开工，不要做任何前置检查**：不要跑 baseline check、不要验证环境。你的主要输入是 `D:\dev-projects\Agent\personal-agent\docs\dispatch-m6d-1.md`（本文件即主契约），**先完整读它**，然后立即用 write 工具创建文件。

**读这些（按需精读片段，不要通读）**：
1. `D:\dev-projects\Agent\personal-agent\docs\decisions\2026-08-19-m6-agent-full.md` —— M6 主 ADR，重点读 M6d 行（人工审批，CLI 侧交互）。
2. `internal/schedule/service.go` + `internal/plan/service.go` —— 接缝模板（Provider/Engine + 哨兵错误 + Close 幂等）。
3. `internal/session/session.go` —— 只读，不加事件。
4. 参考（只借鉴思路，不精读）：`D:\dev-projects\Agent\deepseek-harness\packages\interaction\user-approval\src\types.ts`、`index.ts`（审批请求/响应模型）；`tool-ask-user\src\index.ts`。

**实现内容**：
1. **`internal/interact` 接缝（`service.go`）**：
   ```go
   type ApprovalStatus string // pending | approved | rejected | expired
   type Request struct {
       ID          string
       Prompt      string   // 给用户看的说明（敏感操作描述）
       ToolName    string   // 触发审批的工具名
       Args        string   // 触发时的参数 JSON（有界，≤200 rune）
       Status      ApprovalStatus
       CreatedAt   time.Time
       ResolvedAt  *time.Time
   }
   type Provider interface {
       Name() string
       List(ctx context.Context) ([]Request, error)
       Create(ctx context.Context, r Request) (Request, error)
       Resolve(ctx context.Context, id string, status ApprovalStatus) error
   }
   type Engine interface {
       // Request 创建一个待审批请求，返回其 ID。pending 数量超上限（默认 20）或 Provider 关闭时报错。
       Request(ctx context.Context, prompt, toolName, args string) (Request, error)
       // Resolve 用户决定：approved/rejected。未知 id 或已解决重复解决报错。
       Resolve(ctx context.Context, id string, status ApprovalStatus) (Request, error)
       // Await 阻塞等待某请求被解决（供 CLI 交互用：发起后等待用户在终端输入 y/n）。
       // v1：无后台等待——Await 轮询 Provider（短间隔）直到 Resolve 或 ctx 取消；调用方负责在 CLI 串行路径驱动（D5）。
       Await(ctx context.Context, id string) (Request, error)
       List(ctx context.Context) ([]Request, error)
       Close() error
   }
   ```
   - 默认 Provider：内存（`memProvider`，重启丢失）。
   - 校验：空 Prompt 拒绝；非法 status 拒绝；重复 Resolve 拒绝；pending 上限（默认 20）。
   - Close 幂等无泄漏（无 goroutine）。
2. **测试（`internal/interact/interact_test.go`）**：Request/Resolve（approved/rejected）、未知 id 拒绝、重复 Resolve 拒绝、pending 上限、List、Await（用注入的短轮询 Provider 测试解决路径）、Close 幂等。

**纪律**：**本任务不落任何日志事件、不加任何工具、不加 config**（M6d-2 的事）；不改 loop turn/step（D4）；Await 是轮询（无后台 goroutine，由调用方在串行路径驱动，D5）；零新依赖；CGO-free；原有测试全绿。**不要动**：loop、cmd/pa、config、jobs、subagent、compaction、skill、kb、store、tools、session（只读参考）、schedule、plan、spill 包（只读参考）。**不要做**：interact_* 工具、interact/* 事件、config、组合根接线、敏感工具门（M6d-2）。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）。每次 Go 命令这样跑（用 pwsh）：
`$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\personal-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\personal-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\personal-agent\.gocache'; & 'C:\Program Files\Go\bin\go.exe' test ./...`
git 提交：`git -C D:\dev-projects\Agent\personal-agent -c user.name='Personal Agent' -c user.email='dev@personal-agent.local' commit -m "M6d-1: <what>"`。不要提交 pa.exe/data/缓存。

**上下文管理（关键）**：**分阶段提交**——① service.go 接缝 + mem.go → ② 测试，每阶段一次 commit（信息含 "M6d-1"）。不要通读任何参考库。报告只列文件名+一句话。

**自测**（全部通过再报告）：vet/test/build 三命令全绿。新增测试覆盖：Request/Resolve、未知 id 拒绝、重复 Resolve 拒绝、pending 上限、List、Await 解决路径、Close 幂等。

**完成报告**：改动文件清单、实现决策（Await 轮询设计、上限值）、测试结果、提交 hash 列表、对 M6 主 ADR 的更新说明（如有）。提交后报告即交接，不要等待确认。