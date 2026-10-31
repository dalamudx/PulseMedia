package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/diagnostics"
)

type requestMetricKey struct {
	route, method string
	status        int
}
type requestMetric struct {
	count, bytes uint64
	seconds      float64
	buckets      [7]uint64
}
type requestMetrics struct {
	mu       sync.Mutex
	entries  map[requestMetricKey]requestMetric
	inFlight int64
	runtime  *diagnostics.Service
}

func newRequestMetrics(reader ports.RuntimeMetricsReader) *requestMetrics {
	return &requestMetrics{entries: make(map[requestMetricKey]requestMetric), runtime: diagnostics.NewService(reader)}
}
func durationBounds() [7]float64 { return [7]float64{.005, .025, .1, .5, 2, 10, 30} }
func (m *requestMetrics) observe(route, method string, status int, seconds float64, bytes int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inFlight--
	key := requestMetricKey{route, method, status}
	sample := m.entries[key]
	sample.count++
	sample.bytes += uint64(max(bytes, 0))
	sample.seconds += seconds
	for i, bound := range durationBounds() {
		if seconds <= bound {
			sample.buckets[i]++
		}
	}
	m.entries[key] = sample
}
func metricHeader(b *strings.Builder, name, kind, help string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
}
func (m *requestMetrics) text(runtime ports.RuntimeMetrics) string {
	var b strings.Builder
	m.mu.Lock()
	entries := make(map[requestMetricKey]requestMetric, len(m.entries))
	for k, v := range m.entries {
		entries[k] = v
	}
	active := m.inFlight
	m.mu.Unlock()
	keys := make([]requestMetricKey, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, c := keys[i], keys[j]
		if a.route != c.route {
			return a.route < c.route
		}
		if a.method != c.method {
			return a.method < c.method
		}
		return a.status < c.status
	})
	metricHeader(&b, "pulse_http_requests_in_flight", "gauge", "Requests currently being served, including open streams and this scrape.")
	fmt.Fprintf(&b, "pulse_http_requests_in_flight %d\n", active)
	metricHeader(&b, "pulse_http_requests_total", "counter", "Completed HTTP requests since process start.")
	for _, key := range keys {
		fmt.Fprintf(&b, "pulse_http_requests_total{route=%q,method=%q,status=%q} %d\n", key.route, key.method, fmt.Sprint(key.status), entries[key].count)
	}
	metricHeader(&b, "pulse_http_response_bytes_total", "counter", "Bytes written for completed HTTP responses.")
	for _, key := range keys {
		fmt.Fprintf(&b, "pulse_http_response_bytes_total{route=%q,method=%q,status=%q} %d\n", key.route, key.method, fmt.Sprint(key.status), entries[key].bytes)
	}
	metricHeader(&b, "pulse_http_request_duration_seconds", "histogram", "Duration of completed HTTP requests, including stream lifetimes.")
	for _, key := range keys {
		sample := entries[key]
		labels := fmt.Sprintf("route=%q,method=%q,status=%q", key.route, key.method, fmt.Sprint(key.status))
		for i, bound := range durationBounds() {
			fmt.Fprintf(&b, "pulse_http_request_duration_seconds_bucket{%s,le=%q} %d\n", labels, fmt.Sprint(bound), sample.buckets[i])
		}
		fmt.Fprintf(&b, "pulse_http_request_duration_seconds_bucket{%s,le=\"+Inf\"} %d\npulse_http_request_duration_seconds_sum{%s} %g\npulse_http_request_duration_seconds_count{%s} %d\n", labels, sample.count, labels, sample.seconds, labels, sample.count)
	}
	for _, metric := range []struct {
		name, kind, help string
		value            float64
	}{
		{"pulse_db_pool_connections", "gauge", "Total open database connections.", float64(runtime.PoolTotal)},
		{"pulse_db_pool_idle_connections", "gauge", "Idle database connections.", float64(runtime.PoolIdle)},
		{"pulse_db_pool_acquired_connections", "gauge", "Acquired database connections.", float64(runtime.PoolAcquired)},
		{"pulse_db_pool_max_connections", "gauge", "Configured pool capacity.", float64(runtime.PoolMax)},
		{"pulse_db_pool_acquires_total", "counter", "Successful pool acquires since process start.", float64(runtime.AcquireCount)},
		{"pulse_db_pool_empty_acquires_total", "counter", "Acquires that waited on an empty pool.", float64(runtime.EmptyAcquireCount)},
		{"pulse_db_pool_cancelled_acquires_total", "counter", "Cancelled pool acquires.", float64(runtime.CancelledAcquireCount)},
		{"pulse_db_pool_acquire_seconds_total", "counter", "Time spent acquiring pool connections.", runtime.AcquireSeconds},
		{"pulse_db_pool_wait_seconds_total", "counter", "Time spent waiting on an empty pool.", runtime.EmptyAcquireSeconds},
		{"pulse_db_lock_waiters", "gauge", "Connections waiting for a database lock in the current database and login role.", float64(runtime.LockWaiters)},
		{"pulse_db_lock_wait_age_seconds", "gauge", "Oldest current lock wait in seconds, scoped to the current database and login role; not query age.", runtime.LockWaitAgeSeconds},
	} {
		metricHeader(&b, metric.name, metric.kind, metric.help)
		fmt.Fprintf(&b, "%s %g\n", metric.name, metric.value)
	}
	for _, metric := range []struct {
		name, help string
		value      func(ports.QueueMetrics) float64
	}{
		{"pulse_queue_tasks", "Current active or faulted tasks, excluding successful retained history.", func(q ports.QueueMetrics) float64 { return float64(q.Count) }},
		{"pulse_queue_ready_age_seconds", "Oldest runnable task delay after not_before; future tasks contribute zero.", func(q ports.QueueMetrics) float64 { return q.ReadyAgeSeconds }},
		{"pulse_queue_expired_leases", "Executing tasks with expired logical leases; not permission to retry.", func(q ports.QueueMetrics) float64 { return float64(q.ExpiredLeases) }},
	} {
		metricHeader(&b, metric.name, "gauge", metric.help)
		for _, queue := range []string{"job", "outbox"} {
			states := []string{"queued", "running", "failed", "dead"}
			if queue == "outbox" {
				states = []string{"pending", "delivering", "failed", "dead"}
			}
			for _, state := range states {
				value := float64(0)
				for _, q := range runtime.Queues {
					if q.Queue == queue && q.State == state {
						value = metric.value(q)
					}
				}
				fmt.Fprintf(&b, "%s{queue=%q,state=%q} %g\n", metric.name, queue, state, value)
			}
		}
	}
	metricHeader(&b, "pulse_indexer_budget_endpoints", "gauge", "Indexer time gates, independent of health or execution authorization; cooldown takes precedence over interval.")
	for _, sample := range []struct {
		state string
		count int64
	}{{"open", runtime.IndexerBudget.Open}, {"interval", runtime.IndexerBudget.Interval}, {"cooldown", runtime.IndexerBudget.Cooldown}} {
		fmt.Fprintf(&b, "pulse_indexer_budget_endpoints{state=%q} %d\n", sample.state, sample.count)
	}
	metricHeader(&b, "pulse_indexer_budget_delay_seconds", "gauge", "Largest remaining indexer time gate delay; not an execution promise.")
	fmt.Fprintf(&b, "pulse_indexer_budget_delay_seconds %g\n", runtime.IndexerBudget.MaxDelaySeconds)
	metricHeader(&b, "pulse_work_stage_in_flight", "gauge", "Calls still executing in instrumented workers, including calls past cancellation.")
	for stage, label := range ports.WorkStageLabels() {
		fmt.Fprintf(&b, "pulse_work_stage_in_flight{stage=%q} %d\n", label, runtime.Stages[stage].InFlight)
	}
	metricHeader(&b, "pulse_work_stage_failures_total", "counter", "Failed periodic scheduling cycles since App construction; excludes caller cancellation and does not count individual configurations.")
	for stage, label := range ports.WorkStageLabels() {
		fmt.Fprintf(&b, "pulse_work_stage_failures_total{stage=%q} %d\n", label, runtime.Stages[stage].Failures)
	}
	metricHeader(&b, "pulse_work_stage_duration_seconds", "histogram", "Elapsed duration of returned worker calls since App construction; return does not imply business success. Includes internal waits, excludes queue residence and subsequent worker bookkeeping.")
	for stage, label := range ports.WorkStageLabels() {
		s := runtime.Stages[stage]
		for i, bound := range ports.WorkStageDurationBounds() {
			fmt.Fprintf(&b, "pulse_work_stage_duration_seconds_bucket{stage=%q,le=%q} %d\n", label, fmt.Sprint(bound), s.Buckets[i])
		}
		fmt.Fprintf(&b, "pulse_work_stage_duration_seconds_bucket{stage=%q,le=\"+Inf\"} %d\n", label, s.Returned)
		fmt.Fprintf(&b, "pulse_work_stage_duration_seconds_count{stage=%q} %d\n", label, s.Returned)
		fmt.Fprintf(&b, "pulse_work_stage_duration_seconds_sum{stage=%q} %g\n", label, s.Seconds)
	}
	return b.String()
}

