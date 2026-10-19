package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/ports"
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

func TestGuardedWantedProjection(t *testing.T) {
	a, _, token, root, _, candidate, router, _ := setupOperationHTTP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	q := a.Queries()
	pool := a.DBPool()
	media, unit := uuid.New(), uuid.New()
	if _, err := q.CreateMedia(ctx, db.CreateMediaParams{ID: media, Title: "Projection Movie", MediaType: "movie"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unit, MediaID: media, UnitKey: "movie"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(httpapi.CreateSubscriptionRequest{MediaId: &media, RootId: &root})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", uuid.NewString())
	out := httptest.NewRecorder()
	router.ServeHTTP(out, req)
	var created httpapi.SubscriptionDetails
	if out.Code != 201 || json.Unmarshal(out.Body.Bytes(), &created) != nil {
		t.Fatal(out.Code)
	}
	rows, err := q.ListWantedItemsForSubscription(ctx, created.Id)
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	wanted := rows[0].ID
	evaluator := subscription.NewEvaluator(postgres.NewWantedProjectionStore(pool))
	policy := quality.DefaultPolicy()
	evaluate := func(reason string, satisfied bool) {
		t.Helper()
		report, err := evaluator.EvaluateWantedItem(ctx, wanted, policy)
		if err != nil || report.WaitReason != reason || report.IsSatisfied != satisfied {
			t.Fatal(report, err)
		}
		stored, err := q.GetWantedItemByID(ctx, wanted)
		if err != nil || stored.IsSatisfied != satisfied || !stored.NextEvaluateAt.Valid {
			t.Fatal("projection write or periodic refresh lost", stored, err)
		}
	}
	evaluate("missing", false)
	file, fact := uuid.New(), uuid.New()
	if _, err := q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: file, RootID: root, RelPath: "projection.mkv", SizeBytes: 100, MtimeNs: 1}); err != nil {
		t.Fatal(err)
	}
	if err := q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file, UnitID: unit}); err != nil {
		t.Fatal(err)
	}
	evaluate("quality_unknown", false)
	acquireRaw, _ := json.Marshal(httpapi.AcquireMovieRequest{UnitId: unit, RootId: root, CandidateId: candidate})
	acquireReq := httptest.NewRequest(http.MethodPost, "/api/v1/movies/acquire", bytes.NewReader(acquireRaw))
	acquireReq.Header.Set("Authorization", "Bearer "+token)
	acquireReq.Header.Set("Content-Type", "application/json")
	acquireOut := httptest.NewRecorder()
	router.ServeHTTP(acquireOut, acquireReq)
	if acquireOut.Code != 409 || !bytes.Contains(acquireOut.Body.Bytes(), []byte("OWNERSHIP_UNCERTAIN")) {
		t.Fatal("unknown source became a download or opaque failure", acquireOut.Code)
	}
	var submitted int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE kind='download_submission'").Scan(&submitted); err != nil || submitted != 0 {
		t.Fatal("unknown file created a download job", submitted, err)
	}
	if _, err := q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: fact, UnitID: unit, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: file, Valid: true}, RootID: pgtype.UUID{Bytes: root, Valid: true}, QualityProfile: []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v2"}`)}); err != nil {
		t.Fatal(err)
	}
	policy.ResolutionOrder = map[quality.Resolution]int{quality.Res1080p: 99999} // caller rank map is not authority
	evaluate("", true)
	report, err := evaluator.EvaluateWantedItem(ctx, wanted, policy)
	if err != nil || report.Score != 340 {
		t.Fatal("caller injected a policy rank", report, err)
	}
	// A fact can expire after BEGIN while waiting for the root lock. Compare its
	// expiry against the database time after acquiring all input locks, not BEGIN.
	rootLock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rootLock.Rollback(ctx) }()
	if _, err = rootLock.Exec(ctx, "SELECT id FROM library_root WHERE id=$1 FOR UPDATE", root); err != nil {
		t.Fatal(err)
	}
	if _, err = rootLock.Exec(ctx, "SELECT id FROM media_unit WHERE id=$1 FOR UPDATE", unit); err != nil {
		t.Fatal(err)
	}
	expiredReceipt := make(chan *subscription.EvaluationReport, 1)
	expiredErrors := make(chan error, 1)
	var rootWorker sync.WaitGroup
	rootWorker.Add(1)
	defer func() { cancel(); _ = rootLock.Rollback(context.Background()); rootWorker.Wait() }()
	go func() {
		defer rootWorker.Done()
		report, err := evaluator.EvaluateWantedItem(ctx, wanted, policy)
		expiredReceipt <- report
		expiredErrors <- err
	}()
	rootDeadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if err = pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%LockAcquisitionRoot%')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(rootDeadline) {
			t.Fatal("projection did not wait for the root lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err = rootLock.Exec(ctx, "UPDATE unit_fact SET expires_at=clock_timestamp() WHERE id=$1", fact); err != nil {
		t.Fatal(err)
	}
	if err = rootLock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	expired := <-expiredReceipt
	if err = <-expiredErrors; err != nil || expired.IsSatisfied || expired.WaitReason != "quality_unknown" || expired.FactStage != nil {
		t.Fatal("lock-wait expiry still wrote satisfied", expired, err)
	}
	// Restore evidence by appending a new valid fact, never resurrecting the old observation.
	fact = uuid.New()
	if _, err = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: fact, UnitID: unit, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: file, Valid: true}, RootID: pgtype.UUID{Bytes: root, Valid: true}, QualityProfile: []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v2"}`)}); err != nil {
		t.Fatal(err)
	}
	evaluate("", true)
	before, err := q.GetWantedItemByID(ctx, wanted)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Failed commit writes neither a new stage nor a new satisfied/refresh projection.
	exec("CREATE FUNCTION fail_projection() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'secret-fixture'; END $$; CREATE TRIGGER fail_projection BEFORE UPDATE ON wanted_item FOR EACH ROW EXECUTE FUNCTION fail_projection()")
	_, err = evaluator.EvaluateWantedItem(ctx, wanted, policy)
	if !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("projection fault not sanitized", err)
	}
	after, err := q.GetWantedItemByID(ctx, wanted)
	if err != nil || !after.UpdatedAt.Time.Equal(before.UpdatedAt.Time) {
		t.Fatal("failed projection partially committed", err)
	}
	exec("DROP TRIGGER fail_projection ON wanted_item")
	// Block the policy lock after preflight; pause the subscription before releasing it.
	lock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Rollback(ctx) }()
	if _, err = lock.Exec(ctx, "SELECT id FROM policy WHERE id='default' FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	defer func() { cancel(); _ = lock.Rollback(context.Background()); wg.Wait() }()
	go func() { defer wg.Done(); _, err := evaluator.EvaluateWantedItem(ctx, wanted, policy); result <- err }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if err = pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%LockSearchPolicy%')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("projection did not reach controlled policy lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err = q.UpdateSubscriptionState(ctx, db.UpdateSubscriptionStateParams{ID: created.Id, State: "paused"}); err != nil {
		t.Fatal(err)
	}
	if err = lock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, ports.ErrWantedProjectionChanged) {
		t.Fatal("old intent wrote projection", err)
	}
	after, err = q.GetWantedItemByID(ctx, wanted)
	if err != nil || !after.UpdatedAt.Time.Equal(before.UpdatedAt.Time) {
		t.Fatal("old intent changed projection", err)
	}
	exec("UPDATE unit_fact SET revoked_at=now() WHERE id=$1", fact)
	evaluate("quality_unknown", false)
	exec("UPDATE library_file SET retired_at=now() WHERE id=$1", file)
	evaluate("subscription_paused", false)
	latest, err := q.GetSubscriptionByID(ctx, created.Id)
	if err != nil {
		t.Fatal(err)
	}
	after, err = q.GetWantedItemByID(ctx, wanted)
	if err != nil || after.IntentVersion != latest.IntentVersion || after.CurrentFactStage.Valid {
		t.Fatal("new intent did not reset old stage", after, err)
	}
	policy.Revision = 99
	before = after
	_, err = evaluator.EvaluateWantedItem(ctx, wanted, policy)
	if !errors.Is(err, ports.ErrWantedProjectionChanged) {
		t.Fatal("wrong immutable revision allowed", err)
	}
	after, err = q.GetWantedItemByID(ctx, wanted)
	if err != nil || !after.UpdatedAt.Time.Equal(before.UpdatedAt.Time) {
		t.Fatal("wrong revision wrote projection")
	}
}
