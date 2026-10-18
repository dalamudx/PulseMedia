package httpapi

import (
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"strings"
	"time"
)

// Activity resets carry no private object data. All state is re-read through the scoped GET port.
func (h *SSEHub) activityReset() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sequence++
	id := h.sequence
	h.resetHistory = append(h.resetHistory, id)
	if len(h.resetHistory) > 128 {
		h.resetHistory = h.resetHistory[len(h.resetHistory)-128:]
	}
	return id
}
func (h *SSEHub) InvalidateActivities() {
	id := h.activityReset()
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.activityClients {
		select {
		case ch <- id:
		default:
		}
	}
}
func (h *SSEHub) serveActivity(w http.ResponseWriter, r *http.Request) {
	a := GetActor(r.Context())
	if a == nil {
		WriteJSONError(w, 401, "UNAUTHORIZED", "authentication required", GetRequestID(r.Context()))
		return
	}
	if a.Type != ports.ActorTypeUser || (!a.IsAdmin() && a.Role != ports.RoleMember) {
		WriteJSONError(w, 403, "FORBIDDEN", "user access required", GetRequestID(r.Context()))
		return
	}
	if len(a.Scopes) > 0 {
		read := false
		for _, s := range a.Scopes {
			if s == "read" {
				read = true
			}
		}
		if !read {
			WriteJSONError(w, 403, "FORBIDDEN", "read scope required", GetRequestID(r.Context()))
			return
		}
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteJSONError(w, 500, "UNAVAILABLE", "stream unavailable", GetRequestID(r.Context()))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	ch := make(chan uint64, 32)
	h.mu.Lock()
	h.activityClients[ch] = struct{}{}
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.activityClients, ch); h.mu.Unlock() }()
	write := func(id uint64) bool {
		_, e := fmt.Fprintf(w, "id: %d\nevent: reset\ndata: {}\n\n", id)
		if e != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	// Always reset on connect/reconnect: process restarts and lost hints have no durable replay guarantee.
	if !write(h.activityReset()) {
		return
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-r.Context().Done():
			return
		case id := <-ch:
			if !write(id) {
				return
			}
		case <-ticker.C:
			if !write(h.activityReset()) {
				return
			}
		}
	}
}

func activityChanges(hub *SSEHub, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/operations") {
			w.Header().Set("Cache-Control", "no-store")
		}
		// This POST only compares a snapshot. Resetting activity here erases its own comparison.
		parts := strings.Split(r.URL.Path, "/")
		readOnlyUpgradePreview := r.Method == http.MethodPost && len(parts) == 6 && parts[1] == "api" && parts[2] == "v1" && parts[3] == "subscriptions" && parts[4] != "" && parts[5] == "upgrade-previews"
		if hub == nil || readOnlyUpgradePreview || r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		rec := &responseRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.statusCode >= 200 && rec.statusCode < 300 && !strings.HasPrefix(r.URL.Path, "/api/v1/auth/") {
			hub.InvalidateActivities()
		}
	})
}
