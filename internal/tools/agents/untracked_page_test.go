package agents

import (
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/yogasw/wick/internal/agents/session"
	"github.com/yogasw/wick/internal/tools/agents/view"
)

// Every page pageLooseSessions returns must be exactly that slice of a full
// sidebar-ordered sort — the early stop is an optimisation, never a change
// in what the rail shows. Pool-known sessions are scattered through the
// list with clocks far fresher (and staler) than their saved ones, the case
// where stopping early could go wrong.
func TestPageLooseSessionsMatchesFullSort(t *testing.T) {
	base := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	sessions := map[string]session.Session{}
	lc := map[string]view.SessionLifecycleVM{}
	for i := 0; i < 300; i++ {
		id := fmt.Sprintf("s%03d", i)
		m := session.Meta{ProjectID: "p", LastActive: base.Add(time.Duration(i*37%300) * time.Minute)}
		if i%10 == 0 {
			m.ProjectID = "other" // filtered out
		}
		if i%29 == 0 {
			m.Status = "queued"
		}
		sessions[id] = session.Session{Meta: m}
		switch i % 17 {
		case 3: // pool saw it long after its last save
			lc[id] = view.SessionLifecycleVM{Lifecycle: "idle", LastActiveMs: base.Add(900 * time.Minute).UnixMilli()}
		case 5:
			lc[id] = view.SessionLifecycleVM{Lifecycle: "working"}
		}
	}
	ordered := make([]string, 0, len(sessions))
	for id := range sessions {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := sessions[ordered[i]].Meta.LastActive, sessions[ordered[j]].Meta.LastActive
		if !a.Equal(b) {
			return a.After(b)
		}
		return ordered[i] < ordered[j]
	})
	keep := func(_ string, s session.Session) bool { return s.Meta.ProjectID == "p" }

	var all []string
	for _, id := range ordered {
		if keep(id, sessions[id]) {
			all = append(all, id)
		}
	}
	full := orderSidebarIDs(all, sessions, lc)

	for _, tc := range []struct{ offset, limit int }{{0, 25}, {25, 25}, {50, 25}, {0, 200}, {260, 25}, {400, 25}} {
		got, total := pageLooseSessions(ordered, sessions, lc, keep, tc.offset, tc.limit, true)
		if total != len(all) {
			t.Fatalf("total = %d, want %d", total, len(all))
		}
		var want []string
		if tc.offset < len(full) {
			end := tc.offset + tc.limit
			if end > len(full) {
				end = len(full)
			}
			want = full[tc.offset:end]
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("offset=%d limit=%d:\n got %v\nwant %v", tc.offset, tc.limit, got, want)
		}
	}

	// Rows not asked for: the count still arrives, nothing else is built.
	got, total := pageLooseSessions(ordered, sessions, lc, keep, 0, 25, false)
	if got != nil || total != len(all) {
		t.Fatalf("wantRows=false: got %v total %d", got, total)
	}
}
