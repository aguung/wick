package channels

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	agentconfig "github.com/yogasw/wick/internal/agents/config"
)

const (
	// maxSlackChunk is the safe upper bound for a single Slack message.
	// Slack hard limit is 4000 chars; we leave 200 chars headroom for
	// continuation markers.
	maxSlackChunk = 3800

	// reactions used for the agent lifecycle
	reactionQueued  = "hourglass_flowing_sand" // ⏳
	reactionRunning = "gear"                   // ⚙️
	reactionDone    = "white_check_mark"       // ✅
	reactionBlocked = "no_entry_sign"          // 🚫
	reactionError   = "x"                      // ❌
)

// SlackChannel implements Channel for Slack, supporting both Socket Mode
// (default — no public URL required) and HTTP Event API (requires public URL).
//
// Lifecycle (per incoming message):
//  1. Parse event → extract channel_id, thread_ts, user_id, text
//  2. Access control check (everyone / users / groups)
//  3. Meta-command intercept (dashboard, reset, status, log, agent)
//  4. Dispatch to pool via sendFn
//  5. On agent events: update reactions + post chunked final reply
type SlackChannel struct {
	cfg     agentconfig.SlackConfig
	sendFn  SendFunc
	pubURL  string // base URL for dashboard links (GeneralConfig.PublicURL)

	api    *slack.Client
	socket *socketmode.Client

	mu       sync.Mutex
	// messageTS tracks the Slack message timestamp for each session so
	// we can update reactions on the correct message.
	// key = sessionKey (thread_ts), value = message_ts of the user turn
	messageTS map[string]string

	stopCh chan struct{}
}

// NewSlack builds a SlackChannel from the operator-supplied config.
// sendFn is pool.Send (or a wrapper). pubURL is used for dashboard links.
func NewSlack(cfg agentconfig.SlackConfig, sendFn SendFunc, pubURL string) *SlackChannel {
	api := slack.New(
		cfg.BotToken,
		slack.OptionAppLevelToken(cfg.AppToken),
	)
	socket := socketmode.New(api)
	return &SlackChannel{
		cfg:       cfg,
		sendFn:    sendFn,
		pubURL:    pubURL,
		api:       api,
		socket:    socket,
		messageTS: make(map[string]string),
		stopCh:    make(chan struct{}),
	}
}

// Name satisfies Channel.
func (s *SlackChannel) Name() string { return "slack" }

// IsConfigured returns true when the config has the minimum required fields
// to start. Server.go uses this to skip the channel gracefully rather than
// treating a missing token as a fatal boot error.
func (s *SlackChannel) IsConfigured() bool {
	if s.cfg.BotToken == "" {
		return false
	}
	if s.cfg.Mode != "http" && s.cfg.AppToken == "" {
		// Socket mode (default) requires an app-level token.
		return false
	}
	return true
}

// Start begins listening for Slack events. It blocks until ctx is cancelled
// or a fatal error occurs. Only Socket Mode is implemented; HTTP Event API
// is a future extension (config.Mode == "http").
// Call IsConfigured() before Start() — Start returns an error immediately
// when required tokens are absent.
func (s *SlackChannel) Start(ctx context.Context) error {
	if s.cfg.BotToken == "" {
		return fmt.Errorf("slack: bot token is required")
	}
	if s.cfg.Mode == "http" {
		return fmt.Errorf("slack: HTTP Event API mode is not yet implemented; use mode=socket")
	}
	if s.cfg.AppToken == "" {
		return fmt.Errorf("slack: app token (xapp-...) is required for socket mode")
	}

	log.Info().Str("channel", "slack").Str("mode", "socket").Msg("starting")

	go func() {
		select {
		case <-ctx.Done():
			close(s.stopCh)
		case <-s.stopCh:
		}
	}()

	// socketmode.Client.Run() blocks and reconnects internally.
	go func() {
		if err := s.socket.Run(); err != nil {
			log.Error().Str("channel", "slack").Err(err).Msg("socket run stopped")
		}
	}()

	for {
		select {
		case <-s.stopCh:
			return nil
		case evt, ok := <-s.socket.Events:
			if !ok {
				return nil
			}
			s.handleSocketEvent(ctx, evt)
		}
	}
}

// Stop signals the channel to shut down.
func (s *SlackChannel) Stop() {
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
}

// handleSocketEvent dispatches a raw socket-mode event to the right handler.
func (s *SlackChannel) handleSocketEvent(ctx context.Context, evt socketmode.Event) {
	switch evt.Type {
	case socketmode.EventTypeEventsAPI:
		s.socket.Ack(*evt.Request)
		apiEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return
		}
		s.handleEventsAPI(ctx, apiEvent)
	case socketmode.EventTypeConnecting:
		log.Debug().Str("channel", "slack").Msg("connecting")
	case socketmode.EventTypeConnected:
		log.Info().Str("channel", "slack").Msg("connected")
	case socketmode.EventTypeConnectionError:
		log.Warn().Str("channel", "slack").Msg("connection error, will retry")
	}
}

