package gate

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// shortTempDir returns a path short enough for a unix socket on
// every platform we support. t.TempDir() encodes the test name into
// the dir, which can blow past Windows' bind() limits when test
// names get long. Cleaned up on test exit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "g")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// newTestManager wires a manager whose socketDir + specPath both
// resolve under tmpRoot. Caller passes the sessionID it'll exercise;
// the manager doesn't care what shape that takes.
func newTestManager(t *testing.T, tmpRoot string) (*ApprovalManager, func(string) string) {
	t.Helper()
	sessionDir := func(sid string) string { return filepath.Join(tmpRoot, sid) }
	socketDir := func(sid string) string {
		d := filepath.Join(sessionDir(sid), "g")
		_ = os.MkdirAll(d, 0o755)
		return d
	}
	specPath := func(sid string) string {
		return filepath.Join(socketDir(sid), "spec.json")
	}
	mgr, err := NewApprovalManager(ApprovalManagerOptions{
		Timeout:   200 * time.Millisecond,
		SocketDir: socketDir,
		SpecPath:  specPath,
	})
	if err != nil {
		t.Fatalf("NewApprovalManager: %v", err)
	}
	return mgr, specPath
}

func writeFakeSpec(t *testing.T, path string, s Spec) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestManager_StartStopSession(t *testing.T) {
	mgr, _ := newTestManager(t, shortTempDir(t))
	defer mgr.Stop()

	sock, err := mgr.StartSession("S1")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.Close()

	mgr.StopSession("S1")
	if _, err := net.DialTimeout("unix", sock, 200*time.Millisecond); err == nil {
		t.Fatal("expected dial to fail after StopSession")
	}
}

func TestManager_ResolveApproveSession_AutoApprovesNext(t *testing.T) {
	mgr, _ := newTestManager(t, shortTempDir(t))
	defer mgr.Stop()

	requested := make(chan ApprovalRequest, 4)
	mgr.onRequest = func(_ string, r ApprovalRequest) { requested <- r }

	sock, err := mgr.StartSession("S1")
	if err != nil {
		t.Fatal(err)
	}

	// First request: register pending → approve_session → record key.
	first := ApprovalRequest{ID: "req-1", SessionID: "S1", Cmd: "ls", MatchKey: "key-ls"}
	respCh1 := make(chan ApprovalResponse, 1)
	go func() { respCh1 <- dialAndSend(t, sock, first) }()

	select {
	case <-requested:
	case <-time.After(2 * time.Second):
		t.Fatal("first request never reached onRequest")
	}

	if ok, err := mgr.Resolve("S1", "req-1", DecisionApproveSession, "user clicked", "key-ls"); err != nil || !ok {
		t.Fatalf("Resolve: ok=%v err=%v", ok, err)
	}

	if !mgr.IsSessionApproved("S1", "key-ls") {
		t.Error("matchKey should be in session-approved set after approve_session")
	}

	resp1 := <-respCh1
	if resp1.Decision != DecisionApproveSession {
		t.Errorf("first decision: %q", resp1.Decision)
	}

	// Second request with same matchKey: should auto-approve without
	// reaching onRequest.
	second := ApprovalRequest{ID: "req-2", SessionID: "S1", Cmd: "ls", MatchKey: "key-ls"}
	resp2 := dialAndSend(t, sock, second)
	if resp2.Decision != DecisionApproveSession {
		t.Errorf("second decision: got %q, want %q", resp2.Decision, DecisionApproveSession)
	}

	// onRequest should not have fired again.
	select {
	case r := <-requested:
		t.Errorf("session-approved request should not reach onRequest, got: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestManager_ResolveApproveAlways_PersistsToSpec(t *testing.T) {
	mgr, specPath := newTestManager(t, shortTempDir(t))
	defer mgr.Stop()

	// Pre-populate spec.json so writeSpec round-trips through a real
	// file. This mirrors the production wiring: pool.factory writes
	// the initial spec, manager only edits AutoApproved.
	writeFakeSpec(t, specPath("S1"), Spec{SessionID: "S1"})

	requested := make(chan ApprovalRequest, 1)
	mgr.onRequest = func(_ string, r ApprovalRequest) { requested <- r }

	sock, err := mgr.StartSession("S1")
	if err != nil {
		t.Fatal(err)
	}

	respCh := make(chan ApprovalResponse, 1)
	go func() {
		respCh <- dialAndSend(t, sock, ApprovalRequest{ID: "req-x", SessionID: "S1", Cmd: "git status", MatchKey: "key-gs"})
	}()
	<-requested

	if ok, err := mgr.Resolve("S1", "req-x", DecisionApproveAlways, "always", "key-gs"); err != nil || !ok {
		t.Fatalf("Resolve: ok=%v err=%v", ok, err)
	}
	<-respCh

	// Verify spec.json now contains the matchKey.
	data, err := os.ReadFile(specPath("S1"))
	if err != nil {
		t.Fatal(err)
	}
	var spec Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	if len(spec.AutoApproved) != 1 || spec.AutoApproved[0] != "key-gs" {
		t.Errorf("spec.AutoApproved: %+v", spec.AutoApproved)
	}

	// AutoApprovedFor must also reflect it.
	got := mgr.AutoApprovedFor("S1")
	if len(got) != 1 || got[0] != "key-gs" {
		t.Errorf("AutoApprovedFor: %+v", got)
	}
}

func TestManager_RevokeAlways(t *testing.T) {
	mgr, specPath := newTestManager(t, shortTempDir(t))
	defer mgr.Stop()

	writeFakeSpec(t, specPath("S1"), Spec{
		SessionID:    "S1",
		AutoApproved: []string{"k1", "k2", "k3"},
	})

	if err := mgr.RevokeAlways("S1", "k2"); err != nil {
		t.Fatalf("RevokeAlways: %v", err)
	}
	got := mgr.AutoApprovedFor("S1")
	if len(got) != 2 || got[0] != "k1" || got[1] != "k3" {
		t.Errorf("AutoApprovedFor after revoke: %+v", got)
	}
}

func TestManager_OnResolved_Fires(t *testing.T) {
	mgr, _ := newTestManager(t, shortTempDir(t))
	defer mgr.Stop()

	var (
		mu       sync.Mutex
		resolved []string
	)
	mgr.onResolved = func(_, requestID, decision string) {
		mu.Lock()
		resolved = append(resolved, requestID+"="+decision)
		mu.Unlock()
	}
	requested := make(chan ApprovalRequest, 1)
	mgr.onRequest = func(_ string, r ApprovalRequest) { requested <- r }

	sock, err := mgr.StartSession("S1")
	if err != nil {
		t.Fatal(err)
	}

	respCh := make(chan ApprovalResponse, 1)
	go func() {
		respCh <- dialAndSend(t, sock, ApprovalRequest{ID: "r1", SessionID: "S1", Cmd: "ls", MatchKey: "k"})
	}()
	<-requested

	if _, err := mgr.Resolve("S1", "r1", DecisionApproveOnce, "", "k"); err != nil {
		t.Fatal(err)
	}
	<-respCh

	mu.Lock()
	defer mu.Unlock()
	if len(resolved) != 1 || resolved[0] != "r1=approve_once" {
		t.Errorf("onResolved: %+v", resolved)
	}
}
