package agents

import (
	"strings"
	"testing"

	wf "github.com/yogasw/wick/internal/agents/workflow"
)

// TestTriggerFanoutRoundtrip locks in the bug fix where multiple
// trigger → node edges drawn on the canvas survived save + reload.
// Before the fix, only the entry node kept its trigger edge — every
// other fan-out target lost its line on refresh.
func TestTriggerFanoutRoundtrip(t *testing.T) {
	// Drawflow payload: trigger fans out to two regular nodes.
	body := `{
	  "drawflow":{"Home":{"data":{
	    "1":{"id":1,"name":"__trigger__","class":"node-trigger","html":"",
	         "data":{"id":"__trigger__","type":"trigger","data":{"kind":"manual"}},
	         "pos_x":100,"pos_y":50,
	         "inputs":{},
	         "outputs":{"output_1":{"connections":[
	             {"node":"2","output":"input_1"},
	             {"node":"3","output":"input_1"}
	         ]}}},
	    "2":{"id":2,"name":"alpha","class":"node-shell","html":"",
	         "data":{"id":"alpha","type":"shell","data":{"command":["echo","a"]}},
	         "pos_x":300,"pos_y":80,
	         "inputs":{"input_1":{"connections":[{"node":"1","input":"output_1"}]}},
	         "outputs":{"output_1":{"connections":[]}}},
	    "3":{"id":3,"name":"beta","class":"node-shell","html":"",
	         "data":{"id":"beta","type":"shell","data":{"command":["echo","b"]}},
	         "pos_x":300,"pos_y":260,
	         "inputs":{"input_1":{"connections":[{"node":"1","input":"output_1"}]}},
	         "outputs":{"output_1":{"connections":[]}}}
	  }}}}`

	w, err := drawflowJSONToWorkflow("t", body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Codec stores the fan-out targets under _canvas.trigger_edges.
	tedges := triggerTargetsFromCanvas(w)
	if len(tedges) != 2 {
		t.Fatalf("expected 2 trigger fan-out targets, got %d (%v)", len(tedges), tedges)
	}
	has := map[string]bool{}
	for _, n := range tedges {
		has[n] = true
	}
	if !has["alpha"] || !has["beta"] {
		t.Errorf("trigger fan-out missing target: %v", tedges)
	}

	// Round-trip back to Drawflow JSON and confirm the phantom
	// trigger still emits two outgoing connections.
	out, err := workflowToDrawflowJSON(w)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(out, `"alpha"`) || !strings.Contains(out, `"beta"`) {
		t.Errorf("round-trip output missing target node names: %s", out)
	}
}

// TestTriggerEdgesNotInGraphEdges — the trigger fan-out lives in
// _canvas.trigger_edges, never in Graph.Edges. The engine routes from
// workflow.Triggers + Graph.Entry, so polluting Graph.Edges with
// trigger sources would break the validator.
func TestTriggerEdgesNotInGraphEdges(t *testing.T) {
	body := `{"drawflow":{"Home":{"data":{
	  "1":{"id":1,"name":"__trigger__","class":"node-trigger","html":"",
	       "data":{"id":"__trigger__","type":"trigger","data":{"kind":"manual"}},
	       "pos_x":0,"pos_y":0,
	       "inputs":{},
	       "outputs":{"output_1":{"connections":[{"node":"2","output":"input_1"}]}}},
	  "2":{"id":2,"name":"only","class":"node-shell","html":"",
	       "data":{"id":"only","type":"shell","data":{"command":["x"]}},
	       "pos_x":100,"pos_y":100,
	       "inputs":{"input_1":{"connections":[{"node":"1","input":"output_1"}]}},
	       "outputs":{"output_1":{"connections":[]}}}
	}}}}`
	w, err := drawflowJSONToWorkflow("t", body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, e := range w.Graph.Edges {
		if e.From == "__trigger__" {
			t.Errorf("trigger edge leaked into Graph.Edges: %+v", e)
		}
	}
}

// TestCanvasPositionsRoundtrip — node positions saved on the canvas
// survive load (the int-vs-float YAML decode bug had wiped them).
func TestCanvasPositionsRoundtrip(t *testing.T) {
	w := wf.Workflow{
		Slug:     "p",
		ID:       "id-p",
		Triggers: []wf.Trigger{{Type: wf.TriggerManual}},
		Graph: wf.Graph{
			Entry: "n1",
			Nodes: []wf.Node{{ID: "n1", Type: wf.NodeShell, Command: []string{"echo"}}},
		},
		Canvas: map[string]any{
			"positions": map[string]any{
				// YAML decoder yields ints for whole numbers — codec
				// must accept both int and float64.
				"n1": map[string]any{"x": 320, "y": 180},
			},
		},
	}
	got := canvasPositions(w)
	if got["n1"][0] != 320 || got["n1"][1] != 180 {
		t.Errorf("position lost: %+v", got)
	}
}
