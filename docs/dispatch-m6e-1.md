# M6e-1 实施派发消息（控制会话 → 实施会话）——code 接缝 + 本地子进程沙箱实现

> 状态：已派发 2026-08-19（M6 能力补全六段，ADR `2026-08-19-m6-agent-full.md`；本文件为 M6e 第一半：接缝 + 本地沙箱实现）· 用法：把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\personal-agent` 的 **M6e-1：`internal/code` 代码沙箱接缝（Service 定义 + 多 Provider）+ 本地子进程沙箱实现 + 单元测试**。这是 M6e 的第一半（第二半 M6e-2 做 code_run 工具 + code/* 事件 + config + 组合根接线，依赖你的接缝）。你是实施会话。

**直接开工，不要做任何前置检查**：不要跑 baseline check、不要验证环境。你的主要输入是 `D:\dev-projects\Agent\personal-agent\docs\dispatch-m6e-1.md`（本文件即主契约），**先完整读它**，然后立即用 write 工具创建文件。

**读这些（按需精读片段，不要通读）**：
1. `D:\dev-projects\Agent\personal-agent\docs\decisions\2026-08-19-m6-agent-full.md` —— M6 主 ADR，重点读 M6e 行（代码沙箱；**受控隔离** = 进程边界 + 超时 + 配额 + 默认无网络；强隔离 e2b 云端不做）。
2. `internal/schedule/service.go` + `internal/interact/service.go` —— 接缝模板（Provider/Engine + 哨兵错误 + Close 幂等）。
3. `internal/tools/run_command.go` —— M3 run_command 现有实现（补强对象；参考其 exec/超时/输出截断写法）。
4. 参考（只借鉴思路，不精读）：`D:\dev-projects\Agent\deepseek-harness\packages\code-runtime\code-runtime\src\types.ts`（沙箱接口形状）；`e2b\subprocess-e2b\src\process.ts`（子进程模型）。

**实现内容**：
1. **`internal/code` 接缝（`service.go`）**：
   ```go
   type Result struct {
       ExitCode  int
       Stdout    string   // 有界（输出配额内截断，截断处标记）
       Stderr    string
       TimedOut  bool
       Truncated bool
       Duration  time.Duration
   }
   type Provider interface {
       Name() string
       // Run 在沙箱中执行代码/命令（受控隔离：进程边界 + 超时 + 输出配额）。
       // v1 本地实现：os/exec 子进程 + context 超时硬杀 + stdout/stderr 配额截断。
       Run(ctx context.Context, req RunRequest) (Result, error)
       Close() error
   }
   type Engine interface {
       // Run 经默认 Provider 执行；provider 未设置时用本地子进程沙箱。
       Run(ctx context.Context, req RunRequest) (Result, error)
       Close() error
   }
   type RunRequest struct {
       Lang    string   // "sh"（默认）| 后续可扩展
       Code    string   // 要执行的命令/脚本
       Cwd     string   // 沙箱工作目录（默认 <project>/.sandbox）
       Timeout time.Duration // 0 → 默认 30s
       MaxOutput int    // 每流配额字节；0 → 默认 64KB
   }
   ```
   - **受控隔离语义（ADR M6e）**：进程边界（独立子进程）+ 超时（CommandContext + 硬 kill，TimedOut 标记）+ 输出配额（Stdout/Stderr 各 ≤ MaxOutput，超限截断并 Truncated 标记）+ 沙箱 cwd（独立目录，默认 `<cwd 基准>/.sandbox`，执行前若不存在则创建）+ **默认无网络：声明性开关**（v1 本地实现不注入任何网络凭据；Windows 无网络 namespace，强网络隔离不做——在包注释与 config 中如实记录此边界）。
   - 默认 Provider：`localProvider`（`NewLocalProvider()`，零依赖 os/exec + context）。
   - Close 幂等。
2. **测试（`internal/code/code_test.go`）**：Run 成功（echo 退出码 0 + 输出）、失败（非零退出码 + stderr）、超时（sleep 超长 → TimedOut + 硬杀）、输出配额（大输出 → Truncated）、cwd 生效（pwd 落在沙箱目录）、Close 幂等。

**纪律**：**本任务不落任何日志事件、不加任何工具、不加 config**（M6e-2 的事）；不改 loop turn/step（D4）；子进程执行是**前台串行**（Run 同步返回，无后台 goroutine；D5）；零新依赖（os/exec 是标准库）；CGO-free；原有测试全绿。**不要动**：loop、cmd/pa、config、jobs、subagent、compaction、skill、kb、store、tools、session（只读参考）、schedule、plan、spill、interact 包（只读参考）。**不要做**：code_run 工具、code/* 事件、config、组合根接线（M6e-2）。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）。每次 Go 命令这样跑（用 pwsh）：
`$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\personal-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\personal-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\personal-agent\.gocache'; & 'C:\Program Files\Go\bin\go.exe' test ./...`
git 提交：`git -C D:\dev-projects\Agent\personal-agent -c user.name='Personal Agent' -c user.email='dev@personal-agent.local' commit -m "M6e-1: <what>"`。不要提交 pa.exe/data/缓存。

**上下文管理（关键）**：**分阶段提交**——① service.go 接缝 + local.go → ② 测试，每阶段一次 commit（信息含 "M6e-1"）。不要通读任何参考库。报告只列文件名+一句话。

**自测**（全部通过再报告）：vet/test/build 三命令全绿。新增测试覆盖：Run 成功/失败/超时硬杀/输出配额截断/cwd 生效/Close 幂等。

**完成报告**：改动文件清单、实现决策（受控隔离边界、超时硬杀方式、配额截断、网络隔离声明性处理）、测试结果、提交 hash 列表、对 M6 主 ADR 的更新说明（如有）。提交后报告即交接，不要等待确认。