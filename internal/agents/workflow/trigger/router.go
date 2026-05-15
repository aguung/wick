package trigger

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/yogasw/wick/internal/agents/workflow"
	"github.com/yogasw/wick/internal/agents/workflow/engine"
	"github.com/yogasw/wick/internal/agents/workflow/service"
)

// Router matches incoming events to registered workflows, applies
// dedup, and enqueues per workflow.
type Router struct {
	mu      sync.RWMutex
	engine  *engine.Engine
	service service.Service
	defs    map[string]workflow.Workflow
	queues  map[string]*Queue
	dedups  map[string]*Dedup
	workers map[string]context.CancelFunc
	wg      sync.WaitGroup
	clock   func() time.Time
}

// NewRouter wires a Router to an Engine + Service.
func NewRouter(e *engine.Engine, svc service.Service) *Router {
	return &Router{
		engine:  e,
		service: svc,
		defs:    map[string]workflow.Workflow{},
		queues:  map[string]*Queue{},
		dedups:  map[string]*Dedup{},
		workers: map[string]context.CancelFunc{},
		clock:   func() time.Time { return time.Now() },
	}
}

// Register adds a workflow to the router and spawns its worker goroutine.
func (r *Router) Register(ctx context.Context, w workflow.Workflow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defs[w.Slug] = w
	if _, ok := r.queues[w.Slug]; !ok {
		max := w.Queue.MaxSize
		if max == 0 {
			max = 20
		}
		policy := w.Queue.OnOverflow
		if policy == "" {
			policy = workflow.OverflowDropOldest
		}
		r.queues[w.Slug] = NewQueue(max, policy)
		dedupTTL := 24 * time.Hour
		if t := firstChannelDedupTTL(w); t > 0 {
			dedupTTL = time.Duration(t) * time.Second
		}
		r.dedups[w.Slug] = NewDedup(1024, dedupTTL)
		wctx, cancel := context.WithCancel(ctx)
		r.workers[w.Slug] = cancel
		r.wg.Add(1)
		go r.runWorker(wctx, w.Slug)
	}
}

// Unregister stops the worker for slug and frees its queue.
func (r *Router) Unregister(slug string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cancel, ok := r.workers[slug]; ok {
		cancel()
		delete(r.workers, slug)
	}
	if q, ok := r.queues[slug]; ok {
		q.Close()
		delete(r.queues, slug)
	}
	delete(r.dedups, slug)
	delete(r.defs, slug)
}

// RunNow enqueues a manual run for one explicit slug, bypassing
// Enabled + trigger-match checks. Used by the UI Run-Now button so
// admins can fire a disabled workflow (e.g. dry-run before enable)
// without going through the Dispatch matcher.
//
// Returns an error if the workflow isn't registered with the router
// (caller should HotReload first).
func (r *Router) RunNow(ctx context.Context, slug string, evt workflow.Event) error {
	return r.RunNowWith(ctx, slug, nil, evt)
}

// RunNowWith is RunNow with an explicit workflow override. Pass a
// non-nil `w` to execute that exact definition (typically the
// draft loaded from disk) instead of the published copy registered
// in Router.defs. The router still owns the per-slug queue + worker
// machinery — the override only affects which Workflow value the
// engine receives.
//
// When `w` is nil, behaviour is identical to RunNow.
func (r *Router) RunNowWith(ctx context.Context, slug string, w *workflow.Workflow, evt workflow.Event) error {
	r.mu.RLock()
	_, registered := r.defs[slug]
	q := r.queues[slug]
	r.mu.RUnlock()
	if q == nil {
		// No queue means the workflow was never registered AND no
		// override is enough to spin one up on the fly. Caller should
		// HotReload first (which builds the queue + worker).
		return fmt.Errorf("workflow %q has no router queue — register it first", slug)
	}
	// If neither override nor registered def exists, refuse — engine
	// would have nothing to walk.
	if w == nil && !registered {
		return fmt.Errorf("workflow %q not registered with router", slug)
	}
	return q.Enqueue(WorkItem{Slug: slug, Event: evt, Workflow: w})
}

