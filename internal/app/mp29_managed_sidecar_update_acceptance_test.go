package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/adapter/tmdbartwork"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/budget"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/mediascrape"
	"github.com/dalamudx/PulseMedia/internal/store/mediasidecar"
	scrapeusecase "github.com/dalamudx/PulseMedia/internal/usecase/mediascrape"
	"github.com/dalamudx/PulseMedia/internal/usecase/metadatasidecar"
	"github.com/google/uuid"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// SQL prepares imported inventory; ordinary generation claims the public scrape's
// real queue job. Cookie routes, provider adapters, PG and native files are real.
// This test does not claim App.Run scheduling or external TMDB availability.
func TestMP29ManagedSidecarCookieLifecycle(t *testing.T) {
	for _, scenario := range []string{"movie", "tv", "legacy_manual", "edited_after_preview", "lost_commit_receipt"} {
		t.Run(scenario, func(t *testing.T) {
			f := m7NewScrapeFixture(t)
			kind := "movie"
			if scenario == "tv" {
				kind = "tv"
			}
			v := f.prepareInventory(t, kind)
			client, phase, calls := mp29NativeProvider(t, kind)
			shared := budget.NewMetadataBudget(f.app.Clock())
			metadata := tmdb.NewClient("owned-sidecar-key", tmdb.WithHTTPClient(client), tmdb.WithRequestBudget(shared))
			artwork := tmdbartwork.New(client, shared)
			fs := rootfs.NewFilesystem()
			var file, unit uuid.UUID
			if e := f.pool.QueryRow(t.Context(), `SELECT fu.file_id,fu.unit_id FROM library_file_unit fu JOIN library_file f ON f.id=fu.file_id WHERE f.rel_path=$1 AND fu.retired_at IS NULL`, v.relative).Scan(&file, &unit); e != nil {
				t.Fatal(e)
			}
			nfo := filepath.Join(v.root, v.dir, v.nfo)
			poster := filepath.Join(v.root, v.dir, "poster.jpg")
			if scenario == "legacy_manual" {
				for _, p := range []string{nfo, poster} {
					if e := os.WriteFile(p, []byte("manual legacy bytes"), 0600); e != nil {
						t.Fatal(e)
					}
				}
			}
			batch := m7ScrapeBatch(t, f.request(t.Context(), "POST", "/api/v1/media/scrape", `{"all":true}`, "mp29-first-generation", f.cookie, ""))
			if batch.Total != 1 {
				t.Fatalf("batch=%+v", batch)
			}
			queue := jobs.NewQueue(f.pool, f.app.IDGen(), f.app.Clock())
			job, e := queue.Claim(t.Context(), []string{ports.MediaSidecarJobKind}, "mp29-ordinary", 2*time.Minute)
			if e != nil || job == nil {
				t.Fatalf("claim %v", e)
			}
			if e = metadatasidecar.NewService(mediasidecar.NewRepository(f.pool, f.app.IDGen()), fs, metadata, artwork).Execute(t.Context(), ports.JobLease{ID: job.ID, Owner: "mp29-ordinary", Token: job.LeaseToken}); e != nil {
				t.Fatal(e)
			}
			var trusted int
			if e = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM managed_sidecar_content WHERE file_id=$1`, file).Scan(&trusted); e != nil {
				t.Fatal(e)
			}
			want := 2
			if scenario == "legacy_manual" {
				want = 0
			}
			if trusted != want {
				t.Fatalf("ordinary trusted count=%d want=%d", trusted, want)
			}
			originalNFO, e := os.ReadFile(nfo)
			if e != nil {
				t.Fatal(e)
			}
			originalPoster, e := os.ReadFile(poster)
			if e != nil {
				t.Fatal(e)
			}
			var repo ports.ManagedSidecarUpdateRepository = mediasidecar.NewManagedUpdateRepository(f.pool, f.app.IDGen())
			if scenario == "lost_commit_receipt" {
				repo = mp29LostCompletion{repo}
			}
			svc := metadatasidecar.NewManagedUpdateService(repo, fs, metadata, artwork, f.app.IDGen())
			h := httpapi.NewServerHandler(f.app, f.auth, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			h.SetMediaScrapeService(scrapeusecase.NewService(mediascrape.NewRepository(f.pool), f.app.IDGen()))
			h.SetManagedSidecarUpdateService(svc)
			base := "/api/v1/library/files/" + file.String() + "/sidecar-update-"
			phase.Store(1)
			metadata = tmdb.NewClient("owned-sidecar-key", tmdb.WithHTTPClient(client), tmdb.WithRequestBudget(shared))
			svc = metadatasidecar.NewManagedUpdateService(repo, fs, metadata, artwork, f.app.IDGen())
			h.SetManagedSidecarUpdateService(svc)
			// Route registration freezes dependencies after the fresh provider is configured.
			f.router = httpapi.NewRouter(f.app, h, f.auth, nil)
			if os.Getenv("PULSE_MP29_CHROME") == "1" && (scenario == "movie" || scenario == "tv" || scenario == "legacy_manual") {
				liveScenario := "completed"
				if scenario == "tv" {
					liveScenario = "unknown"
				}
				if scenario == "legacy_manual" {
					liveScenario = "held"
				}
				mp29RunChrome(t, f, file, unit, liveScenario)
				actualNFO, err := os.ReadFile(nfo)
				if err != nil {
					t.Fatal(err)
				}
				actualPoster, err := os.ReadFile(poster)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "legacy_manual" {
					if !bytes.Equal(actualNFO, originalNFO) || !bytes.Equal(actualPoster, originalPoster) {
						t.Fatal("Chrome clobbered manual sidecars")
					}
				} else {
					if bytes.Equal(actualNFO, originalNFO) || bytes.Equal(actualPoster, originalPoster) {
						t.Fatal("Chrome did not update real sidecars")
					}
				}
				v.assertSource(t)
				return
			}
			w := f.request(t.Context(), "POST", base+"previews", `{"unit_id":"`+unit.String()+`"}`, "", f.cookie, "")
			m7ScrapeCode(t, w, 200)
			var preview ports.ManagedSidecarUpdateResult
			if e = json.Unmarshal(w.Body.Bytes(), &preview); e != nil {
				t.Fatal(e)
			}
			if scenario == "legacy_manual" {
				if preview.State != "held" {
					t.Fatalf("manual adopted %+v", preview)
				}
				for _, p := range []string{nfo, poster} {
					b, e := os.ReadFile(p)
					if e != nil || string(b) != "manual legacy bytes" {
						t.Fatalf("manual changed %v", e)
					}
				}
				v.assertSource(t)
				return
			}
			if preview.State != "preview" || len(preview.Artifacts) != 2 {
				t.Fatalf("preview %+v", preview)
			}
			for _, a := range preview.Artifacts {
				if a.State != "ready" || a.OldHash == a.NewHash {
					t.Fatalf("artifact %+v", a)
				}
			}
			if scenario == "edited_after_preview" {
				if e = os.WriteFile(nfo, []byte("concurrent manual edit"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			beforeCalls := calls.Load()
			body := `{"preview_id":"` + preview.ID.String() + `","confirmed":true}`
			w = f.request(t.Context(), "POST", base+"commands", body, "mp29-explicit-update", f.cookie, "")
			if scenario == "lost_commit_receipt" {
				m7ScrapeCode(t, w, 503)
			} else {
				m7ScrapeCode(t, w, 200)
				var result ports.ManagedSidecarUpdateResult
				if e = json.Unmarshal(w.Body.Bytes(), &result); e != nil {
					t.Fatal(e)
				}
				expected := "completed"
				if scenario == "edited_after_preview" {
					expected = "review_required"
				}
				if result.State != expected {
					t.Fatalf("result %+v", result)
				}
			}
			if calls.Load() != beforeCalls {
				t.Fatal("confirmation repeated network instead of using frozen preview")
			}
			afterNFO, e := os.ReadFile(nfo)
			if e != nil {
				t.Fatal(e)
			}
			afterPoster, e := os.ReadFile(poster)
			if e != nil {
				t.Fatal(e)
			}
			if scenario == "edited_after_preview" {
				if string(afterNFO) != "concurrent manual edit" || !bytes.Equal(afterPoster, originalPoster) {
					t.Fatal("concurrent manual contents clobbered")
				}
			} else {
				if bytes.Equal(afterNFO, originalNFO) || bytes.Equal(afterPoster, originalPoster) || !strings.Contains(string(afterNFO), "metadata version 1") {
					t.Fatal("frozen provider content not updated")
				}
				var permit uuid.UUID
				if e = f.pool.QueryRow(t.Context(), `SELECT permit FROM managed_sidecar_update WHERE id=$1`, preview.ID).Scan(&permit); e != nil {
					t.Fatal(e)
				}
				for name, old := range map[string][]byte{v.nfo: originalNFO, "poster.jpg": originalPoster} {
					b, e := os.ReadFile(filepath.Join(v.root, v.dir, ".pulsemedia-update-"+permit.String()+"-"+name+".bak"))
					if e != nil || !bytes.Equal(b, old) {
						t.Fatalf("durable original backup %s: %v", name, e)
					}
				}
			}
			frozenCalls := calls.Load()
			w = f.request(t.Context(), "GET", base+"commands/mp29-explicit-update", "", "", f.cookie, "")
			m7ScrapeCode(t, w, 200)
			if scenario == "lost_commit_receipt" && !strings.Contains(w.Body.String(), "review_required") {
				t.Fatal("unknown commit disguised")
			}
			w = f.request(t.Context(), "POST", base+"commands", body, "mp29-explicit-update", f.cookie, "")
			m7ScrapeCode(t, w, 200)
			if calls.Load() != frozenCalls {
				t.Fatal("receipt recovery performed network")
			}
			againNFO, _ := os.ReadFile(nfo)
			againPoster, _ := os.ReadFile(poster)
			if !bytes.Equal(afterNFO, againNFO) || !bytes.Equal(afterPoster, againPoster) {
				t.Fatal("original command replayed physical writes")
			}
			v.assertSource(t)
			w = f.request(t.Context(), "POST", base+"previews", `{"unit_id":"`+unit.String()+`"}`, "", nil, "")
			m7ScrapeCode(t, w, 401)
			t.Logf("MP29_COOKIE_%s_PASS trusted=%d no_replay=true video_unchanged=true", scenario, trusted)
		})
	}
}

type mp29LostCompletion struct {
	ports.ManagedSidecarUpdateRepository
}

func (r mp29LostCompletion) CompleteManagedUpdate(context.Context, *ports.Actor, ports.ManagedSidecarUpdateTicket, ports.ManagedSidecarUpdateResult, []ports.ManagedSidecarContent) (ports.ManagedSidecarUpdateResult, error) {
	return ports.ManagedSidecarUpdateResult{}, ports.ErrUnavailable
}
func mp29NativeProvider(t *testing.T, kind string) (*http.Client, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	phase, calls := &atomic.Int32{}, &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" {
			t.Error("provider mutation")
			w.WriteHeader(405)
			return
		}
		if r.URL.Path == "/t/p/w342/Sidecar.jpg" {
			img := image.NewRGBA(image.Rect(0, 0, 3, 2))
			img.Set(1, 1, color.RGBA{R: uint8(30 + phase.Load()*190), G: 90, A: 255})
			w.Header().Set("Content-Type", "image/jpeg")
			if e := jpeg.Encode(w, img, nil); e != nil {
				t.Error(e)
			}
			return
		}
		if r.URL.Path != "/3/"+kind+"/6211" {
			t.Error("unexpected metadata path")
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":6211,"title":"Sidecar","name":"Sidecar","original_title":"Sidecar","original_name":"Sidecar","overview":"metadata version %d","release_date":"2024-01-01","first_air_date":"2024-01-01","poster_path":"/Sidecar.jpg","seasons":[]}`, phase.Load())
	}))
	t.Cleanup(server.Close)
	u, e := url.Parse(server.URL)
	if e != nil {
		t.Fatal(e)
	}
	tr := &http.Transport{Proxy: nil}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: m7SidecarRoute{u, tr}, Timeout: 2 * time.Second}, phase, calls
}

