package subagent

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// echoHelperProvider builds a fake external CLI from the current test binary:
// it runs this binary in helper mode (`-test.run=TestExternalHelperProcess`)
// so the child process reads the injected prompt from stdin and echoes it back
// on stdout — the standard Go "current test binary + env flag" cross-platform
// pattern. GO_WANT_EXTERNAL_HELPER is set on the parent (t.Setenv, restored
// after the test), so the spawned child inherits it; the parent's own
// TestExternalHelperProcess run never sees it and returns normally.
func echoHelperProvider(t *testing.T) *ExternalProvider {
	t.Helper()
	t.Setenv("GO_WANT_EXTERNAL_HELPER", "1")
	p := NewExternalProvider("fake", os.Args[0])
	p.args = []string{"-test.run=TestExternalHelperProcess"}
	return p
}

// TestExternalHelperProcess is the fake external CLI entry point: when
// GO_WANT_EXTERNAL_HELPER=1 it reads its whole stdin and echoes it back on
// stdout (the "CLI" behavior the external-provider tests stand in for), then
// exits 0. In any other environment it is a no-op test that passes silently.
func TestExternalHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_EXTERNAL_HELPER") != "1" {
		return
	}
	defer os.Exit(0)
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(2)
	}
	if _, err := os.Stdout.Write(data); err != nil {
		os.Exit(3)
	}
}

// startAndAwait runs the provider to completion and returns the terminal
// Result, failing the test on any Start/Result error.
func startAndAwait(t *testing.T, prov *ExternalProvider, req StartRequest) Result {
	t.Helper()
	run, err := prov.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	res, err := run.Result(context.Background())
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	return res
}

// TestExternalProviderEcho verifies the external provider bridges prompt → stdin
// and stdout → Output: the echoed output equals the injected prompt verbatim
// and a zero exit is reported as completed.
func TestExternalProviderEcho(t *testing.T) {
	prov := echoHelperProvider(t)
	prompt := "do the thing\nacross two lines"
	res := startAndAwait(t, prov, StartRequest{Prompt: prompt})
	if res.StopReason != StopCompleted {
		t.Fatalf("stop reason = %q, want %q", res.StopReason, StopCompleted)
	}
	if res.Output != prompt {
		t.Fatalf("output = %q, want the prompt echoed verbatim %q", res.Output, prompt)
	}
	if !strings.HasPrefix(res.Output, "do the thing") {
		t.Fatalf("output = %q, want it to start with the prompt", res.Output)
	}
}

// TestExternalProviderMissingBinary verifies fail-closed: a command whose
// binary does not exist fails at Start with an error that wraps
// ErrInvalidRequest and mentions "not found" — there is no silent fallback to
// the local provider.
func TestExternalProviderMissingBinary(t *testing.T) {
	prov := NewExternalProvider("fake", filepath.Join(t.TempDir(), "no-such-cli"))
	_, err := prov.Start(context.Background(), StartRequest{Prompt: "x"})
	if err == nil {
		t.Fatal("Start with a missing binary must fail closed")
	}
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v, want it to wrap ErrInvalidRequest", err)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want it to mention \"not found\"", err)
	}
}

// TestExternalProviderEmptyPrompt verifies an empty prompt is rejected with
// ErrInvalidRequest before any process is spawned.
func TestExternalProviderEmptyPrompt(t *testing.T) {
	prov := NewExternalProvider("fake", os.Args[0])
	_, err := prov.Start(context.Background(), StartRequest{})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty prompt error = %v, want ErrInvalidRequest", err)
	}
}

// TestExternalProviderArgsPreset verifies NewExternalProvider presets the
// headless one-shot arguments for the two known names (same-package test reads
// the unexported args field): codex → "exec", claude-code → "-p".
func TestExternalProviderArgsPreset(t *testing.T) {
	codex := NewExternalProvider("codex", "codex")
	if !slices.Contains(codex.args, "exec") {
		t.Fatalf("codex args = %v, want them to contain \"exec\"", codex.args)
	}
	if !slices.Contains(codex.args, "--json") {
		t.Fatalf("codex args = %v, want them to contain \"--json\"", codex.args)
	}
	cc := NewExternalProvider("claude-code", "claude")
	if !slices.Contains(cc.args, "-p") {
		t.Fatalf("claude-code args = %v, want them to contain \"-p\"", cc.args)
	}
	// Any other name runs the binary bare.
	other := NewExternalProvider("custom", "custom")
	if len(other.args) != 0 {
		t.Fatalf("custom args = %v, want none (bare invocation)", other.args)
	}
}

// TestExternalProviderCancel verifies a pre-cancelled context fails closed at
// Start (CommandContext semantics: the process is bound to ctx, so cancellation
// never silently starts a run).
func TestExternalProviderCancel(t *testing.T) {
	prov := echoHelperProvider(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := prov.Start(ctx, StartRequest{Prompt: "x"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled Start error = %v, want context.Canceled", err)
	}
}

// TestExternalProviderAcceptanceInjected verifies acceptance criteria are
// injected into the prompt (D-EVAL-4): the fake CLI echoes the prompt back, so
// the result's Output must contain the 验收标准 section and each criterion.
func TestExternalProviderAcceptanceInjected(t *testing.T) {
	prov := echoHelperProvider(t)
	res := startAndAwait(t, prov, StartRequest{
		Prompt:             "deliver the report",
		AcceptanceCriteria: []string{"contains:输出含报告", "llm:结论合理"},
	})
	if res.StopReason != StopCompleted {
		t.Fatalf("stop reason = %q, want %q", res.StopReason, StopCompleted)
	}
	if !strings.Contains(res.Output, "验收标准") {
		t.Fatalf("output = %q, want the injected 验收标准 section", res.Output)
	}
	for _, c := range []string{"contains:输出含报告", "llm:结论合理"} {
		if !strings.Contains(res.Output, c) {
			t.Fatalf("output = %q, want it to contain criterion %q", res.Output, c)
		}
	}
}
