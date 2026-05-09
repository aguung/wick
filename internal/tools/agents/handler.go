// Package agents implements the Agents manager tool. It mounts at
// /tools/agents and provides a web UI for creating sessions, sending
// messages to Claude subprocesses, and watching responses in real-time
// via SSE.
//
// Caller: internal/tools/registry.RegisterBuiltins → tool.Module.Register
// Dependencies: pool.Pool, registry.Manager, hub (SSE broadcaster)
// Main functions: New, Register, overview, sessions, sessionDetail, send, stream
// Side effects: creates ~/.wick/agents/ layout on first call to New
package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	agentscfg "github.com/yogasw/wick/internal/agents/config"
	"github.com/yogasw/wick/internal/agents/event"
	"github.com/yogasw/wick/internal/agents/pool"
	"github.com/yogasw/wick/internal/agents/registry"
	"github.com/yogasw/wick/internal/agents/session"
	"github.com/yogasw/wick/internal/agents/store"
	"github.com/yogasw/wick/pkg/tool"
)

// Handler holds the runtime dependencies shared across all route handlers.
type Handler struct {
	pool   *pool.Pool
	mgr    *registry.Manager
	hub    *Hub
	layout agentscfg.Layout
}

// New bootstraps the agents layout, registry, and pool, then returns a
// Handler ready to be passed to tool.Module.Register.
func New() *Handler {
	layout := agentscfg.NewLayout(
		agentscfg.ResolveBaseDir(agentscfg.DefaultWorkspaceConfig()),
	)
	mgr, err := registry.Bootstrap(layout)
	if err != nil {
		log.Warn().Err(err).Msg("agents: registry bootstrap failed — tool will be limited")
	}

	hub := newHub()

	factory := &pool.ClaudeFactory{
		Layout:    layout,
		RecordRaw: false,
		OnEvent: func(sessionID, _ string, ev event.AgentEvent) {
			var data string
			switch ev.Type {
			case event.TextDelta:
				b, _ := json.Marshal(map[string]string{"type": "text_delta", "text": ev.Text})
				data = string(b)
			case event.Done:
				data = `{"type":"done"}`
			case event.Error:
				b, _ := json.Marshal(map[string]string{"type": "error", "text": ev.ErrorMsg})
				data = string(b)
			default:
				return
			}
			hub.Publish(sessionID, data)
		},
	}

	p := pool.New(pool.PoolConfig{
		MaxConcurrent: 2,
		IdleTimeout:   120 * time.Second,
		Layout:        layout,
		Factory:       factory,
	})
	// Wire the exit hook after pool is created to avoid circular dep.
	factory.OnExit = p.HandleExit

	return &Handler{pool: p, mgr: mgr, hub: hub, layout: layout}
}

// Register wires the agents tool routes on r. Called once at server boot
// by the tool router.
func (h *Handler) Register(r tool.Router) {
	r.GET("/", h.overview)
	r.GET("/sessions", h.sessions)
	r.GET("/sessions/{id}", h.sessionDetail)
	r.POST("/sessions", h.createSession)
	r.POST("/sessions/{id}/send", h.send)
	r.POST("/sessions/{id}/delete", h.deleteSession)
	r.GET("/stream", h.stream)
	r.Static("/static/", StaticFS)
}

// ── Handlers ────────────────────────────────────────────────────────────

func (h *Handler) overview(c *tool.Ctx) {
	var allSessions []session.Session
	if h.mgr != nil {
		for _, id := range h.mgr.Registry().SessionIDs() {
			s, ok := h.mgr.Registry().Session(id)
			if ok {
				allSessions = append(allSessions, s)
			}
		}
	}
	recent := allSessions
	if len(recent) > 10 {
		recent = recent[:10]
	}
	c.HTML(OverviewPage(c.Base(), h.pool.Active(), h.pool.QueueLen(), len(allSessions), recent))
}

func (h *Handler) sessions(c *tool.Ctx) {
	var all []session.Session
	if h.mgr != nil {
		for _, id := range h.mgr.Registry().SessionIDs() {
			s, ok := h.mgr.Registry().Session(id)
			if ok {
				all = append(all, s)
			}
		}
	}
	c.HTML(SessionsPage(c.Base(), all))
}

func (h *Handler) sessionDetail(c *tool.Ctx) {
	id := c.PathValue("id")
	if id == "" || h.mgr == nil {
		c.NotFound()
		return
	}
	// Refresh from disk in case status changed.
	sess, err := session.Load(h.layout, id)
	if err != nil {
		c.NotFound()
		return
	}
	turns := readConversation(h.layout.SessionConversation(id))
	c.HTML(SessionDetailPage(c.Base(), sess, turns))
}

