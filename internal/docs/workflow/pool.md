# Workflow agent node — pool integration

Status: **proposed**. Doc ini kontrak desain sebelum implementasi.
Update terakhir: 2026-05-16.

---

## TODO

- [ ] `workflow/setup/manager.go` — `WithAgentRuntime(pool *agentpool.Pool, bcast *agentstool.Broadcaster)` setter
- [ ] `workflow/nodes/agent.go` — pool path:
  - resolve sessionID per `n.Session` mode
  - subscribe broadcaster sebelum send
  - call `pool.SendWithWorkspace(ctx, sessionID, "default", "workflow", "user", prompt, n.Workspace)`
  - kumpulin `TextDelta` sampai `Done` / `Error` / `ctx.Done()`
  - return concatenated text
- [ ] `workflow/types.go` — `SessionExternal` constant + tetap empty-string = `run` default
- [ ] `workflow/setup/providers.go` — non-claude (`codex`/`gemini`) tetap pakai `cliProvider`; tambah semaphore biar ngak meledak
- [ ] Drop hardcoded 5-menit timeout di `cliProvider.AgentCall` — ganti respect `n.TimeoutSec` → engine `MaxDurationSec` → `ctx`
- [ ] Canvas inspector node agent — dropdown "Session" + conditional text input untuk `external`
- [ ] Engine emit `node_queued` event saat sesi kena `p.queue` (nice-to-have, lihat §observability)
- [ ] Test: 2 workflow run paralel sesi sama → pool serialize lewat queue
- [ ] Test: ctx batal saat nunggu Done → broadcaster unsub, ngak leak

---

## Latar belakang

