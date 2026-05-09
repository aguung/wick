# Command Gate — Arsitektur & Approval System

Status: draft — arsitektur final, implementasi belum dimulai.
Update terakhir: 2026-05-09.

Keputusan final yang sudah locked:
- IPC: Unix Domain Socket (raw JSON, bukan HTTP)
- Gate binary: embed ke main binary via `//go:embed` (bukan sidecar/subcommand)
- Dev override: `WICK_GATE_BIN` env var di `.env`
- Approval style: Gate style (Pola A) — system intercept, bukan Claude Code style
- Decision modes: 4 (`approve_once` / `approve_session` / `approve_always` / `block`)
- AskUser: MCP tool (bukan harness), bridged ke web UI lewat SSE

---

## Checklist Implementasi (Quick Reference)

Urutan **timeline-aware**: tiap stage hanya butuh stage sebelumnya. Bisa di-pause di akhir stage manapun dan tetap shippable. Detail per task + exit criteria di [§12](#12-checklist-implementasi-detail).

```
Stage 1 — Spec & Wiring Foundation                                    ✅ done
[x] S1.1 gate.Spec field SocketPath + AutoApproved
[x] S1.2 WriteSpawnArtifacts: tulis SocketPath = <sessionDir>/gate/gate.sock
[x] S1.3 Wire Gate di pool/factory.go (saat ini Gate field masih nil)
[x] S1.4 Unit test spec marshal + artifact write

Stage 2 — Daemon Socket Listener                                      ✅ done
[x] S2.1 Unix socket listener per session, chmod 0600
[x] S2.2 Cleanup os.Remove saat session stop / daemon shutdown
[x] S2.3 Goroutine per koneksi (raw JSON newline-delimited)
[x] S2.4 Pending state manager: sync.Map[id]chan ApprovalResponse
[x] S2.5 Timeout goroutine 25s → auto-block
[x] S2.6 Test dial socket dari fake gate

Stage 3 — Gate Binary Upgrade                                         ✅ done
[x] S3.1 wick-gate dial unix socket dari spec
[x] S3.2 auto_approved short-circuit (zero-latency always-allow)
[x] S3.3 Encode ApprovalRequest → kirim
[x] S3.4 Decode ApprovalResponse → exit 0 atau 2
[x] S3.5 Fail-safe: connect refused / timeout → exit 2
[x] S3.6 Integration test dgn fake socket server

Stage 4 — Embed + Binary Resolution                                   ✅ done
[x] S4.1 //go:embed assets/wick-gate-*
[x] S4.2 extractEmbeddedGate(sessionDir), chmod 0755, idempotent
[x] S4.3 resolveGateBin: WICK_GATE_BIN env → fallback embed
[x] S4.4 Wire ke factory.go
[x] S4.5 CI build step "Build wick-gate" sebelum main build

Stage 5 — Web UI: Approval Modal + 4 Modes                            ✅ done
[x] S5.1 SSE event types approval_request + approval_resolved
[x] S5.2 Endpoint POST /api/agents/sessions/{id}/approve
[x] S5.3 approve_session in-memory map per session
[x] S5.4 approve_always persist ke spec.AutoApproved
[x] S5.5 Modal templ: 4 tombol + countdown 25s
[x] S5.6 matchKey hash(tool + cmd), exact match MVP
[x] S5.7 "Approved commands" panel + Revoke per item
[ ] S5.8 Smoke test manual (real-claude end-to-end)

Stage 6 — AskUser MCP Tool + Web Card                                 ✅ done
[x] S6.1 MCP tool "ask_user" register
[x] S6.2 Handler: pending channel + broadcast SSE + 5min timeout
[x] S6.3 SSE event types ask_user + ask_user_resolved
[x] S6.4 Endpoint POST /api/agents/sessions/{id}/answer
[x] S6.5 Card templ inline di composer area
[ ] S6.6 Smoke test agent → web → answer roundtrip (real-claude)

Stage 7 — Dev Tooling                                                 ✅ done
[x] S7.1 .vscode/tasks.json: debug:prep build bin/wick-gate.exe sibling
[-] S7.2 task "gate: sync-spec" — dropped (gak diperlukan, lihat D12)
[-] S7.3 launch "wicklab-gate" — dropped (gate gak bisa standalone, lihat D13)
[x] S7.4 .env.example: WICK_GATE_BIN dokumented (opsional override)
[x] S7.5 ResolveGateBinary tambah sibling-of-exe step (auto-discover bin/)
[x] S7.6 Doc updated dgn flow normal + cara debug via test/logs
```

| Stage | Hot files |
|---|---|
| 1 | `internal/agents/gate/spec.go`, `claude_hook.go`, `pool/factory.go` |
| 2 | `internal/agents/gate/socket.go` (new) |
| 3 | `cmd/wick-gate/main.go` |
| 4 | `internal/agents/gate/embed.go` (new), `template/.github/workflows/release.yml` |
| 5 | `internal/tools/agents/{handler,stream}.go`, `view/approval.templ`, `js/agents.js`, `internal/agents/gate/matchkey.go` (new) |
| 6 | `internal/tools/agents/mcp_askuser.go` (new), `view/askuser.templ` |
| 7 | `.vscode/{tasks,launch}.json` |

---

## 0. TL;DR

**Command Gate** = mekanisme intercept shell command sebelum Claude mengeksekusinya. User bisa approve atau block command secara real-time tanpa restart session.

Dokumen ini menjelaskan:
- Kenapa gate diperlukan dan bagaimana cara kerjanya
- Perbandingan dua pola approval (Claude Code style vs Gate style)
- Perbandingan empat opsi IPC antara gate dan daemon
- Detail Unix Domain Socket — cara kerja, keamanan, isi file
- Bagaimana Web UI perlu render dua jenis interaksi (gate approval + AskUser)
- Cara release dengan dua binary (`wick` + `wick-gate`) termasuk MSI
- Cara resolve path gate di tiga environment: VSCode, serve, MSI
- Rekomendasi akhir dengan justifikasi

---

## 1. Latar Belakang: Kenapa Gate Diperlukan?

### 1.1 Masalah Tanpa Gate

Claude berjalan sebagai subprocess long-lived. Begitu user kirim pesan, Claude bisa langsung eksekusi shell command:

```
User: "hapus semua log lama"
Claude: [langsung jalankan: find /var/log -mtime +30 -delete]
        → tidak ada yang bisa stop
```

Tidak ada titik intercept. Command sudah jalan sebelum user sempat berpikir.

### 1.2 Solusi: PreToolUse Hook

Claude CLI menyediakan hook system — sebelum tool (Bash, dll.) dieksekusi, Claude memanggil binary eksternal dan **menunggu exit code-nya**:

```
exit 0  → lanjutkan eksekusi
exit 2  → batalkan, Claude dapat pesan "blocked by user"
```

`wick-gate` adalah binary yang dipanggil oleh hook ini. Dia yang memutuskan allow atau block.

### 1.3 Sesi Claude Tidak Di-Respawn Per Pesan

Penting untuk dipahami: **Claude tidak di-spawn ulang setiap pesan**. Satu proses Claude hidup sepanjang sesi, menerima pesan via stdin dan membalas via stdout.

```
[kamu] "hai apa kabar"  →  stdin → [claude PID 1234]
[kamu] "tanya lagi"     →  stdin → [claude PID 1234]  ← PID sama
```

Proses baru hanya di-spawn kalau:
- Idle timeout (120 detik tanpa event) → kill → respawn dengan `--resume`
- Explicit `Stop()` dipanggil

Konsekuensinya: gate bisa block di tengah turn yang sama, Claude tetap menunggu. Tidak ada race condition karena proses mati di tengah jalan.

### 1.4 Built-in vs wick-gate

Claude Code punya dialog permission bawaan (TUI terminal):

```
Allow this bash command?
  rtk git status
  Show working tree status

  1 Yes
  2 Yes, allow rtk git * for this session
  3 No

  Tell Claude what to do instead
```

**Wick sengaja mematikan dialog ini** dengan set `bypassPermissions = true` di `settings.json`, lalu pasang `wick-gate` sebagai penggantinya:

```
Tanpa Wick → dialog TUI terminal muncul
Dengan Wick → bypassPermissions = true → dialog mati → wick-gate aktif
```

| | Claude Code Built-in | wick-gate |
|---|---|---|
| UI | Terminal TUI | Web UI Wick |
| "For this session" | Ada otomatis | Perlu diimplementasi |
| Siapa yang render | Claude Code harness | Daemon Wick via SSE |
| Configurable per rule | Terbatas | Full control via spec.json |

---

## 2. Dua Pola Approval

Ada dua cara fundamental untuk mendapat konfirmasi user sebelum command jalan.

### 2.1 Pola A — Gate Style (System Intercept) ✅ REKOMENDASI

System yang memaksa konfirmasi, bukan Claude.

```
Step 1:  User kirim pesan ke Claude
Step 2:  Claude putuskan untuk jalankan command
         → hook fire → gate dipanggil → gate BLOCK
         → UI muncul di user: "Approve rm -rf /data?"
         → user klik Approve
         → gate exit 0 → command jalan (masih dalam turn yang sama)
Step 3:  Claude selesai, balas ke user
```

**Kelebihan:**
- Jaminan 100% setiap Bash command pasti melewati gate, tidak bisa di-bypass Claude
- Blocking dalam satu turn — tidak perlu turn baru
- Audit log otomatis via `commands.jsonl`

**Kekurangan:**
- Lebih kompleks untuk diimplementasi
- Perlu binary terpisah (`wick-gate`) + endpoint daemon + socket

### 2.2 Pola B — Claude Code Style (Voluntary Ask)

Claude sendiri yang memutuskan untuk tanya sebelum bertindak. Ini yang dipakai ketika Claude menampilkan pertanyaan dengan pilihan di chat.

```
Turn 1: User: "hapus log lama"
        Claude: "Ini akan hapus /var/log/app.log. Lanjut?" ← turn selesai

Turn 2: User: "iya"
        Claude: [jalankan: rm /var/log/app.log]
        Claude: "Berhasil dihapus"
```

Mekanismenya: Claude output `tool_use` dengan nama `AskUserQuestion` ke stream, frontend render jadi UI interaktif, user jawab masuk sebagai tool result ke turn berikutnya.

**Kelebihan:**
- Tidak perlu gate binary sama sekali
- Lebih natural, conversational

**Kekurangan:**
- Claude bisa "lupa" untuk tanya → command langsung jalan
- Tidak bisa jadi security enforcement
- `AskUserQuestion` adalah tool harness Claude Code — **tidak tersedia** saat Claude jalan sebagai subprocess Wick (`-p` pipe mode)

### 2.3 Perbandingan Lengkap

| Dimensi | Gate Style (Pola A) | Claude Code Style (Pola B) |
|---|---|---|
| **Jumlah step** | 3 (dari perspektif user) | 4 (turn-based) |
| **Yang memutuskan tanya** | System (selalu) | Claude (boleh lupa) |
| **Bisa di-bypass Claude?** | Tidak — system-level | Ya — Claude bisa langsung eksekusi |
| **Jaminan intercept** | 100% setiap command | Bergantung prompt + behavior Claude |
| **Perlu gate binary?** | Ya | Tidak |
| **Perlu backend endpoint?** | Ya | Tidak |
| **Blocking** | Dalam turn yang sama | Butuh turn baru |
| **Channel komunikasi** | IPC (socket/pipe) | stdin/stdout turn-based |
| **Tersedia di Wick subprocess** | Ya (kita yang pasang) | Tidak (harness-only) |
| **Cocok untuk** | Security-critical, audit wajib | UX conversational, low-risk |

### 2.4 Kapan Pakai Yang Mana?

```
Butuh JAMINAN bahwa setiap command pasti di-approve?
├── Ya → Pola A (Gate Style)
└── Tidak
    └── Cukup Claude tanya sendiri untuk action besar? → Pola B (Claude Code Style)
```

**Keputusan**: Wick pakai **Pola A** untuk enforcement. Pola B tidak bisa menjamin intercept dan tidak tersedia di pipe mode.

---

## 3. Opsi IPC: Gate ↔ Daemon

Gate adalah subprocess terpisah dari daemon. Mereka perlu berkomunikasi. Ada empat opsi.

### 3.1 HTTP (TCP)

```go
// gate
resp, _ := http.Post("http://localhost:9425/api/agents/approve",
    "application/json", payload)
```

| | |
|---|---|
| **Kelebihan** | Familiar, tooling lengkap (curl debug), mudah test |
| **Kekurangan** | Port bisa diakses dari network, perlu auth token, overhead HTTP |
| **Performa** | ~1-5ms (TCP handshake + HTTP parsing) |
| **Keamanan** | Harus bind 127.0.0.1 + auth, tetap ada risiko port scanning |

### 3.2 Unix Domain Socket ✅ DIPILIH

```go
// gate
conn, _ := net.Dial("unix", "~/.wick/sessions/<id>/gate.sock")
json.NewEncoder(conn).Encode(request)
json.NewDecoder(conn).Decode(&response)  // blocking sampai daemon balas
```

| | |
|---|---|
| **Kelebihan** | Zero network exposure, akses via file permission, zero port, cepat |
| **Kekurangan** | Hanya lokal, satu machine |
| **Performa** | ~0.1ms, tanpa TCP overhead, tanpa HTTP parsing |
| **Keamanan** | chmod 0600 cukup, tidak bisa diakses dari network sama sekali |
| **OS Support** | Linux ✅, macOS ✅, Windows 10 build 1803+ ✅ |

### 3.3 Named Pipe / FIFO

```bash
mkfifo gate-req.fifo gate-res.fifo
# gate: tulis ke req → baca dari res
# daemon: baca dari req → tulis ke res
```

| | |
|---|---|
| **Kelebihan** | Zero dependency, primitif, ada di semua Unix |
| **Kekurangan** | Perlu dua file per session, tidak bisa concurrent requests |
| **Performa** | ~0.1ms |

### 3.4 File + inotify / Polling

```
gate tulis → ~/.wick/sessions/<id>/gate/pending/abc123.json
daemon watch dir → baca → proses
daemon tulis → ~/.wick/sessions/<id>/gate/decision/abc123.json
gate poll / watch → baca
```

| | |
|---|---|
| **Kelebihan** | Audit trail otomatis, debuggable dengan `cat` |
| **Kekurangan** | Polling = latency, file di disk = risiko leak credential |
| **Performa** | 10-100ms kalau polling, ~1ms kalau inotify |

### 3.5 Perbandingan Empat Opsi

| Dimensi | HTTP | Unix Socket | Named Pipe | File+inotify |
|---|---|---|---|---|
| **Network exposure** | Ya (loopback) | Tidak | Tidak | Tidak |
| **Concurrent requests** | Ya | Ya | Tidak | Ya (per file) |
| **Overhead** | Tinggi | Rendah | Rendah | Tinggi (polling) |
| **Debug** | Mudah (curl) | Sedang | Sulit | Mudah (cat file) |
| **Auth diperlukan** | Ya | Tidak | Tidak | Tidak |
| **Bidirectional** | Ya | Ya | Perlu 2 pipe | Perlu 2 dir |
| **Windows support** | Ya | Build 1803+ | Tidak | Ya |
| **Implementasi Go** | `net/http` | `net.Listen("unix")` | `os.OpenFile` | `os` + poll |

**Keputusan: Unix socket.** Tidak ada network exposure, performa terbaik, implementasi hampir sama dengan HTTP tapi ganti `tcp` → `unix`.

---

## 4. Deep Dive: Unix Domain Socket

### 4.1 Apa Itu File Socket?

Socket file **bukan file biasa**. Tidak ada data di dalamnya.

```bash
$ ls -la gate.sock
srwxr-xr-x 1 user user 0 May 9 10:00 gate.sock
# ^--- "s" = socket, bukan "-" regular file. Ukuran selalu 0 bytes.

$ cat gate.sock
cat: gate.sock: No such device or address  ← tidak bisa dibaca seperti file
```

Socket file adalah **alamat titik temu** — seperti nomor telepon. Data mengalir di kernel memory buffer, tidak pernah menyentuh disk.

```
gate.sock di filesystem
     │
     │  bukan tempat data disimpan
     │  tapi "pintu" yang bisa di-connect
     │
     ├── gate  → connect() → buka koneksi ke daemon
     └── daemon → listen() → terima koneksi dari gate
                   │
                   └── data JSON mengalir di kernel buffer
                       tidak pernah ke disk
```

**Analogi**: colokan listrik di dinding. Tidak ada "isi" di colokan, tapi kalau kamu colok sesuatu, arus mengalir.

### 4.2 Protokol: Raw JSON Newline-Delimited

Tidak ada protokol HTTP. Langsung kirim JSON diakhiri newline:

```
gate → daemon:   {"id":"abc","cmd":"rm -rf /data","agent":"backend"}\n
daemon → gate:   {"decision":"block","reason":"destructive command"}\n
```

Di Go, `json.NewEncoder` otomatis append newline, `json.NewDecoder` blocking sampai ada data:

```go
// Kirim — satu baris JSON + newline otomatis
json.NewEncoder(conn).Encode(req)

// Terima — blocking sampai daemon tulis jawaban
json.NewDecoder(conn).Decode(&resp)
```

### 4.3 Keamanan

```
❌ /tmp/wick.sock        — /tmp world-writable, proses lain bisa connect
✅ ~/.wick/sessions/<id>/gate.sock  — direktori chmod 700, hanya owner
```

```go
ln, _ := net.Listen("unix", socketPath)
os.Chmod(socketPath, 0600)  // hanya owner bisa read/write socket ini
```

Kalau mau lebih ketat, bisa verify peer credentials (`SO_PEERCRED`) untuk pastikan hanya `wick-gate` dengan UID yang benar yang bisa connect — tapi untuk Wick, `chmod 0600` di session directory sudah cukup.

### 4.4 Lifecycle Socket File

```
Daemon start:
  1. os.Remove(socketPath)      ← hapus sisa run sebelumnya
  2. net.Listen("unix", path)   ← buat socket baru
  3. os.Chmod(path, 0600)       ← lock permission

Daemon running:
  ← terima koneksi masuk (goroutine per connection)

Daemon crash/stop:
  File socket tetap ada di disk tapi tidak bisa di-connect
  Gate: connect() → "connection refused" → fail-safe exit 2

Daemon restart:
  Step 1 hapus sisa → socket baru, tidak ada konflik
```

---

## 5. Flow Lengkap: Mid-Session Approval

### 5.1 Happy Path — User Approve

```
Claude (PID 1234)        wick-gate          daemon         User (Web)
      │                      │                 │               │
      │ mau jalankan         │                 │               │
      │ "git clone ABC"      │                 │               │
      ├──fork────────────────►                 │               │
      │  (nunggu exit code)  │                 │               │
      │                      ├──connect────────►               │
      │                      ├──{"id":"x",     │               │
      │                      │   "cmd":"git"}──►               │
      │                      │  (BLOCK di sini)│               │
      │                      │                 ├──SSE event────►
      │                      │                 │               │ render modal:
      │                      │                 │               │ "Approve git clone?"
      │                      │                 │               │ [Approve] [Block]
      │                      │                 │◄──POST /approve┤
      │                      │                 │  {"decision":  │
      │                      │                 │   "approve"}   │
      │                      │◄──{"decision":  │               │
      │                      │    "approve"}───┤               │
      │◄──exit 0─────────────┤                 │               │
      │                      │                 │               │
      │ git clone ABC jalan  │                 │               │
```

### 5.2 User Block

Sama sampai modal muncul, user klik Block:

```
      │◄──{"decision":"block"}──┤
      │◄──exit 2────────────────┤
      │
      │ [tool blocked]
      │ Claude: "Command blocked by user"
```

### 5.3 Timeout (User Tidak Respond)

```
Daemon set deadline 25 detik (< hook timeout 30 detik Claude)
Setelah 25 detik:
  daemon → {"decision":"block","reason":"timeout"}
  gate → exit 2
  Claude: "Command blocked (timeout)"
```

### 5.4 Daemon Tidak Jalan

```
gate: connect() → "no such file" atau "connection refused"
gate: fail-safe → exit 2 (block semua)
Claude: "Command blocked"
```

---

## 6. Web UI: Dua Jenis Interaksi

Web UI Wick perlu handle **dua jenis interaksi yang berbeda** yang keduanya muncul dari SSE stream.

### 6.1 Gate Approval (Baru)

Dipicu saat `wick-gate` mengirim request ke daemon. Daemon broadcast SSE event dengan tipe baru.

**SSE event dari daemon:**

```json
{
  "session_id": "sess_xyz",
  "agent_name": "backend",
  "type": "approval_request",
  "data": "{\"id\":\"abc123\",\"cmd\":\"rm -rf /data\",\"tool\":\"Bash\",\"work_dir\":\"/home/user/project\"}"
}
```

**Yang perlu dirender:** modal/card menampilkan command, agent, work dir, countdown timer, plus 4 tombol decision (lihat §6.1.1).

**Response dari UI:** `POST /api/agents/sessions/{id}/approve` dengan `{"id":"abc123","decision":"<mode>"}`.

**Timing:** harus dijawab dalam 25 detik atau otomatis di-block oleh daemon.

#### 6.1.1 Decision Modes

User punya empat pilihan saat modal muncul. Tiga di antaranya = approve, satu block. Mode beda di scope memori-nya.

| Decision | API value | Scope | Future requests yang sama |
|---|---|---|---|
| **Approve once** | `approve_once` | Cuma request ini | Tetap muncul modal |
| **Allow this session** | `approve_session` | Sepanjang session hidup (sampai session deleted/restart) | Auto-approve, tidak muncul modal |
| **Always allow** | `approve_always` | Persistent (tersimpan di workspace/general config) | Auto-approve di semua session sekarang & masa depan |
| **Block** | `block` | Cuma request ini | Tetap muncul modal |

**Match key** (untuk auto-approve di session/always): hash dari `(tool, normalized_cmd_pattern)`. Pattern normalization = strip args yg bersifat data (file paths, URLs) tapi keep root command — supaya `git status` dan `git status -s` di-treat sebagai pattern berbeda kalau user tepat. MVP keep simple: exact-string match dulu, pattern engine ditunda.

**Storage:**
- `approve_session` → in-memory map di daemon, key `sessionID + matchKey`, hilang saat daemon restart
- `approve_always` → `gate/spec.json` field `auto_approved: ["<matchKey>", ...]` → diisi ulang saat gate spec di-rewrite, jadi gate binary sendiri yg auto-allow tanpa round-trip ke daemon (zero latency)

**Revocation:** UI `/tools/agents/sessions/{id}` punya panel "Approved commands" — list semua entry session + always, tombol Revoke per item.

### 6.2 AskUser dari Agent (Web Flow)

Wick sediakan `AskUser` sebagai **MCP tool** (bukan harness tool) sehingga tersedia di pipe mode (`-p`) untuk semua CLI yang attach ke wick MCP. Agent panggil tool ini saat butuh input dari user; tool block sampai user balas via web UI.

#### 6.2.1 Mekanisme

```
Agent panggil MCP tool "ask_user" dengan {question, options[]}
  → MCP handler register pending question di daemon (UUID + channel)
  → broadcast SSE: {type: "ask_user", data: {id, question, options}}
  → Web UI render card di session detail (composer area)
  → User pilih option / ketik free text → POST /sessions/{id}/answer {id, answer}
  → daemon resolve channel → MCP tool return jawaban ke agent
  → agent lanjut turn dengan jawaban sebagai tool result
```

Beda dari gate approval: **tidak ada hook subprocess**, tidak ada exit code. Murni MCP request/response yg di-bridge ke web via SSE.

#### 6.2.2 SSE Event

```json
{
  "session_id": "sess_xyz",
  "agent_name": "backend",
  "type": "ask_user",
  "data": "{\"id\":\"q_abc123\",\"question\":\"Pakai PostgreSQL atau MySQL?\",\"options\":[{\"label\":\"Postgres\",\"value\":\"pg\"},{\"label\":\"MySQL\",\"value\":\"mysql\"}],\"allow_freeform\":true}"
}
```

#### 6.2.3 Response

`POST /api/agents/sessions/{id}/answer` dengan `{"id":"q_abc123","answer":"pg"}` (atau `{"id":"q_abc123","answer_text":"...freeform..."}`).

#### 6.2.4 Timeout

Default 5 menit (config-able). Lewat timeout → MCP tool return error `"user did not respond"` → agent boleh decide retry / abort.

### 6.3 Perbedaan Dua Interaksi di UI

| | Gate Approval | AskUser |
|---|---|---|
| **Trigger** | SSE `type: approval_request` | SSE `type: ask_user` |
| **Sumber** | `wick-gate` hook (subprocess) → daemon socket | MCP tool `ask_user` dipanggil agent |
| **Deadline** | 25 detik (sebelum hook timeout 30s) | 5 menit (config-able) |
| **Response ke** | `POST /approve` → daemon → unblock gate (exit 0/2) | `POST /answer` → daemon → unblock MCP tool return |
| **Agent state** | Mid-turn, tool execution di-pause | Mid-turn, tool execution di-pause (MCP tool block) |
| **Visual** | Modal full-screen dgn countdown | Card inline di area composer |
| **Bisa diabaikan?** | Tidak (auto-block setelah timeout) | Tidak (timeout → agent dapat error) |
| **Channel komunikasi** | Unix socket (gate↔daemon) | MCP request/response (agent↔daemon) |

### 6.4 Existing SSE Infrastructure

Wick sudah punya `Broadcaster` di `internal/tools/agents/stream.go` yang fan-out events ke semua SSE subscriber. Event shape yang sudah ada:

```go
type Event struct {
    SessionID string `json:"session_id"`
    AgentName string `json:"agent_name"`
    Type      string `json:"type"`   // existing: "text", "tool_use", "result", dll.
    Data      string `json:"data"`
}
```

Untuk fitur baru, cukup tambah tipe event dan publish via broadcaster yang sama:

| Type | Sumber | Tujuan |
|---|---|---|
| `approval_request` | Daemon (saat gate connect) | UI render modal approval |
| `approval_resolved` | Daemon (saat decision masuk) | UI dismiss modal di semua tab |
| `ask_user` | MCP handler (saat tool dipanggil) | UI render card pertanyaan |
| `ask_user_resolved` | MCP handler (saat answer masuk) | UI dismiss card di semua tab |

Frontend tinggal handle tipe baru ini di SSE listener.

---

## 7. Struktur Data

### 7.1 Request: Gate → Daemon

```go
type ApprovalRequest struct {
    ID        string `json:"id"`         // UUID per request
    SessionID string `json:"session_id"`
    Agent     string `json:"agent"`      // "backend", dll.
    Tool      string `json:"tool"`       // "Bash", "Edit", dll.
    Cmd       string `json:"cmd"`        // command yang mau dieksekusi
    WorkDir   string `json:"work_dir"`   // cwd saat eksekusi
    Timestamp int64  `json:"ts"`         // unix ms
}
```

### 7.2 Response: Daemon → Gate

```go
type ApprovalResponse struct {
    ID       string `json:"id"`       // sama dengan request ID
    Decision string `json:"decision"` // "approve" atau "block"
    Reason   string `json:"reason"`   // opsional
}
```

### 7.3 State Machine di Daemon

```
[idle]
  │
  │ gate connect + send request
  ▼
[pending] ─── 25s timeout ──────────────────────► [auto-block]
  │                                                     │
  │ user klik Approve                                   │
  ▼                                                     │
[approved]                                             │
  │                                                     │
  └──────────────────────────────────────────────────── ┘
                        │
                        ▼
              tulis response ke socket
              broadcast SSE "approval_resolved"
              hapus dari pending map
                        │
                        ▼
                     [idle]
```

**Concurrent requests** — daemon pegang banyak pending sekaligus dengan `sync.Map` + channel per connection:

```go
type pendingApproval struct {
    req ApprovalRequest
    ch  chan ApprovalResponse
}

var pending sync.Map // map[id]pendingApproval

// per goroutine (satu per koneksi gate):
ch := make(chan ApprovalResponse, 1)
pending.Store(req.ID, pendingApproval{req, ch})
defer pending.Delete(req.ID)

select {
case resp := <-ch:
    json.NewEncoder(conn).Encode(resp)
case <-time.After(25 * time.Second):
    json.NewEncoder(conn).Encode(ApprovalResponse{
        Decision: "block", Reason: "timeout",
    })
}
```

---

## 8. Release: Dua Binary

Wick saat ini punya satu binary utama. Untuk mid-session approval, perlu ship **dua binary**:

| Binary | Fungsi |
|---|---|
| `wick` (atau nama app) | Server daemon, web UI, semua logic utama |
| `wick-gate` | Hook binary kecil, dipanggil Claude sebelum Bash |

### 8.1 Bagaimana Build System Wick Bekerja

Wick pakai `internal/builder` — satu package yang handle compile + packaging per platform:

```
wick build              → compile binary + .dmg/.deb/.exe
wick build --installer  → tambah .msi (Windows) / Applications symlink (macOS)
wick build --all        → semua target (windows/amd64, windows/arm64, linux/*, darwin/*)
```

Flow `builder.Build()`:
1. Generate assets (templ + CSS + go generate)
2. Windows: embed icon + version metadata via `.syso` sebelum compile
3. `go build -ldflags "..."` → raw binary
4. Package per platform: `.app`+`.dmg` (macOS), `.deb` (Linux), `.msi` (Windows, opt-in)

### 8.2 Strategi: Embed wick-gate ke Main Binary ✅ DIPILIH

`wick-gate` di-compile dulu untuk platform target, lalu di-embed sebagai bytes di dalam main binary via `//go:embed`. Saat daemon start pertama kali per session, binary di-extract ke session directory.

```go
//go:embed assets/wick-gate-*
var embeddedGates embed.FS

func extractEmbeddedGate(sessionDir string) (string, error) {
    name := fmt.Sprintf("assets/wick-gate-%s-%s", runtime.GOOS, runtime.GOARCH)
    if runtime.GOOS == "windows" {
        name += ".exe"
    }
    data, err := embeddedGates.ReadFile(name)
    if err != nil {
        return "", fmt.Errorf("embedded gate not found for %s/%s", runtime.GOOS, runtime.GOARCH)
    }
    gatePath := filepath.Join(sessionDir, "gate", "wick-gate")
    if runtime.GOOS == "windows" {
        gatePath += ".exe"
    }
    if err := os.MkdirAll(filepath.Dir(gatePath), 0700); err != nil {
        return "", err
    }
    if err := os.WriteFile(gatePath, data, 0755); err != nil {
        return "", err
    }
    return gatePath, nil
}
```

Keuntungan:
- User download satu file — tidak ada binary terpisah yang bisa ketinggalan
- Version selalu sinkron (gate di-compile bersama main binary)
- MSI tidak perlu diubah sama sekali — `msi.go` tetap ship satu `.exe`
- `.deb`, `.dmg`, raw binary — semua sama, tidak ada perubahan

Trade-off:
- Main binary sedikit lebih besar (~2-5MB per platform yang di-embed)
- Hanya embed gate untuk platform yang di-build (bukan semua platform sekaligus)

> Opsi yang tidak dipilih: sidecar binary (dua file terpisah di MSI → risiko version mismatch) dan subcommand `wick gate` (load binary besar untuk proses kecil yang dipanggil ratusan kali per session).

### 8.3 Build Pipeline di CI

Template release workflow (`template/.github/workflows/release.yml`) perlu satu step tambahan **sebelum** `wick build --installer` di setiap matrix job:

```yaml
# Di build job, SEBELUM step "Build":
- name: Build wick-gate
  env:
    GOOS: ${{ matrix.os }}
    GOARCH: ${{ matrix.arch }}
  run: |
    EXT=""
    [ "${{ matrix.os }}" = "windows" ] && EXT=".exe"
    mkdir -p assets
    go build -o "assets/wick-gate-${{ matrix.os }}-${{ matrix.arch }}${EXT}" ./cmd/wick-gate

- name: Build          # ← step existing, tidak berubah
  run: wick build --installer
```

`wick-gate` pure Go (no CGO) sehingga cross-compile works di semua runner. Gate di-compile untuk target platform yang sama dengan main binary, lalu `//go:embed assets/wick-gate-*` otomatis picks it up saat `go build` main binary.

### 8.4 Template Downstream

Proyek downstream yang pakai Wick sebagai framework:
- Tidak perlu buat `cmd/wick-gate/` sendiri — bisa reuse binary dari Wick atau skip gate
- CI workflow tinggal tambah step build gate seperti di atas
- `wick build --installer` tetap tidak berubah

---

## 9. Resolve Gate Binary per Environment

Ada tiga environment dengan cara berbeda untuk menemukan `wick-gate`:

```
Environment           Gate binary dari mana         Cara set
──────────────────────────────────────────────────────────────────
VSCode (wicklab)   →  bin/wick-gate.exe (lokal)  →  WICK_GATE_BIN di .env
Serve (raw binary) →  embedded → extract sekali  →  otomatis
MSI (installer)    →  embedded → extract sekali  →  otomatis
```

### 9.1 Logic Resolve di Daemon

```go
func resolveGateBin(sessionDir string) (string, error) {
    // Dev override — set di .env untuk VSCode / go run
    if p := os.Getenv("WICK_GATE_BIN"); p != "" {
        return p, nil
    }
    // Production: extract dari embed ke session dir (sekali per session)
    return extractEmbeddedGate(sessionDir)
}
```

Urutan prioritas: `WICK_GATE_BIN` env → embedded binary → **sibling-of-executable** (`wick-gate[.exe]` di folder yang sama dgn parent binary) → `wick-gate` di PATH. Kalau semuanya gak ada → gate tidak aktif, log warning, commands lolos semua (fail-open, logged).

### 9.2 VSCode (wicklab)

**Launch config:** `.vscode/launch.json` → `wicklab` → `preLaunchTask: "debug: prep"`

Cara kerjanya simpel — gak ada launch khusus untuk gate, karena gate selalu di-spawn oleh claude (anak wicklab) saat command perlu di-approve.

#### Setup

`debug: prep` task build dua binary ke `bin/`:

```json
{
  "label": "debug: prep",
  "type": "shell",
  "command": "templ generate ./... && bin/tailwindcss.exe -i web/src/input.css -o web/public/css/app.css && go build -o bin/wick-gate.exe ./cmd/wick-gate",
  "problemMatcher": []
}
```

Saat F5 `wicklab`:
- VSCode build wicklab → `bin/wick-lab.exe` (via launch `output` field)
- `debug: prep` udah build → `bin/wick-gate.exe`
- Hasilnya: keduanya satu folder

Saat wicklab boot panggil `gate.ResolveGateBinary`, sibling-of-executable check langsung pickup `bin/wick-gate.exe` — tanpa env var, tanpa task tambahan.

#### Cara Debug Gate

wick-gate **gak bisa di-debug standalone** dgn launch terpisah, karena dia stateless forwarder yg butuh `WICK_GATE_SPEC` env (di-inject parent). Pakai salah satu cara berikut:

**1. Debug via test** (paling praktis)

Buka [internal/agents/gate/integration_test.go](../agents/gate/integration_test.go) atau [cmd/wick-gate/main_test.go](../../../cmd/wick-gate/main_test.go), set breakpoint di [main.go:run()](../../../cmd/wick-gate/main.go), lalu right-click test function → "Debug Test". Test sudah set spec + env + stdin secara realistic.

**2. Logs**

wick-gate tulis decision ke `commands.jsonl` di session dir:

```
~\.wick\agents\sessions\<id>\commands.jsonl
```

Tail file itu sambil F5 wicklab + trigger command via web UI.

**3. Attach to process** (rare)

wick-gate hidup cuma milidetik per call, susah caught. Hanya berguna untuk kasus stuck (socket timeout dll).

#### Flow normal (no gate debugging)

```
1. F5 → "wicklab"                 → daemon jalan + bin/wick-gate.exe ready
2. Buat session di web UI          → wicklab tulis spec.json + start socket listener
3. Kirim pesan ke claude di web UI → claude jalan, command picu wick-gate
4. Gate gak whitelisted → modal approval muncul di web UI
5. Klik salah satu (approve_once / session / always / block)
```

### 9.3 MSI (Windows Installer)

Dibangun via `wick build --installer`. Flow CI:

```
1. go build -o bin/wick-gate-windows-amd64.exe ./cmd/wick-gate   ← step baru di workflow
2. wick build --installer                                          ← existing, tidak berubah
   → compile main binary (embed wick-gate via //go:embed)
   → wixl → .msi (satu binary, wick-gate sudah di dalam)
```

Di-install ke `%LocalAppData%\Programs\<AppName>\<AppName>.exe`. Saat daemon start, gate di-extract ke session dir — tidak perlu WICK_GATE_BIN.

### 9.4 Serve (Raw Binary / Linux / Docker)

Binary dari `wick build` tanpa `--installer`, atau `.deb`, atau Docker image. Sama dengan MSI dari sisi gate: embedded, di-extract ke `~/.wick/sessions/<id>/gate/wick-gate` saat session start.

```
docker run myapp server     → gate di-extract dari embed otomatis
./myapp server              → sama
systemctl start myapp       → sama
```

Tidak ada konfigurasi tambahan yang diperlukan.

### 9.5 Perbandingan Tiga Environment

| | VSCode (wicklab) | Serve / raw binary | MSI |
|---|---|---|---|
| **Gate binary dari** | `bin/wick-gate.exe` (sibling-of-exe) | Embedded → extracted | Embedded → extracted |
| **Cara set** | Otomatis (sibling discovery) | Otomatis (embed extract) | Otomatis (embed extract) |
| **Perlu build manual?** | Ya (via `debug: prep` task) | Tidak | Tidak |
| **Version sync** | Manual (rebuild saat ada perubahan) | Selalu sync (embedded saat compile) | Selalu sync |
| **File yang perlu diedit** | `.vscode/tasks.json` saja | Tidak ada | Tidak ada |

### 9.6 Template Downstream (cmd/lab)

Proyek yang pakai Wick sebagai framework perlu:

1. `cmd/wick-gate/` — bisa copy dari wick atau implement sendiri sesuai rules mereka
2. `.env.example` — tambah `WICK_GATE_BIN` entry (sudah ada di template)
3. `.vscode/tasks.json` — tambah gate build ke `debug: prep`
4. CI workflow — tambah `go build ./cmd/wick-gate` sebelum `wick build --installer`

---

## 10. Lokasi File di Filesystem (Runtime)

```
~/.wick/agents/sessions/<session-id>/
  ├── meta.json                  ← session metadata
  ├── agents.json                ← agent list + CLI session ID
  ├── commands.jsonl             ← audit log semua command
  └── gate/
      ├── spec.json              ← rules whitelist untuk gate
      ├── settings.json          ← Claude hook config (PreToolUse → wick-gate)
      └── gate.sock              ← Unix domain socket
                                    dibuat saat daemon start, chmod 0600
                                    dihapus saat daemon stop
```

Kalau pakai embed (opsi 1):

```
~/.wick/agents/sessions/<session-id>/gate/
  └── wick-gate                  ← di-extract dari embedded binary saat start
                                    chmod 0755, di-recreate tiap spawn
```

---

## 11. Keputusan Desain

| # | Keputusan | Alasan |
|---|---|---|
| D1 | Pakai Unix socket, bukan HTTP | Tidak ada network exposure, performa lebih baik, akses dikontrol filesystem |
| D2 | Socket path di session directory | Direktori sudah chmod 700, isolasi per session, tidak perlu auth tambahan |
| D3 | Raw JSON newline-delimited, bukan HTTP | Tidak ada overhead parsing HTTP header, protokol lebih simpel |
| D4 | Timeout 25 detik di daemon (< hook timeout 30 detik) | Pastikan gate sempat exit bersih sebelum Claude timeout |
| D5 | Fail-safe: block kalau daemon tidak respond | Lebih aman default block daripada default allow |
| D6 | Pending state: `sync.Map` + channel per koneksi | Concurrent safe, goroutine per koneksi, no mutex contention |
| D7 | Gate binary tetap stateless | Semua state di daemon. Gate bisa crash/respawn tanpa kehilangan pending |
| D8 | Embed wick-gate ke binary utama (rekomendasi) | User satu file, version selalu sync, tidak perlu installer logic baru |
| D9 | Broadcast approval_request via Broadcaster yang sudah ada | Tidak perlu infrastruktur SSE baru, cukup tambah tipe event |
| D10 | `WICK_GATE_BIN` env var override untuk dev | VSCode/go run tidak punya embed, perlu path eksplisit. Env var paling tidak invasif — tidak ubah kode path, tidak ubah interface |
| D11 | `debug: prep` task build gate otomatis | Developer tidak perlu ingat build gate manual sebelum debug — F5 langsung siap |
| D12 | Drop `gate: sync-spec` task + `envFile`; operator set `WICK_GATE_SPEC` manual | wick-gate cuma baca env var (tidak ada home-dir discovery), jadi sebelumnya pakai task yang tulis path session terbaru ke `bin/.gate-debug.env` + envFile launch. Trade-off: 1 langkah manual vs ~30 baris tooling untuk save 5 detik per debug session. Pilih simpel — debug gate jarang, dan eksplisit lebih mudah di-troubleshoot kalau mismatch path |
| D13 | Drop `wicklab-gate` launch entirely + tambah sibling-of-executable resolution | wick-gate gak bisa di-debug standalone karena butuh `WICK_GATE_SPEC` env yg di-inject parent. Launch standalone selalu fail-safe exit 2 — bikin operator bingung. Solusi: resolve gate via sibling discovery (sama folder dgn parent .exe) — `debug: prep` task drop binary di `bin/`, wicklab pickup otomatis tanpa env. Debug gate dilakuin via `Debug Test` di VSCode (lihat test files), bukan launch terpisah |

---

## 12. Checklist Implementasi (Detail)

Versi ringkas (just the boxes) di section paling atas dokumen. Sini detail per task + exit criteria. Urutan **timeline-aware**: tiap stage hanya butuh stage sebelumnya. Bisa di-pause di akhir stage manapun dan tetap shippable (gate fallback ke whitelist-mode kalau socket belum ada).

### Stage 1 — Spec & Wiring Foundation

Tujuan: gate spec siap menampung field baru (socket path, auto-approved). Tidak ada perubahan runtime behavior.

```
[ ] S1.1 Tambah field di gate.Spec: SocketPath string, AutoApproved []string
         → internal/agents/gate/spec.go
[ ] S1.2 gate.WriteSpawnArtifacts: tulis SocketPath = <sessionDir>/gate/gate.sock
         → internal/agents/gate/claude_hook.go
[ ] S1.3 Wire Gate di factory.go (saat ini Gate field masih nil di FactoryOptions)
         inject GateConfig + spawn artifact write per session
         → internal/agents/pool/factory.go
[ ] S1.4 Unit test spec marshal + artifact write pakai t.TempDir()
         → internal/agents/gate/{spec,claude_hook}_test.go
```

**Exit criteria**: `wick-gate` baca spec.json yang sudah punya socket_path field; behavior tetap whitelist-only (socket belum dipakai).

### Stage 2 — Daemon Socket Listener

Tujuan: daemon expose socket per session, terima konek tapi belum ada UI — auto-block semua request (smoke test only).

```
[ ] S2.1 Unix socket listener per session, dibuat saat session start, chmod 0600
         path: ~/.wick/agents/sessions/<id>/gate/gate.sock
         → internal/agents/gate/socket.go (paket baru atau extend gate/)
[ ] S2.2 Cleanup: os.Remove socket saat session stop / daemon shutdown
[ ] S2.3 Goroutine per koneksi: read JSON request, send JSON response (raw newline-delimited)
[ ] S2.4 Pending state manager: sync.Map[id]chan ApprovalResponse
[ ] S2.5 Timeout goroutine: 25s → auto-block kalau tidak ada decision
[ ] S2.6 Test: dial socket dari fake gate, kirim ApprovalRequest, expect timeout=block
         → internal/agents/gate/socket_test.go
```

**Exit criteria**: `nc -U gate.sock` bisa konek, kirim JSON dummy, dapat `{"decision":"block","reason":"timeout"}` setelah 25s.

### Stage 3 — Gate Binary Upgrade

Tujuan: `wick-gate` binary konek ke socket sebelum decide. Fallback ke whitelist + block jika socket tidak ada.

```
[ ] S3.1 wick-gate baca SocketPath dari spec, dial unix socket
         → cmd/wick-gate/main.go
[ ] S3.2 Cek auto_approved list di spec → kalau match, langsung exit 0 tanpa round-trip
         (zero-latency path untuk "always allow")
[ ] S3.3 Build ApprovalRequest, encode JSON, kirim ke socket
[ ] S3.4 Decode ApprovalResponse → exit 0 (approve_*) atau 2 (block)
[ ] S3.5 Fail-safe: socket connect refused / timeout → exit 2 (block)
[ ] S3.6 Integration test: spawn wick-gate subprocess dgn fake socket server
         → cmd/wick-gate/main_test.go (extend existing)
```

**Exit criteria**: gate binary cocok dgn socket flow + auto_approved short-circuit; existing whitelist tests masih hijau.

### Stage 4 — Embed + Binary Resolution

Tujuan: production binary ship `wick-gate` di dalamnya, dev pakai `WICK_GATE_BIN` env.

```
[ ] S4.1 //go:embed assets/wick-gate-* di package daemon
         → internal/agents/gate/embed.go
[ ] S4.2 extractEmbeddedGate(sessionDir) — extract ke session dir, chmod 0755, idempotent
[ ] S4.3 resolveGateBin(sessionDir): cek WICK_GATE_BIN env dulu → fallback ke extract
[ ] S4.4 Wire resolveGateBin ke factory.go (ganti hard-coded path)
[ ] S4.5 Build CI step: "Build wick-gate" sebelum main build, output ke assets/
         → template/.github/workflows/release.yml
```

**Exit criteria**: raw binary dari `wick build` berhasil spawn agent + extract gate ke session dir tanpa env var apapun.

### Stage 5 — Web UI: Approval Modal + 4 Modes

Tujuan: user lihat modal saat command butuh approval, klik salah satu dari 4 decision.

```
[ ] S5.1 SSE event type "approval_request" + "approval_resolved" via existing Broadcaster
         → internal/tools/agents/stream.go
[ ] S5.2 Backend endpoint POST /api/agents/sessions/{id}/approve
         body: {"id":"...","decision":"approve_once|approve_session|approve_always|block"}
         → resolve pending channel di daemon
         → internal/tools/agents/handler.go
[ ] S5.3 approve_session: store di in-memory sessionApprovals map[sessionID][]matchKey
         next request match → daemon auto-resolve tanpa SSE broadcast
[ ] S5.4 approve_always: append matchKey ke spec.AutoApproved + rewrite spec.json
         → gate binary handle short-circuit dari Stage 3.2
[ ] S5.5 Web UI modal: render dari SSE, countdown timer 25s, 4 tombol decision
         → internal/tools/agents/view/approval.templ + js/agents.js
[ ] S5.6 matchKey hash: simple hash(tool + cmd) untuk MVP, exact match
         → internal/agents/gate/matchkey.go
[ ] S5.7 "Approved commands" panel di session detail: list session+always entries,
         tombol Revoke per item → DELETE /api/agents/sessions/{id}/approve/{matchKey}
[ ] S5.8 Smoke test manual: claude jalanin command non-whitelisted → modal muncul →
         klik "Allow this session" → command kedua yang sama auto-approve
```

**Exit criteria**: user bisa Approve once / session / always / Block dari web; revoke jalan; auto_approved persist setelah daemon restart.

### Stage 6 — AskUser MCP Tool + Web Card

Tujuan: agent bisa tanya user via MCP tool, web UI render card jawaban.

```
[ ] S6.1 MCP tool "ask_user" register di wick MCP server
         input schema: {question: string, options?: [{label, value}], allow_freeform?: bool}
         → internal/tools/agents/mcp_askuser.go
[ ] S6.2 Tool handler: register pending question (UUID + chan), broadcast SSE,
         block sampai POST /answer atau timeout 5 menit
[ ] S6.3 SSE event type "ask_user" + "ask_user_resolved"
[ ] S6.4 Backend endpoint POST /api/agents/sessions/{id}/answer
         body: {"id":"...","answer":"<value>"} atau {"answer_text":"..."}
[ ] S6.5 Web UI card: render inline di composer area, klik option → POST answer
         → internal/tools/agents/view/askuser.templ + js/agents.js
[ ] S6.6 Smoke test: agent panggil ask_user → card muncul di web → user pilih →
         agent terima jawaban di tool result
```

**Exit criteria**: claude bisa pakai `ask_user` MCP tool, jawaban user dari web masuk balik ke turn yang sama.

### Stage 7 — Dev Tooling

Tujuan: developer flow F5 di VSCode jalan tanpa langkah manual.

```
[x] S7.1 .vscode/tasks.json: extend "debug: prep" — tambah go build wick-gate
[-] S7.2 task "gate: sync-spec" — DROPPED (operator set $env:WICK_GATE_SPEC manual)
[x] S7.3 .vscode/launch.json: launch "wicklab-gate" (no envFile — lebih simpel)
[x] S7.4 .env.example: WICK_GATE_BIN entry sudah ada
[ ] S7.5 .vscode/launch.json: compound "wicklab + gate" (opsional)
[ ] S7.6 Doc snippet: developer flow F5 → wicklab → buat session →
         set $env:WICK_GATE_SPEC → F5 wicklab-gate → paste payload → breakpoint
```

**Exit criteria**: F5 wicklab + wicklab-gate jalan, dengan satu langkah manual yang explicit (set `$env:WICK_GATE_SPEC` di terminal sebelum F5 wicklab-gate).

---

## 13. Nama Teknik & Referensi

Daftar istilah teknis yang dipakai dalam arsitektur ini beserta link dokumentasi primer.

### Naming

| Istilah | Arti dalam konteks wick |
|---|---|
| **Pre-execution Hook** | Hook yang fire sebelum tool dieksekusi — `PreToolUse` di Claude |
| **PEP** (Policy Enforcement Point) | Yang enforce keputusan → claude CLI |
| **PDP** (Policy Decision Point) | Yang bikin keputusan → wick-gate |
| **Stateless ephemeral binary** | Binary tanpa state internal, semua via env/stdin/file, hidup detik-an |
| **HITL** (Human-in-the-loop) | Approval yang butuh keputusan manusia sebelum proses lanjut |
| **Allow-list / deny-by-default** | Hanya yang explicit di whitelist boleh; semua lainnya block |
| **Sidecar** | Proses pendamping kecil yang jalan parallel dengan proses utama |
| **bypassPermissions mode** | Claude mode yang matikan interactive TTY approval — hook jadi authority |

### Hook per CLI

| CLI | Nama Hook | Cara Block | Docs |
|---|---|---|---|
| Claude CLI | `PreToolUse` | exit code `2` | https://code.claude.com/docs/en/hooks-guide |
| Codex CLI | `PermissionRequest` | stdout JSON `{"behavior":"deny"}` | https://developers.openai.com/codex/hooks |
| Gemini CLI | `BeforeTool` | stdout JSON deny | https://geminicli.com/docs/hooks/ |

### Kenapa Pre-exec (bukan post-exec audit)?

- **Pre-exec**: command belum jalan saat hook fire — block = command tidak pernah jalan
- **Post-exec audit**: command sudah jalan, hook cuma rekam — blast radius sudah terjadi

### Kenapa Whitelist (bukan Blacklist)?

Blacklist mudah di-bypass: alias, path absolut (`/usr/bin/rm` vs `rm`), encoding (`r\m`), built-in vs binary. Whitelist + shell-metachar guard = surface area kecil, default deny.

### Bacaan Lanjutan

1. **Claude hooks-guide** — https://code.claude.com/docs/en/hooks-guide
2. **OPA sidecar PDP pattern** — https://www.openpolicyagent.org/docs/latest/
3. **OWASP Command Injection** — https://owasp.org/www-community/attacks/Command_Injection
4. **Slack interactivity** (untuk future Slack approval) — https://api.slack.com/interactivity/handling
5. **12-Factor Processes** — https://12factor.net/processes
6. **XACML PEP/PDP** — https://docs.oasis-open.org/xacml/3.0/xacml-3.0-core-spec-os-en.html
