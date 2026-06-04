// Package rest — integration health probe for the REST (OpenAI-compatible) channel.

package rest

import agentchannels "github.com/yogasw/wick/internal/agents/channels"

// HealthCheck satisfies channels.HealthChecker. Verifies the two
// prerequisites for serving any request: enabled flag and a wired
// authenticator.
func (c *Channel) HealthCheck() []agentchannels.HealthCheck {
	c.cfgMu.Lock()
	enabled := c.cfg.Enabled == "true"
	c.cfgMu.Unlock()

	if !enabled {
		return []agentchannels.HealthCheck{{
			Name:  "REST channel enabled",
			OK:    false,
			Error: "channel is not enabled — set enabled=true in channel config",
		}}
	}

	checks := []agentchannels.HealthCheck{
		{Name: "REST channel enabled", OK: true, Detail: "enabled=true"},
	}
	if c.auth == nil {
		checks = append(checks, agentchannels.HealthCheck{
			Name:  "Authenticator wired",
			OK:    false,
			Error: "no authenticator registered (server startup issue)",
		})
	} else {
		checks = append(checks, agentchannels.HealthCheck{
			Name:   "Authenticator wired",
			OK:     true,
			Detail: "PAT authentication ready",
		})
	}
	return checks
}
