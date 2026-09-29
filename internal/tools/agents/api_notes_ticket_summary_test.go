package agents

import (
	"encoding/json"
	"testing"

	"github.com/yogasw/wick/internal/agents/ticket"
)

func TestNoteTicketSummaryCarriesFields(t *testing.T) {
	tk := ticket.Ticket{
		ID: "T-1", Title: "Kalender dokter", Status: "in_progress", Body: "Desc",
		Fields: map[string]string{"app_code": "locot-uv3", "slack": "https://example.slack.com/archives/C1/p1"},
	}
	got := noteTicketSummary(tk)
	for k, want := range map[string]string{"id": "T-1", "title": "Kalender dokter", "status": "in_progress", "body": "Desc"} {
		if got[k] != want {
			t.Errorf("%s = %v, want %q", k, got[k], want)
		}
	}
	fields, ok := got["fields"].(map[string]string)
	if !ok {
		t.Fatalf("fields = %T, want map[string]string", got["fields"])
	}
	if fields["app_code"] != "locot-uv3" || fields["slack"] != "https://example.slack.com/archives/C1/p1" {
		t.Errorf("fields = %v", fields)
	}
}

// The rail reads fields.<key> directly; a ticket that never had a field
// must serialise as {} rather than null.
func TestNoteTicketSummaryFieldsNeverNull(t *testing.T) {
	b, err := json.Marshal(noteTicketSummary(ticket.Ticket{ID: "T-2"}))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["fields"]) != "{}" {
		t.Errorf(`fields = %s, want {}`, m["fields"])
	}
}
