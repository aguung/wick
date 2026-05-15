package workflow

import "github.com/yogasw/wick/internal/agents/workflow/integration"

// AppMentionEvent fires when the bot is @-mentioned in a channel.
// Text is already stripped of the leading <@BOTID> prefix.
type AppMentionEvent struct {
	User      string `json:"user"`
	Text      string `json:"text"`
	ChannelID string `json:"channel_id"`
	Thread    string `json:"thread"`
	TS        string `json:"ts"`
}

func registerEventAppMention(reg *integration.Registry) {
	reg.RegisterEvent(integration.EventDescriptor{
		Channel:     Channel,
		Event:       "app_mention",
		Name:        "Slack: Bot mentioned",
		Description: "Fires when the bot is @-mentioned in a channel. Text has the leading mention stripped.",
		PayloadType: AppMentionEvent{},
	})
}
