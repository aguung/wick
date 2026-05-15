package workflow

import "github.com/yogasw/wick/internal/agents/workflow/integration"

// ViewClosedEvent fires when a user dismisses a modal without
// submitting (X button or Esc). Use it to clean up state or log
// abandonment.
type ViewClosedEvent struct {
	User       string `json:"user"`
	CallbackID string `json:"callback_id"`
	ViewID     string `json:"view_id"`
	PrivateMD  string `json:"private_metadata"`
}

func registerEventViewClosed(reg *integration.Registry) {
	reg.RegisterEvent(integration.EventDescriptor{
		Channel:     Channel,
		Event:       "view_closed",
		Name:        "Slack: Modal closed",
		Description: "Fires when a user dismisses a modal without submitting (close button or Esc).",
		PayloadType: ViewClosedEvent{},
	})
}
