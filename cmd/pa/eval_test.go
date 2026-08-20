// eval_test.go — the Eval-3b wiring tests (dispatch-eval-3b §交付 3): registerEval
// D10 gate + tool registration, eval_run smoke through the CompositeEvaluator
// (rule / llm judge / manual fallback), the eval/run event sink, /eval-status,
// and eval_result/eval_list. The fakes mirror the jobs_test/subagent_test
// pattern: makeEvalApp builds a minimal app and the whitelist policy lets the
// registry Execute the eval_* tools (in production config.applyDefaults +
// PolicyFromConfig do this). evalFakeLLM is a scripted llm.LLM (照 subagent
// spawn_test 的 scriptedLLM); evalFakeInteract implements interact.Engine with a
// preset decision (approved/rejected) returned immediately by Await.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"personal-agent/internal/config"
	"personal-agent/internal/interact"
	"personal-agent/internal/llm"
	"personal-agent/internal/session"
	"personal-agent/internal/tools"
)

// evalFakeLLM is a scripted llm.LLM: Stream records every request and returns
// a fixed single-delta text stream (the judge's JSON answer).
type evalFakeLLM struct {
	mu    sync.Mutex
	calls int
	text  string
}

func (f *evalFakeLLM) Stream(ctx context.Context, req llm.ChatRequest) (llm.StreamReader, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return &evalFakeReader{events: []llm.StreamEvent{
		{Kind: llm.StreamTextDelta, Text: f.text},
		{Kind: llm.StreamFinish, FinishReason: "stop"},
	}}, nil
}

func (f *evalFakeLLM) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type evalFakeReader struct {
	events []llm.StreamEvent
	i      int
}

func (r *evalFakeReader) Next() (llm.StreamEvent, error) {
	if r.i >= len(r.events) {
		return llm.StreamEvent{}, io.EOF
	}
	ev := r.events[r.i]
	r.i++
	return ev, nil
}

// evalFakeInteract is a scripted interact.Engine: Request records the request
// and returns a pending one; Await returns immediately with the preset decision
// (approved or rejected) so evalManual sees an instant resolution.
type evalFakeInteract struct {
	mu       sync.Mutex
	status   interact.ApprovalStatus
	next     int
	toolName string
	args     string
}

func (f *evalFakeInteract) Request(ctx context.Context, prompt, toolName, args string) (interact.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.toolName = toolName
	f.args = args
	return interact.Request{
		ID:       fmt.Sprintf("req-%d", f.next),
		Prompt:   prompt,
		ToolName: toolName,
		Args:     args,
		Status:   interact.StatusPending,
	}, nil
}

func (f *evalFakeInteract) Resolve(ctx context.Context, id string, status interact.ApprovalStatus) (interact.Request, error) {
	return interact.Request{ID: id, Status: status}, nil
}

func (f *evalFakeInteract) Await(ctx context.Context, id string) (interact.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return interact.Request{ID: id, Status: f.status}, nil
}

func (f *evalFakeInteract) List(ctx context.Context) ([]interact.Request, error) {
	return nil, nil
}

func (f *evalFakeInteract) Close() error { return nil }

func (f *evalFakeInteract) gotToolName() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.toolName
}

// makeEvalApp builds a minimal app for registerEval tests: only the fields
// registerEval touches (cfg.Eval, reg, log, llm, interacts, currentID) are set.
// fakeLLM / fakeInteract may be nil for the disabled gate and for pure rule
// paths that never touch them.
func makeEvalApp(enabled bool, fakeLLM llm.LLM, fakeInteract interact.Engine) *app {
	manual := true
	return &app{
		cfg: config.Config{
			Model: "m",
			Eval:  config.EvalConfig{Enabled: enabled, MaxRecords: 10, ManualFallback: &manual},
		},
		reg:       tools.New(),
		log:       session.New(),
		currentID: "s-test",
		llm:       fakeLLM,
		interacts: fakeInteract,
	}
}

// evalPolicy whitelists the three eval tools so registry Execute can run them
// (in production config.applyDefaults + PolicyFromConfig do this).
func evalPolicy() tools.Policy {
	return tools.Policy{
		Enabled:     []string{"eval_run", "eval_result", "eval_list"},
		Timeout:     0, // no per-tool deadline in tests
		OutputLimit: 0,
	}
}

// captureStdout is provided by compact_test.go: it runs f with os.Stdout
// redirected to a pipe and returns what was written (the /eval-status text goes
// to stdout via fmt.Println).

// TestRegisterEvalDisabledRegistersNothing verifies the D10 gate: with
// eval.enabled=false the composition root creates no engine and registers no
// eval_* tool (dispatch-eval-3b §交付 3).
func TestRegisterEvalDisabledRegistersNothing(t *testing.T) {
	app := makeEvalApp(false, nil, nil)
	if err := app.registerEval(); err != nil {
		t.Fatalf("registerEval: %v", err)
	}
	if app.evalEng != nil {
		t.Fatal("eval engine must be nil when eval.enabled=false")
	}
	for _, spec := range app.reg.Specs() {
		if strings.HasPrefix(spec.Name, "eval_") {
			t.Fatalf("eval tool %q registered while eval disabled", spec.Name)
		}
	}
}

