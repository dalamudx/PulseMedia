package app

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
)

// runtimeHTTP owns accepted handlers even when HTTP shutdown reaches its
// deadline. The admission gate prevents an Add racing with the final Wait.
type runtimeHTTP struct {
	handler http.Handler
	mu      sync.Mutex
	stopped bool
	active  sync.WaitGroup
}

func (h *runtimeHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	h.active.Add(1)
	h.mu.Unlock()
	defer h.active.Done()
	h.handler.ServeHTTP(w, r)
}

func (h *runtimeHTTP) stopAccepting() { h.mu.Lock(); h.stopped = true; h.mu.Unlock() }
func (h *runtimeHTTP) wait()          { h.active.Wait() }

// shutdown retains physical ownership after the HTTP drain deadline.
func (h *runtimeHTTP) shutdown(ctx context.Context, server *http.Server, serverDone <-chan struct{}, logger *slog.Logger) error {
	h.stopAccepting()
	err := server.Shutdown(ctx)
	if err != nil {
		logger.Warn("HTTP drain deadline elapsed; waiting for owned handlers")
		_ = server.Close()
	}
	<-serverDone
	h.wait()
	return err
}
