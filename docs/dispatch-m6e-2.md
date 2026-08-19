# M6e-2 实施派发消息（控制会话 → 实施会话）——code_run 工具 + 事件 + config + 接线

> 状态：待 M6e-1 验收后派发 2026-08-19（M6 能力补全六段，ADR `2026-08-19-m6-agent-full.md`；本文件为 M6e 第二半：工具 + 事件 + config + 接线）· 用法：M6e-1 验收通过后，把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\personal-agent` 的 **M6e-2：`code_run` 工具 + `code/*` 事件 + config + 组合根接线 + 单元测试**。这是 M6e 的第二半（第一半 M6e-1 已做 `internal/code` 接缝 + 本地子进程沙箱，你**依赖它们**）。你是实施会话。

**直接开工，不要做任何前置检查**。你的主要输入是 `D:\dev-projects\Agent\personal-agent\docs\dispatch-m6e-2.md`（本文件即主契约），**先完整读它**，然后立即用 write 工具创建/修改文件。

**读这些（按需精读片段，不要通读）**：
1. `D:\dev-projects\Agent\personal-agent\docs\dispatch-m6e-1.md` —— 接缝契约（Engine/Result/RunRequest 签名、受控隔离语义、网络声明性边界）。
2. `D:\dev-projects\Agent\personal-agent\docs\decisions\2026-08-19-m6-agent-full.md` —— M6 主 ADR（M6e 行）。
3. 现有代码（按需精读）：
   - `internal/code/service.go` + `local.go`（M6e-1 已做）。
   - `internal/session/session.go` —— 各能力事件的 log-only 模式（模板）。
   - `internal/config/config.go` —— 各段模式 + applyDefaults 白名单（job_*/subagent_*/schedule_*/plan_*/spill_*/interact_*）。
   - `cmd/pa/*.go` —— register* 组合根模式、工具注册、onEvent sink、命令注册、/help 状态行。
   - `internal/tools/run_command.go` —— M3 run_command（M6e 补强对象；可选：若语义重叠可保留 run_command，code_run 作为受控沙箱版并存）。
   - `internal/tools/` —— tools.Tool + D7。
4. 参考（只借鉴思路，不精读）：`D:\dev-projects\Agent\deepseek-harness\packages\code-runtime\code-runtime\src\index.ts`。

**实现内容**：
1. **事件（`internal/session/session.go` + 测试）**：新增 `EventCodeRun`（`code/run`）+ `NewCodeRun(lang string, exitCode int, timedOut, truncated bool) any`。**log-only**：DeriveHistory 不派生。
2. **config（`internal/config` + config.yaml）**：`CodeConfig{Enabled bool, Timeout time.Duration, MaxOutput int, SandboxDir string, AllowNetwork bool}`（yaml: `enabled/timeout/max_output/sandbox_dir/allow_network`）；默认 `enabled:false / timeout:30s / max_output:65536 / sandbox_dir:""（默认 <项目>/.sandbox）/ allow_network:false`。**enabled 时自动白名单 `code_run` 工具**（与 M3 run_command 白名单并列；若 run_command 语义被 code_run 取代则说明取舍）。
3. **code_run 工具（`internal/code/tools.go` + 测试）**：`NewCodeTools(e Engine, onEvent func(typ string, data any))` 返回结构化 tools.Tool（不 import tools 包，D2）：
   - `code_run(lang, code, timeout?, cwd?)`：受控沙箱执行；D7 schema（additionalProperties:false；lang enum: ["sh"]，timeout 数值秒，cwd 可选）；返回 Result（退出码/输出/超时/截断标记）；落 `code/run`。
   - 事件经 onEvent sink（串行工具路径）；超时/非零退出码返回给模型（非 panic）。
4. **组合根（cmd/pa `registerCode()`）**：`code.enabled` 时创建本地沙箱 Provider + Engine + 注册 code_run 工具（白名单）+ 事件 sink；disabled 零操作（D10）。`main.go` 调用 + `app.code` 字段 + deferred Close + /help 状态行。**接线位置在工具注册层，不改 loop（D4）**；code_run 执行是前台串行（D5，无后台 goroutine）。

**纪律**：**日志仍追加式（D1）**；不改 loop turn/step（D4）；串行路径（D5）；零新依赖（os/exec 标准库）；CGO-free；原有测试全绿。**不要动**：`internal/code/service.go`/`local.go`（M6e-1 已验收，只读；tools.go 新建）、loop.go（只读，不要改）、compaction、subagent、skill、kb、store、schedule、plan、spill、interact 包（只读参考）。**不要做**：M6f、KB 补全、强隔离（e2b 云端）。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）。每次 Go 命令这样跑（用 pwsh）：
`$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\personal-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\personal-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\personal-agent\.gocache'; & 'C:\Program Files\Go\bin\go.exe' test ./...`
git 提交：`git -C D:\dev-projects\Agent\personal-agent -c user.name='Personal Agent' -c user.email='dev@personal-agent.local' commit -m "M6e-2: <what>"`。不要提交 pa.exe/data/缓存。

**上下文管理（关键）**：**分阶段提交**——① session 事件 → ② config → ③ tools → ④ 组合根，每阶段一次 commit（信息含 "M6e-2"）。不要通读任何参考库。报告只列文件名+一句话。

**自测**（全部通过再报告）：vet/test/build 三命令全绿。新增测试至少覆盖：事件追加/重放/不派生、config 缺省 + 白名单、code_run（D7 + 事件 + 超时/截断/非零退出返回给模型）、enabled=false 不注册。

**完成报告**：改动文件清单、实现决策（code_run 与 run_command 并存或取代、配置缺省）、测试结果、提交 hash 列表、对 M6 主 ADR 的更新说明（如有）。提交后报告即交接，不要等待确认。