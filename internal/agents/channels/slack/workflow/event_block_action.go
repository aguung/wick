package workflow

import "github.com/yogasw/wick/internal/agents/workflow/integration"

// BlockActionEvent fires when a user clicks a button, selects from a
// menu, or interacts with any Block Kit element. ActionID is the
// per-element identifier; CallbackID covers the legacy attachment-level
// callback. TriggerID is short-lived (3s) — only useful for opening a
// modal from the SAME run, downstream nodes that delay (LLM, network)
// will see it expire.
type BlockActionEvent struct {
	User        string         `json:"user"`
	ActionID    string         `json:"action_id"`
	CallbackID  string         `json:"callback_id"`
	BlockID     string         `json:"block_id"`
	Value       string         `json:"value"`        // button/menu value
	SelectedOpt string         `json:"selected_opt"` // selected option for menus
	ChannelID   string         `json:"channel_id"`
	MessageTS   string         `json:"message_ts"`
	Thread      string         `json:"thread"`
	TriggerID   string         `json:"trigger_id"`
	ResponseURL string         `json:"response_url"`
	State       map[string]any `json:"state"` // values from view state when in modal
	Raw         map[string]any `json:"raw"`   // full Slack callback verbatim
}

func registerEventBlockAction(reg *integration.Registry) {
	reg.RegisterEvent(integration.EventDescriptor{
		Channel:     Channel,
		Event:       "block_action",
		Name:        "Slack: Block action (button/menu)",
		Description: "Fires when a user clicks a button or selects a menu item. Use action_id or callback_id to route. trigger_id expires in 3s — open modals from this same run.",
		PayloadType: BlockActionEvent{},
	})
}
