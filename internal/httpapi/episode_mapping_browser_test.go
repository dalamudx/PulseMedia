package httpapi_test

import (
	"context"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestEpisodeMappingBrowserHarness(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit disposable browser fixture required")
	}
	f := setupEpisodeMappingFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	probe := "/fixture-" + uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == probe && r.Method == http.MethodPost {
			if _, e := f.a.DBPool().Exec(ctx, "UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=$1", f.units[0]); e != nil {
				t.Error(e)
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(204)
			return
		}
		f.router.ServeHTTP(w, r)
	}))
	defer server.Close()
	script, e := filepath.Abs("../../web/tests/episode-mapping-browser.mjs")
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.Env = append(os.Environ(), "PULSE_UI_FIXTURE=1", "PULSE_MAPPING_UI_BASE="+server.URL, "PULSE_MAPPING_UI_SUB="+f.sub.Id.String(), "PULSE_MAPPING_UI_PROBE="+probe)
	raw, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("mapping browser failed: %v %s", e, raw)
	}
	t.Log(string(raw))
	sub, e := f.a.Queries().GetSubscriptionByID(ctx, f.sub.Id)
	if e != nil || sub.State != "paused" || sub.EpisodeMappingRevision.Int64 != 2 {
		t.Fatal("browser changed control state or repeated publication", sub, e)
	}
	var revisions, jobs int
	if e = f.a.DBPool().QueryRow(ctx, "SELECT (SELECT count(*) FROM episode_mapping_revision),(SELECT count(*) FROM job WHERE kind='download_submission')").Scan(&revisions, &jobs); e != nil || revisions != 2 || jobs != 0 {
		t.Fatal("preview/read caused new writes or acquisition", revisions, jobs, e)
	}
}
