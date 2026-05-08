package event

import "testing"

// parseAll feeds lines through a parser and returns all non-Unknown
// events. Errors fail the test.
func parseAll(t *testing.T, p Parser, lines []string) []AgentEvent {
	t.Helper()
	var out []AgentEvent
	for i, line := range lines {
		ev, err := p.Parse(line)
		if err != nil {
			t.Fatalf("line %d parse: %v\nline: %s", i, err, line)
		}
		if ev.Type == Unknown {
			continue
		}
		out = append(out, ev)
	}
	return out
}

func TestClaudeParserSessionStartOnce(t *testing.T) {
	p := NewClaudeParser()
	lines := []string{
		`{"type":"message_start","session_id":"abc-123","message":{"id":"m1"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"},"session_id":"abc-123"}`,
	}
	events := parseAll(t, p, lines)
	if len(events) != 2 {
		t.Fatalf("events: got %d, want 2", len(events))
	}
	if events[0].Type != SessionStart || events[0].SessionID != "abc-123" {
		t.Fatalf("first event: %+v", events[0])
	}
	if events[1].Type != TextDelta || events[1].Text != "hi" {
		t.Fatalf("second event: %+v", events[1])
	}
	if p.SessionID() != "abc-123" {
		t.Fatalf("SessionID(): %q", p.SessionID())
	}
}

func TestClaudeParserTextDelta(t *testing.T) {
	p := NewClaudeParser()
	lines := []string{
		`{"type":"message_start","session_id":"s1","message":{"id":"m"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello "}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_stop"}`,
	}
	events := parseAll(t, p, lines)
	// SessionStart, 2× TextDelta, Done = 4 events.
	if len(events) != 4 {
		t.Fatalf("event count: %d (%v)", len(events), events)
	}
	if events[1].Text != "hello " || events[2].Text != "world" {
		t.Fatalf("text deltas: %+v %+v", events[1], events[2])
	}
	if events[3].Type != Done {
		t.Fatalf("last event not Done: %+v", events[3])
	}
}

func TestClaudeParserToolUseBuffersInputJSON(t *testing.T) {
	p := NewClaudeParser()
	lines := []string{
		`{"type":"message_start","session_id":"s1","message":{"id":"m"}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t1","name":"Bash"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":" -la\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
	}
	events := parseAll(t, p, lines)
	// SessionStart + ToolUse only — deltas are absorbed.
	if len(events) != 2 {
		t.Fatalf("event count: %d (%+v)", len(events), events)
	}
	tu := events[1]
	if tu.Type != ToolUse {
		t.Fatalf("tool use: %+v", tu)
	}
	if tu.ToolName != "Bash" {
		t.Fatalf("tool name: %q", tu.ToolName)
	}
	if tu.ToolInput != `{"command":"ls -la"}` {
		t.Fatalf("tool input: %q", tu.ToolInput)
	}
}

func TestClaudeParserThinking(t *testing.T) {
	p := NewClaudeParser()
	lines := []string{
		`{"type":"message_start","session_id":"s1"}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"weighing options"}}`,
		`{"type":"content_block_stop","index":0}`,
	}
	events := parseAll(t, p, lines)
	if len(events) != 2 {
		t.Fatalf("events: %d (%+v)", len(events), events)
	}
	if events[1].Type != Thinking || events[1].Text != "weighing options" {
		t.Fatalf("thinking event: %+v", events[1])
	}
}

func TestClaudeParserError(t *testing.T) {
	p := NewClaudeParser()
	ev, err := p.Parse(`{"type":"error","error":{"message":"rate limited"}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.Type != Error || ev.ErrorMsg != "rate limited" {
		t.Fatalf("event: %+v", ev)
	}
}

func TestClaudeParserBlankLine(t *testing.T) {
	p := NewClaudeParser()
	ev, err := p.Parse("   ")
	if err != nil {
		t.Fatalf("blank line errored: %v", err)
	}
	if ev.Type != Unknown {
		t.Fatalf("blank should be Unknown, got %v", ev.Type)
	}
}

func TestClaudeParserMalformedReturnsError(t *testing.T) {
	p := NewClaudeParser()
	if _, err := p.Parse("not json"); err == nil {
		t.Fatal("expected parse error on garbage input")
	}
}
