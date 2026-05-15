package channel

// SlackActionSpecs returns the Slack channel's outbound Action catalog
// per workflow-design §7. Concrete implementation lives in
// internal/agents/channels/slack/; this file is the schema document
// the workflow engine consumes for introspection, validation, and
// Block Kit payload building.
func SlackActionSpecs() []ActionSpec {
	return []ActionSpec{
		{
			ID:          "send_message",
			Description: "Post plain message to Slack channel. Returns posted ts.",
			InputSchema: map[string]any{
				"properties": map[string]any{
					"channel":   map[string]any{"type": "string"},
					"thread_ts": map[string]any{"type": "string"},
					"text":      map[string]any{"type": "string"},
				},
				"required": []any{"channel", "text"},
			},
			OutputSchema: map[string]any{
				"properties": map[string]any{
					"ts":      map[string]any{"type": "string"},
					"channel": map[string]any{"type": "string"},
				},
			},
		},
		{
			ID:          "reply_thread",
			Description: "Reply to existing Slack thread. Posts message with thread_ts set.",
			InputSchema: map[string]any{
				"properties": map[string]any{
					"channel": map[string]any{"type": "string"},
					"thread":  map[string]any{"type": "string"},
					"text":    map[string]any{"type": "string"},
				},
				"required": []any{"channel", "thread", "text"},
			},
		},
		{
			ID:          "send_dm",
			Description: "Send a direct message to a Slack user.",
			InputSchema: map[string]any{
				"properties": map[string]any{
					"user": map[string]any{"type": "string"},
					"text": map[string]any{"type": "string"},
				},
				"required": []any{"user", "text"},
			},
		},
		{
			ID:          "post_message_with_button",
			Description: "Post message with an interactive Block Kit button. Click fires event: action.",
			InputSchema: map[string]any{
				"properties": map[string]any{
					"channel":   map[string]any{"type": "string"},
					"thread_ts": map[string]any{"type": "string"},
					"text":      map[string]any{"type": "string"},
					"button": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"text":      map[string]any{"type": "string"},
							"action_id": map[string]any{"type": "string"},
							"value":     map[string]any{"type": "string"},
							"metadata":  map[string]any{"type": "object"},
						},
						"required": []any{"text", "action_id"},
					},
				},
				"required": []any{"channel", "text", "button"},
			},
		},
		{
			ID:          "open_modal",
			Description: "Open a Slack modal dialog. trigger_id must come from a prior action event (<3s old). Submit fires event: submission.",
			InputSchema: map[string]any{
				"properties": map[string]any{
					"trigger_id":  map[string]any{"type": "string"},
					"callback_id": map[string]any{"type": "string"},
					"title":       map[string]any{"type": "string"},
					"fields":      map[string]any{"type": "array"},
					"metadata":    map[string]any{"type": "object"},
					"submit_text": map[string]any{"type": "string"},
				},
				"required": []any{"trigger_id", "callback_id", "title", "fields"},
			},
			OutputSchema: map[string]any{
				"properties": map[string]any{"view_id": map[string]any{"type": "string"}},
			},
		},
		{
			ID:          "react",
			Description: "Add an emoji reaction to a message. Idempotent.",
			InputSchema: map[string]any{
				"properties": map[string]any{
					"channel":    map[string]any{"type": "string"},
					"message_ts": map[string]any{"type": "string"},
					"emoji":      map[string]any{"type": "string"},
				},
				"required": []any{"channel", "message_ts", "emoji"},
			},
		},
		{
			ID:          "update_message",
			Description: "Edit a posted Slack message (e.g. to mark a button consumed).",
			Destructive: true,
			InputSchema: map[string]any{
				"properties": map[string]any{
					"channel": map[string]any{"type": "string"},
					"ts":      map[string]any{"type": "string"},
					"text":    map[string]any{"type": "string"},
				},
				"required": []any{"channel", "ts", "text"},
			},
		},
	}
}

// SlackTriggerSpec is the Slack channel's TriggerSpec catalog.
func SlackTriggerSpec() TriggerSpec {
	return TriggerSpec{
		Type:        "channel",
		Events:      []string{"message", "action", "submission", "reaction", "mention"},
		Description: "Slack events forwarded by the wick Slack channel adapter.",
		MatchSchema: map[string]any{
			"properties": map[string]any{
				"keywords":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"regex":             map[string]any{"type": "string"},
				"mention_bot":       map[string]any{"type": "boolean"},
				"from_threads_only": map[string]any{"type": "boolean"},
			},
		},
	}
}

// BuildSlackButtonPayload builds the Block Kit body for
// post_message_with_button.
func BuildSlackButtonPayload(args map[string]any) map[string]any {
	btn, _ := args["button"].(map[string]any)
	text, _ := args["text"].(string)
	return map[string]any{
		"channel": args["channel"],
		"text":    text,
		"blocks": []any{
			map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}},
			map[string]any{
				"type": "actions",
				"elements": []any{
					map[string]any{
						"type":      "button",
						"text":      map[string]any{"type": "plain_text", "text": btn["text"]},
						"action_id": btn["action_id"],
						"value":     btn["value"],
					},
				},
			},
		},
		"metadata": btn["metadata"],
	}
}
