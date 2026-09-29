package opencode

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	provider "github.com/yogasw/wick/internal/agents/provider"
)

func TestBuildArgsResumeAndModel(t *testing.T) {
	got := buildArgs(provider.SpawnOptions{ResumeID: "ses_1", ModelID: "openai/gpt-5.2"}, nil)
	want := []string{"run", "--format", "json", "--thinking", "--auto", "--model", "openai/gpt-5.2", "--session", "ses_1"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv\n got %q\nwant %q", got, want)
	}
	if fresh := buildArgs(provider.SpawnOptions{}, nil); slices.Contains(fresh, "--session") {
		t.Fatalf("fresh spawn resumes: %q", fresh)
	}
}

func TestSpawnEnvIsolatesDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "oc-a")
	ins := provider.Instance{Type: provider.TypeOpencode, Name: "renamed", OpencodeConfig: &provider.OpencodeConfig{DataDir: dir}}
	env, err := spawnEnv(ins, "/s/soul.md", "http://127.0.0.1:1/mcp", "tok-secret")
	if err != nil {
		t.Fatal(err)
	}
	if env[0] != "XDG_DATA_HOME="+dir {
		t.Fatalf("env[0] = %q", env[0])
	}
	var cfgRaw string
	for _, kv := range env {
		if strings.HasPrefix(kv, configEnvVar+"=") {
			cfgRaw = strings.TrimPrefix(kv, configEnvVar+"=")
		}
	}
	if strings.Contains(cfgRaw, "tok-secret") {
		t.Fatal("token inlined into config")
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(cfgRaw), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["share"] != "disabled" {
		t.Errorf("share = %v, want disabled", cfg["share"])
	}
	if cfg["permission"] != "allow" {
		t.Errorf("permission = %v", cfg["permission"])
	}
	wick := cfg["mcp"].(map[string]any)["wick"].(map[string]any)
	if wick["type"] != "remote" || wick["url"] != "{env:WICK_MCP_URL}" {
		t.Errorf("mcp = %v", wick)
	}
	if ins := cfg["instructions"].([]any); ins[0] != "/s/soul.md" {
		t.Errorf("instructions = %v", ins)
	}
	if !slices.Contains(env, "WICK_MCP_TOKEN=tok-secret") {
		t.Error("token missing from env")
	}
}

func TestConfigWithoutMCP(t *testing.T) {
	if c := configContent(false, ""); strings.Contains(c, "mcp") || !strings.Contains(c, `"permission":"allow"`) || !strings.Contains(c, `"share":"disabled"`) {
		t.Fatal(c)
	}
}
