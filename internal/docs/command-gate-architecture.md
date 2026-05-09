# Command Gate — Arsitektur & Approval System

Status: draft.
Update terakhir: 2026-05-09.

---

## 0. TL;DR

**Command Gate** = mekanisme intercept shell command sebelum Claude mengeksekusinya. User bisa approve atau block command secara real-time tanpa restart session.

Dokumen ini menjelaskan:
- Kenapa gate diperlukan dan bagaimana cara kerjanya
- Perbandingan dua pola approval (Claude Code style vs Gate style)
- Perbandingan empat opsi IPC antara gate dan daemon
- Detail Unix Domain Socket — cara kerja, keamanan, isi file
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

---

## 2. Dua Pola Approval

Ada dua cara fundamental untuk mendapat konfirmasi user sebelum command jalan.

### 2.1 Pola A — Gate Style (System Intercept)

System yang memaksa konfirmasi, bukan Claude.

```
Step 1:  User kirim pesan ke Claude
Step 2:  Claude putuskan untuk jalankan command
         → hook fire → gate dipanggil → gate block
         → UI muncul di user: "Approve rm -rf /data?"
         → user klik Approve
         → gate exit 0
         → command jalan (masih dalam turn yang sama)
Step 3:  Claude selesai, balas ke user
```

**Flow detail:**

```
User ──────────────────────────────────────────────────────────────────
  │
  │  [1] kirim pesan
  ▼
Claude (PID 1234) ──────────────────────────────────────────────────────
  │
  │  [2] putuskan: jalankan "rm -rf /data"
  │
  ├── fork → wick-gate ──────────────────────────────────────────────
  │              │                                                     │
  │              │  [3] kirim request ke daemon                        │
  │              │      {"cmd": "rm -rf /data", "id": "abc"}          │
  │  NUNGGU      │                                                     │
  │  exit code   ├── (block di sini sampai daemon balas) ─────────────
  │              │
  │              │         Daemon ───────────────────────────────────
  │              │           │                                        │
  │              │           │  [4] simpan pending                    │
  │              │           │  [5] broadcast SSE ke web UI           │
  │              │           │                                        │
  │              │         User (Web UI) ────────────────────────────
  │              │           │                                        │
  │              │           │  [6] lihat modal approval              │
  │              │           │  [7] klik "Approve"                    │
  │              │           │                                        │
  │              │         Daemon ───────────────────────────────────
  │              │           │                                        │
  │              │           │  [8] resolve pending                   │
  │              │           │  [9] balas ke gate                     │
  │              │           │      {"decision": "approve"}          │
  │              │                                                    │
  │              │  [10] terima response                              │
  │              └── exit 0 ─────────────────────────────────────────
  │
  │  [11] command jalan: rm -rf /data
  │  [12] balas ke user: "done"
  ▼
User menerima hasil
```

### 2.2 Pola B — Claude Code Style (Voluntary Ask)

Claude sendiri yang memutuskan untuk tanya sebelum bertindak.

```
Step 1:  User kirim pesan
Step 2:  Claude output: "Mau beneran hapus? (y/n)"
         → turn selesai, Claude nunggu
Step 3:  User ketik "y" → masuk sebagai pesan baru
Step 4:  Claude terima jawaban, jalankan command
```

**Flow detail:**

```
Turn 1:
  User: "hapus log lama"
  Claude: "Ini akan hapus /var/log/app.log. Lanjut?" ← turn selesai

Turn 2:
  User: "iya"
  Claude: [jalankan: rm /var/log/app.log]
  Claude: "Berhasil dihapus"
```

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
| **Cocok untuk** | Security-critical, audit wajib | UX conversational, low-risk |
| **Kompleksitas implementasi** | Lebih kompleks | Sederhana |

### 2.4 Kapan Pakai Yang Mana?

```
Butuh JAMINAN bahwa setiap command pasti di-approve?
├── Ya → Pola A (Gate Style)
└── Tidak
    ├── Cukup Claude tanya sendiri untuk action besar? → Pola B (Claude Code Style)
    └── Gabungan? → Pola A untuk destructive commands, Pola B untuk read-only
```

**Rekomendasi**: untuk mid-session approval system, pakai **Pola A**. Pola B tidak bisa menjamin intercept.

---

## 3. Opsi IPC: Gate ↔ Daemon

