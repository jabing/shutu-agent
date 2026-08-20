# Eval-1a 派发：internal/eval evaluator 层（Evaluator 接口 + 四实现 + 测试）

> 评测接缝 ADR `docs/decisions/2026-08-20-eval-seam.md`。本文件是 **Eval-1a** 契约：`internal/eval` 包的**类型与评测器实现**（不含 Engine/Provider，不含 config/事件/工具——那些在 Eval-1b/Eval-2/Eval-3）。前置：无（新包）。

## 纪律

- 零新依赖、CGO-free；不 import internal/llm / internal/interact（评测器只吃注入的函数，D2 seam）；不改任何现有文件；gofmt。
- 提交 1 个：`Eval-1a: internal/eval evaluator 层（Evaluator 接口 + rule/llm/manual/composite 四实现）`

## 交付文件

1. `internal/eval/eval.go` — 类型与接口
2. `internal/eval/evaluators.go` — 四实现
3. `internal/eval/evaluators_test.go` — 测试

## 契约

### eval.go

```go
// Package eval defines the task-evaluation capability seam (design.md §10 D2,
// ADR 2026-08-20-eval-seam.md D-EVAL-1/2): a Verdict model with a pluggable
// Evaluator seam. An Evaluator judges an agent deliverable (output text)
// against acceptance criteria; the seam owns the rule/LLM/manual dispatch
// (D-EVAL-3) and consumers (the eval_* tools, Eval-3) depend only on the
// Evaluator interface, never on a concrete judge backend.
package eval

// Verdict is the outcome of one evaluation.
type Verdict string

const (
	// VerdictPass means the deliverable satisfies the criteria.
	VerdictPass Verdict = "pass"
	// VerdictFail means the deliverable violates a criterion.
	VerdictFail Verdict = "fail"
	// VerdictManual means the criteria could not be decided automatically.
	VerdictManual Verdict = "manual"
)

// EvalRecord is one stored evaluation outcome (D-EVAL-1).
type EvalRecord struct {
	ID            string   // provider-issued id ("eval-N")
	TaskID        string   // subagent id or plan todo id being evaluated
	Criteria      []string // acceptance criteria (verbatim)
	Output        string   // bounded deliverable summary (≤ 4000 runes)
	Verdict       Verdict
	Reason        string // human/model-facing justification
	EvaluatorKind string // "rule" | "llm" | "manual" — the kind that decided
	CreatedAt     time.Time
}

// JudgeFunc is an LLM judge (D-EVAL-3): judge whether output satisfies the
// llm-prefixed criteria, returning a verdict and a one-line reason. The
// composition root adapts the configured llm.LLM to this function (Eval-3);
// the seam never imports the llm package (D2).
type JudgeFunc func(ctx context.Context, output string, llmCriteria []string) (Verdict, string, error)

// ManualFunc is the human-fallback hook (D-EVAL-7): create a human approval
// for the undecidable criteria and block until resolved, returning the mapped
// verdict (approved→pass, rejected→fail, otherwise manual). The composition
// root adapts the interact.Engine to this function (Eval-3).
type ManualFunc func(ctx context.Context, taskID, output string, manualCriteria []string) (Verdict, string, error)

// Evaluator judges a deliverable. EvalRecord.EvaluatorKind should report which
// kind decided (rule/llm/manual).
type Evaluator interface {
	Evaluate(ctx context.Context, output string, criteria []string) (Verdict, string, string, error)
	//   ^ returns (verdict, reason, kind, err)
}
```

### evaluators.go（四实现，每个一个 struct）

