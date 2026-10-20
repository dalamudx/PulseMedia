package httpapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// MaxLiveLogEntries bounds replay memory for the live log stream.
const MaxLiveLogEntries = 300

type SSEEvent struct {
	Event string `json:"event"`
	Data  string `json:"data"`
}

// LogRecord is one already-redacted process log line held for replay.
type LogRecord struct {
	At    time.Time `json:"at"`
	Level string    `json:"level"`
	Line  string    `json:"line"`
}

type logSubscription struct {
	ch       chan LogRecord
	minimum  slog.Level
	finished chan struct{}
}

type SSEHub struct {
	clients         map[chan SSEEvent]bool
	activityClients map[chan uint64]struct{}
	logClients      map[*logSubscription]struct{}
	done            chan struct{}
	closeOnce       sync.Once
	sequence        uint64
	resetHistory    []uint64
	mu              sync.RWMutex
	logs            []LogRecord
	logMu           sync.RWMutex
}

func NewSSEHub() *SSEHub {
	return &SSEHub{
		clients:         make(map[chan SSEEvent]bool),
		activityClients: make(map[chan uint64]struct{}),
		logClients:      make(map[*logSubscription]struct{}),
		done:            make(chan struct{}),
	}
}

// Close stops every stream type before HTTP handlers are drained.
func (h *SSEHub) Close() { h.closeOnce.Do(func() { close(h.done) }) }

// PublishLog appends one already-redacted line and fans it out to log streams
// at or above their minimum level. Logs never invalidate activity.
func (h *SSEHub) PublishLog(level slog.Level, line string) {
	if h == nil || strings.TrimSpace(line) == "" {
		return
	}
	record := LogRecord{At: time.Now().UTC(), Level: logLevelName(level), Line: line}
	h.logMu.Lock()
	h.logs = append(h.logs, record)
	if len(h.logs) > MaxLiveLogEntries {
		h.logs = append([]LogRecord(nil), h.logs[len(h.logs)-MaxLiveLogEntries:]...)
	}
	h.logMu.Unlock()

	h.mu.RLock()
	defer h.mu.RUnlock()
	for sub := range h.logClients {
		if level < sub.minimum {
			continue
		}
		select {
		case sub.ch <- record:
		default:
		}
	}
}

// BroadcastLog keeps the line-oriented helper used by existing callers.
func (h *SSEHub) BroadcastLog(logLine string) { h.PublishLog(slog.LevelInfo, logLine) }

func logLevelName(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return "ERROR"
	case level >= slog.LevelWarn:
		return "WARN"
	case level >= slog.LevelInfo:
		return "INFO"
	default:
		return "DEBUG"
	}
}

// ParseLogLevel maps a stream filter to a slog level; unknown values fall back
// to INFO so a malformed filter cannot widen visibility.
func ParseLogLevel(value string) slog.Level {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
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

// ServeLogs streams live process logs. The caller has already authorized the
// request; this function performs no permission decision of its own.
func (h *SSEHub) ServeLogs(w http.ResponseWriter, r *http.Request, minimum slog.Level) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteJSONError(w, http.StatusInternalServerError, "UNAVAILABLE", "log stream unavailable", GetRequestID(r.Context()))
		return
	}
	sub := &logSubscription{ch: make(chan LogRecord, 64), minimum: minimum, finished: make(chan struct{})}
	h.mu.Lock()
	h.logClients[sub] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.logClients, sub)
		h.mu.Unlock()
		close(sub.finished)
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	write := func(record LogRecord) bool {
		if ParseLogLevel(record.Level) < minimum {
			return true
		}
		payload, err := json.Marshal(record)
		if err != nil {
			return false
		}
		if _, err = fmt.Fprintf(w, "event: log\ndata: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	_, _ = fmt.Fprint(w, "event: connected\ndata: {}\n\n")
	h.logMu.RLock()
	replay := append([]LogRecord(nil), h.logs...)
	h.logMu.RUnlock()
	flusher.Flush()
	for _, record := range replay {
		if !write(record) {
			return
		}
	}

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-r.Context().Done():
			return
		case record := <-sub.ch:
			if !write(record) {
				return
			}
		case <-keepAlive.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
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
	// These process-wide events have no per-object owner. Restrict to
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
