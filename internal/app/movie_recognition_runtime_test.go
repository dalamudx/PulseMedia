package app

import (
	"context"
	"encoding/hex"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"io"
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
)

type recognitionHTTPRoute struct {
	target    *url.URL
	transport http.RoundTripper
}

func (r recognitionHTTPRoute) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	u := *req.URL
	u.Scheme = r.target.Scheme
	u.Host = r.target.Host
	clone.URL = &u
	clone.Host = r.target.Host
	return r.transport.RoundTrip(clone)
}
func TestAppRunOwnsMovieRecognitionAndRestart(t *testing.T) {
	observer, dsn := bootstrapTestPool(t)
	ctx := context.Background()
	data := t.TempDir()
	if err := ensureDefaultLibraryRoots(ctx, observer, idgen.New(), data); err != nil {
		t.Fatal(err)
	}
	q := postgres.Queries(observer)
	root, err := q.GetFirstOnlineRootForType(ctx, "movie")
	if err != nil {
		t.Fatal(err)
	}
	known := "Fight.Club.1999.{tmdb-550}.1080p.BluRay.x264.mkv"
	for _, name := range []string{known, "Unidentified.mkv"} {
		if err = os.WriteFile(filepath.Join(root.Path, name), []byte("native media fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var healthy atomic.Bool
	var calls atomic.Int32
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/movie/550" || r.URL.Query().Get("api_key") != "synthetic-runtime-key" {
			t.Errorf("unexpected metadata route %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		calls.Add(1)
		if !healthy.Load() {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":550,"imdb_id":"tt0137523","title":"搏击俱乐部","original_title":"Fight Club","release_date":"1999-10-15","overview":"独立 HTTP 夹具简介"}`)
	}))
	defer stub.Close()
	target, err := url.Parse(stub.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: recognitionHTTPRoute{target, transport}, Timeout: 5 * time.Second}
	t.Setenv("TMDB_API_KEY", "synthetic-runtime-key")
	start := func() func() {
		pool, e := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
		if e != nil {
			t.Fatal(e)
		}
		cfg := config.NewDefault()
		cfg.DataDir = data
		cfg.Database.URL = dsn
		cfg.SecretKey = hex.EncodeToString(outboxTestKey)
		cfg.Server.HTTPAddr = "127.0.0.1:0"
		a, e := New(cfg, WithDBPool(pool), WithLogger(logging.NewLogger(io.Discard, "error")), WithMetadataHTTPClient(client))
		if e != nil {
			pool.Close()
			t.Fatal(e)
		}
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- a.Run(runCtx) }()
		var once sync.Once
		stop := func() {
			once.Do(func() {
				cancel()
				select {
				case e := <-done:
					if e != nil && !errors.Is(e, context.Canceled) {
						t.Error(e)
					}
				case <-time.After(10 * time.Second):
					t.Error("recognition worker did not drain")
				}
				if e := a.Close(); e != nil {
					t.Error(e)
				}
			})
		}
		t.Cleanup(stop)
		return stop
	}
	stop := start()
	waitNativeScan(t, func() (bool, error) {
		var ready bool
		err := observer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM movie_recognition_work w JOIN job j ON j.id=w.job_id JOIN library_file f ON f.id=w.file_id WHERE f.rel_path=$1 AND w.outcome='pending' AND j.state='queued' AND j.attempts=1) AND EXISTS(SELECT 1 FROM movie_recognition_work w JOIN library_file f ON f.id=w.file_id WHERE f.rel_path='Unidentified.mkv' AND w.outcome='review' AND w.reason='low_confidence')`, known).Scan(&ready)
		return ready, err
	})
	stop()
	var fileID, jobID string
	if err = observer.QueryRow(ctx, `SELECT file_id::text,job_id::text FROM movie_recognition_work w JOIN library_file f ON f.id=w.file_id WHERE f.rel_path=$1`, known).Scan(&fileID, &jobID); err != nil {
		t.Fatal(err)
	}
	var imported int
	if err = observer.QueryRow(ctx, `SELECT count(*) FROM unit_fact WHERE source_file_id=$1::uuid`, fileID).Scan(&imported); err != nil || imported != 0 {
		t.Fatal(imported, err)
	}
	healthy.Store(true)
	// The first process's retry is made due in this fixture; production backoff is unchanged.
	if _, err = observer.Exec(ctx, `UPDATE job SET not_before=clock_timestamp() WHERE id=$1::uuid`, jobID); err != nil {
		t.Fatal(err)
	}
	stop = start()
	waitNativeScan(t, func() (bool, error) {
		var ready bool
		err := observer.QueryRow(ctx, `SELECT outcome='linked' AND j.state='succeeded' AND j.attempts=2 FROM movie_recognition_work w JOIN job j ON j.id=w.job_id WHERE w.job_id=$1::uuid`, jobID).Scan(&ready)
		return ready, err
	})
	stop()
	var outcomes, audits, events, mappings int
	if err = observer.QueryRow(ctx, `SELECT (SELECT count(*) FROM movie_recognition_work WHERE file_id=$1::uuid),(SELECT count(*) FROM audit_log WHERE action='file_identification' AND target_id=$1::uuid::text),(SELECT count(*) FROM outbox_event WHERE aggregate_id=$2 AND event_type='media.file_identification.linked'),(SELECT count(*) FROM library_file_unit WHERE file_id=$1::uuid),(SELECT count(*) FROM unit_fact WHERE source_file_id=$1::uuid AND stage='imported' AND revoked_at IS NULL)`, fileID, jobID).Scan(&outcomes, &audits, &events, &mappings, &imported); err != nil {
		t.Fatal(err)
	}
	if outcomes != 1 || audits != 1 || events != 1 || mappings != 1 || imported != 1 || calls.Load() != 2 {
		t.Fatal(outcomes, audits, events, mappings, imported, calls.Load())
	}
	var factJSON string
	if err = observer.QueryRow(ctx, `SELECT quality_profile::text FROM unit_fact WHERE source_file_id=$1::uuid`, fileID).Scan(&factJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(factJSON, "1080p") {
		t.Fatal("native filename evidence was not retained", factJSON)
	}
}
