package download

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	libraryusecase "github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/google/uuid"
)

type watchFixture struct {
	*ratePublisherFixture
	root, unit uuid.UUID
	local      string
	target     ports.DownloadWatchTarget
	sources    *postgres.DownloadWatchStore
}

func newWatchFixture(t *testing.T) *watchFixture {
	t.Helper()
	f := &watchFixture{ratePublisherFixture: newRatePublisherFixture(t), root: uuid.New(), unit: uuid.New()}
	base := t.TempDir()
	f.local = filepath.Join(base, "downloads%_")
	lib := filepath.Join(base, "library")
	for _, p := range []string{f.local, lib} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(filepath.Join(lib, ".pulsemedia_root"), []byte("pulsemedia_ok"), 0600); e != nil {
		t.Fatal(e)
	}
	ratePublisherExec(t, f.pool, `INSERT INTO library_root(id,path,media_type,health,name) VALUES($1,$2,'movie','online','watch library')`, f.root, lib)
	scanner := libraryusecase.NewScanner(idgen.New(), rootfs.NewFilesystem(), postgres.NewScanObservationStore(f.pool))
	report, e := scanner.ScanRoot(t.Context(), f.root)
	if e != nil || report == nil || report.State != "succeeded" {
		t.Fatalf("production baseline failed: %v", e)
	}
	media := uuid.New()
	ratePublisherExec(t, f.pool, `INSERT INTO media(id,title,media_type,year) VALUES($1,'Watch fixture','movie',2026)`, media)
	ratePublisherExec(t, f.pool, `INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,'movie')`, f.unit, media)
	var unitVersion, mediaVersion, gen int64
	if e = f.pool.QueryRow(t.Context(), `SELECT u.metadata_version,m.metadata_version,r.generation FROM media_unit u JOIN media m ON m.id=u.media_id CROSS JOIN library_root r WHERE u.id=$1 AND r.id=$2`, f.unit, f.root).Scan(&unitVersion, &mediaVersion, &gen); e != nil {
		t.Fatal(e)
	}
	fence, _ := json.Marshal(map[string]any{"candidate_id": f.task.CandidateID, "unit_id": f.unit, "root_id": f.root, "root_generation": gen, "unit_metadata_version": unitVersion, "media_metadata_version": mediaVersion})
	ratePublisherExec(t, f.pool, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id,submission_fence) VALUES($1,$2,$3,$4,'system','watch-fixture',$5)`, uuid.New(), f.task.OperationID, f.unit, f.root, fence)
	mapping, _ := json.Marshal(map[string]string{"/remote": f.local})
	ratePublisherExec(t, f.pool, `UPDATE download_client SET remote_path_map=$2 WHERE id=$1`, f.task.ClientID, mapping)
	ratePublisherExec(t, f.pool, `UPDATE download_task SET save_path='/remote',content_path='/remote/item.mkv' WHERE id=$1`, f.task.ID)
	f.info.SavePath = "/remote"
	f.info.ContentPath = "/remote/item.mkv"
	f.sources = postgres.NewDownloadWatchStore(f.pool)
	f.task, e = db.New(f.pool).GetDownloadTaskByOpID(t.Context(), f.task.OperationID)
	if e != nil {
		t.Fatal(e)
	}
	f.target = f.readTarget(t)
	return f
}
func (f *watchFixture) readTarget(t *testing.T) ports.DownloadWatchTarget {
	t.Helper()
	page, e := f.sources.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{})
	if e != nil {
		t.Fatal(e)
	}
	for _, source := range page.Sources {
		if source.Binding.SourceKind != "path_mapping" {
			continue
		}
		p, e := f.sources.ResolveDownloadWatchTargets(t.Context(), source.Binding, ports.RootChangeHint{RelativePath: "item.mkv", Kind: ports.RootChangeCloseWrite}, ports.DownloadWatchTargetQuery{})
		if e != nil {
			t.Fatal(e)
		}
		if len(p.Targets) == 1 {
			return p.Targets[0]
		}
	}
	t.Fatalf("no bound target, sources=%d issues=%d", len(page.Sources), len(page.Issues))
	return ports.DownloadWatchTarget{}
}
func TestDownloadWatchBoundPositivePG(t *testing.T) {
	f := newWatchFixture(t)
	before := f.protected(t)
	partial, e := f.repo.ObserveDownloadWatchTarget(t.Context(), f.target)
	if e != nil || partial == nil || partial.State != "tracking" || partial.Progress != f.info.Progress {
		t.Fatal("partial observation failed", e)
	}
	if !reflect.DeepEqual(before, f.protected(t)) {
		t.Fatal("progress changed protected graph")
	}
	f.info.Progress = 1
	f.info.DownloadedBytes = 100
	f.info.State = "seeding"
	completed, e := f.repo.ObserveDownloadWatchTarget(t.Context(), f.target)
	if e != nil || completed == nil || completed.State != "completed" || completed.OrganizationState != "queued" {
		t.Fatal("completion failed", e)
	}
	var jobs, files int
	if e = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM job WHERE kind='download_organization' AND payload->>'task_id'=$1`, f.task.ID.String()).Scan(&jobs); e != nil {
		t.Fatal(e)
	}
	if e = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM library_file`).Scan(&files); e != nil {
		t.Fatal(e)
	}
	if jobs != 1 || files != 0 {
		t.Fatal("completion duplicated organization or fabricated imported facts")
	}
	if _, e = f.repo.ObserveDownloadWatchTarget(t.Context(), f.target); !errors.Is(e, ports.ErrDownloadWatchStale) {
		t.Fatal("completed task accepted stale hint", e)
	}
	if f.client.calls.Load() != 2 {
		t.Fatal("stale hint dispatched native read")
	}
}
func TestDownloadWatchBoundLateFencePG(t *testing.T) {
	for _, change := range []string{"client", "generation", "demand", "progress", "cancel"} {
		t.Run(change, func(t *testing.T) {
			f := newWatchFixture(t)
			before := f.taskJSON(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			f.client.get = func(context.Context) (*ports.TorrentInfo, error) { close(entered); <-release; return &f.info, nil }
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, e := f.repo.ObserveDownloadWatchTarget(ctx, f.target); done <- e }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("native not entered")
			}
			switch change {
			case "client":
				ratePublisherExec(t, f.pool, `UPDATE download_client SET username='changed' WHERE id=$1`, f.task.ClientID)
			case "generation":
				ratePublisherExec(t, f.pool, `UPDATE library_root SET generation=generation+1 WHERE id=$1`, f.root)
			case "demand":
				ratePublisherExec(t, f.pool, `UPDATE download_requirement SET submission_fence=jsonb_set(submission_fence,'{unit_metadata_version}','999') WHERE operation_id=$1`, f.task.OperationID)
			case "progress":
				ratePublisherExec(t, f.pool, `UPDATE download_task SET progress=0.5,updated_at=updated_at+interval '1 second' WHERE id=$1`, f.task.ID)
				before = f.taskJSON(t)
			case "cancel":
				cancel()
			}
			graph := f.protected(t)
			unblock()
			select {
			case e := <-done:
				if !errors.Is(e, ports.ErrDownloadWatchStale) && !errors.Is(e, context.Canceled) {
					t.Fatal("late response not fenced", e)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("late call did not return")
			}
			if f.taskJSON(t) != before || !reflect.DeepEqual(graph, f.protected(t)) {
				t.Fatal("late response changed protected state")
			}
			var count int
			if e := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM download_rate_observation`).Scan(&count); e != nil || count != 0 {
				t.Fatal("late response published telemetry")
			}
		})
	}
}
func TestDownloadWatchBoundShortAndConcurrentPG(t *testing.T) {
	f := newWatchFixture(t)
	f.info.Progress = 1
	f.info.State = "seeding"
	f.info.DownloadedBytes = 99
	task, e := f.repo.ObserveDownloadWatchTarget(t.Context(), f.target)
	if e != nil || task.State != "tracking" {
		t.Fatal("one-byte-short promoted", e)
	}
	f.info.DownloadedBytes = 100
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, bound := range []bool{false, true} {
		wg.Go(func() {
			<-start
			var e error
			if bound {
				_, e = f.repo.ObserveDownloadWatchTarget(t.Context(), f.target)
			} else {
				_, e = f.repo.ObserveTask(t.Context(), f.task.OperationID)
			}
			results <- e
		})
	}
	close(start)
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil && !errors.Is(e, ports.ErrDownloadWatchStale) {
			t.Fatal("concurrent periodic/hint failed", e)
		}
	}
	var count int
	if e = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM job WHERE kind='download_organization' AND payload->>'task_id'=$1`, f.task.ID.String()).Scan(&count); e != nil || count != 1 {
		t.Fatal("concurrent completion duplicated job", e)
	}
}
func TestDownloadWatchMappingAndCursorPG(t *testing.T) {
	f := newWatchFixture(t)
	binding := f.target.Binding
	if _, e := f.sources.ResolveDownloadWatchTargets(t.Context(), binding, ports.RootChangeHint{RelativePath: "../escape", Kind: ports.RootChangeMovedTo}, ports.DownloadWatchTargetQuery{}); !errors.Is(e, ports.ErrRejected) {
		t.Fatal("escaping hint admitted")
	}
	for _, raw := range []string{`[]`, `null`, `{"/remote":null}`, `{"/remote":"` + f.local + `","bad":4}`} {
		ratePublisherExec(t, f.pool, `UPDATE download_client SET remote_path_map=$2 WHERE id=$1`, f.task.ClientID, raw)
		page, e := f.sources.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{})
		if e != nil || len(page.Sources) != 0 || len(page.Issues) != 1 || page.Complete {
			t.Fatal("malformed complete mapping not reported", e)
		}
	}
}
