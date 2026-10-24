package postgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type hintClock struct{}

func (hintClock) Now() time.Time                  { return time.Now() }
func (hintClock) Since(t time.Time) time.Duration { return time.Since(t) }

type tvHintFixture struct {
	p *pgxpool.Pool
	s *postgres.CompleteTVTrackingRuntimeStore
	r ports.CompleteTVTrackingHintRequest
}

func tvHint(t *testing.T, through ...int64) *tvHintFixture {
	t.Helper()
	p, store, req := publisherCompleteTV(t)
	if _, e := postgres.Migrate(t.Context(), p.Config().ConnConfig.ConnString(), repositoryMigrationsThrough(t, through...)); e != nil {
		t.Fatal(e)
	}
	task, _ := rateTask(t, p, req.OperationID)
	client := &publisherTrackingClient{snapshot: &ports.CompleteTVTrackingSnapshot{InfoHash: task.InfoHash, SavePath: task.SavePath, TotalBytes: 1024, OperationTag: "pulsemedia:op:" + req.OperationID.String()}, progress: 0.5}
	manager := download.NewManager(p, idgen.New(), make([]byte, 32))
	manager.SetClientFactory(func(string, string, string, string) (ports.DownloadClient, error) { return client, nil })
	if out, e := manager.ObserveCompleteTVTracking(t.Context(), store, torrent.Inspector{}, req); e != nil || out.State != "tracking" {
		t.Fatal(out, e)
	}
	execIntent(t, p, `UPDATE job SET state='succeeded',lease_owner=NULL,lease_until=NULL WHERE kind='complete_tv_coverage_download_submission'`)
	var root uuid.UUID
	if e := p.QueryRow(t.Context(), `SELECT root_id FROM complete_tv_execution_authority WHERE operation_id=$1`, req.OperationID).Scan(&root); e != nil {
		t.Fatal(e)
	}
	scan := uuid.New()
	execIntent(t, p, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,finished_at) SELECT $1,id,scan_token,generation,mutation_seq,'succeeded',clock_timestamp() FROM library_root WHERE id=$2`, scan, root)
	execIntent(t, p, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,1,111,true,clock_timestamp() FROM library_root WHERE id=$2`, scan, root)
	raw, e := db.New(p).GetDownloadWatchRootMaterial(t.Context(), db.GetDownloadWatchRootMaterialParams{ClientID: task.ClientID, RootID: root})
	if e != nil {
		t.Fatal(e)
	}
	var material struct {
		ClientVersion time.Time `json:"client_version"`
		ClientStamp   string    `json:"client_stamp"`
		RootPath      string    `json:"root_path"`
		Generation    int64     `json:"generation"`
		Marker        string    `json:"marker"`
	}
	if e = json.Unmarshal(raw, &material); e != nil {
		t.Fatal(e)
	}
	b := ports.DownloadWatchBinding{ClientID: task.ClientID, ClientVersion: material.ClientVersion.UTC(), ClientStamp: material.ClientStamp, RootID: root, Generation: material.Generation, RootPath: material.RootPath, MediaType: "tv", MarkerFilename: material.Marker, BaselineScanID: scan, RootIdentity: ports.FileIdentity{Device: 1, Inode: 111}, SourceKind: "library_identity", LocalRoot: material.RootPath}
	encoded, _ := json.Marshal(b)
	sum := sha256.Sum256(encoded)
	b.Stamp = hex.EncodeToString(sum[:])
	return &tvHintFixture{p: p, s: postgres.NewCompleteTVTrackingRuntimeStore(p, idgen.New()), r: ports.CompleteTVTrackingHintRequest{TaskID: req.TaskID, OperationID: req.OperationID, PriorJobID: req.Lease.ID, HintID: uuid.New(), InstanceID: uuid.New(), Sequence: 1, Hint: ports.RootChangeHint{Kind: ports.RootChangeCloseWrite, RelativePath: "Show/Show.S01E01.mkv"}, Binding: b, SourceIdentity: b.RootIdentity, ReceivedAt: time.Now()}}
}
func (f *tvHintFixture) capture(t *testing.T) ports.CompleteTVTrackingHintCapture {
	t.Helper()
	c, e := f.s.ResolveCompleteTVTrackingHint(t.Context(), f.r, hintClock{})
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func (f *tvHintFixture) counts(t *testing.T) (int, int) {
	t.Helper()
	var jobs, evidence int
	if e := f.p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM job WHERE kind='complete_tv_coverage_tracking' AND id<>$1),(SELECT count(*) FROM complete_tv_tracking_hint)`, f.r.PriorJobID).Scan(&jobs, &evidence); e != nil {
		t.Fatal(e)
	}
	return jobs, evidence
}
func (f *tvHintFixture) unchanged(t *testing.T) string {
	return publisherFrozen(t, f.p, `SELECT jsonb_build_array((SELECT to_jsonb(t) FROM download_task t WHERE id=$1),(SELECT to_jsonb(j) FROM job j WHERE id=$2))::text`, f.r.TaskID, f.r.PriorJobID)
}

func TestCompleteTVTrackingHintEarlyReplayPG(t *testing.T) {
	f := tvHint(t, 101) // This case proves schema101 history cannot be discarded.
	before := f.unchanged(t)
	c := f.capture(t)
	receipt, e := f.s.ScheduleCompleteTVTrackingHint(t.Context(), c)
	if e != nil {
		t.Fatal(e)
	}
	if a, b := f.counts(t); a != 1 || b != 1 {
		t.Fatal(a, b)
	}
	if f.unchanged(t) != before {
		t.Fatal("task or predecessor rewritten")
	}
	var early bool
	if e = f.p.QueryRow(t.Context(), `SELECT h.accepted_at<j.updated_at+interval '30 seconds' FROM complete_tv_tracking_hint h JOIN job j ON j.id=h.prior_job_id`).Scan(&early); e != nil || !early {
		t.Fatal("not early", e)
	}
	evidence, e := f.s.ReadCompleteTVTrackingHint(t.Context(), receipt.JobID)
	if e != nil || evidence.SourceIdentity != f.r.SourceIdentity || evidence.Binding != f.r.Binding {
		t.Fatal(evidence, e)
	}
	// Replay remains original even after this successor has finished and another
	// successful partial result is now latest.
	execIntent(t, f.p, `UPDATE job SET state='succeeded',result=jsonb_build_object('state','tracking') WHERE id=$1`, receipt.JobID)
	again, e := f.s.ScheduleCompleteTVTrackingHint(t.Context(), c)
	if e != nil || *again != *receipt {
		t.Fatal(again, e)
	}
	f.r.ReceivedAt = time.Now().Add(-time.Hour)
	replay := f.capture(t)
	again, e = f.s.ScheduleCompleteTVTrackingHint(t.Context(), replay)
	if e != nil || *again != *receipt {
		t.Fatal(again, e)
	}
	f.r.Hint.RelativePath = "other.mkv"
	if _, e = f.s.ResolveCompleteTVTrackingHint(t.Context(), f.r, hintClock{}); e == nil {
		t.Fatal("mismatched replay accepted")
	}
	if _, e = postgres.Rollback(t.Context(), f.p.Config().ConnConfig.ConnString(), repositoryMigrationsThrough(t, 101)); e == nil {
		t.Fatal("downgrade discarded hint history")
	}
	for _, sql := range []string{`UPDATE complete_tv_tracking_hint SET sequence=2`, `DELETE FROM complete_tv_tracking_hint`, `TRUNCATE complete_tv_tracking_hint`} {
		if _, e = f.p.Exec(t.Context(), sql); e == nil {
			t.Fatal("history changed", sql)
		}
	}
}

func TestCompleteTVTrackingHintConcurrencyPG(t *testing.T) {
	for _, mode := range []string{"same-hint", "different-hint", "periodic"} {
		t.Run(mode, func(t *testing.T) {
			f := tvHint(t)
			if mode == "periodic" {
				execIntent(t, f.p, `UPDATE job SET updated_at=clock_timestamp()-interval '31 seconds' WHERE id=$1`, f.r.PriorJobID)
			}
			c := f.capture(t)
			other := c
			if mode == "different-hint" {
				r := f.r
				r.HintID = uuid.New()
				r.Sequence++
				var e error
				other, e = f.s.ResolveCompleteTVTrackingHint(t.Context(), r, hintClock{})
				if e != nil {
					t.Fatal(e)
				}
			}
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range 8 {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					if mode == "periodic" && i%2 == 1 {
						_ = f.s.ScheduleCompleteTVTracking(context.Background())
						return
					}
					candidate := c
					if i%2 == 1 {
						candidate = other
					}
					_, _ = f.s.ScheduleCompleteTVTrackingHint(context.Background(), candidate)
				}(i)
			}
			close(start)
			wg.Wait()
			a, b := f.counts(t)
			if a != 1 || b > 1 || (mode != "periodic" && b != 1) {
				t.Fatal(a, b)
			}
			if mode == "same-hint" {
				if _, e := f.s.ScheduleCompleteTVTrackingHint(t.Context(), c); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "different-hint" {
				_, _ = f.s.ScheduleCompleteTVTrackingHint(t.Context(), other)
				if a, b = f.counts(t); a != 1 || b != 1 {
					t.Fatal(a, b)
				}
			}
		})
	}
}

func TestCompleteTVTrackingHintRejectPG(t *testing.T) {
	for _, kind := range []string{"no-prior", "failed", "exhausted", "completed", "review", "active", "permit", "rescan", "old", "future", "generation", "baseline", "mapping", "binding", "protection", "isolation"} {
		t.Run(kind, func(t *testing.T) {
			f := tvHint(t)
			switch kind {
			case "no-prior":
				f.r.PriorJobID = uuid.New()
			case "failed":
				execIntent(t, f.p, `UPDATE job SET state='dead' WHERE id=$1`, f.r.PriorJobID)
			case "exhausted":
				execIntent(t, f.p, `UPDATE job SET state='dead',error='complete TV tracking lease expired' WHERE id=$1`, f.r.PriorJobID)
			case "completed":
				execIntent(t, f.p, `UPDATE job SET result=jsonb_set(result,'{state}','"completed"') WHERE id=$1`, f.r.PriorJobID)
			case "review":
				execIntent(t, f.p, `UPDATE job SET result=jsonb_set(result,'{review_required}','true') WHERE id=$1`, f.r.PriorJobID)
			case "active":
				execIntent(t, f.p, `INSERT INTO job(id,kind,state,payload) VALUES($1,'complete_tv_coverage_observation','queued',jsonb_build_object('operation_id',$2::text))`, uuid.New(), f.r.OperationID)
			case "permit":
				execIntent(t, f.p, `INSERT INTO complete_tv_physical_permit(operation_id,token,backend_pid,advisory_key) VALUES($1,$2,pg_backend_pid(),123)`, f.r.OperationID, uuid.New())
			case "rescan":
				f.r.Hint.Kind = ports.RootChangeRescan
			case "old":
				f.r.ReceivedAt = time.Now().Add(-6 * time.Second)
			case "future":
				f.r.ReceivedAt = time.Now().Add(time.Minute)
			case "generation":
				execIntent(t, f.p, `UPDATE library_root SET generation=generation+1 WHERE id=$1`, f.r.Binding.RootID)
			case "baseline":
				execIntent(t, f.p, `UPDATE scan_finalization SET root_inode=222 WHERE scan_run_id=$1`, f.r.Binding.BaselineScanID)
			case "mapping":
				execIntent(t, f.p, `UPDATE download_client SET remote_path_map='{"/":"relative"}' WHERE id=$1`, f.r.Binding.ClientID)
			case "binding":
				f.r.Binding.LocalRoot = "/not-authorized"
			case "protection":
				execIntent(t, f.p, `UPDATE unit_reservation SET state='released' WHERE operation_id=$1`, f.r.OperationID)
			case "isolation":
				execIntent(t, f.p, `UPDATE restore_isolation SET isolated=true,restore_id=gen_random_uuid() WHERE singleton`)
			}
			if _, e := f.s.ResolveCompleteTVTrackingHint(t.Context(), f.r, hintClock{}); e == nil {
				t.Fatal("accepted", kind)
			}
			if a, b := f.counts(t); (a != 0 && kind != "no-prior") || b != 0 {
				t.Fatal(a, b)
			}
		})
	}
}

func TestCompleteTVTrackingHintDeferredRollbackPG(t *testing.T) {
	for _, kind := range []string{"task", "prior", "root", "audit", "evidence", "job"} {
		t.Run(kind, func(t *testing.T) {
			f := tvHint(t)
			c := f.capture(t)
			before := f.unchanged(t)
			action := map[string]string{
				"task":     `UPDATE download_task SET progress=0.9 WHERE id=NEW.task_id;`,
				"prior":    `UPDATE job SET result='{}' WHERE id=NEW.prior_job_id;`,
				"root":     `UPDATE library_root SET generation=generation+1 WHERE id=(SELECT root_id FROM complete_tv_execution_authority WHERE operation_id=NEW.operation_id);`,
				"audit":    `DELETE FROM audit_log WHERE action='complete_tv.tracking_hint';`,
				"evidence": `UPDATE complete_tv_tracking_hint SET sequence=999 WHERE job_id=NEW.job_id;`,
				"job":      `UPDATE job SET max_attempts=99 WHERE id=NEW.job_id;`,
			}[kind]
			execIntent(t, f.p, `CREATE FUNCTION hint_tail_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+action+` RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER hint_tail_test AFTER INSERT ON complete_tv_tracking_hint DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION hint_tail_test()`)
			if _, e := f.s.ScheduleCompleteTVTrackingHint(t.Context(), c); e == nil {
				t.Fatal("tail mutation accepted")
			}
			if a, b := f.counts(t); a != 0 || b != 0 {
				t.Fatal(a, b)
			}
			if f.unchanged(t) != before {
				t.Fatal("rollback changed history")
			}
		})
	}
}