// Dispatch matches an event to all workflows with a fitting trigger
// and enqueues. Returns the number of workflows that accepted the event.
func (r *Router) Dispatch(ctx context.Context, evt workflow.Event) int {
	r.mu.RLock()
	defs := make([]workflow.Workflow, 0, len(r.defs))
	for _, w := range r.defs {
		defs = append(defs, w)
	}
	r.mu.RUnlock()

	matched := 0
	for _, w := range defs {
		if !w.Enabled {
			continue
		}
		if !r.matchWorkflow(w, evt) {
			continue
		}
		if !r.passesDedup(w.Slug, evt) {
			continue
		}
		r.mu.RLock()
		q := r.queues[w.Slug]
		r.mu.RUnlock()
		if q == nil {
			continue
		}
		_ = q.Enqueue(WorkItem{Slug: w.Slug, Event: evt})
		matched++
	}
	return matched
}

func (r *Router) matchWorkflow(w workflow.Workflow, evt workflow.Event) bool {
	for _, tr := range w.Triggers {
		if MatchTrigger(tr, evt) {
			return true
		}
	}
	return false
}

// MatchTrigger checks one trigger spec against an event. Exported so
// router tests can drive it directly.
func MatchTrigger(tr workflow.Trigger, evt workflow.Event) bool {
	if string(tr.Type) != evt.Type {
		return false
	}
	switch tr.Type {
	case workflow.TriggerManual, workflow.TriggerCron, workflow.TriggerScheduleAt:
		return true
	case workflow.TriggerChannel:
		if tr.ChannelName != "" && tr.ChannelName != "*" && tr.ChannelName != evt.Channel && tr.ChannelName != channelFromPayload(evt) {
			return false
		}
		if tr.Event != "" && evt.Subtype != "" && tr.Event != evt.Subtype {
			return false
		}
		if tr.Target != "" && tr.Target != evt.Channel {
			return false
		}
		return matchChannelMatchRules(tr, evt)
	case workflow.TriggerWebhook:
		if tr.Path != "" {
			gotPath, _ := evt.Payload["path"].(string)
			if !PathMatches(tr.Path, gotPath) {
				return false
			}
		}
		if tr.Method != "" {
			gotMethod, _ := evt.Payload["method"].(string)
			if !strings.EqualFold(tr.Method, gotMethod) {
				return false
			}
		}
		return true
	case workflow.TriggerError:
		srcWF, _ := evt.Payload["source_workflow"].(string)
		if tr.SourceWorkflow != "" && tr.SourceWorkflow != "*" && tr.SourceWorkflow != srcWF {
			return false
		}
		if len(tr.Severity) > 0 {
			gotSeverity, _ := evt.Payload["severity"].(string)
			if !containsStr(tr.Severity, gotSeverity) {
				return false
			}
		}
		return true
	}
	return false
}

