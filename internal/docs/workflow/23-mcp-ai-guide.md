# 23 — MCP AI Guide: Building Workflows via MCP

Panduan praktis untuk AI (Claude, GPT, Gemini) yang membangun workflow lewat MCP
tanpa akses file. Berisi kontrak eksak, gotcha, dan template siap pakai.

---

## TODO

- [ ] Dokumentasikan field `Event.Payload` per event type lebih lengkap (reaction, submission, dll)
- [ ] Expose `schema` field di `workflow_node_types` / `workflow_trigger_types` response
- [ ] Expose `workflow_channels` dengan full action input/output schema (saat ini return `[]`)
- [ ] Tambah `workflow_integration` yang return non-empty (saat ini return `{}`)

---

## 1. Golden Rule: Param Keys

MCP `params` key = **snake_case dari struct field name**. Contoh:

| Go struct field | MCP param key |
|---|---|
| `ID` | `id` |
| `Triggers` | `triggers` |
| `NodeID` | `node_id` |
| `FromID` | `from_id` |

Trigger / Node JSON body pakai **Go JSON default** (exact field name, PascalCase) karena
di-`json.Unmarshal` langsung. Contoh:

```json
// workflow_set_triggers → params.triggers (string JSON)
[{"Type":"channel","ChannelName":"slack","Event":"message","Target":"C0ABC","MatchEnabled":true,"Match":{"channel_id":["C0ABC"]}}]
```

---

## 2. Event struct

Engine inject `workflow.Event` ke setiap run. Field yang tersedia di template:

```
{{.Event.Type}}       — "channel" | "manual" | "cron" | "webhook"
{{.Event.Subtype}}    — subtype string (jarang dipakai)
{{.Event.Channel}}    — channel ID (Slack: "C0ASUHYCRNU")
{{.Event.At}}         — time.Time
{{.Event.Payload}}    — map[string]any — semua field event asli ada di sini
```

**Penting:** `Event.Thread`, `Event.User`, `Event.Text` TIDAK ADA. Semua ada di
`{{.Event.Payload}}`. Akses via:

```
{{index .Event.Payload "ts"}}          — timestamp pesan (Slack message)
{{index .Event.Payload "user"}}        — user ID
{{index .Event.Payload "text"}}        — teks pesan
{{index .Event.Payload "trigger_id"}} — trigger_id (dari block_action, untuk open_modal)
{{index .Event.Payload "action_id"}}   — action yang diklik
{{index .Event.Payload "value"}}       — value button yang diklik
{{index .Event.Payload "channel_id"}} — channel ID dari payload
{{index .Event.Payload "thread"}}      — thread timestamp
```

### Event Payload per event type

| Event type | Field penting di Payload |
|---|---|
| `message` | `text`, `ts`, `user`, `channel_id`, `thread`, `is_dm` |
| `block_action` | `trigger_id`, `action_id`, `value`, `user`, `channel_id`, `ts` |
| `submission` | `values` (map form fields), `user`, `channel_id` |
| `reaction` | `reaction`, `item.ts`, `user` |
| `mention` | `text`, `ts`, `user`, `channel_id` |

**Cara tahu payload shape:** run workflow sekali → buka inspector node mana saja →
klik tab **All nodes** di panel INPUT kiri → expand node `trigger` → semua field payload
terlihat dengan path expression siap pakai (`{{index .Event.Payload "..."}}` atau
`{{.Event.Payload.field}}`).

---

## 3. Template RenderCtx

Semua node args / expression / prompt_file di-render dengan:

```
{{.Event}}        — workflow.Event (lihat §2)
{{.Node.<id>}}    — output node upstream (lihat §4)
{{.Env}}          — map[string]string env vars
{{.Secret}}       — map[string]string secrets
{{.Workflow.ID}}  — workflow ID
{{.Run.ID}}       — run ID
```

**Gotcha:** node ID dengan dash (`-`) tidak bisa diakses via `{{.Node.my-node}}` —
Go template parser reject `-`. Pakai underscore atau camelCase: `mynode`, `my_node`.

---

## 4. Node Output Fields

### transform (gotemplate)
```
{{.Node.<id>.result}}   — string hasil render expression
```

### agent
```
{{.Node.<id>.text}}     — last assistant message (string)
```

### channel / send_message
```
{{.Node.<id>.ts}}       — posted message timestamp
{{.Node.<id>.channel}}  — channel ID
```

### channel / open_modal
```
{{.Node.<id>.view_id}}   — view ID (pakai untuk update_modal downstream)
{{.Node.<id>.view_hash}} — view hash
```

---

## 5. Fixed vs Expression — Kapan Pakai Yang Mana

Setiap arg field di channel / connector node punya toggle **Fixed** atau **Expression**
(terlihat di inspector Parameters panel).

