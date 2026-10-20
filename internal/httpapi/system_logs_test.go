package httpapi

import (
	"bufio"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func logActor(role string) *ports.Actor {
	return &ports.Actor{Type: ports.ActorTypeUser, ID: "11111111-1111-1111-1111-111111111111", Role: role}
}

func TestStreamSystemLogsAuthorization(t *testing.T) {
	handler := &ServerHandler{}
	for name, tc := range map[string]struct {
		actor  *ports.Actor
		level  *StreamSystemLogsParamsLevel
		assert func(t *testing.T, response StreamSystemLogsResponseObject)
	}{
		"anonymous": {nil, nil, func(t *testing.T, response StreamSystemLogsResponseObject) {
			if _, ok := response.(StreamSystemLogs401ApplicationProblemPlusJSONResponse); !ok {
				t.Fatalf("want 401, got %T", response)
			}
		}},
		"member": {logActor(ports.RoleMember), nil, func(t *testing.T, response StreamSystemLogsResponseObject) {
			if _, ok := response.(StreamSystemLogs403ApplicationProblemPlusJSONResponse); !ok {
				t.Fatalf("want 403, got %T", response)
			}
		}},
		"service actor": {&ports.Actor{Type: ports.ActorTypeService, ID: "svc", Role: ports.RoleAdmin}, nil, func(t *testing.T, response StreamSystemLogsResponseObject) {
			if _, ok := response.(StreamSystemLogs403ApplicationProblemPlusJSONResponse); !ok {
				t.Fatalf("want 403 for non-user actor, got %T", response)
			}
		}},
		"admin without hub": {logActor(ports.RoleAdmin), nil, func(t *testing.T, response StreamSystemLogsResponseObject) {
			if _, ok := response.(StreamSystemLogs503ApplicationProblemPlusJSONResponse); !ok {
				t.Fatalf("want 503, got %T", response)
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if tc.actor != nil {
				ctx = context.WithValue(ctx, actorKey, tc.actor)
			}
			response, err := handler.StreamSystemLogs(ctx, StreamSystemLogsRequestObject{Params: StreamSystemLogsParams{Level: tc.level}})
			if err != nil {
				t.Fatal(err)
			}
			tc.assert(t, response)
		})
	}
}

func TestStreamSystemLogsAdminGetsStream(t *testing.T) {
	hub := NewSSEHub()
	t.Cleanup(hub.Close)
	handler := &ServerHandler{logHub: hub}
	ctx := context.WithValue(context.Background(), actorKey, logActor(ports.RoleAdmin))
	level := WARN
	response, err := handler.StreamSystemLogs(ctx, StreamSystemLogsRequestObject{Params: StreamSystemLogsParams{Level: &level}})
	if err != nil {
		t.Fatal(err)
	}
	stream, ok := response.(systemLogsResponse)
	if !ok {
		t.Fatalf("want stream response, got %T", response)
	}
	if stream.level != slog.LevelWarn {
		t.Fatalf("level %v", stream.level)
	}
	if stream.hub != hub {
		t.Fatal("stream did not use the injected hub")
	}
}

func TestParseLogLevelFallsBackToInfo(t *testing.T) {
	for value, want := range map[string]slog.Level{
		"DEBUG": slog.LevelDebug, "debug": slog.LevelDebug, "WARN": slog.LevelWarn,
		"ERROR": slog.LevelError, "INFO": slog.LevelInfo, "": slog.LevelInfo, "trace": slog.LevelInfo,
	} {
		if got := ParseLogLevel(value); got != want {
			t.Fatalf("%q: got %v want %v", value, got, want)
		}
	}
}

// readLogStream collects data lines until predicate matches or the deadline expires.
func readLogStream(t *testing.T, ctx context.Context, hub *SSEHub, minimum slog.Level, want func([]string) bool) []string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.ServeLogs(w, r, minimum)
	}))
	defer server.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/system/logs", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var lines []string
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			lines = append(lines, strings.TrimPrefix(line, "data: "))
			if want(lines) {
				return lines
			}
		}
	}
	return lines
}

func TestLiveLogStreamFiltersByLevelAndNeverLeaks(t *testing.T) {
	hub := NewSSEHub()
	t.Cleanup(hub.Close)
	// Pre-existing entries are replayed to a new subscriber.
	hub.PublishLog(slog.LevelDebug, "debug-before")
	hub.PublishLog(slog.LevelInfo, "info-before")
	hub.PublishLog(slog.LevelWarn, "warn-before")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan []string, 1)
	go func() {
		done <- readLogStream(t, ctx, hub, slog.LevelWarn, func(lines []string) bool {
			return strings.Contains(strings.Join(lines, "\n"), "error-after")
		})
	}()
	time.Sleep(50 * time.Millisecond)
	hub.PublishLog(slog.LevelInfo, "info-after")
	hub.PublishLog(slog.LevelError, "error-after")
	var lines []string
	select {
	case lines = <-done:
	case <-ctx.Done():
		t.Fatal("stream did not deliver the error line")
	}
	joined := strings.Join(lines, "\n")
	for _, forbidden := range []string{"debug-before", "info-before", "info-after"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("level filter leaked %q: %s", forbidden, joined)
		}
	}
	if !strings.Contains(joined, "warn-before") {
		t.Fatalf("missing replay at or above the filter: %s", joined)
	}
}

func TestPublishLogDoesNotInvalidateActivity(t *testing.T) {
	hub := NewSSEHub()
	t.Cleanup(hub.Close)
	activity := make(chan uint64, 8)
	hub.mu.Lock()
	hub.activityClients[activity] = struct{}{}
	hub.mu.Unlock()
	hub.PublishLog(slog.LevelInfo, "ordinary log")
	select {
	case id := <-activity:
		t.Fatalf("log publication invalidated activity with %d", id)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHubReplayIsBounded(t *testing.T) {
	hub := NewSSEHub()
	t.Cleanup(hub.Close)
	for i := 0; i < MaxLiveLogEntries+50; i++ {
		hub.PublishLog(slog.LevelInfo, "line")
	}
	hub.logMu.RLock()
	count := len(hub.logs)
	hub.logMu.RUnlock()
	if count != MaxLiveLogEntries {
		t.Fatalf("replay buffer %d, want %d", count, MaxLiveLogEntries)
	}
}
