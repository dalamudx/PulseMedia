package postgres_test

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestM4ScanFinalization100kDBOnly proves bounded database reconciliation and
// durable recovery, using the logical root identity from observationFixture.
// It performs no filesystem traversal or NAS I/O. The separate real-filesystem
// 100k positive scan supplies disk evidence; this deliberately exercises absence.
func TestM4ScanFinalization100kDBOnly(t *testing.T) {
	if os.Getenv("PULSE_M4_SCAN_SCALE") != "1" {
		t.Skip("DB-only 100k catalog reconciliation: opt in with PULSE_M4_SCAN_SCALE=1")
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	f := newObservationFixture(t)
	seal := missingSeal(f)
	var version int
	if err := f.pool.QueryRow(ctx, "SELECT current_setting('server_version_num')::integer").Scan(&version); err != nil || version/10000 != 18 {
		t.Fatalf("requires actual PG18: version=%d err=%v", version, err)
	}
	// 99,999 deterministic UUIDs plus the fixture's mapped file = 100,000.
	// Backdated catalog rows model files indexed before this run, not observations.
	seeded := time.Now()
	if _, err := f.pool.Exec(ctx, `INSERT INTO library_file
 (id,root_id,rel_path,size_bytes,mtime_ns,created_at)
 SELECT ('00000000-0000-0000-0000-'||lpad(to_hex(i),12,'0'))::uuid,
 $1,'scale-'||i||'.mkv',42,1,
 (SELECT created_at FROM scan_run WHERE id=$2)-interval '1 second'
 FROM generate_series(1,99999) i`, f.root, seal.RunID); err != nil {
		t.Fatal(err)
	}
	// Keep both missing mapped files and the original multi-unit file as sentinels.
	for i, n := range []int64{1, 99999} {
		unit := []uuid.UUID{f.first, f.second}[i]
		file := m4ScaleFileID(n)
		if err := f.q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file, UnitID: unit}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.q.CreateUnitFact(ctx, db.CreateUnitFactParams{
			ID: uuid.New(), UnitID: unit, Stage: []string{"imported", "confirmed"}[i],
			SourceFileID:   pgtype.UUID{Bytes: file, Valid: true},
			RootID:         pgtype.UUID{Bytes: f.root, Valid: true},
			QualityProfile: []byte(`{"sentinel":"DB-only scale evidence"}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	file, err := f.q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: f.root, RelPath: f.input.RelPath})
	if err != nil {
		t.Fatal(err)
	}
	in := f.input
	in.Identity = ports.FileIdentity{Size: file.SizeBytes, Mtime: file.MtimeNs}
	if got, err := f.store.Record(ctx, in); err != nil || !got.Recorded || got.Added || got.Updated {
		t.Fatalf("unchanged positive observation: %+v %v", got, err)
	}
	if err := f.store.Seal(ctx, seal); err != nil {
		t.Fatal(err)
	}
	initial := m4ScaleStateRead(t, ctx, f)
	if initial.Indexed != 100000 || initial.Cursor != 0 || initial.Missing != 0 || initial.Baseline {
		t.Fatalf("bad frozen scope: %+v", initial)
	}
	// Mutate after sealing: the frozen denominator stays 100k, while these three
	// files are protected from absence. A fourth, newly created file is also out.
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() { _ = tx.Rollback(context.Background()) }()
		q := db.New(tx)
		seq, err := q.IncrementRootMutationSeq(ctx, f.root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE library_file SET last_mutation_seq=$1
 WHERE id IN($2,$3,$4)`, seq, f.file, m4ScaleFileID(99997), m4ScaleFileID(99998)); err != nil {
			t.Fatal(err)
		}
		if _, err = q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: uuid.New(), RootID: f.root, RelPath: "scale-late.mkv", SizeBytes: 42, MtimeNs: 1}); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}()
	seedDuration := time.Since(seeded)
	protected := m4ScaleProtected(t, ctx, f)
	schema := m4ScaleSchema(t, ctx, f.pool)

	tracer := &m4ScaleCommitTrace{}
	openWorker := func() *pgxpool.Pool {
		t.Helper()
		cfg := f.pool.Config()
		cfg.MinConns, cfg.MaxConns = 0, 1
		cfg.ConnConfig.Tracer = tracer
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		if err := pool.Ping(ctx); err != nil {
			t.Fatal(err)
		}
		return pool
	}
	worker := openWorker()
	store := postgres.NewScanObservationStore(worker)
	state := initial
	var batches, maxCursorDelta, maxCountDelta int64
	var maxBatch time.Duration
	reconcileStarted := time.Now()
	step := func(wantDone bool) {
		t.Helper()
		before := state
		batchStarted := time.Now()
		p, err := store.ReconcileBatch(ctx, seal)
		elapsed := time.Since(batchStarted)
		if err != nil {
			t.Fatalf("batch %d cursor=%d: %v", batches+1, before.Cursor, err)
		}
		state = m4ScaleStateRead(t, ctx, f)
		cursorDelta, countDelta := state.Cursor-before.Cursor, state.Missing-before.Missing
		if cursorDelta < 1 || cursorDelta > 128 || countDelta < 1 || countDelta > 128 || p.Missing != state.Missing || p.Done != wantDone || state.Baseline != wantDone || state.Completed != wantDone || state.Exhausted != wantDone || (state.RunState == "succeeded") != wantDone || state.Indexed != 100000 {
			t.Fatalf("batch %d delta cursor/count=%d/%d before=%+v after=%+v progress=%+v", batches+1, cursorDelta, countDelta, before, state, p)
		}
		if p.Observed != 1 || p.Added != 0 || p.Updated != 0 || p.MissingReviewRequired != wantDone {
			t.Fatalf("observation/review counts: %+v", p)
		}
		maxCursorDelta = max(maxCursorDelta, cursorDelta)
		maxCountDelta = max(maxCountDelta, countDelta)
		maxBatch = max(maxBatch, elapsed)
		batches++
	}
	for range 256 {
		step(false)
	}
	if state.Cursor != 32768 || state.Missing != 32768 {
		t.Fatalf("midpoint: %+v", state)
	}
	// Faults are additive triggers in this private DB. All production guards,
	// foreign keys and durability settings remain active throughout.
	fault := func(name, table, timing, condition string, commit bool) {
		t.Helper()
		before := m4ScaleCheckpoint(t, ctx, f)
		commitErrors := tracer.errors()
		ddl := fmt.Sprintf(`CREATE FUNCTION m4_scale_fault() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
 IF %s THEN RAISE EXCEPTION 'm4 scale %s fault'; END IF;
 RETURN NEW;
 END $$;
 CREATE %s TRIGGER m4_scale_fault %s ON %s %s FOR EACH ROW EXECUTE FUNCTION m4_scale_fault()`, condition, name,
			map[bool]string{false: "", true: "CONSTRAINT"}[commit], timing, table,
			map[bool]string{false: "", true: "DEFERRABLE INITIALLY DEFERRED"}[commit])
		if _, err := f.pool.Exec(ctx, ddl); err != nil {
			t.Fatal(err)
		}
		faultStarted := time.Now()
		_, err := store.ReconcileBatch(ctx, seal)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "P0001" || pgErr.Message != "m4 scale "+name+" fault" {
			t.Fatalf("%s: expected injected error, got %v", name, err)
		}
		wantCommitErrors := commitErrors
		if commit {
			wantCommitErrors++
		}
		if tracer.errors() != wantCommitErrors {
			t.Fatalf("%s: error occurred at wrong SQL stage: COMMIT errors=%d want=%d", name, tracer.errors(), wantCommitErrors)
		}
		if after := m4ScaleCheckpoint(t, ctx, f); after != before {
			t.Fatalf("%s left changed candidates/count/cursor/baseline", name)
		}
		if _, err := f.pool.Exec(ctx, "DROP TRIGGER m4_scale_fault ON "+table+"; DROP FUNCTION m4_scale_fault()"); err != nil {
			t.Fatal(err)
		}
		t.Logf("DB_ONLY fault=%s rollback_verified=true cursor=%d count=%d duration=%s", name, state.Cursor, state.Missing, time.Since(faultStarted))
	}
	// Eleven candidates are inserted before the twelfth write fails.
	fault("candidate_write", "missing_candidate", "BEFORE INSERT", "NEW.file_id='"+m4ScaleFileID(32780).String()+"'::uuid", false)
	// All 128 candidates and the count update precede this cursor-write fault.
	fault("cursor_write", "scan_finalization", "BEFORE UPDATE", "true", false)
	// Initially deferred trigger runs only at COMMIT, after cursor/count writes.
	fault("mid_commit", "scan_finalization", "AFTER UPDATE", "true", true)

	beforeRestart := m4ScaleCheckpoint(t, ctx, f)
	var oldPID, newPID int
	if err := worker.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&oldPID); err != nil {
		t.Fatal(err)
	}
	worker.Close()
	worker = openWorker()
	if err := worker.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&newPID); err != nil || newPID == oldPID {
		t.Fatalf("fresh physical connection required: old=%d new=%d err=%v", oldPID, newPID, err)
	}
	store = postgres.NewScanObservationStore(worker)
	pending, err := store.FindFinalizing(ctx, f.root)
	if err != nil || pending == nil || *pending != seal {
		t.Fatalf("same persisted seal unavailable after fresh pool: %+v %v", pending, err)
	}
	if err := store.Seal(ctx, *pending); err != nil {
		t.Fatal(err)
	}
	if after := m4ScaleCheckpoint(t, ctx, f); after != beforeRestart {
		t.Fatal("pool restart or seal replay changed persisted progress")
	}
	t.Logf("DB_ONLY resumed_new_pool=true cursor=%d count=%d", state.Cursor, state.Missing)
	for batches < 781 {
		step(false)
	}
	if state.Missing != 99968 {
		t.Fatalf("tail checkpoint: %+v", state)
	}
	// Distinct from the 130-row tests: deferred failure after all tail candidates,
	// review decision, success timestamps AND baseline publication were written.
	fault("tail_commit", "library_root", "AFTER UPDATE", "NEW.baseline_completed", true)
	step(true)
	reconcileDuration := time.Since(reconcileStarted)
	if batches != 782 || state.Cursor != 99999 || state.Missing != 99997 || !state.Review {
		t.Fatalf("final counts: batches=%d state=%+v", batches, state)
	}
	var total, unique, validSource, differences int64
	if err := f.pool.QueryRow(ctx, `SELECT count(*),count(DISTINCT file_id),
 count(*) FILTER(WHERE first_run_id=$2 AND last_run_id=$2 AND state='open')
 FROM missing_candidate WHERE root_id=$1`, f.root, seal.RunID).Scan(&total, &unique, &validSource); err != nil {
		t.Fatal(err)
	}
	// Compare exact sets independently of the production filtering query.
	if err := f.pool.QueryRow(ctx, `WITH expected AS (
 SELECT ('00000000-0000-0000-0000-'||lpad(to_hex(i),12,'0'))::uuid AS id
 FROM generate_series(1,99999) i WHERE i NOT IN(99997,99998)),
 actual AS (SELECT file_id AS id FROM missing_candidate WHERE root_id=$1)
 SELECT count(*) FROM (
 (SELECT id FROM expected EXCEPT SELECT id FROM actual) UNION ALL
 (SELECT id FROM actual EXCEPT SELECT id FROM expected)) delta`, f.root).Scan(&differences); err != nil {
		t.Fatal(err)
	}
	if total != 99997 || unique != total || validSource != total || differences != 0 {
		t.Fatalf("exact candidate set: total=%d unique=%d valid_source=%d differences=%d", total, unique, validSource, differences)
	}
	var active int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM library_file WHERE root_id=$1 AND retired_at IS NULL", f.root).Scan(&active); err != nil || active != 100001 {
		t.Fatalf("catalog files retired or lost: active=%d err=%v", active, err)
	}
	if after := m4ScaleProtected(t, ctx, f); after != protected {
		t.Fatal("reconciliation changed mapped files, mappings, facts or quality")
	}
	checks, err := store.ListMissingChecks(ctx, seal, uuid.Nil)
	if !errors.Is(err, ports.ErrScanObservationStale) || len(checks) != 0 {
		t.Fatalf("review-required source authorized invalidation: checks=%d err=%v", len(checks), err)
	}
	completed := m4ScaleCheckpoint(t, ctx, f)
	for range 3 {
		p, err := store.ReconcileBatch(ctx, seal)
		if err != nil || !p.Done || p.Missing != total || !p.MissingReviewRequired {
			t.Fatalf("completed replay: %+v %v", p, err)
		}
	}
	if after := m4ScaleCheckpoint(t, ctx, f); after != completed {
		t.Fatal("completed replay double-counted or rewrote timestamps/candidates")
	}
	if after := m4ScaleSchema(t, ctx, f.pool); after != schema {
		t.Fatal("production triggers or constraints changed")
	}
	t.Logf("DB_ONLY PG=%d catalog=100000 indexed=100000 post_watermark=3 late_created=1 observed=1 batches=%d max_cursor_delta=%d max_count_delta=%d exact_unique_missing=%d set_difference=%d review_required=%t commit_faults=%d replay=3 seed=%s reconcile=%s max_batch=%s total=%s", version, batches, maxCursorDelta, maxCountDelta, unique, differences, state.Review, tracer.errors(), seedDuration, reconcileDuration, maxBatch, time.Since(started))
}

