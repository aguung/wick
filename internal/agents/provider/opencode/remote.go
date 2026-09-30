package opencode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// remote.go is one turn on a shared opencode server, shaped as a
// provider.Process so the pool and parser see what they see for `opencode
// run`: Stdout() yields run-format JSON lines (translate.go), Wait()
// returns once the session goes idle.
//
// Kill() aborts the opencode session (POST /session/:id/abort) and never
// touches the server, which other sessions are using. Pid() is 0 for the
// same reason: the agent's teardown signals the process GROUP of Pid(),
// and that group is the server.

const (
	abortTimeout = 5 * time.Second
	// killGrace is how long Kill waits for the aborted session to go idle
	// before it cuts the stream itself (the agent waits 5 s for EOF).
	killGrace = 3 * time.Second
)

// apiClient speaks the server's HTTP API for one directory.
type apiClient struct {
	base     string
	password string
	dir      string
	http     *http.Client
}

func (c *apiClient) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case []byte:
			rd = bytes.NewReader(b)
		default:
			j, err := json.Marshal(body)
			if err != nil {
				return err
			}
			rd = bytes.NewReader(j)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), rd)
	if err != nil {
		return err
	}
	req.SetBasicAuth(serverUser, c.password)
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &httpError{status: resp.StatusCode, method: method, path: path, body: strings.TrimSpace(string(msg))}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *apiClient) url(path string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return c.base + path + sep + "directory=" + url.QueryEscape(c.dir)
}

type httpError struct {
	status       int
	method, path string
	body         string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("opencode server %s %s: %d %s", e.method, e.path, e.status, e.body)
}

func isNotFound(err error) bool {
	var h *httpError
	return errors.As(err, &h) && h.status == http.StatusNotFound
}

// turnSpec is what one turn sends.
type turnSpec struct {
	resumeID string
	title    string
	model    string // provider/model
	system   string
	prompt   string
	mcpName  string // per-session wick MCP server name, "" = none
	mcpURL   string
	mcpToken string
}

// sessionMCPName is the wick MCP server name for one wick session. Tool
// ids come out as <name>_<tool>, so it must stay [a-z0-9_].
func sessionMCPName(sessionID string) string {
	h := sha256.Sum256([]byte(sessionID))
	return mcpServerName + "_" + hex.EncodeToString(h[:])[:12]
}

// promptBody is the prompt_async body. tools is written by hand: opencode
// turns it into permission rules in key order and the LAST match wins, so
// "deny every wick_*" must come before "allow mine" — a Go map would sort
// the keys.
func promptBody(t turnSpec) []byte {
	var b bytes.Buffer
	b.WriteString(`{`)
	if prov, mod, ok := strings.Cut(t.model, "/"); ok {
		m, _ := json.Marshal(map[string]string{"providerID": prov, "modelID": mod})
		b.WriteString(`"model":`)
		b.Write(m)
		b.WriteString(`,`)
	}
	if t.system != "" {
		s, _ := json.Marshal(t.system)
		b.WriteString(`"system":`)
		b.Write(s)
		b.WriteString(`,`)
	}
	// Every session on the server registers its own wick MCP; only this
	// session's may be callable from this session.
	b.WriteString(`"tools":{"` + mcpServerName + `_*":false`)
	if t.mcpName != "" {
		b.WriteString(`,"` + t.mcpName + `_*":true`)
	}
	b.WriteString(`},`)
	parts, _ := json.Marshal([]map[string]string{{"type": "text", "text": t.prompt}})
	b.WriteString(`"parts":`)
	b.Write(parts)
	b.WriteString(`}`)
	return b.Bytes()
}

// remoteProcess implements provider.Process for one server turn.
type remoteProcess struct {
	pr     *io.PipeReader
	pw     *io.PipeWriter
	env    []string
	bin    string
	argv   []string
	dir    string
	done   chan struct{}
	cancel context.CancelFunc

	mu        sync.Mutex
	err       error
	client    *apiClient
	sessionID string
	prompted  bool
	killed    bool
	// finished: the session went idle — the turn is over on the server,
	// so a Kill has nothing left to abort.
	finished bool

	// injMu orders every prompt this turn sends: the first one and the
	// ones Inject adds, so they reach the session in the order they were
	// written. spec is the turn's prompt settings, reused for injections;
	// early holds messages injected before the first prompt went out (they
	// ride along in it); injected counts prompts added mid-turn.
	injMu    sync.Mutex
	spec     turnSpec
	early    []string
	injected int
}

func newRemoteProcess(env []string, bin string, argv []string, dir string) *remoteProcess {
	pr, pw := io.Pipe()
	return &remoteProcess{pr: pr, pw: pw, env: env, bin: bin, argv: argv, dir: dir, done: make(chan struct{})}
}

