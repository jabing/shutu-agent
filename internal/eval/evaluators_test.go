package eval

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Compile-time assertions that all four evaluators satisfy the Evaluator seam.
var (
	_ Evaluator = RuleEvaluator{}
	_ Evaluator = LLMEvaluator{}
	_ Evaluator = ManualEvaluator{}
	_ Evaluator = CompositeEvaluator{}
)

// --- classifyCriterion ------------------------------------------------------

func TestClassifyCriterion(t *testing.T) {
	tests := []struct {
		in   string
		want criterionKind
		text string
	}{
		{in: "plain text", want: critContains, text: "plain text"},
		{in: "contains: has the summary", want: critContains, text: "has the summary"},
		{in: "  contains:  spaced text  ", want: critContains, text: "spaced text"},
		{in: "not: never say danger", want: critNot, text: "never say danger"},
		{in: "NOT: SHOUTED", want: critNot, text: "SHOUTED"},
		{in: "llm: is the tone right", want: critLLM, text: "is the tone right"},
		{in: "LLM: grammar", want: critLLM, text: "grammar"},
		{in: "manual: needs human review", want: critManual, text: "needs human review"},
		{in: "  Manual:  approved?  ", want: critManual, text: "approved?"},
		{in: "unknown: still bare text", want: critContains, text: "unknown: still bare text"},
	}
	for _, tc := range tests {
		got, text := classifyCriterion(tc.in)
		if got != tc.want || text != tc.text {
			t.Errorf("classifyCriterion(%q) = (%v, %q), want (%v, %q)", tc.in, got, text, tc.want, tc.text)
		}
	}
}

// --- RuleEvaluator ----------------------------------------------------------

func TestRuleEvaluator(t *testing.T) {
	r := RuleEvaluator{}
	ctx := context.Background()

	// contains hit → pass.
	if v, reason, kind, err := r.Evaluate(ctx, "the report contains a summary", []string{"contains: summary"}); v != VerdictPass || err != nil || kind != "rule" || reason == "" {
		t.Errorf("contains hit = (v %v, reason %q, kind %q, err %v), want pass/rule", v, reason, kind, err)
	}

	// contains miss → fail, reason carries the original criterion.
	if v, reason, _, err := r.Evaluate(ctx, "nothing here", []string{"contains: summary"}); v != VerdictFail || err != nil {
		t.Errorf("contains miss = v %v, err %v, want fail", v, err)
	} else if !strings.Contains(reason, "contains: summary") {
		t.Errorf("fail reason %q must contain the original criterion", reason)
	}

	// not hit → fail, reason carries the original criterion.
	if v, reason, _, err := r.Evaluate(ctx, "danger appears in the text", []string{"not: danger"}); v != VerdictFail || err != nil {
		t.Errorf("not hit = v %v, err %v, want fail", v, err)
	} else if !strings.Contains(reason, "not: danger") {
		t.Errorf("fail reason %q must contain the original criterion", reason)
	}

	// not miss → pass.
	if v, _, _, err := r.Evaluate(ctx, "safe and sound", []string{"not: danger"}); v != VerdictPass || err != nil {
		t.Errorf("not miss = v %v, err %v, want pass", v, err)
	}

	// Mixed with llm/manual entries: assertions still enforced, no violation → pass.
	criteria := []string{"contains: summary", "not: danger", "llm: tone", "manual: review"}
	if v, _, kind, err := r.Evaluate(ctx, "the report contains a summary", criteria); v != VerdictPass || err != nil || kind != "rule" {
		t.Errorf("mixed pass = (v %v, kind %q, err %v), want pass/rule", v, kind, err)
	}
	// A violation still fails even alongside llm/manual entries.
	if v, _, _, err := r.Evaluate(ctx, "summary missing and danger", criteria); v != VerdictFail || err != nil {
		t.Errorf("mixed fail = v %v, err %v, want fail", v, err)
	}
}

// --- LLMEvaluator -----------------------------------------------------------

