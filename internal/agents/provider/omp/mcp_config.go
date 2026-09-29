package omp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// mcp_config.go registers wick's MCP server for an omp spawn.
//
// omp has no argv flag for an MCP config. It reads the active profile's
// user-level file ~/.omp/profiles/<p>/agent/mcp.json (oh-my-pi
// docs/mcp-config.md "Profiles") and expands ${VAR} placeholders in url
// and headers at discovery time (same doc, "Discovery-time ${...}
// expansion"). So wick writes ONE static entry whose url and bearer are
// placeholders, and each spawn supplies the values in its own env: two
// sessions of the same profile never race on a token written to disk, and
// the secret never lands in a file.

const (
	mcpServerName  = "wick"
	mcpURLEnvVar   = "WICK_MCP_URL"
	mcpTokenEnvVar = "WICK_MCP_TOKEN"
)

// mcpEndpointFromEnv derives the loopback MCP URL from WICK_PORT. Empty
// when unset — the caller then skips MCP entirely.
func mcpEndpointFromEnv() string {
	port := strings.TrimSpace(os.Getenv("WICK_PORT"))
	if port == "" {
		return ""
	}
	return "http://127.0.0.1:" + port + "/mcp"
}

// wickServerEntry is the placeholder entry wick owns in mcp.json.
func wickServerEntry() map[string]any {
	return map[string]any{
		"type": "http",
		"url":  "${" + mcpURLEnvVar + "}",
		"headers": map[string]any{
			"Authorization": "Bearer ${" + mcpTokenEnvVar + "}",
		},
	}
}

// profileAgentDir is omp's per-profile agent dir: <home>/<configDir>/profiles/<p>/agent,
// configDir = $PI_CONFIG_DIR or ".omp" (packages/utils/src/dirs.ts).
func profileAgentDir(home, configDir, profile string) string {
	if configDir == "" {
		configDir = ".omp"
	}
	return filepath.Join(home, configDir, "profiles", profile, "agent")
}

// ensureMCPConfig merges wick's entry into <agentDir>/mcp.json, keeping
// every other server the operator configured. No write when the entry is
// already current.
func ensureMCPConfig(agentDir string) error {
	path := filepath.Join(agentDir, "mcp.json")
	doc := map[string]any{}
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &doc); err != nil {
			// Never clobber a file the operator hand-edited into invalid JSON.
			return fmt.Errorf("omp mcp.json %s is not valid JSON: %w", path, err)
		}
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	want := wickServerEntry()
	if cur, ok := servers[mcpServerName]; ok {
		a, _ := json.Marshal(cur)
		b, _ := json.Marshal(want)
		if string(a) == string(b) {
			return nil
		}
	}
	servers[mcpServerName] = want
	doc["mcpServers"] = servers
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o600)
}

// mcpEnv is the per-spawn env carrying the values the placeholders expand to.
func mcpEnv(endpoint, token string) []string {
	if endpoint == "" || token == "" {
		return nil
	}
	return []string{mcpURLEnvVar + "=" + endpoint, mcpTokenEnvVar + "=" + token}
}
