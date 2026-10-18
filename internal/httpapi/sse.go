package httpapi

import (
	"fmt"
	"net/http"
	"sync"
)

type SSEEvent struct {
	Event string `json:"event"`
	Data  string `json:"data"`
}

type SSEHub struct {
	clients         map[chan SSEEvent]bool
	activityClients map[chan uint64]struct{}
	done            chan struct{}
	closeOnce       sync.Once
	sequence        uint64
	resetHistory    []uint64
	mu              sync.RWMutex
	logs            []string
	logMu           sync.RWMutex
}

func NewSSEHub() *SSEHub {

	hub := &SSEHub{
		clients:         make(map[chan SSEEvent]bool),
		activityClients: make(map[chan uint64]struct{}),
		done:            make(chan struct{}),
		logs:            nil,
	}

	return hub
}

// Close stops both stream types before HTTP handlers are drained.
func (h *SSEHub) Close() { h.closeOnce.Do(func() { close(h.done) }) }

func (h *SSEHub) BroadcastLog(logLine string) {
	h.logMu.Lock()
	h.logs = append(h.logs, logLine)
	if len(h.logs) > 300 {
		h.logs = h.logs[len(h.logs)-300:]
	}
	h.logMu.Unlock()

	h.Broadcast("", logLine)
}

func (h *SSEHub) Broadcast(event, data string) {
	h.InvalidateActivities()
	h.mu.RLock()
	defer h.mu.RUnlock()

	msg := SSEEvent{Event: event, Data: data}
	for ch := range h.clients {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (h *SSEHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v1/stream" {
		h.serveActivity(w, r)
		return
	}
	actor := GetActor(r.Context())
	if actor == nil {
		WriteJSONError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", GetRequestID(r.Context()))
		return
	}
	// These process-wide logs and events have no per-object owner. Restrict to
	// administrators until each event carries a verified object scope.
	if !actor.IsAdmin() {
		WriteJSONError(w, http.StatusForbidden, "FORBIDDEN", "administrator access required", GetRequestID(r.Context()))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan SSEEvent, 32)
	h.mu.Lock()
	h.clients[ch] = true
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.clients, ch)
		h.mu.Unlock()
		close(ch)
	}()

	_, _ = fmt.Fprint(w, "event: connected\ndata: {}\n\n")
	// Replay only genuine process-local events; do not synthesize infrastructure status.
	h.logMu.RLock()
	for _, l := range h.logs {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", l)
	}
	h.logMu.RUnlock()
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-h.done:
			return
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if msg.Event != "" {
				_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", msg.Event, msg.Data)
			} else {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", msg.Data)
			}
			flusher.Flush()
		}
	}
}