func TestLLMEvaluator(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		v       Verdict
		reason  string
		wantErr error
	}{
		{name: "pass", v: VerdictPass, reason: "looks good"},
		{name: "fail", v: VerdictFail, reason: "misses the point"},
		{name: "manual", v: VerdictManual, reason: "cannot decide"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := LLMEvaluator{Judge: func(ctx context.Context, output string, llmCriteria []string) (Verdict, string, error) {
				if output != "deliverable" || len(llmCriteria) != 1 || llmCriteria[0] != "is it good" {
					t.Errorf("judge got output=%q criteria=%q", output, llmCriteria)
				}
				return tc.v, tc.reason, tc.wantErr
			}}
			v, reason, kind, err := l.Evaluate(ctx, "deliverable", []string{"llm: is it good"})
			if v != tc.v || reason != tc.reason || kind != "llm" || err != tc.wantErr {
				t.Errorf("Evaluate = (v %v, reason %q, kind %q, err %v), want (%v, %q, llm, %v)", v, reason, kind, err, tc.v, tc.reason, tc.wantErr)
			}
		})
	}

	// No llm criteria → pass without a judge call.
	judgeCalled := false
	l := LLMEvaluator{Judge: func(ctx context.Context, _ string, _ []string) (Verdict, string, error) {
		judgeCalled = true
		return VerdictFail, "", nil
	}}
	v, reason, kind, err := l.Evaluate(ctx, "out", []string{"contains: x", "not: y"})
	if v != VerdictPass || reason != "no llm criteria" || kind != "llm" || err != nil {
		t.Errorf("no-llm = (v %v, reason %q, kind %q, err %v), want (pass, \"no llm criteria\", llm, nil)", v, reason, kind, err)
	}
	if judgeCalled {
		t.Error("judge must not be called when there are no llm criteria")
	}

	// Judge error passthrough.
	sentinel := errors.New("judge boom")
	l = LLMEvaluator{Judge: func(ctx context.Context, _ string, _ []string) (Verdict, string, error) {
		return VerdictPass, "irrelevant", sentinel
	}}
	v, _, kind, err = l.Evaluate(ctx, "out", []string{"llm: x"})
	if err != sentinel || kind != "llm" {
		t.Errorf("judge error = (kind %q, err %v), want (llm, sentinel)", kind, err)
	}
}

// --- ManualEvaluator --------------------------------------------------------

func TestManualEvaluator(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name   string
		v      Verdict
		reason string
	}{
		{name: "pass", v: VerdictPass, reason: "approved"},
		{name: "fail", v: VerdictFail, reason: "rejected"},
		{name: "manual", v: VerdictManual, reason: "still pending"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := ManualEvaluator{Manual: func(ctx context.Context, taskID, output string, manualCriteria []string) (Verdict, string, error) {
				if taskID != "" || output != "deliverable" || len(manualCriteria) != 1 || manualCriteria[0] != "human check" {
					t.Errorf("manual hook got taskID=%q output=%q criteria=%q", taskID, output, manualCriteria)
				}
				return tc.v, tc.reason, nil
			}}
			v, reason, kind, err := m.Evaluate(ctx, "deliverable", []string{"manual: human check"})
			if v != tc.v || reason != tc.reason || kind != "manual" || err != nil {
				t.Errorf("Evaluate = (v %v, reason %q, kind %q, err %v), want (%v, %q, manual, nil)", v, reason, kind, err, tc.v, tc.reason)
			}
		})
	}

	// No manual criteria → pass without invoking the hook.
	hookCalled := false
	m := ManualEvaluator{Manual: func(ctx context.Context, _ string, _ string, _ []string) (Verdict, string, error) {
		hookCalled = true
		return VerdictFail, "", nil
	}}
	v, reason, kind, err := m.Evaluate(ctx, "out", []string{"contains: x"})
	if v != VerdictPass || reason != "no manual criteria" || kind != "manual" || err != nil {
		t.Errorf("no-manual = (v %v, reason %q, kind %q, err %v), want (pass, \"no manual criteria\", manual, nil)", v, reason, kind, err)
	}
	if hookCalled {
		t.Error("manual hook must not be called when there are no manual criteria")
	}
}

// --- CompositeEvaluator -----------------------------------------------------

type compositeCase struct {
	name         string
	criteria     []string
	llmV         Verdict
	llmReason    string
	manualV      Verdict
	manualReason string
	fallback     bool
	wantV        Verdict
	wantKind     string
	llmCalls     int
	manualCalls  int
	reasonSubstr string
}

