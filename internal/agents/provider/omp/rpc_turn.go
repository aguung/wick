package omp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/yogasw/wick/internal/agents/provider/cliserver"
)

// rpc_turn.go is one turn on a session's omp RPC process, shaped as a
// provider.Process so the pool and event.OMPParser see what they see for
// `omp -p --mode json`: Stdout() yields the same JSON lines (a session
// header, then the AgentSessionEvents), Wait() returns once the prompt's
// work has settled.
//
// Inject = `steer` (the message joins the run that is going, as with
// claude typed mid-turn). Kill = `abort`: the process stays up for the
// session's next turn, whatever was queued inside omp goes with the abort.
// Pid() is 0: the agent's teardown signals the process GROUP of Pid(), and
// that group is the RPC process the next turn reuses.

const (
	// DefaultServerIdle is the idle window when the instance sets none.
	DefaultServerIdle = cliserver.DefaultIdle
	// rpcMaxAge retires a process before the MCP token baked into its env
	// (12 h TTL) can run out under a session that never goes idle.
	rpcMaxAge = 6 * time.Hour
	killGrace = 3 * time.Second
	// mcpRevokeGrace mirrors pool.mcpRevokeGrace: a tool call can still be
	// in flight when the process goes.
	mcpRevokeGrace = 90 * time.Second
)

var rpcServers = newRPCManager()

func newRPCManager() *cliserver.Manager[*rpcConn] {
	m := cliserver.New[*rpcConn]("omp", 1)
	m.OnStop = func(_ string, c *rpcConn) { revokeLater(c.token) }
	return m
}

// ShutdownServers kills every omp RPC process (wick shutdown / upgrade).
func ShutdownServers() { rpcServers.Shutdown() }

// revoker is the pool's RevokeMCPToken (SetMCPTokenRevoker); nil = tokens
// just expire at their TTL.
var (
	revokerMu sync.Mutex
	revoker   func(string)
)

// SetMCPTokenRevoker wires the per-session MCP token revocation. omp owns
// its tokens' lifetime because in server mode a token lives as long as the
// RPC process that baked it into its env, not as long as one turn.
func SetMCPTokenRevoker(fn func(string)) {
	revokerMu.Lock()
	revoker = fn
	revokerMu.Unlock()
}

func revokeLater(tok string) {
	revokerMu.Lock()
	fn := revoker
	revokerMu.Unlock()
	if fn == nil || tok == "" {
		return
	}
	time.AfterFunc(mcpRevokeGrace, func() { fn(tok) })
}

// rpcControlFrames are protocol frames with no `-p --mode json` twin.
var rpcControlFrames = map[string]bool{
	"response": true, "ready": true, "prompt_result": true, "session_settled": true,
	"rpc_chunk": true, "available_commands_update": true, "extension_ui_request": true,
	"host_tool_call": true, "host_tool_cancel": true, "host_uri_request": true, "host_uri_cancel": true,
	"subagent_lifecycle": true, "subagent_progress": true, "subagent_event": true,
	"command_output": true, "session_info_update": true, "config_update": true,
}

// translateFrame turns one RPC event frame into the line `-p --mode json`
// prints for it (print-mode.ts printableEvent); ok=false drops it.
func translateFrame(line []byte) ([]byte, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(line, &m) != nil {
		return nil, false
	}
	var typ string
	_ = json.Unmarshal(m["type"], &typ)
	if typ == "" || rpcControlFrames[typ] {
		return nil, false
	}
	delete(m, "messageId") // RPC stamps message frames; print mode does not
	switch typ {
	case "message_update":
		// The streamed snapshot (message, partial) is dropped: only the
		// delta matters, the full message follows in message_end.
		delete(m, "message")
		var ev map[string]json.RawMessage
		if json.Unmarshal(m["assistantMessageEvent"], &ev) == nil {
			var et string
			_ = json.Unmarshal(ev["type"], &et)
			if et == "done" || et == "error" {
				ev = map[string]json.RawMessage{"type": ev["type"], "reason": ev["reason"]}
			} else {
				delete(ev, "partial")
			}
			m["assistantMessageEvent"], _ = json.Marshal(ev)
		}
	case "tool_stream_update":
		m = map[string]json.RawMessage{"type": m["type"], "toolCallId": m["toolCallId"], "toolName": m["toolName"]}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, false
	}
	return append(b, '\n'), true
}

