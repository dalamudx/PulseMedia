package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"sync"
	"testing"
	"time"
)

type observationFixture struct {
	factSourceFixture
	input ports.ScanObservation
	store *postgres.ScanObservationStore
}

func newObservationFixture(t *testing.T) observationFixture {
	t.Helper()
	f := newFactSourceFixture(t)
	ctx := context.Background()
	for i, unit := range []uuid.UUID{f.first, f.second} {
		if err := f.q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: f.file, UnitID: unit}); err != nil {
			t.Fatal(err)
		}
		stage := "imported"
		if i == 1 {
			stage = "confirmed"
		}
		if _, err := f.q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: unit, Stage: stage, SourceFileID: pgtype.UUID{Bytes: f.file, Valid: true}, RootID: pgtype.UUID{Bytes: f.root, Valid: true}, QualityProfile: []byte(`{"sentinel":"preserve original quality"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	run, err := f.q.StartScanRun(ctx, db.StartScanRunParams{ID: f.root, ID_2: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	root, err := f.q.GetLibraryRootByID(ctx, f.root)
	if err != nil {
		t.Fatal(err)
	}
	file, err := f.q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: f.root, RelPath: "S01E01.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	return observationFixture{factSourceFixture: f, store: postgres.NewScanObservationStore(f.pool), input: ports.ScanObservation{RunID: run.ID, RootID: f.root, ObservationID: uuid.New(), NewFileID: uuid.New(), Path: root.Path, MediaType: root.MediaType, MarkerFilename: root.MarkerFilename, Generation: root.Generation, HealthRevision: root.HealthRevision, ScanToken: run.ScanToken, StartMutationSeq: run.StartMutationSeq, RelPath: file.RelPath, Identity: ports.FileIdentity{Size: file.SizeBytes + 100, Mtime: file.MtimeNs + 100, Inode: 1234}}}
}
func (f observationFixture) snapshot(t *testing.T) string {
	t.Helper()
	var value string
	err := f.pool.QueryRow(context.Background(), `SELECT jsonb_build_object('root',(SELECT to_jsonb(r) FROM library_root r WHERE id=$1),'files',(SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM library_file f WHERE root_id=$1),'mappings',(SELECT jsonb_agg(to_jsonb(m) ORDER BY file_id,unit_id) FROM library_file_unit m WHERE file_id IN(SELECT id FROM library_file WHERE root_id=$1)),'facts',(SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM unit_fact f WHERE root_id=$1),'units',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM media_unit u WHERE id IN($2,$3)),'observations',(SELECT jsonb_agg(to_jsonb(o) ORDER BY id) FROM scan_observation o WHERE root_id=$1))::text`, f.root, f.first, f.second).Scan(&value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func TestScanObservationReplacementFaultRollsBackAndRecovers(t *testing.T) {
	for _, table := range []string{"library_file", "scan_observation"} {
		t.Run(table, func(t *testing.T) {
			f := newObservationFixture(t)
			ctx := context.Background()
			before := f.snapshot(t)
			if _, err := f.pool.Exec(ctx, `CREATE FUNCTION reject_observation_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic observation fault'; END $$`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, fmt.Sprintf("CREATE TRIGGER reject_observation_write BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION reject_observation_write()", table)); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.Record(ctx, f.input); err == nil {
				t.Fatal("fault was accepted")
			}
			if after := f.snapshot(t); after != before {
				t.Fatal("fault left partial source revocation/file replacement/observation")
			}
			if _, err := f.pool.Exec(ctx, "DROP TRIGGER reject_observation_write ON "+table); err != nil {
				t.Fatal(err)
			}
			got, err := f.store.Record(ctx, f.input)
			if err != nil || !got.Updated || !got.Recorded {
				t.Fatalf("recovery failed: %+v %v", got, err)
			}
		})
	}
}
func TestScanObservationEightWayReplayAndConflictingReplay(t *testing.T) {
	f := newObservationFixture(t)
	ctx := context.Background()
	start := make(chan struct{})
	results := make(chan ports.ScanObservationResult, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			in := f.input
			in.ObservationID = uuid.New()
			in.NewFileID = uuid.New()
			got, err := f.store.Record(ctx, in)
			results <- got
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var recorded, updated int
	for got := range results {
		if got.Recorded {
			recorded++
		}
		if got.Updated {
			updated++
		}
	}
	var files, observations int
	if err := f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM library_file WHERE root_id=$1),(SELECT count(*) FROM scan_observation WHERE scan_run_id=$2)", f.root, f.input.RunID).Scan(&files, &observations); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 || updated != 1 || files != 2 || observations != 1 {
		t.Fatalf("replay counts recorded=%d updated=%d files=%d observations=%d", recorded, updated, files, observations)
	}
	before := f.snapshot(t)
	conflict := f.input
	conflict.Identity.Inode++
	if _, err := f.store.Record(ctx, conflict); !errors.Is(err, ports.ErrScanObservationStale) {
		t.Fatalf("conflicting replay accepted: %v", err)
	}
	if f.snapshot(t) != before {
		t.Fatal("conflicting replay mutated state")
	}
}
func TestScanObservationProtectsPostWatermarkAndLegacyEvidence(t *testing.T) {
	for _, kind := range []string{"post_watermark", "unknown_fingerprint", "opaque_fingerprint"} {
		t.Run(kind, func(t *testing.T) {
			f := newObservationFixture(t)
			ctx := context.Background()
			if kind == "post_watermark" {
				seq, err := f.q.IncrementRootMutationSeq(ctx, f.root)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.pool.Exec(ctx, "UPDATE library_file SET last_mutation_seq=$2 WHERE id=$1", f.file, seq); err != nil {
					t.Fatal(err)
				}
			} else {
				file, err := f.q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: f.root, RelPath: "S01E01.mkv"})
				if err != nil {
					t.Fatal(err)
				}
				f.input.Identity.Size = file.SizeBytes
				f.input.Identity.Mtime = file.MtimeNs
				if kind == "opaque_fingerprint" {
					if _, err = f.pool.Exec(ctx, "UPDATE library_file SET file_fingerprint='legacy-untyped-fingerprint' WHERE id=$1", f.file); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := f.snapshot(t)
			got, err := f.store.Record(ctx, f.input)
			if err != nil || got.Added || got.Updated || !got.Recorded {
				t.Fatalf("protected evidence changed: %+v %v", got, err)
			}
			// Remove only this fixture observation to compare every other persisted row.
			if _, err = f.pool.Exec(ctx, "DELETE FROM scan_observation WHERE id=$1", f.input.ObservationID); err != nil {
				t.Fatal(err)
			}
			if after := f.snapshot(t); after != before {
				t.Fatal("protected file/history/unknown fingerprint was rewritten")
			}
		})
	}
}
func TestScanObservationChecksAuthorityAfterRootWait(t *testing.T) {
	f := newObservationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	blocker, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err = blocker.Exec(ctx, "UPDATE library_root SET generation=generation+1 WHERE id=$1", f.root); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, e := f.store.Record(ctx, f.input); done <- e }()
	for {
		var waiting bool
		if err = f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%LockAcquisitionRoot%')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case e := <-done:
			t.Fatalf("record did not wait: %v", e)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot(t)
	select {
	case e := <-done:
		if !errors.Is(e, ports.ErrScanObservationStale) {
			t.Fatalf("stale owner accepted after wait: %v", e)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if f.snapshot(t) != before {
		t.Fatal("stale snapshot mutated file/facts/observations")
	}
}

func TestScanObservationRejectsMappingChangedWhileWaitingForUnit(t *testing.T) {
	f := newObservationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	blocker, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err = blocker.Exec(ctx, "SELECT id FROM media_unit WHERE id=$1 FOR UPDATE", f.first); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, e := f.store.Record(ctx, f.input); done <- e }()
	for {
		var waiting bool
		if err = f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%LockAcquisitionUnit%')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case e := <-done:
			t.Fatalf("record did not wait for unit: %v", e)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err = blocker.Exec(ctx, "UPDATE library_file SET mapping_revision=mapping_revision+1 WHERE id=$1", f.file); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot(t)
	select {
	case e := <-done:
		if !errors.Is(e, ports.ErrScanObservationStale) {
			t.Fatalf("changed mapping accepted: %v", e)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if f.snapshot(t) != before {
		t.Fatal("stale mapping caused file replacement or source revocation")
	}
}
