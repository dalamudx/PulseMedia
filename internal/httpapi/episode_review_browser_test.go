package httpapi_test

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestEpisodeCandidateReviewBrowserHarness(t *testing.T) {
	for _, mapped := range []bool{false, true} {
		name := "standard"
		if mapped {
			name = "mapped"
		}
		t.Run(name, func(t *testing.T) { runEpisodeCandidateReviewBrowserHarness(t, mapped) })
	}
}
func TestCompleteStandardCoverageBrowserHarness(t *testing.T) {
	runEpisodeCandidateReviewBrowserHarness(t, false, true)
}
func TestUnknownFourDigitEpisodeCoverageBrowserHarness(t *testing.T) {
	runEpisodeCandidateReviewBrowserHarness(t, false, false, true)
}

func runEpisodeCandidateReviewBrowserHarness(t *testing.T, mapped bool, multiProbe ...bool) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit disposable browser fixture required")
	}
	f := setupEpisodeMappingFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	var calls atomic.Int32
	releaseTitle := "Mapping.Browser.Show.2020.S01E01.1080p.WEB-DL.x264-GROUP"
	if len(multiProbe) > 0 && multiProbe[0] {
		releaseTitle = "Mapping.Browser.Show.2020.S01E01E02.1080p.WEB-DL.x264-GROUP"
	}
	if mapped {
		releaseTitle = "[Team] Mapping Browser Show - 25 [1080p WEB-DL x264]"
	}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><item><title>`+releaseTitle+`</title><guid>review-single</guid><link>http://fixture.invalid/single.torrent</link><size>4000000000</size><torznab:attr name="seeders" value="8"/></item></channel></rss>`)
	}))
	defer source.Close()
	cipher, e := crypto.Encrypt(make([]byte, 32), []byte("fixture-review-key"))
	if e != nil {
		t.Fatal(e)
	}
	idx := uuid.New()
	if _, e = f.a.DBPool().Exec(ctx, "INSERT INTO indexer(id,name,url,api_key_encrypted,categories,health) VALUES($1,'Review fixture',$2,$3,'[]','online')", idx, source.URL, cipher); e != nil {
		t.Fatal(e)
	}
	bound, _ := json.Marshal([]uuid.UUID{idx})
	if _, e = f.a.DBPool().Exec(ctx, "UPDATE subscription SET indexer_ids=$2,intent_version=intent_version+1 WHERE id=$1", f.sub.Id, bound); e != nil {
		t.Fatal(e)
	}
	clientID := uuid.New()
	if _, e = f.a.DBPool().Exec(ctx, "INSERT INTO download_client(id,name,client_type,url,username,password_encrypted,remote_path_map) VALUES($1,'TV browser fixture','qbittorrent','http://fixture.invalid','fixture',$2,'{}')", clientID, cipher); e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.DBPool().Exec(ctx, "UPDATE subscription SET state='active',downloader_id=$2,intent_version=intent_version+1 WHERE id=$1", f.sub.Id, clientID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.DBPool().Exec(ctx, "UPDATE media SET year=2020,metadata_version=metadata_version+1 WHERE id=(SELECT media_id FROM subscription WHERE id=$1)", f.sub.Id); e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.DBPool().Exec(ctx, "UPDATE media_unit SET air_date='2020-01-01',metadata_version=metadata_version+1 WHERE id=ANY($1::uuid[])", f.units); e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.DBPool().Exec(ctx, "UPDATE library_root SET health='online',baseline_completed=true WHERE id=(SELECT root_id FROM subscription WHERE id=$1)", f.sub.Id); e != nil {
		t.Fatal(e)
	}

	if mapped {
		path := "/api/v1/subscriptions/" + f.sub.Id.String() + "/episode-mapping"
		draft := ports.EpisodeMappingDraft{Mode: "manual", Entries: []ports.ManualEpisodeMappingEntry{{SourceNumber: 25, UnitID: f.units[0]}, {SourceNumber: 27, UnitID: f.units[1]}}}
		w := f.call(f.admin, "POST", path+"/preview", "", draft)
		var preview ports.EpisodeMappingPreview
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &preview) != nil || !preview.CanPublish {
			t.Fatal("mapped browser preview", w.Code, w.Body.String())
		}
		w = f.call(f.admin, "POST", path, "mapped-browser-publish", ports.EpisodeMappingCommand{Draft: draft, ExpectedInputStamp: preview.InputStamp, ExpectedSourceHash: preview.SourceHash, Confirmed: true})
		if w.Code != 200 {
			t.Fatal("mapped browser publish", w.Code, w.Body.String())
		}
	}

	probe := "/fixture-" + uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == probe && r.Method == http.MethodPost {
			var err error
			if len(multiProbe) > 1 && multiProbe[1] {
				_, err = f.a.DBPool().Exec(ctx, "UPDATE release_candidate SET title='Mapping.Browser.Show.2020.S01E1056.1080p.WEB-DL.x264-GROUP' WHERE indexer_id=$1", idx)
			} else {
				_, err = f.a.DBPool().Exec(ctx, "UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=$1", f.units[0])
			}
			if err != nil {
				t.Error(err)
				w.WriteHeader(503)
			} else {
				w.WriteHeader(204)
			}
			return
		}
		f.router.ServeHTTP(w, r)
	}))
	defer server.Close()
	scriptName := "episode-review-browser.mjs"
	if len(multiProbe) > 0 && multiProbe[0] {
		scriptName = "complete-coverage-browser.mjs"
	}
	if len(multiProbe) > 1 && multiProbe[1] {
		scriptName = "four-digit-episode-browser.mjs"
	}
	script, e := filepath.Abs("../../web/tests/" + scriptName)
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.Env = append(os.Environ(), "PULSE_REVIEW_UI_BASE="+server.URL, "PULSE_REVIEW_UI_SUB="+f.sub.Id.String(), "PULSE_REVIEW_UI_PROBE="+probe, "PULSE_REVIEW_UI_MAPPED="+map[bool]string{false: "0", true: "1"}[mapped])
	raw, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("review browser failed: %v %s", e, raw)
	}
	t.Log(string(raw))
	if len(multiProbe) > 1 && multiProbe[1] {
		var protected, units int
		if e := f.a.DBPool().QueryRow(ctx, "SELECT (SELECT count(*) FROM unit_reservation)+(SELECT count(*) FROM acquisition_reservation)+(SELECT count(*) FROM download_requirement)+(SELECT count(*) FROM download_task)+(SELECT count(*) FROM transfer_plan),(SELECT count(*) FROM media_unit WHERE media_id=(SELECT media_id FROM subscription WHERE id=$1))", f.sub.Id).Scan(&protected, &units); e != nil || protected != 0 || units != len(f.units) {
			t.Fatal("high syntax invented work or canonical units", protected, units, e)
		}
	}
	var reviews, jobs int
	expected := 1
	if len(multiProbe) > 0 && multiProbe[0] || len(multiProbe) > 1 && multiProbe[1] {
		expected = 0
	}
	if e = f.a.DBPool().QueryRow(ctx, "SELECT (SELECT count(*) FROM episode_candidate_review),(SELECT count(*) FROM job WHERE kind IN ('download_submission','episode_download_submission'))").Scan(&reviews, &jobs); e != nil || reviews != expected || jobs != expected || calls.Load() != 1 {
		t.Fatal("duplicate review, fake source, or accidental download", reviews, jobs, calls.Load(), e)
	}
}
