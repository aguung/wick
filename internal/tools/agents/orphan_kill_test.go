package agents

import (
	"errors"
	"testing"
	"time"

	"github.com/yogasw/wick/internal/agents/provider"
)

func spawnLog(t *testing.T, s *provider.SpawnLogger, session string, pid int, exitReason string, at time.Time) string {
	t.Helper()
	path := s.Path("claude", "work", session, at)
	if err := s.Append(path, provider.SpawnEvent{
		Type: "start", At: at, ProviderType: "claude", ProviderName: "work", SessionID: session, PID: pid,
	}); err != nil {
		t.Fatalf("append start: %v", err)
	}
	if exitReason != "" {
		if err := s.Append(path, provider.SpawnEvent{
			Type: "exit", At: at, SessionID: session, ExitReason: exitReason,
		}); err != nil {
			t.Fatalf("append exit: %v", err)
		}
	}
	return path
}

func lastExitReason(t *testing.T, s *provider.SpawnLogger, path string) string {
	t.Helper()
	events, err := s.Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	reason := ""
	for _, ev := range events {
		if ev.Type == "exit" {
			reason = ev.ExitReason
		}
	}
	return reason
}

func TestKillOrphanSpawns(t *testing.T) {
	s := provider.NewSpawnLogger(t.TempDir())
	now := time.Now().UTC()

	sess := "sess-1"
	target := spawnLog(t, s, sess, 100, "", now)
	// A sub-agent of the same session: stopping the parent must stop it
	// too, or it keeps spending tokens on work nobody awaits.
	sub := spawnLog(t, s, sess+"--sub-abc", 101, "", now.Add(-time.Minute))
	// Someone else's session, a finished spawn, and a dead pid: all left be.
	other := spawnLog(t, s, "sess-2", 102, "", now.Add(-2*time.Minute))
	finished := spawnLog(t, s, "sess-3", 103, "stopped", now.Add(-3*time.Minute))
	_ = finished
	dead := spawnLog(t, s, sess+"--sub-dead", 104, "", now.Add(-4*time.Minute))

	var killedPIDs []int
	alive := func(pid int) bool { return pid != 104 }
	kill := func(pid int) error {
		killedPIDs = append(killedPIDs, pid)
		return nil
	}

	got := killOrphanSpawnsWith(s, sess, alive, kill)

	if len(got) != 2 {
		t.Fatalf("killed %v, want the session pid and its sub-agent", got)
	}
	if len(killedPIDs) != 2 {
		t.Errorf("kill called for %v, want two pids", killedPIDs)
	}
	if r := lastExitReason(t, s, target); r != "stopped" {
		t.Errorf("session spawn reason = %q, want stopped", r)
	}
	if r := lastExitReason(t, s, sub); r != "stopped" {
		t.Errorf("sub-agent spawn reason = %q, want stopped", r)
	}
	if r := lastExitReason(t, s, other); r != "" {
		t.Errorf("another session was touched: reason = %q", r)
	}
	if r := lastExitReason(t, s, dead); r != "" {
		t.Errorf("a dead pid should not be signalled or recorded: reason = %q", r)
	}
}

func TestKillOrphanSpawnsSkipsOlderSpawnsOfSameSession(t *testing.T) {
	s := provider.NewSpawnLogger(t.TempDir())
	now := time.Now().UTC()
	sess := "sess-1"

	// The current spawn, and the one before it. An old log's pid may long
	// since have been recycled by an unrelated process — killing that would
	// be the worst bug a Stop button could have.
	current := spawnLog(t, s, sess, 200, "", now)
	previous := spawnLog(t, s, sess, 201, "", now.Add(-time.Hour))

	got := killOrphanSpawnsWith(s, sess, func(int) bool { return true }, func(int) error { return nil })

	if len(got) != 1 || got[0] != 200 {
		t.Fatalf("killed %v, want only the newest spawn's pid (200)", got)
	}
	if r := lastExitReason(t, s, current); r != "stopped" {
		t.Errorf("current spawn reason = %q, want stopped", r)
	}
	if r := lastExitReason(t, s, previous); r != "" {
		t.Errorf("previous spawn was written to: reason = %q", r)
	}
}

func TestKillOrphanSpawnsRecordsNothingWhenTheKillFails(t *testing.T) {
	s := provider.NewSpawnLogger(t.TempDir())
	path := spawnLog(t, s, "sess-1", 300, "", time.Now().UTC())

	got := killOrphanSpawnsWith(s, "sess-1",
		func(int) bool { return true },
		func(int) error { return errors.New("operation not permitted") })

	if len(got) != 0 {
		t.Fatalf("killed %v, want none — the signal was refused", got)
	}
	// Writing "stopped" for a process that is still running would turn a
	// visible failure into an invisible one.
	if r := lastExitReason(t, s, path); r != "" {
		t.Errorf("reason = %q, want empty after a refused kill", r)
	}
}

func TestSessionMatches(t *testing.T) {
	cases := []struct {
		spawn, session string
		want           bool
	}{
		{"abc", "abc", true},
		{"abc--sub-1f2e", "abc", true},
		{"abcdef", "abc", false},
		{"abc-other", "abc", false},
		{"", "abc", false},
	}
	for _, c := range cases {
		if got := sessionMatches(c.spawn, c.session); got != c.want {
			t.Errorf("sessionMatches(%q, %q) = %v, want %v", c.spawn, c.session, got, c.want)
		}
	}
}
