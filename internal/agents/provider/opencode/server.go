package opencode

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/yogasw/wick/internal/agents/provider/procgroup"
	"github.com/yogasw/wick/pkg/safeexec"
)

// server.go runs one `opencode serve` per instance and hands turns to it.
//
// Measured on the 2 vCPU host: `opencode run` costs 6-6.5 s and 610-790 MB
// per turn; a warm server answers in ~1.7 s and holds ~520-615 MB shared by
// every session on it. The server is started lazily by the first turn,
// reached on 127.0.0.1 with a random port and password, and killed by the
// idle reaper once no turn has used it for the instance's idle window.
//
// Key: instance name + a hash of the server's env. The env carries
// everything that makes two servers differ — XDG dirs (the account), the
// inline config (extra MCP servers, foreign MCP names switched off for the
// workspace), the instance Env. The per-session wick MCP token is NOT in
// it: tokens are minted per spawn and revoked when the spawn exits, so a
// token baked into the server would die after the first turn and would
// serve every later session as whoever came first. Each turn instead
// registers its own token under a per-session MCP name (POST /mcp) and
// the prompt denies every other session's wick tools (see remote.go).

const (
	// DefaultServerIdle is the idle window when the instance sets none.
	DefaultServerIdle = 10 * time.Minute
	// DefaultServerTurns caps turns running at once on one server; the
	// rest queue. Two keeps a server under ~800 MB on the 3.6 GB host.
	DefaultServerTurns = 2

	serverUser     = "opencode"
	serverBootWait = 60 * time.Second
	serverKillWait = 3 * time.Second
	reapInterval   = 30 * time.Second
)

// serverSpec is what one server is started from.
type serverSpec struct {
	instance string
	bin      string
	env      []string // full env (scrubbed OS env + instance + wick-added)
	dir      string
	idle     time.Duration
	turns    int
	// wrap turns (bin, args) into the argv actually executed (memory
	// scope); nil = run bin directly.
	wrap func(bin string, args []string) (string, []string, string)
}

func (s serverSpec) key() string {
	h := sha256.New()
	env := append([]string(nil), s.env...)
	sort.Strings(env)
	_, _ = io.WriteString(h, s.bin+"\x00")
	for _, e := range env {
		_, _ = io.WriteString(h, e+"\x00")
	}
	return s.instance + "/" + hex.EncodeToString(h.Sum(nil))[:16]
}

// serverHandle is a started server as the manager sees it.
type serverHandle struct {
	url      string
	password string
	pid      int
	scope    string
	kill     func()
	done     <-chan struct{} // closed when the process has exited
}

type startFunc func(ctx context.Context, spec serverSpec, password string) (*serverHandle, error)

// server is one managed opencode serve.
type server struct {
	key      string
	instance string
	ready    chan struct{} // closed once h / err are set
	h        *serverHandle
	err      error
	slots    chan struct{}

	// guarded by manager.mu
	active   int // leases held (queued or running)
	lastUsed time.Time
	idle     time.Duration
	// stale: the instance's settings moved on (other env, server mode
	// off). It finishes the turns it has and is killed once it has none.
	stale bool
}

func (s *server) dead() bool {
	if s.h == nil {
		return false
	}
	select {
	case <-s.h.done:
		return true
	default:
		return false
	}
}

// manager owns every opencode server of this wick process.
type manager struct {
	mu      sync.Mutex
	servers map[string]*server
	now     func() time.Time
	start   startFunc
	every   time.Duration
	once    sync.Once
	stop    chan struct{}
	closed  bool
}

func newManager(start startFunc) *manager {
	return &manager{servers: map[string]*server{}, now: time.Now, start: start, every: reapInterval, stop: make(chan struct{})}
}

var servers = newManager(startServe)

// ShutdownServers kills every opencode server. Called on wick shutdown so
// no server outlives the process that supervises it.
func ShutdownServers() { servers.shutdown() }

// lease is one turn's hold on a server: while held the server is never
// reaped. release exactly once.
type lease struct {
	m       *manager
	s       *server
	slot    bool
	release func()
}