func (p *remoteProcess) Stdout() io.Reader     { return p.pr }
func (p *remoteProcess) Stdin() io.WriteCloser { return noopWriteCloser{} }
func (p *remoteProcess) Env() []string         { return p.env }
func (p *remoteProcess) Pid() int              { return 0 }
func (p *remoteProcess) Binary() string        { return p.bin }
func (p *remoteProcess) Argv() []string        { return append([]string(nil), p.argv...) }

func (p *remoteProcess) Wait() error {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// Kill aborts the turn: before the prompt went out it just cancels; after,
// it asks the server to abort the session and waits for the idle that
// follows, cutting the stream after killGrace if none comes.
//
// It returns only once the abort is through. The next turn of this wick
// session prompts the SAME opencode session, and an abort still in flight
// when that prompt lands aborts the new turn instead — which then looks
// like a turn that failed on its own and drains the queue again. For the
// same reason a turn that already went idle is not aborted at all.
//
// Whoever kills a turn has stopped reading it, so the pipe is closed
// first: a frame the turn writes after that fails instead of blocking
// the turn (and its lease) forever.
func (p *remoteProcess) Kill() error {
	p.mu.Lock()
	if p.killed {
		p.mu.Unlock()
		return nil
	}
	p.killed = true
	c, sid, prompted, finished := p.client, p.sessionID, p.prompted, p.finished
	p.mu.Unlock()
	_ = p.pr.CloseWithError(errTurnKilled)
	if finished {
		return nil
	}
	if !prompted || c == nil || sid == "" {
		p.cancel()
		return nil
	}
	if err := abortSession(c, sid); err != nil {
		log.Warn().Err(err).Str("session", sid).Msg("agents.opencode: abort failed; ending the turn")
	}
	select {
	case <-p.done:
	case <-time.After(killGrace):
		p.cancel()
	}
	return nil
}

// errTurnOver is Inject's answer once the turn cannot take a message.
var errTurnOver = errors.New("opencode turn is over")

// Inject adds text to the turn that is running. opencode takes a prompt
// for a busy session as another user message of the loop already running
// (SessionPrompt.prompt → runner.ensureRunning joins it), so the reply to
// both comes out of this same stream — what claude does with input typed
// mid-turn. Before the first prompt is out the text is folded into it.
func (p *remoteProcess) Inject(text string) error {
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
	c, sid, t := p.client, p.sessionID, p.spec
	p.mu.Unlock()
	t.prompt = text
	ctx, cancel := context.WithTimeout(context.Background(), abortTimeout)
	defer cancel()
	if err := c.do(ctx, http.MethodPost, "/session/"+sid+"/prompt_async", promptBody(t), nil); err != nil {
		return err
	}
	p.mu.Lock()
	p.injected++
	p.mu.Unlock()
	return nil
}

// injectSettle is how long an idle that follows an injection waits before
// asking whether the session is really done: the injected prompt is
// processed asynchronously, and it can start its own run just after the
// one it was meant to join went idle.
const injectSettle = 300 * time.Millisecond

// stillBusy reports whether the server lists sid as working. Used only
// after an injection, where an idle frame may belong to the run that
// preceded the injected message's own.
func stillBusy(c *apiClient, sid string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), abortTimeout)
	defer cancel()
	var st map[string]struct {
		Type string `json:"type"`
	}
	if c.do(ctx, http.MethodGet, "/session/status", nil, &st) != nil {
		return false
	}
	s, ok := st[sid]
	return ok && s.Type != "" && s.Type != "idle"
}

// errTurnKilled is what a killed turn's Wait reports. It wraps
// context.Canceled because the agent reads that as a stop it asked for,
// not as a crash to recover from.
var errTurnKilled = fmt.Errorf("opencode turn aborted: %w", context.Canceled)

func abortSession(c *apiClient, sid string) error {
	ctx, cancel := context.WithTimeout(context.Background(), abortTimeout)
	defer cancel()
	return c.do(ctx, http.MethodPost, "/session/"+sid+"/abort", nil, nil)
}

func (p *remoteProcess) finish(err error) {
	p.mu.Lock()
	if p.err == nil {
		p.err = err
	}
	if p.killed {
		// Whatever the turn ended with, it ended because it was killed.
		p.err = errTurnKilled
	}
	p.mu.Unlock()
	_ = p.pw.Close()
	close(p.done)
}

func (p *remoteProcess) emit(b []byte) {
	_, _ = p.pw.Write(b)
}

