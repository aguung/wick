package agents

import (
	"sort"

	"github.com/yogasw/wick/internal/agents/session"
	"github.com/yogasw/wick/internal/tools/agents/view"
)

// orderSidebarIDs puts running rows first (working / spawning / a working
// sub-agent / queued), then everything else by last use, newest first.
//
// It runs BEFORE the sidebar cap on purpose: "which one is running" is the
// question the sidebar is scanned for, and a busy session that happened to
// be touched eleventh would otherwise be cut off the list entirely.
//
// The registry already hands ids over LastActive-descending, but only by
// the persisted meta; the pool's in-memory LastActive is fresher mid-turn,
// so the age is recomputed from both. MIRRORED client-side by
// fe/agents/shell/src/sidebarOrder.ts (sortRows) for live re-sorts.
func orderSidebarIDs(ids []string, sessions map[string]session.Session, lc map[string]view.SessionLifecycleVM) []string {
	type row struct {
		id      string
		running bool
		at      int64
	}
	rows := make([]row, 0, len(ids))
	for _, id := range ids {
		sess := sessions[id]
		l := lc[id]
		rows = append(rows, row{
			id:      id,
			running: view.IsRunningStatus(view.SidebarRowStatus(sess, l)),
			at:      view.SidebarLastActiveMs(sess, l),
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].running != rows[j].running {
			return rows[i].running
		}
		return rows[i].at > rows[j].at
	})
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.id
	}
	return out
}
