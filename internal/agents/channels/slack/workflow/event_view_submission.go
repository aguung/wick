package workflow

import "github.com/yogasw/wick/internal/agents/workflow/integration"

// ViewSubmissionEvent fires when a user submits a modal. CallbackID is
// the modal identifier (set when opening). Values is the flattened
// state.values shape Slack returns — Block ID → Action ID → typed
// value — so templates can do `{{.Trigger.values.subject.input.value}}`.
type ViewSubmissionEvent struct {
	User       string         `json:"user"`
	CallbackID string         `json:"callback_id"`
	ViewID     string         `json:"view_id"`
	ViewHash   string         `json:"view_hash"`
	PrivateMD  string         `json:"private_metadata"`
	TriggerID  string         `json:"trigger_id"`
	Values     map[string]any `json:"values"` // state.values: block_id → action_id → value
	Raw        map[string]any `json:"raw"`    // full view payload
}

func registerEventViewSubmission(reg *integration.Registry) {
	reg.RegisterEvent(integration.EventDescriptor{
		Channel:     Channel,
		Event:       "view_submission",
		Name:        "Slack: Modal submitted",
		Description: "Fires when a user clicks Submit on a modal. Match by callback_id to route different modal forms.",
		PayloadType: ViewSubmissionEvent{},
	})
}