// headerLine is the `{"type":"session",...}` line print mode opens with;
// the parser reads the session id from it.
func headerLine(sessionID, cwd string) []byte {
	b, _ := json.Marshal(map[string]string{"type": "session", "id": sessionID, "cwd": cwd})
	return append(b, '\n')
}

// errorLines end a turn with err the way a failed `-p` run does: an
// assistant message that stopped on error, then the terminal agent_end.
func errorLines(msg string) []byte {
	a, _ := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{
		"role": "assistant", "stopReason": "error", "errorMessage": msg, "content": []any{}}})
	b, _ := json.Marshal(map[string]any{"type": "agent_end", "messages": []any{}})
	return append(append(append(a, '\n'), b...), '\n')
}

// errTurnKilled wraps context.Canceled: the agent reads that as a stop it
// asked for, not as a crash to recover from.
var errTurnKilled = fmt.Errorf("omp turn aborted: %w", context.Canceled)

// errTurnOver is Inject's answer once the turn cannot take a message.
var errTurnOver = errors.New("omp turn is over")

// rpcTurnSpec is what one turn sends.
type rpcTurnSpec struct {
	prompt string
	cwd    string
	// fresh: the wick session has no omp session yet (no resume id) but
	// the process already served one — start a new omp session first.
	fresh bool
}

// rpcProcess implements provider.Process (and provider.Injector) for one turn.
type rpcProcess struct {
	pr     *io.PipeReader
	pw     *io.PipeWriter
	env    []string
	bin    string
	argv   []string
	done   chan struct{}
	cancel context.CancelFunc

	mu       sync.Mutex
	err      error
	conn     *rpcConn
	prompted bool
	killed   bool
	finished bool
	early    []string

	// injMu orders the first prompt and every steer.
	injMu sync.Mutex
}

func newRPCProcess(env []string, bin string, argv []string) *rpcProcess {
	pr, pw := io.Pipe()
	return &rpcProcess{pr: pr, pw: pw, env: env, bin: bin, argv: argv, done: make(chan struct{})}
}

func (p *rpcProcess) Stdout() io.Reader     { return p.pr }
func (p *rpcProcess) Stdin() io.WriteCloser { return noopWriteCloser{} }
func (p *rpcProcess) Env() []string         { return p.env }
func (p *rpcProcess) Pid() int              { return 0 }
func (p *rpcProcess) Binary() string        { return p.bin }
func (p *rpcProcess) Argv() []string        { return append([]string(nil), p.argv...) }

func (p *rpcProcess) Wait() error {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// Kill aborts the turn and returns once the abort is through (the next
// turn prompts the same omp session; an abort landing after that prompt
// would abort the new turn instead). The pipe is closed first: whoever
// kills a turn has stopped reading it.
func (p *rpcProcess) Kill() error {
	p.mu.Lock()
	if p.killed {
		p.mu.Unlock()
		return nil
	}
	p.killed = true
	c, prompted, finished := p.conn, p.prompted, p.finished
	p.mu.Unlock()
	_ = p.pr.CloseWithError(errTurnKilled)
	if finished {
		return nil
	}
	if !prompted || c == nil {
		p.cancel()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), rpcCallWait)
	defer cancel()
	if _, err := c.call(ctx, map[string]any{"type": "abort"}); err != nil {
		log.Warn().Err(err).Msg("agents.omp: abort failed; ending the turn")
	}
	select {
	case <-p.done:
	case <-time.After(killGrace):
		p.cancel()
	}
	return nil
}

// Inject steers the running turn with text: omp delivers it to the run
// that is going (rpc steer → session.steer). Before the prompt is out the
// text rides along in it.
func (p *rpcProcess) Inject(text string) error {
	p.injMu.Lock()
	defer p.injMu.Unlock()
	p.mu.Lock()
	if p.killed || p.finished {
		p.mu.Unlock()
		return errTurnOver
	}
	if !p.prompted {
		p.early = append(p.early, text)
		p.mu.Unlock()
		return nil
	}
	c := p.conn
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), rpcCallWait)
	defer cancel()
	_, err := c.call(ctx, map[string]any{"type": "steer", "message": text})
	return err
}

func (p *rpcProcess) emit(b []byte) { _, _ = p.pw.Write(b) }