// handleEventsAPI handles Events API payloads (message events etc.).
func (s *SlackChannel) handleEventsAPI(ctx context.Context, outer slackevents.EventsAPIEvent) {
	switch outer.Type {
	case slackevents.CallbackEvent:
		switch ev := outer.InnerEvent.Data.(type) {
		case *slackevents.MessageEvent:
			// Ignore bot messages to avoid feedback loops.
			if ev.BotID != "" || ev.SubType == "bot_message" {
				return
			}
			s.handleMessage(ctx, ev)
		}
	}
}

// handleMessage is the main entry point for an inbound user message.
func (s *SlackChannel) handleMessage(ctx context.Context, ev *slackevents.MessageEvent) {
	// thread_ts is the session key. For top-level messages it equals the
	// message ts; for thread replies it is ev.ThreadTimeStamp.
	threadTS := ev.ThreadTimeStamp
	if threadTS == "" {
		threadTS = ev.TimeStamp
	}

	// 1. Access control
	groupIDs, err := s.resolveUserGroups(ev.User)
	if err != nil {
		log.Warn().Str("channel", "slack").Str("user", ev.User).Err(err).Msg("resolve groups failed; falling back to empty")
	}
	if !s.allowed(ev.User, groupIDs) {
		log.Debug().Str("channel", "slack").Str("user", ev.User).Msg("access denied, ignoring message")
		return
	}

	// 2. Meta-command intercept
	meta := ParseMeta(ev.Text)
	if meta.IsMeta {
		s.handleMetaCmd(ctx, meta, ev.Channel, threadTS)
		return
	}

	// 3. Record message ts for reaction updates on this turn.
	s.mu.Lock()
	s.messageTS[threadTS] = ev.TimeStamp
	s.mu.Unlock()

	// 4. Add ⏳ reaction immediately so user sees acknowledgment.
	s.setReaction(reactionQueued, ev.Channel, ev.TimeStamp, "")

	// 5. Dispatch to pool — use context.Background() so the agent
	// subprocess lives past this goroutine's lifetime.
	if err := s.sendFn(context.Background(), threadTS, "main", "slack", "user", ev.Text); err != nil {
		log.Error().Str("channel", "slack").Str("session", threadTS).Err(err).Msg("pool send failed")
		s.setReaction(reactionError, ev.Channel, ev.TimeStamp, reactionQueued)
		s.postReply(ev.Channel, threadTS, "Agent error: could not queue message. Check the dashboard for details.")
	}
}

// NotifyState is called by the agent event pipeline to update the Slack
// reaction and post a final reply when the agent turn is done.
//
// state: "running" | "done" | "blocked" | "error"
// text is only used when state == "done" | "blocked" | "error".
func (s *SlackChannel) NotifyState(channelID, sessionKey, state, text string) {
	s.mu.Lock()
	msgTS, ok := s.messageTS[sessionKey]
	s.mu.Unlock()
	if !ok {
		return
	}

	switch state {
	case "running":
		s.setReaction(reactionRunning, channelID, msgTS, reactionQueued)
	case "done":
		s.setReaction(reactionDone, channelID, msgTS, reactionRunning)
		if text != "" {
			s.postChunked(channelID, sessionKey, text)
		}
	case "blocked":
		s.setReaction(reactionBlocked, channelID, msgTS, reactionRunning)
		note := text
		if note == "" {
			note = "Agent turn completed with blocked commands. See the dashboard for details."
		} else {
			note += "\n\n_Some commands were blocked — see the dashboard for details._"
		}
		s.postChunked(channelID, sessionKey, note)
	case "error":
		s.setReaction(reactionError, channelID, msgTS, reactionRunning)
		msg := "Agent error."
		if text != "" {
			msg = fmt.Sprintf("Agent error: %s", text)
		}
		s.postReply(channelID, sessionKey, msg+"\n\nSee the dashboard for details.")
	}
}

// setReaction removes `old` (if non-empty) and adds `new` on the given message.
// Both operations use exponential backoff for Slack rate limits.
func (s *SlackChannel) setReaction(newReaction, channelID, msgTS, oldReaction string) {
	ref := slack.ItemRef{Channel: channelID, Timestamp: msgTS}
	if oldReaction != "" {
		s.withBackoff(func() error {
			return s.api.RemoveReaction(oldReaction, ref)
		})
	}
	s.withBackoff(func() error {
		return s.api.AddReaction(newReaction, ref)
	})
}

// postReply sends a single message into the thread.
func (s *SlackChannel) postReply(channelID, threadTS, text string) {
	s.withBackoff(func() error {
		_, _, err := s.api.PostMessage(
			channelID,
			slack.MsgOptionText(text, false),
			slack.MsgOptionTS(threadTS),
		)
		return err
	})
}

// postChunked splits text at maxSlackChunk chars and posts each chunk as
// a threaded reply. Chunks after the first are prefixed with "(cont.)" so
// the reader sees continuity.
func (s *SlackChannel) postChunked(channelID, threadTS, text string) {
	chunks := chunkText(text, maxSlackChunk)
	for i, chunk := range chunks {
		msg := chunk
		if i > 0 {
			msg = "_(cont.)_\n" + chunk
		}
		s.postReply(channelID, threadTS, msg)
	}
}

