# Eval-2a 派发：plan 验收标准（Todo.Acceptance + AddTodo + plan_todo + session 事件）

> 评测接缝 ADR `docs/decisions/2026-08-20-eval-seam.md` D-EVAL-4。本文件是 **Eval-2a** 契约：plan 域支持验收标准（Todo.Acceptance 字段 + AddTodo 带 acceptance + plan_todo 工具 + plan/create 事件载荷）。前置：Eval-1 已交付（本段不依赖）。subagent 部分在 Eval-2b。

## 纪律

- 零新依赖、CGO-free；只改 plan/session 域相关文件；gofmt；不改 loop。
- 提交 1 个：`Eval-2a: plan 验收标准（Todo.Acceptance + AddTodo + plan_todo + plan/create 事件）`

## 变更清单（精确）

### 1. internal/plan/service.go — Todo 加字段
`Todo` struct（当前 `ID/Title/Status/Details/CreatedAt/CompletedAt`）加：
```go
	// Acceptance lists the acceptance criteria this todo must satisfy when
	// evaluated (eval seam, ADR D-EVAL-4); entries may carry a mode prefix
	// (contains:/not:/llm:/manual:). Optional.
	Acceptance []string
```
且 Engine 接口 `AddTodo` 签名改为：
```go
	AddTodo(ctx context.Context, planID, title string, acceptance []string) (Todo, error)
```
（注释补一句 "acceptance is the optional eval criteria list (ADR D-EVAL-4)"）

### 2. internal/plan/engine.go — AddTodo 实现
`AddTodo(ctx, planID, title string, acceptance []string)`：构造 Todo 时 `Acceptance: append([]string(nil), acceptance...)`。

### 3. internal/plan/tools.go — plan_todo
- Schema 加：
```go
			"acceptance": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string", "minLength": 1},
				"description": "optional acceptance criteria this todo must satisfy (eval); entries may carry a mode prefix (contains:/not:/llm:/manual:)",
			},
```
- Execute：`a` struct 加 `Acceptance []string \`json:"acceptance"\``；`AddTodo(ctx, a.PlanID, a.Title, a.Acceptance)`；emit 改 `session.NewPlanCreate(string(ScopeTodo), todo.ID, todo.Title, todo.Acceptance)`。

### 4. internal/session/session.go — NewPlanCreate 带 acceptance
- `planCreateData` struct（NewPlanCreate 附近）加 `Acceptance []string \`json:"acceptance,omitempty"\``。
- `NewPlanCreate(scope, id, title string, acceptance []string)`：返回 `planCreateData{Scope, ID, Title, Acceptance: acceptance}`。

### 5. 全部调用点更新
- `internal/plan/tools.go` 190（goal）：`session.NewPlanCreate(string(ScopeGoal), g.ID, g.Title, nil)`
- 250（plan）：`session.NewPlanCreate(string(ScopePlan), p.ID, p.Title, nil)`
- `internal/plan/plan_test.go`：6 处 `e.AddTodo(ctx, X, "Y")` → `e.AddTodo(ctx, X, "Y", nil)`（92/227/312/355/364/386 行）
- `internal/session/session_test.go`：1026/1117 行 `NewPlanCreate("goal", "goal-1", "Ship...")` → 加第 4 参 `nil`。

### 6. 新增测试
- `internal/plan/plan_test.go` 加 `TestAddTodoAcceptance`：CreatePlan → AddTodo(ctx, planID, "step", []string{"contains:输出包含报告", "llm:结论合理"}) → 断言返回 Todo.Acceptance == 传入（顺序一致、非共享底层数组）；经 engine.List → GetPlan → plan.Steps[0].Acceptance 可回读。
- `internal/plan/tools_test.go`（若存在 plan_todo 工具测试）补：plan_todo 带 acceptance 调 AddTodo 的断言（无则加一个最小用例：Schema 含 acceptance 键；Execute 带 acceptance 返回 todo 含 Acceptance）。

## 验证

`go build ./...` + `go test -count=1 ./internal/plan/ ./internal/session/` 全 PASS 后提交。

## 环境

- Go：`C:\Program Files\Go\bin\go.exe`；env：`$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\shutu-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\shutu-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\shutu-agent\.gocache'`；提交身份 `-c user.name='Personal Agent' -c user.email='dev@shutu-agent.local'`；pwsh，workdir=`D:\dev-projects\Agent\shutu-agent`。
