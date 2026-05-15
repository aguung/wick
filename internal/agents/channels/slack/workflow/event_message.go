package workflow

import "github.com/yogasw/wick/internal/agents/workflow/integration"

// MessageEvent is the payload shape the Slack channel emits when a
// user posts a message in a channel/DM the bot can see. Downstream
// nodes reference these fields via `{{.Trigger.<field>}}` templates.
type MessageEvent struct {
	User      string `json:"user"`       // sender user ID (U…)
	Text      string `json:"text"`       // raw message text
	ChannelID string `json:"channel_id"` // C… or D… (DM)
	Thread    string `json:"thread"`     // thread_ts (== ts when starting a new thread)
	TS        string `json:"ts"`         // message ts (Slack message ID)
	IsDM      bool   `json:"is_dm"`      // true for direct messages
}

func registerEventMessage(reg *integration.Registry) {
	reg.RegisterEvent(integration.EventDescriptor{
		Channel:     Channel,
		Event:       "message",
		Name:        "Slack: New message",
		Description: "Fires when a user posts a message in a channel/DM the bot can see (excluding bot's own messages).",
		PayloadType: MessageEvent{},
	})
}
