package opencode

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/yogasw/wick/internal/agents/provider"
	"github.com/yogasw/wick/internal/pkg/envscrub"
)

// provider_catalog.go fetches an instance's provider catalog from its
// opencode server (GET /provider + GET /provider/auth) for
// provider.OpencodeCatalog: login choices, API-key providers and the live
// model list all come from opencode itself.
//
// Which server: the instance's running turn server for the same data
// folder when there is one (read-only GETs, no lease). Otherwise a
// throwaway `opencode serve` is booted for the listing and killed right
// after — NOT through the manager: its env differs from a turn server's
// (no workspace MCP switches), so acquiring it would be a new key in the
// same group and the stale sweep would kill the real server. One boot
// costs about what `opencode models` does, and yields all three lists.

func init() { provider.OpencodeCatalogFetcher = fetchInstanceCatalog }

// catalogServe boots the throwaway server; swapped in tests.
var catalogServe = startServe

const catalogRequestTimeout = 20 * time.Second

func fetchInstanceCatalog(ctx context.Context, ins provider.Instance) (*provider.OpencodeCatalog, error) {
	dataDir, err := provider.OpencodeDataDir(ins)
	if err != nil {
		return nil, err
	}
	spec := serverSpec{instance: ins.Name, dir: dataDir}
	if h, ok := servers.LiveInGroup(spec.group()); ok {
		if cat, err := fetchCatalog(ctx, catalogClient(h, dataDir)); err == nil {
			return cat, nil
		}
	}
	bin, found := provider.ResolveBinary(ins)
	if !found {
		return nil, fmt.Errorf("opencode binary not found: %s", bin)
	}
	// The listing needs no MCP: the instance's extra servers stay off, so
	// the boot neither spawns nor dials them.
	lite := ins
	lite.ExtraMCPServers = ""
	added, err := spawnEnv(lite, "", "", "", nil)
	if err != nil {
		return nil, err
	}
	// AccountEnv carries the instance Env, so API keys count as connected.
	spec.bin = bin
	spec.env = append(append(envscrub.ScrubOSEnv(), provider.AccountEnv(ins)...), added...)
	h, err := catalogServe(ctx, spec, randomPassword())
	if err != nil {
		return nil, err
	}
	defer h.Kill()
	return fetchCatalog(ctx, catalogClient(h, dataDir))
}

func catalogClient(h *serverHandle, dir string) *apiClient {
	return &apiClient{base: h.url, password: h.password, dir: dir, http: &http.Client{Timeout: catalogRequestTimeout}}
}

// providerList is GET /provider.
type providerList struct {
	All []struct {
		ID     string   `json:"id"`
		Name   string   `json:"name"`
		Env    []string `json:"env"`
		Models map[string]struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"models"`
	} `json:"all"`
	Default   map[string]string `json:"default"`
	Connected []string          `json:"connected"`
}

// providerAuth is GET /provider/auth: providerID → methods.
type providerAuth map[string][]struct {
	Type  string `json:"type"`
	Label string `json:"label"`
}

// fetchCatalog reads both routes; /provider/auth failing still yields the
// providers (the login picker then falls back on its own).
func fetchCatalog(ctx context.Context, c *apiClient) (*provider.OpencodeCatalog, error) {
	var list providerList
	if err := c.do(ctx, http.MethodGet, "/provider", nil, &list); err != nil {
		return nil, err
	}
	cat := &provider.OpencodeCatalog{Connected: list.Connected, Default: list.Default}
	for _, p := range list.All {
		if p.ID == "" {
			continue
		}
		cp := provider.OpencodeCatalogProvider{ID: p.ID, Name: p.Name, Env: p.Env}
		for key, m := range p.Models {
			if m.Status == "deprecated" {
				continue
			}
			id := m.ID
			if id == "" {
				id = key
			}
			cp.Models = append(cp.Models, provider.ModelSeed{ID: p.ID + "/" + id, Desc: m.Name})
		}
		cat.Providers = append(cat.Providers, cp)
	}
	var auth providerAuth
	if err := c.do(ctx, http.MethodGet, "/provider/auth", nil, &auth); err == nil {
		cat.Auth = make(map[string][]provider.OpencodeAuthMethod, len(auth))
		for id, ms := range auth {
			for _, m := range ms {
				cat.Auth[id] = append(cat.Auth[id], provider.OpencodeAuthMethod{Type: m.Type, Label: m.Label})
			}
		}
	}
	return cat, nil
}
