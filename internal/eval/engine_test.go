package eval

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// mockEvaluator returns a fixed outcome, optionally an error, and records the
// last input it judged.
type mockEvaluator struct {
	verdict  Verdict
	reason   string
	kind     string
	err      error
	gotOut   string
	gotCrits []string
}

func (m *mockEvaluator) Evaluate(ctx context.Context, output string, criteria []string) (Verdict, string, string, error) {
	m.gotOut = output
	m.gotCrits = append([]string(nil), criteria...)
	if m.err != nil {
		return "", "", "", m.err
	}
	return m.verdict, m.reason, m.kind, nil
}

func TestNewEngineDefaults(t *testing.T) {
	// Nil Evaluator is rejected.
	if _, err := NewEngine(EngineOpts{Evaluator: nil}); err == nil {
		t.Fatal("NewEngine with nil Evaluator: expected error, got nil")
	}

	// MaxRecords 0 → default cap of 100; next starts at 1.
	eng, err := NewEngine(EngineOpts{Evaluator: &mockEvaluator{}})
	if err != nil {
		t.Fatalf("NewEngine: unexpected error: %v", err)
	}
	impl, ok := eng.(*evalEngine)
	if !ok {
		t.Fatalf("NewEngine returned %T, want *evalEngine", eng)
	}
	if impl.next != 1 {
		t.Errorf("next = %d, want 1", impl.next)
	}
	prov, ok := impl.prov.(*memProvider)
	if !ok {
		t.Fatalf("default provider is %T, want *memProvider", impl.prov)
	}
	if prov.max != 100 {
		t.Errorf("memProvider.max = %d, want 100 (default)", prov.max)
	}

	// Negative MaxRecords also falls back to the default.
	eng2, err := NewEngine(EngineOpts{Evaluator: &mockEvaluator{}, MaxRecords: -5})
	if err != nil {
		t.Fatalf("NewEngine (negative): unexpected error: %v", err)
	}
	if prov2 := eng2.(*evalEngine).prov.(*memProvider); prov2.max != 100 {
		t.Errorf("memProvider.max = %d, want 100 (negative -> default)", prov2.max)
	}

	// Explicit MaxRecords is honored.
	eng3, err := NewEngine(EngineOpts{Evaluator: &mockEvaluator{}, MaxRecords: 7})
	if err != nil {
		t.Fatalf("NewEngine (max 7): unexpected error: %v", err)
	}
	if prov3 := eng3.(*evalEngine).prov.(*memProvider); prov3.max != 7 {
		t.Errorf("memProvider.max = %d, want 7", prov3.max)
	}
}

func TestEngineEvaluateStores(t *testing.T) {
	ctx := context.Background()
	mock := &mockEvaluator{verdict: VerdictPass, reason: "ok", kind: "rule"}
	eng, err := NewEngine(EngineOpts{Evaluator: mock})
	if err != nil {
		t.Fatalf("NewEngine: unexpected error: %v", err)
	}

	crits := []string{"complete", "correct"}
	longOutput := strings.Repeat("a", 5000)
	rec, err := eng.Evaluate(ctx, "task-1", longOutput, crits)
	if err != nil {
		t.Fatalf("Evaluate: unexpected error: %v", err)
	}
	if rec.ID != "eval-1" {
		t.Errorf("ID = %q, want %q", rec.ID, "eval-1")
	}
	if rec.TaskID != "task-1" {
		t.Errorf("TaskID = %q, want %q", rec.TaskID, "task-1")
	}
	if rec.Verdict != VerdictPass {
		t.Errorf("Verdict = %q, want %q", rec.Verdict, VerdictPass)
	}
	if rec.Reason != "ok" {
		t.Errorf("Reason = %q, want %q", rec.Reason, "ok")
	}
	if rec.EvaluatorKind != "rule" {
		t.Errorf("EvaluatorKind = %q, want %q", rec.EvaluatorKind, "rule")
	}
	if !equalStrings(rec.Criteria, crits) {
		t.Errorf("Criteria = %v, want %v", rec.Criteria, crits)
	}
	if !strings.HasPrefix(rec.Output, strings.Repeat("a", recordOutputMax)) || !strings.HasSuffix(rec.Output, "…") {
		t.Errorf("Output = %q (len %d), want %d×a + …", rec.Output, len([]rune(rec.Output)), recordOutputMax)
	}
	if got := len([]rune(rec.Output)); got != recordOutputMax+1 {
		t.Errorf("Output rune len = %d, want %d", got, recordOutputMax+1)
	}
	if rec.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}

	// The evaluator saw the full, unbounded output.
	if mock.gotOut != longOutput {
		t.Errorf("evaluator got output len %d, want full %d", len(mock.gotOut), len(longOutput))
	}
	if !equalStrings(mock.gotCrits, crits) {
		t.Errorf("evaluator got criteria %v, want %v", mock.gotCrits, crits)
	}

	// Second Evaluate issues the next id.
	rec2, err := eng.Evaluate(ctx, "task-1", "short", nil)
	if err != nil {
		t.Fatalf("Evaluate #2: unexpected error: %v", err)
	}
	if rec2.ID != "eval-2" {
		t.Errorf("ID = %q, want %q", rec2.ID, "eval-2")
	}
	if rec2.Output != "short" {
		t.Errorf("Output = %q, want %q (unbounded stays as-is)", rec2.Output, "short")
	}
}

