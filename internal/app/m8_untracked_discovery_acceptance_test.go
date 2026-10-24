//go:build linux

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func m8DiscoveryKernelReady(t *testing.T, directory string) bool {
	t.Helper()
	root, err := rootfs.NewFilesystem().OpenRoot(t.Context(), directory)
	if err != nil {
		return false
	}
	identity, err := root.Identity(t.Context())
	root.Close()
	if err != nil {
		return false
	}
	want := "ino:" + strconv.FormatInt(identity.Inode, 16)
	entries, err := os.ReadDir("/proc/self/fdinfo")
	if err != nil || len(entries) > 4096 {
		return false
	}
	for _, entry := range entries {
		raw, e := os.ReadFile(filepath.Join("/proc/self/fdinfo", entry.Name()))
		if e != nil || len(raw) > 256*1024 {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 || fields[0] != "inotify" {
				continue
			}
			for _, field := range fields {
				if field == want {
					return true
				}
			}
		}
	}
	return false
}
func m8DiscoveryBusiness(t *testing.T, f *m8ScopeConfigFixture) string {
	t.Helper()
	snapshot := m8ConfigSnapshot(t, f.m7ScrapeFixture, "media", "media_identity", "media_unit", "subscription", "wanted_item", "library_file", "library_file_unit", "unit_fact", "download_task", "download_client", "library_root", "scan_run", "scan_finalization", "untracked_directory_scope", "untracked_directory_scope_receipt", "idempotency_record", "audit_log", "outbox_event")
	var jobs string
	if err := f.pool.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(to_jsonb(j) ORDER BY j.id),'[]'::jsonb)::text FROM job j WHERE kind<>'untracked_discovery'`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	return snapshot + jobs
}
func TestM8UntrackedDiscoveryRuntimeCookiePG(t *testing.T) {
	if os.Getenv("PULSE_M8_UNTRACKED_DISCOVERY_ACCEPTANCE") != "1" {
		t.Skip("explicit owned directory event acceptance required")
	}
	f := m8NewScopeConfigFixture(t)
	nested := filepath.Join(f.source, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
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
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("owned App shutdown: %v", err)
			}
		case <-time.After(45 * time.Second):
			t.Error("owned App did not drain actual directory I/O")
		}
	}()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 2 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	call := func(method, path, body, key string, cookie *http.Cookie) (int, []byte, error) {
		req, err := http.NewRequestWithContext(t.Context(), method, "http://"+address+path, strings.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Origin", "http://"+address)
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		response, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer func() { _ = response.Body.Close() }()
		raw, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
		if strings.HasPrefix(path, "/api/v1/library/untracked-scopes/") && response.Header.Get("Cache-Control") != "private, no-store" {
			return response.StatusCode, nil, fmt.Errorf("scope response missing private no-store")
		}
		return response.StatusCode, raw, err
	}
	wait := func(label string, probe func() bool) {
		t.Helper()
		deadline := time.NewTimer(15 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			if probe() {
				return
			}
			select {
			case <-deadline.C:
				t.Fatalf("directory acceptance wait: %s", label)
			case err := <-done:
				done <- err
				t.Fatalf("App stopped before %s: %v", label, err)
			case <-ticker.C:
			}
		}
	}
	wait("live production route and initial scans", func() bool {
		status, _, err := call("GET", f.path(), "", "", f.cookie)
		if err != nil || status != http.StatusOK {
			return false
		}
		var ready bool
		err = f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*)>=2 FROM scan_run WHERE root_id=$1 AND state='succeeded') AND NOT EXISTS(SELECT 1 FROM job WHERE (kind LIKE 'library_%' OR kind='movie_recognition') AND state IN ('queued','running')) AND NOT EXISTS(SELECT 1 FROM library_root WHERE NOT baseline_completed OR health<>'online')`, f.root).Scan(&ready)
		return err == nil && ready
	})
	if err = f.pool.QueryRow(t.Context(), `SELECT c.updated_at,r.generation FROM download_client c CROSS JOIN library_root r WHERE c.id=$1 AND r.id=$2`, f.client, f.root).Scan(&f.input.ExpectedClientVersion, &f.input.ExpectedRootGeneration); err != nil {
		t.Fatal(err)
	}
	status, raw, err := call("PUT", f.path(), f.body(t, 0, true), "discovery-scope-enable", f.cookie)
	if err != nil || status != http.StatusOK {
		t.Fatalf("public configuration status=%d err=%v", status, err)
	}
	var receipt ports.UntrackedScopeReceipt
	if err = json.Unmarshal(raw, &receipt); err != nil || receipt.ExecutionAllowed || receipt.Scope.ID != f.scope {
		t.Fatal("configuration receipt lost scope or granted execution", err)
	}
	var beforeSweep time.Time
	var beforeSequence int64
	wait("kernel nested registration and sealed initial sweep", func() bool {
		if !m8DiscoveryKernelReady(t, nested) {
			return false
		}
		var ready bool
		err = f.pool.QueryRow(t.Context(), `SELECT active_job_id IS NULL AND requested_sequence=completed_sequence AND completed_sequence>0 AND next_sweep_at>clock_timestamp()+interval '40 seconds',next_sweep_at,completed_sequence FROM untracked_discovery_state WHERE scope_id=$1`, f.scope).Scan(&ready, &beforeSweep, &beforeSequence)
		return err == nil && ready
	})
	before := m8DiscoveryBusiness(t, f)
	video := filepath.Join(nested, "Arrival.2026.mkv")
	body := []byte("owned observed video bytes; no identity or import permission")
	closedAt := time.Now()
	if err = os.WriteFile(video, body, 0600); err != nil {
		t.Fatal(err)
	}
	var fileID, jobID uuid.UUID
	var revision int64
	var queuedAt time.Time
	wait("event-driven persisted observation before periodic sweep", func() bool {
		var complete bool
		err = f.pool.QueryRow(t.Context(), `SELECT f.id,f.revision,f.last_job_id,j.created_at,d.active_job_id IS NULL AND d.completed_sequence>d.requested_sequence-1 AND d.completed_sequence>$2 FROM untracked_discovered_file f JOIN untracked_discovery_state d ON d.scope_id=f.scope_id JOIN job j ON j.id=f.last_job_id WHERE f.scope_id=$1 AND f.relative_path='nested/Arrival.2026.mkv'`, f.scope, beforeSequence).Scan(&fileID, &revision, &jobID, &queuedAt, &complete)
		return err == nil && complete
	})
	elapsed := time.Since(closedAt)
	if fileID == uuid.Nil || jobID == uuid.Nil || revision != 1 || !queuedAt.Before(beforeSweep) || elapsed >= 15*time.Second {
		t.Fatal("event failed original sweep exclusion or identity/revision", elapsed)
	}
	if before != m8DiscoveryBusiness(t, f) || f.calls.Load() != 0 {
		t.Fatal("discovery changed protected business facts or called downloader")
	}
	readBefore := m8ConfigSnapshot(t, f.m7ScrapeFixture, "untracked_discovery_state", "untracked_discovered_file", "job") + m8DiscoveryBusiness(t, f)
	seen := map[uuid.UUID]bool{}
	cursor := ""
	for pageNumber := 0; pageNumber < 4; pageNumber++ {
		path := f.path() + "/discoveries?limit=1"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		status, raw, err = call("GET", path, "", "", f.cookie)
		if err != nil || status != http.StatusOK {
			t.Fatalf("persisted public read status=%d err=%v", status, err)
		}
		var page httpapi.UntrackedDiscoveryList
		if err = json.Unmarshal(raw, &page); err != nil || bool(page.InventoryComplete) || bool(page.ExecutionAllowed) {
			t.Fatal("observation list claimed inventory or execution", err)
		}
		for _, item := range page.Items {
			if seen[item.Id] || item.ScopeId != f.scope {
				t.Fatal("duplicate or foreign observation")
			}
			seen[item.Id] = true
		}
		if !page.HasMore {
			break
		}
		if page.NextCursor == "" {
			t.Fatal("missing continuation")
		}
		cursor = page.NextCursor
	}
	if !seen[fileID] || len(seen) != 2 {
		t.Fatal("real observations were omitted or non-video sidecars exposed", len(seen))
	}
	if readBefore != m8ConfigSnapshot(t, f.m7ScrapeFixture, "untracked_discovery_state", "untracked_discovered_file", "job")+m8DiscoveryBusiness(t, f) || f.calls.Load() != 0 {
		t.Fatal("GET changed full business/task/observation snapshot or triggered network")
	}
	status, _, err = call("GET", f.path()+"/discoveries", "", "", nil)
	if err != nil || status != http.StatusUnauthorized {
		t.Fatal("anonymous observation read accepted", status, err)
	}
	sourceRoot, err := rootfs.NewFilesystem().OpenRoot(t.Context(), f.source)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := sourceRoot.Inspect(t.Context(), "nested/Arrival.2026.mkv")
	sourceRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	var stored ports.FileIdentity
	if err = f.pool.QueryRow(t.Context(), `SELECT device,inode,size,mtime FROM untracked_discovered_file WHERE id=$1`, fileID).Scan(&stored.Device, &stored.Inode, &stored.Size, &stored.Mtime); err != nil || stored != identity {
		t.Fatal("persisted identity did not match actual source", err)
	}
	content, err := os.ReadFile(video)
	if err != nil || string(content) != string(body) {
		t.Fatal("source changed during read-only discovery", err)
	}
	target, err := os.ReadFile(filepath.Join(f.target, "user.nfo"))
	if err != nil || string(target) != "user sidecar remains" {
		t.Fatal("user target sidecar changed", err)
	}
	t.Logf("M8_UNTRACKED_DISCOVERY_APP_PASS app=REAL cookie=REAL rootfs=REAL inotify=REAL periodic_60s_excluded=true elapsed_ms=%d source_identity=true observations_only=true GET_writes=0 downloader_calls=0 execution_allowed=false inventory_complete=false imported=0", elapsed.Milliseconds())
}
