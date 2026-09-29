package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yogasw/wick/internal/agents/event"
)

func ev(typ string, props any) sseEvent {
	b, _ := json.Marshal(props)
	return sseEvent{Type: typ, Properties: b}
}

func part(p map[string]any) sseEvent { return ev("message.part.updated", map[string]any{"part": p}) }

// The translated lines must parse like `opencode run --format json`.
func TestTranslatorMatchesRunFormat(t *testing.T) {
	tr := newTranslator("ses_1")
	evs := []sseEvent{
		ev("message.updated", map[string]any{"info": map[string]any{"id": "msg_u", "role": "user", "sessionID": "ses_1"}}),
		part(map[string]any{"type": "text", "text": "the prompt", "sessionID": "ses_1", "messageID": "msg_u", "time": map[string]any{"end": 1}}),
		ev("session.status", map[string]any{"sessionID": "ses_1", "status": map[string]any{"type": "busy"}}),
		part(map[string]any{"type": "step-start", "sessionID": "ses_1", "messageID": "msg_a"}),
		part(map[string]any{"type": "text", "text": "", "sessionID": "ses_1", "messageID": "msg_a", "time": map[string]any{"start": 1}}),
		part(map[string]any{"type": "text", "text": "other session", "sessionID": "ses_2", "messageID": "msg_x", "time": map[string]any{"end": 2}}),
		part(map[string]any{"type": "tool", "tool": "bash", "callID": "c1", "sessionID": "ses_1", "messageID": "msg_a", "state": map[string]any{"status": "running"}}),
		part(map[string]any{"type": "tool", "tool": "bash", "callID": "c1", "sessionID": "ses_1", "messageID": "msg_a", "state": map[string]any{"status": "completed", "input": map[string]any{"command": "ls"}, "output": "a.txt"}}),
		part(map[string]any{"type": "step-finish", "reason": "tool-calls", "sessionID": "ses_1", "messageID": "msg_a", "tokens": map[string]any{"input": 1, "output": 1}}),
		part(map[string]any{"type": "text", "text": "hi there", "sessionID": "ses_1", "messageID": "msg_b", "time": map[string]any{"start": 1, "end": 2}}),
		part(map[string]any{"type": "step-finish", "reason": "stop", "sessionID": "ses_1", "messageID": "msg_b", "tokens": map[string]any{"input": 10, "output": 2}}),
		ev("session.idle", map[string]any{"sessionID": "ses_2"}),
	}
	var out []string
	for _, e := range evs {
		lines, done := tr.feed(e)
		if done {
			t.Fatalf("turn ended early at %s", e.Type)
		}
		for _, l := range lines {
			out = append(out, strings.TrimSpace(string(l)))
		}
	}
	if _, done := tr.feed(ev("session.status", map[string]any{"sessionID": "ses_1", "status": map[string]any{"type": "idle"}})); !done {
		t.Fatal("idle of our session did not end the turn")
	}
	var kinds []string
	for _, l := range out {
		var m struct{ Type, SessionID string }
		if err := json.Unmarshal([]byte(l), &m); err != nil || m.SessionID != "ses_1" {
			t.Fatalf("bad line %s", l)
		}
		kinds = append(kinds, m.Type)
	}
	if got := strings.Join(kinds, ","); got != "step_start,tool_use,step_finish,text,step_finish" {
		t.Fatalf("kinds = %s", got)
	}

	p := event.NewOpencodeParser("oc")
	var types []event.EventType
	var text string
	for _, l := range out {
		es, _ := p.ParseAll(l)
		for _, e := range es {
			types = append(types, e.Type)
			if e.Type == event.TextDelta {
				text += e.Text
			}
		}
	}
	if text != "hi there" {
		t.Fatalf("text = %q", text)
	}
	if types[0] != event.SessionStart || types[len(types)-1] != event.Done {
		t.Fatalf("event types = %v", types)
	}
}

