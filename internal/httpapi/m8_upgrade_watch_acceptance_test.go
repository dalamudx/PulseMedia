package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Main runs this opt-in acceptance. No stored observation clock is accelerated.
func TestM8MovieUpgradeDirectoryHintHTTPPG(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("PULSE_M8_MOVIE_UPGRADE_WATCH_ACCEPTANCE") != "1" || strings.TrimSpace(os.Getenv("PULSE_TEST_DATABASE_URL")) == "" {
		t.Skip("requires Linux, PULSE_M8_MOVIE_UPGRADE_WATCH_ACCEPTANCE=1 and an explicitly owned PULSE_TEST_DATABASE_URL")
	}
	dataDir, err := os.MkdirTemp("/data/temp", "m8-upgrade-watch-")
	if err != nil {
		t.Fatal("create owned fixture directory")
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dataDir); err != nil {
			t.Error("remove owned fixture directory")
		}
	})
	for _, key := range []string{"TMPDIR", "TMP", "TEMP", "PULSE_TEST_TEMP_ROOT"} {
		t.Setenv(key, dataDir)
	}
	for _, scenario := range []struct {
		name          string
		lost, replace bool
	}{
		{name: "ack_stop_at_cutoff"},
		{name: "lost_ack_stop_at_cutoff", lost: true},
		{name: "ack_replace_if_better", replace: true},
		{name: "lost_ack_replace_if_better", lost: true, replace: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
			defer cancel()
			wait := func(label string, probe func() bool) {
				t.Helper()
				for !probe() {
					select {
					case <-ctx.Done():
						t.Fatal("acceptance deadline: " + label)
					case <-time.After(25 * time.Millisecond):
					}
				}
			}
			var adds, removes atomic.Int32
			var completed atomic.Bool
			var native atomic.Pointer[trackingAppNative]
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				current := native.Load()
				switch r.URL.Path {
				case "/api/v2/auth/login":
					_, _ = io.WriteString(w, "Ok.")
				case "/api/v2/torrents/add":
					if adds.Add(1) != 1 {
						t.Error("duplicate native Add")
					}
					if err := r.ParseMultipartForm(ports.MaxTorrentMetainfoBytes); err != nil {
						t.Error("invalid native multipart")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					defer func() { _ = r.MultipartForm.RemoveAll() }()
					file, _, err := r.FormFile("torrents")
					if err != nil {
						t.Error("missing original torrent")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					raw, err := io.ReadAll(file)
					_ = file.Close()
					if err != nil || current == nil || !bytes.Equal(raw, current.raw) || r.FormValue("savepath") != current.path || r.FormValue("tags") != "pulsemedia,"+current.tag {
						t.Error("native Add changed original bytes/savepath/tag")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if scenario.lost {
						panic(http.ErrAbortHandler)
					} // POST was processed; lose only the response.
					_, _ = io.WriteString(w, "Ok.")
				case "/api/v2/torrents/delete":
					removes.Add(1)
					t.Error("directory hint attempted Remove")
					w.WriteHeader(http.StatusForbidden)
				case "/api/v2/torrents/info", "/api/v2/torrents/files", "/api/v2/torrents/export":
					if current == nil {
						t.Error("native observation before preparation")
						w.WriteHeader(http.StatusConflict)
						return
					}
					progress, downloaded, state := 0.5, 60, "downloading"
					if completed.Load() {
						progress, downloaded, state = 1, 120, "uploading"
					}
					switch r.URL.Path {
					case "/api/v2/torrents/info":
						_ = json.NewEncoder(w).Encode([]any{map[string]any{"hash": current.hash, "name": "Fight.Club.1999.2160p.Bluray.x265.mkv", "tags": current.tag, "save_path": current.path, "size": 120, "downloaded": downloaded, "progress": progress, "state": state}})
					case "/api/v2/torrents/files":
						_ = json.NewEncoder(w).Encode([]any{map[string]any{"index": 0, "name": "Fight.Club.1999.2160p.Bluray.x265.mkv", "size": 120, "progress": progress, "priority": 1}})
					case "/api/v2/torrents/export":
						w.Header().Set("Content-Type", "application/x-bittorrent")
						_, _ = w.Write(current.raw)
					}
				default:
					t.Error("unexpected native endpoint")
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			var start func() func()
			var stop func()
			var ownedLibraryRoot, ownedSourceRoot string
			upgradeMode, cutoff := ports.UpgradeStopAtCutoff, 1000
			if scenario.replace {
				upgradeMode, cutoff = ports.UpgradeReplaceIfBetter, 300
			}
			f := newPublicPreparationFixtureWithMode(t, upgradeMode, cutoff, func(f *publicPreparationFixture) {
				master, err := crypto.EnsureMasterKey(f.a.Config().DataDir, f.a.Config().SecretKey)
				if err != nil {
					t.Fatal("fixture master key unavailable")
				}
				encrypted, err := crypto.Encrypt(master, []byte("watch-native-password"))
				if err != nil {
					t.Fatal("fixture password encryption failed")
				}
				f.exec(t, `UPDATE download_client SET url=$2,username='native-admin',password_encrypted=$3 WHERE id=$1`, f.client, server.URL, encrypted)
				ownedLibraryRoot, ownedSourceRoot = preparePublicUpgradePhysicalFiles(t, f.publicUpgradeFixture)
				start = publicUpgradeAppStarter(t, f.publicUpgradeFixture)
				// Undo only fixture setup's synthetic baseline before any observation,
				// then let the original App establish the first physical scan identity.
				f.exec(t, `UPDATE library_root SET baseline_completed=false WHERE id=$1`, f.root)
				f.exec(t, `UPDATE library_scan_schedule SET next_run_at=clock_timestamp() WHERE root_id=$1`, f.root)
				var runs int
				if err := f.a.DBPool().QueryRow(ctx, `SELECT count(*) FROM scan_run WHERE root_id=$1`, f.root).Scan(&runs); err != nil || runs != 0 {
					t.Fatal("fixture must have no fabricated scan baseline")
				}
				stop = start()
				wait("first real App baseline", func() bool {
					var ready bool
					if err := f.a.DBPool().QueryRow(ctx, `SELECT r.baseline_completed AND EXISTS(SELECT 1 FROM scan_run s JOIN scan_finalization f ON f.scan_run_id=s.id WHERE s.root_id=r.id AND s.generation=r.generation AND s.state='succeeded' AND f.completed_at IS NOT NULL) FROM library_root r WHERE r.id=$1`, f.root).Scan(&ready); err != nil {
						t.Fatal("baseline read failed")
					}
					return ready
				})
				f.project(t)
			})
			// Registered after all fixture servers; starter also owns cancellation/join.
			t.Cleanup(func() { stop() })
			pool := f.a.DBPool() // Fixture App stays open; starter never replaces f.a.
			body := publicSendBody(t, f)
			var hash, remote, root string
			var mappingJSON []byte
			if err := pool.QueryRow(ctx, `SELECT p.evidence->'proof'->>'info_hash',p.evidence->>'save_path',c.remote_path_map,r.path FROM movie_upgrade_preparation p JOIN movie_upgrade_authority a USING(operation_id) JOIN download_client c ON c.id=a.client_id JOIN library_root r ON r.id=a.root_id WHERE p.operation_id=$1`, f.operation).Scan(&hash, &remote, &mappingJSON, &root); err != nil {
				t.Fatal("frozen native source read failed")
			}
			var mapping map[string]string
			if json.Unmarshal(mappingJSON, &mapping) != nil || len(mapping) != 1 || mapping[remote] == "" || remote != root {
				t.Fatal("fixture remote/local root mapping is not exact")
			}
			sourceDir := mapping[remote]
			if root != ownedLibraryRoot || sourceDir != ownedSourceRoot {
				t.Fatal("configured paths differ from the exact fixture-owned roots")
			}
			for _, dir := range []string{ownedLibraryRoot, ownedSourceRoot} {
				resolved, resolveErr := filepath.EvalSymlinks(dir)
				if resolveErr != nil || resolved != dir || !filepath.IsAbs(dir) {
					t.Fatal("fixture root contains a symbolic link or is not absolute")
				}
			}
			sourceFile := filepath.Join(sourceDir, "Fight.Club.1999.2160p.Bluray.x265.mkv")
			payload, err := os.ReadFile(sourceFile)
			if err != nil || len(payload) != 120 {
				t.Fatal("original source payload unavailable")
			}
			oldPath := filepath.Join(root, "original.mkv")
			oldBytes, err := os.ReadFile(oldPath)
			if err != nil {
				t.Fatal("old physical file unavailable")
			}
			oldStat, err := os.Stat(oldPath)
			if err != nil {
				t.Fatal("old physical identity unavailable")
			}
			native.Store(&trackingAppNative{hash: hash, path: remote, tag: "pulsemedia:op:" + f.operation.String(), raw: bytes.Clone(f.raw)})
			const key = "m8-upgrade-watch-send"
			sent := f.command("upgrade-sends", body, key, f.admin, nil)
			if sent.Code != http.StatusAccepted {
				t.Fatal("public send was not accepted")
			}
			if scenario.lost {
				wait("frozen unknown send result", func() bool {
					var unknown bool
					if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_send_result WHERE operation_id=$1 AND status='unknown')`, f.operation).Scan(&unknown); err != nil {
						t.Fatal("unknown result read failed")
					}
					return unknown
				})
				stop()
				frozen := m8UpgradeWatchIdentity(t, ctx, f, "unknown")
				files := f.unchangedFiles(t)
				stop = start()
				completed.Store(true)
				if err := os.WriteFile(sourceFile, payload, 0600); err != nil {
					t.Fatal("unknown source close_write failed")
				}
				closed := time.Now()
				// Include the next periodic tracking scan; an unknown send has no confirmed predecessor.
				wait("unknown result remains protected across periodic scan", func() bool { return time.Since(closed) >= 32*time.Second })
				recovered := f.command("upgrade-sends", body, key, f.admin, nil)
				if recovered.Code != http.StatusAccepted || recovered.Body.String() != sent.Body.String() {
					t.Fatal("unknown send receipt changed")
				}
				stop()
				if !bytes.Equal(frozen, m8UpgradeWatchIdentity(t, ctx, f, "unknown")) || files != f.unchangedFiles(t) {
					t.Fatal("event/restart changed unknown send protection")
				}
				var fenced bool
				if err := pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM movie_upgrade_tracking_job WHERE operation_id=$1) AND NOT EXISTS(SELECT 1 FROM movie_upgrade_organization_job) AND NOT EXISTS(SELECT 1 FROM movie_upgrade_organization_ingestion) AND NOT EXISTS(SELECT 1 FROM movie_upgrade_retirement_command) AND NOT EXISTS(SELECT 1 FROM transfer_plan)`, f.operation).Scan(&fenced); err != nil || !fenced {
					t.Fatal("unknown send gained tracking or organization authority")
				}
				if oldAfter, err := os.ReadFile(oldPath); err != nil || !bytes.Equal(oldBytes, oldAfter) {
					t.Fatal("unknown send changed old file bytes")
				}
				if oldAfter, err := os.Stat(oldPath); err != nil || !os.SameFile(oldStat, oldAfter) || !oldStat.ModTime().Equal(oldAfter.ModTime()) {
					t.Fatal("unknown send changed old file identity")
				}
				if adds.Load() != 1 || removes.Load() != 0 || f.fetches.Load() != 2 {
					t.Fatal("unknown send recovery repeated native side effects")
				}
				t.Logf("M8_MOVIE_UPGRADE_WATCH_UNKNOWN_PASS scenario=%s lost_ack=true replace_if_better=%t Add=1 restart_and_close_write=true periodic_30s_observed=true tracking_authority=false organization=false original_receipt=true", scenario.name, scenario.replace)
				return
			}
			var prior, priorJob, task uuid.UUID
			var partial []byte
			var recorded, bootFence time.Time
			wait("genuine terminal partial tracking", func() bool {
				err := pool.QueryRow(ctx, `SELECT r.observation_id,b.job_id,b.task_id,to_jsonb(r),r.recorded_at FROM movie_upgrade_tracking_result r JOIN movie_upgrade_tracking_job_claim c USING(observation_id) JOIN movie_upgrade_tracking_job b USING(job_id) JOIN job j ON j.id=b.job_id JOIN movie_upgrade_send_result s ON s.operation_id=b.operation_id WHERE b.operation_id=$1 AND b.prior_observation_id IS NULL AND s.status='confirmed' AND r.outcome='tracking' AND r.disposition='tracking' AND j.state='succeeded' AND to_jsonb(j)=pulse_upgrade_tracking_terminal_job(r.observation_id)`, f.operation).Scan(&prior, &priorJob, &task, &partial, &recorded)
				if errors.Is(err, pgx.ErrNoRows) {
					return false
				}
				if err != nil {
					t.Fatal("partial read failed")
				}
				return true
			})
			identity := m8UpgradeWatchIdentity(t, ctx, f)
			files := f.unchangedFiles(t)
			stop()
			// A real five-second margin excludes the restarted 30-second scanner.
			wait("partial plus five seconds", func() bool {
				var ready bool
				if err := pool.QueryRow(ctx, `SELECT clock_timestamp()>=$1::timestamptz+interval '5 seconds'`, recorded).Scan(&ready); err != nil {
					t.Fatal("restart margin clock read failed")
				}
				return ready
			})
			if err := pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&bootFence); err != nil {
				t.Fatal("boot fence read failed")
			}
			stop = start()
			client := &http.Client{Timeout: time.Second}
			t.Cleanup(client.CloseIdleConnections)
			wait("healthy App, initial maintenance select and kernel source watch", func() bool {
				var beforeDue bool
				if err := pool.QueryRow(ctx, `SELECT clock_timestamp()<$1::timestamptz+interval '30 seconds'`, recorded).Scan(&beforeDue); err != nil || !beforeDue {
					t.Fatal("readiness missed original thirty-second due gate")
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.runtimeURL+"/api/v1/auth/status", nil)
				if err != nil {
					t.Fatal("health request construction failed")
				}
				response, err := client.Do(req)
				if err != nil {
					return false
				}
				healthy := response.StatusCode == http.StatusOK
				_ = response.Body.Close()
				return healthy && m8UpgradeWatchSchedulerWaiting() && completeTVWatchKernelReady(t, sourceDir)
			})
			var clean bool
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*)=1 FROM movie_upgrade_tracking_job WHERE operation_id=$1) AND (SELECT count(*)=1 FROM movie_upgrade_tracking_observation WHERE operation_id=$1) AND (SELECT count(*)=1 FROM movie_upgrade_tracking_permit WHERE operation_id=$1) AND (SELECT to_jsonb(r)=$3::jsonb FROM movie_upgrade_tracking_result r WHERE observation_id=$2) AND clock_timestamp()<$4::timestamptz+interval '30 seconds'`, f.operation, prior, string(partial), recorded).Scan(&clean); err != nil || !clean {
				t.Fatal("event must retain the exact partial predecessor without successors")
			}
			started := time.Now()
			completed.Store(true)
			if err := os.WriteFile(sourceFile, payload, 0600); err != nil {
				t.Fatal("real source close_write failed")
			}
			wait("completed normal tracking successor", func() bool {
				var done bool
				if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_tracking_job b JOIN movie_upgrade_tracking_job_claim c USING(job_id) JOIN movie_upgrade_tracking_result r USING(observation_id) JOIN job j ON j.id=b.job_id WHERE b.operation_id=$1 AND b.prior_observation_id=$2 AND b.task_id=$3 AND r.outcome='completed' AND r.disposition='organization_review_required' AND j.state='succeeded' AND to_jsonb(j)=pulse_upgrade_tracking_terminal_job(r.observation_id))`, f.operation, prior, task).Scan(&done); err != nil {
					t.Fatal("completion read failed")
				}
				return done
			})
			var successor uuid.UUID
			var rawPayload, finalPartial []byte
			var phase, terminal, counts bool
			if err := pool.QueryRow(ctx, `SELECT j.id,j.payload,j.created_at>=$3::timestamptz+interval '30 seconds' AND j.created_at<$4::timestamptz+interval '30 seconds',j.kind=$5 AND j.state='succeeded' AND to_jsonb(j)=pulse_upgrade_tracking_terminal_job(c.observation_id) AND (c.claimed_job->>'lease_until')::timestamptz>(c.claimed_job->>'updated_at')::timestamptz+interval '119 seconds' AND (c.claimed_job->>'lease_until')::timestamptz<=(c.claimed_job->>'updated_at')::timestamptz+interval '2 minutes' FROM movie_upgrade_tracking_job b JOIN job j ON j.id=b.job_id JOIN movie_upgrade_tracking_job_claim c USING(job_id) WHERE b.operation_id=$1 AND b.prior_observation_id=$2`, f.operation, prior, recorded, bootFence, ports.MovieUpgradeTrackingJobKind).Scan(&successor, &rawPayload, &phase, &terminal); err != nil || !phase || !terminal || successor == priorJob {
				t.Fatalf("successor violated gate/phase/terminal/lease: phase=%t terminal=%t same_job=%t scan_error=%T", phase, terminal, successor == priorJob, err)
			}
			var originalTerminal bool
			if err := pool.QueryRow(ctx, `SELECT j.state='succeeded' AND j.kind=$3 AND j.payload=jsonb_build_object('tracking_job_id',j.id) AND to_jsonb(j)=pulse_upgrade_tracking_terminal_job($2) FROM job j WHERE j.id=$1`, priorJob, prior, ports.MovieUpgradeTrackingJobKind).Scan(&originalTerminal); err != nil || !originalTerminal {
				t.Fatal("original terminal tracking job or one-key payload changed")
			}
			var decoded map[string]uuid.UUID
			if json.Unmarshal(rawPayload, &decoded) != nil || len(decoded) != 1 || decoded["tracking_job_id"] != successor {
				t.Fatal("successor changed original one-key tracking payload")
			}
			if err := pool.QueryRow(ctx, `SELECT to_jsonb(r) FROM movie_upgrade_tracking_result r WHERE observation_id=$1`, prior).Scan(&finalPartial); err != nil || !bytes.Equal(partial, finalPartial) {
				t.Fatal("original full partial JSON changed")
			}
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*)=2 FROM movie_upgrade_tracking_job WHERE operation_id=$1) AND (SELECT count(*)=2 FROM movie_upgrade_tracking_observation WHERE operation_id=$1) AND (SELECT count(*)=2 FROM movie_upgrade_tracking_permit WHERE operation_id=$1) AND (SELECT count(*)=2 FROM movie_upgrade_tracking_result r JOIN movie_upgrade_tracking_observation o USING(observation_id) WHERE o.operation_id=$1) AND (SELECT count(*)=1 FROM download_task WHERE operation_id=$1 AND id=$2) AND (SELECT count(*)=1 FROM acquisition_reservation WHERE operation_id=$1) AND (SELECT count(*)=1 FROM unit_reservation WHERE operation_id=$1) AND (SELECT count(*)=1 FROM download_requirement WHERE operation_id=$1) AND NOT EXISTS(SELECT 1 FROM movie_upgrade_organization_job) AND NOT EXISTS(SELECT 1 FROM movie_upgrade_organization_ingestion) AND NOT EXISTS(SELECT 1 FROM movie_upgrade_retirement_command) AND NOT EXISTS(SELECT 1 FROM transfer_plan) AND NOT EXISTS(SELECT 1 FROM job WHERE kind LIKE '%organiz%' OR kind LIKE '%recycl%')`, f.operation, task).Scan(&counts); err != nil || !counts {
				t.Fatal("hint must add exactly one observation/result/permit without organization, retirement or release")
			}
			if !bytes.Equal(identity, m8UpgradeWatchIdentity(t, ctx, f)) || files != f.unchangedFiles(t) {
				t.Fatal("original task/send/authority/preparation/reservations/file facts changed")
			}
			oldAfter, err := os.ReadFile(oldPath)
			if err != nil || !bytes.Equal(oldBytes, oldAfter) {
				t.Fatal("protected old file bytes changed")
			}
			afterStat, err := os.Stat(oldPath)
			if err != nil || !os.SameFile(oldStat, afterStat) || !oldStat.ModTime().Equal(afterStat.ModTime()) {
				t.Fatal("protected old file identity changed")
			}
			// The existing upgrade contract recovers by same-key POST; it has no GET
			// send-receipt route. Do not invent an endpoint or a second send identity.
			recovered := f.command("upgrade-sends", body, key, f.admin, nil)
			if recovered.Code != http.StatusAccepted || recovered.Body.String() != sent.Body.String() {
				t.Fatal("original send receipt changed")
			}
			stop()
			if adds.Load() != 1 || removes.Load() != 0 || f.fetches.Load() != 2 {
				t.Fatal("directory hint or receipt recovery repeated native side effects")
			}
			t.Logf("M8_MOVIE_UPGRADE_WATCH_APP_PASS scenario=%s lost_ack=%t replace_if_better=%t real_inotify=true gate_30s=true periodic_excluded=true Add=1 organization_review_required=true close_to_result_ms=%d", scenario.name, scenario.lost, scenario.replace, time.Since(started).Milliseconds())
		})
	}
}

func m8UpgradeWatchIdentity(t *testing.T, ctx context.Context, f *publicPreparationFixture, expectedStatus ...string) []byte {
	t.Helper()
	status := "confirmed"
	if len(expectedStatus) == 1 {
		status = expectedStatus[0]
	}
	var frozen []byte
	if err := f.a.DBPool().QueryRow(ctx, `SELECT jsonb_build_object('authority',to_jsonb(a),'preparation',to_jsonb(p),'task',to_jsonb(t),'send',to_jsonb(s),'authorization',(SELECT to_jsonb(x) FROM movie_upgrade_send_authorization x WHERE x.operation_id=a.operation_id),'dispatch',(SELECT to_jsonb(x) FROM movie_upgrade_send_dispatch x WHERE x.operation_id=a.operation_id),'acquisition',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM acquisition_reservation x WHERE x.operation_id=a.operation_id),'reservation',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_reservation x WHERE x.operation_id=a.operation_id),'requirement',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM download_requirement x WHERE x.operation_id=a.operation_id)) FROM movie_upgrade_authority a JOIN movie_upgrade_preparation p USING(operation_id) JOIN movie_upgrade_send_result s USING(operation_id) JOIN download_task t ON t.id=p.task_id WHERE a.operation_id=$1 AND s.status=$2`, f.operation, status).Scan(&frozen); err != nil {
		t.Fatal("frozen upgrade identity read failed")
	}
	return frozen
}

func m8UpgradeWatchSchedulerWaiting() bool {
	buffer := make([]byte, 2<<20)
	n := runtime.Stack(buffer, true)
	if n == len(buffer) {
		return false
	}
	for _, goroutine := range strings.Split(string(buffer[:n]), "\n\n") {
		lines := strings.Split(goroutine, "\n")
		if len(lines) > 1 && strings.Contains(lines[0], "[select]") && strings.Contains(lines[1], ".(*App).runMovieUpgradeTrackingWorker(") {
			return true
		}
	}
	return false
}
