package agents

import (
	"context"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"

	wf "github.com/yogasw/wick/internal/agents/workflow"
	"github.com/yogasw/wick/internal/agents/workflow/mcp"
	"github.com/yogasw/wick/internal/agents/workflow/parse"
	"github.com/yogasw/wick/internal/agents/workflow/setup"
	wfview "github.com/yogasw/wick/internal/tools/agents/view/workflow"
	"github.com/yogasw/wick/pkg/tool"
)

// globalWorkflowMgr is the wired workflow stack. Server.go calls
// SetWorkflowManager once at boot; nil = every workflows handler 503s.
var globalWorkflowMgr *setup.Manager

// SetWorkflowManager wires in the workflow Manager constructed by
// server.go.
func SetWorkflowManager(m *setup.Manager) { globalWorkflowMgr = m }

func notReadyWorkflow(c *tool.Ctx) bool {
	if globalWorkflowMgr == nil {
		c.Error(http.StatusServiceUnavailable, "workflows not initialised — check server boot logs")
		return true
	}
	return false
}

// ── List + Create ───────────────────────────────────────────────────

func workflowsPage(c *tool.Ctx) {
	if notReadyWorkflow(c) {
		return
	}
	summaries, err := globalWorkflowMgr.MCP.List()
	if err != nil {
		c.Error(http.StatusInternalServerError, err.Error())
		return
	}
	c.HTML(wfview.List(wfview.ListVM{
		Layout:    sidebarVM(c, "workflows", ""),
		Base:      c.Base(),
		Workflows: summaries,
	}))
}

func createWorkflow(c *tool.Ctx) {
	if notReadyWorkflow(c) {
		return
	}
	slug := strings.TrimSpace(c.Form("slug"))
	template := strings.TrimSpace(c.Form("template"))
	if template == "" {
		template = "empty"
	}
	w, err := globalWorkflowMgr.MCP.Create(mcp.CreateInput{Slug: slug, Template: template})
	if err != nil {
		log.Ctx(c.Context()).Error().Msgf("create workflow %s: %s", slug, err.Error())
		c.Error(http.StatusInternalServerError, err.Error())
		return
	}
	// Register the fresh workflow with the router so Run Now / triggers
	// work without a manual restart. Bootstrap only registers existing
	// folders at startup — first-time Create needs an explicit reload.
	_ = setup.HotReload(context.Background(), globalWorkflowMgr.Service, globalWorkflowMgr.Router, w.Slug)
	c.Redirect(c.Base()+"/workflows/edit/"+w.Slug, http.StatusSeeOther)
}

// ── Editor + CRUD ──────────────────────────────────────────────────

func workflowEditor(c *tool.Ctx) {
	if notReadyWorkflow(c) {
		return
	}
	slug := c.PathValue("slug")
	w, err := globalWorkflowMgr.MCP.Get(slug)
	if err != nil {
		c.NotFound()
		return
	}
	yamlBytes, _ := parse.Marshal(w)
	graphJSON, err := workflowToDrawflowJSON(w)
	if err != nil {
		log.Ctx(c.Context()).Warn().Msgf("graph json serialize: %s", err.Error())
		graphJSON = "{}"
	}
	report := globalWorkflowMgr.Guard.Review(c.Context(), w)
	runs, _ := globalWorkflowMgr.MCP.GetRuns(slug, 20)
	approved := false
	if st, err := globalWorkflowMgr.Service.LoadState(slug); err == nil {
		approved = st.Approved
	}

	c.HTML(wfview.Editor(wfview.EditorVM{
		Layout:      sidebarVM(c, "workflows", ""),
		Base:        c.Base(),
		Slug:        slug,
		Workflow:    w,
		YAML:        string(yamlBytes),
		GraphJSON:   graphJSON,
		Approved:    approved,
		GuardReport: &report,
		NodeTypes:   globalWorkflowMgr.MCP.NodeTypes(),
		Runs:        runs,
	}))
}

