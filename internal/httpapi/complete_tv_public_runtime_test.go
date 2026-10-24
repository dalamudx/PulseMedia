package httpapi_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
)

// Exercise the canonical App router and worker together from a fresh reviewed
// candidate: no acquisition, task or preparation exists before the HTTP command.
func TestCompleteTVPublicAppNativeCommandAndRecovery(t *testing.T) {
	runCompleteTVPublicAppNative(t, false)
}

func TestCompleteTVTrackingRuntimePublicPartialThenCompleted(t *testing.T) {
	runCompleteTVPublicAppNative(t, true)
}

func runCompleteTVPublicAppNative(t *testing.T, tracking bool, watch ...bool) {
	useHint := len(watch) == 1 && watch[0]
	for _, lost := range []bool{false, true} {
		if os.Getenv("PULSE_COMPLETE_UI_BROWSER") == "1" && lost {
			continue
		}
		t.Run(map[bool]string{false: "ack", true: "lost-ack"}[lost], func(t *testing.T) {
			f := newCompleteTVFixture(t)
			ctx := context.Background()
			key := bytes.Repeat([]byte{23}, 32)
			var adds, fetches, lookups, invalid atomic.Int32
			var remoteComplete atomic.Bool
			var mu sync.Mutex
			var remoteTag string
			var expectedPath string
			if err := f.f.a.DBPool().QueryRow(ctx, `SELECT path FROM library_root WHERE id=(SELECT root_id FROM subscription WHERE id=$1)`, f.f.sub.Id).Scan(&expectedPath); err != nil {
				t.Fatal(err)
			}
			marker, err := os.OpenFile(filepath.Join(expectedPath, ".pulsemedia_root"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = marker.WriteString("pulsemedia-native-public-root\n"); err != nil {
				_ = marker.Close()
				t.Fatal(err)
			}
			if err = marker.Close(); err != nil {
				t.Fatal(err)
			}
			if useHint {
				if err = os.MkdirAll(filepath.Join(expectedPath, "Show"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = library.NewScanner(idgen.New(), rootfs.NewFilesystem(), postgres.NewScanObservationStore(f.f.a.DBPool())).ScanRoot(ctx, f.f.sub.RootId); err != nil {
				t.Fatal(err)
			}
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if os.Getenv("PULSE_COMPLETE_UI_BROWSER") == "1" && (r.URL.Query().Get("t") == "search" || r.URL.Query().Get("t") == "tvsearch" || r.URL.Query().Get("t") == "movie") {
					w.Header().Set("Content-Type", "application/rss+xml")
					_, _ = io.WriteString(w, `<?xml version="1.0"?><rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><title>Native complete coverage</title><item><title>Mapping.Browser.Show.S01E01E02.1080p.WEB-DL</title><guid>native-complete-browser</guid><link>http://`+r.Host+`/native?passkey=private-link</link><size>1024</size><torznab:attr name="seeders" value="5"/><torznab:attr name="infohash" value="d497dcf93e2dbd4b917ba68788ba7d42590cf594"/></item><item><title>Mapping.Browser.Show.S01E01.1080p.WEB-DL</title><guid>native-single-browser-e01</guid><link>http://`+r.Host+`/single-e01?passkey=private-link</link><size>512</size><torznab:attr name="seeders" value="5"/><torznab:attr name="infohash" value="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"/></item><item><title>Mapping.Browser.Show.S01E02.1080p.WEB-DL</title><guid>native-single-browser-e02</guid><link>http://`+r.Host+`/single-e02?passkey=private-link</link><size>512</size><torznab:attr name="seeders" value="5"/><torznab:attr name="infohash" value="cccccccccccccccccccccccccccccccccccccccc"/></item></channel></rss>`)
					return
				}
				fetches.Add(1)
				if r.URL.RawQuery != "passkey=private-link" {
					invalid.Add(1)
				}
				_, _ = io.WriteString(w, completeTVRaw)
			}))
			defer source.Close()
			native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					if r.ParseForm() != nil || r.Form.Get("username") != "current-user" || r.Form.Get("password") != "private-password" {
						invalid.Add(1)
					}
					_, _ = io.WriteString(w, "Ok.")
				case "/api/v2/torrents/add":
					adds.Add(1)
					if r.ParseMultipartForm(1<<20) != nil {
						invalid.Add(1)
						w.WriteHeader(500)
						return
					}
					defer func() { _ = r.MultipartForm.RemoveAll() }()
					file, _, err := r.FormFile("torrents")
					if err != nil {
						invalid.Add(1)
						w.WriteHeader(500)
						return
					}
					raw, err := io.ReadAll(file)
					_ = file.Close()
					if err != nil || string(raw) != completeTVRaw || r.FormValue("urls") != "" || r.FormValue("savepath") != expectedPath {
						invalid.Add(1)
					}
					mu.Lock()
					remoteTag = r.FormValue("tags")
					mu.Unlock()
					if lost {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err == nil {
							_ = conn.Close()
						}
						return
					}
					_, _ = io.WriteString(w, "Ok.")
				case "/api/v2/torrents/info":
					lookups.Add(1)
					mu.Lock()
					tag := remoteTag
					mu.Unlock()
					if !strings.Contains(tag, r.URL.Query().Get("tag")) {
						invalid.Add(1)
					}
					progress, downloaded, state := 0, 0, "downloading"
					if remoteComplete.Load() {
						progress, downloaded, state = 1, 1024, "uploading"
					}
					_ = json.NewEncoder(w).Encode([]any{map[string]any{"hash": "d497dcf93e2dbd4b917ba68788ba7d42590cf594", "save_path": expectedPath, "size": 1024, "downloaded": downloaded, "progress": progress, "state": state, "tags": tag}})
				case "/api/v2/torrents/export":
					_, _ = io.WriteString(w, completeTVRaw)
				case "/api/v2/torrents/files":
					_, _ = io.WriteString(w, trackingFilesJSON)
				default:
					invalid.Add(1)
					w.WriteHeader(500)
				}
			}))
			defer native.Close()
			encrypt := func(value string) []byte {
				raw, err := crypto.Encrypt(key, []byte(value))
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}
			f.exec(t, `UPDATE download_client SET url=$1,username='current-user',password_encrypted=$2`, native.URL, encrypt("private-password"))
			f.exec(t, `UPDATE indexer SET url=$1,api_key_encrypted=$2`, source.URL, encrypt("private-api-key"))
			f.exec(t, `UPDATE release_candidate SET link_encrypted=$1,source_config_version=(SELECT config_version FROM indexer WHERE id=indexer_id)`, encrypt(source.URL+"/native?passkey=private-link"))
			f.exec(t, `UPDATE indexer_query SET config_version=(SELECT config_version FROM indexer WHERE id=indexer_id)`)
			preview := f.preview(t)
			review, err := f.svc.Review(ctx, f.actor, f.coverageFixture.command(preview), "public-runtime-review")
			if err != nil {
				t.Fatal(err)
			}
			f.command = ports.AcquireCompleteTVCoverageCommand{SubscriptionID: f.f.sub.Id, ReviewID: review.ID, ExpectedInputStamp: preview.InputStamp, ExpectedMaterialStamp: preview.MaterialStamp, Confirmed: true}
			if f.work(t) != [9]int{} {
				t.Fatal("work existed before public command", f.work(t))
			}

			cfg := config.NewDefault()
			cfg.DataDir = t.TempDir()
			cfg.SecretKey = hex.EncodeToString(key)
			cfg.Database.URL = f.f.a.Config().Database.URL
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			cfg.Server.HTTPAddr = listener.Addr().String()
			if err = listener.Close(); err != nil {
				t.Fatal(err)
			}
			baseURL := "http://" + cfg.Server.HTTPAddr
			transport := &http.Transport{}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
			start := func() func() {
				a, err := app.New(cfg, app.WithLogger(logging.NewLogger(io.Discard, "error")))
				if err != nil {
					t.Fatal(err)
				}
				runctx, cancel := context.WithCancel(ctx)
				done := make(chan error, 1)
				go func() { done <- a.Run(runctx) }()
				var once sync.Once
				stop := func() {
					once.Do(func() {
						cancel()
						select {
						case err := <-done:
							if err != nil {
								t.Error(err)
							}
						case <-time.After(20 * time.Second):
							t.Error("public App failed to drain")
							_ = a.Close()
						}
					})
				}
				t.Cleanup(stop)
				runtimeTVWait(t, func() bool {
					response, err := client.Get(baseURL + "/api/v1/health/live")
					if err != nil {
						return false
					}
					_ = response.Body.Close()
					return true
				})
				return stop
			}
			stop := start()
			probeTVSelectionApp(t, client, baseURL, f)
			if os.Getenv("PULSE_COMPLETE_UI_BROWSER") == "1" {
				runCompleteTVPublicBrowser(t, ctx, baseURL, f)
				stop()
				if fetches.Load() != 1 || adds.Load() != 1 || invalid.Load() != 0 {
					t.Fatalf("browser native wire counts: fetch=%d add=%d invalid=%d", fetches.Load(), adds.Load(), invalid.Load())
				}
				return
			}
			call := func() ([]byte, ports.AcquireResult) {
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/subscriptions/"+f.f.sub.Id.String()+"/complete-tv-acquisitions", strings.NewReader(completePublicBody(f)))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Origin", baseURL)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Idempotency-Key", "public-native-command")
				req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: f.f.admin})
				response, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = response.Body.Close() }()
				raw, err := io.ReadAll(response.Body)
				var receipt ports.AcquireResult
				if err != nil || response.StatusCode != 202 || json.Unmarshal(raw, &receipt) != nil || receipt.Status != "queued" || len(receipt.Reservations) != 2 {
					t.Fatalf("public receipt: %d %s", response.StatusCode, raw)
				}
				return raw, receipt
			}
			first, receipt := call()
			t.Cleanup(func() {
				if t.Failed() {
					var state, failure string
					if err := f.f.a.DBPool().QueryRow(ctx, `SELECT state,coalesce(error,'') FROM job WHERE id=$1`, receipt.JobID).Scan(&state, &failure); err != nil {
						t.Log("job diagnostic", err)
					}
					t.Log("job state", state, failure, "native counts", fetches.Load(), adds.Load(), lookups.Load(), invalid.Load())
				}
			})
			runtimeTVWait(t, func() bool {
				var state string
				err := f.f.a.DBPool().QueryRow(ctx, `SELECT state FROM download_task WHERE operation_id=$1`, receipt.OperationID).Scan(&state)
				return err == nil && state == "tracking"
			})
			if tracking {
				// Move only this fixture's persisted due timestamp, then wait for the
				// actual worker commit. No elapsed sleep is accepted as evidence.
				f.exec(t, `UPDATE download_task SET updated_at=clock_timestamp()-interval '31 seconds' WHERE operation_id=$1`, receipt.OperationID)
				runtimeTVWait(t, func() bool {
					var ok bool
					_ = f.f.a.DBPool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job WHERE kind=$1 AND state='succeeded' AND result->>'state'='tracking')`, ports.CompleteTVTrackingJobKind).Scan(&ok)
					return ok
				})
				if !useHint {
					stop()
				}
				var partial bool
				if err := f.f.a.DBPool().QueryRow(ctx, `SELECT state='tracking' AND progress=0 AND downloaded_bytes=0 FROM download_task WHERE operation_id=$1`, receipt.OperationID).Scan(&partial); err != nil || !partial {
					t.Fatal("partial observation invented progress", err)
				}
				scheduler := postgres.NewCompleteTVTrackingRuntimeStore(f.f.a.DBPool(), idgen.New())
				for range 4 {
					if err := scheduler.ScheduleCompleteTVTracking(ctx); err != nil {
						t.Fatal(err)
					}
				}
				var jobs int
				if err := f.f.a.DBPool().QueryRow(ctx, `SELECT count(*) FROM job WHERE kind=$1`, ports.CompleteTVTrackingJobKind).Scan(&jobs); err != nil || jobs != 1 {
					t.Fatal("partial busy loop", jobs, err)
				}
				if useHint {
					assertCompleteTVWatchEvent(t, f.f.a.DBPool(), expectedPath, receipt.OperationID, func() { remoteComplete.Store(true) })
				} else {
					remoteComplete.Store(true)
					f.exec(t, `UPDATE job SET updated_at=clock_timestamp()-interval '31 seconds' WHERE kind=$1`, ports.CompleteTVTrackingJobKind)
					stop = start()
				}
				runtimeTVWait(t, func() bool {
					var ok bool
					_ = f.f.a.DBPool().QueryRow(ctx, `SELECT state='completed' AND progress=1 AND total_bytes=1024 AND downloaded_bytes=1024 AND organization_state='review_required' AND transfer_plan_id IS NULL FROM download_task WHERE operation_id=$1`, receipt.OperationID).Scan(&ok)
					return ok
				})
				stop()
				for range 4 {
					if err := scheduler.ScheduleCompleteTVTracking(ctx); err != nil {
						t.Fatal(err)
					}
				}
				if err := f.f.a.DBPool().QueryRow(ctx, `SELECT count(*) FROM job WHERE kind=$1`, ports.CompleteTVTrackingJobKind).Scan(&jobs); err != nil || jobs != 2 {
					t.Fatal("completed scheduled again", jobs, err)
				}
			}
			stop()
			stop = start()
			replay, again := call()
			stop()
			if !bytes.Equal(first, replay) || receipt.OperationID != again.OperationID || receipt.JobID != again.JobID {
				t.Fatal("public restart did not retain original receipt")
			}
			mu.Lock()
			tag := remoteTag
			mu.Unlock()
			if tag != "pulsemedia,pulsemedia:op:"+receipt.OperationID.String() || adds.Load() != 1 || fetches.Load() != 1 || invalid.Load() != 0 || (!tracking && lost && lookups.Load() != 1) || (!tracking && !lost && lookups.Load() != 0) || (tracking && lookups.Load() != 4+map[bool]int32{false: 0, true: 1}[lost]) {
				t.Fatal("native request/observation count", adds.Load(), fetches.Load(), lookups.Load(), invalid.Load())
			}
			var authorities, tasks, preparations, units, permits, organization, imported int
			err = f.f.a.DBPool().QueryRow(ctx, `SELECT (SELECT count(*) FROM complete_tv_execution_authority),(SELECT count(*) FROM download_task),(SELECT count(*) FROM complete_tv_submission_preparation),(SELECT count(*) FROM complete_tv_submission_unit),(SELECT count(*) FROM complete_tv_physical_permit),(SELECT count(*) FROM job WHERE kind='download_organization'),(SELECT count(*) FROM unit_fact WHERE stage='imported')`).Scan(&authorities, &tasks, &preparations, &units, &permits, &organization, &imported)
			if err != nil || authorities != 1 || tasks != 1 || preparations != 1 || units != 2 || permits != 0 || organization != 0 || imported != 0 {
				t.Fatal("public runtime persistence", err, authorities, tasks, preparations, units, permits, organization, imported)
			}
			var lanes, reservations, demands int
			err = f.f.a.DBPool().QueryRow(ctx, `SELECT (SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL),(SELECT count(*) FROM unit_reservation WHERE state='dispatched'),(SELECT count(*) FROM download_requirement WHERE state='pending')`).Scan(&lanes, &reservations, &demands)
			if err != nil || lanes != 2 || reservations != 2 || demands != 2 {
				t.Fatal("lost full-set protection", err, lanes, reservations, demands)
			}
		})
	}
}
