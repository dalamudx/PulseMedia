package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"sync"
	"testing"
)

func missingSeal(f observationFixture) ports.ScanSeal {
	in := f.input
	return ports.ScanSeal{RunID: in.RunID, RootID: in.RootID, Path: in.Path, MediaType: in.MediaType, MarkerFilename: in.MarkerFilename, Generation: in.Generation, HealthRevision: in.HealthRevision, ScanToken: in.ScanToken, StartMutationSeq: in.StartMutationSeq, RootIdentity: ports.FileIdentity{Device: 1, Inode: 99}}
}
func nextMissingSeal(t *testing.T, f observationFixture) ports.ScanSeal {
	t.Helper()
	ctx := context.Background()
	root, err := f.store.LoadRoot(ctx, f.root)
	if err != nil {
		t.Fatal(err)
	}
	runID := uuid.New()
	run, err := f.store.Start(ctx, root, runID)
	if err != nil {
		t.Fatal(err)
	}
	return ports.ScanSeal{RunID: runID, RootID: root.ID, Path: root.Path, MediaType: root.MediaType, MarkerFilename: root.MarkerFilename, Generation: root.Generation, HealthRevision: root.HealthRevision, ScanToken: run.ScanToken, StartMutationSeq: run.StartMutationSeq, RootIdentity: ports.FileIdentity{Device: 1, Inode: 99}}
}
func finishMissing(t *testing.T, f observationFixture, seal ports.ScanSeal) ports.ScanFinalizationProgress {
	t.Helper()
	ctx := context.Background()
	if err := f.store.Seal(ctx, seal); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		p, err := f.store.ReconcileBatch(ctx, seal)
		if err != nil {
			t.Fatal(err)
		}
		if p.Done {
			return p
		}
	}
	t.Fatal("fixture cursor did not finish")
	return ports.ScanFinalizationProgress{}
}
func missingProtectedSnapshot(t *testing.T, f observationFixture) string {
	t.Helper()
	var result string
	err := f.pool.QueryRow(context.Background(), `SELECT jsonb_build_object(
 'files',(SELECT jsonb_agg(to_jsonb(file) ORDER BY id) FROM library_file file WHERE root_id=$1),
 'mappings',(SELECT jsonb_agg(to_jsonb(m) ORDER BY file_id,unit_id) FROM library_file_unit m WHERE file_id IN(SELECT id FROM library_file WHERE root_id=$1)),
 'facts',(SELECT jsonb_agg(to_jsonb(fact) ORDER BY id) FROM unit_fact fact WHERE root_id=$1))::text`, f.root).Scan(&result)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func assertMissingCycle(t *testing.T, f observationFixture, first, last uuid.UUID, count int) {
	t.Helper()
	ctx := context.Background()
	var gotFirst, gotLast uuid.UUID
	var rows int
	if err := f.pool.QueryRow(ctx, "SELECT first_run_id,last_run_id FROM missing_candidate WHERE file_id=$1 AND state='open'", f.file).Scan(&gotFirst, &gotLast); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM missing_candidate WHERE file_id=$1", f.file).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if gotFirst != first || gotLast != last || rows != count {
		t.Fatalf("candidate cycle first=%s last=%s rows=%d", gotFirst, gotLast, rows)
	}
}
func TestMissingCandidatePreservesFactsAndRequiresSeparateSpacedScan(t *testing.T) {
	f := newObservationFixture(t)
	ctx := context.Background()
	seal := missingSeal(f)
	before := missingProtectedSnapshot(t, f)
	p := finishMissing(t, f, seal)
	if p.Missing != 1 || p.MissingReviewRequired {
		t.Fatal(p)
	}
	assertMissingCycle(t, f, seal.RunID, seal.RunID, 1)
	qualified, err := f.q.ListQualifiedScanMissingCandidates(ctx, db.ListQualifiedScanMissingCandidatesParams{RootID: f.root, RunID: seal.RunID})
	if err != nil || len(qualified) != 0 {
		t.Fatalf("one run qualified: %d %v", len(qualified), err)
	}
	second := nextMissingSeal(t, f)
	finishMissing(t, f, second)
	assertMissingCycle(t, f, seal.RunID, second.RunID, 1)
	qualified, err = f.q.ListQualifiedScanMissingCandidates(ctx, db.ListQualifiedScanMissingCandidatesParams{RootID: f.root, RunID: second.RunID})
	if err != nil || len(qualified) != 0 {
		t.Fatalf("short interval qualified: %d %v", len(qualified), err)
	}
	// Synthetic fixture time, without sleeping or relaxing the five-minute rule.
	if _, err = f.pool.Exec(ctx, "UPDATE scan_run SET finished_at=finished_at-interval '10 minutes' WHERE id=$1", seal.RunID); err != nil {
		t.Fatal(err)
	}
	qualified, err = f.q.ListQualifiedScanMissingCandidates(ctx, db.ListQualifiedScanMissingCandidatesParams{RootID: f.root, RunID: second.RunID})
	if err != nil || len(qualified) != 1 || qualified[0].FileID != f.file {
		t.Fatalf("spaced complete sources unavailable: %v %v", qualified, err)
	}
	stable := finalizationSnapshot(t, f)
	for range 3 {
		if _, err = f.store.ReconcileBatch(ctx, second); err != nil {
			t.Fatal(err)
		}
	}
	if after := finalizationSnapshot(t, f); after != stable {
		t.Fatal("replayed completion rewrote candidate")
	}
	if after := missingProtectedSnapshot(t, f); after != before {
		t.Fatal("candidate phase changed file, mapping or facts")
	}
	for v := int64(postgres.CurrentMigrationVersion); v > 49; {
		var err error
		v, err = postgres.Rollback(ctx, f.dsn, migrationsDir)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = postgres.Rollback(ctx, f.dsn, migrationsDir); err == nil {
		t.Fatal("downgrade discarded candidate history")
	}
	if err = postgres.CheckSchemaVersion(ctx, f.pool, 49); err != nil {
		t.Fatal(err)
	}
	if _, err = f.q.UpdateRootHealth(ctx, db.UpdateRootHealthParams{ID: f.root, Health: "offline"}); err != nil {
		t.Fatal(err)
	}
	qualified, err = f.q.ListQualifiedScanMissingCandidates(ctx, db.ListQualifiedScanMissingCandidatesParams{RootID: f.root, RunID: second.RunID})
	if err != nil || len(qualified) != 0 {
		t.Fatal("offline root qualified", err)
	}
}
func TestMissingCandidateClearsOnPositivePartialScanAndRetainsHistory(t *testing.T) {
	f := newObservationFixture(t)
	ctx := context.Background()
	first := missingSeal(f)
	finishMissing(t, f, first)
	before := missingProtectedSnapshot(t, f)
	second := nextMissingSeal(t, f)
	file, err := f.q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: f.root, RelPath: f.input.RelPath})
	if err != nil {
		t.Fatal(err)
	}
	input := f.input
	input.RunID = second.RunID
	input.ScanToken = second.ScanToken
	input.HealthRevision = second.HealthRevision
	input.StartMutationSeq = second.StartMutationSeq
	input.ObservationID = uuid.New()
	input.NewFileID = uuid.New()
	input.Identity = ports.FileIdentity{Size: file.SizeBytes, Mtime: file.MtimeNs}
	if _, err = f.store.Record(ctx, input); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Interrupt(ctx, second); err != nil {
		t.Fatal(err)
	}
	var state, reason string
	if err = f.pool.QueryRow(ctx, "SELECT state,clear_reason FROM missing_candidate WHERE file_id=$1 AND first_run_id=$2", f.file, first.RunID).Scan(&state, &reason); err != nil || state != "cleared" || reason != "file observed" {
		t.Fatalf("reappearance not cleared: %s %s %v", state, reason, err)
	}
	if after := missingProtectedSnapshot(t, f); after != before {
		t.Fatal("positive presence changed original facts or file")
	}
	third := nextMissingSeal(t, f)
	finishMissing(t, f, third)
	assertMissingCycle(t, f, third.RunID, third.RunID, 2)
}
func TestMissingCandidateRestartsAfterChangedEpochOrFailedSource(t *testing.T) {
	for _, kind := range []string{"health", "generation", "physical_identity", "mutation", "partial_source", "review_source"} {
		t.Run(kind, func(t *testing.T) {
			f := newObservationFixture(t)
			ctx := context.Background()
			first := missingSeal(f)
			finishMissing(t, f, first)
			switch kind {
			case "health":
				for _, h := range []string{"offline", "online"} {
					if _, err := f.q.UpdateRootHealth(ctx, db.UpdateRootHealthParams{ID: f.root, Health: h}); err != nil {
						t.Fatal(err)
					}
				}
			case "generation":
				if _, err := f.pool.Exec(ctx, "UPDATE library_root SET generation=generation+1 WHERE id=$1", f.root); err != nil {
					t.Fatal(err)
				}
			case "mutation":
				seq, err := f.q.IncrementRootMutationSeq(ctx, f.root)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.pool.Exec(ctx, "UPDATE library_file SET last_mutation_seq=$2 WHERE id=$1", f.file, seq); err != nil {
					t.Fatal(err)
				}
			case "partial_source":
				if _, err := f.pool.Exec(ctx, "UPDATE scan_run SET state='partial' WHERE id=$1", first.RunID); err != nil {
					t.Fatal(err)
				}
			case "review_source":
				if err := f.q.SetScanMissingReview(ctx, db.SetScanMissingReviewParams{ScanRunID: first.RunID, MissingReviewRequired: true}); err != nil {
					t.Fatal(err)
				}
			}
			before := missingProtectedSnapshot(t, f)
			second := nextMissingSeal(t, f)
			if kind == "physical_identity" {
				second.RootIdentity.Inode++
				unchanged := finalizationSnapshot(t, f)
				if err := f.store.Seal(ctx, second); !errors.Is(err, ports.ErrScanObservationStale) {
					t.Fatalf("replacement sealed without new generation: %v", err)
				}
				if after := finalizationSnapshot(t, f); after != unchanged {
					t.Fatal("rejected physical root changed persisted scan state")
				}
				assertMissingCycle(t, f, first.RunID, first.RunID, 1)
				// Only an explicit new root epoch may establish a different binding.
				if _, err := f.pool.Exec(ctx, "UPDATE library_root SET generation=generation+1,baseline_completed=false WHERE id=$1", f.root); err != nil {
					t.Fatal(err)
				}
				second = nextMissingSeal(t, f)
				second.RootIdentity.Inode++
			}
			finishMissing(t, f, second)
			assertMissingCycle(t, f, second.RunID, second.RunID, 2)
			if after := missingProtectedSnapshot(t, f); after != before {
				t.Fatal("epoch reset changed file or facts")
			}
		})
	}
}
func TestMissingCandidateBatchAndPublicationFaultAtomic(t *testing.T) {
	for _, site := range []string{"candidate", "completed_health"} {
		t.Run(site, func(t *testing.T) {
			f := newObservationFixture(t)
			ctx := context.Background()
			seal := missingSeal(f)
			if err := f.store.Seal(ctx, seal); err != nil {
				t.Fatal(err)
			}
			ddl := `CREATE FUNCTION candidate_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic candidate fault'; END $$;
 CREATE TRIGGER candidate_fault BEFORE INSERT OR UPDATE ON missing_candidate FOR EACH ROW EXECUTE FUNCTION candidate_fault()`
			table := "missing_candidate"
			if site == "completed_health" {
				table = "scan_finalization"
				ddl = `CREATE FUNCTION candidate_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.completed_health_revision IS NOT NULL THEN RAISE EXCEPTION 'synthetic publication fault'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER candidate_fault BEFORE UPDATE ON scan_finalization FOR EACH ROW EXECUTE FUNCTION candidate_fault()`
			}
			if _, err := f.pool.Exec(ctx, ddl); err != nil {
				t.Fatal(err)
			}
			before := finalizationSnapshot(t, f)
			protected := missingProtectedSnapshot(t, f)
			if _, err := f.store.ReconcileBatch(ctx, seal); err == nil {
				t.Fatal("fault was ignored")
			}
			if after := finalizationSnapshot(t, f); after != before {
				t.Fatal("candidate, cursor, run or root survived rollback")
			}
			if after := missingProtectedSnapshot(t, f); after != protected {
				t.Fatal("fault changed file or facts")
			}
			if _, err := f.pool.Exec(ctx, fmt.Sprintf("DROP TRIGGER candidate_fault ON %s; DROP FUNCTION candidate_fault()", table)); err != nil {
				t.Fatal(err)
			}
			p, err := f.store.ReconcileBatch(ctx, seal)
			if err != nil || !p.Done || p.Missing != 1 {
				t.Fatal(p, err)
			}
			assertMissingCycle(t, f, seal.RunID, seal.RunID, 1)
		})
	}
}
func TestMissingCandidateEightWayReplay(t *testing.T) {
	f := newObservationFixture(t)
	seal := missingSeal(f)
	ctx := context.Background()
	if err := f.store.Seal(ctx, seal); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := f.store.ReconcileBatch(ctx, seal)
			if err == nil && (!p.Done || p.Missing != 1) {
				err = fmt.Errorf("unexpected counts: %+v", p)
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertMissingCycle(t, f, seal.RunID, seal.RunID, 1)
}
func TestMissingReviewPersistsStrictFractionAndNoInvalidation(t *testing.T) {
	for _, c := range []struct {
		indexed, missing int
		review           bool
	}{{50, 10, false}, {49, 10, true}, {51, 10, false}, {50, 11, true}, {9, 9, false}} {
		t.Run(fmt.Sprintf("%d_of_%d", c.missing, c.indexed), func(t *testing.T) {
			f := newObservationFixture(t)
			ctx := context.Background()
			seal := missingSeal(f)
			if _, err := f.pool.Exec(ctx, `INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns,created_at)
 SELECT gen_random_uuid(),$1,'ratio-'||i||'.mkv',1,1,(SELECT created_at FROM scan_run WHERE id=$2)-interval '1 second' FROM generate_series(1,$3::int) i`, f.root, seal.RunID, c.indexed-1); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < c.indexed-c.missing; i++ {
				rel := f.input.RelPath
				if i > 0 {
					rel = fmt.Sprintf("ratio-%d.mkv", i)
				}
				file, err := f.q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: f.root, RelPath: rel})
				if err != nil {
					t.Fatal(err)
				}
				input := f.input
				input.ObservationID = uuid.New()
				input.NewFileID = uuid.New()
				input.RelPath = file.RelPath
				input.Identity = ports.FileIdentity{Size: file.SizeBytes, Mtime: file.MtimeNs}
				if _, err = f.store.Record(ctx, input); err != nil {
					t.Fatal(err)
				}
			}
			protected := missingProtectedSnapshot(t, f)
			p := finishMissing(t, f, seal)
			if p.Missing != int64(c.missing) || p.MissingReviewRequired != c.review {
				t.Fatalf("progress: %+v", p)
			}
			final, err := f.q.LockScanFinalization(ctx, seal.RunID)
			if err != nil || final.IndexedFiles != int64(c.indexed) || final.MissingReviewRequired != c.review || !final.CompletedHealthRevision.Valid {
				t.Fatalf("stored scope/decision: %+v %v", final, err)
			}
			if after := missingProtectedSnapshot(t, f); after != protected {
				t.Fatal("review changed protected file/facts")
			}
			var count int
			if err = f.pool.QueryRow(ctx, "SELECT count(*) FROM missing_candidate WHERE root_id=$1 AND state='open'", f.root).Scan(&count); err != nil || count != c.missing {
				t.Fatal(count, err)
			}
		})
	}
}

