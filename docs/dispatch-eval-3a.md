# Eval-3a 派发：config eval 段 + eval/run 事件 + eval_* 工具层

> 评测接缝 ADR `docs/decisions/2026-08-20-eval-seam.md` D-EVAL-5/6。本文件是 **Eval-3a** 契约：config `eval:` 段、session `eval/run` 事件、`internal/eval/tools.go` 工具层（eval_run/eval_result/eval_list）。前置：Eval-1（Engine/Evaluator）、Eval-2 已交付。cmd/pa 接线在 Eval-3b。

## 纪律

- 零新依赖、CGO-free；只动 internal/config、internal/session、internal/eval；gofmt；不改 loop。
- 提交 1 个：`Eval-3a: config eval 段 + eval/run 事件 + eval_* 工具层`

## 变更清单（精确）

### 1. internal/config/config.go — EvalConfig
- Config struct：`Terminal TerminalConfig` 行后加 `Eval EvalConfig \`yaml:"eval"\`  // task-evaluation seam (eval)`。
- 类型（TerminalConfig 附近）：
```go
// EvalConfig is the task-evaluation policy (ADR 2026-08-20-eval-seam.md
// D-EVAL-6). The capability is default off (D10): when Enabled is false the
// composition root registers no eval_* tools and no /eval-status command.
type EvalConfig struct {
	Enabled        bool `yaml:"enabled"`          // default false (D10)
	ManualFallback bool `yaml:"manual_fallback"`  // LLM undecided → human (true) or fail (false); default true
	MaxRecords     int  `yaml:"max_records"`      // evaluation-history cap; default 100
}
```
- 常量：`DefaultEvalManualFallback = true`、`DefaultEvalMaxRecords = 100`（const 块末尾，照 Terminal 常量）。
- applyDefaults 尾部（Terminal 块后）：
```go
	if cfg.Eval.MaxRecords <= 0 {
		cfg.Eval.MaxRecords = DefaultEvalMaxRecords
	}
	// Enabling eval whitelists its three consumer tools as well, so the
	// single eval.enabled switch turns the whole capability on; default off
	// (D10).
	if cfg.Eval.Enabled {
		for _, name := range evalToolNames {
			if !contains(cfg.Tools.Enabled, name) {
				cfg.Tools.Enabled = append(cfg.Tools.Enabled, name)
			}
		}
	}
```
- evalToolNames（terminalToolNames 后）：
```go
// evalToolNames are the task-evaluation consumer tools (ADR
// 2026-08-20-eval-seam.md D-EVAL-6). They are registered and whitelisted only
// when eval is enabled; keeping the names here makes the "eval.enabled ⇒ 工具
// 自动白名单" rule a single, tested fact shared by applyDefaults and the
// composition root.
var evalToolNames = []string{"eval_run", "eval_result", "eval_list"}
```
（注意 ManualFallback 默认 true 是布尔，applyDefaults 不覆盖显式 false——只对 Enabled/MaxRecords 做钳制；ManualFallback 用零值语义：default 需要应用。**决策**：applyDefaults 里不钳制 ManualFallback（bool 无法区分未设/false）；组合根用 `cfg.Eval.ManualFallback || true`？不——直接让 composite 用 `cfg.Eval.ManualFallback`，零值 false 会让 LLM manual→fail。为满足"默认 true"，config 解析后若 `!cfg.Eval.Enabled` 无所谓；enabled 时希望默认 true。**简化**：applyDefaults 加 `if !cfg.Eval.Enabled { /* 无需 */ }` 不适用。用另一个方式：EvalConfig 加方法 `ManualFallbackValue() bool { if cfg.Enabled { return cfg.ManualFallback } ... }` 不。**最终决策**：config.yaml 注释明确"manual_fallback 默认 true（未写时 false 生效即 fail）"不理想。改用默认值注入：applyDefaults 无法区分。**最简正确方案**：EvalConfig 保持 bool，组合根读 `cfg.Eval.ManualFallback || cfg.Eval.Enabled`？错。**就用指针或默认常量判断**：不引入复杂度——**决定：manual_fallback 的"默认 true"由 applyDefaults 用 `if !cfg.Eval.ManualFallback { cfg.Eval.ManualFallback = DefaultEvalManualFallback }` 但这样显式 false 也会被翻成 true！** 这是 bool 默认值的经典问题。**最终**：加一个 sentinel——用 `*bool`？config 结构用 `ManualFallback *bool` 太多侵入。**务实**：默认 false 也能满足 D10 精神（LLM manual → fail），但 ADR 说默认 true。**折中**：config.yaml 文档写"manual_fallback: true  # 默认 true"；applyDefaults 不钳制；组合根用 `manualFallback := cfg.Eval.ManualFallback; if cfg.Eval.Enabled && !cfg.Eval.ManualFallback { manualFallback = true }`？这也把显式 false 翻成 true。**接受**：提供 `EvalConfig.ManualFallbackValue() bool`——`return cfg.ManualFallback || !cfg.Enabled`？不行。**换思路**：默认 true 通过 config.yaml 示例呈现 + 组合根 `composite.ManualFallback = cfg.Eval.ManualFallback`（默认 false → fail）。并在 config 测试断言默认（Enabled false 时 ManualFallback false）。**接受这个简化并记录**：manual_fallback 默认 false（组合根用它），config.yaml 文档示例写 true 推荐。**等等**——最干净：`applyDefaults` 里 `if !cfg.Eval.Enabled { cfg.Eval.ManualFallback = DefaultEvalManualFallback }` 不对。**最终拍板**：ManualFallback 用三态枚举？过重。**用 DefaultEvalManualFallback 常量 + 组合根 `manualFallback := cfg.Eval.ManualFallback || !cfg.Eval.Enabled`？** 不对。**放弃纠结**：`applyDefaults` 设 `if !cfg.Eval.Enabled && !cfg.Eval.ManualFallback { cfg.Eval.ManualFallback = DefaultEvalManualFallback }`——enabled=false 时默认 true（无意义，因为工具不注册）；enabled=true 时用户写 false 则 fail、不写则 false→fail。**这仍不符合"默认 true"**。**最终决定（写进契约）**：EvalConfig 用 `ManualFallback *bool`？不，侵入太多。**就按 applyDefaults 模式的其他 bool 处理（KB.Enabled 等无默认值问题）**：EvalConfig 加方法 `ManualFallbackValue() bool { return cfg.ManualFallback || !cfg.Enabled }`——当 eval 关闭时返回 true（无意义），开启时默认 false→false。**还是不符合**。
    **停止**：契约最终方案 = `ManualFallback *bool`（yaml 指针，nil→默认 true）。这是唯一正确表达"默认 true 且可显式 false"的方式，Go yaml 支持指针。改动：EvalConfig{Enabled bool, ManualFallback *bool `yaml:"manual_fallback"`, MaxRecords int}；applyDefaults：`if cfg.Eval.ManualFallback == nil { v := true; cfg.Eval.ManualFallback = &v }`；组合根 `manualFallback := *cfg.Eval.ManualFallback`。config.yaml 示例注释。测试：默认 nil → applyDefaults 后 *true；显式 false → 保持 false。