Gate adalah subprocess terpisah dari daemon. Mereka perlu berkomunikasi. Ada empat opsi.

### 3.1 HTTP (TCP)

Gate kirim HTTP POST ke daemon yang listen di port tertentu.

```go
// gate
resp, _ := http.Post("http://localhost:9425/api/agents/approve",
    "application/json", payload)

// daemon
http.HandleFunc("/api/agents/approve", func(w http.ResponseWriter, r *http.Request) {
    // tunggu user → tulis response
})
```

**Diagram:**

```
gate ──── TCP :9425 ────► daemon
     ◄─────────────────── response
```

| | |
|---|---|
| **Kelebihan** | Familiar, tooling lengkap (curl, Postman), mudah debug |
| **Kekurangan** | Port bisa di-akses dari network, perlu auth, overhead protokol HTTP |
| **Performa** | ~1-5ms overhead TCP handshake + HTTP parsing |
| **Keamanan** | Perlu bind ke 127.0.0.1 + auth token, tetap ada risiko port scanning |
| **Cocok untuk** | Multi-machine, publik API |

### 3.2 Unix Domain Socket ✅ REKOMENDASI

Gate connect ke file socket di filesystem, kirim/terima raw JSON.

```go
// gate
conn, _ := net.Dial("unix", "~/.wick/sessions/<id>/gate.sock")
json.NewEncoder(conn).Encode(request)
json.NewDecoder(conn).Decode(&response)

// daemon
ln, _ := net.Listen("unix", "~/.wick/sessions/<id>/gate.sock")
conn, _ := ln.Accept()
json.NewDecoder(conn).Decode(&req)
// ... tunggu user ...
json.NewEncoder(conn).Encode(response)
```

**Diagram:**

```
gate ──── gate.sock ────► daemon
     ◄─────────────────── response
     (lewat kernel buffer, tidak ke disk)
```

| | |
|---|---|
| **Kelebihan** | Tidak ada network exposure, akses dikontrol file permission, zero port, cepat |
| **Kekurangan** | Hanya lokal, satu machine |
| **Performa** | ~0.1ms, tanpa TCP overhead, tanpa HTTP parsing |
| **Keamanan** | Kontrol via chmod + chown, tidak bisa diakses dari network |
| **Cocok untuk** | Local IPC — persis use case ini |

### 3.3 Named Pipe / FIFO

Dua file FIFO: satu untuk request, satu untuk response.

```bash
mkfifo gate-req.fifo gate-res.fifo

# gate: tulis ke req, baca dari res
# daemon: baca dari req, tulis ke res
```

**Diagram:**

```
gate ──── gate-req.fifo ────► daemon
     ◄─── gate-res.fifo ───── daemon
```

| | |
|---|---|
| **Kelebihan** | Zero dependency, paling primitif, ada di semua Unix |
| **Kekurangan** | Perlu dua file per session, satu koneksi sekaligus, tidak bisa concurrent |
| **Performa** | ~0.1ms, sama seperti Unix socket |
| **Keamanan** | File permission sama, tapi tidak bisa concurrent requests |
| **Cocok untuk** | Skenario sangat sederhana, satu gate satu daemon |

### 3.4 File + inotify / Polling

Gate tulis file "pending", daemon watch direktori, daemon tulis file "decision".

```
gate tulis → ~/.wick/sessions/<id>/gate/pending/abc123.json
daemon watch dir → baca file → proses → hapus
daemon tulis → ~/.wick/sessions/<id>/gate/decision/abc123.json
gate poll / watch → baca decision
```

| | |
|---|---|
| **Kelebihan** | Audit trail otomatis (file tersisa), debuggable, survives restart |
| **Kekurangan** | Polling = latency, inotify = extra dependency, bersih-bersih file |
| **Performa** | 10-100ms kalau polling, ~1ms kalau inotify |
| **Keamanan** | File permission, tapi file di disk = risiko leak credential di command |
| **Cocok untuk** | Audit log use case, bukan real-time approval |

### 3.5 Perbandingan Empat Opsi

