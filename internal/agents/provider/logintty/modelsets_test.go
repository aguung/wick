package logintty

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yogasw/wick/internal/agents/provider"
)

func fakeModelSets(t *testing.T, models []provider.ModelSeed, accts []PoolAccount) {
	t.Helper()
	pm, pa := modelSetsModels, modelSetsAccounts
	modelSetsModels = func(context.Context, provider.Instance) ([]provider.ModelSeed, error) { return models, nil }
	modelSetsAccounts = func(provider.Instance) []PoolAccount { return accts }
	t.Cleanup(provider.SetModelStateDirForTest(t.TempDir()))
	t.Cleanup(func() { modelSetsModels, modelSetsAccounts = pm, pa })
}

func ids(rows []provider.ModelChoice) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

var liveIns = provider.Instance{Type: provider.TypeOMP, Name: "t", ModelSelect: true, LiveModels: true}

func TestCLIModelSetsProviderAndAccountLevels(t *testing.T) {
	fakeModelSets(t,
		[]provider.ModelSeed{{ID: "openai-codex/gpt-5.5"}, {ID: "openai-codex/gpt-5.4"}, {ID: "anthropic/claude-x"}},
		labelPool([]PoolAccount{
			{Provider: "openai-codex", Email: "a@example.test", Plan: "free", Status: "active"},
			{Provider: "openai-codex", Email: "b@example.test", Plan: "plus", Status: "active"},
			{Provider: "anthropic", Email: "c@example.test", Status: "active"},
			{Provider: "anthropic", Email: "d@example.test", Status: "disabled"},
		}))
	s, ok := provider.ModelSetsFor(provider.TypeOMP)
	if !ok {
		t.Fatal("omp ModelSets not registered")
	}
	ctx := context.Background()
	sets, _ := s.Sets(ctx, liveIns)
	if got := ids(sets); !reflect.DeepEqual(got, []string{"openai-codex", "anthropic"}) || !sets[0].Live {
		t.Fatalf("level 1 = providers: %v", sets)
	}
	// openai-codex has 2 accounts → Auto + one row each.
	acc, _ := s.Expand(ctx, liveIns, []string{"openai-codex"})
	if got := ids(acc); !reflect.DeepEqual(got, []string{"auto", "1", "2"}) {
		t.Fatalf("account level: %v", got)
	}
	// anthropic has ONE active account (the other is disabled) → models directly.
	m, _ := s.Expand(ctx, liveIns, []string{"anthropic"})
	if got := ids(m); !reflect.DeepEqual(got, []string{"anthropic/claude-x"}) || m[0].Live {
		t.Fatalf("single-account provider must list models: %v", m)
	}
	// provider/account → models of that provider only.
	m, _ = s.Expand(ctx, liveIns, []string{"openai-codex", "2"})
	if got := ids(m); !reflect.DeepEqual(got, []string{"openai-codex/gpt-5.5", "openai-codex/gpt-5.4"}) || m[0].Label != "gpt-5.5" {
		t.Fatalf("model level: %v", m)
	}
	// Refused on account 2 only → greyed there, default moves; Auto untouched.
	provider.MarkModelUnavailable(liveIns, provider.AvailabilityAccount("openai-codex", "2"), "openai-codex/gpt-5.5", "free")
	m, _ = s.Expand(ctx, liveIns, []string{"openai-codex", "2"})
	if !m[0].Unavailable || m[0].Default || !m[1].Default {
		t.Fatalf("refused model: %+v", m)
	}
	m, _ = s.Expand(ctx, liveIns, []string{"openai-codex", "auto"})
	if m[0].Unavailable {
		t.Fatalf("refusal leaked to auto: %+v", m)
	}
}

func TestCLIModelSetsSkipsTrivialLevels(t *testing.T) {
	fakeModelSets(t, []provider.ModelSeed{{ID: "openai/gpt-5"}, {ID: "openai/gpt-5-mini"}}, nil)
	s, _ := provider.ModelSetsFor(provider.TypeOpencode)
	ins := liveIns
	ins.Type = provider.TypeOpencode
	sets, _ := s.Sets(context.Background(), ins)
	if got := ids(sets); !reflect.DeepEqual(got, []string{"openai/gpt-5", "openai/gpt-5-mini"}) {
		t.Fatalf("one provider, one account → models at level 1: %v", got)
	}
	// Live models off → no grouping (flat curated list applies).
	ins.LiveModels = false
	if sets, _ := s.Sets(context.Background(), ins); sets != nil {
		t.Fatalf("live off must not group: %v", sets)
	}
}

