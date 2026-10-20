package httpapi

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestActivityStreamOnlyResetAndReconnectCleanup(t *testing.T) {
	hub := NewSSEHub()
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: "synthetic-member", Role: ports.RoleMember}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey, actor)))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/stream", nil)
	req.Header.Set("Last-Event-ID", "old-instance:999999")
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("member stream", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	read := func() {
		t.Helper()
		event, data := "", ""
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				event = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
			if line == "" {
				break
			}
		}
		if event != "reset" || data != "{}" {
			t.Fatal("unsafe stream", event, data)
		}
	}
	read()

	// A log line is not an activity change: it must neither reach this stream
	// nor synthesize a reset hint for scoped GET requeries.
	var mu sync.Mutex
	frames := make(chan string, 8)
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		event, data := "", ""
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				event = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
			if line == "" {
				mu.Lock()
				frames <- event + "|" + data
				mu.Unlock()
				event, data = "", ""
			}
		}
	}()
	hub.BroadcastLog("PRIVATE-CREDENTIAL https://synthetic.invalid/?api_key=secret")
	select {
	case frame := <-frames:
		t.Fatalf("log publication reached the activity stream: %q", frame)
	case <-time.After(250 * time.Millisecond):
	}

	hub.Close()
	hub.Close()
	readers.Wait()
	if e = resp.Body.Close(); e != nil {
		t.Fatal(e)
	}
	cancel()
	for i := 0; i < 200; i++ {
		hub.InvalidateActivities()
	}
	hub.mu.RLock()
	size := len(hub.resetHistory)
	hub.mu.RUnlock()
	if size != 128 {
		t.Fatal("unbounded reset history", size)
	}
	server.Close()
	hub.mu.RLock()
	clients := len(hub.activityClients)
	hub.mu.RUnlock()
	if clients != 0 {
		t.Fatal("stream subscription leaked", clients)
	}
	rr := httptest.NewRecorder()
	hub.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/events", nil).WithContext(context.WithValue(context.Background(), actorKey, actor)))
	if rr.Code != 403 {
		t.Fatal("member log access", rr.Code)
	}
	body, _ := io.ReadAll(rr.Result().Body)
	if strings.Contains(string(body), "PRIVATE-CREDENTIAL") {
		t.Fatal("logs exposed")
	}
}
