package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/download"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type organizationFixture struct {
	pool                                  *pgxpool.Pool
	q                                     *db.Queries
	gen                                   ports.IDGen
	queue                                 *jobs.Queue
	mgr                                   *download.Manager
	svc                                   *transfer.Service
	root, unit, media, op, taskID, client uuid.UUID
	source, target, path                  string
	info                                  *ports.TorrentInfo
}

func organizationSetup(t *testing.T) *organizationFixture {
	t.Helper()
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(ctx, dsn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := &organizationFixture{pool: pool, q: db.New(pool), gen: idgen.New(), root: uuid.New(), unit: uuid.New(), media: uuid.New(), op: uuid.New(), taskID: uuid.New(), client: uuid.New(), source: t.TempDir(), target: t.TempDir()}
	f.queue = jobs.NewQueue(pool, f.gen, clock.New())
	f.mgr = download.NewManager(downloadstore.NewRepository(pool, f.gen, make([]byte, 32)))
	f.svc = f.newService()
	f.path = filepath.Join(f.source, "movie.mkv")
	if err := os.WriteFile(f.path, []byte("frozen video bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.target, ".pulsemedia_root"), []byte("root identity marker"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: f.root, Path: f.target, MediaType: "movie", Name: "Movies"}); err != nil {
		t.Fatal(err)
	}
	if _, err := library.NewScanner(f.gen, rootfs.NewFilesystem(), postgres.NewScanObservationStore(pool)).ScanRoot(ctx, f.root); err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.CreateMedia(ctx, db.CreateMediaParams{ID: f.media, Title: "Movie", Year: pgtype.Int4{Int32: 2020, Valid: true}, MediaType: "movie"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: f.unit, MediaID: f.media, UnitKey: "movie"}); err != nil {
		t.Fatal(err)
	}
	indexer, candidate := uuid.New(), uuid.New()
	if _, err := f.q.CreateIndexer(ctx, db.CreateIndexerParams{ID: indexer, Name: "fixture", Url: "https://example.test", ApiKeyEncrypted: []byte("fixture"), Categories: []byte("[]")}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{ID: candidate, IndexerID: indexer, Title: "Movie.2020.1080p", Guid: "fixture", LinkEncrypted: []byte("fixture"), InfoHash: pgtype.Text{String: "aaa", Valid: true}, QualityProfile: []byte(`{"resolution":"1080p"}`), MatchedMediaID: pgtype.UUID{Bytes: f.media, Valid: true}, MatchedUnitID: pgtype.UUID{Bytes: f.unit, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	mapping, _ := json.Marshal(map[string]string{"/remote/downloads": f.source})
	if _, err := f.q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: f.client, Name: "fixture", ClientType: "qbittorrent", Url: "http://fixture", Username: "fixture", PasswordEncrypted: []byte("fixture"), RemotePathMap: mapping}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.CreateDownloadTask(ctx, db.CreateDownloadTaskParams{ID: f.taskID, OperationID: f.op, ClientID: f.client, CandidateID: candidate, InfoHash: "aaa", State: "tracking", SavePath: "/remote/downloads"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.CreateDownloadRequirement(ctx, db.CreateDownloadRequirementParams{ID: uuid.New(), OperationID: f.op, UnitID: f.unit, RootID: f.root, ActorType: "user", ActorID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.CreateUnitReservation(ctx, db.CreateUnitReservationParams{ID: uuid.New(), OperationID: f.op, UnitID: f.unit, RootID: f.root, Purpose: "acquire", CandidateID: candidate, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	f.info = &ports.TorrentInfo{InfoHash: "aaa", SavePath: "/remote/downloads", ContentPath: "/remote/downloads/movie.mkv", Progress: 1, State: "uploading", TotalBytes: 18, DownloadedBytes: 18}
	f.mgr.RegisterClient(f.client, &lostResponseClient{info: f.info})
	return f
}
func (f *organizationFixture) newService() *transfer.Service {
	return transfer.NewService(transferstore.NewRepository(f.pool, f.gen, outbox.New(f.pool, f.gen, clock.New()), rootfs.NewFilesystem()))
}
func (f *organizationFixture) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func (f *organizationFixture) observe(t *testing.T) {
	t.Helper()
	task, err := f.mgr.ObserveTask(context.Background(), f.op)
	if err != nil || task.State != "completed" || task.OrganizationState != "queued" {
		t.Fatalf("not durably queued: %+v %v", task, err)
	}
	if n := f.count(t, "unit_fact"); n != 0 {
		t.Fatal("download completion falsely imported")
	}
}
func (f *organizationFixture) claim(t *testing.T, owner string) ports.JobLease {
	t.Helper()
	job, err := f.queue.Claim(context.Background(), []string{"download_organization"}, owner, 5*time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim: %v %v", job, err)
	}
	return ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken}
}
func (f *organizationFixture) assertImported(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{"unit_fact", "library_file_unit", "transfer_plan"} {
		if n := f.count(t, table); n != 1 {
			t.Fatalf("duplicate/absent %s: %d", table, n)
		}
	}
	task, err := f.q.GetDownloadTaskByOpID(ctx, f.op)
	if err != nil || task.OrganizationState != "completed" {
		t.Fatalf("unfinished task: %+v %v", task, err)
	}
	plan, err := f.q.GetTransferPlanByID(ctx, uuid.UUID(task.TransferPlanID.Bytes))
	if err != nil || plan.State != "completed" {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	steps, err := f.q.ListStepsForPlan(ctx, plan.ID)
	if err != nil || len(steps) != 1 {
		t.Fatal(err)
	}
	source, err := os.Stat(f.path)
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.Stat(steps[0].TargetPath)
	if err != nil || !os.SameFile(source, target) {
		t.Fatal("hardlink postcondition missing", err)
	}
	reqs, err := f.q.ListRequirementsForOperation(ctx, f.op)
	if err != nil || len(reqs) != 1 || reqs[0].State != "imported" {
		t.Fatalf("requirements: %+v %v", reqs, err)
	}
	var reserved int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM unit_reservation WHERE state IN ('reserved','dispatched')").Scan(&reserved); err != nil || reserved != 0 {
		t.Fatalf("import retained completed tuple reservation: %d %v", reserved, err)
	}
	var importedEvents int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM outbox_event WHERE event_type='media.imported'").Scan(&importedEvents); err != nil || importedEvents != 1 {
		t.Fatal("duplicate/absent original imported event", importedEvents, err)
	}
	var object bool
	if err := f.pool.QueryRow(ctx, "SELECT jsonb_typeof(payload)='object' FROM outbox_event WHERE event_type='media.imported'").Scan(&object); err != nil || !object {
		t.Fatal("outbox payload not JSON object", err)
	}
}
func TestCompletionAtomicallyQueuesAndDeduplicates(t *testing.T) {
	f := organizationSetup(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION reject_org_job() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$; CREATE TRIGGER reject_org_job BEFORE INSERT ON job FOR EACH ROW EXECUTE FUNCTION reject_org_job()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.mgr.ObserveTask(ctx, f.op); err == nil {
		t.Fatal("enqueue failure ignored")
	}
	task, err := f.q.GetDownloadTaskByOpID(ctx, f.op)
	if err != nil || task.State != "tracking" || f.count(t, "job") != 0 || f.count(t, "audit_log") != 0 {
		t.Fatalf("split completion transaction: %+v %v", task, err)
	}
	if _, err := f.pool.Exec(ctx, "DROP TRIGGER reject_org_job ON job"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := f.mgr.ObserveTask(ctx, f.op); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.count(t, "job") != 1 || f.count(t, "audit_log") != 1 || f.count(t, "unit_fact") != 0 {
		t.Fatal("completion duplicated or falsely imported")
	}
}
func TestOrganizationProductionWorkerUsesDurableQueue(t *testing.T) {
	f := organizationSetup(t)
	f.observe(t)
	a := &App{dbPool: f.pool, idgen: f.gen, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	done := make(chan struct{})
	go func() { defer close(done); a.runOrganizationWorker(ctx, f.queue, f.svc) }()
	defer func() { cancel(); <-done }()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		task, err := f.q.GetDownloadTaskByOpID(ctx, f.op)
		if err != nil {
			t.Fatal(err)
		}
		if task.OrganizationState == "completed" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("production worker did not organize")
		case <-ticker.C:
		}
	}
	f.assertImported(t)
	var succeeded int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE state='succeeded'").Scan(&succeeded); err != nil || succeeded != 1 {
		t.Fatal("job not atomically settled", err)
	}
}
func TestOrganizationFaultRestartObservesOneFrozenPlan(t *testing.T) {
	for _, failure := range []string{"outbox", "finish"} {
		t.Run(failure, func(t *testing.T) {
			f := organizationSetup(t)
			f.observe(t)
			ctx := context.Background()
			lease := f.claim(t, "old-process")
			body := `CREATE FUNCTION reject_org_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$; CREATE TRIGGER reject_org_write BEFORE INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION reject_org_write()`
			table := "outbox_event"
			if failure == "finish" {
				table = "job"
				body = `CREATE FUNCTION reject_org_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='succeeded' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_org_write BEFORE UPDATE ON job FOR EACH ROW EXECUTE FUNCTION reject_org_write()`
			}
			if _, err := f.pool.Exec(ctx, body); err != nil {
				t.Fatal(err)
			}
			if err := f.svc.OrganizeDownload(ctx, lease); err == nil {
				t.Fatal("injected SQL failure ignored")
			}
			if failure == "outbox" && f.count(t, "unit_fact") != 0 {
				t.Fatal("failed imported transaction leaked fact")
			}
			task, err := f.q.GetDownloadTaskByOpID(ctx, f.op)
			if err != nil || !task.TransferPlanID.Valid || task.OrganizationState != "queued" {
				t.Fatalf("checkpoint lost/fake complete: %+v %v", task, err)
			}
			if _, err := f.pool.Exec(ctx, "DROP TRIGGER reject_org_write ON "+table); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, "UPDATE job SET lease_until=statement_timestamp()-interval '1 second' WHERE id=$1", lease.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, "UPDATE transfer_plan SET lease_until=statement_timestamp()-interval '1 second' WHERE id=$1", uuid.UUID(task.TransferPlanID.Bytes)); err != nil {
				t.Fatal(err)
			}
			if _, err := f.q.RecoverExpiredOrganizationJobs(ctx); err != nil {
				t.Fatal(err)
			}
			restarted := f.newService()
			next := f.claim(t, "new-process")
			if next.Token <= lease.Token {
				t.Fatal("restart did not fence old owner")
			}
			if err := restarted.OrganizeDownload(ctx, next); err != nil {
				t.Fatal(err)
			}
			f.assertImported(t)
		})
	}
}
func TestOrganizationRejectsStaleJobAuthority(t *testing.T) {
	f := organizationSetup(t)
	f.observe(t)
	ctx := context.Background()
	lease := f.claim(t, "owner")
	wrong := lease
	wrong.Owner = "other"
	if err := f.svc.OrganizeDownload(ctx, wrong); !errors.Is(err, transfer.ErrExecutionRejected) {
		t.Fatal(err)
	}
	wrong = lease
	wrong.Token++
	if err := f.svc.OrganizeDownload(ctx, wrong); !errors.Is(err, transfer.ErrExecutionRejected) {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE job SET state='queued' WHERE id=$1", lease.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.OrganizeDownload(ctx, lease); !errors.Is(err, transfer.ErrExecutionRejected) {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE job SET state='running',lease_until=statement_timestamp()-interval '1 second' WHERE id=$1", lease.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.OrganizeDownload(ctx, lease); !errors.Is(err, transfer.ErrExecutionRejected) {
		t.Fatal(err)
	}
	if f.count(t, "transfer_plan") != 0 || f.count(t, "unit_fact") != 0 {
		t.Fatal("stale job wrote business state")
	}
	if err := f.queue.Fail(ctx, lease.ID, lease.Owner, lease.Token, "stale", time.Second); !errors.Is(err, jobs.ErrStaleLease) {
		t.Fatalf("expired failure write accepted: %v", err)
	}
}
func TestOrganizationDirectoryWithoutInventoryKeepsProtection(t *testing.T) {
	f := organizationSetup(t)
	f.info.ContentPath = "/remote/downloads/package"
	if _, e := f.mgr.ObserveTask(context.Background(), f.op); !errors.Is(e, ports.ErrDownloadReconcilePending) {
		t.Fatal("directory without inventory accepted", e)
	}
	task, e := f.q.GetDownloadTaskByOpID(context.Background(), f.op)
	if e != nil || task.State == "completed" || f.count(t, "transfer_plan") != 0 || f.count(t, "unit_fact") != 0 || f.count(t, "job") != 0 {
		t.Fatal("directory fabricated completion", task, e)
	}
	reqs, e := f.q.ListRequirementsForOperation(context.Background(), f.op)
	if e != nil || len(reqs) != 1 || reqs[0].State != "pending" {
		t.Fatal("demand released", reqs, e)
	}
	if _, e = f.q.GetActiveReservation(context.Background(), db.GetActiveReservationParams{UnitID: f.unit, RootID: f.root, Purpose: "acquire"}); e != nil {
		t.Fatal("protection released", e)
	}
}

func TestOrganizationAmbiguityKeepsReservationsForReview(t *testing.T) {
	for _, condition := range []string{"directory", "mapping", "size", "coverage", "symlink", "baseline", "calendar", "text-calendar", "partial-movie", "collection", "numbered-collection", "long-numbered-collection", "numeric-group-year", "french-collection", "rip-tail-year", "season-scope"} {
		t.Run(condition, func(t *testing.T) {
			f := organizationSetup(t)
			ctx := context.Background()
			switch condition {
			case "calendar", "text-calendar", "partial-movie", "collection", "numbered-collection", "long-numbered-collection", "numeric-group-year", "french-collection", "rip-tail-year", "season-scope":
				raw := "Example.Movie.2024.01.02.1080p.BluRay.x264"
				if condition == "season-scope" {
					raw = "Example.Movie.2024.S01.1080p.BluRay.x264"
				}
				if condition == "rip-tail-year" {
					raw = "Example.Movie.DVDRip.2024.1080p.x264"
				}
				if condition == "french-collection" {
					raw = "Example.Movie.2024.INTEGRALE.MULTI.1080p.BluRay.x264"
				}
				if condition == "numeric-group-year" {
					raw = "Example.Movie.1080p.BluRay.x264-1920"
				}
				if condition == "text-calendar" {
					raw = "Example.Movie.3rd.Nov.2024.1080p.BluRay.x264"
				}
				if condition == "collection" {
					raw = "Example.Movie.Complete.Collection.2024.1080p.BluRay.x264"
				}
				if condition == "numbered-collection" {
					raw = "Example.Movie.Quadrilogy.2024.1080p.BluRay.x264"
				}
				if condition == "long-numbered-collection" {
					raw = "Example.Movie.Heptalogy.SWESUB.2024.1080p.BluRay.x264"
				}
				if condition == "partial-movie" {
					raw = "Example.Movie.2024.1080p.BluRay.sample.x264"
				}
				if _, err := f.pool.Exec(ctx, "UPDATE release_candidate SET title=$2 WHERE id=(SELECT candidate_id FROM download_task WHERE operation_id=$1)", f.op, raw); err != nil {
					t.Fatal(err)
				}
			case "directory":
				// Keep a valid completed tracker identity and exercise a physical directory.
				// A suffix-less package without file-list evidence is held before organization.
				if err := os.Remove(f.path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(f.path, 0700); err != nil {
					t.Fatal(err)
				}
			case "mapping":
				if _, err := f.pool.Exec(ctx, "UPDATE download_client SET remote_path_map='{}' WHERE id=$1", f.client); err != nil {
					t.Fatal(err)
				}
			case "size":
				f.info.TotalBytes++
				f.info.DownloadedBytes++
			case "coverage":
				u := uuid.New()
				if _, err := f.q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: u, MediaID: f.media, UnitKey: "other"}); err != nil {
					t.Fatal(err)
				}
				if _, err := f.q.CreateDownloadRequirement(ctx, db.CreateDownloadRequirementParams{ID: uuid.New(), OperationID: f.op, UnitID: u, RootID: f.root, ActorType: "user", ActorID: "owner"}); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(f.path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "outside.mkv"), f.path); err != nil {
					t.Fatal(err)
				}
			case "baseline":
				if _, err := f.pool.Exec(ctx, "UPDATE library_root SET baseline_completed=false WHERE id=$1", f.root); err != nil {
					t.Fatal(err)
				}
			}
			f.observe(t)
			lease := f.claim(t, "owner")
			if err := f.svc.OrganizeDownload(ctx, lease); err != nil {
				t.Fatal(err)
			}
			task, err := f.q.GetDownloadTaskByOpID(ctx, f.op)
			if err != nil || task.OrganizationState != "review_required" {
				t.Fatalf("not reviewed: %+v %v", task, err)
			}
			job, err := f.q.GetJobByID(ctx, lease.ID)
			if err != nil || job.State != "dead" {
				t.Fatalf("review job not actionable: %+v %v", job, err)
			}
			if f.count(t, "unit_fact") != 0 {
				t.Fatal("ambiguous source was imported")
			}
			if _, err := f.q.GetActiveReservation(ctx, db.GetActiveReservationParams{UnitID: f.unit, RootID: f.root, Purpose: "acquire"}); err != nil {
				t.Fatal("review released reservation", err)
			}
		})
	}
}