| Dimensi | HTTP | Unix Socket | Named Pipe | File+inotify |
|---|---|---|---|---|
| **Network exposure** | Ya (loopback) | Tidak | Tidak | Tidak |
| **Concurrent requests** | Ya | Ya | Tidak | Ya (per file) |
| **Overhead** | Tinggi | Rendah | Rendah | Tinggi (polling) |
| **Debug** | Mudah (curl) | Sedang | Sulit | Mudah (cat file) |
| **Auth diperlukan** | Ya | Tidak (chmod cukup) | Tidak | Tidak |
| **Bidirectional** | Ya (req/resp) | Ya | Perlu 2 pipe | Perlu 2 dir |
| **Implementasi** | Mudah | Mudah | Sedang | Sedang |
| **Go stdlib** | `net/http` | `net.Listen("unix")` | `os.OpenFile` | `os` + polling |

---

## 4. Deep Dive: Unix Domain Socket

### 4.1 Apa itu Socket File?

Socket file (`gate.sock`) **bukan file biasa**. Tidak ada data di dalamnya.

```bash
$ ls -la gate.sock
srwxr-xr-x 1 user user 0 May 9 10:00 gate.sock
# ^
# "s" = socket type, bukan regular file "-"
# Ukuran selalu 0 bytes

$ cat gate.sock
cat: gate.sock: No such device or address  ← tidak bisa dibaca seperti file
```

Socket file adalah **alamat titik temu** — seperti nomor telepon. Data yang sebenarnya mengalir di kernel memory buffer, tidak pernah menyentuh disk.

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

**Analogi**: colokan listrik di dinding. Tidak ada "isi" di colokannya, tapi kalau kamu colok sesuatu, listrik mengalir.

### 4.2 Protokol: Raw JSON Newline-Delimited

Tidak ada protokol HTTP. Langsung kirim JSON diakhiri newline:

```
gate → daemon:   {"id":"abc","cmd":"rm -rf /data","agent":"backend"}\n
daemon → gate:   {"decision":"block","reason":"destructive command"}\n
```

Di Go:

```go
// Kirim: satu encode = satu baris JSON + newline otomatis
json.NewEncoder(conn).Encode(req)

// Terima: baca sampai newline, parse JSON
json.NewDecoder(conn).Decode(&resp)
```

`json.NewDecoder` blocking sampai ada data masuk — inilah yang bikin gate "nunggu" tanpa aktif polling.

### 4.3 Keamanan Unix Socket

**Kenapa lebih aman dari HTTP:**

| Ancaman | HTTP :9425 | Unix Socket |
|---|---|---|
| Akses dari network | Ya, kalau firewall bocor | Tidak mungkin — by design |
| Port scanning | Terdeteksi | Tidak ada port |
| Proses lain di machine sama | Bisa connect ke port | Hanya kalau tahu path + punya permission |
| Brute force auth | Bisa | Tidak relevan |

**Yang perlu dijaga:**

```
❌ /tmp/wick.sock
   Masalah: /tmp world-writable, proses lain bisa connect

✅ ~/.wick/sessions/<id>/gate.sock
   Aman: direktori ini chmod 700, hanya owner yang akses
```

**Set permission socket:**

```go
ln, _ := net.Listen("unix", socketPath)
os.Chmod(socketPath, 0600)  // hanya owner bisa connect
```

**Opsional: verify peer credentials (SO_PEERCRED)**

```go
// Pastikan yang connect adalah wick-gate dengan UID yang benar
uc := conn.(*net.UnixConn)
raw, _ := uc.SyscallConn()
raw.Control(func(fd uintptr) {
    cred, _ := syscall.GetsockoptUcred(int(fd),
        syscall.SOL_SOCKET, syscall.SO_PEERCRED)
    if cred.Uid != uint32(os.Getuid()) {
        conn.Close()  // reject
    }
})
```

Untuk use case Wick, taruh socket di session directory sudah cukup tanpa SO_PEERCRED.

### 4.4 Lifecycle Socket File

```
Daemon start:
  1. os.Remove(socketPath)     ← hapus sisa dari run sebelumnya
  2. net.Listen("unix", path)  ← buat socket baru
  3. os.Chmod(path, 0600)      ← lock permission

Daemon running:
  ← menerima koneksi masuk
  ← handle concurrent requests (goroutine per connection)

Daemon stop:
  4. ln.Close()                ← stop accept
  5. os.Remove(socketPath)     ← bersihkan (optional, step 1 sudah handle)

Kalau daemon mati mendadak (crash):
  Gate: connect() → "connection refused"
  Gate: fail-safe → exit 2 (block)  ← aman by default
```