| Mode | Kapan dipakai | Contoh |
|---|---|---|
| **Fixed** | Nilai literal tidak berubah per-run | `C0ASUHYCRNU`, `true`, `Ada pesan baru` |
| **Expression** | Nilai bergantung pada event / output node lain | `{{index .Event.Payload "ts"}}`, `{{.Node.build.result}}` |

**Tips:** gunakan panel **All nodes** (tab kiri INPUT saat inspector terbuka) untuk
melihat semua output dari run terakhir. Klik/drag nilai ke field expression — path
`{{...}}` otomatis ter-insert.

**Jika belum ada data:** jalankan workflow sekali (Run Now atau tunggu trigger real),
buka inspector, tab All nodes, lihat shape payload-nya. Lalu set expression sesuai.
Untuk share ke AI: paste isi tab All nodes sebagai context — AI bisa langsung tulis
expression yang tepat.

---

## 6. Channel Node: Slack Actions

### send_message

```yaml
- id: sendmsg
  type: channel
  channel: slack
  op: send_message
  args:
    channel: '{{index .Event.Payload "channel_id"}}'
    thread_ts: '{{index .Event.Payload "ts"}}'
    text: 'Fallback text (wajib jika blocks kosong)'
    blocks: |
      [{"type":"actions","elements":[{"type":"button","text":{"type":"plain_text","text":"Buat Tiket"},"action_id":"create_tiket","value":"{{index .Event.Payload \"ts\"}}"}]}]
  arg_modes:
    channel: expression
    thread_ts: expression
    text: fixed
    blocks: expression
```

**Gotcha `blocks` — 2 cara valid:**

1. **Inline expression** (value/ts embed di blocks JSON): gunakan `transform` node dulu
   karena quote dalam YAML string sulit di-escape. Lihat §6.1.

2. **Static blocks** (tidak ada template): tulis JSON langsung di field `blocks`,
   mode = `fixed`. Tidak perlu transform.

```yaml
# blocks fixed — tidak ada expression di dalamnya
- id: sendmsg
  type: channel
  channel: slack
  op: send_message
  args:
    channel: C0ASUHYCRNU
    text: 'Ada pesan baru. Klik tombol.'
    blocks: |
      [{"type":"actions","elements":[{"type":"button","text":{"type":"plain_text","text":"Buat Tiket"},"action_id":"create_tiket","value":"static"}]}]
  arg_modes:
    channel: fixed
    text: fixed
    blocks: fixed
```

### 6.1 transform → send_message (untuk blocks dengan expression)

Kapan perlu `transform`: blocks JSON mengandung nilai dinamis (ts, user ID, dll) yang
harus embed lewat template. Transform node build string JSON-nya, send_message ambil
hasilnya.

```yaml
- id: buildblocks
  type: transform
  engine: gotemplate
  expression: '[{"type":"actions","elements":[{"type":"button","text":{"type":"plain_text","text":"Buat Tiket"},"action_id":"create_tiket","value":"{{index .Event.Payload "ts"}}"}]}]'

- id: sendbutton
  type: channel
  channel: slack
  op: send_message
  args:
    channel: '{{index .Event.Payload "channel_id"}}'
    thread_ts: '{{index .Event.Payload "ts"}}'
    text: 'Ada pesan baru.'
    blocks: '{{.Node.buildblocks.result}}'
  arg_modes:
    channel: expression
    thread_ts: expression
    text: fixed
    blocks: expression
```

### open_modal

`view` bisa YAML map langsung (tidak harus JSON string) — engine auto-marshal:

```yaml
- id: openmodal
  type: channel
  channel: slack
  op: open_modal
  args:
    trigger_id: '{{index .Event.Payload "trigger_id"}}'
    view:
      type: modal
      title:
        type: plain_text
        text: Detail Tiket
      close:
        type: plain_text
        text: Batal
      blocks:
        - type: section
          text:
            type: mrkdwn
            text: '{{.Node.summarize.text}}'
  arg_modes:
    trigger_id: expression
    view: fixed
```

**Penting:** `trigger_id` hanya tersedia dari event `block_action`. Harus dipakai
dalam **3 detik** setelah event diterima (batas Slack). Jangan ada LLM call antara
trigger `block_action` dan node `open_modal` — trigger_id akan expired.

**Pola yang aman:** `block_action` trigger → `open_modal` (langsung, tanpa LLM) →
baru jalankan agent / summarize di node berikutnya lalu `update_modal`.

### reply_thread

```yaml
- id: reply
  type: channel
  channel: slack
  op: reply_thread
  args:
    channel: '{{index .Event.Payload "channel_id"}}'
    thread: '{{index .Event.Payload "ts"}}'
    text: 'Balasan di thread'
  arg_modes:
    channel: expression
    thread: expression
    text: fixed
```

### send_ephemeral

```yaml
- id: ephemeral
  type: channel
  channel: slack
  op: send_ephemeral
  args:
    channel: '{{index .Event.Payload "channel_id"}}'
    user: '{{index .Event.Payload "user"}}'
    text: 'Hanya kamu yang bisa lihat ini'
  arg_modes:
    channel: expression
    user: expression
    text: fixed
```

