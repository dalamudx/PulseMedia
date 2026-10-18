package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

type trackingAppClock struct{}

func (trackingAppClock) Now() time.Time                  { return time.Now() }
func (trackingAppClock) Since(t time.Time) time.Duration { return time.Since(t) }

type trackingAppNative struct {
	hash, path, tag string
	raw             []byte
}

func TestPublicUpgradeTrackingAppPG(t *testing.T) {
	for _, mode := range []string{"completed_restart", "expired_running", "organization", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			var adds, infos, files, exports atomic.Int32
			var snapshot atomic.Pointer[trackingAppNative]
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					_, _ = io.WriteString(w, "Ok.")
				case "/api/v2/torrents/add":
					adds.Add(1)
					if e := r.ParseMultipartForm(ports.MaxTorrentMetainfoBytes); e != nil {
						t.Error(e)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					defer func() {
						if e := r.MultipartForm.RemoveAll(); e != nil {
							t.Error(e)
						}
					}()
					file, _, e := r.FormFile("torrents")
					if e != nil {
						t.Error(e)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					raw, e := io.ReadAll(file)
					_ = file.Close()
					current := snapshot.Load()
					if e != nil || current == nil || !bytes.Equal(raw, current.raw) || r.FormValue("savepath") != current.path || r.FormValue("tags") != "pulsemedia,"+current.tag {
						t.Error("original byte/path/tag mismatch")
					}
					_, _ = io.WriteString(w, "Ok.")
				case "/api/v2/torrents/info":
					infos.Add(1)
					current := snapshot.Load()
					if current == nil {
						t.Error("query before frozen snapshot")
						_, _ = io.WriteString(w, "[]")
						return
					}
					_ = json.NewEncoder(w).Encode([]any{map[string]any{"hash": current.hash, "name": "Fight.Club.1999.2160p.Bluray.x265.mkv", "tags": current.tag, "save_path": current.path, "size": 120, "downloaded": 120, "progress": 1, "state": "uploading"}})
				case "/api/v2/torrents/files":
					files.Add(1)
					_ = json.NewEncoder(w).Encode([]any{map[string]any{"index": 0, "name": "Fight.Club.1999.2160p.Bluray.x265.mkv", "size": 120, "progress": 1, "priority": 1}})
				case "/api/v2/torrents/export":
					exports.Add(1)
					current := snapshot.Load()
					if current == nil {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					w.Header().Set("Content-Type", "application/x-bittorrent")
					_, _ = w.Write(current.raw)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			var start func() func()
			var stop func()
			upgradeMode, cutoff := ports.UpgradeStopAtCutoff, 1000
			if mode == "replacement" {
				upgradeMode, cutoff = ports.UpgradeReplaceIfBetter, 300
			}
			f := newPublicPreparationFixtureWithMode(t, upgradeMode, cutoff, func(f *publicPreparationFixture) {
				master, e := crypto.EnsureMasterKey(f.a.Config().DataDir, f.a.Config().SecretKey)
				if e != nil {
					t.Fatal(e)
				}
				encrypted, e := crypto.Encrypt(master, []byte("tracking-native-password"))
				if e != nil {
					t.Fatal(e)
				}
				f.exec(t, `UPDATE download_client SET url=$2,username='native-admin',password_encrypted=$3 WHERE id=$1`, f.client, server.URL, encrypted)
				if mode == "organization" || mode == "replacement" {
					preparePublicUpgradePhysicalFiles(t, f.publicUpgradeFixture)
				}
				start = publicUpgradeAppStarter(t, f.publicUpgradeFixture)
				stop = start()
			})
			t.Cleanup(func() { stop() })
			body := publicSendBody(t, f)
			original := f.unchangedFiles(t)
			var hash, path string
			if e := f.a.DBPool().QueryRow(context.Background(), `SELECT evidence->'proof'->>'info_hash',evidence->>'save_path' FROM movie_upgrade_preparation WHERE operation_id=$1`, f.operation).Scan(&hash, &path); e != nil {
				t.Fatal(e)
			}
			snapshot.Store(&trackingAppNative{hash: hash, path: path, tag: "pulsemedia:op:" + f.operation.String(), raw: bytes.Clone(f.raw)})
			sent := f.command("upgrade-sends", body, "app-tracking-send", f.admin, nil)
			if sent.Code != http.StatusAccepted {
				t.Fatal(sent.Code, sent.Body.String())
			}
			selectionWaitFor(t, func() bool {
				var yes bool
				e := f.a.DBPool().QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM movie_upgrade_send_result WHERE operation_id=$1 AND status='confirmed')`, f.operation).Scan(&yes)
				if e != nil {
					t.Fatal(e)
				}
				return yes
			})
			if mode != "expired_running" {
				waitPublicUpgradeObservation(t, func() bool {
					var yes bool
					e := f.a.DBPool().QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM movie_upgrade_tracking_job b JOIN movie_upgrade_tracking_job_claim c USING(job_id) JOIN movie_upgrade_tracking_result r USING(observation_id) JOIN job j ON j.id=b.job_id WHERE b.operation_id=$1 AND r.outcome='completed' AND r.disposition='organization_review_required' AND j.state='succeeded' AND to_jsonb(j)=pulse_upgrade_tracking_terminal_job(r.observation_id))`, f.operation).Scan(&yes)
					if e != nil {
						t.Fatal(e)
					}
					return yes
				})
				if infos.Load() != 2 || files.Load() != 1 || exports.Load() != 1 {
					t.Fatal("incomplete native proof", infos.Load(), files.Load(), exports.Load())
				}
				var verifyOrganization func()
				if mode == "organization" || mode == "replacement" {
					verifyOrganization = exercisePublicUpgradeOrganization(t, f)
				}
				stop()
				stop = start()
				if verifyOrganization != nil {
					verifyOrganization()
				} else {
					time.Sleep(1200 * time.Millisecond)
				}
			} else {
				stop()
				pool, e := pgxpool.New(context.Background(), f.a.Config().Database.URL)
				if e != nil {
					t.Fatal(e)
				}
				defer pool.Close()
				preparation := publicPreparationStoreForPool(t, pool)
				runtime, e := postgres.NewMovieUpgradeTrackingRuntimeStore(preparation)
				if e != nil {
					t.Fatal(e)
				}
				if n, e := runtime.ScheduleMovieUpgradeTracking(context.Background()); e != nil || n != 1 {
					t.Fatal(n, e)
				}
				queue := jobs.NewQueue(pool, idgen.New(), trackingAppClock{})
				j, e := queue.Claim(context.Background(), []string{ports.MovieUpgradeTrackingJobKind}, "expired-app-tracking", 100*time.Millisecond)
				if e != nil || j == nil {
					t.Fatal(j, e)
				}
				deadline := time.Now().Add(time.Second)
				for {
					var expired bool
					if e = pool.QueryRow(context.Background(), `SELECT lease_until<=clock_timestamp() FROM job WHERE id=$1`, j.ID).Scan(&expired); e != nil {
						t.Fatal(e)
					}
					if expired {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("expiry missing")
					}
					time.Sleep(10 * time.Millisecond)
				}
				stop = start()
				time.Sleep(1200 * time.Millisecond)
				var untouched bool
				if e = f.a.DBPool().QueryRow(context.Background(), `SELECT state='running' AND lease_owner='expired-app-tracking' AND lease_token=1 AND NOT EXISTS(SELECT 1 FROM movie_upgrade_tracking_observation) FROM job WHERE id=$1`, j.ID).Scan(&untouched); e != nil || !untouched || infos.Load() != 0 {
					t.Fatal("expired takeover", untouched, infos.Load(), e)
				}
			}
			if adds.Load() != 1 || f.fetches.Load() != 2 || (mode != "organization" && mode != "replacement" && f.unchangedFiles(t) != original) {
				t.Fatal("repeat send/fetch or original facts changed", adds.Load(), f.fetches.Load())
			}
			recovered := f.command("upgrade-sends", body, "app-tracking-send", f.admin, nil)
			if recovered.Code != http.StatusAccepted || recovered.Body.String() != sent.Body.String() {
				t.Fatal("SEND receipt changed", recovered.Code)
			}
		})
	}
}
