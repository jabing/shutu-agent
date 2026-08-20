// Package eval defines the task-evaluation capability seam (design.md §10 D2,
// ADR 2026-08-20-eval-seam.md D-EVAL-1/2): a Verdict model with a pluggable
// Evaluator seam. An Evaluator judges an agent deliverable (output text)
// against acceptance criteria; the seam owns the rule/LLM/manual dispatch
// (D-EVAL-3) and consumers (the eval_* tools, Eval-3) depend only on the
// Evaluator interface, never on a concrete judge backend.
package eval

import (
	"context"
	"time"
)

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
