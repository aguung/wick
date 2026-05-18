---
name: workflow-node-module
description: Use for ANY work on a workflow node type — creating a new node executor under internal/agents/workflow/nodes/, refactoring/improving an existing one, adding fields to a node's schema, or wiring the executor into setup/manager.go. Covers the full executor contract — workflow.Executor interface, engine.NodeDescriptor for MCP catalog, schema reflection via wick:"..." tags, output field documentation, the engine.Register/RegisterWithDesc dispatch, and the goroutine-context discipline shared with connectors. Also mandates the "Descriptor() method is the source of truth" rule — schema and output docs live next to the executor, never in mcp/.
allowed-tools: Read, Grep, Glob, Edit, Write, Bash
paths:
  - "internal/agents/workflow/nodes/**"
  - "internal/agents/workflow/engine/**"
  - "internal/agents/workflow/setup/manager.go"
  - "internal/agents/workflow/types.go"
  - "internal/agents/workflow/executor.go"
  - "internal/agents/workflow/template/**"
  - "internal/agents/workflow/integration/schema.go"
  - "internal/agents/workflow/mcp/mcp.go"
---

# Workflow Node Module — wick core

Activate this skill whenever the user touches a workflow node type — creating, improving, fixing, or adding fields. When editing an existing node, audit it against the rules below and bring it up to spec as part of the change.

## Mental model

A node is one unit of execution inside a workflow graph.

- One `Executor` impl per node type lives under `internal/agents/workflow/nodes/<type>.go`.
- Engine dispatches by `node.Type` (resolved via `Engine.Executors` map populated at boot in `setup/manager.go`).
- Schema + output docs live next to the executor as a per-node schema struct + `Descriptor()` method. **Single source of truth** — MCP catalog (`workflow_node_types`) is built from `Engine.Descriptors`, no hardcode in `mcp/`.
- Adding a new node type = 2 files touched: `nodes/<type>.go` (executor + schema + Descriptor) and `setup/manager.go` (one `eng.Register` line). Plus a `NodeType` constant in `types.go`.

The catalog flow:

```
eng.Register(workflow.NodeFoo, nodes.NewFooExecutor())
  → engine.Register checks if executor implements engine.Describer
  → calls Descriptor() → stores engine.NodeDescriptor in Engine.Descriptors[t]
  → mcp.NodeTypesCatalog(eng) builds workflow_node_types response from Descriptors map
```

## Before you build

Lock down the contract before writing code:

| What to gather | Why |
|---|---|
| **What does this node DO** at runtime | Decides whether it should be a new node type at all, or just a new arg to an existing one (e.g. add a flag to http, not a new "http_with_retry" type) |
| **Inputs** the node accepts | Becomes the schema struct fields with `wick:"..."` tags |
| **Outputs** the node produces (field names + types) | Becomes the `Output` map in `Descriptor()`. References as `{{.Node.<id>.<field>}}` downstream |
| **Side effects** — pure compute? network? mutate dataset? | Decides whether the executor needs `c.Context()` discipline (any network/blocking call MUST honor context) |
| **Failure modes** — what raises an error vs returns empty | Drives error wrapping in `Execute` |

## File layout

Default — one file under `internal/agents/workflow/nodes/`:

```
internal/agents/workflow/nodes/
  myop.go    # Executor struct + NewMyOpExecutor + Execute + schema struct + Descriptor()
```

Pattern (read [`http.go`](../../../internal/agents/workflow/nodes/http.go) as canonical):

```go
package nodes

import (
    "context"

    "github.com/yogasw/wick/internal/agents/workflow"
    "github.com/yogasw/wick/internal/agents/workflow/engine"
    "github.com/yogasw/wick/internal/agents/workflow/integration"
    "github.com/yogasw/wick/internal/agents/workflow/template"
)

// MyOpExecutor performs <what it does>.
type MyOpExecutor struct {
    // dependencies injected via constructor (registry pointers, http clients, etc.)
}

// NewMyOpExecutor wires the executor.
func NewMyOpExecutor() *MyOpExecutor { return &MyOpExecutor{} }

// Execute runs the op described by node n.
func (e *MyOpExecutor) Execute(ctx context.Context, n workflow.Node, rc *workflow.RunContext) (workflow.NodeOutput, error) {
    rctx := rc.RenderCtx()
    // 1. validate required fields on n
    // 2. render any template-bearing strings via template.Render(s, rctx)
    // 3. call into pure logic or external I/O — honor ctx
    // 4. return workflow.NodeOutput{Fields: map[string]any{...}}
    _ = rctx
    return workflow.NodeOutput{}, nil
}

// myOpSchema reflects into JSON schema for workflow_node_types — single
// source of truth for AI consumers and the inspector UI.
type myOpSchema struct {
    Required  string `wick:"required;key=required;desc=Mandatory input"`
    Optional  string `wick:"key=optional;desc=Optional input"`
    Enum      string `wick:"key=mode;dropdown=a|b|c;desc=Pick one"`
    Multiline string `wick:"key=body;textarea;desc=Multi-line input"`
}

// Descriptor exposes schema + docs for the MCP catalog.
func (e *MyOpExecutor) Descriptor() engine.NodeDescriptor {
    return engine.NodeDescriptor{
        Description: "Action verb. Returns <output shape>.",
        WhenToUse:   "Use when <condition>; prefer X over this when <other condition>.",
        Example:     "- id: myop\n  type: my_op\n  required: foo\n  mode: a",
        Schema:      integration.StructSchema(myOpSchema{}),
        Output: map[string]string{
            "result": "string — rendered output",
        },
    }
}
```

