package nodes

import (
	"context"
	"fmt"

	"github.com/yogasw/wick/internal/agents/workflow"
	"github.com/yogasw/wick/internal/agents/workflow/channel"
	"github.com/yogasw/wick/internal/agents/workflow/template"
)

// ChannelExecutor dispatches `type: channel` action nodes via the
// registry. Args templates are rendered against RunContext before invoke.
type ChannelExecutor struct {
	Registry *channel.Registry
}

// NewChannelExecutor wires the executor.
func NewChannelExecutor(reg *channel.Registry) *ChannelExecutor {
	return &ChannelExecutor{Registry: reg}
}

// Execute invokes Channel.Send for the named op.
func (e *ChannelExecutor) Execute(ctx context.Context, n workflow.Node, rc *workflow.RunContext) (workflow.NodeOutput, error) {
	if e.Registry == nil {
		return workflow.NodeOutput{}, fmt.Errorf("channel executor: no registry configured")
	}
	ch, ok := e.Registry.Get(n.ChannelName)
	if !ok {
		return workflow.NodeOutput{}, fmt.Errorf("channel %q not registered (available: %v)", n.ChannelName, e.Registry.List())
	}
	opFound := false
	var spec channel.ActionSpec
	for _, a := range ch.Actions() {
		if a.ID == n.Op {
			opFound = true
			spec = a
			break
		}
	}
	if !opFound {
		return workflow.NodeOutput{}, fmt.Errorf("channel %q has no op %q", n.ChannelName, n.Op)
	}
	rendered, err := template.RenderInto(n.Args, rc.RenderCtx())
	if err != nil {
		return workflow.NodeOutput{}, fmt.Errorf("render args: %w", err)
	}
	args, _ := rendered.(map[string]any)
	if args == nil {
		args = map[string]any{}
	}
	if err := channel.ValidateActionInput(spec, args); err != nil {
		return workflow.NodeOutput{}, err
	}
	result, err := ch.Send(ctx, n.Op, args)
	if err != nil {
		return workflow.NodeOutput{}, fmt.Errorf("channel %s.%s: %w", n.ChannelName, n.Op, err)
	}
	out := workflow.NodeOutput{Result: result, Fields: map[string]any{"result": result}}
	if m, ok := result.(map[string]any); ok {
		for k, v := range m {
			out.Fields[k] = v
		}
	}
	return out, nil
}
