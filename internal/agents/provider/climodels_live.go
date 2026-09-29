package provider

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yogasw/wick/internal/agents/provider/modelfilter"
)

// climodels_live.go backs the omp/opencode "live from CLI" model list
// (Instance.LiveModels): the picker offers what ListCLIModels returns,
// narrowed by LiveModelFilter, with LiveModelDefault (or the first match) as
// the default. The CLI is exec'd at most once per cliModelsTTL per instance
// store; render paths only PEEK at the cache and refresh it in the
// background, so a page render never waits on a CLI.

// cliModelsTTL is how long a fetched CLI model list stays fresh.
const cliModelsTTL = 10 * time.Minute

// cliModelsFetchTimeout bounds one background/blocking CLI list call.
const cliModelsFetchTimeout = 60 * time.Second

type cliModelsEntry struct {
	models []ModelSeed
	err    error
	at     time.Time
}

var (
	cliModelsMu       sync.Mutex
	cliModelsCache    = map[string]cliModelsEntry{}
	cliModelsInflight = map[string]chan struct{}{}
	cliModelsNow      = time.Now
)

// cliModelsKey identifies one account store: the list depends on who is
// logged in, so the profile / data dir is part of the key, not just the name.
func cliModelsKey(ins Instance) string {
	k := string(ins.Type) + "/" + ins.Name + "|" + ins.Binary
	if ins.OMPConfig != nil {
		k += "|" + ins.OMPConfig.Profile
	}
	if ins.OpencodeConfig != nil {
		k += "|" + ins.OpencodeConfig.DataDir
	}
	return k
}

// LiveModelsEnabled reports whether ins offers its CLI's live model list.
func LiveModelsEnabled(ins Instance) bool {
	return ins.LiveModels && (ins.Type == TypeOMP || ins.Type == TypeOpencode)
}

// CachedCLIModels returns ins's CLI model list, exec'ing the CLI only when
// the cache is cold, older than cliModelsTTL, or refresh is set. Concurrent
// callers share one exec. fetchedAt is when the returned list was fetched.
func CachedCLIModels(ctx context.Context, ins Instance, refresh bool) (models []ModelSeed, fetchedAt time.Time, err error) {
	key := cliModelsKey(ins)
	for {
		cliModelsMu.Lock()
		e, ok := cliModelsCache[key]
		if ok && !refresh && cliModelsNow().Sub(e.at) < cliModelsTTL {
			cliModelsMu.Unlock()
			return e.models, e.at, e.err
		}
		if wait, busy := cliModelsInflight[key]; busy {
			cliModelsMu.Unlock()
			select {
			case <-wait:
				// Another caller just fetched; a refresh is satisfied by it.
				refresh = false
				continue
			case <-ctx.Done():
				return nil, time.Time{}, ctx.Err()
			}
		}
		done := make(chan struct{})
		cliModelsInflight[key] = done
		cliModelsMu.Unlock()

		models, err := ListCLIModels(ctx, ins)
		cliModelsMu.Lock()
		e = cliModelsEntry{models: models, err: err, at: cliModelsNow()}
		// A failed refresh keeps the last good list on screen/in the picker.
		if prev, had := cliModelsCache[key]; err != nil && had && prev.err == nil {
			e.models = prev.models
		}
		cliModelsCache[key] = e
		delete(cliModelsInflight, key)
		close(done)
		cliModelsMu.Unlock()
		return e.models, e.at, e.err
	}
}

// PeekCLIModels returns whatever list is cached for ins (possibly stale or
// nil) without blocking, and starts a background refresh when the cache is
// cold or expired. For render paths (provider list, picker levels).
func PeekCLIModels(ins Instance) []ModelSeed {
	key := cliModelsKey(ins)
	cliModelsMu.Lock()
	e, ok := cliModelsCache[key]
	_, busy := cliModelsInflight[key]
	stale := !ok || cliModelsNow().Sub(e.at) >= cliModelsTTL
	cliModelsMu.Unlock()
	if stale && !busy {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), cliModelsFetchTimeout)
			defer cancel()
			_, _, _ = CachedCLIModels(ctx, ins, false)
		}()
	}
	return e.models
}

// opencodeHostedProviders are opencode's own hosted services (Zen and its
// Go plan). Mirrors opencode.hostedProviders — the spawn-side rule (6a).
var opencodeHostedProviders = []string{"opencode", "opencode-go"}

// IsOpencodeHostedModel reports whether an opencode model id runs on
// opencode's own servers (opencode/…, opencode-go/…).
func IsOpencodeHostedModel(id string) bool {
	p, _, _ := strings.Cut(id, "/")
	return slices.Contains(opencodeHostedProviders, p)
}

// OpencodeHostedAllowed reports whether hosted opencode models may run on
// ins: the operator opted in (opencode_allow_hosted), or the instance is
// itself logged into opencode's service (an auth.json entry for it, or
// OPENCODE_API_KEY in its env) — then the hosted service IS the account.
func OpencodeHostedAllowed(ins Instance) bool {
	if ins.OpencodeConfig != nil && ins.OpencodeConfig.AllowHosted {
		return true
	}
	for _, e := range ins.Env {
		if k, v, ok := strings.Cut(e, "="); ok && k == "OPENCODE_API_KEY" && strings.TrimSpace(v) != "" {
			return true
		}
	}
	p, err := OpencodeAuthFile(ins)
	if err != nil {
		return false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	for _, h := range opencodeHostedProviders {
		if _, ok := m[h]; ok {
			return true
		}
	}
	return false
}

// FilterLiveModels narrows a CLI model list to what ins offers: hosted
// opencode models only when OpencodeHostedAllowed, then LiveModelFilter.
func FilterLiveModels(ins Instance, models []ModelSeed) []ModelSeed {
	hosted := ins.Type != TypeOpencode || OpencodeHostedAllowed(ins)
	out := make([]ModelSeed, 0, len(models))
	for _, m := range models {
		if !hosted && IsOpencodeHostedModel(m.ID) {
			continue
		}
		if modelfilter.Match(m.ID+" "+m.Desc, ins.LiveModelFilter) {
			out = append(out, m)
		}
	}
	return out
}

// LiveDefaultFirst returns filtered with the effective default moved to the
// front: LiveModelDefault when still listed, else the first match. The
// picker treats entry 0 as the default, so the order IS the default rule.
func LiveDefaultFirst(filtered []ModelSeed, pin string) []ModelSeed {
	pin = strings.TrimSpace(pin)
	for i, m := range filtered {
		if pin != "" && m.ID == pin {
			out := make([]ModelSeed, 0, len(filtered))
			out = append(out, m)
			out = append(out, filtered[:i]...)
			return append(out, filtered[i+1:]...)
		}
	}
	return filtered
}

// LiveDefaultModel is the model a live-mode instance runs when the session
// pinned none: the pin if still offered, else the first filtered match. It
// may exec the CLI (cold cache). "" when live mode is off or nothing matches;
// a failed fetch falls back to the pin as typed.
func LiveDefaultModel(ctx context.Context, ins Instance) string {
	if !LiveModelsEnabled(ins) {
		return ""
	}
	models, _, err := CachedCLIModels(ctx, ins, false)
	if err != nil && len(models) == 0 {
		return strings.TrimSpace(ins.LiveModelDefault)
	}
	if list := LiveDefaultFirst(FilterLiveModels(ins, models), ins.LiveModelDefault); len(list) > 0 {
		return list[0].ID
	}
	return ""
}
