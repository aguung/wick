# Provider baru: OMP (oh-my-pi) dan opencode

Status: in-progress · branch `ai/feat/omp-opencode-providers` (dari master `f8388ea2`)
Pemilik keputusan: Yoga. Implementasi dikerjakan sub-agent per irisan, main agent review + gate + deploy.

## Tujuan

1. Wick bisa menjalankan sesi agent lewat dua CLI baru, `omp` dan `opencode`, setara
   claude/codex: jalan headless, event turn terbaca di chat, resume sesi, MCP wick
   tersambung, skill + context file terbaca.
2. Tiap **instance = satu akun**. User bisa menambah banyak instance per type
   (mis. 3 opencode, 2 omp-codex) dari halaman Providers dengan alur yang sama
   halusnya seperti menambah instance codex hari ini: add → login lewat TTY
   browser → status akun → (usage kalau tersedia) → pakai.
3. Tidak ada load-balancing antar akun di dalam CLI. Satu instance hanya kenal
   satu akun. (Failover antar instance = fase berikutnya, di luar irisan ini.)

## Fakta yang sudah diverifikasi (source 29 Sep 2026)

Clone ada di `<session>/research/oh-my-pi` (commit fc671eb) dan `<session>/research/opencode` (7945de2).

OMP (`omp`, TypeScript di Bun + native Rust, dirilis sebagai satu binary):
- Headless: `omp -p "<prompt>"`, `--mode json` = stream event JSONL
  (`packages/coding-agent/src/modes/print-mode.ts`, `printableEvent`). Event:
  `agent_start/end`, `turn_start/end`, `message_start/update(delta)/end`,
  `tool_execution_start/update/end`, `auto_compaction_*`, `auto_retry_*`,
  `todo_*`. `agent_end.isTerminal === false` = masih akan lanjut.
- Resume: `--continue`, `--resume <id|path>`; `--session-dir <dir>`; `--cwd`;
  `--append-system-prompt <text|file>`; `--yolo`/`--approval-mode yolo`;
  `--no-title`; `--max-time`; `--model <provider/id>`; `--thinking <level>`.
- Isolasi akun: `--profile <name>` (atau env `OMP_PROFILE`) mengisolasi auth,
  sesi, settings, cache di `~/.omp/profiles/<name>/agent`. `PI_CODING_AGENT_DIR`
  hanya berlaku untuk profile default.
- Login non-TUI: `omp login [<provider>]` (cetak URL, baca prompt dari stdin,
  simpan ke `agent.db` profile aktif). Callback OAuth lokal: anthropic `54545`,
  openai-codex `1455`. Satu profile yang di-login berkali-kali = multi akun
  dengan rotasi otomatis — **wick tidak boleh melakukan itu**; satu instance =
  satu profile = satu login.
- Usage: `omp usage` (5 jam / mingguan per akun). Cek flag JSON-nya di `cli/usage-cli.ts`.
- Model: `omp models` (`cli/models-cli.ts`).
- Skill/context: membaca `.claude/skills`, `.agents/skills`, `CLAUDE.md`, `AGENTS.md`,
  `~/.codex/AGENTS.md` (`docs/skills.md`, `docs/context-files.md`). Skill user-level
  provider lain harus opt-in `enabledProviders`.
- MCP: `docs/mcp-config.md`.
- Hook: ekstensi TS di `.omp/hooks/pre|post` — tidak kompatibel dengan gate
  wick hari ini → gate/hook capability OFF untuk MVP (seperti gemini).
- Memory bawaan OFF secara default — biarkan OFF; memory tetap urusan wick.
- Mode `--mode rpc` (proses hidup, `steer`/`follow_up`) = fase 2, bukan MVP.

opencode (`opencode`, TypeScript di Bun, satu binary):
- Headless: `opencode run [message..]` + `--format json` (JSONL
  `{type,timestamp,sessionID,...}`): `step_start`, `step_finish`, `text`
  (dikirim setelah part teks selesai, tanpa delta), `reasoning`, `tool_use`
  (dikirim saat tool completed/error, tidak ada event mulai), `error`.
  Lihat `packages/opencode/src/cli/cmd/run.ts` fungsi `emit`.
- Resume: `-c`, `-s <sessionID>`, `--fork`; `-m provider/model`; `--agent`;
  `-f` file; `--title`.
