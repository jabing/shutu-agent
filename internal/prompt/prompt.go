// Package prompt assembles the system prompt. M1 uses a single persona section
// (design.md §7); sectioned assembly (persona / skills / knowledge / tools)
// lands in M2. The builder stays a seam so M2 only adds sections without
// touching the loop.
package prompt

// Builder renders a system prompt from its sections. In M1 it holds a single
// persona section.
type Builder struct {
	persona string
}

// New returns a Builder with the given persona as its only section.
func New(persona string) *Builder {
	return &Builder{persona: persona}
}

// Build renders the current system prompt. Tool schemas are not inlined here
// in M1: they are sent through the request's tools field, and the M2 catalog
// section will list them in the prompt itself.
func (b *Builder) Build() string {
	return b.persona
}
