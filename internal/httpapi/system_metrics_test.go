package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type metricsRead func(context.Context) (ports.RuntimeMetrics, error)

func (f metricsRead) ReadRuntimeMetrics(c context.Context) (ports.RuntimeMetrics, error) { return f(c) }
func TestSystemMetricsAuthorizationAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		actor  *ports.Actor
		status int
	}{
		{"anonymous", nil, 401},
		{"member read", &ports.Actor{Type: ports.ActorTypeUser, Role: ports.RoleMember, Scopes: []string{"read"}}, 403},
		{"system", &ports.Actor{Type: ports.ActorTypeSystem, Role: ports.RoleAdmin}, 403},
		{"token without read", &ports.Actor{Type: ports.ActorTypeUser, Role: ports.RoleAdmin, Scopes: []string{}}, 403},
		{"role scope cannot elevate", &ports.Actor{Type: ports.ActorTypeUser, Role: ports.RoleMember, Scopes: []string{"admin", "read"}}, 403},
		{"admin session", &ports.Actor{Type: ports.ActorTypeUser, Role: ports.RoleAdmin}, 200},
		{"admin read", &ports.Actor{Type: ports.ActorTypeUser, Role: ports.RoleAdmin, Scopes: []string{"read"}}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			failure := false
			m := newRequestMetrics(metricsRead(func(context.Context) (ports.RuntimeMetrics, error) {
				calls++
				if failure {
					return ports.RuntimeMetrics{}, errors.New("postgres://private-secret")
				}
				return ports.RuntimeMetrics{PoolMax: 4}, nil
			}))
			handler := metricsNoStore(ProblemResponseMiddleware()(APITokenScopeMiddleware()(Handler(NewStrictHandler(&ServerHandler{metrics: m}, nil)))))
			request := httptest.NewRequest(http.MethodGet, "/api/v1/system/metrics?secret=private-query", nil)
			if tc.actor != nil {
				request = request.WithContext(context.WithValue(request.Context(), actorKey, tc.actor))
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status || response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("status/cache %d %v", response.Code, response.Header())
			}
			if tc.status == 200 {
				if calls != 1 || response.Header().Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" || !strings.Contains(response.Body.String(), "pulse_db_pool_max_connections 4\n") {
					t.Fatal(response.Body.String())
				}
				failure = true
				response = httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != 503 || strings.Contains(response.Body.String(), "pulse_queue_tasks") {
					t.Fatal("unavailable observation represented as metrics")
				}
			} else if calls != 0 {
				t.Fatal("unauthorized database read")
			}
			if strings.Contains(response.Body.String(), "private-") {
				t.Fatal("sensitive value leaked")
			}
		})
	}
}
func TestSystemMetricsBoundedLabelsConcurrentRequestsAndStreaming(t *testing.T) {
	m := newRequestMetrics(nil)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /objects/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202); _, _ = w.Write([]byte("ok")) })
	handler := requestDiagnostics(slog.New(slog.NewTextHandler(io.Discard, nil)), m, func(r *http.Request) string { _, p := mux.Handler(r); return p })(mux)
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/objects/private-id?cookie=private-cookie", nil))
		})
		wg.Go(func() {
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("PRIVATE-METHOD", "/private-path", nil))
		})
	}
	wg.Wait()
	text := m.text(ports.RuntimeMetrics{})
	if strings.Contains(text, `state="stalled"`) {
		t.Fatal("invented queue state")
	}
	for _, fragment := range []string{"pulse_http_requests_in_flight 0\n", "pulse_http_requests_total{route=\"GET /objects/{id}\",method=\"GET\",status=\"202\"} 50\n", "pulse_http_request_duration_seconds_count{route=\"GET /objects/{id}\",method=\"GET\",status=\"202\"} 50\n", "pulse_http_response_bytes_total{route=\"GET /objects/{id}\",method=\"GET\",status=\"202\"} 100\n", "pulse_http_requests_total{route=\"unmatched\",method=\"OTHER\",status=\"404\"} 50\n"} {
		if !strings.Contains(text, fragment) {
			t.Errorf("missing %s", fragment)
		}
	}
	if len(m.entries) != 2 || strings.Contains(text, "private-") || strings.Contains(text, "PRIVATE-METHOD") {
		t.Fatal("unbounded labels")
	}
	stream := requestDiagnostics(slog.New(slog.NewTextHandler(io.Discard, nil)), m)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(m.text(ports.RuntimeMetrics{}), "pulse_http_requests_in_flight 1\n") {
			t.Error("open stream not counted")
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
		}
	}))
	response := httptest.NewRecorder()
	stream.ServeHTTP(response, httptest.NewRequest("GET", "/stream", nil))
	if !response.Flushed || response.Code != 200 {
		t.Fatal("streaming changed")
	}
	if !strings.Contains(m.text(ports.RuntimeMetrics{}), "pulse_http_requests_in_flight 0\n") {
		t.Fatal("active stream leaked")
	}
	if strings.Contains(newRequestMetrics(nil).text(ports.RuntimeMetrics{}), "/objects/") {
		t.Fatal("router metrics shared globally")
	}
}

func TestSystemMetricsLockWaitExposition(t *testing.T) {
	text := newRequestMetrics(nil).text(ports.RuntimeMetrics{LockWaiters: 2, LockWaitAgeSeconds: 3.25})
	for _, want := range []string{"# TYPE pulse_db_lock_waiters gauge\n", "pulse_db_lock_waiters 2\n", "# TYPE pulse_db_lock_wait_age_seconds gauge\n", "pulse_db_lock_wait_age_seconds 3.25\n"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(text, "pulse_db_lock_waiters{") || strings.Contains(text, "pulse_db_lock_wait_age_seconds{") {
		t.Fatal("lock identities must not be labels")
	}
}

func TestSystemMetricsBudgetAndStageExposition(t *testing.T) {
	snapshot := ports.RuntimeMetrics{IndexerBudget: ports.IndexerBudgetMetrics{Open: 2, Interval: 1, Cooldown: 3, MaxDelaySeconds: 12}}
	snapshot.Stages[ports.StageLibraryScan] = ports.WorkStageMetrics{InFlight: 1, Returned: 2, Seconds: 1.5, Buckets: [7]uint64{0, 1, 2, 2, 2, 2, 2}}
	text := newRequestMetrics(nil).text(snapshot)
	for _, want := range []string{
		"pulse_indexer_budget_endpoints{state=\"open\"} 2\n",
		"pulse_indexer_budget_endpoints{state=\"interval\"} 1\n",
		"pulse_indexer_budget_endpoints{state=\"cooldown\"} 3\n",
		"pulse_indexer_budget_delay_seconds 12\n",
		"pulse_work_stage_in_flight{stage=\"library_scan\"} 1\n",
		"pulse_work_stage_duration_seconds_bucket{stage=\"library_scan\",le=\"1\"} 1\n",
		"pulse_work_stage_duration_seconds_bucket{stage=\"library_scan\",le=\"+Inf\"} 2\n",
		"pulse_work_stage_duration_seconds_count{stage=\"library_scan\"} 2\n",
		"pulse_work_stage_duration_seconds_sum{stage=\"library_scan\"} 1.5\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Count(text, "pulse_work_stage_in_flight{stage=") != int(ports.WorkStageCount) {
		t.Fatal("stage label set is not fixed")
	}
}
