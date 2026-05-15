package nodes

import (
	"context"
	"fmt"

	"github.com/yogasw/wick/internal/agents/workflow"
	"github.com/yogasw/wick/internal/agents/workflow/dataset"
	"github.com/yogasw/wick/internal/agents/workflow/template"
)

// DatasetExecutor handles all 7 dataset_* node types.
type DatasetExecutor struct {
	Service dataset.Service
}

// NewDatasetExecutor wires the executor.
func NewDatasetExecutor(svc dataset.Service) *DatasetExecutor {
	return &DatasetExecutor{Service: svc}
}

// Execute dispatches per node.Type.
func (e *DatasetExecutor) Execute(ctx context.Context, n workflow.Node, rc *workflow.RunContext) (workflow.NodeOutput, error) {
	if e.Service == nil {
		return workflow.NodeOutput{}, fmt.Errorf("dataset: no service configured")
	}
	if n.Dataset == "" {
		return workflow.NodeOutput{}, fmt.Errorf("dataset: node %q missing dataset field", n.ID)
	}
	if err := e.enforceAccess(n, rc); err != nil {
		return workflow.NodeOutput{}, err
	}
	rctx := rc.RenderCtx()
	where, err := renderMap(n.Where, rctx)
	if err != nil {
		return workflow.NodeOutput{}, err
	}
	key, err := renderMap(n.Key, rctx)
	if err != nil {
		return workflow.NodeOutput{}, err
	}
	row, err := renderMap(n.RowValues, rctx)
	if err != nil {
		return workflow.NodeOutput{}, err
	}

	switch n.Type {
	case workflow.NodeDatasetExists:
		found, err := e.Service.Exists(n.Dataset, where)
		if err != nil {
			return workflow.NodeOutput{}, err
		}
		return workflow.NodeOutput{Verdict: boolStr(found), Fields: map[string]any{"found": found}}, nil

	case workflow.NodeDatasetGet:
		got, found, err := e.Service.Get(n.Dataset, key)
		if err != nil {
			return workflow.NodeOutput{}, err
		}
		verdict := "not_found"
		if found {
			verdict = "found"
		}
		return workflow.NodeOutput{Verdict: verdict, Fields: map[string]any{"found": found, "row": got}}, nil

	case workflow.NodeDatasetQuery:
		rows, err := e.Service.Query(n.Dataset, where, n.OrderBy, n.Limit, n.Offset)
		if err != nil {
			return workflow.NodeOutput{}, err
		}
		return workflow.NodeOutput{Result: rows, Fields: map[string]any{"rows": rows, "row_count": len(rows)}}, nil

	case workflow.NodeDatasetCount:
		count, err := e.Service.Count(n.Dataset, where)
		if err != nil {
			return workflow.NodeOutput{}, err
		}
		return workflow.NodeOutput{Result: count, Fields: map[string]any{"count": count}}, nil

	case workflow.NodeDatasetInsert:
		if err := e.Service.Insert(n.Dataset, row); err != nil {
			return workflow.NodeOutput{}, err
		}
		return workflow.NodeOutput{Fields: map[string]any{"success": true, "row": row}}, nil

	case workflow.NodeDatasetUpsert:
		action, err := e.Service.Upsert(n.Dataset, row)
		if err != nil {
			return workflow.NodeOutput{}, err
		}
		return workflow.NodeOutput{Fields: map[string]any{"action": action, "row": row}}, nil

	case workflow.NodeDatasetDelete:
		count, err := e.Service.Delete(n.Dataset, where)
		if err != nil {
			return workflow.NodeOutput{}, err
		}
		return workflow.NodeOutput{Fields: map[string]any{"deleted_count": count}}, nil
	}
	return workflow.NodeOutput{}, fmt.Errorf("dataset: unsupported node type %q", n.Type)
}

func (e *DatasetExecutor) enforceAccess(n workflow.Node, rc *workflow.RunContext) error {
	sc, err := e.Service.LoadSchema(n.Dataset)
	if err != nil {
		return err
	}
	if len(sc.Access.Workflows) > 0 && isWriteOp(n.Type) {
		if !containsStr(sc.Access.Workflows, rc.Workflow.Slug) {
			return fmt.Errorf("dataset %q: workflow %q not in access.workflows", n.Dataset, rc.Workflow.Slug)
		}
	}
	return nil
}

func isWriteOp(t workflow.NodeType) bool {
	switch t {
	case workflow.NodeDatasetInsert, workflow.NodeDatasetUpsert, workflow.NodeDatasetDelete:
		return true
	}
	return false
}

func renderMap(in map[string]any, rctx workflow.RenderCtx) (map[string]any, error) {
	if in == nil {
		return nil, nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if s, ok := v.(string); ok {
			rs, err := template.Render(s, rctx)
			if err != nil {
				return nil, fmt.Errorf("render %q: %w", k, err)
			}
			out[k] = rs
			continue
		}
		out[k] = v
	}
	return out, nil
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