// chunkText splits s into chunks of at most max runes, breaking on newlines
// where possible to avoid cutting mid-word.
func chunkText(s string, max int) []string {
	if len(s) <= max {
		return []string{s}
	}
	var chunks []string
	for len(s) > max {
		cut := max
		// Try to break on a newline within the last 200 chars of the chunk.
		if idx := strings.LastIndex(s[:cut], "\n"); idx > cut-200 {
			cut = idx + 1
		}
		chunks = append(chunks, strings.TrimRight(s[:cut], "\n"))
		s = s[cut:]
	}
	if s != "" {
		chunks = append(chunks, s)
	}
	return chunks
}

// withBackoff calls fn with exponential backoff on Slack rate-limit errors
// (HTTP 429). Retries up to 5 times with a cap of 32 s.
func (s *SlackChannel) withBackoff(fn func() error) {
	const maxRetries = 5
	for attempt := 0; attempt < maxRetries; attempt++ {
		err := fn()
		if err == nil {
			return
		}
		if !isRateLimit(err) {
			log.Warn().Str("channel", "slack").Err(err).Msg("slack api call failed")
			return
		}
		wait := time.Duration(math.Pow(2, float64(attempt))) * time.Second
		if wait > 32*time.Second {
			wait = 32 * time.Second
		}
		log.Warn().Str("channel", "slack").Dur("wait", wait).Msg("rate limited, backing off")
		time.Sleep(wait)
	}
	log.Error().Str("channel", "slack").Msg("slack api call failed after max retries")
}

// isRateLimit returns true when err is a Slack rate-limit response.
func isRateLimit(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "rate_limited") ||
		strings.Contains(err.Error(), "ratelimited")
}

// allowed checks whether the given user is permitted to trigger agents,
// based on SlackConfig.AccessMode.
func (s *SlackChannel) allowed(userID string, groupIDs []string) bool {
	switch s.cfg.AccessMode {
	case "everyone", "":
		return true
	case "users":
		return s.inList(s.cfg.AllowedUsers, userID)
	case "groups":
		for _, gid := range groupIDs {
			if s.inList(s.cfg.AllowedGroups, gid) {
				return true
			}
		}
		return false
	}
	return false
}

// inList checks whether id appears in the newline/comma-separated list.
// AllowedUsers and AllowedGroups are stored as kvlist (one entry per line
// in the wick config system).
func (s *SlackChannel) inList(list, id string) bool {
	for _, entry := range strings.FieldsFunc(list, func(r rune) bool {
		return r == '\n' || r == ',' || r == ' '
	}) {
		if strings.TrimSpace(entry) == id {
			return true
		}
	}
	return false
}

// resolveUserGroups fetches the Slack User Groups that userID belongs to.
// Returns an empty slice (not an error) when groups cannot be resolved —
// access check falls back gracefully.
func (s *SlackChannel) resolveUserGroups(userID string) ([]string, error) {
	groups, err := s.api.GetUserGroups(slack.GetUserGroupsOptionIncludeUsers(true))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, g := range groups {
		for _, uid := range g.Users {
			if uid == userID {
				out = append(out, g.ID)
				break
			}
		}
	}
	return out, nil
}

// handleMetaCmd processes a wick meta-command and replies in the thread.
func (s *SlackChannel) handleMetaCmd(ctx context.Context, meta MetaResult, channelID, threadTS string) {
	switch meta.Cmd {
	case "dashboard", "link":
		url := s.dashboardURL(threadTS)
		s.postReply(channelID, threadTS, fmt.Sprintf("Dashboard: %s", url))
	case "status":
		s.postReply(channelID, threadTS, "_Use the dashboard to view real-time agent status._\n"+s.dashboardURL(threadTS))
	case "reset":
		// Reset is handled by sending a special wick-internal marker to
		// the pool. For now, acknowledge and note it's not yet wired.
		s.postReply(channelID, threadTS, "_Reset acknowledged. The next message will start a fresh context._")
	case "agent":
		if meta.Arg == "" {
			s.postReply(channelID, threadTS, "_Usage: /agent <name>_")
			return
		}
		s.postReply(channelID, threadTS, fmt.Sprintf("_Switching to agent `%s` is not yet wired. Coming soon._", meta.Arg))
	case "log":
		s.postReply(channelID, threadTS, "_Command log: see the dashboard for full details._\n"+s.dashboardURL(threadTS))
	}
}

// dashboardURL builds the session detail URL from PublicURL + thread_ts.
func (s *SlackChannel) dashboardURL(threadTS string) string {
	base := strings.TrimRight(s.pubURL, "/")
	if base == "" {
		return "(dashboard URL not configured — set PublicURL in Settings → Agents)"
	}
	return fmt.Sprintf("%s/tools/agents/sessions/%s", base, threadTS)
}
