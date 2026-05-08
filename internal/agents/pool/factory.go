package pool

import (
	"github.com/yogasw/wick/internal/agents/agent"
	"github.com/yogasw/wick/internal/agents/agent/claude"
	"github.com/yogasw/wick/internal/agents/config"
	"github.com/yogasw/wick/internal/agents/event"
	"github.com/yogasw/wick/internal/agents/state"
	"github.com/yogasw/wick/internal/agents/store"
)

// ClaudeFactory is the production AgentFactory: wires a ClaudeParser +
// ClaudeSpawner into a fresh agent.Agent for each Build call.
//
// The factory owns no per-spawn state; the pool calls Build once per
// session activation.
type ClaudeFactory struct {
	Layout    config.Layout
	Spawner   agent.Spawner // optional override; nil = real claude
	RecordRaw bool
	OnEvent   func(sessionID, agentName string, ev event.AgentEvent)
	OnExit    func(sessionID, agentName string, reason agent.ExitReason)
}

// Build returns a fresh agent + state machine + store wired for one
// session+agent. Caller (the pool) is responsible for calling
// agent.Start.
func (f *ClaudeFactory) Build(opt FactoryOptions) (*agent.Agent, *state.Machine, *store.Store, error) {
	st := state.New(nil)
	sto := store.New(store.Options{
		Layout:    f.Layout,
		SessionID: opt.SessionID,
		AgentName: opt.AgentName,
		RecordRaw: f.RecordRaw,
	})

	spawner := f.Spawner
	if spawner == nil {
		spawner = claude.Spawner{}
	}

	var onEvent func(event.AgentEvent)
	if f.OnEvent != nil {
		sid, name := opt.SessionID, opt.AgentName
		onEvent = func(ev event.AgentEvent) { f.OnEvent(sid, name, ev) }
	}
	var onExit func(agent.ExitReason)
	if f.OnExit != nil {
		sid, name := opt.SessionID, opt.AgentName
		onExit = func(r agent.ExitReason) { f.OnExit(sid, name, r) }
	}

	a := agent.New(agent.Options{
		Workspace:     opt.Workspace,
		ResumeID:      opt.ResumeID,
		IdleTimeout:   opt.IdleTimeout,
		ParserFactory: func() event.Parser { return event.NewClaudeParser() },
		Spawner:       spawner,
		Store:         sto,
		State:         st,
		OnEvent:       onEvent,
		OnExit:        onExit,
	})
	return a, st, sto, nil
}
