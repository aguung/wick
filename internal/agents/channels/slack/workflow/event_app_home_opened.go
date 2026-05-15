package workflow

import "github.com/yogasw/wick/internal/agents/workflow/integration"

// AppHomeOpenedEvent fires when a user opens the bot's Home tab. Use
// it to push a fresh Home view via the publish_home action.
type AppHomeOpenedEvent struct {
	User string `json:"user"`
	Tab  string `json:"tab"` // "home" | "messages"
}

func registerEventAppHomeOpened(reg *integration.Registry) {
	reg.RegisterEvent(integration.EventDescriptor{
		Channel:     Channel,
		Event:       "app_home_opened",
		Name:        "Slack: App Home opened",
		Description: "Fires when a user opens the bot's Home tab. Pair with publish_home to render dynamic content.",
		PayloadType: AppHomeOpenedEvent{},
	})
}