## Wire into the engine

Two locations:

### 1. `internal/agents/workflow/types.go` — add `NodeType` constant

```go
const (
    // existing types …
    NodeMyOp NodeType = "my_op"
)
```

Also add any new fields you read in `Execute` to the `Node` struct (with `yaml:"…"` tag matching your schema key).

### 2. `internal/agents/workflow/setup/manager.go` — register

```go
eng.Register(workflow.NodeMyOp, nodes.NewMyOpExecutor())
```

`Register` auto-detects the `Describer` interface and captures the descriptor — no separate `RegisterWithDesc` call needed.

Exception: when **one executor instance serves multiple node types** (like `DatasetExecutor` handling 7 dataset_* types), use `RegisterWithDesc(t, exec, desc)` and provide a helper that switches on the type:

```go
for _, t := range []workflow.NodeType{NodeFoo, NodeBar, NodeBaz} {
    eng.RegisterWithDesc(t, exec, nodes.FooDescriptor(t))
}
```

## Contract

### `Executor` interface (`workflow/executor.go`)

```go
type Executor interface {
    Execute(ctx context.Context, node Node, rctx *RunContext) (NodeOutput, error)
}
```

### `Describer` interface (`engine/engine.go`)

```go
type Describer interface {
    Descriptor() engine.NodeDescriptor
}
```

Implement on the executor struct (pointer receiver). Optional but **strongly recommended** — without it the node appears in workflows but never surfaces in `workflow_node_types` so AI cannot discover its schema.

### `NodeDescriptor` fields

| Field | Purpose |
|---|---|
| `Description` | One-liner shown in palette + AI catalog. Action verbs, describe input/output shape. |
| `WhenToUse` | Disambiguation — when to pick this node over the closest alternative |
| `Example` | YAML snippet copy-pasteable into workflow.yaml. Use real field values, not placeholders |
| `Schema` | `integration.StructSchema(myOpSchema{})` — never hardcode the map |
| `Output` | `map[string]string` field name → description. Becomes `{{.Node.<id>.<key>}}` reference in templates |

### Schema struct tags

Same `wick:"..."` grammar as Tools / Connectors / Channel events. See the **config-tags** skill (sibling folder) for the full grid. Common modifiers:

| Tag | Effect |
|---|---|
| `required` | Field must be present |
| `key=name` | Override the snake_cased field name |
| `desc=...` | Help text — surfaces in inspector + AI schema |
| `textarea` | Multi-line input widget |
| `dropdown=a\|b\|c` | Enum constraint |
| `picker=<source>` | Lookup-backed picker (rare for nodes; common for channel match) |

### `NodeOutput` shape

```go
type NodeOutput struct {
    Verdict    string         // routing key for classify/branch
    Confidence float64
    Reasoning  string
    Result     any
    Fields     map[string]any // merged into top-level when exposed as {{.Node.<id>.X}}
}
```

For most nodes use `Fields` for typed outputs:

```go
return workflow.NodeOutput{Fields: map[string]any{
    "status": resp.StatusCode,
    "body":   string(raw),
}}, nil
```

Match the keys you put in `Fields` to the `Output` map in `Descriptor()` — that's the contract AI relies on.

## Template rendering

Args that bear user-supplied expressions (URL, body, header values, command, etc.) MUST be rendered via `template.Render` or `template.RenderInto`:

```go
import "github.com/yogasw/wick/internal/agents/workflow/template"

rctx := rc.RenderCtx()
url, err := template.Render(n.URL, rctx)
```

For nodes that accept a free-form `Args map[string]any` (like channel / connector), use `renderArgsWithModes(n.Args, n.ArgModes, rc)` — that helper honors per-field `fixed` vs `expression` mode from the inspector.