func matchChannelMatchRules(tr workflow.Trigger, evt workflow.Event) bool {
	if len(tr.Match) == 0 {
		return true
	}
	if kw, ok := tr.Match["keywords"].([]any); ok && len(kw) > 0 {
		lower := strings.ToLower(evt.Text)
		hit := false
		for _, k := range kw {
			if s, ok := k.(string); ok && strings.Contains(lower, strings.ToLower(s)) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if mb, ok := tr.Match["mention_bot"].(bool); ok && mb {
		mentioned, _ := evt.Payload["mention_bot"].(bool)
		if !mentioned {
			return false
		}
	}
	return true
}

func channelFromPayload(evt workflow.Event) string {
	if v, ok := evt.Payload["channel_module"].(string); ok {
		return v
	}
	return ""
}

// PathMatches compares a trigger path template against an actual
// request path. Supports `{param}` segments.
func PathMatches(tmpl, got string) bool {
	tParts := strings.Split(strings.Trim(tmpl, "/"), "/")
	gParts := strings.Split(strings.Trim(got, "/"), "/")
	if len(tParts) != len(gParts) {
		return false
	}
	for i, tp := range tParts {
		if strings.HasPrefix(tp, "{") && strings.HasSuffix(tp, "}") {
			continue
		}
		if tp != gParts[i] {
			return false
		}
	}
	return true
}

func (r *Router) passesDedup(slug string, evt workflow.Event) bool {
	r.mu.RLock()
	d := r.dedups[slug]
	r.mu.RUnlock()
	if d == nil {
		return true
	}
	key := dedupKey(evt)
	if key == "" {
		return true
	}
	return !d.Seen(slug + ":" + key)
}

func dedupKey(evt workflow.Event) string {
	if id, ok := evt.Payload["event_id"].(string); ok && id != "" {
		return evt.Channel + ":" + id
	}
	if id, ok := evt.Payload["message_id"].(string); ok && id != "" {
		return evt.Channel + ":" + id
	}
	return ""
}

func (r *Router) runWorker(ctx context.Context, slug string) {
	defer r.wg.Done()
	r.mu.RLock()
	q := r.queues[slug]
	r.mu.RUnlock()
	if q == nil {
		return
	}
	for {
		item, ok := q.Dequeue(ctx)
		if !ok {
			return
		}
		// Pick the workflow the engine should walk: explicit override
		// (Run Now with a freshly-loaded draft) wins over the
		// router's registered published copy.
		var w workflow.Workflow
		if item.Workflow != nil {
			w = *item.Workflow
		} else {
			r.mu.RLock()
			reg, defOK := r.defs[slug]
			r.mu.RUnlock()
			if !defOK {
				continue
			}
			w = reg
		}
		st, err := r.engine.Run(ctx, w, item.Event)
		if item.Done != nil {
			item.Done <- RunResult{State: st, Err: err}
		}
		if err != nil {
			_ = r.fireErrorWorkflow(ctx, w, st, err)
		}
	}
}

func (r *Router) fireErrorWorkflow(ctx context.Context, w workflow.Workflow, st workflow.RunState, runErr error) error {
	if w.OnError == nil || w.OnError.TriggerWorkflow == "" {
		return nil
	}
	depth := 0
	if d, ok := st.Event.Payload["error_depth"].(int); ok {
		depth = d
	}
	if depth >= 3 {
		return fmt.Errorf("error workflow chain depth %d exceeded", depth)
	}
	payload := map[string]any{
		"source_workflow": w.Slug,
		"source_run_id":   st.RunID,
		"error":           runErr.Error(),
		"severity":        w.OnError.Severity,
		"error_depth":     depth + 1,
	}
	if st.Error != nil {
		payload["failed_node"] = st.Error.Node
		payload["node_type"] = st.Error.Type
	}
	if w.OnError.IncludeState {
		payload["state_snapshot"] = st
	}
	if w.OnError.IncludeNodeOutput {
		payload["node_outputs"] = st.Outputs
	}
	errEvt := workflow.Event{Type: string(workflow.TriggerError), At: time.Now().UTC(), Payload: payload}
	r.Dispatch(ctx, errEvt)
	return nil
}

// Stop unregisters all and waits for workers to drain.
func (r *Router) Stop() {
	r.mu.Lock()
	slugs := make([]string, 0, len(r.workers))
	for s := range r.workers {
		slugs = append(slugs, s)
	}
	r.mu.Unlock()
	for _, s := range slugs {
		r.Unregister(s)
	}
	r.wg.Wait()
}

func firstChannelDedupTTL(w workflow.Workflow) int {
	for _, tr := range w.Triggers {
		if tr.Type == workflow.TriggerChannel && tr.DedupTTLSec > 0 {
			return tr.DedupTTLSec
		}
	}
	return 0
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
