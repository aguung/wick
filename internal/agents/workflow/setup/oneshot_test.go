package setup

import (
	"path/filepath"
	"slices"
	"testing"

	agentprovider "github.com/yogasw/wick/internal/agents/provider"
)

func TestOneShotArgs(t *testing.T) {
	omp := agentprovider.Instance{Type: agentprovider.TypeOMP, Name: "o", OMPConfig: &agentprovider.OMPConfig{Profile: "wick-o"}}
	env, args, err := oneShotArgs(omp, "-hi")
	if err != nil || env != nil || !slices.Equal(args, []string{"--profile", "wick-o", "-p", "--no-title", "--yolo", "--", "-hi"}) {
		t.Fatalf("omp %v %q %v", env, args, err)
	}
	dir := filepath.Join(t.TempDir(), "oc")
	oc := agentprovider.Instance{Type: agentprovider.TypeOpencode, Name: "oc", OpencodeConfig: &agentprovider.OpencodeConfig{DataDir: dir}}
	env, args, err = oneShotArgs(oc, "hi")
	if err != nil || env[0] != "XDG_DATA_HOME="+dir || !slices.Equal(args, []string{"run", "--auto", "--", "hi"}) {
		t.Fatalf("opencode %v %q %v", env, args, err)
	}
	if _, args, _ := oneShotArgs(agentprovider.Instance{Type: agentprovider.TypeClaude}, "x"); args != nil {
		t.Fatal("claude must keep its own shape")
	}
}
