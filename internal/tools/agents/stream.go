package agents

import (
	"sync"
)

// Hub is the SSE broadcaster. Handlers call Publish whenever an agent
// event arrives; SSE handler goroutines Subscribe and forward events
// to their client connections.
type Hub struct {
	mu      sync.RWMutex
	clients map[string][]chan string // sessionID → client channels
}

func newHub() *Hub {
	return &Hub{clients: map[string][]chan string{}}
}

// Subscribe returns a receive-only channel for events on sessionID and
// an unsubscribe func the caller must defer. Channel is buffered (32)
// so a slow HTTP flush doesn't stall the pool goroutine.
func (h *Hub) Subscribe(sessionID string) (<-chan string, func()) {
	ch := make(chan string, 32)
	h.mu.Lock()
	h.clients[sessionID] = append(h.clients[sessionID], ch)
	h.mu.Unlock()
	unsub := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		chans := h.clients[sessionID]
		for i, c := range chans {
			if c == ch {
				h.clients[sessionID] = append(chans[:i], chans[i+1:]...)
				break
			}
		}
		close(ch)
	}
	return ch, unsub
}

// Publish sends data to every subscriber of sessionID. Slow clients
// get their event dropped (non-blocking send).
func (h *Hub) Publish(sessionID, data string) {
	h.mu.RLock()
	chans := h.clients[sessionID]
	h.mu.RUnlock()
	for _, ch := range chans {
		select {
		case ch <- data:
		default:
		}
	}
}