// run drives one turn to the end. It owns the lease.
func (p *remoteProcess) run(ctx context.Context, l *lease, t turnSpec) {
	defer l.release()
	p.mu.Lock()
	p.spec = t
	p.mu.Unlock()
	err := p.turn(ctx, l, t)
	if err != nil && !p.wasKilled() {
		p.mu.Lock()
		sid := p.sessionID
		p.mu.Unlock()
		// The failure is reported IN the turn, as its error frame: the
		// chat shows it and the turn ends like any failed turn. Returning
		// it from Wait as well would make it an unexplained process exit,
		// and the pool would "recover" — restart the agent with a
		// "stopped unexpectedly" notice and a second reply to the same
		// message.
		p.emit(errorLine(sid, err.Error()))
		err = nil
	}
	p.finish(err)
}

func (p *remoteProcess) wasKilled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.killed
}

func (p *remoteProcess) turn(ctx context.Context, l *lease, t turnSpec) error {
	if err := l.waitSlot(ctx); err != nil {
		return err
	}
	c := p.clientFor(l)
	sid, err := ensureSession(ctx, c, t.resumeID, t.title)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.client, p.sessionID = c, sid
	p.mu.Unlock()

	if t.mcpName != "" {
		cfg := map[string]any{"name": t.mcpName, "config": map[string]any{
			"type": "remote", "url": t.mcpURL, "enabled": true,
			"headers": map[string]string{"Authorization": "Bearer " + t.mcpToken},
		}}
		if err := c.do(ctx, http.MethodPost, "/mcp", cfg, nil); err != nil {
			// The turn still runs, just without wick's tools — same as a
			// run spawn whose MCP failed to connect.
			log.Warn().Err(err).Str("session", sid).Msg("agents.opencode: register wick MCP failed")
		}
		defer func() {
			dctx, cancel := context.WithTimeout(context.Background(), abortTimeout)
			defer cancel()
			_ = c.do(dctx, http.MethodPost, "/mcp/"+t.mcpName+"/disconnect", nil, nil)
		}()
	}

	// Subscribe before prompting, or the first frames are lost.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/event"), nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(serverUser, c.password)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("opencode server event stream: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("opencode server event stream: %d", resp.StatusCode)
	}

	p.injMu.Lock()
	p.mu.Lock()
	if len(p.early) > 0 {
		t.prompt = strings.Join(append([]string{t.prompt}, p.early...), "\n\n")
		p.early = nil
	}
	p.mu.Unlock()
	if err := c.do(ctx, http.MethodPost, "/session/"+sid+"/prompt_async", promptBody(t), nil); err != nil {
		p.injMu.Unlock()
		return err
	}
	p.mu.Lock()
	p.prompted = true
	killed := p.killed
	p.mu.Unlock()
	p.injMu.Unlock()
	if killed {
		// Kill landed between session and prompt: it only cancelled.
		_ = abortSession(c, sid)
	}

	tr := newTranslator(sid)
	finished := false
	err = readSSE(resp.Body, func(ev sseEvent) bool {
		lines, done := tr.feed(ev)
		for _, ln := range lines {
			p.emit(ln)
		}
		if done && p.moreInjected(c, sid) {
			done = false
		}
		finished = done
		return !done
	})
	if finished {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		err = errors.New("opencode server closed the event stream mid-turn")
	}
	return err
}

// moreInjected is called on the idle that would end the turn. With no
// injection it just marks the turn finished. After one, it holds Inject
// off, lets the injected prompt settle, and keeps reading when the
// session is still working on it.
func (p *remoteProcess) moreInjected(c *apiClient, sid string) bool {
	p.injMu.Lock()
	defer p.injMu.Unlock()
	p.mu.Lock()
	n := p.injected
	p.mu.Unlock()
	if n > 0 {
		time.Sleep(injectSettle)
		if stillBusy(c, sid) {
			return true
		}
	}
	p.mu.Lock()
	p.finished = true
	p.mu.Unlock()
	return false
}

func (p *remoteProcess) clientFor(l *lease) *apiClient {
	return &apiClient{base: l.s.h.url, password: l.s.h.password, dir: p.dir, http: &http.Client{Timeout: 60 * time.Second}}
}

// ensureSession resumes resumeID when the server still has it, else
// creates a session. A title is always set: an untitled session costs an
// extra LLM call to name it (SessionPrompt.ensureTitle).
func ensureSession(ctx context.Context, c *apiClient, resumeID, title string) (string, error) {
	if resumeID != "" {
		var s struct {
			ID string `json:"id"`
		}
		err := c.do(ctx, http.MethodGet, "/session/"+resumeID, nil, &s)
		if err == nil && s.ID != "" {
			return s.ID, nil
		}
		if err != nil && !isNotFound(err) {
			return "", err
		}
		log.Info().Str("resume", resumeID).Msg("agents.opencode: resume session not found; starting a new one")
	}
	var s struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/session", map[string]string{"title": title}, &s); err != nil {
		return "", err
	}
	if s.ID == "" {
		return "", errors.New("opencode server created a session without an id")
	}
	return s.ID, nil
}
