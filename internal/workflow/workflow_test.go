package workflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeSpawn is a scripted Spawn: it records every prompt it receives, counts
// calls and the live-concurrency peak (atomic), and delegates each call to fn
// (defaulting to a fixed output). When block is set, every call first announces
// itself on entered and waits on release — a deterministic concurrency probe:
// the test reads the wanted number of entries (proving overlap) before letting
// the callers proceed. It reflects a cancelled context the way a real spawn
// capability would, so engine-level ctx failures surface.
type fakeSpawn struct {
	fn      func(ctx context.Context, prompt string) (string, error)
	block   bool
	entered chan struct{} // announced per call when block (buffered)
	release chan struct{} // each blocked caller waits here (unbuffered; closing releases all)

	mu      sync.Mutex
	prompts []string

	calls   atomic.Int32
	current atomic.Int32
	peak    atomic.Int32
}

func (f *fakeSpawn) spawn(ctx context.Context, prompt string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	n := f.current.Add(1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	defer f.current.Add(-1)
	if f.block {
		f.entered <- struct{}{}
		<-f.release
	}
	f.mu.Lock()
	f.prompts = append(f.prompts, prompt)
	f.mu.Unlock()
	f.calls.Add(1)
	if f.fn != nil {
		return f.fn(ctx, prompt)
	}
	return "默认输出", nil
}

func (f *fakeSpawn) promptList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.prompts...)
}

func (f *fakeSpawn) hasPromptContaining(sub string) bool {
	for _, p := range f.promptList() {
		if strings.Contains(p, sub) {
			return true
		}
	}
	return false
}

func mustEngine(t *testing.T, f *fakeSpawn, maxConcurrent int) *Engine {
	t.Helper()
	eng, err := NewEngine(f.spawn, maxConcurrent)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return eng
}

