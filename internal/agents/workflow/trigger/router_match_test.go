package trigger

import (
	"testing"

	"github.com/yogasw/wick/internal/agents/workflow"
)

// TestMatchEventPayload_Empty — empty spec map fires every event.
// Used to assert the dump-all default when MatchEnabled is true but
// the form was never filled.
func TestMatchEventPayload_Empty(t *testing.T) {
	if !matchEventPayload(nil, map[string]any{"text": "hi"}) {
		t.Errorf("nil spec should pass")
	}
	if !matchEventPayload(map[string]any{}, map[string]any{"text": "hi"}) {
		t.Errorf("empty spec map should pass")
	}
}

// TestMatchEventPayload_StringEquality — plain string spec equals
// the payload key. Empty-string spec is treated as "no filter on
// this key" so the inspector can leave optional fields blank.
func TestMatchEventPayload_StringEquality(t *testing.T) {
	cases := []struct {
		name string
		spec map[string]any
		got  map[string]any
		want bool
	}{
		{"exact match", map[string]any{"mode": "all"}, map[string]any{"mode": "all"}, true},
		{"mismatch", map[string]any{"mode": "all"}, map[string]any{"mode": "whitelist"}, false},
		{"empty spec value skips key", map[string]any{"mode": ""}, map[string]any{"mode": "whitelist"}, true},
		{"missing payload key fails", map[string]any{"mode": "all"}, map[string]any{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := matchEventPayload(c.spec, c.got)
			if got != c.want {
				t.Errorf("matchEventPayload(%v, %v) = %v, want %v", c.spec, c.got, got, c.want)
			}
		})
	}
}

// TestMatchEventPayload_PickerMembership — picker output is JSON
// `[{"id":"C1","name":"general"},...]`. Spec passes when
// payload[key] is in the list of ids.
func TestMatchEventPayload_PickerMembership(t *testing.T) {
	spec := map[string]any{
		"channel_id": `[{"id":"C123","name":"general"},{"id":"C456","name":"random"}]`,
	}
	if !matchEventPayload(spec, map[string]any{"channel_id": "C123"}) {
		t.Errorf("C123 should match (in whitelist)")
	}
	if !matchEventPayload(spec, map[string]any{"channel_id": "C456"}) {
		t.Errorf("C456 should match (in whitelist)")
	}
	if matchEventPayload(spec, map[string]any{"channel_id": "C999"}) {
		t.Errorf("C999 should fail (not in whitelist)")
	}
	if matchEventPayload(spec, map[string]any{}) {
		t.Errorf("missing payload key should fail when whitelist non-empty")
	}
}

// TestMatchEventPayload_PickerEmpty — an empty picker list means
// "no chips selected yet" so the filter is dormant. Reflects the
// inspector UX where toggling Filter Events on but leaving the
// chip list empty shouldn't kill the trigger.
func TestMatchEventPayload_PickerEmpty(t *testing.T) {
	cases := []map[string]any{
		{"channel_id": `[]`},
	}
	for _, spec := range cases {
		if !matchEventPayload(spec, map[string]any{"channel_id": "C123"}) {
			t.Errorf("empty picker spec %v should pass", spec)
		}
	}
}

// TestMatchEventPayload_Multi — every key in the spec must pass
// (AND-combined). One mismatch kills the run.
func TestMatchEventPayload_Multi(t *testing.T) {
	spec := map[string]any{
		"mode":       "whitelist",
		"channel_id": `[{"id":"C123","name":"general"}]`,
	}
	if !matchEventPayload(spec, map[string]any{"mode": "whitelist", "channel_id": "C123"}) {
		t.Errorf("both match should pass")
	}
	if matchEventPayload(spec, map[string]any{"mode": "all", "channel_id": "C123"}) {
		t.Errorf("mode mismatch should fail")
	}
	if matchEventPayload(spec, map[string]any{"mode": "whitelist", "channel_id": "C999"}) {
		t.Errorf("channel mismatch should fail")
	}
}

// TestTriggerPassesRouterChecks_MatchDisabled — MatchEnabled=false
// (the default) means dump-all even if Match is populated. Guards
// against accidental filtering on workflows that were edited then
// left disabled.
func TestTriggerPassesRouterChecks_MatchDisabled(t *testing.T) {
	tr := workflow.Trigger{
		Type:         workflow.TriggerChannel,
		ChannelName:  "slack",
		Event:        "message",
		MatchEnabled: false,
		Match:        map[string]any{"mode": "whitelist"},
	}
	evt := workflow.Event{
		Type:    string(workflow.TriggerChannel),
		Channel: "slack",
		Subtype: "message",
		Payload: map[string]any{"mode": "all"},
	}
	if !triggerPassesRouterChecks(tr, evt) {
		t.Errorf("disabled match should let event through")
	}
}

// TestTriggerPassesRouterChecks_MatchEnabledFilters — MatchEnabled=true
// + Match populated → router skips runs whose payload doesn't match.
func TestTriggerPassesRouterChecks_MatchEnabledFilters(t *testing.T) {
	tr := workflow.Trigger{
		Type:         workflow.TriggerChannel,
		ChannelName:  "slack",
		Event:        "message",
		MatchEnabled: true,
		Match: map[string]any{
			"channel_id": `[{"id":"C123","name":"ops"}]`,
		},
	}
	pass := workflow.Event{
		Type:    string(workflow.TriggerChannel),
		Channel: "slack",
		Subtype: "message",
		Payload: map[string]any{"channel_id": "C123"},
	}
	fail := workflow.Event{
		Type:    string(workflow.TriggerChannel),
		Channel: "slack",
		Subtype: "message",
		Payload: map[string]any{"channel_id": "C999"},
	}
	if !triggerPassesRouterChecks(tr, pass) {
		t.Errorf("C123 in whitelist should pass")
	}
	if triggerPassesRouterChecks(tr, fail) {
		t.Errorf("C999 not in whitelist should fail")
	}
}