func m4ScaleFileID(n int64) uuid.UUID {
	var id uuid.UUID
	binary.BigEndian.PutUint64(id[8:], uint64(n))
	return id
}

type m4ScaleState struct {
	Cursor, Missing, Indexed               int64
	Baseline, Completed, Exhausted, Review bool
	RunState                               string
}

func m4ScaleStateRead(t *testing.T, ctx context.Context, f observationFixture) m4ScaleState {
	t.Helper()
	var s m4ScaleState
	var cursor pgtype.UUID
	if err := f.pool.QueryRow(ctx, `SELECT fin.cursor_file_id,run.files_missing_candidates,
 fin.indexed_files,root.baseline_completed,fin.completed_at IS NOT NULL,
 fin.exhausted,fin.missing_review_required,run.state
 FROM scan_finalization fin JOIN scan_run run ON run.id=fin.scan_run_id
 JOIN library_root root ON root.id=fin.root_id WHERE run.id=$1`, f.input.RunID).Scan(
		&cursor, &s.Missing, &s.Indexed, &s.Baseline, &s.Completed, &s.Exhausted, &s.Review, &s.RunState); err != nil {
		t.Fatal(err)
	}
	if cursor.Valid {
		s.Cursor = int64(binary.BigEndian.Uint64(cursor.Bytes[8:]))
	}
	return s
}