- Auth: `opencode auth login [-p provider] [-m method]`, `auth list`, `auth logout`;
  disimpan di `~/.local/share/opencode/auth.json` (XDG data dir). Satu kredensial
  per provider. Isolasi akun per instance = data dir per instance — cari env
  resmi di source (`packages/opencode/src/global` / `xdg`) sebelum memakai
  `XDG_DATA_HOME` mentah.
- Claude Pro/Max **dilarang** di opencode (docs providers); ChatGPT Plus/Pro resmi.
- Skill: `.claude/skills`, `~/.claude/skills`, `.agents/skills`, `.opencode/skills`.
  Rules: `AGENTS.md`, fallback `CLAUDE.md`.
- Permission: `docs/permissions.mdx` — headless wick butuh semua tool `allow`.
- MCP: `docs/mcp-servers.mdx` (config `mcp` di opencode.json; `OPENCODE_CONFIG`).
- `opencode serve` (REST + SSE, banyak sesi satu proses) = fase 2, bukan MVP.

Host: `bun` tidak terpasang, tidak dibutuhkan (binary sudah membawa Bun).
Binary claude/codex dipanggil lewat shim cgroup di `~/.local/share/wick/bin/*`
(`MemoryMax=1200M`, `agents.slice`) — omp/opencode wajib ikut pola yang sama
(`cmd/cli/memory_wrapper.go`, `internal/agents/provider/memscope`).

## Desain

### Type & instance
- `internal/agents/provider/provider.go`: `TypeOMP Type = "omp"`,
  `TypeOpencode Type = "opencode"`; tambahkan ke `SupportedTypes()` (urutan UI
  setelah gemini, sebelum wick), `isSupported`, default seed bootstrap
  (**jangan** auto-seed instance kalau binary tidak ada — ikuti perilaku gemini).
- Semua titik yang menyebut `TypeGemini`/`"gemini"` di luar paket gemini harus
  ditinjau untuk dua type baru (daftar hasil grep ada di bawah).

### Isolasi akun per instance (inti permintaan user)
- OMP: instance menyimpan nama profile (default = `wick-<instanceName>`), wick
  selalu menambahkan `--profile <p>` ke argv spawn DAN login DAN usage.
  Catalog field `OMP_PROFILE` (bisa di-override).
- opencode: instance menyimpan data dir (default
  `<wick data>/providers/opencode/<instanceName>`), dibuat saat instance disimpan,
  diinjeksi lewat env yang tepat ke spawn DAN login DAN `auth list`.
- Rename instance tidak boleh memutus akun: nama profile/dir disimpan eksplisit
  di config instance saat dibuat, bukan diturunkan ulang dari nama.
- Delete instance: tanya/beri opsi hapus folder kredensial (ikuti pola yang ada
  untuk codex kalau ada; kalau tidak ada, jangan hapus otomatis).

### Spawn (MVP = satu proses per turn, sama seperti codex exec)
- Paket baru `internal/agents/provider/omp/` dan `internal/agents/provider/opencode/`
  dengan struktur cermin `gemini/` + `codex/`: `capability_init.go`, `catalog.go`,
  `spawn.go`, `mcp_config.go`, `skilldir.go`, `hide_console_*.go`, `doc.go`, tests.
- OMP argv: `--profile <p> -p --mode json --cwd <workspace> --no-title --yolo
  [--resume <sid>] [--model ...] [--append-system-prompt <file>] <prompt via stdin>`.
- opencode argv: `run --format json [-s <sid>] [-m ...] [--agent ...] <prompt>`;
  permission allow-all + MCP wick lewat config per-spawn.
- Parser event → model event wick yang sama dengan yang dipakai codex/claude
  (teks, reasoning, tool start/end, usage token, error, selesai). Untuk opencode,
  tool yang cuma punya event selesai dirender sebagai start+end sekaligus.
- Tangkap session id dari stream untuk resume (OMP: cari di event awal /
  header sesi; opencode: field `sessionID`).
- Prompt sistem wick (aturan + blok memory) diteruskan: OMP lewat
  `--append-system-prompt <file>`; opencode lewat mekanisme yang tersedia
  (agent/instructions config) — pilih yang tidak menimpa AGENTS.md project.