func TestCompleteTVTrackingHintLeaseBindingPG(t *testing.T) {
	f := tvHint(t)
	receipt, e := f.s.ScheduleCompleteTVTrackingHint(t.Context(), f.capture(t))
	if e != nil {
		t.Fatal(e)
	}
	lease, e := db.New(f.p).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{ports.CompleteTVTrackingJobKind}, Column2: "hint-test", Column3: 60000})
	if e != nil || lease.ID != receipt.JobID {
		t.Fatal(e)
	}
	r := ports.CompleteTVTrackingRequest{TaskID: f.r.TaskID, OperationID: f.r.OperationID, Lease: ports.JobLease{ID: lease.ID, Owner: "hint-test", Token: lease.LeaseToken}}
	validate := func() (string, error) {
		tx, e := f.p.Begin(t.Context())
		if e != nil {
			return "", e
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if e = postgres.LockCompleteTVTrackingHintRoot(t.Context(), tx, r); e != nil {
			return "", e
		}
		return postgres.ValidateCompleteTVTrackingHint(t.Context(), tx, r)
	}
	evidence, e := f.s.ReadCompleteTVTrackingHint(t.Context(), receipt.JobID)
	if e != nil {
		t.Fatal(e)
	}
	if digest, e := validate(); e != nil || len(digest) != 64 || digest != evidence.Digest {
		t.Fatal(digest, e)
	}
	tx, e := f.p.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(t.Context(), `UPDATE job SET state='succeeded',lease_owner=NULL,lease_until=NULL WHERE id=$1`, receipt.JobID); e != nil {
		t.Fatal(e)
	}
	if digest, e := postgres.ValidateCompleteTVTrackingHintEvidence(t.Context(), tx, r); e != nil || digest != evidence.Digest {
		t.Fatal(digest, e)
	}
	if e = tx.Rollback(t.Context()); e != nil {
		t.Fatal(e)
	}
	r.Lease.Token++
	if _, e = validate(); !errors.Is(e, ports.ErrCompleteTVObservationStale) {
		t.Fatal(e)
	}
	r.Lease.Token--
	execIntent(t, f.p, `UPDATE library_root SET health='offline' WHERE id=$1`, f.r.Binding.RootID)
	if _, e = validate(); e == nil {
		t.Fatal("stale binding accepted")
	}
}