// Only checkpoint faults/restart/replays. Candidate aggregates stay in PG; no
// 100k-row JSON snapshot is transferred, and normal batches read eight scalars.
func m4ScaleCheckpoint(t *testing.T, ctx context.Context, f observationFixture) string {
	t.Helper()
	var value string
	if err := f.pool.QueryRow(ctx, `SELECT jsonb_build_object(
 'root',(SELECT to_jsonb(r) FROM library_root r WHERE id=$1),
 'run',(SELECT to_jsonb(r) FROM scan_run r WHERE id=$2),
 'final',(SELECT to_jsonb(r) FROM scan_finalization r WHERE scan_run_id=$2),
 'candidates',(SELECT jsonb_build_array(count(*),count(DISTINCT file_id),
 coalesce(sum(hashtextextended(to_jsonb(c)::text,0)::numeric),0))
 FROM missing_candidate c WHERE root_id=$1))::text`, f.root, f.input.RunID).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func m4ScaleProtected(t *testing.T, ctx context.Context, f observationFixture) string {
	t.Helper()
	var value string
	if err := f.pool.QueryRow(ctx, `SELECT jsonb_build_object(
 'files',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM library_file r WHERE id IN($2,$3,$4)),
 'mappings',(SELECT jsonb_agg(to_jsonb(r) ORDER BY file_id,unit_id) FROM library_file_unit r WHERE file_id IN($2,$3,$4)),
 'facts',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM unit_fact r WHERE root_id=$1),
 'units',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM media_unit r WHERE id IN($5,$6)))::text`,
		f.root, f.file, m4ScaleFileID(1), m4ScaleFileID(99999), f.first, f.second).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func m4ScaleSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var value string
	if err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
 'triggers',(SELECT jsonb_agg(jsonb_build_array(tgrelid,tgname,tgenabled,pg_get_triggerdef(oid)) ORDER BY oid)
 FROM pg_trigger WHERE tgrelid IN(SELECT oid FROM pg_class WHERE relnamespace='public'::regnamespace)),
 'constraints',(SELECT jsonb_agg(jsonb_build_array(conrelid,conname,convalidated,pg_get_constraintdef(oid)) ORDER BY oid)
 FROM pg_constraint WHERE connamespace='public'::regnamespace))::text`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

type m4ScaleSQLKey struct{}

type m4ScaleCommitTrace struct {
	mu           sync.Mutex
	commitErrors int
}

func (*m4ScaleCommitTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, m4ScaleSQLKey{}, data.SQL)
}

func (s *m4ScaleCommitTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if ctx.Value(m4ScaleSQLKey{}) == "commit" && data.Err != nil {
		s.mu.Lock()
		s.commitErrors++
		s.mu.Unlock()
	}
}

func (s *m4ScaleCommitTrace) errors() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commitErrors
}
