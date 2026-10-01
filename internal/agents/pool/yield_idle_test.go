package pool

import (
	"context"
	"sync"
	"testing"
)

// A spawn about to be admitted first asks idle warm provider servers to
// yield, naming the session and the provider it will run on.
func TestSendYieldsIdleServersBeforeSpawn(t *testing.T) {
	var mu sync.Mutex
	var calls [][3]string
	prev := yieldIdleServers
	yieldIdleServers = func(sessionID, pType, pName string) {
		mu.Lock()
		calls = append(calls, [3]string{sessionID, pType, pName})
		mu.Unlock()
	}
	t.Cleanup(func() { yieldIdleServers = prev })

	sp := &scriptedSpawner{}
	p, layout := newPool(t, 2, sp)
	setupSession(t, layout, "S-yield")
	if err := p.Send(context.Background(), "S-yield", "default", "ui", "user", "hello"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) == 0 || calls[0][0] != "S-yield" || calls[0][1] != "claude" {
		t.Fatalf("yield calls = %v", calls)
	}
}
