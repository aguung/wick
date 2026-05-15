package nodes

import (
	"context"
	"fmt"

	"github.com/yogasw/wick/internal/agents/workflow"
	"github.com/yogasw/wick/internal/agents/workflow/template"
)

// TransformExecutor runs an in-process transform on an input value.
//
//   - gotemplate (default) — Go template render
//   - jsonpath              — minimal walker (placeholder)
//   - jq                    — not implemented in this build
type TransformExecutor struct{}

// NewTransformExecutor builds the transform executor.
func NewTransformExecutor() *TransformExecutor { return &TransformExecutor{} }

// Execute runs the transform described by node n.
func (e *TransformExecutor) Execute(ctx context.Context, n workflow.Node, rc *workflow.RunContext) (workflow.NodeOutput, error) {
	rctx := rc.RenderCtx()
	engine := n.Engine
	if engine == "" {
		engine = "gotemplate"
	}
	switch engine {
	case "gotemplate":
		out, err := template.Render(n.Expression, rctx)
		if err != nil {
			return workflow.NodeOutput{}, err
		}
		return workflow.NodeOutput{Result: out, Fields: map[string]any{"result": out}}, nil
	case "jsonpath":
		inputRendered, err := template.Render(n.Input, rctx)
		if err != nil {
			return workflow.NodeOutput{}, err
		}
		return workflow.NodeOutput{Result: inputRendered, Fields: map[string]any{"result": inputRendered}}, nil
	case "jq":
		return workflow.NodeOutput{}, fmt.Errorf("transform jq: not implemented in this build")
	default:
		return workflow.NodeOutput{}, fmt.Errorf("transform: unknown engine %q", engine)
	}
}