func TestCompleteTVTrackingHintMigrationPG(t *testing.T) {
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if v, e := postgres.Migrate(t.Context(), dsn, repositoryMigrationsThrough(t, 100)); e != nil || v != 100 {
		t.Fatal(v, e)
	}
	if v, e := postgres.Migrate(t.Context(), dsn, repositoryMigrationsThrough(t, 101)); e != nil || v != 101 {
		t.Fatal(v, e)
	}
	// The same goose migration runner and its guarded Down are exercised below.
	if _, e := postgres.Rollback(t.Context(), dsn, repositoryMigrationsThrough(t, 101)); e != nil {
		t.Fatal(e)
	}
	if v, e := postgres.Migrate(t.Context(), dsn, repositoryMigrationsThrough(t, 100)); e != nil || v != 100 {
		t.Fatal(v, e)
	}
}

// A delayed schedule must use the captured predecessor and configuration, even
// if a fresh resolution could now observe a different valid state.
func TestCompleteTVTrackingHintCaptureFencePG(t *testing.T) {
	for _, kind := range []string{"mapping", "generation", "baseline", "protection", "new-prior", "expiry"} {
		t.Run(kind, func(t *testing.T) {
			f := tvHint(t)
			var c ports.CompleteTVTrackingHintCapture
			if kind == "expiry" {
				var e error
				c, e = f.s.ResolveCompleteTVTrackingHint(t.Context(), f.r, hintAgeClock{})
				if e != nil {
					t.Fatal(e)
				}
				execIntent(t, f.p, `SELECT pg_sleep(0.03)`)
			} else {
				c = f.capture(t)
			}
			switch kind {
			case "mapping":
				execIntent(t, f.p, `UPDATE download_client SET remote_path_map='{"/remote":"/local"}' WHERE id=$1`, f.r.Binding.ClientID)
			case "generation":
				execIntent(t, f.p, `UPDATE library_root SET generation=generation+1 WHERE id=$1`, f.r.Binding.RootID)
			case "baseline":
				execIntent(t, f.p, `UPDATE scan_finalization SET expected_marker_filename='changed' WHERE scan_run_id=$1`, f.r.Binding.BaselineScanID)
			case "protection":
				execIntent(t, f.p, `UPDATE acquisition_reservation SET released_at=clock_timestamp() WHERE operation_id=$1`, f.r.OperationID)
			case "new-prior":
				execIntent(t, f.p, `INSERT INTO job(id,kind,state,payload,result) SELECT $1,kind,state,payload,result FROM job WHERE id=$2`, uuid.New(), f.r.PriorJobID)
			}
			if _, e := f.s.ScheduleCompleteTVTrackingHint(t.Context(), c); e == nil {
				t.Fatal("changed capture accepted")
			}
			if jobs, evidence := f.counts(t); evidence != 0 || (jobs != 0 && kind != "new-prior") {
				t.Fatal(jobs, evidence)
			}
		})
	}
}