```go
// criterionKind classifies one acceptance criterion by its mode prefix.
type criterionKind int

const (
	critContains criterionKind = iota // bare text or "contains:"
	critNot                           // "not:" — output must NOT contain
	critLLM                           // "llm:" — judge via LLM
	critManual                        // "manual:" — force human fallback
)

func classifyCriterion(c string) (criterionKind, string)
// 实现：去掉前缀（"contains:"/"not:"/"llm:"/"manual:"，大小写不敏感、可带前导空白）；无前缀/未知前缀 → critContains。

// RuleEvaluator is the deterministic assertion judge (D-EVAL-3): every
// contains/not criterion is checked against the output with zero model calls.
type RuleEvaluator struct{}

func (RuleEvaluator) Evaluate(ctx context.Context, output string, criteria []string) (Verdict, string, string, error)
// 逐条 classify：critContains → output 必须 strings.Contains（不包含 → 返回 fail + 理由 "criterion not satisfied: <原文>"）；critNot → output 必须不含（含 → fail + "forbidden text present: <原文>"）；critLLM/critManual → 跳过（不属于规则断言）。全部规则断言通过且 criteria 中无任何 llm/manual 条目 → pass（理由 "all rule assertions satisfied"）；若存在 llm/manual 条目但规则断言全过 → 返回 verdict=pass 由调用方忽略？——不，规则评测器只处理规则部分：若无违例但存在 llm/manual 条目，返回 (VerdictPass, "rule assertions satisfied", "rule", nil) 并让 Composite 决定是否继续（见下）。为简单：RuleEvaluator 只做自己的部分，逐条规则断言，返回 pass（无违例）或 fail（首个违例）。

// LLMEvaluator judges via the injected JudgeFunc (D-EVAL-3).
type LLMEvaluator struct {
	Judge JudgeFunc
}

func (l LLMEvaluator) Evaluate(ctx context.Context, output string, criteria []string) (Verdict, string, string, error)
// 收集全部 critLLM 条目的原文（strip 前缀）；空 → (pass, "no llm criteria", "llm", nil)。否则 Judge(ctx, output, llmCriteria) → 返回 (v, reason, "llm", err)。

// ManualEvaluator forces the human fallback via ManualFunc (D-EVAL-7).
type ManualEvaluator struct {
	Manual ManualFunc
}

func (m ManualEvaluator) Evaluate(ctx context.Context, output string, criteria []string) (Verdict, string, string, error)
// 收集全部 critManual 条目原文；空 → (pass, "no manual criteria", "manual", nil)。否则 Manual(ctx, taskID="", output, manualCriteria) → (v, reason, "manual", err)。

// CompositeEvaluator runs the D-EVAL-3 orchestration: rule assertions first
// (any violation → fail), then manual criteria (any → human), then llm
// criteria (judge), with manualFallback mapping an LLM "manual" verdict to
// manual (true) or fail (false).
type CompositeEvaluator struct {
	Rule   Evaluator
	LLM    Evaluator
	Manual Evaluator
	// ManualFallback maps an LLM "manual" verdict to manual (true) or fail (false).
	ManualFallback bool
}

func (c CompositeEvaluator) Evaluate(ctx context.Context, output string, criteria []string) (Verdict, string, string, error)
// 编排：
//  1. 无任何 criteria → (pass, "no criteria to check", "rule", nil)。
//  2. ruleV, ruleReason, _, ruleErr := c.Rule.Evaluate(output, criteria)；ruleErr → err；ruleV == fail → (fail, ruleReason, "rule", nil)。
//  3. 若 criteria 含 critManual 条目：manualV, manualReason, _, err := c.Manual.Evaluate(...)；err → err；manualV != pass（即 manual 或 fail）→ (manualV, manualReason, "manual", nil)。manual 全过但…… manualV==pass 表示无 manual 条目（ManualEvaluator 空输入返回 pass）→ 继续。
//  4. 若 criteria 含 critLLM 条目：llmV, llmReason, _, err := c.LLM.Evaluate(...)；err → err；llmV == pass → (pass, llmReason, "llm", nil)；llmV == fail → (fail, llmReason, "llm", nil)；llmV == manual → ManualFallback ? (manual, llmReason+" (llm undecided)", "llm", nil) : (fail, llmReason+" (llm undecided)", "llm", nil)。
//  5. 否则（规则全过、无 manual/llm 条目）→ (ruleV, ruleReason, "rule", nil)（ruleV 应为 pass）。
```

### evaluators_test.go

- `TestClassifyCriterion`：各前缀映射（裸文本→contains、contains:/not:/llm:/manual: 前缀剥离、未知前缀→contains、大小写不敏感、前导空白）。
- `TestRuleEvaluator`：contains 命中→pass；contains 未命中→fail（理由含原文）；not 命中→fail；not 未命中→pass；混合含 llm/manual 条目时规则断言部分正确（违例仍 fail，无违例 pass）。
- `TestLLMEvaluator`：Judge mock 返回 (pass)/ (fail)/ (manual) → 正确映射；kind=="llm"；无 llm 条目→(pass,"no llm criteria")；Judge 返回 error → 透传。
- `TestManualEvaluator`：Manual mock 返回 (pass)/ (fail)/ (manual) → 正确；kind=="manual"；无 manual 条目→(pass)。
- `TestCompositeEvaluator` 表驱动：
  - 空 criteria → pass。
  - 仅规则且全过 → pass(kind=rule)。
  - 规则违例 → fail(kind=rule)（短路，不调 LLM/Manual mock——断言 mock 未被调用）。
  - contains 全过 + 一个 llm 条目 → LLM judge pass → pass(kind=llm)。
  - 同上 LLM fail → fail(kind=llm)。
  - 同上 LLM manual + ManualFallback=true → manual；=false → fail。
  - 含 manual 条目 → Manual 决定（Manual 返回 fail → fail(kind=manual)；返回 pass（无 manual 条目情形）→ 继续走 llm）。
  - 混合 contains+llm 全过 → 最终由 llm 决定。

## 验证

`go build ./...` + `go test -count=1 ./internal/eval/ -v` 全 PASS 后提交。

## 环境

- Go：`C:\Program Files\Go\bin\go.exe`；env：`$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\personal-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\personal-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\personal-agent\.gocache'`；提交身份 `-c user.name='Personal Agent' -c user.email='dev@personal-agent.local'`；pwsh，workdir=`D:\dev-projects\Agent\personal-agent`。
