package postgres_test

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func capacityFixture(t *testing.T) (observationFixture, ports.ScanSeal) {
	t.Helper()
	f := newObservationFixture(t)
	ctx := t.Context()
	// Capacity tests exercise the current schema, without changing historical helpers.
	var version, server int
	if err := f.pool.QueryRow(ctx, "SELECT max(version_id) FROM goose_db_version WHERE is_applied").Scan(&version); err != nil || version != 92 {
		t.Fatalf("capacity requires schema 92: version=%d err=%v", version, err)
	}
	if err := f.pool.QueryRow(ctx, "SELECT current_setting('server_version_num')::integer").Scan(&server); err != nil || server != 180006 {
		t.Fatalf("capacity requires PG18.6: version=%d err=%v", server, err)
	}
	return f, capacitySeal(t, f)
}

func capacitySeal(t *testing.T, f observationFixture) ports.ScanSeal {
	t.Helper()
	var sampled time.Time
	if err := f.pool.QueryRow(t.Context(), "SELECT clock_timestamp()").Scan(&sampled); err != nil {
		t.Fatal(err)
	}
	in := f.input
	return ports.ScanSeal{RunID: in.RunID, RootID: in.RootID, Path: in.Path, MediaType: in.MediaType, MarkerFilename: in.MarkerFilename,
		Generation: in.Generation, HealthRevision: in.HealthRevision, ScanToken: in.ScanToken, StartMutationSeq: in.StartMutationSeq,
		RootIdentity: ports.FileIdentity{Device: 1, Inode: 99},
		Capacity: &ports.RootCapacity{TotalBytes: 9007199254740993, AvailableBytes: 4007199254740993,
			SampledAt: sampled.Add(789 * time.Nanosecond).In(time.FixedZone("capacity-offset", 9*60*60))}}
}

func capacityRows(t *testing.T, f observationFixture, capacity, finalization int, state string) {
	t.Helper()
	var c, n int
	var got string
	if err := f.pool.QueryRow(t.Context(), `SELECT
	 (SELECT count(*) FROM root_capacity_observation WHERE scan_run_id=$1),
	 (SELECT count(*) FROM scan_finalization WHERE scan_run_id=$1),state FROM scan_run WHERE id=$1`, f.input.RunID).Scan(&c, &n, &got); err != nil {
		t.Fatal(err)
	}
	if c != capacity || n != finalization || got != state {
		t.Fatalf("capacity=%d finalization=%d state=%s; want %d/%d/%s", c, n, got, capacity, finalization, state)
	}
}

