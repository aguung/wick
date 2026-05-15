package workflow

import "github.com/yogasw/wick/internal/agents/workflow/integration"

// ShortcutEvent fires for both global shortcuts (App menu) and message
// shortcuts (a message's ⋮ menu). Type is "shortcut" for global,
// "message_action" for message shortcuts. CallbackID is the shortcut
// identifier configured in the Slack app settings.
type ShortcutEvent struct {
	User       string         `json:"user"`
	Type       string         `json:"type"` // "shortcut" | "message_action"
	CallbackID string         `json:"callback_id"`
	TriggerID  string         `json:"trigger_id"`
	ChannelID  string         `json:"channel_id"`   // message_action only
	MessageTS  string         `json:"message_ts"`   // message_action only
	MessageRaw map[string]any `json:"message_raw"`  // message_action only — the message itself
}

func registerEventShortcut(reg *integration.Registry) {
	reg.RegisterEvent(integration.EventDescriptor{
		Channel:     Channel,
		Event:       "shortcut",
		Name:        "Slack: Shortcut invoked",
		Description: "Fires for both global app shortcuts and message shortcuts. Match by callback_id to route per shortcut.",
		PayloadType: ShortcutEvent{},
	})
}
