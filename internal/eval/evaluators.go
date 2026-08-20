package eval

import (
	"context"
	"errors"
	"strings"
)

// criterionKind classifies one acceptance criterion by its mode prefix.
type criterionKind int

const (
	critContains criterionKind = iota // bare text or "contains:"
	critNot                           // "not:" — output must NOT contain
	critLLM                           // "llm:" — judge via LLM
	critManual                        // "manual:" — force human fallback
)

// classifyCriterion strips a recognized mode prefix ("contains:", "not:",
// "llm:", "manual:" — case-insensitive, tolerating leading whitespace) and
// returns the criterion kind plus the remainder. Bare text and unknown
// prefixes classify as critContains with the text kept verbatim.
func classifyCriterion(c string) (criterionKind, string) {
	c = strings.TrimLeft(c, " \t")
	lower := strings.ToLower(c)
	for _, p := range []struct {
		prefix string
		kind   criterionKind
	}{
		{"contains:", critContains},
		{"not:", critNot},
		{"llm:", critLLM},
		{"manual:", critManual},
	} {
		if strings.HasPrefix(lower, p.prefix) {
			return p.kind, strings.TrimSpace(c[len(p.prefix):])
		}
	}
	return critContains, c
}

// RuleEvaluator is the deterministic assertion judge (D-EVAL-3): every
// contains/not criterion is checked against the output with zero model calls.
type RuleEvaluator struct{}

// Evaluate runs only the deterministic rule assertions: contains criteria must
// appear in output, not criteria must be absent. llm:/manual: criteria are not
// rule assertions and are skipped. It returns pass when no assertion
// violates, or fail at the first violation with the offending criterion.
func (RuleEvaluator) Evaluate(ctx context.Context, output string, criteria []string) (Verdict, string, string, error) {
	for _, c := range criteria {
		kind, text := classifyCriterion(c)
		switch kind {
		case critContains:
			if !strings.Contains(output, text) {
				return VerdictFail, "criterion not satisfied: " + c, "rule", nil
			}
		case critNot:
			if strings.Contains(output, text) {
				return VerdictFail, "forbidden text present: " + c, "rule", nil
			}
		}
	}
	return VerdictPass, "rule assertions satisfied", "rule", nil
}

// LLMEvaluator judges via the injected JudgeFunc (D-EVAL-3).
type LLMEvaluator struct {
	Judge JudgeFunc
}

// Evaluate collects every llm: criterion (prefix stripped) and hands them to
// the injected JudgeFunc. With no llm criteria it passes without a model call.
func (l LLMEvaluator) Evaluate(ctx context.Context, output string, criteria []string) (Verdict, string, string, error) {
	var llmCriteria []string
	for _, c := range criteria {
		kind, text := classifyCriterion(c)
		if kind == critLLM {
			llmCriteria = append(llmCriteria, text)
		}
	}
	if len(llmCriteria) == 0 {
		return VerdictPass, "no llm criteria", "llm", nil
	}
	if l.Judge == nil {
		return VerdictManual, "no llm judge configured", "llm", errors.New("eval: llm evaluator has no judge configured")
	}
	v, reason, err := l.Judge(ctx, output, llmCriteria)
	return v, reason, "llm", err
}

// ManualEvaluator forces the human fallback via ManualFunc (D-EVAL-7).
type ManualEvaluator struct {
	Manual ManualFunc
}

// Evaluate collects every manual: criterion (prefix stripped) and hands them
// to the injected ManualFunc with an empty taskID (the Engine assigns it).
// With no manual criteria it passes without invoking the hook.
func (m ManualEvaluator) Evaluate(ctx context.Context, output string, criteria []string) (Verdict, string, string, error) {
	var manualCriteria []string
	for _, c := range criteria {
		kind, text := classifyCriterion(c)
		if kind == critManual {
			manualCriteria = append(manualCriteria, text)
		}
	}
	if len(manualCriteria) == 0 {
		return VerdictPass, "no manual criteria", "manual", nil
	}
	if m.Manual == nil {
		return VerdictManual, "no manual hook configured", "manual", errors.New("eval: manual evaluator has no hook configured")
	}
	v, reason, err := m.Manual(ctx, "", output, manualCriteria)
	return v, reason, "manual", err
}

// CompositeEvaluator runs the D-EVAL-3 orchestration: rule assertions first
// (any violation → fail), then manual criteria (any → human), then llm
// criteria (judge), with ManualFallback mapping an LLM "manual" verdict to
// manual (true) or fail (false).
type CompositeEvaluator struct {
	Rule   Evaluator
	LLM    Evaluator
	Manual Evaluator
	// ManualFallback maps an LLM "manual" verdict to manual (true) or fail (false).
	ManualFallback bool
}

// Evaluate implements the D-EVAL-3 orchestration order:
//  1. no criteria → pass (kind "rule");
//  2. rule assertions first, any violation fails immediately (kind "rule");
//  3. then manual criteria, whose hook verdict decides (kind "manual") unless
//     it passes, in which case orchestration continues;
//  4. then llm criteria, decided by the judge (kind "llm"), with an undecided
//     verdict mapped to manual or fail by ManualFallback;
//  5. otherwise (rules passed, no manual/llm criteria) the rule result stands.
func (c CompositeEvaluator) Evaluate(ctx context.Context, output string, criteria []string) (Verdict, string, string, error) {
	if len(criteria) == 0 {
		return VerdictPass, "no criteria to check", "rule", nil
	}

	hasManual, hasLLM := false, false
	for _, cr := range criteria {
		kind, _ := classifyCriterion(cr)
		switch kind {
		case critManual:
			hasManual = true
		case critLLM:
			hasLLM = true
		}
	}

	ruleV, ruleReason, _, ruleErr := c.Rule.Evaluate(ctx, output, criteria)
	if ruleErr != nil {
		return ruleV, ruleReason, "rule", ruleErr
	}
	if ruleV == VerdictFail {
		return VerdictFail, ruleReason, "rule", nil
	}

	if hasManual {
		manualV, manualReason, _, manualErr := c.Manual.Evaluate(ctx, output, criteria)
		if manualErr != nil {
			return manualV, manualReason, "manual", manualErr
		}
		if manualV != VerdictPass {
			return manualV, manualReason, "manual", nil
		}
	}

	if hasLLM {
		llmV, llmReason, _, llmErr := c.LLM.Evaluate(ctx, output, criteria)
		if llmErr != nil {
			return llmV, llmReason, "llm", llmErr
		}
		switch llmV {
		case VerdictPass:
			return VerdictPass, llmReason, "llm", nil
		case VerdictFail:
			return VerdictFail, llmReason, "llm", nil
		default: // VerdictManual
			if c.ManualFallback {
				return VerdictManual, llmReason + " (llm undecided)", "llm", nil
			}
			return VerdictFail, llmReason + " (llm undecided)", "llm", nil
		}
	}

	return ruleV, ruleReason, "rule", nil
}