func saveWorkflow(c *tool.Ctx) {
	if notReadyWorkflow(c) {
		return
	}
	slug := c.PathValue("slug")
	body := c.Form("body")
	w, err := drawflowJSONToWorkflow(slug, body)
	if err != nil {
		c.Error(http.StatusBadRequest, "invalid graph payload: "+err.Error())
		return
	}
	// Carry forward triggers + entry from disk — the canvas only edits
	// nodes + edges, so refreshing the trigger list each save would
	// drop user-configured cron/channel/webhook bindings.
	if prev, err := globalWorkflowMgr.MCP.Get(slug); err == nil {
		w.Triggers = prev.Triggers
		w.Enabled = prev.Enabled
		w.Name = prev.Name
		w.Description = prev.Description
		w.Env = prev.Env
		w.Datasets = prev.Datasets
		w.OnError = prev.OnError
		if w.Graph.Entry == "" {
			w.Graph.Entry = prev.Graph.Entry
		}
	}
	if r := parse.Validate(w); !r.Ok() {
		c.Error(http.StatusBadRequest, "validation failed:\n"+r.Error())
		return
	}
	if err := globalWorkflowMgr.Service.Update(slug, w, nil); err != nil {
		log.Ctx(c.Context()).Error().Msgf("save workflow %s: %s", slug, err.Error())
		c.Error(http.StatusInternalServerError, err.Error())
		return
	}
	_ = setup.HotReload(context.Background(), globalWorkflowMgr.Service, globalWorkflowMgr.Router, slug)
	c.Redirect(c.Base()+"/workflows/edit/"+slug, http.StatusSeeOther)
}

func toggleWorkflow(c *tool.Ctx) {
	if notReadyWorkflow(c) {
		return
	}
	slug := c.PathValue("slug")
	w, err := globalWorkflowMgr.MCP.Get(slug)
	if err != nil {
		c.NotFound()
		return
	}
	if _, err := globalWorkflowMgr.MCP.Toggle(slug, !w.Enabled); err != nil {
		c.Error(http.StatusInternalServerError, err.Error())
		return
	}
	_ = setup.HotReload(context.Background(), globalWorkflowMgr.Service, globalWorkflowMgr.Router, slug)
	c.Redirect(c.Base()+"/workflows/edit/"+slug, http.StatusSeeOther)
}

func runWorkflowNow(c *tool.Ctx) {
	if notReadyWorkflow(c) {
		return
	}
	slug := c.PathValue("slug")
	w, err := globalWorkflowMgr.MCP.Get(slug)
	if err != nil {
		c.NotFound()
		return
	}
	// Defensive HotReload — covers the case where boot saw an empty
	// workflows/ dir and never registered this slug.
	_ = setup.HotReload(context.Background(), globalWorkflowMgr.Service, globalWorkflowMgr.Router, slug)
	report := globalWorkflowMgr.Guard.Review(c.Context(), w)
	if err := globalWorkflowMgr.Guard.Apply(report, nil); err != nil {
		c.Error(http.StatusForbidden, err.Error())
		return
	}
	if err := globalWorkflowMgr.MCP.RunNow(c.Context(), slug, wf.Event{Type: string(wf.TriggerManual)}); err != nil {
		c.Error(http.StatusInternalServerError, err.Error())
		return
	}
	c.Redirect(c.Base()+"/workflows/edit/"+slug, http.StatusSeeOther)
}

func deleteWorkflow(c *tool.Ctx) {
	if notReadyWorkflow(c) {
		return
	}
	slug := c.PathValue("slug")
	if err := globalWorkflowMgr.MCP.Delete(slug); err != nil {
		c.Error(http.StatusInternalServerError, err.Error())
		return
	}
	c.Redirect(c.Base()+"/workflows", http.StatusSeeOther)
}

// ── Run detail ────────────────────────────────────────────────────

func workflowRunDetail(c *tool.Ctx) {
	if notReadyWorkflow(c) {
		return
	}
	slug := c.PathValue("slug")
	runID := c.PathValue("runID")
	st, err := globalWorkflowMgr.StateStore.Load(slug, runID)
	if err != nil {
		c.NotFound()
		return
	}
	events, _ := globalWorkflowMgr.StateStore.ListEvents(slug, runID)
	c.HTML(wfview.Run(wfview.RunVM{
		Layout: sidebarVM(c, "workflows", ""),
		Base:   c.Base(),
		Slug:   slug,
		RunID:  runID,
		State:  st,
		Events: events,
	}))
}