func TestEngineEvaluateErrorNotStored(t *testing.T) {
	ctx := context.Background()
	mock := &mockEvaluator{err: errors.New("boom")}
	eng, err := NewEngine(EngineOpts{Evaluator: mock})
	if err != nil {
		t.Fatalf("NewEngine: unexpected error: %v", err)
	}

	if _, err := eng.Evaluate(ctx, "task-1", "out", nil); err == nil {
		t.Fatal("Evaluate: expected error from evaluator, got nil")
	}
	recs, err := eng.List(ctx)
	if err != nil {
		t.Fatalf("List: unexpected error: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("List returned %d records, want 0 (failed evaluation must not be stored)", len(recs))
	}
}

func TestEngineListMostRecentFirst(t *testing.T) {
	ctx := context.Background()
	eng, err := NewEngine(EngineOpts{Evaluator: &mockEvaluator{verdict: VerdictPass, kind: "rule"}})
	if err != nil {
		t.Fatalf("NewEngine: unexpected error: %v", err)
	}
	for _, id := range []string{"task-1", "task-2", "task-3"} {
		if _, err := eng.Evaluate(ctx, id, "out-"+id, nil); err != nil {
			t.Fatalf("Evaluate(%s): unexpected error: %v", id, err)
		}
	}

	recs, err := eng.List(ctx)
	if err != nil {
		t.Fatalf("List: unexpected error: %v", err)
	}
	got := make([]string, 0, len(recs))
	for _, r := range recs {
		got = append(got, r.ID)
	}
	want := []string{"eval-3", "eval-2", "eval-1"}
	if !equalStrings(got, want) {
		t.Errorf("List order = %v, want %v (most recent first)", got, want)
	}
}

func TestEngineGetUnknown(t *testing.T) {
	ctx := context.Background()
	eng, err := NewEngine(EngineOpts{Evaluator: &mockEvaluator{}})
	if err != nil {
		t.Fatalf("NewEngine: unexpected error: %v", err)
	}
	if _, err := eng.Get(ctx, "nope"); !errors.Is(err, ErrUnknownRecord) {
		t.Errorf("Get(unknown) error = %v, want ErrUnknownRecord", err)
	}
}

func TestEngineMaxRecordsEvicts(t *testing.T) {
	ctx := context.Background()
	eng, err := NewEngine(EngineOpts{Evaluator: &mockEvaluator{verdict: VerdictPass, kind: "rule"}, MaxRecords: 2})
	if err != nil {
		t.Fatalf("NewEngine: unexpected error: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := eng.Evaluate(ctx, "task", "out", nil); err != nil {
			t.Fatalf("Evaluate #%d: unexpected error: %v", i, err)
		}
	}

	recs, err := eng.List(ctx)
	if err != nil {
		t.Fatalf("List: unexpected error: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("List returned %d records, want 2", len(recs))
	}
	if recs[0].ID != "eval-3" || recs[1].ID != "eval-2" {
		t.Errorf("List order = [%s %s], want [eval-3 eval-2]", recs[0].ID, recs[1].ID)
	}
	if _, err := eng.Get(ctx, "eval-1"); !errors.Is(err, ErrUnknownRecord) {
		t.Errorf("Get(eval-1) error = %v, want ErrUnknownRecord (oldest evicted)", err)
	}
	if _, err := eng.Get(ctx, "eval-2"); err != nil {
		t.Errorf("Get(eval-2): unexpected error: %v", err)
	}
}

func TestEngineClosed(t *testing.T) {
	ctx := context.Background()
	eng, err := NewEngine(EngineOpts{Evaluator: &mockEvaluator{verdict: VerdictPass, kind: "rule"}})
	if err != nil {
		t.Fatalf("NewEngine: unexpected error: %v", err)
	}
	if _, err := eng.Evaluate(ctx, "task", "out", nil); err != nil {
		t.Fatalf("Evaluate before Close: unexpected error: %v", err)
	}

	if err := eng.Close(); err != nil {
		t.Fatalf("Close: unexpected error: %v", err)
	}
	// Close is idempotent.
	if err := eng.Close(); err != nil {
		t.Errorf("second Close: unexpected error: %v", err)
	}

	if _, err := eng.Evaluate(ctx, "task", "out", nil); !errors.Is(err, ErrEngineClosed) {
		t.Errorf("Evaluate after Close error = %v, want ErrEngineClosed", err)
	}
	if _, err := eng.List(ctx); !errors.Is(err, ErrEngineClosed) {
		t.Errorf("List after Close error = %v, want ErrEngineClosed", err)
	}
	if _, err := eng.Get(ctx, "eval-1"); !errors.Is(err, ErrEngineClosed) {
		t.Errorf("Get after Close error = %v, want ErrEngineClosed", err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