func TestCLIModelSetsResolve(t *testing.T) {
	s, _ := provider.ModelSetsFor(provider.TypeOMP)
	sp, err := s.Resolve(liveIns, []string{"openai-codex", "2"}, "openai-codex/gpt-5.4")
	if err != nil || sp != (provider.SpawnPin{Model: "openai-codex/gpt-5.4", Provider: "openai-codex", Account: "2"}) {
		t.Fatalf("resolve: %+v %v", sp, err)
	}
	sp, _ = s.Resolve(liveIns, []string{"openai-codex", "auto"}, "openai-codex/gpt-5.4")
	if sp.Account != "" {
		t.Fatalf("auto must not pin an account: %+v", sp)
	}
	if _, err := s.Resolve(liveIns, []string{"anthropic"}, "openai-codex/gpt-5.4"); err == nil {
		t.Fatal("model of another provider must be refused")
	}
	// End to end through ModelArgs: the CLI gets the bare model id.
	pin := provider.EncodePin([]string{"openai-codex", "2"}, "openai-codex/gpt-5.4")
	args := provider.ModelArgs(provider.SpawnOptions{Instance: &liveIns, ModelID: pin}, nil)
	if !reflect.DeepEqual(args, []string{"--model", "openai-codex/gpt-5.4"}) {
		t.Fatalf("ModelArgs: %q", args)
	}
}

func TestOpencodeAccountFoldersAndRotation(t *testing.T) {
	base := t.TempDir()
	ins := provider.Instance{Type: provider.TypeOpencode, Name: "oc", ModelSelect: true, LiveModels: true,
		OpencodeConfig: &provider.OpencodeConfig{DataDir: base}}
	writeAuth := func(i provider.Instance, providers ...string) {
		t.Helper()
		f, err := provider.OpencodeAuthFile(i)
		if err != nil {
			t.Fatal(err)
		}
		_ = os.MkdirAll(filepath.Dir(f), 0o700)
		m := map[string]any{}
		for _, p := range providers {
			m[p] = map[string]string{"type": "api"}
		}
		b, _ := json.Marshal(m)
		if err := os.WriteFile(f, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeAuth(ins, "openai", "anthropic")
	id, a2, err := provider.NewOpencodeAccount(ins)
	if err != nil || id != "a2" {
		t.Fatalf("new account: %q %v", id, err)
	}
	writeAuth(a2, "openai")

	pm, pa := modelSetsModels, modelSetsAccounts
	t.Cleanup(func() { modelSetsModels, modelSetsAccounts = pm, pa })
	modelSetsModels = func(context.Context, provider.Instance) ([]provider.ModelSeed, error) {
		return []provider.ModelSeed{{ID: "openai/gpt-5"}, {ID: "anthropic/claude-x"}}, nil
	}
	t.Cleanup(provider.SetModelStateDirForTest(t.TempDir()))
	s, _ := provider.ModelSetsFor(provider.TypeOpencode)
	acc, _ := s.Expand(context.Background(), ins, []string{"openai"})
	if got := ids(acc); !reflect.DeepEqual(got, []string{"auto", "main", "a2"}) {
		t.Fatalf("openai has two folders → account level: %v", got)
	}
	if m, _ := s.Expand(context.Background(), ins, []string{"anthropic"}); len(m) != 1 || m[0].Live {
		t.Fatalf("anthropic has one folder → models: %v", ids(m))
	}

	// Pinned folder → spawn runs in that folder.
	pin := provider.EncodePin([]string{"openai", "a2"}, "openai/gpt-5")
	if _, acct := provider.OpencodeSpawnAccount(ins, pin); acct != "a2" {
		t.Fatalf("pinned account = %q", acct)
	}
	// Auto: main first; a quota hit on main rotates to a2.
	auto := provider.EncodePin([]string{"openai", "auto"}, "openai/gpt-5")
	if _, acct := provider.OpencodeSpawnAccount(ins, auto); acct != "" {
		t.Fatalf("auto should start on main, got %q", acct)
	}
	provider.MarkAccountExhausted(ins, "openai", "main")
	if _, acct := provider.OpencodeSpawnAccount(ins, auto); acct != "a2" {
		t.Fatalf("auto after quota hit = %q, want a2", acct)
	}
	d, _ := provider.WithOpencodeAccount(ins, "a2")
	if got, _ := provider.OpencodeDataDir(d); got != filepath.Join(base, "accounts", "a2") {
		t.Fatalf("account data dir = %q", got)
	}
}