// TestRunLinear: a A→B→C chain runs in topological order (A,B,C), each
// dependent's prompt carries the bounded output summary of its dependency.
func TestRunLinear(t *testing.T) {
	fs := &fakeSpawn{}
	eng := mustEngine(t, fs, 0)
	rep, err := eng.Run(context.Background(), Spec{Tasks: []Task{
		{ID: "A", Prompt: "任务A"},
		{ID: "B", Prompt: "任务B", DependsOn: []string{"A"}},
		{ID: "C", Prompt: "任务C", DependsOn: []string{"B"}},
	}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Tasks) != 3 {
		t.Fatalf("tasks = %d, want 3", len(rep.Tasks))
	}
	ids := make([]string, 0, 3)
	for _, r := range rep.Tasks {
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "A,B,C" {
		t.Fatalf("report order = %s, want A,B,C (topological)", strings.Join(ids, ","))
	}
	// B's prompt carries A's output summary; C's carries B's.
	if !fs.hasPromptContaining("A:\n默认输出") {
		t.Error("no prompt carries A's output summary (B's prompt must)")
	}
	if !fs.hasPromptContaining("B:\n默认输出") {
		t.Error("no prompt carries B's output summary (C's prompt must)")
	}
	for _, r := range rep.Tasks {
		if r.Status != StatusCompleted {
			t.Errorf("%s status = %s, want completed", r.ID, r.Status)
		}
		if r.Output != "默认输出" {
			t.Errorf("%s output = %q, want 默认输出", r.ID, r.Output)
		}
		if r.Error != "" {
			t.Errorf("%s error = %q, want empty on success", r.ID, r.Error)
		}
	}
}

// TestRunFanOut: A→{B,C} — B and C are concurrent in one layer and both carry
// A's output summary.
func TestRunFanOut(t *testing.T) {
	fs := &fakeSpawn{block: true, entered: make(chan struct{}, 16), release: make(chan struct{})}
	eng := mustEngine(t, fs, 0)
	done := make(chan error, 1)
	go func() {
		_, err := eng.Run(context.Background(), Spec{Tasks: []Task{
			{ID: "A", Prompt: "任务A"},
			{ID: "B", Prompt: "任务B", DependsOn: []string{"A"}},
			{ID: "C", Prompt: "任务C", DependsOn: []string{"A"}},
		}})
		done <- err
	}()
	// A runs alone in layer 0; release it, then wait for both layer-1 entries
	// to overlap before releasing them. Engine.Run executes layer by layer, so
	// the blocked spawns are observed one layer at a time and released with a
	// token each (never a single close).
	<-fs.entered
	fs.release <- struct{}{}
	<-fs.entered
	<-fs.entered
	fs.release <- struct{}{}
	fs.release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fs.peak.Load() < 2 {
		t.Errorf("concurrency peak = %d, want >= 2 (B and C concurrent)", fs.peak.Load())
	}
	bPrompt, cPrompt := false, false
	for _, p := range fs.promptList() {
		if strings.Contains(p, "任务B") && strings.Contains(p, "A:\n默认输出") {
			bPrompt = true
		}
		if strings.Contains(p, "任务C") && strings.Contains(p, "A:\n默认输出") {
			cPrompt = true
		}
	}
	if !bPrompt || !cPrompt {
		t.Errorf("B/C prompts must both carry A's output summary (b=%v c=%v)", bPrompt, cPrompt)
	}
}

// TestRunIndependentParallel: two dependency-free tasks run concurrently (the
// layer-0 concurrency probe observes both entries overlapping, peak >= 2).
func TestRunIndependentParallel(t *testing.T) {
	fs := &fakeSpawn{block: true, entered: make(chan struct{}, 16), release: make(chan struct{})}
	eng := mustEngine(t, fs, 0)
	done := make(chan error, 1)
	go func() {
		_, err := eng.Run(context.Background(), Spec{Tasks: []Task{
			{ID: "A", Prompt: "任务A"},
			{ID: "B", Prompt: "任务B"},
		}})
		done <- err
	}()
	<-fs.entered
	<-fs.entered
	close(fs.release)
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fs.peak.Load() < 2 {
		t.Errorf("concurrency peak = %d, want >= 2 for independent tasks", fs.peak.Load())
	}
}

// TestRunCycle: A→B→A is rejected with ErrCycle before any spawn.
func TestRunCycle(t *testing.T) {
	fs := &fakeSpawn{}
	eng := mustEngine(t, fs, 0)
	_, err := eng.Run(context.Background(), Spec{Tasks: []Task{
		{ID: "A", Prompt: "p", DependsOn: []string{"B"}},
		{ID: "B", Prompt: "p", DependsOn: []string{"A"}},
	}})
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("Run error = %v, want ErrCycle", err)
	}
	if fs.calls.Load() != 0 {
		t.Errorf("spawn called %d times before a cycle error, want 0", fs.calls.Load())
	}
}

// TestRunUnknownDep: a depends_on that references a missing id is rejected.
func TestRunUnknownDep(t *testing.T) {
	fs := &fakeSpawn{}
	eng := mustEngine(t, fs, 0)
	_, err := eng.Run(context.Background(), Spec{Tasks: []Task{
		{ID: "A", Prompt: "p", DependsOn: []string{"NOPE"}},
	}})
	if err == nil {
		t.Fatal("Run: want an error for an unknown dependency")
	}
	if !strings.Contains(err.Error(), "unknown task") {
		t.Errorf("error = %v, want it to mention unknown task", err)
	}
}

// TestRunDuplicateID: duplicate task ids are rejected.
func TestRunDuplicateID(t *testing.T) {
	fs := &fakeSpawn{}
	eng := mustEngine(t, fs, 0)
	_, err := eng.Run(context.Background(), Spec{Tasks: []Task{
		{ID: "A", Prompt: "p"},
		{ID: "A", Prompt: "p"},
	}})
	if err == nil {
		t.Fatal("Run: want an error for a duplicate task id")
	}
	if !strings.Contains(err.Error(), "duplicate task id") {
		t.Errorf("error = %v, want it to mention duplicate task id", err)
	}
}

// TestRunTaskFailure: a failed spawn marks its task failed (bounded error,
// empty output) while its dependent still runs — with a "依赖 B 失败" note in
// the dependent's prompt instead of the output summary.
func TestRunTaskFailure(t *testing.T) {
	fs := &fakeSpawn{fn: func(ctx context.Context, prompt string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if strings.Contains(prompt, "任务B") {
			return "", errors.New("B 的 spawn 失败")
		}
		return "输出X", nil
	}}
	eng := mustEngine(t, fs, 0)
	rep, err := eng.Run(context.Background(), Spec{Tasks: []Task{
		{ID: "A", Prompt: "任务A"},
		{ID: "B", Prompt: "任务B", DependsOn: []string{"A"}},
		{ID: "C", Prompt: "任务C", DependsOn: []string{"B"}},
	}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Tasks) != 3 {
		t.Fatalf("tasks = %d, want 3", len(rep.Tasks))
	}
	var bRep, cRep *TaskReport
	for i := range rep.Tasks {
		if rep.Tasks[i].ID == "B" {
			bRep = &rep.Tasks[i]
		}
		if rep.Tasks[i].ID == "C" {
			cRep = &rep.Tasks[i]
		}
	}
	if bRep == nil || cRep == nil {
		t.Fatalf("report = %+v, want B and C reports", rep.Tasks)
	}
	if bRep.Status != StatusFailed || !strings.Contains(bRep.Error, "B 的 spawn 失败") {
		t.Fatalf("B report = %+v, want failed with the spawn error", *bRep)
	}
	if bRep.Output != "" {
		t.Errorf("B output = %q, want empty on failure", bRep.Output)
	}
	if cRep.Status != StatusCompleted {
		t.Fatalf("C report = %+v, want completed despite B's failure", *cRep)
	}
	if !fs.hasPromptContaining("（依赖 B 失败）") {
		t.Error("C's prompt must note 依赖 B 失败")
	}
}

// TestRunContextCancel: a pre-cancelled context surfaces as context.Canceled
// with no task run.
func TestRunContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fs := &fakeSpawn{}
	eng := mustEngine(t, fs, 0)
	rep, err := eng.Run(ctx, Spec{Tasks: []Task{
		{ID: "A", Prompt: "任务A"},
		{ID: "B", Prompt: "任务B"},
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if len(rep.Tasks) != 0 {
		t.Errorf("report tasks = %d, want 0 (nothing completed before a pre-cancel)", len(rep.Tasks))
	}
	if fs.calls.Load() != 0 {
		t.Errorf("spawn called %d times, want 0", fs.calls.Load())
	}
}

// TestRunContextCancelPartialRecovery: cancelling mid-run stops scheduling
// further tasks and preserves the already-completed reports alongside
// ctx.Err() (部分恢复).
func TestRunContextCancelPartialRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	fs := &fakeSpawn{fn: func(ctx context.Context, prompt string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		once.Do(cancel) // cancel after the first task produces its output
		return "输出X", nil
	}}
	eng := mustEngine(t, fs, 4)
	rep, err := eng.Run(ctx, Spec{Tasks: []Task{
		{ID: "A", Prompt: "任务A"},
		{ID: "B", Prompt: "任务B"},
		{ID: "C", Prompt: "任务C"},
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if len(rep.Tasks) == 0 {
		t.Fatal("Run must return a partial report with the completed tasks on cancel")
	}
	// The first task produced 输出X before the cancel; every completed task
	// preserved in the report must carry it.
	for _, r := range rep.Tasks {
		if r.Status == StatusCompleted && r.Output != "输出X" {
			t.Errorf("completed task %s output = %q, want 输出X", r.ID, r.Output)
		}
	}
}

// TestMaxConcurrentCap: with maxConcurrent=1 the concurrency peak never
// exceeds 1, even for a layer of three independent tasks.
func TestMaxConcurrentCap(t *testing.T) {
	fs := &fakeSpawn{block: true, entered: make(chan struct{}, 16), release: make(chan struct{})}
	eng := mustEngine(t, fs, 1)
	done := make(chan error, 1)
	go func() {
		_, err := eng.Run(context.Background(), Spec{Tasks: []Task{
			{ID: "A", Prompt: "任务A"},
			{ID: "B", Prompt: "任务B"},
			{ID: "C", Prompt: "任务C"},
		}})
		done <- err
	}()
	// With the cap at 1 the semaphore admits exactly one live call: entries
	// arrive one at a time and are released one at a time.
	for i := 0; i < 3; i++ {
		<-fs.entered
		if fs.peak.Load() > 1 {
			t.Fatalf("concurrency peak = %d, want <= 1 with maxConcurrent=1", fs.peak.Load())
		}
		fs.release <- struct{}{}
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestNewEngineNilSpawn: a nil spawn capability is rejected at construction.
func TestNewEngineNilSpawn(t *testing.T) {
	if _, err := NewEngine(nil, 0); err == nil {
		t.Fatal("NewEngine(nil, 0) must return an error")
	}
}

// TestBoundRunes: over-max text is cut with "…", at-max and empty text pass
// through, the cut is rune-safe, and a non-positive max yields empty.
func TestBoundRunes(t *testing.T) {
	if got := boundRunes("abcdef", 3); got != "abc…" {
		t.Errorf("boundRunes(abcdef, 3) = %q, want abc…", got)
	}
	if got := boundRunes("abc", 3); got != "abc" {
		t.Errorf("boundRunes(abc, 3) = %q, want abc", got)
	}
	if got := boundRunes("", 3); got != "" {
		t.Errorf("boundRunes(\"\", 3) = %q, want empty", got)
	}
	if got := boundRunes("你好世界", 2); got != "你好…" {
		t.Errorf("boundRunes(你好世界, 2) = %q, want 你好…", got)
	}
	if got := boundRunes("abc", 0); got != "" {
		t.Errorf("boundRunes(abc, 0) = %q, want empty", got)
	}
}
