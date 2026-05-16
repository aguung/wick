## 9. MCP surface — complete API for any AI environment

Workflow editing dirancang biar **AI dari mana saja** bisa bikin/edit,
ga peduli punya file tool atau tidak. Tiga tier op + remote access
patterns.

### Akses environment matrix

| Env | File access | MCP transport | Pattern |
|---|---|---|---|
| Claude Code, Cursor (local CLI) | ✓ native | stdio (local) | File tool + thin MCP introspection |
| Claude Desktop | ✗ | stdio atau HTTP/SSE ke wick | **Full MCP ops** (tier 1 + 2 + 3) |
| ChatGPT (custom GPT, plugin) | ✗ | HTTP `/mcp` + bearer | Full MCP ops |
| Gemini Gems / custom action | ✗ | HTTP `/mcp` + bearer | Full MCP ops |
| Wick built-in UI assistant | ✓ (server-side proxy) | in-process | File tool internal |

Kunci: **tanpa file tool, MCP harus self-sufficient**. Semua yang AI
butuh — read state, write state, action, file CRUD — semua ada di MCP.

Wick HTTP MCP udah ada (lihat
[docs/guide/connector-module.md](../docs/guide/connector-module.md)
"`/mcp` endpoint, bearer token"). Tinggal register workflow ops ke
existing server.

### Tier 1 — introspection (read-only)

AI butuh tau apa yang ada sebelum edit.

| Op | Param | Hasil |
|---|---|---|
| `workflow_workspace` | — | `{base_dir, schema_ref, node_types[], trigger_types[], templates[]}` — entry point |
| `workflow_node_types` | — | `[{type, schema, example, when_to_use}]` |
| `workflow_trigger_types` | — | `[{type, schema, example}]` |
| `workflow_channels` | — | `[{name, configured, triggers[], actions[]}]` — channel registry (lihat §7) — used for both trigger + action node discovery |
| `workflow_connectors` | — | `[{module, rows: [], operations: []}]` — connector module rows + ops (existing `wick_list` discoverable via tool_id `conn:{id}/{op}`) |
| `workflow_skills` | provider? | `[{name, provider, description, input_schema, source}]` — per-provider skill catalog discovered via `Provider.ListSkills()`. Filter by provider param atau return semua kalau kosong. NOT channel actions / connector ops (lihat `workflow_channels` / `workflow_connectors`) |
| `workflow_providers` | — | `[{name, configured, capabilities, default_preset}]` — list providers (claude/codex/gemini) + their capabilities (structured_output support, etc.) |
| `workflow_list` | filter? | list semua workflow `[{slug, id, name, enabled, approved}]` |
| `workflow_get` | slug | full workflow definition `{id, name, triggers[], graph{...}, files[]}` — sumber kebenaran AI buat edit |
| `workflow_list_files` | slug | list isi folder `[{path, size, modified}]` — buat AI tau ada file apa |
| `workflow_read_file` | slug, path | content file (prompt.md, script.sh, dst) — replace `Read` tool buat AI tanpa file access |

### Tier 2 — write (state-mutating)

Edit workflow. AI bisa pilih: write file langsung (kalau ada file tool)
atau canvas ops (deklaratif).

**File ops (replace native file tool buat remote AI):**

| Op | Param | Hasil |
|---|---|---|
| `workflow_create` | slug, template? | scaffold folder lengkap (id, default workflow.yaml, README); return `{slug, path, files, id}` |
| `workflow_write_file` | slug, path, content | atomic write ke `<base>/<slug>/<path>` — sanitize (no `..`, no symlink, no escape folder) |
| `workflow_delete_file` | slug, path | hapus file dalam folder workflow |
| `workflow_delete` | slug | hapus full workflow folder + unregister scheduler |

**Canvas ops (deklaratif, lebih ringkas dari nulis YAML):**

| Op | Param | Hasil |
|---|---|---|
| `workflow_add_node` | slug, node | add node to graph, validate; return updated YAML |
| `workflow_update_node` | slug, id, patch | merge patch ke node fields |
| `workflow_delete_node` | slug, id | remove node + edges yang refer ke dia |
| `workflow_connect` | slug, from_id, to_id, case? | add edge; case = key kalau dari classify/branch |
| `workflow_disconnect` | slug, from_id, to_id | remove edge |
| `workflow_move_node` | slug, id, x, y | canvas position hint |
| `workflow_set_triggers` | slug, triggers[] | replace triggers list |
| `workflow_toggle` | slug, enabled | enable/disable |

Canvas position disimpan di `workflow.yaml` field optional `_canvas:`:
```yaml
_canvas:
  positions:
    classify-intent: {x: 120, y: 200}
    handle-bug: {x: 380, y: 100}
```

YAML engine ignore `_canvas`; UI baca buat render.

### Tier 3 — action (validate, simulate, test, run, approve)

| Op | Param | Hasil |
|---|---|---|
| `workflow_validate` | slug | parse + cycle + schema + guard dry-run; return `{ok, errors[], warnings[]}` |
| `workflow_simulate` | slug, event | run dgn event sintetis, ga persist, ga notify. Return per-node output + final result |
| `workflow_test` | slug | run dengan `__tests__/` fixtures, compare ke expected |
| `workflow_run_now` | slug, event? | trigger run beneran (manual trigger pattern), return run_id |
| `workflow_get_runs` | slug, limit | list runs dgn event + status + cost |
| `workflow_get_run` | slug, run_id | full run state + events.jsonl + node outputs |
| `workflow_request_review` | slug, message | notify admin di UI; workflow stay `enabled=false` |
| `workflow_capture_fixture` | slug, run_id, node_id | snapshot run sebagai `__tests__/<node>.json` |

