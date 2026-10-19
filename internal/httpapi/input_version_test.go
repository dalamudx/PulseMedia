package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestWantedInputVersionsInvalidateAtCommit(t *testing.T) {
	a, _, token, root, _, _, router, _ := setupOperationHTTP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	q := a.Queries()
	pool := a.DBPool()
	media, unit := uuid.New(), uuid.New()
	if _, e := q.CreateMedia(ctx, db.CreateMediaParams{ID: media, Title: "Version Movie", MediaType: "movie"}); e != nil {
		t.Fatal(e)
	}
	if _, e := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unit, MediaID: media, UnitKey: "movie"}); e != nil {
		t.Fatal(e)
	}
	body, _ := json.Marshal(httpapi.CreateSubscriptionRequest{MediaId: &media, RootId: &root})
	req := httptest.NewRequest("POST", "/api/v1/subscriptions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", uuid.NewString())
	r := httptest.NewRecorder()
	router.ServeHTTP(r, req)
	var sub httpapi.SubscriptionDetails
	if r.Code != 201 || json.Unmarshal(r.Body.Bytes(), &sub) != nil {
		t.Fatal(r.Code)
	}
	if sub.WantedItems == nil || len(*sub.WantedItems) != 1 {
		t.Fatal("wanted missing in creation")
	}
	wanted := (*sub.WantedItems)[0].Id
	file := uuid.New()
	if _, e := q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: file, RootID: root, RelPath: "version.mkv", SizeBytes: 100, MtimeNs: 1}); e != nil {
		t.Fatal(e)
	}
	if e := q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file, UnitID: unit}); e != nil {
		t.Fatal(e)
	}
	appendFact := func() {
		t.Helper()
		if _, e := q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: unit, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: file, Valid: true}, RootID: pgtype.UUID{Bytes: root, Valid: true}, QualityProfile: []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v2"}`)}); e != nil {
			t.Fatal(e)
		}
	}
	appendFact()
	eval := subscription.NewEvaluator(postgres.NewWantedProjectionStore(pool))
	project := func() {
		t.Helper()
		if _, e := eval.EvaluateWantedItem(ctx, wanted, quality.DefaultPolicy()); e != nil {
			t.Fatal(e)
		}
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	assertCurrent := func(want bool) {
		t.Helper()
		var current bool
		if e := pool.QueryRow(ctx, "SELECT inputs_current FROM wanted_projection_validity WHERE id=$1", wanted).Scan(&current); e != nil || current != want {
			t.Fatal("input stamp mismatch", current, want, e)
		}
	}
	assertHTTP := func(satisfied bool) {
		t.Helper()
		rq := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+sub.Id.String(), nil)
		rq.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, rq)
		var details httpapi.SubscriptionDetails
		if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &details) != nil || details.WantedItems == nil || len(*details.WantedItems) != 1 || (*details.WantedItems)[0].IsSatisfied != satisfied {
			t.Fatal("stale projection green or read failed", rr.Code, details)
		}
		if !satisfied && (*details.WantedItems)[0].CurrentFactStage != nil {
			t.Fatal("stale stage leaked")
		}
	}
	assertDue := func() {
		t.Helper()
		rows, e := q.ListPendingEvaluationWantedItems(ctx, 100)
		if e != nil {
			t.Fatal(e)
		}
		for _, row := range rows {
			if row.ID == wanted {
				return
			}
		}
		t.Fatal("input change failed to wake the existing scheduler")
	}
	project()
	assertCurrent(true)
	assertHTTP(true)
	// Far-future scheduling cannot suppress an input-version invalidation.
	exec("UPDATE wanted_item SET next_evaluate_at=clock_timestamp()+interval '1 year' WHERE id=$1", wanted)
	old, e := q.GetLibraryRootByID(ctx, root)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, e = tx.Exec(ctx, "UPDATE library_root SET health='offline' WHERE id=$1", root); e != nil {
		t.Fatal(e)
	}
	assertHTTP(true) // uncommitted source is invisible
	if e = tx.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	assertCurrent(true)
	after, e := q.GetLibraryRootByID(ctx, root)
	if e != nil || after.HealthRevision != old.HealthRevision {
		t.Fatal("rollback bumped root revision", e)
	}
	exec("UPDATE library_root SET health='offline' WHERE id=$1", root)
	assertCurrent(false)
	assertHTTP(false)
	assertDue()
	exec("UPDATE library_root SET health='online' WHERE id=$1", root)
	assertCurrent(false)
	project()
	assertHTTP(true)
	before, e := q.GetMediaUnit(ctx, db.GetMediaUnitParams{MediaID: media, UnitKey: "movie"})
	if e != nil {
		t.Fatal(e)
	}
	exec("UPDATE media_unit SET air_date=CURRENT_DATE+1 WHERE id=$1", unit)
	assertCurrent(false)
	assertHTTP(false)
	assertDue()
	// DATE has a defined UTC calendar boundary in the domain, independent of DB session zone.
	zone, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = zone.Rollback(ctx) }()
	if _, e = zone.Exec(ctx, "SET LOCAL TIME ZONE 'Asia/Tokyo'"); e != nil {
		t.Fatal(e)
	}
	observed, e := q.GetSearchDatabaseTime(ctx)
	if e != nil {
		t.Fatal(e)
	}
	horizon, e := q.WithTx(zone).GetWantedProjectionHorizon(ctx, db.GetWantedProjectionHorizonParams{ID: wanted, ObservedAt: observed})
	if e != nil {
		t.Fatal(e)
	}
	future, e := q.GetMediaUnit(ctx, db.GetMediaUnitParams{MediaID: media, UnitKey: "movie"})
	if e != nil || !horizon.Valid || !horizon.Time.Equal(future.AirDate.Time) {
		t.Fatal("calendar horizon depended on session timezone", horizon, e)
	}
	if e = zone.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	exec("UPDATE media_unit SET air_date=NULL WHERE id=$1", unit)
	project()
	assertHTTP(true)
	if _, e = pool.Exec(ctx, "UPDATE media_unit SET metadata_version=$2 WHERE id=$1", unit, before.MetadataVersion); e == nil {
		t.Fatal("metadata revision was allowed to rewind")
	}
	// Identity changes (same title/year) still invalidate matching provenance.
	if _, e = q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: uuid.New(), MediaID: media, Namespace: "tmdb.movie", ExternalID: "910001"}); e != nil {
		t.Fatal(e)
	}
	assertCurrent(false)
	assertHTTP(false)
	project()
	assertHTTP(true)
	// A late version-write failure must roll back the content change, revocation,
	// and input revision together; the last legitimate projection remains current.
	exec("CREATE FUNCTION reject_input_version() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.facts_version>OLD.facts_version THEN RAISE EXCEPTION 'fixture version failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_input_version BEFORE UPDATE ON media_unit FOR EACH ROW EXECUTE FUNCTION reject_input_version()")
	if _, e = pool.Exec(ctx, "UPDATE library_file SET mtime_ns=mtime_ns+1 WHERE id=$1", file); e == nil {
		t.Fatal("late version fault was acknowledged")
	}
	assertCurrent(true)
	assertHTTP(true)
	var unchanged int64
	if e = pool.QueryRow(ctx, "SELECT mtime_ns FROM library_file WHERE id=$1", file).Scan(&unchanged); e != nil || unchanged != 1 {
		t.Fatal("file update partially committed on version failure", unchanged, e)
	}
	exec("DROP TRIGGER reject_input_version ON media_unit")
	// Content change revokes old imports, but keeps file mappings/facts and reservations.
	exec("UPDATE library_file SET mtime_ns=mtime_ns+1 WHERE id=$1", file)
	assertCurrent(false)
	assertHTTP(false)
	assertDue()
	project()
	report, e := eval.EvaluateWantedItem(ctx, wanted, quality.DefaultPolicy())
	if e != nil || report.IsSatisfied || report.WaitReason != "quality_unknown" {
		t.Fatal("changed bytes retained old quality", report, e)
	}
	var reason string
	if e = pool.QueryRow(ctx, "SELECT revocation_reason FROM unit_fact WHERE source_file_id=$1 ORDER BY created_at LIMIT 1", file).Scan(&reason); e != nil || reason != "file_content_changed" {
		t.Fatal("content invalidation missing provenance", reason, e)
	}
	appendFact()
	project()
	assertHTTP(true)
	// Temporal invalidity needs no DB mutation event; both read masking and due query enforce it.
	exec("UPDATE wanted_item SET projection_valid_until=clock_timestamp()-interval '1 second',next_evaluate_at=clock_timestamp()+interval '1 year' WHERE id=$1", wanted)
	assertCurrent(true)
	assertHTTP(false)
	assertDue()
	project()
	assertHTTP(true)
	exec("UPDATE unit_fact SET expires_at=clock_timestamp()+interval '20 seconds' WHERE source_file_id=$1 AND revoked_at IS NULL", file)
	project()
	stamped, e := q.GetWantedItemByID(ctx, wanted)
	if e != nil || !stamped.ProjectionValidUntil.Valid || stamped.NextEvaluateAt.Time.After(stamped.ProjectionValidUntil.Time) {
		t.Fatal("TTL horizon not bounded", stamped, e)
	}
	// FK KEY SHARE acquired by a producer must not form a cycle with guarded
	// root/unit business locks. Pause it AFTER its FK checks, BEFORE version bump.
	exec("CREATE FUNCTION pause_input_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(210021); RETURN NEW; END $$; CREATE TRIGGER zz_pause_input AFTER INSERT ON unit_fact FOR EACH ROW EXECUTE FUNCTION pause_input_write()")
	barrier, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = barrier.Rollback(ctx) }()
	if _, e = barrier.Exec(ctx, "SELECT pg_advisory_xact_lock(210021)"); e != nil {
		t.Fatal(e)
	}
	owner, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = owner.Rollback(ctx) }()
	locked := q.WithTx(owner)
	if _, e = locked.LockAcquisitionRoot(ctx, root); e != nil {
		t.Fatal(e)
	}
	receipt := make(chan error, 1)
	var writer sync.WaitGroup
	writer.Add(1)
	defer func() {
		cancel()
		_ = barrier.Rollback(context.Background())
		_ = owner.Rollback(context.Background())
		writer.Wait()
	}()
	go func() {
		defer writer.Done()
		_, err := q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: unit, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: file, Valid: true}, RootID: pgtype.UUID{Bytes: root, Valid: true}, QualityProfile: []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v2"}`)})
		receipt <- err
	}()
	fkDeadline := time.Now().Add(3 * time.Second)
	for {
		var paused bool
		if e = pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND query LIKE '%CreateUnitFact%')").Scan(&paused); e != nil {
			t.Fatal(e)
		}
		if paused {
			break
		}
		if time.Now().After(fkDeadline) {
			t.Fatal("FK producer blocked on parent root instead of reaching the controlled barrier")
		}
		time.Sleep(5 * time.Millisecond)
	}
	unitCtx, unitStop := context.WithTimeout(ctx, time.Second)
	defer unitStop()
	if _, e = locked.LockAcquisitionUnit(unitCtx, unit); e != nil {
		t.Fatal("unit business lock conflicts with producer FK KEY SHARE", e)
	}
	if e = barrier.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	assertCurrent(true) // new fact/version cannot partially escape the blocked statement
	if e = owner.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-receipt; e != nil {
		t.Fatal("facts-version producer deadlocked", e)
	}
	assertCurrent(false)
	assertDue()
	project()
	assertHTTP(true)
	exec("DROP TRIGGER zz_pause_input ON unit_fact")
	// Equal numeric revisions on another entity must not reuse this unit's green cache.
	cached, e := q.GetWantedItemByID(ctx, wanted)
	if e != nil {
		t.Fatal(e)
	}
	other := uuid.New()
	if _, e = q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: other, MediaID: media, UnitKey: "alternate"}); e != nil {
		t.Fatal(e)
	}
	exec("UPDATE media_unit SET metadata_version=$2,facts_version=$3 WHERE id=$1", other, cached.EvaluatedUnitMetadataVersion.Int64, cached.EvaluatedFactsVersion.Int64)
	exec("UPDATE wanted_item SET unit_id=$2 WHERE id=$1", wanted, other)
	assertCurrent(false)
	assertHTTP(false)
	assertDue()
	exec("UPDATE wanted_item SET unit_id=$2 WHERE id=$1", wanted, unit)
	assertCurrent(true)
	assertHTTP(true)
	// Mapping retirement invalidates current cache without deleting source history.
	if e = pool.QueryRow(ctx, "SELECT facts_version FROM media_unit WHERE id=$1", unit).Scan(&before.FactsVersion); e != nil {
		t.Fatal(e)
	}
	exec("UPDATE library_file_unit SET retired_at=clock_timestamp() WHERE file_id=$1 AND unit_id=$2", file, unit)
	assertCurrent(false)
	assertDue()
	assertHTTP(false)
	afterUnit, e := q.GetMediaUnit(ctx, db.GetMediaUnitParams{MediaID: media, UnitKey: "movie"})
	if e != nil || afterUnit.FactsVersion <= before.FactsVersion {
		t.Fatal("mapping change did not bump facts", e)
	}
	exec("UPDATE library_root SET generation=generation+1 WHERE id=$1", root)
	changed, e := q.GetLibraryRootByID(ctx, root)
	if e != nil || changed.BaselineCompleted {
		t.Fatal("new root generation reused old baseline", changed, e)
	}
	report, e = eval.EvaluateWantedItem(ctx, wanted, quality.DefaultPolicy())
	if e != nil || report.WaitReason != "baseline_pending" {
		t.Fatal("root identity changed without revalidation", report, e)
	}
}