// acquire returns a lease on the server for spec, starting it when there
// is none (or the previous one died). A failed start is not cached: the
// next turn tries again.
func (m *manager) acquire(ctx context.Context, spec serverSpec) (*lease, error) {
	m.once.Do(func() { go m.reapLoop() })
	key := spec.key()
	idle := spec.idle
	if idle <= 0 {
		idle = DefaultServerIdle
	}
	turns := spec.turns
	if turns <= 0 {
		turns = DefaultServerTurns
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("opencode: shutting down")
	}
	s := m.servers[key]
	if s != nil {
		select {
		case <-s.ready:
			if s.err != nil || s.dead() {
				delete(m.servers, key)
				s = nil
			}
		default:
		}
	}
	starting := false
	if s == nil {
		s = &server{key: key, instance: spec.instance, ready: make(chan struct{}), slots: make(chan struct{}, turns)}
		m.servers[key] = s
		starting = true
	}
	s.active++
	s.idle = idle
	s.lastUsed = m.now()
	idleOld := m.retireLocked(spec.instance, key)
	m.mu.Unlock()
	killAll(idleOld)

	if starting {
		h, err := m.start(ctx, spec, randomPassword())
		m.mu.Lock()
		s.h, s.err = h, err
		if err != nil && m.servers[key] == s {
			delete(m.servers, key)
		}
		m.mu.Unlock()
		close(s.ready)
		if h != nil {
			log.Info().Str("instance", spec.instance).Str("key", key).Int("pid", h.pid).
				Str("url", h.url).Dur("idle", idle).Msg("agents.opencode: server started")
		}
	}
	select {
	case <-s.ready:
	case <-ctx.Done():
		m.put(s)
		return nil, ctx.Err()
	}
	if s.err != nil {
		m.put(s)
		return nil, s.err
	}
	l := &lease{m: m, s: s}
	var once sync.Once
	l.release = func() { once.Do(func() { l.dropSlot(); m.put(s) }) }
	return l, nil
}

// put gives a lease back and stamps the server as used now.
func (m *manager) put(s *server) {
	m.mu.Lock()
	s.active--
	s.lastUsed = m.now()
	var victims []*server
	if s.stale && s.active == 0 {
		victims = m.dropLocked(s)
	}
	m.mu.Unlock()
	killAll(victims)
}

// retire marks every server of instance stale (server mode switched off):
// idle ones die now, busy ones when their last turn ends.
func (m *manager) retire(instance string) {
	m.mu.Lock()
	victims := m.retireLocked(instance, "")
	m.mu.Unlock()
	killAll(victims)
}

// retireLocked marks instance's servers other than keep stale and returns
// the ones with no turn, already removed from the map.
func (m *manager) retireLocked(instance, keep string) []*server {
	var victims []*server
	for k, s := range m.servers {
		if s.instance != instance || k == keep {
			continue
		}
		s.stale = true
		if s.active == 0 {
			victims = append(victims, m.dropLocked(s)...)
		}
	}
	return victims
}

// dropLocked forgets s; returns it for killing when it is a live server.
func (m *manager) dropLocked(s *server) []*server {
	if m.servers[s.key] == s {
		delete(m.servers, s.key)
	}
	select {
	case <-s.ready:
		if s.h != nil {
			return []*server{s}
		}
	default:
	}
	return nil
}

func killAll(ss []*server) {
	for _, s := range ss {
		log.Info().Str("key", s.key).Int("pid", s.h.pid).Msg("agents.opencode: stale server stopped")
		s.h.kill()
	}
}

// waitSlot blocks until the server has room for one more running turn.
func (l *lease) waitSlot(ctx context.Context) error {
	select {
	case l.s.slots <- struct{}{}:
		l.slot = true
		return nil
	case <-l.s.h.done:
		return errors.New("opencode server exited")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *lease) dropSlot() {
	if l.slot {
		l.slot = false
		<-l.s.slots
	}
}

// reapLoop runs for the life of the process: it is the only thing that
// ever stops an idle server, so there is no switch to turn it off.
func (m *manager) reapLoop() {
	t := time.NewTicker(m.every)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.reap()
		}
	}
}