// TestRegisterEvalEnabledRegistersTools verifies the enabled path: the engine
// is created and the three eval_* tools are registered (dispatch-eval-3b §交付
// 3).
func TestRegisterEvalEnabledRegistersTools(t *testing.T) {
	app := makeEvalApp(true, &evalFakeLLM{text: `{"verdict":"pass","reason":"ok"}`}, &evalFakeInteract{status: interact.StatusApproved})
	if err := app.registerEval(); err != nil {
		t.Fatalf("registerEval: %v", err)
	}
	defer app.evalEng.Close()
	if app.evalEng == nil {
		t.Fatal("eval engine must be created when eval.enabled=true")
	}
	specs := app.reg.Specs()
	names := make([]string, 0, len(specs))
	for _, s := range specs {
		names = append(names, s.Name)
	}
	for _, want := range []string{"eval_run", "eval_result", "eval_list"} {
		if !containsStr(names, want) {
			t.Fatalf("registered tools %v lack %q", names, want)
		}
	}
}

// TestEvalRunRulePass is the rule-assertion smoke: a contains: criterion is
// decided deterministically with zero model calls, eval_run returns the pass
// verdict, and the eval/run event lands in the session log (D3) with verdict
// pass.
func TestEvalRunRulePass(t *testing.T) {
	app := makeEvalApp(true, nil, nil)
	app.reg.SetPolicy(evalPolicy())
	if err := app.registerEval(); err != nil {
		t.Fatalf("registerEval: %v", err)
	}
	defer app.evalEng.Close()
	res, err := app.reg.Execute(context.Background(), "eval_run", json.RawMessage(`{"task_id":"t-1","output":"报告已产出","criteria":["contains:报告"]}`))
	if err != nil {
		t.Fatalf("eval_run via registry: %v", err)
	}
	if !strings.Contains(res.Output, "pass") {
		t.Fatalf("eval_run output = %q, want pass verdict", res.Output)
	}
	found := false
	for _, ev := range app.log.Events() {
		if ev.Type != session.EventEvalRun {
			continue
		}
		found = true
		var p struct {
			Verdict string `json:"verdict"`
		}
		if err := json.Unmarshal(ev.Data, &p); err != nil {
			t.Fatalf("unmarshal eval/run payload: %v", err)
		}
		if p.Verdict != "pass" {
			t.Errorf("eval/run verdict = %q, want pass", p.Verdict)
		}
	}
	if !found {
		t.Fatal("eval/run event missing from the session log after eval_run")
	}
}

// TestEvalRunRuleFail verifies a contains: violation is judged fail by the
// deterministic rule assertion.
func TestEvalRunRuleFail(t *testing.T) {
	app := makeEvalApp(true, nil, nil)
	app.reg.SetPolicy(evalPolicy())
	if err := app.registerEval(); err != nil {
		t.Fatalf("registerEval: %v", err)
	}
	defer app.evalEng.Close()
	res, err := app.reg.Execute(context.Background(), "eval_run", json.RawMessage(`{"task_id":"t-1","output":"报告已产出","criteria":["contains:不存在的内容"]}`))
	if err != nil {
		t.Fatalf("eval_run via registry: %v", err)
	}
	if !strings.Contains(res.Output, "fail") {
		t.Fatalf("eval_run output = %q, want fail verdict", res.Output)
	}
}

// TestEvalRunLLMJudge verifies the llm: criterion routes to the injected LLM
// judge: the fakeLLM's JSON answer ({"verdict":"pass","reason":"ok"}) maps to a
// pass verdict and the judge is invoked exactly once.
func TestEvalRunLLMJudge(t *testing.T) {
	judge := &evalFakeLLM{text: `{"verdict":"pass","reason":"ok"}`}
	app := makeEvalApp(true, judge, nil)
	app.reg.SetPolicy(evalPolicy())
	if err := app.registerEval(); err != nil {
		t.Fatalf("registerEval: %v", err)
	}
	defer app.evalEng.Close()
	res, err := app.reg.Execute(context.Background(), "eval_run", json.RawMessage(`{"task_id":"t-1","output":"结论合理","criteria":["llm:结论合理"]}`))
	if err != nil {
		t.Fatalf("eval_run via registry: %v", err)
	}
	if !strings.Contains(res.Output, "pass") {
		t.Fatalf("eval_run output = %q, want pass verdict", res.Output)
	}
	if got := judge.callCount(); got != 1 {
		t.Errorf("LLM judge invoked %d times, want exactly 1", got)
	}
}

