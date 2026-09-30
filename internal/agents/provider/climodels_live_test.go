package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func liveTestInstance(t *testing.T) Instance {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "opencode")
	os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755)
	return Instance{
		Type: TypeOpencode, Name: "oc-live", Binary: bin, ModelSelect: true, LiveModels: true,
		OpencodeConfig: &OpencodeConfig{DataDir: t.TempDir()},
	}
}

func stubCLIModels(t *testing.T, out string, err error) *int {
	t.Helper()
	calls := 0
	prev := cliModelsRunner
	cliModelsRunner = func(ctx context.Context, bin string, args, env []string) ([]byte, error) {
		calls++
		return []byte(out), err
	}
	t.Cleanup(func() {
		cliModelsRunner = prev
		cliModelsMu.Lock()
		cliModelsCache = map[string]cliModelsEntry{}
		cliModelsMu.Unlock()
	})
	return &calls
}

func TestCachedCLIModelsTTLAndRefresh(t *testing.T) {
	ins := liveTestInstance(t)
	calls := stubCLIModels(t, "openai/gpt-5.5\nopenai/gpt-5.5-mini\n", nil)
	now := time.Unix(1000, 0)
	cliModelsNow = func() time.Time { return now }
	t.Cleanup(func() { cliModelsNow = time.Now })

	for i := 0; i < 3; i++ {
		if m, _, err := CachedCLIModels(context.Background(), ins, false); err != nil || len(m) != 2 {
			t.Fatalf("got %v %v", m, err)
		}
	}
	if *calls != 1 {
		t.Fatalf("fresh cache re-exec'd: %d calls", *calls)
	}
	CachedCLIModels(context.Background(), ins, true)
	if *calls != 2 {
		t.Fatalf("refresh did not exec: %d", *calls)
	}
	now = now.Add(cliModelsTTL)
	CachedCLIModels(context.Background(), ins, false)
	if *calls != 3 {
		t.Fatalf("expired cache not refetched: %d", *calls)
	}
}

func TestCachedCLIModelsFailedRefreshKeepsList(t *testing.T) {
	ins := liveTestInstance(t)
	stubCLIModels(t, "openai/gpt-5.5\n", nil)
	CachedCLIModels(context.Background(), ins, false)
	cliModelsRunner = func(ctx context.Context, bin string, args, env []string) ([]byte, error) {
		return nil, errors.New("boom")
	}
	m, _, err := CachedCLIModels(context.Background(), ins, true)
	if err == nil || len(m) != 1 {
		t.Fatalf("want last good list + error, got %v %v", m, err)
	}
}

func TestFilterLiveModelsHostedAndFilter(t *testing.T) {
	ins := liveTestInstance(t)
	all := []ModelSeed{{ID: "opencode/big-pickle"}, {ID: "opencode-go/kimi"}, {ID: "openai/gpt-5.5"}, {ID: "openai/gpt-5.5-mini"}, {ID: "anthropic/claude-sonnet"}, {ID: "google/gemini-3"}}
	ids := func(ms []ModelSeed) string {
		var s []string
		for _, m := range ms {
			s = append(s, m.ID)
		}
		return strings.Join(s, ",")
	}
	if got := ids(FilterLiveModels(ins, all)); strings.Contains(got, "opencode/") {
		t.Fatalf("hosted leaked without opt-in: %s", got)
	}
	ins.LiveModelFilter = "claude|gpt !mini"
	if got := ids(FilterLiveModels(ins, all)); got != "openai/gpt-5.5,anthropic/claude-sonnet" {
		t.Fatalf("filter: %s", got)
	}
	// logged into opencode Zen → hosted models are the account itself
	ins.LiveModelFilter = ""
	os.MkdirAll(filepath.Join(ins.OpencodeConfig.DataDir, "opencode"), 0o700)
	os.WriteFile(filepath.Join(ins.OpencodeConfig.DataDir, "opencode", "auth.json"), []byte(`{"opencode":{"type":"api"}}`), 0o600)
	if got := ids(FilterLiveModels(ins, all)); !strings.HasPrefix(got, "opencode/big-pickle") {
		t.Fatalf("zen login should allow hosted: %s", got)
	}
}

func TestLiveDefaultModelAndEffectiveModels(t *testing.T) {
	ins := liveTestInstance(t)
	stubCLIModels(t, "openai/gpt-5.5\nanthropic/claude-sonnet\nopencode/big-pickle\n", nil)
	if got := LiveDefaultModel(context.Background(), ins); got != "openai/gpt-5.5" {
		t.Fatalf("first match: %q", got)
	}
	ins.LiveModelDefault = "anthropic/claude-sonnet"
	if got := LiveDefaultModel(context.Background(), ins); got != "anthropic/claude-sonnet" {
		t.Fatalf("pin: %q", got)
	}
	em := ins.EffectiveModels()
	if len(em) != 2 || em[0].ID != "anthropic/claude-sonnet" {
		t.Fatalf("effective models (pin first, no hosted): %v", em)
	}
	ins.LiveModelDefault = "gone/model"
	if got := LiveDefaultModel(context.Background(), ins); got != "openai/gpt-5.5" {
		t.Fatalf("vanished pin falls back to first: %q", got)
	}
	ins.LiveModels = false
	if got := LiveDefaultModel(context.Background(), ins); got != "" {
		t.Fatalf("live off: %q", got)
	}
}
