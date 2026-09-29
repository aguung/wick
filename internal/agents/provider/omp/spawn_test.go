package omp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	provider "github.com/yogasw/wick/internal/agents/provider"
)

func TestBuildArgsProfileFirstAndResume(t *testing.T) {
	ins := provider.Instance{Type: provider.TypeOMP, Name: "work", OMPConfig: &provider.OMPConfig{Profile: "wick-old-name"}}
	opt := provider.SpawnOptions{Workspace: "/w", ResumeID: "sid-1", ModelID: "openai-codex/gpt-5.2", Instance: &ins}
	got := buildArgs(ins, opt, "/s/.omp/soul.md", nil)
	want := []string{"--profile", "wick-old-name", "-p", "--mode", "json", "--no-title", "--yolo",
		"--cwd", "/w", "--append-system-prompt", "/s/.omp/soul.md",
		"--model", "openai-codex/gpt-5.2", "--resume", "sid-1"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv\n got %q\nwant %q", got, want)
	}
}

func TestBuildArgsFreshSessionNoResume(t *testing.T) {
	ins := provider.Instance{Type: provider.TypeOMP, Name: "My Omp"}
	got := buildArgs(ins, provider.SpawnOptions{}, "", nil)
	if got[1] != "wick-my-omp" || slices.Contains(got, "--resume") || slices.Contains(got, "--cwd") {
		t.Fatalf("argv = %q", got)
	}
}

func TestEnsureMCPConfigMergesAndIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mcp.json")
	os.WriteFile(path, []byte(`{"mcpServers":{"other":{"type":"stdio","command":"x"}}}`), 0o600)
	if err := ensureMCPConfig(dir); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.MCPServers["other"] == nil {
		t.Fatal("operator's server dropped")
	}
	w := doc.MCPServers["wick"]
	if w["url"] != "${WICK_MCP_URL}" || !strings.Contains(string(b), "Bearer ${WICK_MCP_TOKEN}") {
		t.Fatalf("wick entry = %v", w)
	}
	if strings.Contains(string(b), "tok-secret") {
		t.Fatal("token written to disk")
	}
	st1, _ := os.Stat(path)
	if err := ensureMCPConfig(dir); err != nil {
		t.Fatal(err)
	}
	st2, _ := os.Stat(path)
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Error("rewrote an already-current file")
	}
}

func TestEnsureMCPConfigRefusesInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(`{nope`), 0o600)
	if err := ensureMCPConfig(dir); err == nil {
		t.Fatal("expected error, file must not be clobbered")
	}
}

func TestProfileAgentDir(t *testing.T) {
	if got := profileAgentDir("/h", "", "wick-a"); got != filepath.Join("/h", ".omp", "profiles", "wick-a", "agent") {
		t.Fatal(got)
	}
	if got := profileAgentDir("/h", ".pi", "p"); got != filepath.Join("/h", ".pi", "profiles", "p", "agent") {
		t.Fatal(got)
	}
}

func TestMCPEnvNeedsBoth(t *testing.T) {
	if mcpEnv("", "t") != nil || mcpEnv("u", "") != nil {
		t.Fatal("partial MCP env must be nil")
	}
	if got := mcpEnv("http://x/mcp", "t"); len(got) != 2 {
		t.Fatal(got)
	}
}
