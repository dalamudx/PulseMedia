//go:build linux

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestM8UntrackedReviewRuntimeCookiePG(t *testing.T) {
	if os.Getenv("PULSE_M8_UNTRACKED_REVIEW_ACCEPTANCE") != "1" {
		t.Skip("explicit owned review acceptance required")
	}
	for _, scenario := range []string{"confirmed_receipt", "disabled_during_metadata", "file_changed_before_confirmation"} {
		t.Run(scenario, func(t *testing.T) { m8RunUntrackedReview(t, scenario) })
	}
}

func m8ReviewBusiness(t *testing.T, f *m8ScopeConfigFixture) string {
	return m8ConfigSnapshot(t, f.m7ScrapeFixture, "media", "media_identity", "media_unit", "subscription", "wanted_item", "library_file", "library_file_unit", "unit_fact", "download_task", "download_client", "library_root", "scan_run", "scan_finalization")
}

func m8RunUntrackedReview(t *testing.T, scenario string) {
	t.Helper()
	t.Setenv("TMDB_API_KEY", "owned-untracked-review")
	t.Setenv("PULSE_TMDB_API_KEY", "")
	f := m8NewScopeConfigFixture(t)
	var metadataCalls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadataCalls.Add(1)
		if r.Method != "GET" || r.URL.Query().Get("api_key") != "owned-untracked-review" || r.URL.Query().Get("language") != "zh-CN" {
			t.Errorf("native review metadata contract method=%s path=%s key_matches=%t locale_matches=%t", r.Method, r.URL.Path, r.URL.Query().Get("api_key")=="owned-untracked-review", r.URL.Query().Get("language")=="zh-CN")
			w.WriteHeader(400)
			return
		}
		movie := `{"id":551,"media_type":"movie","title":"Movie","original_title":"Movie","release_date":"2026-01-01","overview":"Owned review fixture","vote_average":8}`
		body := ""
		switch r.URL.Path {
		case "/3/search/movie":
			if r.URL.Query().Get("query") != "Movie" || r.URL.Query().Get("year") != "2026" {
				t.Error("review search lost parsed identity")
				w.WriteHeader(400)
				return
			}
			body = `{"page":1,"total_pages":1,"total_results":1,"results":[` + movie + `]}`
		case "/3/movie/551":
			if scenario == "disabled_during_metadata" {
				enterOnce.Do(func() { close(entered) })
				<-release
			}
			body = movie
		default:
			t.Error("unexpected review metadata path")
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, body); err != nil && r.Context().Err() == nil {
			t.Error("native review response failed")
		}
	}))
	t.Cleanup(metadata.Close)
	target, err := url.Parse(metadata.URL)
	if err != nil {
		t.Fatal("owned metadata URL")
	}
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	f.app.metadataHTTPClient = &http.Client{Transport: m8WishRoute{target: target, transport: transport}, Timeout: 10 * time.Second}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	f.app.cfg.Server.HTTPAddr = address
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- f.app.Run(ctx) }()
	defer func() {
		releaseOnce.Do(func() { close(release) })
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("review App shutdown: %v", err)
			}
		case <-time.After(45 * time.Second):
			t.Error("review App failed to drain actual I/O")
		}
	}()
	httpTransport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 3 * time.Second}
	defer httpTransport.CloseIdleConnections()
	client := &http.Client{Transport: httpTransport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	call := func(method, path, body, key string) (int, []byte, error) {
		req, e := http.NewRequestWithContext(t.Context(), method, "http://"+address+path, strings.NewReader(body))
		if e != nil {
			return 0, nil, e
		}
		req.AddCookie(f.cookie)
		req.Header.Set("Origin", "http://"+address)
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		res, e := client.Do(req)
		if e != nil {
			return 0, nil, e
		}
		defer func() { _ = res.Body.Close() }()
		b, e := io.ReadAll(io.LimitReader(res.Body, 1024*1024))
		if res.Header.Get("Cache-Control") != "private, no-store" {
			return res.StatusCode, nil, fmt.Errorf("review response missing private no-store")
		}
		return res.StatusCode, b, e
	}
	wait := func(label string, probe func() bool) {
		t.Helper()
		deadline := time.NewTimer(20 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			if probe() {
				return
			}
			select {
			case <-deadline.C:
				t.Fatalf("review acceptance wait: %s", label)
			case err := <-done:
				done <- err
				t.Fatalf("App stopped before %s: %v", label, err)
			case <-ticker.C:
			}
		}
	}
	wait("production HTTP and settled initial root scans", func() bool {
		status, _, e := call("GET", f.path(), "", "")
		if e != nil || status != 200 {
			return false
		}
		var ready bool
		e = f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*)>=2 FROM scan_run WHERE root_id=$1 AND state='succeeded') AND NOT EXISTS(SELECT 1 FROM job WHERE (kind LIKE 'library_%' OR kind='movie_recognition') AND state IN ('queued','running')) AND NOT EXISTS(SELECT 1 FROM library_root WHERE NOT baseline_completed OR health<>'online')`, f.root).Scan(&ready)
		return e == nil && ready
	})
	if err = f.pool.QueryRow(t.Context(), `SELECT c.updated_at,r.generation FROM download_client c CROSS JOIN library_root r WHERE c.id=$1 AND r.id=$2`, f.client, f.root).Scan(&f.input.ExpectedClientVersion, &f.input.ExpectedRootGeneration); err != nil {
		t.Fatal(err)
	}
	status, raw, err := call("PUT", f.path(), f.body(t, 0, true), "review-scope-enable")
	if err != nil || status != 200 {
		t.Fatalf("scope enable status=%d err=%v", status, err)
	}
	var scopeReceipt ports.UntrackedScopeReceipt
	if err = json.Unmarshal(raw, &scopeReceipt); err != nil || scopeReceipt.ExecutionAllowed {
		t.Fatal("invalid scope receipt", err)
	}
	var fileID uuid.UUID
	var revision int64
	wait("persistent discovery and completed sweep", func() bool {
		var idle bool
		e := f.pool.QueryRow(t.Context(), `SELECT f.id,f.revision,d.active_job_id IS NULL AND d.completed_sequence=d.requested_sequence FROM untracked_discovered_file f JOIN untracked_discovery_state d ON d.scope_id=f.scope_id WHERE f.scope_id=$1 AND f.relative_path='Movie.2026.mkv'`, f.scope).Scan(&fileID, &revision, &idle)
		return e == nil && idle
	})
	before := m8ReviewBusiness(t, f)
	video := filepath.Join(f.source, "Movie.2026.mkv")
	original, err := os.ReadFile(video)
	if err != nil {
		t.Fatal(err)
	}
	base := f.path() + "/discoveries/" + fileID.String()
	input := ports.UntrackedReviewInput{ScopeVersion: scopeReceipt.Scope.Version, FileRevision: revision, MediaType: "movie"}
	b, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	requestBody := string(b)
	status, raw, err = call("POST", base+"/reviews", requestBody, "review-original")
	if err != nil || status != 202 {
		t.Fatalf("review enqueue status=%d err=%v", status, err)
	}
	var receipt ports.UntrackedReviewRequestReceipt
	if err = json.Unmarshal(raw, &receipt); err != nil || receipt.ExecutionAllowed || receipt.Request.ID == uuid.Nil || receipt.Request.JobID == uuid.Nil {
		t.Fatal("invalid review request receipt", err)
	}
	requestRaw := append([]byte(nil), raw...)
	status, raw, err = call("POST", base+"/reviews", requestBody, "review-original")
	if err != nil || status != 202 || !bytes.Equal(bytes.TrimSpace(raw), bytes.TrimSpace(requestRaw)) {
		t.Fatal("same key changed original review request")
	}
	reviewPath := base + "/reviews/" + receipt.Request.ID.String()
	if scenario == "disabled_during_metadata" {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("metadata preparation not reached")
		}
		status, _, err = call("PUT", f.path(), f.body(t, scopeReceipt.Scope.Version, false), "review-disable")
		if err != nil || status != 200 {
			t.Fatalf("disable status=%d err=%v", status, err)
		}
		releaseOnce.Do(func() { close(release) })
		wait("late request rejected after actual metadata return", func() bool {
			var state string
			e := f.pool.QueryRow(t.Context(), `SELECT state FROM job WHERE id=$1`, receipt.Request.JobID).Scan(&state)
			return e == nil && state != "running"
		})
		status, raw, err = call("GET", reviewPath, "", "")
		var view ports.UntrackedReview
		if err != nil || status != 200 || json.Unmarshal(raw, &view) != nil || view.Evidence != nil || !view.Stale || view.ExecutionAllowed {
			t.Fatal("disabled late result became trusted review")
		}
	} else {
		var view ports.UntrackedReview
		wait("trusted candidate and original job success", func() bool {
			code, body, e := call("GET", reviewPath, "", "")
			return e == nil && code == 200 && json.Unmarshal(body, &view) == nil && view.Evidence != nil && view.Evidence.Candidate != nil
		})
		candidate := view.Evidence.Candidate
		if view.Stale || view.ExecutionAllowed || !view.Evidence.Complete || candidate.Namespace != "tmdb.movie" || candidate.TMDBID != 551 || len(candidate.Units) != 1 || candidate.Units[0].UnitKey != "movie" || len(view.MaterialHash) != 64 {
			t.Fatal("review lost trusted identity, coverage or read-only boundary")
		}
		confirm := ports.UntrackedConfirmationInput{ReviewID: receipt.Request.ID, MaterialHash: view.MaterialHash, ScopeVersion: input.ScopeVersion, FileRevision: revision, CandidateKey: candidate.Key, Action: "hardlink", TargetRelativePath: "Movie (2026)/Movie.mkv"}
		confirmBody, marshalErr := json.Marshal(confirm)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if scenario == "file_changed_before_confirmation" {
			if err = os.WriteFile(video, append(original, []byte(" externally changed")...), 0600); err != nil {
				t.Fatal(err)
			}
			wait("new source revision", func() bool {
				var current int64
				e := f.pool.QueryRow(t.Context(), `SELECT revision FROM untracked_discovered_file WHERE id=$1`, fileID).Scan(&current)
				return e == nil && current > revision
			})
			status, _, err = call("POST", base+"/confirmations", string(confirmBody), "confirmation-original")
			if err != nil || status != 409 {
				t.Fatalf("changed source confirmation status=%d err=%v", status, err)
			}
		} else {
			status, raw, err = call("POST", base+"/confirmations", string(confirmBody), "confirmation-original")
			if err != nil || status != 200 {
				t.Fatalf("confirmation status=%d err=%v", status, err)
			}
			var confirmed struct {
				CommandKey       string `json:"command_key"`
				State            string `json:"state"`
				ExecutionAllowed bool   `json:"execution_allowed"`
				Material         struct {
					ID    uuid.UUID                        `json:"id"`
					Input ports.UntrackedConfirmationInput `json:"input"`
				} `json:"material"`
			}
			if json.Unmarshal(raw, &confirmed) != nil || confirmed.CommandKey != "confirmation-original" || confirmed.State != "confirmed_pending_organization" || confirmed.ExecutionAllowed || confirmed.Material.ID == uuid.Nil || confirmed.Material.Input != confirm {
				t.Fatal("confirmation lost frozen intent or granted authority")
			}
			originalReceipt := append([]byte(nil), raw...)
			offline := f.source + ".offline"
			if err = os.Rename(f.source, offline); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if e := os.Rename(offline, f.source); e != nil {
					t.Error("restore own source", e)
				}
			}()
			for _, method := range []string{"GET", "POST"} {
				path, body, key := base+"/confirmation-commands/confirmation-original", "", ""
				if method == "POST" {
					path, body, key = base+"/confirmations", string(confirmBody), "confirmation-original"
				}
				status, raw, err = call(method, path, body, key)
				if err != nil || status != 200 || !bytes.Equal(bytes.TrimSpace(raw), bytes.TrimSpace(originalReceipt)) {
					t.Fatal("offline original-key confirmation recovery changed receipt")
				}
			}
			status, raw, err = call("GET", base+"/review-commands/review-original", "", "")
			if err != nil || status != 200 || !bytes.Equal(bytes.TrimSpace(raw), bytes.TrimSpace(requestRaw)) {
				t.Fatal("offline review request recovery changed receipt")
			}
		}
	}
	if before != m8ReviewBusiness(t, f) || f.calls.Load() != 0 {
		t.Fatal("review/confirmation changed protected business graph or called downloader")
	}
	var requestJobs int
	if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM job WHERE kind=$1`, ports.UntrackedReviewJobKind).Scan(&requestJobs); err != nil || requestJobs != 1 {
		t.Fatal("original request duplicated job", err)
	}
	if metadataCalls.Load() != 2 {
		t.Fatalf("native metadata requests=%d want search+detail once", metadataCalls.Load())
	}
	entries, err := os.ReadDir(f.target)
	if err != nil || len(entries) != 2 {
		t.Fatal("review published target file", err)
	}
	t.Logf("M8_UNTRACKED_REVIEW_APP_PASS scenario=%s app=REAL cookie=REAL PG=18.6 metadata=NATIVE_OWNED_HTTP source=REAL review_jobs=1 metadata_calls=2 downloader_calls=0 imported=0 execution_allowed=false", scenario)
}
