package skill

import (
	"context"
	"errors"
	"testing"
)

// fakeProvider is a scriptable Provider for registry tests. defs maps a
// candidate name to the Definition the provider returns on Get; the returned
// Definition's own Name field may deliberately differ (name-mismatch tests).
type fakeProvider struct {
	name    string
	cands   []Candidate
	defs    map[string]*Definition // keyed by candidate name
	listErr error
	getErr  error
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) List(ctx context.Context) ([]Candidate, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.cands, nil
}

func (f *fakeProvider) Get(ctx context.Context, c Candidate) (*Definition, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.defs[c.Name], nil
}

// TestRegistryRegisterValidation covers nil / empty-name / duplicate provider
// rejection.
func TestRegistryRegisterValidation(t *testing.T) {
	reg := NewRegistry()
	if err := reg.RegisterProvider(nil); err == nil {
		t.Fatal("nil provider must be rejected")
	}
	if err := reg.RegisterProvider(&fakeProvider{name: ""}); err == nil {
		t.Fatal("empty provider name must be rejected")
	}
	if err := reg.RegisterProvider(&fakeProvider{name: "p"}); err != nil {
		t.Fatalf("register p: %v", err)
	}
	if err := reg.RegisterProvider(&fakeProvider{name: "p"}); !errors.Is(err, ErrDuplicateProvider) {
		t.Fatalf("duplicate register err = %v, want ErrDuplicateProvider", err)
	}
}

// TestRegistryListMergeAndRank covers merging two providers, same-name
// resolution by rank (lower wins), and name-sorted output.
func TestRegistryListMergeAndRank(t *testing.T) {
	reg := NewRegistry()
	proj := &fakeProvider{name: "proj", cands: []Candidate{
		{Name: "alpha", Description: "proj alpha", Source: SourceProjectDSH, Rank: RankProjectDSH, Path: "/p/alpha.md"},
		{Name: "beta", Description: "proj beta", Source: SourceProjectDSH, Rank: RankProjectDSH, Path: "/p/beta.md"},
	}}
	user := &fakeProvider{name: "user", cands: []Candidate{
		{Name: "alpha", Description: "user alpha", Source: SourceUserDSH, Rank: RankUserDSH, Path: "/u/alpha.md"},
		{Name: "zeta", Description: "user zeta", Source: SourceUserDSH, Rank: RankUserDSH, Path: "/u/zeta.md"},
	}}
	if err := reg.RegisterProvider(proj); err != nil {
		t.Fatalf("register proj: %v", err)
	}
	if err := reg.RegisterProvider(user); err != nil {
		t.Fatalf("register user: %v", err)
	}

	got, err := reg.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("List returned %d candidates, want 3: %+v", len(got), got)
	}
	// Sorted by name: alpha, beta, zeta.
	names := []string{got[0].Name, got[1].Name, got[2].Name}
	if names[0] != "alpha" || names[1] != "beta" || names[2] != "zeta" {
		t.Fatalf("List names = %v, want [alpha beta zeta]", names)
	}
	// "alpha" appears in both roots; rank 100 (project-dsh) wins over 400.
	if got[0].Source != SourceProjectDSH || got[0].Rank != RankProjectDSH {
		t.Fatalf("winning alpha = %+v, want project-dsh rank 100", got[0])
	}
}

// TestRegistryTieBreak covers equal-rank same-name resolution: provider
// registration order, then the provider's local (list) order.
func TestRegistryTieBreak(t *testing.T) {
	t.Run("provider registration order", func(t *testing.T) {
		reg := NewRegistry()
		first := &fakeProvider{name: "first", cands: []Candidate{
			{Name: "tie", Description: "first-wins", Rank: RankCustom},
		}}
		second := &fakeProvider{name: "second", cands: []Candidate{
			{Name: "tie", Description: "second-loses", Rank: RankCustom},
		}}
		if err := reg.RegisterProvider(first); err != nil {
			t.Fatalf("register first: %v", err)
		}
		if err := reg.RegisterProvider(second); err != nil {
			t.Fatalf("register second: %v", err)
		}
		got, err := reg.List(context.Background())
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 1 || got[0].Description != "first-wins" {
			t.Fatalf("tie winner = %+v, want the first-registered provider's candidate", got)
		}
	})

	t.Run("local order within one provider", func(t *testing.T) {
		reg := NewRegistry()
		p := &fakeProvider{name: "p", cands: []Candidate{
			{Name: "dup", Description: "locally-first", Rank: RankCustom},
			{Name: "dup", Description: "locally-second", Rank: RankCustom},
		}}
		if err := reg.RegisterProvider(p); err != nil {
			t.Fatalf("register: %v", err)
		}
		got, err := reg.List(context.Background())
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 1 || got[0].Description != "locally-first" {
			t.Fatalf("local tie winner = %+v, want the earlier candidate", got)
		}
	})
}

