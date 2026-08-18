package prompt

import "testing"

func TestBuildSingleSection(t *testing.T) {
	b := New("You are helpful.")
	got := b.Build()
	if got != "You are helpful." {
		t.Fatalf("Build() = %q", got)
	}
}
