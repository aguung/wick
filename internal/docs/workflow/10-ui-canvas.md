## 10. UI — canvas editor

Tab baru `Workflows` di [internal/tools/agents/](../tools/agents/),
sejajar Sessions/Workspaces/Presets/Providers.

### Files

```
internal/tools/agents/
  workflows.go              # handlers
  view/
    workflows_list_templ.go
    workflows_editor_templ.go
    workflows_runs_templ.go
  static/
    workflow-canvas.js      # Drawflow integration
    workflow-canvas.css
```

### Routes

```go
r.GET("/workflows", listPage)
r.GET("/workflows/{slug}", detailPage)
r.GET("/workflows/{slug}/edit", editorPage)
r.POST("/workflows", create)
r.POST("/workflows/{slug}", update)
r.POST("/workflows/{slug}/toggle", toggle)
r.POST("/workflows/{slug}/approve", approve)
r.POST("/workflows/{slug}/run", runNow)
r.POST("/workflows/{slug}/test", runTest)
r.DELETE("/workflows/{slug}", delete)

// Canvas API (UI + MCP canvas ops backend)
r.POST("/workflows/{slug}/nodes", addNode)
r.PATCH("/workflows/{slug}/nodes/{id}", updateNode)
r.DELETE("/workflows/{slug}/nodes/{id}", deleteNode)
r.POST("/workflows/{slug}/edges", connect)
r.DELETE("/workflows/{slug}/edges/{from}/{to}", disconnect)

// File explorer
r.GET("/workflows/{slug}/files", listFiles)
r.GET("/workflows/{slug}/files/{path...}", readFile)
r.PUT("/workflows/{slug}/files/{path...}", writeFile)

// Run history + replay
r.GET("/workflows/{slug}/runs", listRuns)
r.GET("/workflows/{slug}/runs/{id}", runDetail)
r.POST("/workflows/{slug}/runs/{id}/replay", replay)
r.POST("/workflows/{slug}/runs/{id}/resume", resume)
```

### List page

Tabel: Name, Triggers (badges), Nodes count, Last Run, Status. Filter
`unapproved` di atas. Tombol `+ New Workflow` → modal pilih template
(empty / support-triage / incident-response / daily-digest).

### Editor page — 3-pane layout

```
┌─────────────────────────────────────────────────────────────────┐
│  Header: name | type | enabled toggle | Save | Test | Approve   │
├──────────┬──────────────────────────────────┬───────────────────┤
│          │                                  │                   │
│  Node    │   Canvas (Drawflow)              │  Inspector        │
│  palette │   - drag-drop nodes              │  (selected node)  │
│          │   - draw edges between nodes     │  - id, label      │
│  classify│   - click node → inspector       │  - type-spec      │
│  agent   │   - delete edge / node           │    fields         │
│  skill   │                                  │  - schema-driven  │
│  shell   │   [trigger]                      │    form           │
│  ...     │        ↓                         │                   │
│          │   [classify]                     │  Output ref       │
│  --      │     ├─bug→ [skill:create-ticket] │  available:       │
│          │     └─...                        │  {{.Event.Payload.text}}  │
│ Triggers │                                  │  {{.Node.x.y}}    │
│  + cron  │                                  │                   │
│  + ...   │                                  │  [test fixture]   │
│          │                                  │                   │
├──────────┴──────────────────────────────────┴───────────────────┤
│  Bottom: YAML preview (read-only) | Files | Runs | Logs         │
└─────────────────────────────────────────────────────────────────┘
```

**Node palette** (left): drag node type to canvas. Categories:
- AI: classify, agent
- Action: skill, shell, python, http, db_query
- Logic: branch, parallel, merge, transform, end

**Canvas** (center): Drawflow instance. Edge labels show case names
(bug/question/...) for classify/branch. Right-click node → inspector.
Double-click → open prompt/script in editor modal.

**Canvas interactions** (post-Phase 19, see `editor.js`):