// TestEvalRunManual verifies the manual: criterion forces the human fallback:
// evalManual creates an interact approval request (toolName "eval_manual") and
// maps approved→pass / rejected→fail.
func TestEvalRunManual(t *testing.T) {
	for _, tc := range []struct {
		status interact.ApprovalStatus
		want   string
	}{
		{interact.StatusApproved, "pass"},
		{interact.StatusRejected, "fail"},
	} {
		inter := &evalFakeInteract{status: tc.status}
		app := makeEvalApp(true, nil, inter)
		app.reg.SetPolicy(evalPolicy())
		if err := app.registerEval(); err != nil {
			t.Fatalf("registerEval: %v", err)
		}
		defer app.evalEng.Close()
		res, err := app.reg.Execute(context.Background(), "eval_run", json.RawMessage(`{"task_id":"t-1","output":"交付内容","criteria":["manual:人工确认"]}`))
		if err != nil {
			t.Fatalf("eval_run via registry: %v", err)
		}
		if !strings.Contains(res.Output, tc.want) {
			t.Errorf("eval_run output = %q, want %s verdict", res.Output, tc.want)
		}
		if got := inter.gotToolName(); got != "eval_manual" {
			t.Errorf("interact request toolName = %q, want eval_manual", got)
		}
	}
}

// TestEvalStatus verifies the /eval-status handler: the disabled engine prints
// the disabled line and stays nil; the enabled engine prints the enabled line
// with the record count.
func TestEvalStatus(t *testing.T) {
	// Disabled: evalStatus prints the disabled line and the engine stays nil.
	app := makeEvalApp(false, nil, nil)
	if err := app.registerEval(); err != nil {
		t.Fatalf("registerEval: %v", err)
	}
	if app.evalEng != nil {
		t.Fatal("eval engine must be nil when eval disabled")
	}
	if got := captureStdout(func() {
		if err := app.evalStatus(); err != nil {
			t.Fatalf("evalStatus (disabled): %v", err)
		}
	}); !strings.Contains(got, "disabled") {
		t.Errorf("evalStatus (disabled) = %q, want the disabled line", got)
	}

	// Enabled: evalStatus prints the enabled line with the history summary.
	app = makeEvalApp(true, &evalFakeLLM{text: `{"verdict":"pass","reason":"ok"}`}, &evalFakeInteract{status: interact.StatusApproved})
	app.reg.SetPolicy(evalPolicy())
	if err := app.registerEval(); err != nil {
		t.Fatalf("registerEval: %v", err)
	}
	defer app.evalEng.Close()
	if app.evalEng == nil {
		t.Fatal("eval engine must be created when eval enabled")
	}
	if _, err := app.reg.Execute(context.Background(), "eval_run", json.RawMessage(`{"task_id":"t-1","output":"报告已产出","criteria":["contains:报告"]}`)); err != nil {
		t.Fatalf("eval_run: %v", err)
	}
	if got := captureStdout(func() {
		if err := app.evalStatus(); err != nil {
			t.Fatalf("evalStatus (enabled): %v", err)
		}
	}); !strings.Contains(got, "enabled") || !strings.Contains(got, "records=1") {
		t.Errorf("evalStatus (enabled) = %q, want enabled + records=1", got)
	}
}

// TestEvalResultList verifies eval_list renders the history most-recent-first
// (eval-2 before eval-1) and eval_result returns a single record by id.
func TestEvalResultList(t *testing.T) {
	app := makeEvalApp(true, nil, nil)
	app.reg.SetPolicy(evalPolicy())
	if err := app.registerEval(); err != nil {
		t.Fatalf("registerEval: %v", err)
	}
	defer app.evalEng.Close()
	if _, err := app.reg.Execute(context.Background(), "eval_run", json.RawMessage(`{"task_id":"t-1","output":"alpha report","criteria":["contains:alpha"]}`)); err != nil {
		t.Fatalf("eval_run #1: %v", err)
	}
	if _, err := app.reg.Execute(context.Background(), "eval_run", json.RawMessage(`{"task_id":"t-2","output":"beta report","criteria":["contains:beta"]}`)); err != nil {
		t.Fatalf("eval_run #2: %v", err)
	}
	// eval_list returns both records, most recent first (eval-2 first).
	out, err := app.reg.Execute(context.Background(), "eval_list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("eval_list via registry: %v", err)
	}
	if !strings.Contains(out.Output, "eval eval-2: pass") || !strings.Contains(out.Output, "eval eval-1: pass") {
		t.Errorf("eval_list output = %q, want both records", out.Output)
	}
	if strings.Index(out.Output, "eval-2") > strings.Index(out.Output, "eval-1") {
		t.Errorf("eval_list = %q, want eval-2 before eval-1 (most recent first)", out.Output)
	}
	// eval_result returns a single record by id.
	res, err := app.reg.Execute(context.Background(), "eval_result", json.RawMessage(`{"id":"eval-1"}`))
	if err != nil {
		t.Fatalf("eval_result via registry: %v", err)
	}
	if !strings.Contains(res.Output, "eval eval-1: pass") || strings.Contains(res.Output, "eval-2") {
		t.Errorf("eval_result output = %q, want exactly eval-1", res.Output)
	}
}
