package agents

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/yogasw/wick/internal/agents/provider"
	"github.com/yogasw/wick/internal/agents/provider/logintty"
	"github.com/yogasw/wick/pkg/tool"
)

// apiProviderLoginTTYLogout is POST .../logintty/logout?login_provider=X:
// removes every stored credential of one provider from the instance's
// store (omp `auth-broker logout`). A manage grant, like reconnect.
func apiProviderLoginTTYLogout(c *tool.Ctx) {
	if notReady(c) || !requireApprovedUser(c) {
		return
	}
	ins, ok := findLoginInstance(c)
	if !ok {
		return
	}
	if !requireProviderManage(c, ins.Type, ins.Name) {
		return
	}
	prov := strings.TrimSpace(c.Query("login_provider"))
	if err := logintty.LogoutProvider(ins.Type, provider.AccountEnv(ins), prov); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// apiProviderAPIKeySet is POST .../logintty/apikey with JSON body
// {provider, key} (a body, never the query, so the key stays out of access
// logs): stores the key as the instance Env var the CLI reads
// for that provider (empty key removes it), then refreshes the live model
// list so newly unlocked models show up. Editing Env is an admin write,
// same as the detail form's env editor.
func apiProviderAPIKeySet(c *tool.Ctx) {
	if notReady(c) || !requireApprovedUser(c) || !requireProviderAdmin(c) {
		return
	}
	ins, ok := findLoginInstance(c)
	if !ok {
		return
	}
	var body struct {
		Provider string `json:"provider"`
		Key      string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(c.W, c.R.Body, 16<<10)).Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	envVar, ok := logintty.APIKeyEnvVar(ins.Type, strings.TrimSpace(body.Provider))
	if !ok {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "unknown API-key provider"})
		return
	}
	key := strings.TrimSpace(body.Key)
	if strings.ContainsAny(key, "\r\n") {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "key must be a single line"})
		return
	}
	ins.Env = logintty.SetEnvVar(ins.Env, envVar, key)
	if err := provider.Save(ins); err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if provider.LiveModelsEnabled(ins) {
		saved := ins
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if _, _, err := provider.CachedCLIModels(ctx, saved, true); err != nil {
				log.Debug().Err(err).Str("provider", string(saved.Type)+"/"+saved.Name).Msg("live models refresh after API key save")
			}
		}()
	}
	c.JSON(http.StatusOK, map[string]any{"ok": true, "env": envVar, "set": key != ""})
}