Workflow agent node ([internal/agents/workflow/nodes/agent.go](../../agents/workflow/nodes/agent.go))
sekarang bypass agent pool: `cliProvider.AgentCall`
([internal/agents/workflow/setup/providers.go:92-105](../../agents/workflow/setup/providers.go#L92-L105))
panggil `exec.CommandContext(bin, "--print", prompt)` langsung. Akibat:

1. **PC meledak kalau workflow rame.** Tiap call spawn subprocess baru,
   ngak ada cap MaxConcurrent, ngak ada queue.
2. **Sesi ngak muncul di sidebar.** Pool punya `ensureSession` yg auto-bikin
   row di registry → sidebar "RECENT". Skip pool = skip sidebar.
3. **`AgentRequest.SessionID` di-ignore.** `classifySessionID`
   ([internal/agents/workflow/nodes/classify.go:111-119](../../agents/workflow/nodes/classify.go#L111-L119))
   build session string tapi `cliProvider.AgentCall` ngak forward ke CLI
   (no `--session-id`/`--resume`). Setting `session: persistent` di YAML
   ngak ngaruh sama sekali.
4. **Default empty = "new"** — fresh subprocess per call. Konteks
   ilang antar node padahal sering perlu di-share.

Pool sudah punya semua mekanik yg dibutuhin:
[internal/agents/pool/pool.go:235-326](../../agents/pool/pool.go#L235-L326)
`Pool.Send`:
- auto `ensureSession` → sidebar muncul
- FIFO `p.queue` saat `MaxConcurrent` penuh → `StatusQueued`
- buffer per sesi → message persist ke `conversation.jsonl`
- reuse subprocess kalau sesi masih alive
- `PreemptIdle=true` (default) kick longest-idle subprocess saat queue nunggu
- `KillAfterIdle` opsional kill total
- `QueueLen()` / `QueueSnapshot()` / `Active()` snapshot

Channels (Slack, REST) udah pakai pola ini lewat `sendFnFor` closure di
[internal/pkg/api/server.go:507-520](../../pkg/api/server.go#L507-L520).
Workflow tinggal ngikut.

---

## Session mode

Tambah field semantik di node agent ([types.go:147](../../agents/workflow/types.go#L147)):

```yaml
- id: ask-agent
  type: agent
  session: run                          # mode (default kalau kosong)
  session_id: "{{event.thread_ts}}"     # cuma diisi kalau session: external
  prompt: |
    {{event.text}}
```

| Mode | sessionID resolved jadi | Cocok buat |
|---|---|---|
| `external` | template `n.SessionID` (mis `{{event.thread_ts}}`, `{{event.channel_id}}`) | Slack thread = 1 sesi cross-run |
| `workflow` | `wf:<slug>` | Per-definition, jarang berguna (semua run share state) |
| `run` ← default | `wf:<slug>:run:<runID>` | Per-run shared antar node — isolated, reuse subprocess dalam 1 run |
| `new` | UUID baru per-call | Fresh tiap node, ngak ada konteks carry |

Default `run` aman: tiap workflow run isolated, tapi node `agent` ke-2 di
run yg sama reuse subprocess `agent` ke-1 → cepat (skip spawn handshake,
context carry).

Mode `external` punya implikasi cross-run: 2 webhook trigger paralel utk
thread_ts yg sama → pool dedup by `sessionKey` → kedua workflow buffer ke
sesi yg sama, output bisa intertwine. Mitigation: user yg pilih mode ini
udah aware (didokumentasi di inspector helper text). Pool serialize via
queue, ngak race, cuma urutan turn-nya bisa muter.

---

## Wiring

### 1. Manager dapet pool + broadcaster

```go
// internal/agents/workflow/setup/manager.go
type Manager struct {
    // ... existing fields
    AgentPool  *agentpool.Pool
    AgentBcast *agentstool.Broadcaster
}

func (m *Manager) WithAgentRuntime(p *agentpool.Pool, b *agentstool.Broadcaster) *Manager {
    m.AgentPool = p
    m.AgentBcast = b
    // re-register agent executor dgn pool path
    m.Engine.Register(workflow.NodeAgent, nodes.NewAgentExecutor(m.Providers, p, b))
    return m
}
```

server.go panggil setter ini setelah pool + bcast ke-bikin.

### 2. AgentExecutor pool path

```go
// internal/agents/workflow/nodes/agent.go
type AgentExecutor struct {
    Providers *provider.Registry
    Pool      *agentpool.Pool
    Bcast     *agentstool.Broadcaster
}

func (e *AgentExecutor) Execute(ctx context.Context, n workflow.Node, rc *workflow.RunContext) (workflow.NodeOutput, error) {
    sessionID, err := resolveSessionID(n, rc)
    if err != nil {
        return workflow.NodeOutput{}, err
    }

    prompt, err := template.Render(n.Prompt, rc.RenderCtx())
    if err != nil {
        return workflow.NodeOutput{}, err
    }

    // Pool path — claude lewat sini.
    if e.Pool != nil && e.Bcast != nil && providerUsesPool(n.Provider) {
        return e.runViaPool(ctx, sessionID, prompt, n)
    }

    // Fallback — non-claude provider via cliProvider (semaphore'd).
    prov, err := e.Providers.Get(n.Provider)
    if err != nil {
        return workflow.NodeOutput{}, err
    }
    req := provider.AgentRequest{
        Prompt:    prompt,
        Preset:    n.Preset,
        Workspace: n.Workspace,
        Skills:    n.Skills,
        Tools:     n.Tools,
        MaxTurns:  n.MaxTurns,
        SessionID: sessionID,
    }
    res, err := prov.AgentCall(ctx, req)
    // ...
}

func (e *AgentExecutor) runViaPool(ctx context.Context, sessionID, prompt string, n workflow.Node) (workflow.NodeOutput, error) {
    // Subscribe SEBELUM Send biar event awal ngak ke-miss.
    evCh, unsub := e.Bcast.Subscribe(sessionID)
    defer unsub()

    if err := e.Pool.SendWithWorkspace(ctx, sessionID, "default", "workflow", "user", prompt, n.Workspace); err != nil {
        return workflow.NodeOutput{}, fmt.Errorf("pool send: %w", err)
    }

    var buf strings.Builder
    toolsUsed := []string{}
    for {
        select {
        case <-ctx.Done():
            return workflow.NodeOutput{}, ctx.Err()
        case ev, ok := <-evCh:
            if !ok {
                return workflow.NodeOutput{}, fmt.Errorf("event channel closed before done")
            }
            switch ev.Type {
            case "text_delta":
                buf.WriteString(ev.Data)
            case "tool_use":
                toolsUsed = append(toolsUsed, ev.Data) // tool name
            case "error":
                return workflow.NodeOutput{}, fmt.Errorf("agent error: %s", ev.Data)
            case "done":
                text := strings.TrimSpace(buf.String())
                return workflow.NodeOutput{
                    Result: text,
                    Fields: map[string]any{
                        "text":       text,
                        "tools_used": toolsUsed,
                        "session_id": sessionID,
                    },
                }, nil
            }
        }
    }
}
```

### 3. SessionID resolver

```go
func resolveSessionID(n workflow.Node, rc *workflow.RunContext) (string, error) {
    mode := n.Session
    if mode == "" {
        mode = workflow.SessionRun
    }
    switch mode {
    case workflow.SessionExternal:
        if n.SessionID == "" {
            return "", fmt.Errorf("session: external butuh session_id template")
        }
        return template.Render(n.SessionID, rc.RenderCtx())
    case workflow.SessionWorkflow:
        return "wf:" + rc.Workflow.Slug, nil
    case workflow.SessionRun:
        return "wf:" + rc.Workflow.Slug + ":run:" + rc.RunID, nil
    case workflow.SessionNew:
        return "wf:adhoc:" + uuid.NewString(), nil
    default:
        return "", fmt.Errorf("unknown session mode %q", mode)
    }
}
```

### 4. Constants

```go
// internal/agents/workflow/types.go
const (
    SessionExternal  = "external"
    SessionWorkflow  = "workflow"
    SessionRun       = "run"       // default
    SessionNew       = "new"

    // Legacy alias: "root" tetep dianggep "run" buat backward compat
    // workflow.yaml lama. "persistent" → "workflow".
)
```

---

## Timeout strategy

Workflow agent node bisa lama (long-running tool use, ask_user wait,
agent reasoning panjang) — itu **expected**, bukan bug.

- ❌ Drop hardcoded 5-menit timeout di `cliProvider.AgentCall`
- ✅ Layer timeout:
  1. **Node-level** — `n.TimeoutSec` (per-node, user explicit)
  2. **Workflow-level** — `w.MaxDurationSec` (default 10 menit, engine
     wrap di [engine.go:212-217](../../agents/workflow/engine/engine.go#L212-L217))
  3. **`ctx.Done()`** cascade — engine cancel → pool send unblock → event
     loop break

Sesi yg nunggu di `p.queue` ngak punya timeout sendiri — nunggu sampe
slot bebas atau ctx batal. `preempt_idle=true` default kick idle
subprocess buat ngasih jalan ke yg nunggu.

`IdleTimeout` (default 120s) cuma kill subprocess yg ngak terima
message — sesi `Working` aman. `KillAfterIdle=0` (default) → ngak ada
hard kill. Sesi ke-kill saat idle → next message auto-respawn via
`--resume <CLI session id>` (dari `agents.json`), konteks tetep.

---

## Observability

Event yg udah free dari pool:
- Sidebar entry — `ensureSession` bikin row, label = first user message
- `conversation.jsonl` — semua turn persist
- `agents.json` — CLI session ID untuk resume
- `meta.json` Status: `Queued` / `Working` / `Idle`

Workflow-side event tetep emit lewat engine `emit`:
- `node_started` — sebelum `Pool.Send` di-call
- `node_completed` — setelah `Done` diterima, latency_ms = total time termasuk wait di queue

**Nice-to-have (deferred):**
- Emit `node_queued` saat `Pool.Send` return tanpa langsung spawn (i.e.,
  sesi masuk `p.queue`). Butuh pool expose flag dari `Send` return atau
  callback. Saat ini operator harus polling `Pool.QueueSnapshot()` buat
  tau.

---

## Non-claude provider (codex/gemini)

Pool factory saat ini `ClaudeFactory` only — single-binary. Codex/gemini
ngak punya pool path; tetep lewat `cliProvider.AgentCall`.

Mitigation: tambah global semaphore di `cliProvider` (chan struct{} sized
N) biar concurrent agent call ke non-claude pun bounded. Skip queue + sidebar
(deferred sampai pool multi-factory).

```go
// internal/agents/workflow/setup/providers.go
var nonClaudeSem chan struct{} // init di NewCLIProviders, sized = MaxConcurrent

func (p *cliProvider) AgentCall(ctx context.Context, req provider.AgentRequest) (provider.AgentResult, error) {
    select {
    case nonClaudeSem <- struct{}{}:
        defer func() { <-nonClaudeSem }()
    case <-ctx.Done():
        return provider.AgentResult{}, ctx.Err()
    }
    // ... exec
}
```

---

## Migration

Backward-compat workflow.yaml lama:

```yaml
session: root        # → treat as "run" (legacy alias)
session: persistent  # → treat as "workflow" (legacy alias)
session: new         # tetap valid
session: (empty)     # default jadi "run" (sebelumnya effectively "new")
```

Default behavior berubah dari "fresh subprocess per call" → "reuse dalam
1 run". Buat workflow yg butuh isolation absolut antar node, set explicit
`session: new`. Validator output warning di publish: "session mode
default berubah ke `run` di phase pool — set explicit kalau perlu isolasi
per-call".

---

## Open questions

- AgentName hardcoded `"default"` di pool.Send call. Kalau workflow mau
  pakai agent name beda (mis "researcher"), perlu kah expose `agent_name`
  field di node? Saat ini Pool key = `sessionKey(sessionID, agentName)`
  → beda agentName = beda subprocess di sesi yg sama. Mungkin
  nice-to-have, tapi tunggu use case konkret.
- Multi-tool-call streaming: kalau agent panggil tool berkali-kali dalam
  1 turn, kita kumpulin semua `tool_use` event di `tools_used`. OK?
  Atau perlu nest per-tool result? Saat ini cukup nama tool aja.
- Skills allowlist — masih perlu `validateSkills` di executor? Pool
  factory yg launch subprocess dgn `--skills` flag, tapi cliProvider
  ngak. Konsisten lebih baik validate di executor sebelum send.