func mp29RunChrome(t *testing.T, f *m7ScrapeFixture, file, unit uuid.UUID, scenario string) {
	t.Helper()
	server := httptest.NewServer(f.router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "--test", "--test-reporter=tap", "../../web/tests/managed-sidecar-update-live.test.mjs")
	chromeTemp := os.Getenv("PULSE_TEST_TEMP_ROOT")
	if !strings.HasPrefix(chromeTemp, "/data/temp/") {
		t.Fatal("owned short Chrome temp root required")
	}
	cmd.Env = append(os.Environ(), "TMPDIR="+chromeTemp, "TMP="+chromeTemp, "TEMP="+chromeTemp, "PULSE_MP29_LIVE_URL="+server.URL, "PULSE_MP29_LIVE_FILE_ID="+file.String(), "PULSE_MP29_LIVE_UNIT_ID="+unit.String(), "PULSE_MP29_LIVE_SESSION="+f.cookie.Value, "PULSE_MP29_LIVE_SCENARIO="+scenario)
	output, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("real Cookie Chrome %s: %v\n%s", scenario, e, output)
	}
	if !strings.Contains(string(output), "# tests 1\n") || !strings.Contains(string(output), "# pass 1\n") || !strings.Contains(string(output), "# fail 0\n") || !strings.Contains(string(output), "# skipped 0\n") || !strings.Contains(string(output), "MP29_LIVE_COOKIE_CHROME_PASS") {
		t.Fatalf("missing exact live Chrome receipt: %s", output)
	}
	t.Log(string(output))
}
