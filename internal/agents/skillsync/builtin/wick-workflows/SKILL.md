---
name: wick-workflows
description: Use when the user asks to build, edit, debug, or run a wick workflow — a multi-step automation stored as a graph of typed nodes with triggers (cron, channel, webhook, manual). Covers when a workflow is the right tool versus an agent or a job, the node catalog, trigger types, the render context, and how to author one over MCP.
---

# Using wick workflows

A **workflow** is a multi-step automation stored as a JSON graph of typed nodes plus one or more triggers. One inbound event — a Slack mention, a cron tick, a webhook — starts a deterministic, replayable run that wick traces node by node.

A workflow is **not** an agent; it is the layer above. An `agent` node inside a workflow spawns an agent turn the same way a channel message would. What the workflow adds is control over *when* that turn fires, *what context* it gets, and *what happens to its output*.

## When a workflow is the right answer

| The user wants | Use |
|---|---|
| One-shot chat in Slack / Telegram / web | An agent — channel → pool → reply |
| An LLM that calls APIs | Connectors over MCP |
| A cron that runs a script | A background job |
| **One trigger firing a multi-step pipeline** — classify the message, branch on intent, fetch from several APIs, hand a focused prompt to an agent, post a structured reply | **A workflow** |
| Runs you can replay and edit visually | **A workflow** |

If the task is a single prompt and a single reply, a workflow is overhead. Say so rather than building one.

## Anatomy

```json
{
  "id": "support-triage",
  "version": 1,
  "name": "Support Triage",
  "enabled": true,
  "triggers": [
    {
      "id": "trigger-slack-message",
      "type": "channel",
      "channel": "slack",
      "event": "app_mention",
      "entry_node": "classify_intent"
    }
  ],
  "graph": {
    "entry": "classify_intent",
    "nodes": [
      { "id": "classify_intent", "type": "classify",
        "output_cases": ["bug_report", "how_to", "refund"],
        "input": "{{index .Event.Payload \"text\"}}" },
      { "id": "bug_report", "type": "agent",
        "prompt": "Triage this bug report: {{.Node.classify_intent.input}}" }
    ]
  }
}
```

Node types include `classify`, `agent`, `connector`, `http`, `shell`, `branch`, `parallel`, the `datatable_*` family, and `sticky_note` — a canvas-only annotation (fields `content`, `color`, `width`, `height`, `texts`) that never executes and takes no edges. Do not guess a node's schema — fetch it (see *Authoring over MCP* below).

## Triggers

| Trigger | Fires when |
|---|---|
| `cron` | A 6-field cron expression matches. Disabled workflows skip ticks but still run manually. |
| `channel` | An inbound message matches `channel` + `event` + `match`. |
| `webhook` | A request hits `/webhook/<workflow-id>`. |
| `manual` | Someone presses Run in the canvas or on the detail page. |
| `schedule_at` | A one-shot timer queued by an earlier node. |
| `error` | Another workflow failed — receives the failed run's metadata, so you can route alerts. |

One workflow can carry several triggers. The queue policy — per-workflow concurrency cap, drop / queue / parallel — decides what happens when two land at once.

For `channel` triggers, `match` is **event-shape specific**. Slack events (`message`, `thread_started`, `app_mention`, `command`, `block_action`, `view_submission`, `shortcut`, …) each have their own filter schema. Discover the exact shape via the `workflow_integration` MCP op, which returns the per-channel event catalog with `match_schema` and `payload_schema`. Note `thread_started` fires only for a top-level post that starts a thread, never for replies.

## Render context

Every trigger contributes to `.Event` — payload, source identity, timestamp, user. Earlier node results are available under `.Node.<id>`. Templates use Go template syntax:

```
{{index .Event.Payload "text"}}
{{.Node.classify_intent.input}}
```

## Authoring over MCP

Workflows are scaffolded and edited through `workflow_*` operations. The discipline is the same as connectors: **fetch the schema, do not guess it.** The node catalog, per-node input schema, and output fields are all introspectable — a node's `Descriptor()` is the source of truth, so what MCP returns is always current.

Editing a live workflow creates a new version rather than mutating the running one. Publish makes a version active.

### Rules for every edit (required)

These keep a workflow readable by the next person — human or AI — who opens it. A workflow that breaks them is not finished.

1. **Every node and every trigger gets a `description`.** Markdown: one line on what it is for, then a `Kenapa:` line on why it exists. Pass it in `workflow_add_node` / `workflow_update_node` (nodes) and `workflow_set_triggers` (triggers). `workflow_validate` warns once per id that lacks one. When you change what a node does, update its `Kenapa:` line in the same edit.

   ```markdown
   Kirim ringkasan ke thread asal.

   Kenapa: pelapor butuh jawaban di thread yang sama, bukan DM.
   ```

2. **Every `go_script` / code body opens with a header comment** — `Buat apa` / `Kenapa` / `Input` / `Output` — and each helper function gets a one-line comment.

   ```go
   // Buat apa: ambil nomor tiket dari teks pesan.
   // Kenapa: node berikutnya butuh id tiket, bukan teks mentah.
   // Input: .Node.trigger.payload.text
   // Output: {"ticket_id": "T-123"} atau {"ticket_id": ""}
   ```

3. **Group each path with a `sticky_note` node.** Add it with `workflow_add_node` (`type: sticky_note`, `content` = markdown title of the path + a short summary, `color` one of yellow/green/blue/purple/red/gray, `width`/`height` big enough to cover the block). It renders behind the block's nodes. The note is a board: `content` is its title (plain markdown, top left). To pin several annotations on it (like writing on a screenshot), set `texts`: `[{id, content, x, y, width, color, size}]` — each is a small sticky card; `x`/`y` (top-left of the card) and `width` are relative 0..1 to the note, so cards follow it on resize; `color` uses the same presets (default yellow), `size` is the font size `sm`/`md`/`lg` (default md). E.g. the flow explanation in the empty column on the right at `{x:0.55, y:0.15, width:0.4, color:"blue"}`. Its `content` or any `texts[].content` counts as its description. There is no separate note op — `workflow_update_node`, `workflow_move_nodes` and `workflow_delete_node` handle it like any node.
4. **After every graph edit, tidy the canvas.** One column per trigger, top→bottom, parallel branches in the column next to it, no crossing edges, each sticky note wrapping its block. `workflow_auto_layout` does the lanes (it leaves sticky notes where they are, so re-wrap them with `workflow_move_nodes`); check `workflow_canvas_view` before `workflow_publish`.
5. **Plan, do not poll.** Every mutation returns the whole workflow, so plan the edits up front, batch moves in one `workflow_move_nodes`, and do not call `workflow_get` between edits to "see" a result you already have.

## Debugging a run

Runs are traced node by node and stored on disk, so a failed run can be inspected and replayed rather than re-triggered blindly. Read the run state first: which node failed, what its input actually was, and what the previous node emitted. A workflow that fails on the third node usually fails because the first node's output was not the shape the third node expected.

## The canvas

The editor lives at `/tools/agents/workflows/edit/<id>` — canvas, inspector, run timeline, version history with side-by-side compare, and Publish. Point the user there for visual editing; it is usually faster than describing a graph edit in prose.
