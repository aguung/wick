package parse

import (
	"strings"
	"testing"

	"github.com/yogasw/wick/internal/agents/workflow"
)

func stickyWorkflow() workflow.Workflow {
	return workflow.Workflow{
		ID:   "wf",
		Name: "wf",
		Triggers: []workflow.Trigger{
			{ID: "t1", Type: workflow.TriggerManual, EntryNode: "done", Description: "Manual run. Kenapa: testing."},
		},
		Graph: workflow.Graph{
			Nodes: []workflow.Node{
				{ID: "done", Type: workflow.NodeEnd, Description: "Stop. Kenapa: nothing else to do."},
				{ID: "note", Type: workflow.NodeStickyNote, Content: "# Block A", Color: "green"},
			},
		},
	}
}

func hasMsg(errs []Error, sub string) bool {
	for _, e := range errs {
		if strings.Contains(e.Path+" "+e.Message, sub) {
			return true
		}
	}
	return false
}

// A lone sticky note is neither an error nor an "unreachable" node.
func TestValidate_StickyNoteNotOrphan(t *testing.T) {
	r := Validate(stickyWorkflow())
	if !r.Ok() {
		t.Fatalf("unexpected errors: %v", r.Errors)
	}
	if hasMsg(r.Warnings, `"note" is unreachable`) {
		t.Fatalf("sticky note reported unreachable: %v", r.Warnings)
	}
	if len(r.Warnings) != 0 {
		t.Fatalf("want no warnings, got %v", r.Warnings)
	}
}

func TestValidate_StickyNoteEdgesAndEntryRejected(t *testing.T) {
	w := stickyWorkflow()
	w.Graph.Edges = []workflow.Edge{{From: "done", To: "note"}}
	if r := Validate(w); !hasMsg(r.Errors, "sticky_note") {
		t.Fatalf("edge to sticky note: want error, got %v", r.Errors)
	}

	w = stickyWorkflow()
	w.Triggers[0].EntryNode = "note"
	if r := Validate(w); !hasMsg(r.Errors, "entry_node") {
		t.Fatalf("sticky entry_node: want error, got %v", r.Errors)
	}

	w = stickyWorkflow()
	w.Graph.Nodes[1].Color = "pink"
	if r := Validate(w); !hasMsg(r.Errors, ".color") {
		t.Fatalf("bad color: want error, got %v", r.Errors)
	}
}

func TestValidate_MissingDescriptionWarns(t *testing.T) {
	w := stickyWorkflow()
	w.Triggers[0].Description = ""
	w.Graph.Nodes[0].Description = ""
	w.Graph.Nodes[1].Content = ""
	r := Validate(w)
	if !r.Ok() {
		t.Fatalf("missing description must be a warning, not an error: %v", r.Errors)
	}
	for _, want := range []string{"triggers[0].description", "graph.nodes[done].description", "graph.nodes[note].content"} {
		if !hasMsg(r.Warnings, want) {
			t.Errorf("missing warning %s in %v", want, r.Warnings)
		}
	}
}