---

## 5. Flow Lengkap: Mid-Session Approval

Ini adalah flow end-to-end untuk Pola A (Gate Style) dengan Unix Socket.

### 5.1 Happy Path: User Approve

```
Claude (PID 1234)          wick-gate           daemon          User (Web)
      │                        │                  │                │
      │ [1] mau jalankan       │                  │                │
      │     "git clone ABC"    │                  │                │
      │                        │                  │                │
      ├──fork──────────────────►                  │                │
      │   (nunggu exit code)   │                  │                │
      │                        │ [2] connect      │                │
      │                        ├─────────────────►│                │
      │                        │                  │                │
      │                        │ [3] send JSON    │                │
      │                        │ {"id":"x",       │                │
      │                        │  "cmd":"git..."}│                │
      │                        ├─────────────────►│                │
      │                        │                  │                │
      │                        │   (blocking      │ [4] simpan     │
      │                        │    di sini)      │  pending["x"]  │
      │                        │                  │                │
      │                        │                  │ [5] SSE event  │
      │                        │                  ├───────────────►│
      │                        │                  │                │
      │                        │                  │                │ [6] render modal
      │                        │                  │                │ "Approve git clone ABC?"
      │                        │                  │                │ [Approve] [Block]
      │                        │                  │                │
      │                        │                  │ [7] POST       │
      │                        │                  │ /approve {"id":"x",
      │                        │                  │ "decision":"approve"}
      │                        │                  │◄───────────────┤
      │                        │                  │                │
      │                        │ [8] {"decision": │                │
      │                        │    "approve"}    │                │
      │                        │◄─────────────────┤                │
      │                        │                  │                │
      │                        │ [9] close conn   │                │
      │                        ├─────────────────►│                │
      │                        │                  │                │
      │◄───────────────────────┤                  │                │
      │   exit 0               │                  │                │
      │                        │                  │                │
      │ [10] git clone ABC     │                  │                │
      │      (jalan!)          │                  │                │
```

### 5.2 Sad Path: User Block

```
      ...sama sampai step [7], tapi user klik Block...

      │                        │ {"decision":     │                │
      │                        │    "block"}      │                │
      │                        │◄─────────────────┤                │
      │                        │                  │                │
      │◄───────────────────────┤                  │                │
      │   exit 2               │                  │                │
      │                        │                  │                │
      │ [tool blocked]         │                  │                │
      │ Claude: "Command       │                  │                │
      │  blocked by user"      │                  │                │
```

### 5.3 Sad Path: Timeout (User Tidak Respond)

```
      ...gate connect, kirim request...
      ...daemon set deadline 25 detik (< hook timeout 30 detik)...

      Setelah 25 detik:
      daemon: deadline exceeded → tulis {"decision":"block"}
      gate: terima response → exit 2

      Claude: "Command blocked (timeout)"
```

### 5.4 Sad Path: Daemon Tidak Jalan

```
      gate: connect() → "no such file or address" atau "connection refused"
      gate: fail-safe → exit 2 (block semua)
```

---

## 6. Struktur Data

### 6.1 Request (Gate → Daemon)

```go
type ApprovalRequest struct {
    ID        string `json:"id"`         // unique per request, UUID
    SessionID string `json:"session_id"` // untuk daemon routing
    Agent     string `json:"agent"`      // nama agent (misal: "backend")
    Tool      string `json:"tool"`       // "Bash", "Edit", dll.
    Cmd       string `json:"cmd"`        // command yang mau dieksekusi
    WorkDir   string `json:"work_dir"`   // current working directory
    Timestamp int64  `json:"ts"`         // unix ms
}
```

### 6.2 Response (Daemon → Gate)

```go
type ApprovalResponse struct {
    ID       string `json:"id"`       // sama dengan request ID
    Decision string `json:"decision"` // "approve" atau "block"
    Reason   string `json:"reason"`   // opsional, alasan block
}
```

### 6.3 SSE Event (Daemon → Web UI)

```json
{
    "type": "approval_request",
    "id": "abc123",
    "session_id": "sess_xyz",
    "agent": "backend",
    "tool": "Bash",
    "cmd": "rm -rf /data",
    "work_dir": "/home/user/project",
    "ts": 1746787200000
}
```