| Gesture | Behaviour |
|---|---|
| Drag empty canvas | Marquee box-select — every node intersecting the rect joins the multi-selection set |
| Shift+drag empty | Additive marquee (keeps prior selection) |
| Shift+click node | Toggle membership without dragging |
| Drag node in multi-set (≥2) | Multi-drag — all selected nodes move with the same delta; `editor.updateConnectionNodes` re-renders edges live |
| Delete / Cmd+Backspace | Removes every multi-selected node (focus-gated so typing in inspector inputs doesn't trigger) |
| Mouse wheel / 2-finger trackpad | Pan canvas (Figma-style); replaces drawflow's drag-pan |
| Ctrl/⌘+wheel or pinch | Zoom (drawflow's native `zoom_enter`) |
| Lock toggle (🔒) | Switch drawflow into `editor_mode='fixed'` — node drag/delete/connect/palette-drop all disabled; click still opens inspector via manual `nodeSelected` dispatch; state persisted to `localStorage['wf-canvas-locked']` |
| Reset view button (⤢) | Fit-to-view: compute bbox of all nodes (via `offsetWidth/Height`, unscaled), zoom = `min((vw − 160) / bboxW, (vh − 160) / bboxH)` clamped to `[zoom_min .. 1.0]`, pan to centre. Same routine fires on initial page load behind a `.wf-fitting` opacity gate so the canvas never paints at the wrong origin before fitting |

The marquee/multi-drag and pan-replacement listeners run at capture
phase on `canvasEl` so they preempt drawflow's bubble-phase
mousedown handler. Anything that lands on a `.drawflow-node`,
`.input`, or `.output` falls through unchanged — drawflow keeps
owning single-node drag and connection drawing.

**Inspector** (right): schema-driven form. Untuk classify: prompt
textarea, `output_cases` chip list (engine derive edge case labels).
Untuk connector: module + op dropdown (autocomplete dari registry),
args form auto-render dari `Operation.Input` struct. Per-type schema →
templ partial server-rendered. **Edges editor terpisah** — separate
panel di canvas, list of `{from, to, case?}` edit-able.

**Bottom panel** (collapsible tabs):
- **YAML preview** — read-only mirror, real-time render dari canvas
- **Files** — file explorer per workflow folder
- **Runs** — recent runs, click → timeline view
- **Logs** — live log dari run yang sedang jalan

### YAML mode toggle

Switch dari canvas ke YAML editor full screen. Power user friendly. Save
parse + cycle check + re-render canvas. Round-trip lossless (canvas
positions di `_canvas:` field).

### Test panel

Tombol "Test" → modal:
- Pilih trigger (pretend event input): `channel` dgn text apa,
  `cron` (tick now), `webhook` dgn payload JSON, atau pakai fixture
  dari `__tests__/`.
- Run engine in test mode (no notify, no real skill side-effects —
  skills run dgn mock kalau punya `mock` field).
- Canvas show animation: node hijau saat completed, merah kalau fail,
  abu skip. Edge yang dilewati di-highlight.
- Per-node output panel di bawah: input/output JSON, duration, cost.

### Run timeline view

```
[10:00:01] ▶ Started (trigger: channel #support)
[10:00:01]   ├─ classify-intent
[10:00:03]   │    ├─ input: "ada bug di widget"
[10:00:03]   │    ├─ output: {verdict: "bug"}
[10:00:03]   │    └─ duration: 2.1s, tokens: 245, cost: $0.0008
[10:00:03]   └─ handle-bug
[10:00:05]        ├─ skill: create-linear-ticket
[10:00:05]        ├─ output: {ticket_id: "LINEAR-123"}
[10:00:05]        └─ duration: 2.0s
[10:00:05] ✓ Completed (4.1s, $0.0008)
```

Plus inline canvas mini-map sebelah kanan, dengan node yang dilewati
di-highlight.

### Hand-edit ↔ UI consistency

File ditulis admin via editor luar tetep dikenali UI saat refresh.
`Service.List()` baca disk tiap call, fsnotify watcher push update via
SSE (Server-Sent Events) ke browser yang lagi buka editor.

### Approval banner

`enabled=false` dan ada `shell`/`python` node + `approved=false` → list
page tampilin banner kuning: "1 workflow pending approval — created by
AI via MCP". Klik → editor dgn tombol Approve di header.

---