### 2. internal/session/session.go — eval/run 事件
- 事件常量（terminal 块后）：
```go
	// M-eval evaluation events (ADR 2026-08-20-eval-seam.md D-EVAL-5): eval/run
	// lands when an evaluation completes. It is log-only (D3): the model sees
	// the deliverable and verdict through the eval_* tools' tool/result events,
	// and DeriveHistory treats these types as opaque data. The payload is a
	// lean summary — never the deliverable output (D-EVAL-5).
	EventEvalRun = "eval/run"
```
- payload 类型 + 构造函数（terminal 事件附近）：
```go
// evalRunData is the eval/run payload (D-EVAL-5): a lean summary only.
type evalRunData struct {
	ID            string `json:"id"`
	TaskID        string `json:"taskId,omitempty"`
	Verdict       string `json:"verdict"`
	Reason        string `json:"reason,omitempty"`
	EvaluatorKind string `json:"evaluatorKind,omitempty"`
	CriteriaCount int    `json:"criteriaCount"`
}

// NewEvalRun builds the eval/run payload (D-EVAL-5).
func NewEvalRun(id, taskID, verdict, reason, kind string, criteriaCount int) any {
	return evalRunData{ID: id, TaskID: taskID, Verdict: verdict, Reason: reason, EvaluatorKind: kind, CriteriaCount: criteriaCount}
}
```