// An idle seen before the session ever went busy is the previous turn's.
func TestTranslatorIgnoresIdleBeforeBusy(t *testing.T) {
	tr := newTranslator("ses_1")
	if _, done := tr.feed(ev("session.idle", map[string]any{"sessionID": "ses_1"})); done {
		t.Fatal("ended on a stale idle")
	}
	lines, _ := tr.feed(ev("session.error", map[string]any{"sessionID": "ses_1", "error": map[string]any{"name": "APIError", "data": map[string]any{"message": "nope"}}}))
	if len(lines) != 1 || !strings.Contains(string(lines[0]), `"type":"error"`) {
		t.Fatalf("error line = %q", lines)
	}
	if _, done := tr.feed(ev("session.idle", map[string]any{"sessionID": "ses_1"})); !done {
		t.Fatal("idle after an error did not end the turn")
	}
}

func TestReadSSE(t *testing.T) {
	in := "data: {\"type\":\"a\",\"properties\":{}}\n\n: comment\n\ndata: {\"type\":\"b\",\"properties\":{\"x\":1}}\n\n"
	var got []string
	if err := readSSE(strings.NewReader(in), func(e sseEvent) bool { got = append(got, e.Type); return true }); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a,b" {
		t.Fatalf("got %v", got)
	}
}

func TestPromptBodyToolOrder(t *testing.T) {
	b := string(promptBody(turnSpec{model: "openai/gpt-5.5", system: "soul", prompt: "hi", mcpName: "wick_abc"}))
	deny, allow := strings.Index(b, `"wick_*":false`), strings.Index(b, `"wick_abc_*":true`)
	if deny < 0 || allow < 0 || deny > allow {
		t.Fatalf("deny-all must precede allow-mine: %s", b)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(b), &m); err != nil {
		t.Fatalf("not JSON: %v %s", err, b)
	}
	if m["system"] != "soul" || m["model"].(map[string]any)["modelID"] != "gpt-5.5" {
		t.Fatalf("body = %s", b)
	}
	if n := sessionMCPName("s1"); n != sessionMCPName("s1") || n == sessionMCPName("s2") || !strings.HasPrefix(n, "wick_") {
		t.Fatalf("mcp name %q", n)
	}
}

// fakeOpencode is a minimal opencode server: sessions, prompt_async that
// streams a canned turn (or hangs until abort), abort, mcp.
type fakeOpencode struct {
	mu       sync.Mutex
	subs     []chan string
	hang     bool
	aborted  []string
	mcp      []string
	created  int
	prompts  []string
	password string
}

func (f *fakeOpencode) publish(typ string, props any) {
	b, _ := json.Marshal(map[string]any{"type": typ, "properties": props})
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.subs {
		c <- "data: " + string(b) + "\n\n"
	}
}