// reap kills every server no lease has touched for its idle window, and
// forgets servers that died on their own. Returns the keys it killed.
func (m *manager) reap() []string {
	now := m.now()
	var victims []*server
	m.mu.Lock()
	for k, s := range m.servers {
		select {
		case <-s.ready:
		default:
			continue // still starting
		}
		if s.err != nil || s.dead() {
			if s.active == 0 {
				delete(m.servers, k)
			}
			continue
		}
		if s.active == 0 && now.Sub(s.lastUsed) >= s.idle {
			delete(m.servers, k)
			victims = append(victims, s)
		}
	}
	m.mu.Unlock()
	var keys []string
	for _, s := range victims {
		log.Info().Str("key", s.key).Int("pid", s.h.pid).Msg("agents.opencode: idle server killed")
		s.h.kill()
		keys = append(keys, s.key)
	}
	return keys
}

func (m *manager) shutdown() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	close(m.stop)
	all := make([]*server, 0, len(m.servers))
	for _, s := range m.servers {
		all = append(all, s)
	}
	m.servers = map[string]*server{}
	m.mu.Unlock()
	for _, s := range all {
		<-s.ready
		if s.h != nil {
			s.h.kill()
		}
	}
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func randomPassword() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

var listenLine = regexp.MustCompile(`listening on (https?://\S+)`)

// startServe execs `opencode serve` on a kernel-chosen port and waits until
// it answers /global/health.
func startServe(ctx context.Context, spec serverSpec, password string) (*serverHandle, error) {
	// --port 0 is not "any port" to opencode (it falls back to 4096), so
	// the kernel picks one here; the tiny reuse race only costs a retry on
	// the next turn.
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	args := []string{"serve", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port)}
	bin, argv, scope := spec.bin, args, ""
	if spec.wrap != nil {
		bin, argv, scope = spec.wrap(spec.bin, args)
	}
	// Not CommandContext: the server outlives the turn that started it.
	cmd := safeexec.Command(bin, argv...)
	cmd.Dir = spec.dir
	cmd.Env = append(append([]string(nil), spec.env...), "OPENCODE_SERVER_USERNAME="+serverUser, "OPENCODE_SERVER_PASSWORD="+password)
	hideConsole(cmd)
	procgroup.Apply(cmd)
	dieWithParent(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start opencode serve: %w", err)
	}
	done := make(chan struct{})
	urlCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		sent := false
		for sc.Scan() {
			line := sc.Text()
			if !sent {
				if m := listenLine.FindStringSubmatch(line); m != nil {
					urlCh <- m[1]
					sent = true
					continue
				}
			}
			log.Debug().Str("instance", spec.instance).Str("line", line).Msg("agents.opencode: serve")
		}
	}()
	go func() { _ = cmd.Wait(); close(done) }()
	pid := cmd.Process.Pid
	kill := func() { killServer(pid, done) }

	var url string
	select {
	case url = <-urlCh:
	case <-done:
		return nil, errors.New("opencode serve exited before listening")
	case <-time.After(serverBootWait):
		kill()
		return nil, errors.New("opencode serve did not start listening in time")
	case <-ctx.Done():
		kill()
		return nil, ctx.Err()
	}
	h := &serverHandle{url: strings.TrimRight(url, "/"), password: password, pid: pid, scope: scope, kill: kill, done: done}
	if err := waitHealthy(ctx, h); err != nil {
		kill()
		return nil, err
	}
	return h, nil
}

func waitHealthy(ctx context.Context, h *serverHandle) error {
	c := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(serverBootWait)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.url+"/global/health", nil)
		req.SetBasicAuth(serverUser, h.password)
		if resp, err := c.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-h.done:
			return errors.New("opencode serve exited during boot")
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return errors.New("opencode serve not healthy in time")
}

// killServer terminates the server's process group, forcing it after a
// short grace.
func killServer(pid int, done <-chan struct{}) {
	signalServerGroup(pid, false)
	select {
	case <-done:
	case <-time.After(serverKillWait):
	}
	signalServerGroup(pid, true)
}