func TestCompositeEvaluator(t *testing.T) {
	tests := []compositeCase{
		{
			name:     "empty criteria passes via rule",
			criteria: nil,
			wantV:    VerdictPass,
			wantKind: "rule",
		},
		{
			name:     "rules only all pass",
			criteria: []string{"contains: summary", "not: danger"},
			wantV:    VerdictPass,
			wantKind: "rule",
		},
		{
			name:         "rule violation short-circuits before llm",
			criteria:     []string{"contains: missing", "llm: x"},
			llmV:         VerdictPass,
			wantV:        VerdictFail,
			wantKind:     "rule",
			reasonSubstr: "criterion not satisfied",
		},
		{
			name:         "llm judge pass decides",
			criteria:     []string{"contains: summary", "llm: x"},
			llmV:         VerdictPass,
			llmReason:    "good",
			wantV:        VerdictPass,
			wantKind:     "llm",
			llmCalls:     1,
			reasonSubstr: "good",
		},
		{
			name:      "llm judge fail decides",
			criteria:  []string{"contains: summary", "llm: x"},
			llmV:      VerdictFail,
			llmReason: "bad",
			wantV:     VerdictFail,
			wantKind:  "llm",
			llmCalls:  1,
		},
		{
			name:         "llm undecided maps to manual with fallback",
			criteria:     []string{"contains: summary", "llm: x"},
			llmV:         VerdictManual,
			llmReason:    "ambiguous",
			fallback:     true,
			wantV:        VerdictManual,
			wantKind:     "llm",
			llmCalls:     1,
			reasonSubstr: "llm undecided",
		},
		{
			name:         "llm undecided maps to fail without fallback",
			criteria:     []string{"contains: summary", "llm: x"},
			llmV:         VerdictManual,
			llmReason:    "ambiguous",
			fallback:     false,
			wantV:        VerdictFail,
			wantKind:     "llm",
			llmCalls:     1,
			reasonSubstr: "llm undecided",
		},
		{
			name:         "manual decides fail",
			criteria:     []string{"contains: summary", "manual: review"},
			manualV:      VerdictFail,
			manualReason: "rejected",
			wantV:        VerdictFail,
			wantKind:     "manual",
			manualCalls:  1,
			reasonSubstr: "rejected",
		},
		{
			name:         "manual pass falls through to llm",
			criteria:     []string{"contains: summary", "manual: review", "llm: x"},
			manualV:      VerdictPass,
			manualReason: "approved",
			llmV:         VerdictPass,
			llmReason:    "good",
			wantV:        VerdictPass,
			wantKind:     "llm",
			manualCalls:  1,
			llmCalls:     1,
		},
		{
			name:      "mixed contains+llm all pass decided by llm",
			criteria:  []string{"contains: summary", "not: danger", "llm: x"},
			llmV:      VerdictPass,
			llmReason: "good",
			wantV:     VerdictPass,
			wantKind:  "llm",
			llmCalls:  1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			llmCalls := 0
			manualCalls := 0
			c := CompositeEvaluator{
				Rule: RuleEvaluator{},
				LLM: LLMEvaluator{Judge: func(ctx context.Context, _ string, _ []string) (Verdict, string, error) {
					llmCalls++
					return tc.llmV, tc.llmReason, nil
				}},
				Manual: ManualEvaluator{Manual: func(ctx context.Context, _ string, _ string, _ []string) (Verdict, string, error) {
					manualCalls++
					return tc.manualV, tc.manualReason, nil
				}},
				ManualFallback: tc.fallback,
			}
			v, reason, kind, err := c.Evaluate(context.Background(), "the report contains a summary", tc.criteria)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if v != tc.wantV || kind != tc.wantKind {
				t.Errorf("Evaluate = (verdict %v, kind %q), want (%v, %q)", v, kind, tc.wantV, tc.wantKind)
			}
			if llmCalls != tc.llmCalls || manualCalls != tc.manualCalls {
				t.Errorf("calls = llm %d manual %d, want llm %d manual %d", llmCalls, manualCalls, tc.llmCalls, tc.manualCalls)
			}
			if tc.reasonSubstr != "" && !strings.Contains(reason, tc.reasonSubstr) {
				t.Errorf("reason %q must contain %q", reason, tc.reasonSubstr)
			}
		})
	}
}
