package opencode

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	provider "github.com/yogasw/wick/internal/agents/provider"
)

func TestBuildArgsResumeAndModel(t *testing.T) {
	got := buildArgs(provider.SpawnOptions{ResumeID: "ses_1"}, nil, "openai/gpt-5.2", false)
	want := []string{"run", "--format", "json", "--thinking", "--auto", "--model", "openai/gpt-5.2", "--session", "ses_1"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv\n got %q\nwant %q", got, want)
	}
	// --model already in the instance args: not added twice
	got = buildArgs(provider.SpawnOptions{ExtraArgs: []string{"-m", "openai/x"}}, nil, "openai/x", true)
	if n := strings.Count(strings.Join(got, " "), "openai/x"); n != 1 || slices.Contains(got, "--session") {
		t.Fatalf("argv %q", got)
	}
}

func cfgOf(t *testing.T, env []string) map[string]any {
	t.Helper()
	for _, kv := range env {
		if strings.HasPrefix(kv, configEnvVar+"=") {
			var cfg map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(kv, configEnvVar+"=")), &cfg); err != nil {
				t.Fatal(err)
			}
			return cfg
		}
	}
	t.Fatal("no inline config")
	return nil
}

func TestSpawnEnvIsolatesAndMerges(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "oc-a")
	ins := provider.Instance{Type: provider.TypeOpencode, Name: "renamed", OpencodeConfig: &provider.OpencodeConfig{DataDir: dir},
		ExtraMCPServers: `{"mcpServers":{"gh":{"url":"https://api.gh/mcp","headers":{"Authorization":"Bearer ${GH_TOKEN}"}}}}`}
	env, err := spawnEnv(ins, "/s/soul.md", "http://127.0.0.1:1/mcp", "tok-secret", []string{"hostmcp", "gh"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"XDG_DATA_HOME=" + dir, "XDG_CONFIG_HOME=" + filepath.Join(dir, "config"),
		"OPENCODE_DISABLE_AUTOUPDATE=true", "OPENCODE_CONFIG=", "OPENCODE_CONFIG_DIR=", "OPENCODE_AUTO_SHARE=false", "WICK_MCP_TOKEN=tok-secret"} {
		if !slices.Contains(env, want) {
			t.Errorf("env missing %q", want)
		}
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, configEnvVar+"=") && strings.Contains(kv, "tok-secret") {
			t.Fatal("token inlined into config")
		}
	}
	cfg := cfgOf(t, env)
	if cfg["permission"] != "allow" || cfg["share"] != "disabled" {
		t.Errorf("cfg %v", cfg)
	}
	mcp := cfg["mcp"].(map[string]any)
	if w := mcp["wick"].(map[string]any); w["type"] != "remote" || w["url"] != "{env:WICK_MCP_URL}" {
		t.Errorf("wick %v", w)
	}
	if h := mcp["hostmcp"].(map[string]any); h["enabled"] != false {
		t.Errorf("host server not disabled: %v", h)
	}
	gh := mcp["gh"].(map[string]any)
	if gh["enabled"] != true || gh["headers"].(map[string]any)["Authorization"] != "Bearer {env:GH_TOKEN}" {
		t.Errorf("extra overridden by the disable list or not converted: %v", gh)
	}
	if ins := cfg["instructions"].([]any); ins[0] != "/s/soul.md" {
		t.Errorf("instructions = %v", ins)
	}
}

func TestSpawnEnvRejectsBadExtras(t *testing.T) {
	ins := provider.Instance{Type: provider.TypeOpencode, Name: "x", OpencodeConfig: &provider.OpencodeConfig{DataDir: t.TempDir()},
		ExtraMCPServers: `{"wick":{"url":"https://evil"}}`}
	if _, err := spawnEnv(ins, "", "", "", nil); err == nil {
		t.Fatal("extra named wick accepted")
	}
}

func TestConfigWithoutMCP(t *testing.T) {
	if c := configContent(false, "", nil, nil); strings.Contains(c, "mcp") || !strings.Contains(c, `"share":"disabled"`) {
		t.Fatal(c)
	}
}

func TestForeignMCPNames(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "repo", "sub")
	home := filepath.Join(root, "home")
	os.MkdirAll(filepath.Join(ws, ".opencode"), 0o755)
	os.MkdirAll(filepath.Join(home, ".opencode"), 0o755)
	os.WriteFile(filepath.Join(root, "repo", "opencode.jsonc"), []byte("{\n // comment\n \"mcp\": {\"projmcp\": {\"type\": \"remote\", \"url\": \"http://x//y\"},},\n}"), 0o644)
	os.WriteFile(filepath.Join(ws, ".opencode", "opencode.json"), []byte(`{"mcp":{"dotmcp":{}}}`), 0o644)
	os.WriteFile(filepath.Join(home, ".opencode", "opencode.json"), []byte(`{"mcp":{"homemcp":{}}}`), 0o644)
	got := foreignMCPNames(ws, home)
	if strings.Join(got, ",") != "dotmcp,homemcp,projmcp" {
		t.Fatalf("got %v", got)
	}
}

func TestResolveModel(t *testing.T) {
	data := t.TempDir()
	ins := provider.Instance{Type: provider.TypeOpencode, Name: "oc", OpencodeConfig: &provider.OpencodeConfig{DataDir: data}}
	// no model, no login → refuse, mention login
	if _, _, err := resolveModel(ins, provider.SpawnOptions{}, nil); !errors.Is(err, ErrNoModel) || !strings.Contains(err.Error(), "log in first") {
		t.Fatalf("got %v", err)
	}
	// logged in but still no model → refuse (would fall back to hosted)
	os.MkdirAll(filepath.Join(data, "opencode"), 0o700)
	os.WriteFile(filepath.Join(data, "opencode", "auth.json"), []byte(`{"openai":{"type":"oauth"}}`), 0o600)
	if _, _, err := resolveModel(ins, provider.SpawnOptions{}, nil); !errors.Is(err, ErrNoModel) || !strings.Contains(err.Error(), "pick a model") {
		t.Fatalf("got %v", err)
	}
	ins.OpencodeConfig.Model = "openai/gpt-5.5"
	if m, _, err := resolveModel(ins, provider.SpawnOptions{}, nil); err != nil || m != "openai/gpt-5.5" {
		t.Fatalf("instance model: %q %v", m, err)
	}
	// session pin wins over the instance default
	if m, _, _ := resolveModel(ins, provider.SpawnOptions{ModelID: "openai/o5"}, nil); m != "openai/o5" {
		t.Fatalf("pin: %q", m)
	}
	// hosted opencode/… refused unless allowed
	if _, _, err := resolveModel(ins, provider.SpawnOptions{}, []string{"--model", "opencode/big-pickle"}); err == nil || !strings.Contains(err.Error(), "opencode_allow_hosted") {
		t.Fatalf("hosted allowed by default: %v", err)
	}
	ins.OpencodeConfig.AllowHosted = true
	if m, inArgs, err := resolveModel(ins, provider.SpawnOptions{}, []string{"--model=opencode/big-pickle"}); err != nil || m != "opencode/big-pickle" || !inArgs {
		t.Fatalf("hosted opt-in: %q %v %v", m, inArgs, err)
	}
}