func (p *rpcProcess) finish(err error) {
	p.mu.Lock()
	if p.err == nil {
		p.err = err
	}
	if p.killed {
		p.err = errTurnKilled
	}
	p.finished = true
	p.mu.Unlock()
	_ = p.pw.Close()
	close(p.done)
}

// run drives one turn to the end. It owns the lease.
func (p *rpcProcess) run(ctx context.Context, l *cliserver.Lease[*rpcConn], t rpcTurnSpec) {
	defer l.Release()
	err := p.turn(ctx, l, t)
	killed := func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.killed }()
	if err != nil && !killed {
		// Reported IN the turn, as its error frames: returning it from Wait
		// would read as an unexplained exit and the pool would "recover".
		p.emit(errorLines(err.Error()))
		err = nil
	}
	p.finish(err)
}

func (p *rpcProcess) turn(ctx context.Context, l *cliserver.Lease[*rpcConn], t rpcTurnSpec) error {
	if err := l.WaitSlot(ctx); err != nil {
		return err
	}
	c := l.H
	if t.fresh {
		if _, err := c.call(ctx, map[string]any{"type": "new_session"}); err != nil {
			return err
		}
	}
	st, err := c.call(ctx, map[string]any{"type": "get_state"})
	if err != nil {
		return err
	}
	var state struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(st.Data, &state)
	c.mu.Lock()
	c.sessionID = state.SessionID
	c.turns++
	c.mu.Unlock()

	// Frames of this turn, in order; the reader goroutine never blocks on
	// a slow consumer for long (buffered), and emit is the only sink.
	frames := make(chan []byte, 256)
	results := make(chan rpcFrame, 4)
	unsub := c.subscribe(func(line []byte, f rpcFrame) {
		switch f.Type {
		case "prompt_result", "session_settled":
			results <- f
		default:
			select {
			case frames <- line:
			case <-p.done:
			}
		}
	})
	defer unsub()
	p.emit(headerLine(state.SessionID, t.cwd))

	p.injMu.Lock()
	p.mu.Lock()
	p.conn = c
	if len(p.early) > 0 {
		t.prompt = strings.Join(append([]string{t.prompt}, p.early...), "\n\n")
		p.early = nil
	}
	p.mu.Unlock()
	// streamingBehavior steer: if a run is somehow still going (a turn
	// killed while its abort was in flight) the prompt joins it rather
	// than failing with "already streaming".
	id, ch, err := c.send(map[string]any{"type": "prompt", "message": t.prompt, "streamingBehavior": "steer"})
	if err == nil {
		select {
		case f := <-ch:
			if !f.Success {
				err = fmt.Errorf("omp rpc prompt: %s", f.errText())
			}
		case <-c.done:
			err = errors.New("omp rpc process exited")
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	if err != nil {
		p.injMu.Unlock()
		return err
	}
	p.mu.Lock()
	p.prompted = true
	killed := p.killed
	p.mu.Unlock()
	p.injMu.Unlock()
	if killed {
		_, _ = c.call(context.Background(), map[string]any{"type": "abort"})
	}

	sawEnd := false
	var result *rpcFrame
	for {
		select {
		case line := <-frames:
			out, ok := translateFrame(line)
			if !ok {
				continue
			}
			if strings.Contains(string(out), `"type":"agent_end"`) {
				sawEnd = true
			}
			p.emit(out)
			continue
		case f := <-results:
			if f.Type == "prompt_result" && f.ID == id {
				r := f
				result = &r
				if !f.SessionSettled {
					continue // background work may still wake it; session_settled follows
				}
			} else if f.Type != "session_settled" || result == nil {
				continue
			}
		case <-c.done:
			return errors.New("omp rpc process exited mid-turn")
		case <-ctx.Done():
			return ctx.Err()
		}
		break
	}
	// Flush frames that arrived before the result.
	for {
		select {
		case line := <-frames:
			if out, ok := translateFrame(line); ok {
				if strings.Contains(string(out), `"type":"agent_end"`) {
					sawEnd = true
				}
				p.emit(out)
			}
			continue
		default:
		}
		break
	}
	p.mu.Lock()
	p.finished = true
	p.mu.Unlock()
	if result.Status == "error" && !sawEnd {
		// Failed before the agent ran (model unavailable, not logged in):
		// no agent_end carried the error, so say it the parser's way.
		msg := result.errText()
		if msg == "" {
			msg = "omp prompt failed"
		}
		p.emit(errorLines(msg))
	}
	return nil
}