func capacitySnapshot(t *testing.T, f observationFixture) string {
	t.Helper()
	var extra string
	if err := f.pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
	 'capacity',(SELECT jsonb_agg(to_jsonb(c) ORDER BY scan_run_id) FROM root_capacity_observation c),
	 'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM job j),
	 'work',(SELECT jsonb_agg(to_jsonb(w) ORDER BY job_id) FROM library_scan_job w),
	 'media',(SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM media m))::text`).Scan(&extra); err != nil {
		t.Fatal(err)
	}
	return f.snapshot(t) + finalizationSnapshot(t, f) + extra
}

func TestScanCapacitySealAndReplay(t *testing.T) {
	for _, optional := range []bool{false, true} {
		t.Run(fmt.Sprintf("nil_%t", optional), func(t *testing.T) {
			f, seal := capacityFixture(t)
			ctx := t.Context()
			if optional {
				seal.Capacity = nil
			}
			if _, err := f.store.Record(ctx, f.input); err != nil {
				t.Fatal(err)
			}
			capacityRows(t, f, 0, 0, "running")
			frozen := f.snapshot(t)
			var mediaBefore, mediaAfter string
			if err := f.pool.QueryRow(ctx, "SELECT jsonb_agg(to_jsonb(m) ORDER BY id)::text FROM media m").Scan(&mediaBefore); err != nil {
				t.Fatal(err)
			}
			if err := f.store.Seal(ctx, seal); err != nil {
				t.Fatal(err)
			}
			want := 1
			if optional {
				want = 0
			}
			capacityRows(t, f, want, 1, "finalizing")
			if frozen != f.snapshot(t) {
				t.Fatal("capacity publication changed frozen root/file/unit/fact/observation rows")
			}
			if err := f.pool.QueryRow(ctx, "SELECT jsonb_agg(to_jsonb(m) ORDER BY id)::text FROM media m").Scan(&mediaAfter); err != nil || mediaAfter != mediaBefore {
				t.Fatalf("capacity publication changed frozen media: %v", err)
			}
			if !optional {
				var exact bool
				if err := f.pool.QueryRow(ctx, `SELECT total_bytes=$2::bigint AND available_bytes=$3::bigint AND sampled_at=$4::timestamptz
				 FROM root_capacity_observation WHERE scan_run_id=$1`, seal.RunID, seal.Capacity.TotalBytes, seal.Capacity.AvailableBytes, seal.Capacity.SampledAt).Scan(&exact); err != nil || !exact {
					t.Fatalf("sample lost precision or timestamp instant: %v", err)
				}
			}
			before := capacitySnapshot(t, f)
			var wg sync.WaitGroup
			errs := make(chan error, 8)
			for i := 0; i < 8; i++ {
				wg.Go(func() { errs <- f.store.Seal(ctx, seal) })
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatalf("original sample replay: %v", err)
				}
			}
			if capacitySnapshot(t, f) != before {
				t.Fatal("concurrent replay rewrote persisted rows")
			}
			for _, field := range []string{"total", "available", "time", "zero_time"} {
				bad := seal
				value := *capacitySeal(t, f).Capacity
				if seal.Capacity != nil {
					value = *seal.Capacity
				}
				switch field {
				case "total":
					value.TotalBytes++
				case "available":
					value.AvailableBytes++
				case "time":
					value.SampledAt = value.SampledAt.Add(time.Microsecond)
				case "zero_time":
					value.SampledAt = time.Time{}
				}
				bad.Capacity = &value
				if err := f.store.Seal(ctx, bad); !errors.Is(err, ports.ErrScanObservationStale) {
					t.Fatalf("tampered %s replay: %v", field, err)
				}
			}
			if capacitySnapshot(t, f) != before {
				t.Fatal("tampered replay changed persisted rows")
			}
			if seal.Capacity != nil {
				utc := seal
				value := *seal.Capacity
				value.SampledAt = value.SampledAt.UTC().Truncate(time.Microsecond)
				utc.Capacity = &value
				if err := f.store.Seal(ctx, utc); err != nil {
					t.Fatalf("PG-equivalent UTC/microsecond replay: %v", err)
				}
			}
			legacy := seal
			legacy.Capacity = nil
			if err := f.store.Seal(ctx, legacy); err != nil {
				t.Fatalf("legacy nil replay: %v", err)
			}
			p, err := f.store.ReconcileBatch(ctx, seal)
			if err != nil || !p.Done {
				t.Fatalf("completion: %+v %v", p, err)
			}
			capacityRows(t, f, want, 1, "succeeded")
			before = capacitySnapshot(t, f)
			if err = f.store.Seal(ctx, seal); err != nil || capacitySnapshot(t, f) != before {
				t.Fatalf("completed original replay: %v", err)
			}
		})
	}
}

func TestScanCapacityInvalidSampleRollsBackSeal(t *testing.T) {
	for _, field := range []string{"zero_total", "negative_total", "negative_available", "available_over_total", "zero_time", "future", "before_run"} {
		t.Run(field, func(t *testing.T) {
			f, seal := capacityFixture(t)
			bad := seal
			value := *seal.Capacity
			switch field {
			case "zero_total":
				value.TotalBytes, value.AvailableBytes = 0, 0
			case "negative_total":
				value.TotalBytes, value.AvailableBytes = -1, 0
			case "negative_available":
				value.AvailableBytes = -1
			case "available_over_total":
				value.AvailableBytes = value.TotalBytes + 1
			case "zero_time":
				value.SampledAt = time.Time{}
			case "future":
				value.SampledAt = value.SampledAt.Add(time.Hour)
			case "before_run":
				if err := f.pool.QueryRow(t.Context(), "SELECT created_at-interval '1 microsecond' FROM scan_run WHERE id=$1", seal.RunID).Scan(&value.SampledAt); err != nil {
					t.Fatal(err)
				}
			}
			bad.Capacity = &value
			before := capacitySnapshot(t, f)
			if err := f.store.Seal(t.Context(), bad); !errors.Is(err, ports.ErrScanObservationStale) {
				t.Fatalf("malformed sample accepted: %v", err)
			}
			capacityRows(t, f, 0, 0, "running")
			if capacitySnapshot(t, f) != before {
				t.Fatal("rejected sample left finalization or other writes")
			}
			if err := f.store.Seal(t.Context(), seal); err != nil {
				t.Fatalf("original sample cannot recover: %v", err)
			}
			capacityRows(t, f, 1, 1, "finalizing")
		})
	}
}

func TestScanCapacityRejectsChangedAuthority(t *testing.T) {
	for _, change := range []string{"path", "media_type", "marker", "generation", "scan_token", "health_revision", "offline", "device", "inode", "owner", "lease_token", "state", "expiry", "work_root", "work_phase", "work_run"} {
		t.Run(change, func(t *testing.T) {
			f, seal := capacityFixture(t)
			ctx := t.Context()
			if change == "device" || change == "inode" {
				// Bind this generation through a genuinely completed scan first.
				original := seal
				original.Capacity = nil
				if err := f.store.Seal(ctx, original); err != nil {
					t.Fatal(err)
				}
				if p, err := f.store.ReconcileBatch(ctx, original); err != nil || !p.Done {
					t.Fatal(p, err)
				}
				run, err := f.q.StartScanRun(ctx, db.StartScanRunParams{ID: f.root, ID_2: uuid.New()})
				if err != nil {
					t.Fatal(err)
				}
				root, err := f.q.GetLibraryRootByID(ctx, f.root)
				if err != nil {
					t.Fatal(err)
				}
				f.input.RunID, f.input.ScanToken, f.input.HealthRevision = run.ID, run.ScanToken, root.HealthRevision
				f.input.StartMutationSeq = run.StartMutationSeq
				seal = capacitySeal(t, f)
				if change == "device" {
					seal.RootIdentity.Device++
				} else {
					seal.RootIdentity.Inode++
				}
			}
			repo, work := claimScanWork(t, f)
			var err error
			switch change {
			case "path":
				_, err = f.pool.Exec(ctx, "UPDATE library_root SET path=path||'-replacement' WHERE id=$1", f.root)
			case "media_type":
				_, err = f.pool.Exec(ctx, "UPDATE library_root SET media_type='movie' WHERE id=$1", f.root)
			case "marker":
				_, err = f.pool.Exec(ctx, "UPDATE library_root SET marker_filename='.replacement' WHERE id=$1", f.root)
			case "generation", "scan_token", "health_revision":
				_, err = f.pool.Exec(ctx, "UPDATE library_root SET "+change+"="+change+"+1 WHERE id=$1", f.root)
			case "offline":
				_, err = f.pool.Exec(ctx, "UPDATE library_root SET health='offline' WHERE id=$1", f.root)
			case "owner":
				_, err = f.pool.Exec(ctx, "UPDATE job SET lease_owner='replacement' WHERE id=$1", work.Lease.ID)
			case "lease_token":
				_, err = f.pool.Exec(ctx, "UPDATE job SET lease_token = 1 + lease_token WHERE id=$1", work.Lease.ID)
			case "state":
				_, err = f.pool.Exec(ctx, "UPDATE job SET state='queued' WHERE id=$1", work.Lease.ID)
			case "expiry":
				_, err = f.pool.Exec(ctx, "UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", work.Lease.ID)
			case "work_root":
				work.RootID = uuid.New()
				repo = postgres.NewLeasedScanObservationStore(f.pool, idgen.New(), work)
			case "work_phase":
				_, err = f.pool.Exec(ctx, "UPDATE library_scan_job SET phase='missing' WHERE job_id=$1", work.Lease.ID)
			case "work_run":
				_, err = f.pool.Exec(ctx, "UPDATE library_scan_job SET run_id=NULL WHERE job_id=$1", work.Lease.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := capacitySnapshot(t, f)
			if err = repo.Seal(ctx, seal); !errors.Is(err, ports.ErrScanObservationStale) {
				t.Fatalf("changed %s accepted: %v", change, err)
			}
			capacityRows(t, f, 0, 0, "running")
			if capacitySnapshot(t, f) != before {
				t.Fatal("stale seal published capacity or mutated business rows")
			}
		})
	}
}

func TestScanCapacityTailAndCommitRollbackRecover(t *testing.T) {
	for _, fault := range []string{"tail", "commit", "tail_expiry"} {
		t.Run(fault, func(t *testing.T) {
			f, seal := capacityFixture(t)
			repo, _ := claimScanWork(t, f)
			ctx := t.Context()
			body := `IF NOT EXISTS(SELECT 1 FROM root_capacity_observation c JOIN scan_finalization f ON f.scan_run_id=c.scan_run_id JOIN scan_run s ON s.id=c.scan_run_id WHERE c.scan_run_id=`
			if fault == "commit" {
				body += `NEW.scan_run_id AND s.state='finalizing') THEN RAISE EXCEPTION 'sample was not staged with seal'; END IF; RAISE EXCEPTION 'capacity commit fault';`
			} else {
				body += `NEW.id AND s.state='finalizing') THEN RAISE EXCEPTION 'sample was not staged with seal'; END IF; `
				if fault == "tail_expiry" {
					body += `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id IN(SELECT job_id FROM library_scan_job WHERE run_id=NEW.id); RETURN NEW;`
				} else {
					body += `RAISE EXCEPTION 'capacity tail fault';`
				}
			}
			if _, err := f.pool.Exec(ctx, `CREATE FUNCTION capacity_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$`); err != nil {
				t.Fatal(err)
			}
			table := "scan_run"
			trigger := `CREATE TRIGGER capacity_fault AFTER UPDATE ON scan_run FOR EACH ROW WHEN(NEW.state='finalizing') EXECUTE FUNCTION capacity_fault()`
			if fault == "commit" {
				table = "root_capacity_observation"
				trigger = `CREATE CONSTRAINT TRIGGER capacity_fault AFTER INSERT ON root_capacity_observation DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION capacity_fault()`
			}
			if _, err := f.pool.Exec(ctx, trigger); err != nil {
				t.Fatal(err)
			}
			before := capacitySnapshot(t, f)
			err := repo.Seal(ctx, seal)
			if fault == "tail_expiry" {
				if !errors.Is(err, ports.ErrScanObservationStale) {
					t.Fatalf("tail lease guard: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "capacity "+fault+" fault") {
				t.Fatalf("expected %s fault after staging capacity: %v", fault, err)
			}
			capacityRows(t, f, 0, 0, "running")
			if capacitySnapshot(t, f) != before {
				t.Fatal("failed seal committed capacity/finalization/run/job changes")
			}
			if _, err = f.pool.Exec(ctx, "DROP TRIGGER capacity_fault ON "+table+"; DROP FUNCTION capacity_fault()"); err != nil {
				t.Fatal(err)
			}
			if err = repo.Seal(ctx, seal); err != nil {
				t.Fatalf("original seal could not recover: %v", err)
			}
			capacityRows(t, f, 1, 1, "finalizing")
		})
	}
}