- Exit code / error limit akun dikenali dan dilaporkan jelas (pesan "akun
  instance X kena limit") — dasar untuk failover fase 2.
- `SendMode` default: `queue` (seperti codex) — pesan susulan menunggu turn selesai.

### Login, akun, usage, model (halaman Providers)
- `internal/agents/provider/logintty/omp.go`: login argv
  `--profile <p> login <provider>`; provider dipilih user di UI (minimal
  `openai-codex`, `anthropic`; tampilkan peringatan policy untuk anthropic).
  Parse URL OAuth dari output (pola yang sama dengan codex). Account probe:
  baca identitas akun dari profile (lewat CLI kalau ada output JSON, bukan
  membaca sqlite langsung kecuali terpaksa).
- `logintty/opencode.go`: `auth login -p <provider>`; probe via `auth list`.
  Tampilkan catatan bahwa Claude subscription tidak didukung opencode.
- Usage: OMP → `omp --profile <p> usage` di-parse ke kartu usage yang sama
  dengan codex (cache + pace gate yang sudah ada di `usage_probe.go`).
  opencode → tidak ada; kartu menampilkan "tidak tersedia".
- Model picker: seed awal + refresh dari `omp models` / `opencode models`.

### UI/UX (fe/agents/providers)
- Dua type baru muncul di pilihan "Add provider" dengan ikon + deskripsi singkat.
- Alur add instance: nama otomatis disarankan (`omp`, `omp-2`, …), profile/dir
  otomatis terisi dan tampil read-only-with-override, lalu langsung tawarkan
  tombol **Login** (TTY browser yang sudah ada) — tanpa langkah config manual.
- Kartu instance: akun terhubung (email/plan), status binary (ketemu/versi),
  usage (OMP), badge "1 instance = 1 akun".
- Banyak instance satu type ditampilkan berkelompok dan mudah dibedakan
  (nama + akun).
- Ikuti skill repo `.claude/skills/fe-module` dan `design-system`. FE wajib
  `npx vite build` di `fe/agents/providers` sebelum `wick build`.

### Resource guard
- Shim cgroup untuk `omp` dan `opencode` sama seperti claude/codex
  (`MemoryMax` dari instance/global). Tambahkan ke daftar wrapper
  (`cmd/cli/memory_wrapper.go`, `memscope/wrapper`).

## Titik sentuh (hasil grep "gemini" di luar paket gemini)

`internal/agents/pool/factory.go`, `internal/agents/pool/pool.go`,
`internal/agents/skillsync/sync.go`, `internal/agents/workflow/setup/providers.go`,
`internal/agents/workflow/nodes/agent.go`, `internal/agents/provider/memscope/wrapper/wrapper.go`,
`internal/mcpconfig/install.go`, `internal/pkg/api/server.go`, `cmd/cli/memory.go`,
`cmd/gate/main.go`, `internal/entity/agent_profile.go`, `internal/tools/agents/providers.go`,
`internal/tools/agents/view/models.go`, `internal/tools/agents/memory_handler.go`,
`internal/tools/provider-storage/handler.go`, `fe/common/ui/src/Composer.svelte`,
`fe/agents/providers/src/lib/components/*`, `docs/guide/agents/pool.md`.

## Irisan kerja

1. **Backend spawn** (sub-agent #1): type, catalog, isolasi profile/dir, spawn +
   parser event + resume + MCP + skilldir + wiring pool/factory + shim cgroup, dengan
   unit test parser dari fixture yang disusun dari source (tanpa akun asli).
2. **Akun & UI** (sub-agent #2): logintty omp/opencode, account probe, usage OMP,
   model list, halaman Providers (add/login/kartu/multi instance), docs pool.md.
3. **Main agent**: review diff, gate unit test (skill `wick-support-tools-unit-test`
   mode CHANGED), install binary omp/opencode di `<session>/tooling/agents-bin`
   (bukan global), build + deploy ke host (skill `redeploy-and-update-wick` mode C),
   lapor ke Yoga. Login akun asli dilakukan Yoga dari UI. PR satu kali setelah Yoga OK.

## Di luar irisan ini (fase 2)
- OMP `--mode rpc` / opencode `serve` proses hidup (steer, hemat spawn).
- Failover otomatis antar instance saat akun kena limit.
- Gate/hook command untuk omp/opencode.

## Aturan kerja untuk sub-agent
- Jangan push, jangan deploy, jangan reload, jangan ubah repo lain.
- Semua file kerja di folder sesi; jangan `/tmp`.
- Test: `export GOWORK=off GOFLAGS=`; jalankan dengan `env -u DATABASE_URL`.
- Jangan tebak flag CLI: setiap flag/field yang dipakai harus dicek di clone source di
  `<session>/research/...` dan dicantumkan file:line-nya di laporan.
- Laporan akhir: daftar file:line yang diubah, apa yang belum diverifikasi live, test
  yang dijalankan + hasil.
