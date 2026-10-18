package app

import (
	"context"
	"encoding/hex"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type recognitionBlockedTransport struct {
	entered, release chan struct{}
	once             sync.Once
	calls            atomic.Int32
}

func (r *recognitionBlockedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if r.calls.Add(1) == 1 {
		r.once.Do(func() { close(r.entered) })
		<-r.release
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":550,"title":"搏击俱乐部","original_title":"Fight Club","imdb_id":"tt0137523","release_date":"1999-10-15","overview":"complete details"}`)), Request: req}, nil
}
func TestAppRunMovieRecognitionDrainsUncancellableMetadataAndRecovers(t *testing.T) {
	observer, dsn := bootstrapTestPool(t)
	ctx := context.Background()
	data := t.TempDir()
	if e := ensureDefaultLibraryRoots(ctx, observer, idgen.New(), data); e != nil {
		t.Fatal(e)
	}
	root, e := postgres.Queries(observer).GetFirstOnlineRootForType(ctx, "movie")
	if e != nil {
		t.Fatal(e)
	}
	name := "Fight.Club.1999.{tmdb-550}.1080p.BluRay.x264.mkv"
	if e = os.WriteFile(filepath.Join(root.Path, name), []byte("native media fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("TMDB_API_KEY", "synthetic-ignored-cancel-key")
	transport := &recognitionBlockedTransport{entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(transport.release) }) }
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	cfg := config.NewDefault()
	cfg.DataDir = data
	cfg.Database.URL = dsn
	cfg.SecretKey = hex.EncodeToString(outboxTestKey)
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	start := func() (*App, context.CancelFunc, <-chan error) {
		pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
		if err != nil {
			t.Fatal(err)
		}
		a, err := New(cfg, WithDBPool(pool), WithMetadataHTTPClient(client), WithLogger(logging.NewLogger(io.Discard, "error")))
		if err != nil {
			pool.Close()
			t.Fatal(err)
		}
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- a.Run(runCtx) }()
		return a, cancel, done
	}
	a, cancel, done := start()
	t.Cleanup(func() {
		release()
		cancel()
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	})
	select {
	case <-transport.entered:
	case <-time.After(20 * time.Second):
		t.Fatal("native metadata operation not reached")
	}
	var jobID, fileID string
	if e = observer.QueryRow(ctx, "SELECT job_id::text,file_id::text FROM movie_recognition_work WHERE root_id=$1", root.ID).Scan(&jobID, &fileID); e != nil {
		t.Fatal(e)
	}
	closing := make(chan error, 1)
	go func() { closing <- a.Close() }()
	select {
	case err := <-closing:
		t.Fatal("closed while physical metadata operation remained", err)
	case <-time.After(100 * time.Millisecond):
	}
	slotCtx, slotCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_, unlock, err := rootfs.NewFilesystem().LockRoots(slotCtx, root.Path)
	slotCancel()
	if err == nil {
		unlock()
		t.Fatal("recognition released root slot before physical completion")
	}
	if probe, err := New(cfg); err == nil {
		if e := probe.Close(); e != nil {
			t.Error(e)
		}
		t.Fatal("shutdown released data lock before physical completion")
	}
	release()
	select {
	case err = <-closing:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("application close did not drain")
	}
	select {
	case err = <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("application run did not drain")
	}
	cancel()
	var state, outcome string
	var facts int
	if e = observer.QueryRow(ctx, "SELECT j.state,w.outcome,(SELECT count(*) FROM unit_fact WHERE source_file_id=w.file_id) FROM movie_recognition_work w JOIN job j ON j.id=w.job_id WHERE w.job_id=$1::uuid", jobID).Scan(&state, &outcome, &facts); e != nil {
		t.Fatal(e)
	}
	if state != "running" || outcome != "pending" || facts != 0 {
		t.Fatal(state, outcome, facts)
	}
	// The cancelled owner's lease expires in the restart fixture; physical I/O is already drained.
	if _, e = observer.Exec(ctx, "UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid", jobID); e != nil {
		t.Fatal(e)
	}
	next, nextCancel, nextDone := start()
	t.Cleanup(func() {
		nextCancel()
		if err := next.Close(); err != nil {
			t.Error(err)
		}
	})
	waitNativeScan(t, func() (bool, error) {
		var ready bool
		err := observer.QueryRow(ctx, "SELECT w.outcome='linked' AND j.state='succeeded' AND j.attempts=2 AND j.lease_token=2 AND w.file_id=$2::uuid FROM movie_recognition_work w JOIN job j ON j.id=w.job_id WHERE w.job_id=$1::uuid", jobID, fileID).Scan(&ready)
		return ready, err
	})
	nextCancel()
	if e = next.Close(); e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-nextDone:
		if e != nil && !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("recovery did not drain")
	}
	if e = observer.QueryRow(ctx, "SELECT count(*) FROM unit_fact WHERE source_file_id=$1::uuid AND stage='imported'", fileID).Scan(&facts); e != nil || facts != 1 || transport.calls.Load() != 2 {
		t.Fatal(facts, transport.calls.Load(), e)
	}
}