### 3. internal/eval/tools.go — eval_* 工具层（照 internal/jobs/tools.go 模式）
```go
// tools.go — the Eval-3a Consumer half of the eval seam (ADR D-EVAL-5):
// eval_run, eval_result and eval_list are registered into the tools.Registry by
// the composition root (cmd/pa) when eval.enabled, and auto-whitelisted by
// config.applyDefaults the same way the job_* tools are. They implement the
// tools.Tool method set structurally (Go structural typing), so this package
// never imports the tools package — the seam stays decoupled. D7 is enforced by
// the registry. D3 event logging lives here: eval_run emits eval/run.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"personal-agent/internal/session"
)

// Tool names (whitelisted when eval.enabled; see config.evalToolNames).
const (
	ToolRunName    = "eval_run"
	ToolResultName = "eval_result"
	ToolListName   = "eval_list"
)

// listDefaultLimit is eval_list's page size when limit is absent.
const listDefaultLimit = 20

// EvalTools bundles the shared state of the three eval_* tools.
type EvalTools struct {
	eng     Engine
	onEvent func(typ string, data any)
}

// NewEvalTools returns the shared eval-tool bundle bound to an Engine. onEvent,
// when non-nil, receives the eval/* event payloads; the composition root wires
// it to the session log (D3).
func NewEvalTools(eng Engine, onEvent func(typ string, data any)) *EvalTools {
	return &EvalTools{eng: eng, onEvent: onEvent}
}

func (t *EvalTools) Run() EvalRunTool     { return EvalRunTool{t: t} }
func (t *EvalTools) Result() EvalResultTool { return EvalResultTool{t: t} }
func (t *EvalTools) List() EvalListTool   { return EvalListTool{t: t} }

func (t *EvalTools) emit(typ string, data any) {
	if t.onEvent != nil {
		t.onEvent(typ, data)
	}
}
```
- **EvalRunTool**（Name=eval_run）：Schema `{type:object, properties:{task_id:{type:string,minLength:1,description:"the subagent id or plan todo id being evaluated"}, output:{type:string,minLength:1,description:"the deliverable output text to judge"}, criteria:{type:array,items:{type:string,minLength:1},description:"acceptance criteria; entries may carry a mode prefix (contains:/not:/llm:/manual:)"}}, required:[output,criteria], additionalProperties:false}`。Execute：unmarshal → 空 output/criteria 拒绝 → `rec, err := t.t.eng.Evaluate(ctx, a.TaskID, a.Output, a.Criteria)` → err → fmt.Errorf("eval_run: %w") → `t.t.emit(session.EventEvalRun, session.NewEvalRun(rec.ID, rec.TaskID, string(rec.Verdict), rec.Reason, rec.EvaluatorKind, len(rec.Criteria)))` → 返回 formatRecord(rec)。
- **EvalResultTool**（Name=eval_result）：Schema `{type:object, properties:{id:{type:string,minLength:1}}, required:[id], additionalProperties:false}`。Execute：`rec, err := t.t.eng.Get(ctx, a.ID)` → formatRecord。
- **EvalListTool**（Name=eval_list）：Schema `{type:object, properties:{limit:{type:integer,minimum:1,description:"max records (default 20)"}}, additionalProperties:false}`。Execute：`recs, err := t.t.eng.List(ctx)` → 空 → "no evaluation records yet"；取前 limit 条 → formatRecords。
- 格式化辅助：
```go
func formatRecord(rec EvalRecord) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "eval %s: %s (kind=%s)\n", rec.ID, rec.Verdict, rec.EvaluatorKind)
	if rec.TaskID != "" {
		fmt.Fprintf(&sb, "  task: %s\n", rec.TaskID)
	}
	if rec.Reason != "" {
		fmt.Fprintf(&sb, "  reason: %s\n", rec.Reason)
	}
	fmt.Fprintf(&sb, "  criteria: %d\n", len(rec.Criteria))
	return strings.TrimSuffix(sb.String(), "\n")
}

func formatRecords(recs []EvalRecord, limit int) string {
	if len(recs) == 0 {
		return "no evaluation records yet"
	}
	if limit <= 0 || limit > len(recs) {
		limit = len(recs)
	}
	var b strings.Builder
	for i, r := range recs[:limit] {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(formatRecord(r))
	}
	return b.String()
}
```

### 4. 测试
- config_test.go：`TestEvalDefaults`（零值 → MaxRecords 100、ManualFallback 非 nil 且 *true、Enabled 默认 false）；`TestEvalEnabledWhitelists`（false → 不含 eval_*；true → 3 个 eval_* 全进白名单）；`TestEvalManualFallbackExplicitFalse`（ManualFallback 指向 false → 保持 false）。
- session_test.go：eval/run 事件 Append + DeriveHistory 不回归（照 job 事件测试模式，可选最小断言：Append(EventEvalRun, NewEvalRun(...)) 成功且回读类型/字段）。
- internal/eval/tools_test.go：`TestEvalToolsRun`（mock Engine——用真 Engine + mock Evaluator？直接构造 evalEngine + mock Evaluator，经 NewEvalTools 走 eval_run：断言返回含 verdict、事件 emit 捕获 session.EventEvalRun + payload 字段）；`TestEvalToolsResult`/`TestEvalToolsList`（formatRecord/formatRecords 断言 + 空列表）；`TestEvalToolsEmptyInput`（空 output/criteria → error）。

## 验证

`go build ./...` + `go test -count=1 ./internal/config/ ./internal/session/ ./internal/eval/` 全 PASS 后提交。

## 环境

- Go：`C:\Program Files\Go\bin\go.exe`；env：`$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\personal-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\personal-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\personal-agent\.gocache'`；提交身份 `-c user.name='Personal Agent' -c user.email='dev@personal-agent.local'`；pwsh，workdir=`D:\dev-projects\Agent\personal-agent`。
