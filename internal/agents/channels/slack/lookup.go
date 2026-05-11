// Package slack — picker lookup sources.
//
// Implements channels.LookupProvider so the admin UI's picker widget can
// search the Slack workspace in real time (users, user groups, channels).
// Results are cached briefly per (source,query) to avoid hammering Slack's
// rate limits when the operator types.

package slack

import (
	"fmt"
	"strings"
	"sync"
	"time"

	slackgo "github.com/slack-go/slack"

	agentchannels "github.com/yogasw/wick/internal/agents/channels"
)

const (
	lookupMaxResults = 20
	lookupCacheTTL   = 60 * time.Second
)

type lookupCacheEntry struct {
	at    time.Time
	items []agentchannels.LookupItem
}

var (
	lookupCacheMu sync.Mutex
	lookupCache   = map[string]lookupCacheEntry{}
)

// Lookup satisfies channels.LookupProvider. Supported sources:
//   - "slack.users"      → workspace users (skips bots / deleted)
//   - "slack.usergroups" → user groups (matches name + handle)
//   - "slack.channels"   → public + private channels the bot can see
func (s *Channel) Lookup(source, query string) ([]agentchannels.LookupItem, error) {
	s.cfgMu.Lock()
	api := s.api
	s.cfgMu.Unlock()
	if api == nil {
		return nil, fmt.Errorf("slack not configured")
	}

	q := strings.ToLower(strings.TrimSpace(query))
	cacheKey := source + "|" + q
	lookupCacheMu.Lock()
	if e, ok := lookupCache[cacheKey]; ok && time.Since(e.at) < lookupCacheTTL {
		lookupCacheMu.Unlock()
		return e.items, nil
	}
	lookupCacheMu.Unlock()

	var items []agentchannels.LookupItem
	var err error
	switch source {
	case "slack.users":
		items, err = lookupSlackUsers(api, q)
	case "slack.usergroups":
		items, err = lookupSlackUserGroups(api, q)
	case "slack.channels":
		items, err = lookupSlackChannels(api, q)
	default:
		return nil, fmt.Errorf("unknown source %q", source)
	}
	if err != nil {
		return nil, err
	}

	lookupCacheMu.Lock()
	lookupCache[cacheKey] = lookupCacheEntry{at: time.Now(), items: items}
	lookupCacheMu.Unlock()
	return items, nil
}

func lookupSlackUsers(api *slackgo.Client, q string) ([]agentchannels.LookupItem, error) {
	users, err := api.GetUsers()
	if err != nil {
		return nil, err
	}
	out := make([]agentchannels.LookupItem, 0, lookupMaxResults)
	for _, u := range users {
		if u.Deleted || u.IsBot {
			continue
		}
		name := u.RealName
		if name == "" {
			name = u.Profile.DisplayName
		}
		if name == "" {
			name = u.Name
		}
		if q != "" && !containsFold(name, q) && !containsFold(u.Name, q) && !containsFold(u.ID, q) {
			continue
		}
		out = append(out, agentchannels.LookupItem{ID: u.ID, Name: name})
		if len(out) >= lookupMaxResults {
			break
		}
	}
	return out, nil
}

func lookupSlackUserGroups(api *slackgo.Client, q string) ([]agentchannels.LookupItem, error) {
	groups, err := api.GetUserGroups()
	if err != nil {
		return nil, err
	}
	out := make([]agentchannels.LookupItem, 0, lookupMaxResults)
	for _, g := range groups {
		if q != "" && !containsFold(g.Name, q) && !containsFold(g.Handle, q) && !containsFold(g.ID, q) {
			continue
		}
		label := g.Name
		if g.Handle != "" {
			label = g.Name + " (@" + g.Handle + ")"
		}
		out = append(out, agentchannels.LookupItem{ID: g.ID, Name: label})
		if len(out) >= lookupMaxResults {
			break
		}
	}
	return out, nil
}

func lookupSlackChannels(api *slackgo.Client, q string) ([]agentchannels.LookupItem, error) {
	params := &slackgo.GetConversationsParameters{
		ExcludeArchived: true,
		Limit:           200,
		Types:           []string{"public_channel", "private_channel"},
	}
	out := make([]agentchannels.LookupItem, 0, lookupMaxResults)
	for {
		chans, cursor, err := api.GetConversations(params)
		if err != nil {
			return nil, err
		}
		for _, ch := range chans {
			if q != "" && !containsFold(ch.Name, q) && !containsFold(ch.ID, q) {
				continue
			}
			out = append(out, agentchannels.LookupItem{ID: ch.ID, Name: "#" + ch.Name})
			if len(out) >= lookupMaxResults {
				return out, nil
			}
		}
		if cursor == "" {
			break
		}
		params.Cursor = cursor
	}
	return out, nil
}

func containsFold(s, sub string) bool {
	if sub == "" {
		return true
	}
	return strings.Contains(strings.ToLower(s), sub)
}
