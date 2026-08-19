package subagent

import (
	"context"
	"errors"
	"testing"
)

// fakeProvider is a scriptable Provider for registry/capability tests.
type fakeProvider struct {
	name    string
	caps    Capabilities
	started int
	lastReq StartRequest
	run     *Run
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Capabilities() Capabilities {
	return f.caps
}
func (f *fakeProvider) Start(ctx context.Context, req StartRequest) (*Run, error) {
	f.started++
	f.lastReq = req
	if f.run != nil {
		return f.run, nil
	}
	return &Run{
		ID: f.name + "-run",
		Result: func(ctx context.Context) (Result, error) {
			return Result{Output: "ok", StopReason: StopCompleted}, nil
		},
		Cancel: func(reason string) error { return nil },
	}, nil
}

// TestRuntimeRegistry covers register / get / list / unknown-provider /
// duplicate-registration behavior of the Runtime registry.
func TestRuntimeRegistry(t *testing.T) {
	rt := NewRuntime()
	alpha := &fakeProvider{name: "alpha"}
	beta := &fakeProvider{name: "beta"}
	if err := rt.RegisterProvider(alpha); err != nil {
		t.Fatalf("register alpha: %v", err)
	}
	if err := rt.RegisterProvider(beta); err != nil {
		t.Fatalf("register beta: %v", err)
	}
	if p, ok := rt.GetProvider("alpha"); !ok || p != alpha {
		t.Fatalf("GetProvider(alpha) = %v, %v", p, ok)
	}
	if _, ok := rt.GetProvider("nope"); ok {
		t.Fatal("GetProvider(nope) unexpectedly present")
	}
	got := rt.ListProviders()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("ListProviders = %v, want [alpha beta]", got)
	}
	// Duplicate registration is rejected.
	if err := rt.RegisterProvider(&fakeProvider{name: "alpha"}); !errors.Is(err, ErrDuplicateProvider) {
		t.Fatalf("duplicate register err = %v, want ErrDuplicateProvider", err)
	}
	// Empty and nil providers are rejected.
	if err := rt.RegisterProvider(&fakeProvider{name: ""}); err == nil {
		t.Fatal("empty provider name must be rejected")
	}
	if err := rt.RegisterProvider(nil); err == nil {
		t.Fatal("nil provider must be rejected")
	}
	// Unknown provider name fails Start.
	if _, err := rt.Start(context.Background(), "nope", StartRequest{Prompt: "p"}); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("unknown-provider start err = %v, want ErrUnknownProvider", err)
	}
}

// TestRuntimeCapabilityValidation covers the Runtime.Start capability gate:
// MaxDepth/ToolFilter/Persona requests are rejected against a provider that
// does not declare the matching capability, accepted (and delegated) when it
// does.
func TestRuntimeCapabilityValidation(t *testing.T) {
	rt := NewRuntime()
	minimal := &fakeProvider{name: "minimal", caps: Capabilities{}}
	full := &fakeProvider{name: "full", caps: Capabilities{DepthLimit: true, ToolFilter: true, Persona: true}}
	if err := rt.RegisterProvider(minimal); err != nil {
		t.Fatalf("register minimal: %v", err)
	}
	if err := rt.RegisterProvider(full); err != nil {
		t.Fatalf("register full: %v", err)
	}

	// The minimal provider rejects each requested capability.
	if _, err := rt.Start(context.Background(), "minimal", StartRequest{Prompt: "p", MaxDepth: 3}); !errors.Is(err, ErrCapabilityNotSupported) {
		t.Fatalf("max_depth err = %v, want ErrCapabilityNotSupported", err)
	}
	if _, err := rt.Start(context.Background(), "minimal", StartRequest{Prompt: "p", ToolFilter: []string{"x"}}); !errors.Is(err, ErrCapabilityNotSupported) {
		t.Fatalf("tool filter err = %v, want ErrCapabilityNotSupported", err)
	}
	if _, err := rt.Start(context.Background(), "minimal", StartRequest{Prompt: "p", Persona: "agent"}); !errors.Is(err, ErrCapabilityNotSupported) {
		t.Fatalf("persona err = %v, want ErrCapabilityNotSupported", err)
	}
	// A plain request passes validation and is delegated.
	if _, err := rt.Start(context.Background(), "minimal", StartRequest{Prompt: "p"}); err != nil {
		t.Fatalf("plain start: %v", err)
	}
	if minimal.started != 1 {
		t.Fatalf("minimal.started = %d, want 1 (delegated)", minimal.started)
	}

	// The full provider accepts all three and receives the request fields.
	if _, err := rt.Start(context.Background(), "full", StartRequest{Prompt: "p", MaxDepth: 3, ToolFilter: []string{"x"}, Persona: "agent"}); err != nil {
		t.Fatalf("full start: %v", err)
	}
	if full.lastReq.MaxDepth != 3 || len(full.lastReq.ToolFilter) != 1 || full.lastReq.ToolFilter[0] != "x" || full.lastReq.Persona != "agent" {
		t.Fatalf("full.lastReq = %+v, want the requested capabilities carried through", full.lastReq)
	}
}

// TestRuntimeClose covers Close semantics: idempotent, and Register/Start are
// rejected after Close.
func TestRuntimeClose(t *testing.T) {
	rt := NewRuntime()
	if err := rt.RegisterProvider(&fakeProvider{name: "a"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("close must be idempotent: %v", err)
	}
	if err := rt.RegisterProvider(&fakeProvider{name: "b"}); !errors.Is(err, ErrProviderClosed) {
		t.Fatalf("register after close err = %v, want ErrProviderClosed", err)
	}
	if _, err := rt.Start(context.Background(), "a", StartRequest{Prompt: "p"}); !errors.Is(err, ErrProviderClosed) {
		t.Fatalf("start after close err = %v, want ErrProviderClosed", err)
	}
}
