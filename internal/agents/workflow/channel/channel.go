// Package channel is the workflow-facing channel abstraction.
// Concrete channels (slack, telegram, rest, ...) opt in by implementing
// Channel — the base channels.Channel contract is untouched; this is
// purely additive for the workflow engine.
package channel

import (
	"context"
	"sort"
	"sync"
)

// Channel is the surface workflows need from a channel module.
type Channel interface {
	// Name is the registry key used in YAML (`channel: slack`).
	Name() string

	// TriggerSpecs declares what kinds of events this channel can emit.
	TriggerSpecs() []TriggerSpec

	// Actions declares the outbound operations callable from a
	// `type: channel` action node.
	Actions() []ActionSpec

	// Send invokes one of Actions(). Engine renders args via templates
	// before calling.
	Send(ctx context.Context, op string, args map[string]any) (any, error)
}

// TriggerSpec describes one inbound event class for AI introspection.
type TriggerSpec struct {
	Type          string         `json:"type"` // always "channel"
	Events        []string       `json:"events"`
	Description   string         `json:"description"`
	MatchSchema   map[string]any `json:"match_schema,omitempty"`
	PayloadSchema map[string]any `json:"payload_schema,omitempty"`
}

// ActionSpec describes one outbound op.
type ActionSpec struct {
	ID           string         `json:"id"`
	Description  string         `json:"description"`
	Destructive  bool           `json:"destructive,omitempty"`
	InputSchema  map[string]any `json:"input_schema"`
	OutputSchema map[string]any `json:"output_schema,omitempty"`
}

// Registry holds the workflow-facing channels.
type Registry struct {
	mu       sync.RWMutex
	channels map[string]Channel
}

// NewRegistry constructs an empty registry.
func NewRegistry() *Registry {
	return &Registry{channels: map[string]Channel{}}
}

// Register adds (or replaces) a channel by name.
func (r *Registry) Register(ch Channel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.channels[ch.Name()] = ch
}

// Get looks up a channel by name.
func (r *Registry) Get(name string) (Channel, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ch, ok := r.channels[name]
	return ch, ok
}

// List returns all registered channel names sorted.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.channels))
	for n := range r.channels {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Describe returns introspection metadata for `workflow_channels` MCP op.
func (r *Registry) Describe() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []Info{}
	for n, ch := range r.channels {
		out = append(out, Info{
			Name:     n,
			Triggers: ch.TriggerSpecs(),
			Actions:  ch.Actions(),
		})
	}
	return out
}

// Info is one row of the introspection response.
type Info struct {
	Name     string        `json:"name"`
	Triggers []TriggerSpec `json:"triggers"`
	Actions  []ActionSpec  `json:"actions"`
}

// ValidateActionInput checks `args` map against ActionSpec.InputSchema.
// Lightweight — only enforces top-level required keys.
func ValidateActionInput(spec ActionSpec, args map[string]any) error {
	props, _ := spec.InputSchema["properties"].(map[string]any)
	if props == nil {
		return nil
	}
	required, _ := spec.InputSchema["required"].([]any)
	for _, r := range required {
		name, _ := r.(string)
		if name == "" {
			continue
		}
		if _, ok := args[name]; !ok {
			return missingArgError{op: spec.ID, name: name}
		}
	}
	return nil
}

type missingArgError struct {
	op   string
	name string
}

func (e missingArgError) Error() string {
	return "missing required arg \"" + e.name + "\" for op \"" + e.op + "\""
}