---

## 7. Trigger: 1 Workflow 2 Trigger

Engine support `entry_node` per trigger — satu workflow bisa punya 2 jalur masuk:

```yaml
triggers:
  - type: channel
    channel: slack
    event: message
    target: C0ASUHYCRNU
    entry_node: buildblocks
    match:
      channel_id: ["C0ASUHYCRNU"]
    match_enabled: true

  - type: channel
    channel: slack
    event: block_action
    target: C0ASUHYCRNU
    entry_node: summarize
    match:
      action_id: ["create_tiket"]
      channel_id: ["C0ASUHYCRNU"]
    match_enabled: true
```

**Graph tidak perlu connect kedua jalur.** Engine resolve entry dari `trigger.entry_node`
langsung — bukan dari `graph.entry`. Dua sub-graph terpisah dalam satu workflow.yaml valid.

**Gotcha `graph.entry`:** tetap wajib diisi salah satu node (untuk fallback manual trigger).
Isi dengan entry node trigger pertama.

---

## 8. Trigger Match Filter

```yaml
match:
  channel_id: ["C0ASUHYCRNU"]     # whitelist channel
  action_id: ["create_tiket"]     # whitelist action (block_action)
  text_contains: "bug"            # substring match pada message text
match_enabled: true               # WAJIB true, default false = no filter
```

---

## 9. Trigger JSON Body (untuk workflow_set_triggers)

Field name = Go struct field (PascalCase, no json tag):

```json
[
  {
    "Type": "channel",
    "ChannelName": "slack",
    "Event": "message",
    "Target": "C0ASUHYCRNU",
    "EntryNode": "buildblocks",
    "Match": {"channel_id": ["C0ASUHYCRNU"]},
    "MatchEnabled": true,
    "DedupTTLSec": 30
  },
  {
    "Type": "channel",
    "ChannelName": "slack",
    "Event": "block_action",
    "Target": "C0ASUHYCRNU",
    "EntryNode": "summarize",
    "Match": {"action_id": ["create_tiket"], "channel_id": ["C0ASUHYCRNU"]},
    "MatchEnabled": true
  }
]
```

---

## 10. Agent Node

```yaml
- id: summarize
  type: agent
  provider: claude
  prompt_file: nodes/summarize.md
```

`prompt_file` di-render sebagai Go template dengan RenderCtx — bisa embed
`{{index .Event.Payload "value"}}` dll. Output: `{{.Node.summarize.text}}`.

**Gotcha simulate:** agent node gagal di `workflow_simulate` jika provider tidak
di-wire ke stdio MCP (`"provider not registered"`). Normal — hanya gagal di simulate,
runtime HTTP server punya provider penuh.

---

## 11. Workflow Build Checklist

```
1. workflow_check_name          — cek nama belum dipakai
2. workflow_create              — scaffold
3. workflow_write_file          — tulis workflow.yaml lengkap
4. workflow_write_file          — tulis nodes/*.md untuk agent
5. workflow_validate            — cek errors/warnings
6. workflow_publish             — promote draft → live
7. workflow_simulate            — dry-run (skip agent node di stdio mode)
8. workflow_list                — verifikasi muncul di list
```

---

## 12. Debug Workflow via UI

Saat workflow error atau output tidak sesuai:

1. **Lihat badge node** — merah = failed, hijau = success
2. **Klik node yang merah** → buka inspector → panel OUTPUT → error message tampil
3. **Tab All nodes** (panel INPUT kiri) → lihat output semua node yang sudah run
4. **Drag nilai** dari All nodes ke field expression — path `{{...}}` otomatis ter-insert
5. **Execute step** — jalankan satu node saja dengan input dari run sebelumnya
6. **Replay run** — load ulang run tertentu dari Runs panel untuk inspect step-by-step

**Cara share payload ke AI untuk perbaikan expression:**
- Run workflow (bisa gagal, tidak apa)
- Buka inspector node mana saja → tab All nodes
- Copy isi JSON dari node `trigger` atau node upstream
- Paste ke AI sebagai context — AI bisa tulis expression yang tepat berdasarkan shape data asli

---

## 13. Known Limitations (MCP / Simulate)

| Limitasi | Keterangan |
|---|---|
| `workflow_channels` return `[]` | Slack actions tidak expose schema via MCP |
| `workflow_integration` return `{}` | Integration registry tidak ter-expose di stdio mode |
| `workflow_node_types` schema null | Schema field belum di-populate |
| Agent node gagal di simulate | Provider tidak di-wire ke stdio MCP |
| Node ID dengan `-` | Go template reject, pakai `_` atau camelCase |
| `.Event.Thread` / `.Event.User` tidak ada | Semua ada di `{{index .Event.Payload "..."}}` |
| `trigger_id` expired | open_modal harus fire dalam 3s dari block_action — jangan LLM di antara |