### Pattern per environment

**Local AI dgn file tool (Claude Code, Cursor):**
```
1. workflow_workspace()          ← tau lokasi + schema
2. workflow_create(slug, template) ← scaffold
3. Edit workflow.yaml via Write/Edit native
4. Edit nodes/*.md, script.sh native
5. workflow_validate(slug)       ← check
6. workflow_simulate(slug, evt)  ← dry-run
7. workflow_request_review(slug) ← admin approve
```

**Remote AI tanpa file tool (Claude Desktop, ChatGPT, Gemini):**
```
1. workflow_workspace()          ← entry
2. workflow_node_types()         ← discover apa yg bisa dipake
3. workflow_create(slug)         ← scaffold (lewat MCP)
4. workflow_add_node(slug, ...)  ← bangun graph step by step
5. workflow_connect(slug, ...)   ← sambungin edge
6. workflow_write_file(slug, "nodes/prompt.md", content)
                                 ← isi prompt panjang via MCP
7. workflow_validate(slug)
8. workflow_simulate(slug, evt)
9. workflow_request_review(slug)
```

Dua flow output sama — file di folder yang sama, approval flow sama.
Diferensiator cuma channel komunikasi: native file tool vs MCP write
op.

### HTTP MCP transport — setup buat remote AI

Wick MCP server udah ada di `/mcp` (lihat existing connector-module
docs). Buat workflow ops:

```
POST https://wick.your-host.com/mcp
Authorization: Bearer <token>
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "method": "tools/call",
  "params": {
    "name": "workflow_add_node",
    "arguments": {
      "slug": "support-triage",
      "node": {"id": "classify-intent", "type": "classify", ...}
    }
  },
  "id": 1
}
```

**Setup AI client:**

- **Claude Desktop** — edit `claude_desktop_config.json`:
  ```json
  {
    "mcpServers": {
      "wick": {
        "url": "https://wick.your-host.com/mcp",
        "headers": {"Authorization": "Bearer wick_token_..."}
      }
    }
  }
  ```
- **ChatGPT (custom GPT)** — Action dgn OpenAPI spec yang reference
  `/mcp` endpoint. Bearer token di Action authentication.
- **Gemini Gems** — Function calling dgn HTTP action ke `/mcp`.
- **Wick UI assistant** — in-process MCP client, ga butuh auth (already
  authenticated session).

### Auth + permission

Token scope (per token):
- **Workflow allowlist** — bisa edit workflow apa aja? `["*"]` atau
  list slug.
- **Op allowlist** — read-only? write-only? full? Default sesuai role.
- **Approval cap** — bisa langsung enable atau wajib request_review?
  Token AI default = ga bisa enable, wajib `request_review`.

Audit log catat tiap MCP call: token ID, user yang issue token, op,
arguments hash, result, timestamp.

### Limit MCP tanpa file tool

Beberapa hal yang lebih ribet di remote-AI mode:

- **Long file edit** — AI ga punya Edit/PartialEdit, harus full-replace
  via `workflow_write_file`. Engine handle diff via tmp+rename atomic.
- **Browse files** — `workflow_list_files` cuma list path; isi besar
  harus `workflow_read_file` per-file. AI biasa cope, tapi lebih round
  trip.
- **Search** — ga ada Grep equivalent saat ini. Tambah `workflow_grep`
  kalau use-case sering muncul (future).

Trade-off: AI tanpa file tool sedikit lebih ribet tapi tetap full-capable.
Workflow logic ga compromise.

### Template per starter

`workflow_create(slug, template)` scaffold:

- `template: empty` — folder kosong + workflow.yaml minimal (1 trigger
  manual + 1 node end).
- `template: support-triage` — Use case 1 di §3.
- `template: incident-response` — Use case 2.
- `template: daily-digest` — Use case 3.

User pilih template di UI Create. AI lewat MCP pake `template: empty`
+ langsung edit, atau pake pre-built starter.

### Contoh AI flow

User: *"AI, buatin workflow: trigger `!support` di Slack, klasifikasi
bug/question/feature, bug ke Linear, question ke skill docs-search."*

```
AI → MCP: workflow_workspace()
       ← {base_dir, node_types, trigger_types, ...}

AI → MCP: workflow_node_types()
       ← [classify, agent, channel, connector, shell, branch, ...]

AI → MCP: workflow_create(slug="support-triage", template="empty")
       ← {slug, path, id, files: [workflow.yaml, README.md]}

AI → Edit workflow.yaml (or use workflow_add_node + workflow_connect)
       set triggers, add 4 nodes:
         classify-intent (cases: bug/question/feature/other)
         handle-bug (skill: create-linear-ticket)
         handle-question (skill: docs-search)
         handle-feature (skill: log-airtable)

AI → MCP: workflow_validate("support-triage")
       ← {ok: true, warnings: []}

AI → MCP: workflow_simulate("support-triage", {
            Type: "channel",
            Text: "chat widget error di production"
          })
       ← {final_result: "ticket created LINEAR-123",
          node_outputs: {
            classify-intent: {verdict: "bug"},
            handle-bug: {ticket_id: "LINEAR-123", url: "..."}
          },
          path: ["classify-intent", "handle-bug"]}

AI → MCP: workflow_request_review("support-triage",
            "Workflow triage #support: klasifikasi LLM + route ke skill.")
       ← {url: "https://wick.local/tools/agents/workflows/support-triage"}

AI ke user: "Done, di-simulate dengan sample 'chat widget error' —
            terdeteksi sebagai bug, akan bikin tiket Linear. Review +
            approve di <url>."
```

---