### 6.4 HTTP Endpoint (Web UI → Daemon)

```
POST /api/agents/sessions/{id}/approve
Content-Type: application/json

{
    "id": "abc123",
    "decision": "approve"
}
```

---

## 7. State Machine di Daemon

```
[idle]
  │
  │ gate connect + send request
  ▼
[pending] ─── timeout (25s) ──────────────────► [resolved: block]
  │                                                    │
  │ user klik Approve                                  │
  ▼                                                    │
[resolved: approve]                                    │
  │                                                    │
  └────────────────────────────────────────────────────┘
                         │
                         ▼
               tulis response ke socket
               hapus dari pending map
                         │
                         ▼
                      [idle]
```

**Concurrent requests**: daemon bisa pegang banyak pending sekaligus (map[id]channel). Tiap goroutine handle satu koneksi gate, block di channel sampai user decide.

```go
type pendingApproval struct {
    req ApprovalRequest
    ch  chan ApprovalResponse
}

var pending = sync.Map{} // map[id]pendingApproval

// Gate handler goroutine:
ch := make(chan ApprovalResponse, 1)
pending.Store(req.ID, pendingApproval{req, ch})
defer pending.Delete(req.ID)

select {
case resp := <-ch:
    json.NewEncoder(conn).Encode(resp)
case <-time.After(25 * time.Second):
    json.NewEncoder(conn).Encode(ApprovalResponse{Decision: "block", Reason: "timeout"})
}
```

---

## 8. Lokasi File di Filesystem

```
~/.wick/agents/sessions/<session-id>/
  ├── meta.json                  ← session metadata
  ├── agents.json                ← agent list + CLI session ID
  ├── commands.jsonl             ← audit log semua command
  └── gate/
      ├── spec.json              ← rules whitelist untuk gate binary
      ├── settings.json          ← Claude hook config (PreToolUse)
      └── gate.sock              ← Unix domain socket (dibuat saat daemon start)
                                    chmod 0600, owner = user yang jalankan daemon
```

---

## 9. Keputusan Desain

| # | Keputusan | Alasan |
|---|---|---|
| D1 | Pakai Unix socket, bukan HTTP | Tidak ada network exposure, performa lebih baik, akses dikontrol filesystem |
| D2 | Socket path di session directory | Direktori sudah chmod 700, isolasi per session, tidak perlu auth tambahan |
| D3 | Raw JSON newline-delimited, bukan HTTP | Tidak ada overhead parsing HTTP header, protokol lebih simpel |
| D4 | Timeout 25 detik di daemon (< hook timeout 30 detik) | Pastikan gate sempat exit dengan bersih sebelum Claude timeout |
| D5 | Fail-safe: block kalau daemon tidak respond | Lebih aman default block daripada default allow |
| D6 | Pending state pakai `sync.Map` + channel | Concurrent safe, goroutine per koneksi, no mutex contention |
| D7 | Gate binary tetap stateless | Gate tidak simpan state, semua state di daemon. Gate bisa crash/respawn tanpa kehilangan pending |

---

## 10. Rekomendasi Akhir

**Untuk mid-session approval system di Wick: gunakan Pola A (Gate Style) dengan Unix Domain Socket.**

Ringkasan justifikasi:

```
Kebutuhan                    Solusi
─────────────────────────────────────────────────────────
Jaminan intercept 100%    → Gate (bukan Claude Code style)
Tidak ada port terbuka    → Unix socket (bukan HTTP)
Performa rendah latency   → Unix socket (bukan file+inotify)
Concurrent requests       → Unix socket (bukan named pipe)
Keamanan lokal            → chmod 0600 di session dir
Audit trail               → commands.jsonl tetap dipertahankan
```

**Yang perlu diimplementasikan:**

```
[ ] 1. Endpoint approve di daemon (POST /api/agents/sessions/{id}/approve)
[ ] 2. Unix socket listener di daemon (per session, dibuat saat session start)
[ ] 3. Pending state manager (sync.Map + goroutine per connection)
[ ] 4. SSE event type baru: "approval_request"
[ ] 5. Update wick-gate: tambah Unix socket path dari env, fallback ke rule-based
[ ] 6. Web UI: render modal approval saat terima SSE "approval_request"
[ ] 7. Wire Gate di factory.go (saat ini masih nil)
```
