package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicUpgradeSendAppCookieRestart(t *testing.T) {
	for _, ack := range []string{"Ok.", "ambiguous"} {
		t.Run(ack, func(t *testing.T) {
			var adds, queries atomic.Int32
			uploaded := make(chan []byte, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					if _, err := io.WriteString(w, "Ok."); err != nil {
						t.Error(err)
					}
				case "/api/v2/torrents/info":
					queries.Add(1)
					if _, err := io.WriteString(w, "[]"); err != nil {
						t.Error(err)
					}
				case "/api/v2/torrents/add":
					adds.Add(1)
					if err := r.ParseMultipartForm(ports.MaxTorrentMetainfoBytes); err != nil {
						t.Error(err)
						http.Error(w, "invalid form", 400)
						return
					}
					defer func() {
						if err := r.MultipartForm.RemoveAll(); err != nil {
							t.Error(err)
						}
					}()
					file, _, err := r.FormFile("torrents")
					if err != nil {
						t.Error(err)
						http.Error(w, "missing torrent", 400)
						return
					}
					raw, err := io.ReadAll(io.LimitReader(file, ports.MaxTorrentMetainfoBytes+1))
					if e := file.Close(); e != nil {
						t.Error(e)
					}
					if err != nil {
						t.Error(err)
						return
					}
					uploaded <- raw
					if r.FormValue("savepath") == "" || r.FormValue("tags") == "" {
						t.Error("missing frozen path/tag")
					}
					if _, err = io.WriteString(w, ack); err != nil {
						t.Error(err)
					}
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			var start func() func()
			var stop func()
			f := newPublicPreparationFixture(t, func(f *publicPreparationFixture) {
				master, err := crypto.EnsureMasterKey(f.a.Config().DataDir, f.a.Config().SecretKey)
				if err != nil {
					t.Fatal(err)
				}
				encrypted, err := crypto.Encrypt(master, []byte("native-public-password"))
				if err != nil {
					t.Fatal(err)
				}
				f.exec(t, "UPDATE download_client SET url=$2,username='native-admin',password_encrypted=$3 WHERE id=$1", f.client, server.URL, encrypted)
				start = publicUpgradeAppStarter(t, f.publicUpgradeFixture)
				stop = start()
			})
			t.Cleanup(func() { stop() })
			body := publicSendBody(t, f)
			original := f.unchangedFiles(t)
			if adds.Load() != 0 || f.fetches.Load() != 1 {
				t.Fatal("preparation sent")
			}
			first := f.command("upgrade-sends", body, "app-public-send", f.admin, nil)
			if first.Code != 202 {
				t.Fatalf("App send %d %s", first.Code, first.Body.String())
			}
			var receipt ports.MovieUpgradeSendJobReceipt
			if err := json.Unmarshal(first.Body.Bytes(), &receipt); err != nil {
				t.Fatal(err)
			}
			selectionWaitFor(t, func() bool {
				var complete bool
				err := f.a.DBPool().QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM movie_upgrade_send_result r JOIN movie_upgrade_send_job b USING(operation_id) JOIN job j ON j.id=b.job_id WHERE r.operation_id=$1 AND j.state='succeeded')`, f.operation).Scan(&complete)
				if err != nil {
					t.Fatal(err)
				}
				return complete
			})
			select {
			case raw := <-uploaded:
				if !bytes.Equal(raw, f.raw) {
					t.Fatal("sent altered bytes")
				}
			case <-time.After(time.Second):
				t.Fatal("missing actual Add bytes")
			}
			if ack != "Ok." {
				waitPublicUpgradeObservation(t, func() bool {
					var observed bool
					err := f.a.DBPool().QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM movie_upgrade_observation_job b JOIN job j ON j.id=b.job_id JOIN movie_upgrade_observation_result r ON r.observation_id=b.observation_id WHERE b.operation_id=$1 AND j.state='succeeded' AND r.disposition='manual_review' AND to_jsonb(j)=pulse_upgrade_observation_terminal_job(b.observation_id))`, f.operation).Scan(&observed)
					if err != nil {
						t.Fatal(err)
					}
					return observed
				})
			} else {
				waitPublicUpgradeObservation(t, func() bool {
					var observed bool
					err := f.a.DBPool().QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM movie_upgrade_tracking_job b JOIN movie_upgrade_tracking_job_claim c USING(job_id) JOIN movie_upgrade_tracking_result r USING(observation_id) JOIN job j ON j.id=b.job_id WHERE b.operation_id=$1 AND r.disposition='manual_review' AND to_jsonb(j)=pulse_upgrade_tracking_terminal_job(r.observation_id))`, f.operation).Scan(&observed)
					if err != nil {
						t.Fatal(err)
					}
					return observed
				})
			}
			if adds.Load() != 1 || f.fetches.Load() != 2 || f.unchangedFiles(t) != original {
				t.Fatal("repeat Add/fetch or changed original facts", adds.Load(), f.fetches.Load())
			}
			queryCount := queries.Load()
			stop()
			stop = start()
			recovered := f.command("upgrade-sends", body, "app-public-send", f.admin, nil)
			if recovered.Code != 202 || recovered.Body.String() != first.Body.String() {
				t.Fatalf("restart replay %d %s", recovered.Code, recovered.Body.String())
			}
			<-time.After(1200 * time.Millisecond)
			if adds.Load() != 1 || f.fetches.Load() != 2 || queries.Load() != queryCount {
				t.Fatal("restart repeated physical operation")
			}
			if f.unchangedFiles(t) != original {
				t.Fatal("restart changed original facts")
			}
		})
	}
}
