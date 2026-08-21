# M6b-1 实施派发消息（控制会话 → 实施会话）——plan 接缝 + 域模型

> 状态：已派发 2026-08-19（M6 能力补全六段，ADR `2026-08-19-m6-agent-full.md`；本文件为 M6b 第一半：接缝 + 域模型）· 用法：把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\shutu-agent` 的 **M6b-1：`internal/plan` 任务规划接缝（Service 定义 + 多 Provider）+ goal→plan→todo 域模型 + 单元测试**。这是 M6b 的第一半（第二半 M6b-2 做 plan_* 工具 + plan/* 事件 + config + 组合根接线，依赖你的接缝）。你是实施会话。

**直接开工，不要做任何前置检查**：不要跑 baseline check、不要验证环境。你的主要输入是 `D:\dev-projects\Agent\shutu-agent\docs\dispatch-m6b-1.md`（本文件即主契约），**先完整读它**，然后立即用 write 工具创建文件。

**读这些（按需精读片段，不要通读）**：
1. `D:\dev-projects\Agent\shutu-agent\docs\decisions\2026-08-19-m6-agent-full.md` —— M6 主 ADR，重点读 M6b 行。
2. `internal/schedule/service.go` —— 接缝模板（TriggerKind/Schedule/Provider/Engine + 哨兵错误 + Close 幂等模式）。
3. `internal/jobs/service.go` + `local.go` —— owner-fenced Registry + 生命周期模板。
4. `internal/session/session.go` —— 只读，不加事件。
5. 参考（只借鉴思路，不精读）：`D:\dev-projects\Agent\deepseek-harness\packages\goal\goal\src\domain.ts`、`fold.ts`；`plan\plan-mode\src\types.ts`；`todo\tool-todo\src\types.ts`。

**实现内容**：
1. **`internal/plan` 接缝（`service.go`）**——三层域模型（goal → plan → todo）：
   ```go
   type Status string // pending | in-progress | blocked | done | cancelled
   type Todo struct {
       ID          string
       Title       string
       Status      Status
       Details     string
       CreatedAt   time.Time
       CompletedAt *time.Time
   }
   type Plan struct {
       ID         string
       Title      string
       GoalID     string   // 所属 goal；"" = 独立 plan
       Status     Status
       Steps      []Todo    // 有序步骤
       CreatedAt  time.Time
   }
   type Goal struct {
       ID          string
       Title       string
       Objective   string   // 目标描述（一段话）
       Status      Status
       Plans       []string  // plan id 列表（有向无环，goal 聚合下）
       Owner       string    // 归属会话（可选）
       CreatedAt   time.Time
       CompletedAt *time.Time
   }
   type Provider interface {
       Name() string
       ListGoals(ctx context.Context) ([]Goal, error)
       GetGoal(ctx context.Context, id string) (Goal, error)
       PutGoal(ctx context.Context, g Goal) error        // 幂等 upsert
       DeleteGoal(ctx context.Context, id string) error  // 连带删除其 plans/todos
       ListPlans(ctx context.Context) ([]Plan, error)
       GetPlan(ctx context.Context, id string) (Plan, error)
       PutPlan(ctx context.Context, p Plan) error
       DeletePlan(ctx context.Context, id string) error
   }
   type Engine interface {
       CreateGoal(ctx context.Context, title, objective string) (Goal, error)
       CreatePlan(ctx context.Context, goalID, title string, steps []string) (Plan, error)
       AddTodo(ctx context.Context, planID, title string) (Todo, error)
       SetStatus(ctx context.Context, scope string, id string, st Status) error  // scope: goal|plan|todo
       List(ctx context.Context) ([]Goal, error)   // 聚合树（goal→plans→todos）
       Remove(ctx context.Context, scope string, id string) error
       Close() error
   }
   ```
   - 校验：Status 非法值拒绝；goalID 未知时 CreatePlan 拒绝；planID 未知时 AddTodo/SetStatus 拒绝；Remove 级联（删 goal 连删其 plans）。
   - 默认 Provider：内存（`memProvider`，重启丢失；store 持久化留接口注释说明后续可加）。
   - Close 幂等无泄漏（无 goroutine）。
2. **测试（`internal/plan/plan_test.go`）**：CreateGoal/CreatePlan/AddTodo；SetStatus 校验 + 未知 id 拒绝；List 聚合树（goal→plans→todos 排序）；Remove 级联；Close 幂等；非法 Status 拒绝。

**纪律**：**本任务不落任何日志事件、不加任何工具、不加 config**（M6b-2 的事）；不改 loop turn/step（D4）；零新依赖；CGO-free；原有测试全绿。**不要动**：loop、cmd/pa、config、jobs、subagent、compaction、skill、kb、store、tools、session、schedule 包（只读参考）。**不要做**：plan_* 工具、plan/* 事件、config、组合根接线（M6b-2）。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）。每次 Go 命令这样跑（用 pwsh）：
`$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\shutu-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\shutu-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\shutu-agent\.gocache'; & 'C:\Program Files\Go\bin\go.exe' test ./...`
git 提交：`git -C D:\dev-projects\Agent\shutu-agent -c user.name='Personal Agent' -c user.email='dev@shutu-agent.local' commit -m "M6b-1: <what>"`。不要提交 pa.exe/data/缓存。

**上下文管理（关键）**：**分阶段提交**——① service.go 接缝 + mem.go → ② 测试，每阶段一次 commit（信息含 "M6b-1"）。不要通读任何参考库。报告只列文件名+一句话。

**自测**（全部通过再报告）：vet/test/build 三命令全绿。新增测试覆盖：CreateGoal/CreatePlan/AddTodo、SetStatus 校验 + 未知 id、List 聚合树、Remove 级联、Close 幂等、非法 Status。

**完成报告**：改动文件清单、实现决策（域模型取舍、级联删除语义）、测试结果、提交 hash 列表、对 M6 主 ADR 的更新说明（如有）。提交后报告即交接，不要等待确认。