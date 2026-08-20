// external.go — external subagent providers (D-GAP-4): codex / claude-code
// spawn the external CLI in a child process, bridge the prompt over stdin and
// the output from stdout, and report the exit as completed/error. The
// capability is optional and default off: the composition root registers a
// provider only for an enabled, configured external command; a missing binary
// fails closed at Start (no silent fallback to the local provider). The
// provider is honest about capabilities: it enforces none of the harness's
// depth/filter/persona semantics (the CLI owns its own behavior).
package subagent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// ExternalProvider is one external-CLI subagent backend.
type ExternalProvider struct {
	name    string   // provider name ("codex" / "claude-code" / config key)
	command string   // CLI binary; LookPath'd at Start (fail-closed)
	args    []string // CLI arguments for the headless one-shot mode

	mu     sync.Mutex
	nextID int
}

// NewExternalProvider returns a provider for an external one-shot CLI. For the
// two known names the headless arguments are preset: codex → ["exec","--json"],
// claude-code → ["-p"]; any other name runs the binary bare (stdin=prompt,
// stdout=output).
func NewExternalProvider(name, command string) *ExternalProvider {
	p := &ExternalProvider{name: name, command: command}
	switch name {
	case "codex":
		p.args = []string{"exec", "--json"}
	case "claude-code":
		p.args = []string{"-p"}
	}
	return p
}

// Name returns the provider name.
func (p *ExternalProvider) Name() string { return p.name }

// Capabilities declares what the external provider actually enforces: none of
// the harness's depth/filter/persona semantics — the CLI owns its own behavior.
// An honest empty set means Runtime.Start rejects any request asking for a
// harness-enforced capability (fail-closed), so consumers cannot expect the
// external CLI to honor them.
func (p *ExternalProvider) Capabilities() Capabilities { return Capabilities{} }

// externalRun is one in-flight external-CLI subagent: the started command, the
// captured stdout, and the settle/cancel bookkeeping. It is never handed out;
// callers receive fresh Run closures.
type externalRun struct {
	id   string
	cmd  *exec.Cmd
	done chan struct{} // closed once the process settles
	out  bytes.Buffer

	mu      sync.Mutex
	result  Result
	settled bool
}

// Start spawns the external CLI with the prompt on stdin and the output on
// stdout. It does not block: the returned Run's Result awaits the terminal
// outcome and Cancel kills the process (idempotent). A missing binary fails
// closed (ErrInvalidRequest) — there is never a silent fallback to the local
// provider. ctx cancellation kills the process through CommandContext, so a
// cancelled run settles as StopError and Result reports the ctx error.
func (p *ExternalProvider) Start(ctx context.Context, req StartRequest) (*Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.Prompt == "" {
		return nil, fmt.Errorf("%w: prompt is required", ErrInvalidRequest)
	}
	if _, err := exec.LookPath(p.command); err != nil {
		return nil, fmt.Errorf("%w: external provider %q: command %q not found", ErrInvalidRequest, p.name, p.command)
	}
	req.Prompt = withAcceptance(req.Prompt, req.AcceptanceCriteria)

	p.mu.Lock()
	p.nextID++
	id := fmt.Sprintf("%s-%d", p.name, p.nextID)
	p.mu.Unlock()

	cmd := exec.CommandContext(ctx, p.command, p.args...)
	cmd.Stdin = strings.NewReader(req.Prompt)
	run := &externalRun{id: id, cmd: cmd, done: make(chan struct{})}
	cmd.Stdout = &run.out
	cmd.Stderr = io.Discard // stderr is dropped, never part of the Result

	go run.wait()
	return &Run{
		ID:     id,
		Result: run.resultFunc,
		Cancel: run.cancelFunc,
	}, nil
}

// wait drives the external command to its exit and settles the first-wins
// Result: exit 0 → completed, anything else (a non-zero exit, a ctx-cancellation
// kill, or a Start failure) → error. A Start failure leaves Output empty.
func (r *externalRun) wait() {
	defer close(r.done)
	err := r.cmd.Run()
	res := Result{Output: r.out.String()}
	if err == nil {
		res.StopReason = StopCompleted
	} else {
		res.StopReason = StopError
	}
	r.settle(res)
}

// settle records the first terminal result. First-wins: nothing races here in
// practice, but the guard keeps the invariant consistent with the local
// providers.
func (r *externalRun) settle(res Result) {
	r.mu.Lock()
	if !r.settled {
		r.result = res
		r.settled = true
	}
	r.mu.Unlock()
}

// resultFunc returns the Run.Result closure: it blocks until the process
// settles (or ctx is cancelled) and returns the terminal outcome.
func (r *externalRun) resultFunc(ctx context.Context) (Result, error) {
	select {
	case <-r.done:
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result, nil
}

// cancelFunc returns the Run.Cancel closure: it kills the process (idempotent
// — killing an already-exited process is a no-op error) and fails once the run
// has settled.
func (r *externalRun) cancelFunc(reason string) error {
	r.mu.Lock()
	if r.settled {
		r.mu.Unlock()
		return fmt.Errorf("subagent: %s: already finished", r.id)
	}
	cmd := r.cmd
	r.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	return nil
}