// TestRegistryGetLoadsFullDefinition covers Get returning the complete loaded
// definition (body, invocation flags) for the winning candidate.
func TestRegistryGetLoadsFullDefinition(t *testing.T) {
	reg := NewRegistry()
	p := &fakeProvider{
		name:  "p",
		cands: []Candidate{{Name: "foo", Description: "d", Source: SourceUserDSH, Rank: RankUserDSH, Path: "/u/foo.md"}},
		defs: map[string]*Definition{
			"foo": {Name: "foo", Description: "d", Content: "# Foo\nbody", Source: SourceUserDSH, Path: "/u/foo.md", ModelInvocable: false, UserInvocable: true},
		},
	}
	if err := reg.RegisterProvider(p); err != nil {
		t.Fatalf("register: %v", err)
	}
	def, err := reg.Get(context.Background(), "foo")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if def == nil {
		t.Fatal("Get returned nil definition")
	}
	if def.Name != "foo" || def.Content != "# Foo\nbody" || def.ModelInvocable || !def.UserInvocable {
		t.Fatalf("def = %+v, want loaded body + invocation flags", def)
	}
}

// TestRegistryGetNameMismatchRejected covers the required rejection: a loaded
// definition whose Name no longer matches the requested skill name.
func TestRegistryGetNameMismatchRejected(t *testing.T) {
	reg := NewRegistry()
	p := &fakeProvider{
		name:  "p",
		cands: []Candidate{{Name: "foo", Description: "d", Rank: RankCustom}},
		defs: map[string]*Definition{
			"foo": {Name: "renamed", Description: "d", Content: "body"},
		},
	}
	if err := reg.RegisterProvider(p); err != nil {
		t.Fatalf("register: %v", err)
	}
	if def, err := reg.Get(context.Background(), "foo"); def != nil || err == nil {
		t.Fatalf("Get = %v, %v; want nil definition with a name-mismatch error", def, err)
	}
}

// TestRegistryGetNotFoundAndInvalidName covers (nil, nil) for an unknown
// skill and ErrInvalidName for a non-kebab-case name.
func TestRegistryGetNotFoundAndInvalidName(t *testing.T) {
	reg := NewRegistry()
	if err := reg.RegisterProvider(&fakeProvider{name: "p"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	def, err := reg.Get(context.Background(), "nope")
	if def != nil || err != nil {
		t.Fatalf("Get(unknown) = %v, %v; want (nil, nil)", def, err)
	}
	if _, err := reg.Get(context.Background(), "Not-Kebab"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("Get(invalid name) err = %v, want ErrInvalidName", err)
	}
}

// TestRegistryProviderFailures covers fail-closed behavior: a provider whose
// List fails, a provider Get failure, and a provider returning an invalid
// candidate are all surfaced as errors.
func TestRegistryProviderFailures(t *testing.T) {
	t.Run("list error propagates", func(t *testing.T) {
		reg := NewRegistry()
		if err := reg.RegisterProvider(&fakeProvider{name: "bad", listErr: errors.New("boom")}); err != nil {
			t.Fatalf("register: %v", err)
		}
		if _, err := reg.List(context.Background()); err == nil {
			t.Fatal("List must propagate a provider list error")
		}
	})

	t.Run("get error propagates", func(t *testing.T) {
		reg := NewRegistry()
		p := &fakeProvider{name: "bad", cands: []Candidate{{Name: "foo", Description: "d", Rank: RankCustom}}, getErr: errors.New("boom")}
		if err := reg.RegisterProvider(p); err != nil {
			t.Fatalf("register: %v", err)
		}
		if _, err := reg.Get(context.Background(), "foo"); err == nil {
			t.Fatal("Get must propagate a provider get error")
		}
	})

	t.Run("invalid candidate rejected", func(t *testing.T) {
		reg := NewRegistry()
		p := &fakeProvider{name: "p", cands: []Candidate{{Name: "Bad Name", Description: "d", Rank: RankCustom}}}
		if err := reg.RegisterProvider(p); err != nil {
			t.Fatalf("register: %v", err)
		}
		if _, err := reg.List(context.Background()); err == nil {
			t.Fatal("List must reject a provider candidate with an invalid name")
		}
	})
}

// TestRegistryClose covers Close semantics: idempotent, and
// Register/List/Get are rejected afterwards.
func TestRegistryClose(t *testing.T) {
	reg := NewRegistry()
	if err := reg.RegisterProvider(&fakeProvider{name: "p"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("second Close must be idempotent, got %v", err)
	}
	if err := reg.RegisterProvider(&fakeProvider{name: "q"}); !errors.Is(err, ErrProviderClosed) {
		t.Fatalf("register after Close err = %v, want ErrProviderClosed", err)
	}
	if _, err := reg.List(context.Background()); !errors.Is(err, ErrProviderClosed) {
		t.Fatalf("List after Close err = %v, want ErrProviderClosed", err)
	}
	if _, err := reg.Get(context.Background(), "foo"); !errors.Is(err, ErrProviderClosed) {
		t.Fatalf("Get after Close err = %v, want ErrProviderClosed", err)
	}
}
