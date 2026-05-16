## 15. Bootstrap & hot-reload

### Boot

`internal/jobs/workflow/registry.go` punya `RegisterAll(svc)`:
- Loop `svc.List()`, register tiap workflow ke `jobs.Register(job.Module{
  Meta.Key: "workflow:<slug>:<trigger-idx>", DefaultCron: ..., Run: ...
  })`.
- Idempotent on Key.

Dipanggil dari:
- [internal/pkg/worker/server.go](../pkg/worker/server.go) sebelum
  `configsSvc.Bootstrap`.
- [internal/pkg/api/server.go](../pkg/api/server.go).

### Reload setelah CRUD

CRUD (UI canvas / MCP / hand-edit + fsnotify) → handler panggil
`RegisterAll(svc)` lagi. Worker tick berikutnya pakai schedule baru.

### Delete

Hapus folder + `jobs.Unregister("workflow:<slug>:*")` (perlu tambah
method `UnregisterPrefix` di
[internal/jobs/registry.go](../jobs/registry.go) — sekarang cuma ada
`Register`).

### File watcher

fsnotify watcher di `<BaseDir>/workflows/` — kalau ada file berubah:
- Invalidate hash cache.
- Re-validate workflow (cycle check, schema).
- Push update event via SSE ke UI clients yang lagi buka detail page.

Recommended untuk mendukung gitops + manual edit workflow.

---