type hintAgeClock struct{}

func (hintAgeClock) Now() time.Time                { return time.Now() }
func (hintAgeClock) Since(time.Time) time.Duration { return 4990 * time.Millisecond }

func TestCompleteTVTrackingHintInsertRewritePG(t *testing.T) {
	for _, kind := range []string{"sequence", "source", "authority"} {
		t.Run(kind, func(t *testing.T) {
			f := tvHint(t)
			c := f.capture(t)
			action := map[string]string{"sequence": "NEW.sequence=99;", "source": "NEW.source_inode=222;", "authority": "NEW.authority_digest=repeat('a',64);"}[kind]
			execIntent(t, f.p, `CREATE FUNCTION hint_insert_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+action+` RETURN NEW; END $$; CREATE TRIGGER hint_insert_test BEFORE INSERT ON complete_tv_tracking_hint FOR EACH ROW EXECUTE FUNCTION hint_insert_test()`)
			if _, e := f.s.ScheduleCompleteTVTrackingHint(t.Context(), c); e == nil {
				t.Fatal("insert rewrite accepted")
			}
			if a, b := f.counts(t); a != 0 || b != 0 {
				t.Fatal(a, b)
			}
		})
	}
}

func TestCompleteTVTrackingHintRunningReaderPG(t *testing.T) {
	f := tvHint(t)
	store := postgres.NewCompleteTVTrackingStore(f.p, idgen.New())
	var reader ports.CompleteTVTrackingHintReader = store
	// The optional projection adds no physical-permit or root requirement to a
	// periodic job; existing Load remains its authority check.
	if out, e := reader.ReadCompleteTVTrackingHintForLease(t.Context(), ports.JobLease{ID: f.r.PriorJobID, Owner: "periodic", Token: 1}); e != nil || out != nil {
		t.Fatal(out, e)
	}
	receipt, e := f.s.ScheduleCompleteTVTrackingHint(t.Context(), f.capture(t))
	if e != nil {
		t.Fatal(e)
	}
	job, e := db.New(f.p).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{ports.CompleteTVTrackingJobKind}, Column2: "hint-reader", Column3: 60000})
	if e != nil || job.ID != receipt.JobID {
		t.Fatal(e)
	}
	lease := ports.JobLease{ID: job.ID, Owner: "hint-reader", Token: job.LeaseToken}
	if out, e := reader.ReadCompleteTVTrackingHintForLease(t.Context(), lease); e == nil || out != nil {
		t.Fatal("missing physical permit accepted", out, e)
	}
	conn, e := f.p.Acquire(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(cleanup, `DELETE FROM complete_tv_physical_permit WHERE operation_id=$1`, f.r.OperationID); err != nil {
			t.Error(err)
		}
		if _, err := conn.Exec(cleanup, `SELECT pg_advisory_unlock(123::bigint)`); err != nil {
			t.Error(err)
		}
		conn.Release()
	}()
	if _, e = conn.Exec(t.Context(), `SELECT pg_advisory_lock(123::bigint)`); e != nil {
		t.Fatal(e)
	}
	if _, e = conn.Exec(t.Context(), `INSERT INTO complete_tv_physical_permit(operation_id,token,backend_pid,advisory_key) VALUES($1,$2,pg_backend_pid(),123)`, f.r.OperationID, uuid.New()); e != nil {
		t.Fatal(e)
	}
	expected, e := f.s.ReadCompleteTVTrackingHint(t.Context(), job.ID)
	if e != nil {
		t.Fatal(e)
	}
	for range 2 {
		out, e := reader.ReadCompleteTVTrackingHintForLease(t.Context(), lease)
		if e != nil || out == nil || *out != *expected {
			t.Fatal(out, e)
		}
	}
	// A fresh allocation of the optional result cannot affect snapshot equality.
	r := ports.CompleteTVTrackingRequest{Lease: lease, TaskID: f.r.TaskID, OperationID: f.r.OperationID}
	first, e := store.LoadCompleteTVTracking(t.Context(), r)
	if e != nil {
		t.Fatal(e)
	}
	second, e := store.LoadCompleteTVTracking(t.Context(), r)
	if e != nil || *first != *second {
		t.Fatal("snapshot comparison changed", e)
	}
	bad := lease
	bad.Token++
	if out, e := reader.ReadCompleteTVTrackingHintForLease(t.Context(), bad); e == nil || out != nil {
		t.Fatal("wrong lease accepted", out, e)
	}
	execIntent(t, f.p, `UPDATE library_root SET health='offline' WHERE id=$1`, f.r.Binding.RootID)
	if out, e := reader.ReadCompleteTVTrackingHintForLease(t.Context(), lease); e == nil || out != nil {
		t.Fatal("stale root accepted", out, e)
	}
}
