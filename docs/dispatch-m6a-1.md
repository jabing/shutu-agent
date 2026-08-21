# M6a-1 实施派发消息（控制会话 → 实施会话）——schedule 接缝 + 内核

> 状态：已派发 2026-08-19（M6 能力补全六段，ADR `2026-08-19-m6-agent-full.md`；本文件为 M6a 第一半：接缝 + 内核）· 用法：把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\shutu-agent` 的 **M6a-1：`internal/schedule` 定时调度接缝（Service 定义 + 多 Provider）+ 间隔/cron 内核实现 + 单元测试**。这是 M6a 的第一半（第二半 M6a-2 做 schedule_* 工具 + schedule/* 事件 + config + 组合根接线，依赖你的接缝）。你是实施会话。

**必读（先读这些，不要通读参考源码）**：
1. `D:\dev-projects\Agent\shutu-agent\docs\decisions\2026-08-19-m6-agent-full.md` —— M6 主 ADR，重点读 M6a 行（定时调度接缝、D5 触发语义）。
2. `D:\dev-projects\Agent\shutu-agent\Agent.md` 第 10 节 D1–D10 纪律（实际是 §2 设计基线 + §5 开发纪律）+ design.md §10。
3. 现有代码（按需精读片段）：
   - `internal/jobs/service.go` + `local.go` —— **owner-fenced Registry + 生命周期 + Close 无泄漏模式（你要复制的模板）**。
   - `internal/subagent/service.go` —— 多 Provider 注册表模式。
   - `internal/session/session.go` —— Event/Seq/Append 模式（本任务只读，不加事件）。
   - `internal/store/` —— 持久化抽象（本任务判断：schedule 是否需要持久化，见下）。
   - `internal/config/config.go` —— 段模式（本任务只读，config 由 M6a-2 加）。
4. 参考源码（**只借鉴思路与契约，不照搬 TS，不精读**）：`D:\dev-projects\Agent\deepseek-harness\packages\schedule\` 的 `src/domain.ts`、`runtime.ts`、`invariant.ts`（调度域模型、回归规则、不变量）。

**实现内容**：
1. **`internal/schedule` 接缝（`service.go`）**：
   ```go
   type TriggerKind string // interval | cron
   type Schedule struct {
       ID         string
       Kind       TriggerKind
       Spec       string      // interval: "30m"/"1h30m"；cron: "0 9 * * *"
       Payload    string      // 触发时交给执行者的指令/动作文本
       Enabled    bool
       CreatedAt  time.Time
       LastFire   *time.Time
       NextFire   time.Time
   }
   type Provider interface {
       Name() string
       // List 返回当前全部调度；Create/Update/Delete 由引擎统一调用并透传。
       List(ctx context.Context) ([]Schedule, error)
       Create(ctx context.Context, s Schedule) (Schedule, error)
       Update(ctx context.Context, s Schedule) (Schedule, error)
       Delete(ctx context.Context, id string) error
   }
   type Engine interface {
       Add(ctx context.Context, kind TriggerKind, spec, payload string) (Schedule, error)
       Remove(ctx context.Context, id string) error
       List(ctx context.Context) ([]Schedule, error)
       // Tick 推进一次调度时钟：找出到期的 Enabled 调度，逐条返回其 ID（由接线方负责把到期事件落日志/入队 job——本内核不做任何日志副作用，D5 由接线方保证）。
       Tick(ctx context.Context, now time.Time) ([]string, error)
       Close() error
   }
   ```
2. **内核实现（`engine.go` + `interval.go` + `cron.go`）**：
   - `NewEngine(prov Provider) *Engine`：`Provider` 默认 `memProvider`（内存，重启丢失，v1 够用——**持久化到 store 作为可选，本任务先内存**，若你判断 store 接入成本低且不破坏接缝可做，否则留接口注释说明 M6a-2/后续再加）。
   - **interval**：Go stdlib `time.ParseDuration` 支持的格式（"30m"、"1h30m"、"24h"）；NextFire = 上次 + spec；≤0 拒绝。
   - **cron**：5 字段（分 时 日 月 周）标准 cron，**零依赖**（手写简单调度：逐分钟扫描或按字段推进；不支持秒/复杂表达式，文档注明）。无效表达式 Create 时报错。
   - `Tick`：对每个 Enabled 且 NextFire ≤ now 的调度，LastFire=now、NextFire=下一次，返回其 ID（恰好一次）。禁用/删除的不返回。
   - `Close`：无泄漏（本内核无 goroutine；若实现加了后台 ticker 则必须可 Close，建议**不加后台 ticker**——由接线方在串行路径调 Tick，D5）。
3. **测试（`internal/schedule/engine_test.go` + `interval_test.go` + `cron_test.go`）**：interval 格式解析 + NextFire 推进；cron 5 字段基本调度 + 非法表达式拒绝；Add/Remove/List；Tick 恰好一次返回到期 ID + LastFire/NextFire 更新 + 禁用不触发 + 跨越多期只触发一次；Close 幂等。

**纪律**：**本任务不落任何日志事件、不加任何工具、不加 config**（M6a-2 的事）；不改 loop turn/step（D4）；Tick 是纯推进、无副作用（D5）；零新依赖；CGO-free；原有测试全绿。**不要动**：loop、cmd/pa、config、jobs、subagent、compaction、skill、kb、store、tools、session 包（只读参考）。**不要做**：schedule_* 工具、schedule/* 事件、config、组合根接线（M6a-2）。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）；每次 Go 命令设 `$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\shutu-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\shutu-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\shutu-agent\.gocache'`。用 pwsh 执行命令。git 提交用 `git -C D:\dev-projects\Agent\shutu-agent -c user.name='Personal Agent' -c user.email='dev@shutu-agent.local' commit -m "..."`。不要提交 `pa.exe`、`data/`、缓存目录。

**上下文管理（关键）**：**分阶段提交**（service.go 接缝一次 → engine.go+interval.go+cron.go 一次 → 测试一次，信息含 "M6a-1"）；只按需精读片段，不要通读参考库；报告只列文件名 + 一句话。

**自测**（全部通过再报告）：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试覆盖：interval 解析/推进、cron 调度/非法拒绝、Add/Remove/List、Tick 恰好一次 + 禁用不触发、Close 幂等。

**完成报告**：改动文件清单、实现决策（interval/cron 实现方式、是否做 store 持久化）、测试结果、提交 hash 列表、对 M6 主 ADR 的更新说明（如有）。提交后报告，不要等待确认——报告即交接。