func (h *Handler) createSession(c *tool.Ctx) {
	if h.mgr == nil {
		c.Error(http.StatusInternalServerError, "agents registry unavailable")
		return
	}
	id := uuid.New().String()
	_, err := session.Create(c.Context(), h.layout, session.CreateOptions{
		ID:     id,
		Origin: session.OriginUI,
	})
	if err != nil {
		log.Error().Err(err).Msg("agents: create session")
		c.Error(http.StatusInternalServerError, "failed to create session")
		return
	}
	// Ensure workspace dir exists — claude spawn sets cmd.Dir to this
	// path and fails with ENOENT when it's missing (no project attached).
	wsDir := filepath.Join(h.layout.SessionDir(id), "workspace")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		log.Warn().Err(err).Str("session", id).Msg("agents: mkdir workspace")
	}
	// Refresh in-memory registry.
	_ = h.mgr.Registry().Reload()
	c.Redirect(c.Base()+"/sessions/"+id, http.StatusSeeOther)
}

type sendRequest struct {
	Text      string `json:"text"`
	AgentName string `json:"agent_name"`
}

func (h *Handler) send(c *tool.Ctx) {
	id := c.PathValue("id")
	if id == "" {
		c.Error(http.StatusBadRequest, "missing session id")
		return
	}
	var req sendRequest
	if err := c.BindJSON(&req); err != nil {
		c.Error(http.StatusBadRequest, "invalid JSON")
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		c.Error(http.StatusBadRequest, "empty message")
		return
	}
	agentName := req.AgentName
	if agentName == "" {
		agentName = "default"
	}

	// Ensure workspace dir exists before handing off to pool — claude
	// spawn will chdir there and fail with ENOENT otherwise.
	wsDir := filepath.Join(h.layout.SessionDir(id), "workspace")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		log.Warn().Err(err).Str("session", id).Msg("agents: mkdir workspace on send")
	}

	// Use context.Background — the subprocess must outlive this request.
	// Passing c.Context() would kill the claude process the instant the
	// HTTP handler returns (Go cancels the request context on ServeHTTP
	// return), leaving "Waiting for agent…" forever on the client.
	if err := h.pool.Send(context.Background(), id, agentName, "ui", "user", text); err != nil {
		log.Error().Err(err).Str("session", id).Msg("agents: pool.Send")
		c.Error(http.StatusInternalServerError, fmt.Sprintf("send failed: %s", err.Error()))
		return
	}
	c.JSON(http.StatusAccepted, map[string]string{"status": "queued"})
}

func (h *Handler) deleteSession(c *tool.Ctx) {
	id := c.PathValue("id")
	if id == "" || h.mgr == nil {
		c.NotFound()
		return
	}
	if err := session.Delete(c.Context(), h.layout, id); err != nil {
		log.Error().Err(err).Str("session", id).Msg("agents: delete session")
	}
	_ = h.mgr.Registry().Reload()
	c.Redirect(c.Base()+"/sessions", http.StatusSeeOther)
}

// stream is the SSE endpoint. Clients connect once per session detail
// page and receive events until they disconnect or the server shuts down.
//
// Uses http.NewResponseController instead of a direct http.Flusher
// assertion so it works even when the ResponseWriter is wrapped by
// logging or auth middleware (which is always the case here).
func (h *Handler) stream(c *tool.Ctx) {
	sessionID := c.Query("session")
	if sessionID == "" {
		c.Error(http.StatusBadRequest, "missing ?session=")
		return
	}

	w := c.W
	rc := http.NewResponseController(w)
	// Remove the server-level write deadline so the long-lived SSE
	// connection isn't killed by the 60 s WriteTimeout.
	_ = rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsub := h.hub.Subscribe(sessionID)
	defer unsub()

	// Keepalive comment so the browser sees the connection as open.
	fmt.Fprintf(w, ": connected\n\n")
	_ = rc.Flush()

	ctx := c.Context()
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case data, open := <-ch:
			if !open {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			_ = rc.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			_ = rc.Flush()
		}
	}
}

// ── Helpers ─────────────────────────────────────────────────────────────

// readConversation reads conversation.jsonl and returns the turns in
// order. Silently returns empty on any error.
func readConversation(path string) []store.ConversationTurn {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var turns []store.ConversationTurn
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var t store.ConversationTurn
		if json.Unmarshal(line, &t) == nil {
			turns = append(turns, t)
		}
	}
	return turns
}
