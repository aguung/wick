package nodes

import (
	"context"
	"fmt"

	"github.com/yogasw/wick/internal/agents/workflow"
	"github.com/yogasw/wick/internal/agents/workflow/engine"
	"github.com/yogasw/wick/internal/agents/workflow/integration"
)

type channelSchema struct {
	ChannelName string `wick:"required;key=channel;desc=Channel module name (e.g. slack)"`
	Op          string `wick:"required;key=op;desc=Action name — call workflow_integration to list available ops per channel"`
	Args        string `wick:"key=args;desc=Op inputs as YAML map — see workflow_integration for exact schema per op"`
	ArgModes    string `wick:"key=arg_modes;desc=Per-field mode: fixed=literal value, expression=Go template render (default)"`
}

func (e *ChannelExecutor) Descriptor() engine.NodeDescriptor {
	return engine.NodeDescriptor{
		Description: "Invoke a channel action (send_message, open_modal, …). Call workflow_integration for available ops + schemas.",
		WhenToUse:   "Send messages, open modals, react, reply via Slack/Telegram/etc.",
		Example:     "- id: sendmsg\n  type: channel\n  channel: slack\n  op: send_message\n  args:\n    channel: '{{index .Event.Payload \"channel_id\"}}'\n    text: Hello\n  arg_modes:\n    channel: expression\n    text: fixed",
		Schema:      integration.StructSchema(channelSchema{}),
		Output: map[string]string{
			"ts":        "channel-dependent (Slack: message timestamp)",
			"channel":   "channel ID",
			"view_id":   "open_modal only",
			"view_hash": "open_modal only",
		},
	}
}

// ChannelExecutor dispatches `type: channel` action nodes through the
// integration registry. Each registered (channel, action) pair
// describes its own input schema and Execute closure, so this executor
// is just glue: render args → look up descriptor → call Execute.
//
// Adding a new outbound op = drop a file under
// internal/agents/channels/<name>/workflow/ that registers an
// ActionDescriptor. No engine change required.
type ChannelExecutor struct {
	Registry *integration.Registry
}

// NewChannelExecutor wires the executor to the integration registry.
func NewChannelExecutor(reg *integration.Registry) *ChannelExecutor {
	return &ChannelExecutor{Registry: reg}
}

// Execute renders the node's args, resolves the descriptor by
// "<channel>.<op>", and dispatches.
func (e *ChannelExecutor) Execute(ctx context.Context, n workflow.Node, rc *workflow.RunContext) (workflow.NodeOutput, error) {
	if e.Registry == nil {
		return workflow.NodeOutput{}, fmt.Errorf("channel executor: no integration registry")
	}
	key := n.ChannelName + "." + n.Op
	desc, ok := e.Registry.Action(key)
	if !ok {
		return workflow.NodeOutput{}, fmt.Errorf("channel action %q not registered", key)
	}
	args, err := renderArgsWithModes(n.Args, n.ArgModes, rc)
	if err != nil {
		return workflow.NodeOutput{}, fmt.Errorf("render args: %w", err)
	}
	result, err := desc.Execute(ctx, args)
	if err != nil {
		return workflow.NodeOutput{}, fmt.Errorf("%s: %w", key, err)
	}
	out := workflow.NodeOutput{Result: result, Fields: map[string]any{"result": result}}
	if m, ok := result.(map[string]any); ok {
		for k, v := range m {
			out.Fields[k] = v
		}
	}
	return out, nil
}
