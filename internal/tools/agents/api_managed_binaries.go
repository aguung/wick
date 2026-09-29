package agents

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/yogasw/wick/internal/agents/provider"
	"github.com/yogasw/wick/internal/agents/provider/managedbin"
	"github.com/yogasw/wick/pkg/tool"
)

// api_managed_binaries.go is the Providers page's "Binary" section:
// status, releases, and the admin actions (install/update, activate =
// rollback, remove, re-verify). Every write is admin-only through the same
// guard as the rest of provider configuration. Nothing here downloads on
// its own: install starts only from a POST, and the list at most refreshes
// the cached "newest release" once an hour, in the background.

// ManagedBinaryDTO is one managed type's status.
type ManagedBinaryDTO struct {
	managedbin.Status
	Enabled bool `json:"enabled"`
	// SessionsOnOld counts running processes per non-current version —
	// "2 sessions still on v18.4.2".
	SessionsOnOld map[string]int `json:"sessions_on_old,omitempty"`
}

func managedDTO(t provider.Type) (ManagedBinaryDTO, error) {
	// Retention for versions whose last process has ended since the last
	// look — cheap (a /proc scan) and keeps the list honest.
	managedbin.Default.Prune(string(t))
	st, err := managedbin.Default.Status(string(t))
	if err != nil {
		return ManagedBinaryDTO{}, err
	}
	dto := ManagedBinaryDTO{Status: st, Enabled: provider.ManagedEnabled(t)}
	for _, iv := range st.Installed {
		if !iv.Current && iv.InUse > 0 {
			if dto.SessionsOnOld == nil {
				dto.SessionsOnOld = map[string]int{}
			}
			dto.SessionsOnOld[iv.Version] = iv.InUse
		}
	}
	// Stale newest-release answer: refresh off the request path.
	if !st.Job.Running() && (st.LatestCheckedAt.IsZero() || time.Since(st.LatestCheckedAt) > managedbin.LatestTTL) {
		go func(typ string) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, _ = managedbin.Default.CheckLatest(ctx, typ, false)
		}(string(t))
	}
	return dto, nil
}

func managedTypeParam(c *tool.Ctx) (provider.Type, bool) {
	t := provider.Type(c.PathValue("type"))
	if _, ok := managedbin.Lookup(string(t)); !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "not a managed binary type"})
		return "", false
	}
	return t, true
}

// apiManagedBinariesList: GET /api/managed-binaries — every registered type.
func apiManagedBinariesList(c *tool.Ctx) {
	if notReady(c) || !requireApprovedUser(c) {
		return
	}
	out := []ManagedBinaryDTO{}
	for _, s := range managedbin.Types() {
		dto, err := managedDTO(provider.Type(s))
		if err != nil {
			continue
		}
		out = append(out, dto)
	}
	c.JSON(http.StatusOK, map[string]any{"types": out, "is_admin": callerIsAdmin(c)})
}

// apiManagedBinaryReleases: GET …/{type}/releases — for "pick a version".
func apiManagedBinaryReleases(c *tool.Ctx) {
	if notReady(c) || !requireProviderAdmin(c) {
		return
	}
	t, ok := managedTypeParam(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Context(), 20*time.Second)
	defer cancel()
	rs, err := managedbin.Default.Releases(ctx, string(t))
	if err != nil {
		c.JSON(http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{"releases": rs})
}

// apiManagedBinaryCheck: POST …/{type}/check — refresh "newest release" now.
func apiManagedBinaryCheck(c *tool.Ctx) {
	if notReady(c) || !requireProviderAdmin(c) {
		return
	}
	t, ok := managedTypeParam(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Context(), 20*time.Second)
	defer cancel()
	_, _ = managedbin.Default.CheckLatest(ctx, string(t), true)
	dto, err := managedDTO(t)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, dto)
}

// apiManagedBinaryInstall: POST …/{type}/install[?tag=v1.2.3] — starts the
// background job and returns at once; the UI polls the list for progress.
func apiManagedBinaryInstall(c *tool.Ctx) {
	if notReady(c) || !requireProviderAdmin(c) {
		return
	}
	t, ok := managedTypeParam(c)
	if !ok {
		return
	}
	if !provider.ManagedEnabled(t) {
		c.JSON(http.StatusConflict, map[string]string{"error": "managed binaries are disabled for " + string(t)})
		return
	}
	tag := strings.TrimSpace(c.Query("tag"))
	j, err := managedbin.Default.StartInstall(string(t), tag)
	if errors.Is(err, managedbin.ErrJobRunning) {
		c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, map[string]any{"job": j})
}

// apiManagedBinaryActivate: POST …/{type}/activate?version= — rollback /
// roll forward to an installed version. Instant; sha256 re-checked.
func apiManagedBinaryActivate(c *tool.Ctx) {
	managedAction(c, func(t provider.Type, v string) error { return managedbin.Default.Activate(string(t), v) })
}

// apiManagedBinaryRemove: POST …/{type}/remove?version=.
func apiManagedBinaryRemove(c *tool.Ctx) {
	managedAction(c, func(t provider.Type, v string) error { return managedbin.Default.Remove(string(t), v) })
}

func managedAction(c *tool.Ctx, do func(provider.Type, string) error) {
	if notReady(c) || !requireProviderAdmin(c) {
		return
	}
	t, ok := managedTypeParam(c)
	if !ok {
		return
	}
	if err := do(t, strings.TrimSpace(c.Query("version"))); err != nil {
		c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	provider.InvalidateProbeCache("", "")
	dto, err := managedDTO(t)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, dto)
}

// apiManagedBinaryVerify: POST …/{type}/verify — wick runs the active
// managed binary's `--version` itself (sandboxed) and returns the line.
func apiManagedBinaryVerify(c *tool.Ctx) {
	if notReady(c) || !requireProviderAdmin(c) {
		return
	}
	t, ok := managedTypeParam(c)
	if !ok {
		return
	}
	raw, err := managedbin.Default.VerifyCurrent(c.Context(), string(t))
	resp := map[string]any{"output": raw}
	if err != nil {
		resp["error"] = err.Error()
	}
	c.JSON(http.StatusOK, resp)
}