Available built-in template functions live in `template/template.go::BuiltinFuncs` — auto-exposed to AI via `workflow_workspace.format_contracts.template_functions`. To add a new function: add to both `BuiltinFuncs` (impl) and `BuiltinFuncDocs` (name + description) — they're paired, single source of truth.

## Golden rules for `Execute`

1. **MUST** honor `ctx`. Any network call uses `http.NewRequestWithContext(ctx, …)`. Any blocking op selects on `<-ctx.Done()`. Skip = goroutine leak on workflow cancel.
2. **MUST** validate required fields on `n` early — return error before any I/O.
3. **MUST** render template-bearing strings before use. Raw `{{.Event.Payload.x}}` substrings in URL / body = bug.
4. **MUST** populate `Fields` keys that match the `Output` map you advertised in `Descriptor()`. Renaming a field is a breaking change for every downstream `{{.Node.<id>.X}}` reference.
5. **SHOULD** wrap upstream / dependency errors with `fmt.Errorf("…: %w", err)` so the error chain renders cleanly in run history.
6. **SHOULD** use `provider.Registry`, `connector.Registry`, etc. injected via constructor — never global singletons.
7. **MAY** emit progress logging via `rc` if your op is long-running.

## Anti-patterns

- ❌ Hardcoding schema in `mcp/mcp.go::NodeTypesCatalog` — `Descriptor()` is the only source.
- ❌ `http.NewRequest` without context — goroutine leak on workflow cancel.
- ❌ Field name in `Fields` map ≠ key in `Output` doc — AI writes broken `{{.Node.X.Y}}` based on doc, runtime returns nothing.
- ❌ Reading `n.<Field>` for a field that isn't declared in the schema struct — works at runtime but invisible to AI/inspector.
- ❌ Skipping `eng.Register` in `setup/manager.go` — engine returns "no executor for type X" at first run.
- ❌ Putting the schema struct in a different package (`mcp/`, `types.go`) — defeats the purpose of co-location.
- ❌ Mutating state across `Execute` calls on the same executor — engine reuses one executor instance for every concurrent run.

## Special node types

| Type | Notes |
|---|---|
| **One executor, many node types** | Like `DatasetExecutor` — provide `<Name>Descriptor(t workflow.NodeType) engine.NodeDescriptor` switch helper. Use `RegisterWithDesc` per type. |
| **Branching nodes** | Return `Verdict` (string) — engine filters outgoing edges by matching `case:` label. See `BranchExecutor`. |
| **Classify nodes** | Same `Verdict` mechanism; provider integration via injected `provider.Registry`. |
| **End nodes** | Terminator. Set `Result` field — surfaces as `{{.Run.final_result}}`. |
| **Parallel / merge** | No new schema fields needed — engine reads `Branches` / `Inputs` / `Strategy` directly off `Node`. Descriptor doc should explain the fan-out/fan-in shape. |

## Verifying your work

```bash
go build ./internal/...
```

Smoke from MCP:

1. Boot wick — `go run main.go server &`
2. Call `workflow_node_types` — verify your new entry appears with the schema you declared.
3. Create a draft workflow that uses the node, call `workflow_validate` — confirms no schema errors.
4. Call `workflow_simulate` with a synthetic event — confirms `Execute` runs and outputs match your `Output` doc.
5. Kill the port.

## When to ask before acting

- **New node type vs new arg on existing node** — confirm with user. Adding a field is almost always cheaper.
- **Removing a node type** — orphans every workflow.yaml that references it. Migration plan needs to land same change.
- **Renaming output fields** — breaks every `{{.Node.<id>.X}}` reference in user workflows. Treat as breaking change.
- **New template function** — confirm name + signature with user; functions are global to every workflow.

## Reference

- Canonical example: [`nodes/http.go`](../../../internal/agents/workflow/nodes/http.go) — schema + Descriptor pattern
- Multi-type executor: [`nodes/dataset.go`](../../../internal/agents/workflow/nodes/dataset.go) — `DatasetDescriptor(t)` switch
- Engine + Descriptor types: [`engine/engine.go`](../../../internal/agents/workflow/engine/engine.go)
- Executor interface: [`executor.go`](../../../internal/agents/workflow/executor.go)
- Node struct + NodeType constants: [`types.go`](../../../internal/agents/workflow/types.go)
- Wiring site: [`setup/manager.go`](../../../internal/agents/workflow/setup/manager.go)
- MCP catalog builder: [`mcp/mcp.go::NodeTypesCatalog`](../../../internal/agents/workflow/mcp/mcp.go)
- Template engine: [`template/template.go`](../../../internal/agents/workflow/template/template.go)
- Tag grammar: sibling `config-tags` skill folder
