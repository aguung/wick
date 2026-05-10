package provider

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Why a separate file: provider.go owns the read/write API for
// instances; this file owns the *cached probe result* for those
// instances. Keeping them apart lets cache rules (TTLs, persistence,
// rescan triggers) evolve without churning the registry CRUD path.
//
// Storage layout (when CacheStore is wired): one configs row per
// instance under owner="agents" with key
// `provider_status:<type>/<name>` and a JSON-encoded persistedStatus
// value. The DB cache survives restart so the Providers page renders
// instantly on first load instead of waiting on 3 cold `--version`
// spawns. Path scan + version probe refresh on user-triggered Rescan
// or once per VersionRefreshInterval when AutoRescan is enabled.

// VersionRefreshInterval is how stale a persisted version probe must
// be before a page render kicks off a background re-probe (when
// auto-rescan is on). Path scan is cheap and re-runs on every
// Rescan; version probe is the expensive bit and rarely changes
// outside CLI upgrades.
const VersionRefreshInterval = 24 * time.Hour

const (
	cacheOwner  = "agents"
	cacheKeyFmt = "provider_status:%s/%s"
)

type persistedStatus struct {
	Path       string    `json:"path"`
	PathFound  bool      `json:"path_found"`
	Version    string    `json:"version"`
	VersionErr string    `json:"version_err,omitempty"`
	ScannedAt  time.Time `json:"scanned_at"`
	VersionAt  time.Time `json:"version_at"`
}

func cacheKey(t Type, name string) string {
	return "provider_status:" + string(t) + "/" + name
}

// loadPersisted reads one cached entry, or zero value + ok=false.
func loadPersisted(t Type, name string) (persistedStatus, bool) {
	store := getCacheStore()
	if store == nil {
		return persistedStatus{}, false
	}
	raw := store.GetOwned(cacheOwner, cacheKey(t, name))
	if raw == "" {
		return persistedStatus{}, false
	}
	var ps persistedStatus
	if err := json.Unmarshal([]byte(raw), &ps); err != nil {
		log.Warn().Err(err).Str("type", string(t)).Str("name", name).Msg("agents.cache: persisted unmarshal failed, ignoring")
		return persistedStatus{}, false
	}
	return ps, true
}

// savePersisted writes one cached entry. Errors are logged but not
// returned — cache miss is recoverable, blocking the user on a DB
// write failure is not.
func savePersisted(ctx context.Context, t Type, name string, ps persistedStatus) {
	store := getCacheStore()
	if store == nil {
		return
	}
	b, err := json.Marshal(ps)
	if err != nil {
		log.Warn().Err(err).Msg("agents.cache: marshal failed")
		return
	}
	if err := store.SetOwned(ctx, cacheOwner, cacheKey(t, name), string(b)); err != nil {
		log.Warn().Err(err).Str("type", string(t)).Str("name", name).Msg("agents.cache: persist failed")
	}
}

// statusFromPersisted hydrates a Status from cache + the in-memory
// Instance (Disabled, Binary, etc. live in registry, not cache).
func statusFromPersisted(ins Instance, ps persistedStatus) Status {
	return Status{
		Instance:   ins,
		ResolvedAt: ps.ScannedAt,
		Path:       ps.Path,
		PathFound:  ps.PathFound,
		Version:    ps.Version,
		VersionErr: ps.VersionErr,
	}
}

// LoadCached returns Status per configured instance, served from the
// persistent cache when available. Misses fall through to a fresh
// Probe. Used by the Providers page so a cold reload doesn't spawn
// version probes.
//
// Background refresh: when AutoRescanEnabled() and a cached entry's
// version_at is older than VersionRefreshInterval, this spawns a
// detached re-probe so the *next* render sees fresh data without
// blocking *this* one.
func LoadCached(ctx context.Context) ([]Status, error) {
	all, err := Load()
	if err != nil {
		return nil, err
	}
	out := make([]Status, len(all))
	auto := AutoRescanEnabled()
	now := time.Now()
	for i := range all {
		ins := all[i]
		if ps, ok := loadPersisted(ins.Type, ins.Name); ok {
			out[i] = statusFromPersisted(ins, ps)
			if auto && now.Sub(ps.VersionAt) > VersionRefreshInterval {
				go func(ins Instance) {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					_ = RescanOne(ctx, ins.Type, ins.Name)
				}(ins)
			}
			continue
		}
		// Cache miss: fall through to live Probe so the user sees a
		// useful card on first boot. Persist for next time.
		st := Probe(ctx, ins)
		persistFromStatus(ctx, ins.Type, ins.Name, st)
		out[i] = st
	}
	return out, nil
}

// persistFromStatus writes a fresh Probe result to the cache.
func persistFromStatus(ctx context.Context, t Type, name string, st Status) {
	now := time.Now()
	savePersisted(ctx, t, name, persistedStatus{
		Path:       st.Path,
		PathFound:  st.PathFound,
		Version:    st.Version,
		VersionErr: st.VersionErr,
		ScannedAt:  now,
		VersionAt:  now,
	})
}

// RescanOne forces a fresh Probe + persist for one instance. Returns
// the new Status so callers (UI button, Save hook) can render it
// immediately.
func RescanOne(ctx context.Context, t Type, name string) Status {
	ins, err := Find(t, name)
	if err != nil {
		return Status{}
	}
	st := Probe(ctx, ins)
	persistFromStatus(ctx, t, name, st)
	log.Info().
		Str("type", string(t)).
		Str("name", name).
		Str("path", st.Path).
		Str("version", st.Version).
		Bool("found", st.PathFound).
		Msg("agents.rescan: one")
	return st
}

// RescanAll re-probes every configured instance in parallel and
// persists each result. Used by the boot prime + the "Rescan all"
// button.
func RescanAll(ctx context.Context) []Status {
	all, err := Load()
	if err != nil {
		return nil
	}
	out := make([]Status, len(all))
	var wg sync.WaitGroup
	for i := range all {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := Probe(ctx, all[i])
			persistFromStatus(ctx, all[i].Type, all[i].Name, st)
			out[i] = st
		}()
	}
	wg.Wait()
	log.Info().Int("count", len(out)).Msg("agents.rescan: all")
	return out
}

// ── Auto-rescan toggle ────────────────────────────────────────────────

const autoRescanKey = "auto_rescan"

// AutoRescanEnabled reads the auto-rescan toggle from configs. Default
// is true (auto-refresh stale version probes in the background).
func AutoRescanEnabled() bool {
	store := getCacheStore()
	if store == nil {
		return true
	}
	v := store.GetOwned(cacheOwner, autoRescanKey)
	return v != "false"
}

// SetAutoRescan persists the toggle.
func SetAutoRescan(ctx context.Context, on bool) error {
	store := getCacheStore()
	if store == nil {
		return nil
	}
	val := "true"
	if !on {
		val = "false"
	}
	return store.SetOwned(ctx, cacheOwner, autoRescanKey, val)
}
