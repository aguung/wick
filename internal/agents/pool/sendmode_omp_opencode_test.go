package pool

import (
	"testing"

	"github.com/yogasw/wick/internal/agents/provider"
)

// omp/opencode run one process per turn, like codex: a mid-turn message
// must queue, not stack a second process.
func TestSendModeForOneShotCLIs(t *testing.T) {
	for _, ty := range []provider.Type{provider.TypeOMP, provider.TypeOpencode} {
		if got := sendModeFor(ty, ""); got != provider.SendRespawnQueue {
			t.Errorf("%s default = %v, want queue", ty, got)
		}
	}
	if got := sendModeFor(provider.TypeGemini, ""); got != provider.SendAppend {
		t.Errorf("gemini default changed: %v", got)
	}
}
