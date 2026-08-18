// Package tools implements the capability registry: registration, the
// model-facing schema projection, and the single validated execution gate
// (D7). Every Execute validates the model-generated arguments against the
// tool's JSON Schema before dispatch; tools never parse bare JSON themselves.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"personal-agent/internal/llm"
)

// Tool is one capability the agent can invoke.
type Tool interface {
	Name() string
	// Description is a short human-readable summary of what the tool does. It
	// feeds the prompt's automatic tool catalog (design.md §7) and the
	// model-facing request schema.
	Description() string
	Schema() map[string]any // JSON Schema of the arguments; also sent to the model
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry owns the registered tools and their compiled schemas.
type Registry struct {
	tools   map[string]Tool
	schemas map[string]*jsonschema.Schema
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{
		tools:   map[string]Tool{},
		schemas: map[string]*jsonschema.Schema{},
	}
}

// Register adds a tool. A duplicate name is rejected. The argument schema is
// compiled once at registration so Execute has no per-call compile cost.
func (r *Registry) Register(t Tool) error {
	if _, ok := r.tools[t.Name()]; ok {
		return fmt.Errorf("tools: tool %q already registered", t.Name())
	}
	raw, err := json.Marshal(t.Schema())
	if err != nil {
		return fmt.Errorf("tools: marshal schema for %q: %w", t.Name(), err)
	}
	compiler := jsonschema.NewCompiler()
	url := "tool://" + t.Name()
	if err := compiler.AddResource(url, bytes.NewReader(raw)); err != nil {
		return fmt.Errorf("tools: add schema for %q: %w", t.Name(), err)
	}
	sch, err := compiler.Compile(url)
	if err != nil {
		return fmt.Errorf("tools: compile schema for %q: %w", t.Name(), err)
	}
	r.tools[t.Name()] = t
	r.schemas[t.Name()] = sch
	return nil
}

// Specs returns the model-facing tool schemas, sorted by name for a stable
// prompt/request.
func (r *Registry) Specs() []llm.ToolSchema {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	specs := make([]llm.ToolSchema, 0, len(names))
	for _, name := range names {
		specs = append(specs, llm.ToolSchema{
			Name:        name,
			Description: r.tools[name].Description(),
			Parameters:  r.tools[name].Schema(),
		})
	}
	return specs
}

// Execute validates name and arguments, then dispatches to the tool. An
// unknown tool name is an error; malformed or schema-invalid arguments never
// reach the tool body (D7).
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("tools: unknown tool %q", name)
	}
	var v any
	if err := json.Unmarshal(args, &v); err != nil {
		return "", fmt.Errorf("tools: %s: invalid arguments JSON: %w", name, err)
	}
	if err := r.schemas[name].Validate(v); err != nil {
		return "", fmt.Errorf("tools: %s: invalid arguments: %w", name, err)
	}
	return t.Execute(ctx, args)
}
