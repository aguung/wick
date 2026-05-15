package workflow

import "github.com/yogasw/wick/internal/agents/workflow/integration"

// CommandEvent fires when a user invokes a slash command (e.g.
// `/support-ticket some text`). Command includes the leading slash;
// Text is whatever the user typed after it.
type CommandEvent struct {
	User        string `json:"user"`
	Command     string `json:"command"` // "/support-ticket"
	Text        string `json:"text"`    // text after the command
	ChannelID   string `json:"channel_id"`
	TeamID      string `json:"team_id"`
	TriggerID   string `json:"trigger_id"`
	ResponseURL string `json:"response_url"`
}

func registerEventCommand(reg *integration.Registry) {
	reg.RegisterEvent(integration.EventDescriptor{
		Channel:     Channel,
		Event:       "command",
		Name:        "Slack: Slash command",
		Description: "Fires when a user invokes a slash command. Match by command (with leading slash) to route per command.",
		PayloadType: CommandEvent{},
	})
}
