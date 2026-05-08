package event

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ClaudeParser parses the Claude CLI `--output-format stream-json`
// stream. It is stateful: content_block_start opens a block (kind +
// optional tool name) keyed by index, content_block_delta carries
// payload deltas, content_block_stop closes a block.
//
// Concurrency: not safe for concurrent use. One parser per subprocess.
// Thread the parser through the same goroutine that scans stdout.
type ClaudeParser struct {
	// blocks maps content_block index → block kind/tool snapshot. Only
	// blocks Claude has opened-but-not-stopped live here; we delete on
	// content_block_stop so memory stays bounded.
	blocks map[int]claudeBlock

	// sessionID is captured from the first event that exposes it.
	// Claude tags every event with `session_id`; we hold onto it so a
	// second SessionStart event isn't emitted on every parse.
	sessionID string

	// sessionEmitted is true after the first SessionStart we returned.
	// Without this, every event would re-fire SessionStart.
	sessionEmitted bool
}

type claudeBlock struct {
	kind     string // "thinking" | "text" | "tool_use" | other
	toolName string // populated when kind == "tool_use"
	// inputBuf accumulates partial_json for tool_use blocks so we can
	// hand the gate the full arg JSON on content_block_stop.
	inputBuf strings.Builder
}

// NewClaudeParser returns a fresh parser ready to consume Claude
// stream-json lines.
func NewClaudeParser() *ClaudeParser {
	return &ClaudeParser{blocks: map[int]claudeBlock{}}
}

// claudeRaw is the wire shape of one stream-json line. We model only
// the fields we use; unknown fields are ignored.
type claudeRaw struct {
	Type         string `json:"type"`
	SessionID    string `json:"session_id,omitempty"`
	Index        int    `json:"index,omitempty"`
	ContentBlock *struct {
		Type string `json:"type"`
		Name string `json:"name,omitempty"`
	} `json:"content_block,omitempty"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text,omitempty"`
		Thinking    string `json:"thinking,omitempty"`
		PartialJSON string `json:"partial_json,omitempty"`
	} `json:"delta,omitempty"`
	Message *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text,omitempty"`
		} `json:"content,omitempty"`
	} `json:"message,omitempty"`
	Error *struct {
		Message string `json:"message,omitempty"`
	} `json:"error,omitempty"`
}

// Parse decodes one line and returns the normalized event. Empty / ws-
// only lines yield (Unknown, nil) — caller can stream stdout without
// filtering.
func (p *ClaudeParser) Parse(line string) (AgentEvent, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return AgentEvent{}, nil
	}

	var raw claudeRaw
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return AgentEvent{}, fmt.Errorf("claude parse: %w", err)
	}

	// Capture session_id from any event that carries it. We emit
	// SessionStart exactly once, on the first event that exposes a
	// non-empty session_id.
	if raw.SessionID != "" && !p.sessionEmitted {
		p.sessionID = raw.SessionID
		p.sessionEmitted = true
		return AgentEvent{
			Type:      SessionStart,
			SessionID: raw.SessionID,
			Raw:       trimmed,
		}, nil
	}

	switch raw.Type {
	case "content_block_start":
		if raw.ContentBlock == nil {
			return AgentEvent{Type: Unknown, Raw: trimmed}, nil
		}
		blk := claudeBlock{
			kind:     raw.ContentBlock.Type,
			toolName: raw.ContentBlock.Name,
		}
		p.blocks[raw.Index] = blk
		// content_block_start for tool_use is the natural moment to
		// announce ToolUse — but the gate needs the input JSON, which
		// only arrives in subsequent input_json_delta deltas. Defer
		// emission until content_block_stop so ToolInput is final.
		return AgentEvent{Type: Unknown, Raw: trimmed}, nil

	case "content_block_delta":
		if raw.Delta == nil {
			return AgentEvent{Type: Unknown, Raw: trimmed}, nil
		}
		blk := p.blocks[raw.Index]
		switch raw.Delta.Type {
		case "text_delta":
			return AgentEvent{Type: TextDelta, Text: raw.Delta.Text, Raw: trimmed}, nil
		case "thinking_delta":
			return AgentEvent{Type: Thinking, Text: raw.Delta.Thinking, Raw: trimmed}, nil
		case "input_json_delta":
			// Buffer the partial JSON; final ToolUse event fires on
			// content_block_stop with the full string.
			blk.inputBuf.WriteString(raw.Delta.PartialJSON)
			p.blocks[raw.Index] = blk
			return AgentEvent{Type: Unknown, Raw: trimmed}, nil
		}
		return AgentEvent{Type: Unknown, Raw: trimmed}, nil

	case "content_block_stop":
		blk, ok := p.blocks[raw.Index]
		delete(p.blocks, raw.Index)
		if !ok {
			return AgentEvent{Type: Unknown, Raw: trimmed}, nil
		}
		if blk.kind == "tool_use" {
			return AgentEvent{
				Type:      ToolUse,
				ToolName:  blk.toolName,
				ToolInput: blk.inputBuf.String(),
				Raw:       trimmed,
			}, nil
		}
		return AgentEvent{Type: Unknown, Raw: trimmed}, nil

	case "message_stop":
		return AgentEvent{Type: Done, SessionID: p.sessionID, Raw: trimmed}, nil

	case "error":
		msg := ""
		if raw.Error != nil {
			msg = raw.Error.Message
		}
		return AgentEvent{Type: Error, ErrorMsg: msg, Raw: trimmed}, nil
	}

	// Unknown event types (message_start, ping, ...) — pass through as
	// Unknown so the raw.jsonl writer still records them but the rest
	// of the pipeline ignores.
	return AgentEvent{Type: Unknown, Raw: trimmed}, nil
}

// SessionID returns the captured CLI session ID, or "" if no event
// carrying one has been seen yet. Used by store / agent to persist
// cli_session_id even when SessionStart was emitted earlier.
func (p *ClaudeParser) SessionID() string { return p.sessionID }
