# Eval-1b 派发：internal/eval Engine + mem Provider

> 评测接缝 ADR `docs/decisions/2026-08-20-eval-seam.md` D-EVAL-1/2。本文件是 **Eval-1b** 契约：`internal/eval` 的 **Engine（Service）+ mem Provider**（评测历史存储）。前置：**Eval-1a 已交付** evaluator 层（`eval.go` 的 `Verdict`/`EvalRecord`/`Evaluator` 接口 + `evaluators.go` 四实现）。

## 纪律

- 零新依赖、CGO-free；只新增 `internal/eval/engine.go` + `internal/eval/engine_test.go`；不改已有文件（eval.go/evaluators.go 只读）；gofmt。
- 提交 1 个：`Eval-1b: internal/eval Engine + mem Provider（评测历史存储 + 上限淘汰）`

## 已知 API（Eval-1a 已交付，勿重复读）

```go
type Verdict string
const (VerdictPass = "pass"; VerdictFail = "fail"; VerdictManual = "manual")

type EvalRecord struct {
	ID            string
	TaskID        string
	Criteria      []string
	Output        string   // bounded deliverable summary (≤ 4000 runes)
	Verdict       Verdict
	Reason        string
	EvaluatorKind string   // "rule" | "llm" | "manual"
	CreatedAt     time.Time
}

type Evaluator interface {
	Evaluate(ctx context.Context, output string, criteria []string) (Verdict, string, string, error)
	// ^ (verdict, reason, kind, err)
}
```

## 契约

### engine.go

```go
// Provider is one evaluation-record backend (D-EVAL-2): a dumb store the
// Engine calls through. Callers receive fresh value copies, never live state.
type Provider interface {
	Name() string
	// List returns every record, most recent first.
	List(ctx context.Context) ([]EvalRecord, error)
	// Get returns the record with id; an unknown id is rejected.
	Get(ctx context.Context, id string) (EvalRecord, error)
	// Put stores a record, returning it with any provider-issued id filled.
	Put(ctx context.Context, r EvalRecord) (EvalRecord, error)
}

// Engine is the evaluation Service (D-EVAL-2). Consumers depend only on this
// interface. Lifecycle: Evaluate judges a deliverable and records it; List/Get
// observe the history; Close releases the backend. Close is idempotent.
type Engine interface {
	// Evaluate runs the configured Evaluator over (output, criteria) and
	// records the outcome under a fresh engine-issued id ("eval-N"), bounding
	// the stored Output to recordOutputMax runes. An Evaluator error is
	// returned without recording.
	Evaluate(ctx context.Context, taskID, output string, criteria []string) (EvalRecord, error)
	// List returns every record, most recent first.
	List(ctx context.Context) ([]EvalRecord, error)
	// Get returns the record with id; an unknown id is rejected.
	Get(ctx context.Context, id string) (EvalRecord, error)
	// Close releases the backend and marks the engine closed. It is idempotent;
	// every other operation after Close is rejected with ErrEngineClosed.
	Close() error
}

// EngineOpts configures NewEngine.
type EngineOpts struct {
	Evaluator  Evaluator
	MaxRecords int // >0 caps stored history, evicting oldest; 0 → default (100)
}

func NewEngine(opts EngineOpts) (Engine, error)
// 校验：opts.Evaluator nil → error；MaxRecords<=0 → 100。

// recordOutputMax bounds the stored deliverable summary (D-EVAL-1).
const recordOutputMax = 4000

// boundRunes truncates s to at most max runes (append "…" when cut).
func boundRunes(s string, max int) string

// Sentinel errors.
var (
	ErrEngineClosed = errors.New("eval: engine closed")
	ErrUnknownRecord = errors.New("eval: unknown record")
	ErrProviderClosed = errors.New("eval: provider closed")
)
```

- 实现 `evalEngine` struct：`{eval Evaluator; prov Provider; next uint64; mu sync.Mutex; closed bool}`。
- `NewEngine`：memProvider 默认；把 `opts.Evaluator` 存下；`next=1`。
- `Evaluate`：closed 检查；`eval.Evaluate(ctx, output, criteria)` → err 则 return（不落库）；构造 `EvalRecord{ID: fmt.Sprintf("eval-%d", next), TaskID: taskID, Criteria: copy(criteria), Output: boundRunes(output, recordOutputMax), Verdict, Reason, EvaluatorKind, CreatedAt: time.Now()}`；`next++`；`prov.Put`（Put 内部做上限淘汰 + 返回带 id 的记录）→ 返回。
- `List`/`Get`：closed 检查 → prov。
- `Close`：idempotent；置 closed；prov closer（mem 无资源，直接返回 nil）。

### memProvider（同文件或 mem.go，放 engine.go 内即可）

```go
// memProvider is the default Provider: in-memory, most-recent-first ordering,
// capped at maxRecords (evicting the oldest on overflow).
type memProvider struct {
	mu      sync.Mutex
	records map[string]EvalRecord
	order   []string // insertion order, oldest first
	max     int
}
func newMemProvider(max int) *memProvider
// Put：存在则替换（保留原插入位）；新增则追加 order；超 max 则丢 order[0]。
// List：order 倒序返回对应记录副本。
// Get：未知 id → ErrUnknownRecord。
```

### engine_test.go

- `TestNewEngineDefaults`：MaxRecords 0 → 内部 100；Evaluator nil → error。
- `TestEngineEvaluateStores`：mock Evaluator（返回 pass/"ok"/"rule"）→ Evaluate 返回 EvalRecord{ID:"eval-1", TaskID, Verdict:pass, Kind:"rule", Output 截断：传 5000 字符 output → 存 4000+…}；再 Evaluate → ID "eval-2"。
- `TestEngineEvaluateErrorNotStored`：mock Evaluator 返回 error → Evaluate error；List 空。
- `TestEngineListMostRecentFirst`：3 次 Evaluate → List 顺序 eval-3, eval-2, eval-1。
- `TestEngineGetUnknown`：Get("nope") → ErrUnknownRecord。
- `TestEngineMaxRecordsEvicts`：MaxRecords=2 → 3 次 Evaluate → List 只含后 2 条（最老的 eval-1 被淘汰）。
- `TestEngineClosed`：Close 后 Evaluate/List/Get → ErrEngineClosed；Close 幂等。

## 验证

`go build ./...` + `go test -count=1 ./internal/eval/ -v` 全 PASS 后提交。

## 环境

- Go：`C:\Program Files\Go\bin\go.exe`；env：`$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\shutu-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\shutu-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\shutu-agent\.gocache'`；提交身份 `-c user.name='Personal Agent' -c user.email='dev@shutu-agent.local'`；pwsh，workdir=`D:\dev-projects\Agent\shutu-agent`。