func (f *fakeOpencode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if u, p, ok := r.BasicAuth(); !ok || u != serverUser || p != f.password {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == "/event":
		c := make(chan string, 64)
		f.mu.Lock()
		f.subs = append(f.subs, c)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for {
			select {
			case s := <-c:
				_, _ = io.WriteString(w, s)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	case r.URL.Path == "/session" && r.Method == http.MethodPost:
		f.mu.Lock()
		f.created++
		id := fmt.Sprintf("ses_new%d", f.created)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
	case r.URL.Path == "/mcp":
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.mcp = append(f.mcp, string(b))
		f.mu.Unlock()
		_, _ = io.WriteString(w, "true")
	case strings.HasPrefix(r.URL.Path, "/mcp/"):
		_, _ = io.WriteString(w, "true")
	case strings.HasSuffix(r.URL.Path, "/prompt_async"):
		sid := strings.Split(r.URL.Path, "/")[2]
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.prompts = append(f.prompts, string(b))
		hang := f.hang
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		go func() {
			f.publish("session.status", map[string]any{"sessionID": sid, "status": map[string]any{"type": "busy"}})
			if hang {
				return
			}
			f.publish("message.part.updated", map[string]any{"part": map[string]any{"type": "text", "text": "hello", "sessionID": sid, "messageID": "m", "time": map[string]any{"end": 1}}})
			f.publish("message.part.updated", map[string]any{"part": map[string]any{"type": "step-finish", "reason": "stop", "sessionID": sid, "messageID": "m"}})
			f.publish("session.idle", map[string]any{"sessionID": sid})
		}()
	case strings.HasSuffix(r.URL.Path, "/abort"):
		sid := strings.Split(r.URL.Path, "/")[2]
		f.mu.Lock()
		f.aborted = append(f.aborted, sid)
		f.mu.Unlock()
		_, _ = io.WriteString(w, "true")
		go f.publish("session.idle", map[string]any{"sessionID": sid})
	case strings.HasPrefix(r.URL.Path, "/session/") && r.Method == http.MethodGet:
		sid := strings.Split(r.URL.Path, "/")[2]
		if sid == "ses_known" {
			_ = json.NewEncoder(w).Encode(map[string]string{"id": sid})
			return
		}
		http.Error(w, `{"name":"NotFoundError"}`, http.StatusNotFound)
	default:
		http.NotFound(w, r)
	}
}

// startFake runs one turn against a fake server through a real lease.
func startFake(t *testing.T, f *fakeOpencode, ts turnSpec) (*remoteProcess, *fakeStarter) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	fs := &fakeStarter{}
	m := newManager(func(ctx context.Context, spec serverSpec, pw string) (*serverHandle, error) {
		h, err := fs.start(ctx, spec, pw)
		if err == nil {
			h.url = srv.URL
			f.mu.Lock()
			f.password = pw
			f.mu.Unlock()
		}
		return h, err
	})
	m.every = time.Hour
	t.Cleanup(m.shutdown)
	l, err := m.acquire(context.Background(), spec(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	p := newRemoteProcess(nil, "opencode", nil, "/ws")
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go func() { defer cancel(); p.run(ctx, l, ts) }()
	return p, fs
}

func readAll(t *testing.T, p *remoteProcess) []string {
	t.Helper()
	var lines []string
	sc := bufio.NewScanner(p.Stdout())
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

func TestRemoteTurnNewSessionWithMCP(t *testing.T) {
	f := &fakeOpencode{}
	p, _ := startFake(t, f, turnSpec{title: "wick s1", model: "openai/gpt-5.5", prompt: "hi", resumeID: "ses_gone",
		mcpName: "wick_abc", mcpURL: "http://127.0.0.1:1/mcp", mcpToken: "tok"})
	lines := readAll(t, p)
	if err := p.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if len(lines) != 2 || !strings.Contains(lines[0], `"text":"hello"`) || !strings.Contains(lines[0], `"sessionID":"ses_new1"`) {
		t.Fatalf("lines = %v", lines)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.created != 1 {
		t.Fatalf("unknown resume id should create a session, created=%d", f.created)
	}
	if len(f.mcp) != 1 || !strings.Contains(f.mcp[0], `"Bearer tok"`) || !strings.Contains(f.mcp[0], `"wick_abc"`) {
		t.Fatalf("mcp registration = %v", f.mcp)
	}
}

func TestRemoteTurnResumes(t *testing.T) {
	f := &fakeOpencode{}
	p, _ := startFake(t, f, turnSpec{title: "t", model: "a/b", prompt: "hi", resumeID: "ses_known"})
	lines := readAll(t, p)
	_ = p.Wait()
	if f.created != 0 || len(lines) == 0 || !strings.Contains(lines[0], `"sessionID":"ses_known"`) {
		t.Fatalf("resume did not reuse the session: created=%d lines=%v", f.created, lines)
	}
}

// Kill aborts the session; the server itself stays up.
func TestRemoteKillAborts(t *testing.T) {
	f := &fakeOpencode{hang: true}
	p, fs := startFake(t, f, turnSpec{title: "t", model: "a/b", prompt: "long job"})
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.prompts)
		f.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("prompt never sent")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // prompted flag set after the POST returns
	if p.Pid() != 0 {
		t.Fatal("Pid must be 0: the agent signals Pid's process group, which is the server")
	}
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { readAll(t, p); _ = p.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(killGrace + 2*time.Second):
		t.Fatal("turn did not end after Kill")
	}
	f.mu.Lock()
	aborted := append([]string(nil), f.aborted...)
	f.mu.Unlock()
	if len(aborted) != 1 || aborted[0] != "ses_new1" {
		t.Fatalf("aborted = %v", aborted)
	}
	if _, k := fs.counts(); k != 0 {
		t.Fatal("Kill killed the shared server")
	}
	if p.Wait() == nil {
		t.Fatal("an aborted turn must not report success")
	}
}