func TestMissingCandidateSourceScopeForeignKeys(t *testing.T) {
	f := newObservationFixture(t)
	ctx := context.Background()
	other, err := f.q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: t.TempDir(), MediaType: "tv", Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.q.StartScanRun(ctx, db.StartScanRunParams{ID: other.ID, ID_2: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"file_root", "first_run_root", "last_run_root"} {
		root, first, last := f.root, f.input.RunID, f.input.RunID
		switch kind {
		case "file_root":
			root = other.ID
			first = run.ID
			last = run.ID
		case "first_run_root":
			first = run.ID
		case "last_run_root":
			last = run.ID
		}
		_, err = f.pool.Exec(ctx, "INSERT INTO missing_candidate(file_id,root_id,first_run_id,last_run_id) VALUES($1,$2,$3,$4)", f.file, root, first, last)
		requireForeignKeyFailure(t, err)
	}
	var count int
	if err = f.pool.QueryRow(ctx, "SELECT count(*) FROM missing_candidate WHERE file_id=$1", f.file).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
func TestMissingMigrationPreservesUncertifiedLegacyCursorAndFacts(t *testing.T) {
	f := newObservationFixture(t)
	ctx := context.Background()
	v := int64(postgres.CurrentMigrationVersion)
	var err error
	for v > 48 {
		v, err = postgres.Rollback(ctx, f.dsn, migrationsDir)
		if err != nil {
			t.Fatal(err)
		}
	}
	in := f.input
	if _, err = f.pool.Exec(ctx, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at)
 VALUES($1,$2,$3,$4,$5,$6,1,99,true,clock_timestamp())`, in.RunID, in.RootID, in.Path, in.MediaType, in.MarkerFilename, in.HealthRevision); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, "UPDATE scan_run SET state='succeeded',finished_at=clock_timestamp() WHERE id=$1", in.RunID); err != nil {
		t.Fatal(err)
	}
	protected := missingProtectedSnapshot(t, f)
	if v, err = postgres.Migrate(ctx, f.dsn, migrationsDir); err != nil || v != postgres.CurrentMigrationVersion {
		t.Fatal(v, err)
	}
	final, err := f.q.LockScanFinalization(ctx, in.RunID)
	if err != nil || final.IndexedFiles != 0 || final.CompletedHealthRevision.Valid {
		t.Fatal("legacy cursor was certified", err)
	}
	var count int
	if err = f.pool.QueryRow(ctx, "SELECT count(*) FROM missing_candidate WHERE root_id=$1", f.root).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if after := missingProtectedSnapshot(t, f); after != protected {
		t.Fatal("migration rewrote files, mappings or quality")
	}
	for v > 48 {
		v, err = postgres.Rollback(ctx, f.dsn, migrationsDir)
		if err != nil {
			t.Fatal(err)
		}
	}
	if v, err = postgres.Migrate(ctx, f.dsn, migrationsDir); err != nil || v != postgres.CurrentMigrationVersion {
		t.Fatal(v, err)
	}
}