type systemMetricsResponse string

func (body systemMetricsResponse) VisitReadSystemMetricsResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	_, err := w.Write([]byte(body))
	return err
}
func (s *ServerHandler) ReadSystemMetrics(ctx context.Context, _ ReadSystemMetricsRequestObject) (ReadSystemMetricsResponseObject, error) {
	collector := s.metrics
	if collector == nil {
		collector = newRequestMetrics(nil)
	}
	runtime, err := collector.runtime.Read(ctx, GetActor(ctx))
	requestID := GetRequestID(ctx)
	if errors.Is(err, ports.ErrUnauthorized) {
		return ReadSystemMetrics401ApplicationProblemPlusJSONResponse(problemResponse(401, "UNAUTHORIZED", "authentication required", requestID)), nil
	}
	if errors.Is(err, ports.ErrForbidden) {
		return ReadSystemMetrics403ApplicationProblemPlusJSONResponse(problemResponse(403, "FORBIDDEN", "administrator read access required", requestID)), nil
	}
	if err != nil {
		return ReadSystemMetrics503ApplicationProblemPlusJSONResponse(problemResponse(503, "UNAVAILABLE", "metrics unavailable", requestID)), nil
	}
	return systemMetricsResponse(collector.text(runtime)), nil
}
func metricsNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/system/metrics" {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		next.ServeHTTP(w, r)
	})
}
