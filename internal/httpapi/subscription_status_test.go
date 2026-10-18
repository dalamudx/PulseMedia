package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestCurrentSubscriptionStatus(t *testing.T) {
	a, _, token, root, _, candidate, router, _ := setupOperationHTTP(t)
	ctx := context.Background()
	q := a.Queries()
	pool := a.DBPool()
	if _, e := pool.Exec(ctx, "UPDATE library_root SET baseline_completed=false WHERE id=$1", root); e != nil {
		t.Fatal(e)
	}
	cand, e := q.GetCandidateByID(ctx, candidate)
	if e != nil {
		t.Fatal(e)
	}
	// Use a fresh movie target, no dependency on the operation helper's media ordering.
	media, unit := uuid.New(), uuid.New()
	if _, e = q.CreateMedia(ctx, db.CreateMediaParams{ID: media, Title: "Status Movie", MediaType: "movie"}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unit, MediaID: media, UnitKey: "movie"}); e != nil {
		t.Fatal(e)
	}
	body, _ := json.Marshal(httpapi.CreateSubscriptionRequest{MediaId: &media, RootId: &root})
	create := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions", bytes.NewReader(body))
	create.Header.Set("Authorization", "Bearer "+token)
	create.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	router.ServeHTTP(out, create)
	if out.Code != 201 {
		t.Fatal(out.Code, out.Body.String())
	}
	var sub httpapi.SubscriptionDetails
	if json.Unmarshal(out.Body.Bytes(), &sub) != nil {
		t.Fatal("create response")
	}
	wanteds, e := q.ListWantedItemsForSubscription(ctx, sub.Id)
	if e != nil || len(wanteds) != 1 {
		t.Fatal(e)
	}
	wanted := wanteds[0]
	read := func() (httpapi.SubscriptionStatusReport, int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+sub.Id.String()+"/status", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, r)
		if rr.Code == 200 && rr.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("ownership observation is cacheable")
		}
		var report httpapi.SubscriptionStatusReport
		if rr.Code == 200 && json.Unmarshal(rr.Body.Bytes(), &report) != nil {
			t.Fatal("status response")
		}
		if bytes.Contains(rr.Body.Bytes(), []byte("fixture.invalid")) {
			t.Fatal("endpoint leaked")
		}
		return report, rr.Code
	}
	check := func(reason string, satisfied, protected bool) {
		t.Helper()
		r, code := read()
		if code != 200 || len(r.Items) != 1 {
			t.Fatal("status read", code, r)
		}
		v := r.Items[0]
		got := ""
		if v.WaitReason != nil {
			got = *v.WaitReason
		}
		if got != reason || v.IsSatisfied != satisfied || v.ReservationProtected != protected {
			t.Fatalf("got %+v reason=%s", v, got)
		}
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec("UPDATE wanted_item SET is_satisfied=true,current_fact_stage='imported',next_evaluate_at=now()+interval '1 day' WHERE id=$1", wanted.ID)
	before, e := q.GetWantedItemByID(ctx, wanted.ID)
	if e != nil {
		t.Fatal(e)
	}
	check("baseline_pending", false, false)
	after, e := q.GetWantedItemByID(ctx, wanted.ID)
	if e != nil || !after.IsSatisfied || !after.NextEvaluateAt.Time.Equal(before.NextEvaluateAt.Time) || !after.UpdatedAt.Time.Equal(before.UpdatedAt.Time) {
		t.Fatal("GET changed projection", e)
	}
	exec("UPDATE library_root SET baseline_completed=true WHERE id=$1", root)
	check("no_indexers", false, false)
	// Files in another root or files without verified imported evidence do not own the target.
	other := uuid.New()
	if _, e = q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: other, Path: "/tmp/other-status-root", MediaType: "movie", Name: "Other"}); e != nil {
		t.Fatal(e)
	}
	addFile := func(rootID uuid.UUID, path, profile string) (uuid.UUID, uuid.UUID) {
		t.Helper()
		id, fact := uuid.New(), uuid.New()
		if _, e := q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: id, RootID: rootID, RelPath: path, SizeBytes: 100, MtimeNs: 1}); e != nil {
			t.Fatal(e)
		}
		if e := q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: id, UnitID: unit}); e != nil {
			t.Fatal(e)
		}
		if profile != "" {
			if _, e := q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: fact, UnitID: unit, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: id, Valid: true}, RootID: pgtype.UUID{Bytes: rootID, Valid: true}, QualityProfile: []byte(profile)}); e != nil {
				t.Fatal(e)
			}
		}
		return id, fact
	}
	addFile(other, "Other.mkv", `{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v2"}`)
	check("no_indexers", false, false)
	bad, badFact := addFile(root, "unknown.mkv", "")
	check("quality_unknown", false, false)
	_ = badFact
	// Legacy title-derived resolution has no current evidence; do not rewrite its
	// observation or turn its real file/mapping into a missing-file assertion.
	legacy, legacyFact := addFile(root, "legacy.mkv", `{"resolution":"720p","source":"HDTV","codec":"H264"}`)
	check("quality_unknown", false, false)
	var legacyProfile []byte
	var legacyPresence, legacyMappings int
	if e = pool.QueryRow(ctx, "SELECT quality_profile FROM unit_fact WHERE id=$1", legacyFact).Scan(&legacyProfile); e != nil {
		t.Fatal(e)
	}
	var legacyStored map[string]any
	if e = json.Unmarshal(legacyProfile, &legacyStored); e != nil || legacyStored["resolution"] != "720p" {
		t.Fatal("GET rewrote legacy evidence", legacyStored, e)
	}
	if e = pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM library_file WHERE id=$1 AND retired_at IS NULL),(SELECT count(*) FROM library_file_unit WHERE file_id=$1 AND retired_at IS NULL)", legacy).Scan(&legacyPresence, &legacyMappings); e != nil || legacyPresence != 1 || legacyMappings != 1 {
		t.Fatal("unknown resolution removed file/history", legacyPresence, legacyMappings, e)
	}

	good, goodFact := addFile(root, "good.mkv", `{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v2"}`)
	check("", true, false)
	exec("UPDATE library_file SET retired_at=now() WHERE id=$1", bad)
	// Use the referenced immutable revision, not an old rank or current policy pointer.
	exec("INSERT INTO policy_revision(id,policy_id,revision,rules) SELECT $1,policy_id,2,jsonb_set(rules,'{reject_terms}','[\"good\"]'::jsonb) FROM policy_revision WHERE policy_id='default' AND revision=1", uuid.New())
	exec("UPDATE subscription SET quality_policy_revision=2,intent_version=intent_version+1 WHERE id=$1", sub.Id)
	check("quality_unknown", false, false) // unversioned legacy observation still blocks a new acquisition.
	// Explicitly retire only this fixture observation to isolate the rejected-file branch.
	exec("UPDATE library_file SET retired_at=now() WHERE id=$1", legacy)
	check("file_rejected", false, false)
	exec("INSERT INTO policy_revision(id,policy_id,revision,rules) SELECT $1,policy_id,3,rules FROM policy_revision WHERE policy_id='default' AND revision=1", uuid.New())
	exec("UPDATE subscription SET quality_policy_revision=3,intent_version=intent_version+1 WHERE id=$1", sub.Id)
	check("", true, false)
	exec("UPDATE unit_fact SET revoked_at=now() WHERE id=$1", goodFact)
	check("quality_unknown", false, false)
	if _, e = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: unit, Stage: "confirmed", SourceFileID: pgtype.UUID{Bytes: good, Valid: true}, RootID: pgtype.UUID{Bytes: root, Valid: true}, QualityProfile: []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v2"}`)}); e != nil {
		t.Fatal(e)
	}
	check("quality_unknown", false, false) // confirmed enhances imported, never replaces a revoked import.
	exec("UPDATE library_file SET retired_at=now() WHERE id=ANY($1::uuid[])", []uuid.UUID{bad, good})
	check("no_indexers", false, false)
	// Unobserved task + an expired TTL still protects the actual target reservation.
	operation, reservation, client, task := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, e = q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: client, Name: "Status Downloader", ClientType: "qbittorrent", Url: "https://fixture.invalid", PasswordEncrypted: []byte("sealed"), RemotePathMap: []byte(`[]`)}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateUnitReservation(ctx, db.CreateUnitReservationParams{ID: reservation, UnitID: unit, RootID: root, Purpose: "manual_acquisition", CandidateID: cand.ID, OperationID: operation, ExpiresAt: pgtype.Timestamptz{Time: before.CreatedAt.Time, Valid: true}}); e != nil {
		t.Fatal(e)
	}
	check("reservation_unknown", false, true)
	if _, e = q.CreateDownloadTask(ctx, db.CreateDownloadTaskParams{ID: task, OperationID: operation, ClientID: client, CandidateID: cand.ID, State: "tracking", SavePath: "/tmp/not-exposed"}); e != nil {
		t.Fatal(e)
	}
	check("reservation_active", false, true)
	r, _ := read()
	if r.Items[0].CurrentFactStage != nil {
		t.Fatal("reservation fabricated submitted fact")
	}
	if _, e = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: unit, Stage: "submitted", SourceTaskID: pgtype.UUID{Bytes: task, Valid: true}, RootID: pgtype.UUID{Bytes: root, Valid: true}, QualityProfile: []byte(`{}`), ExpiresAt: pgtype.Timestamptz{Time: before.CreatedAt.Time, Valid: true}}); e != nil {
		t.Fatal(e)
	}
	r, _ = read()
	if r.Items[0].CurrentFactStage != nil {
		t.Fatal("expired observation fabricated stage")
	}
	exec("UPDATE unit_fact SET expires_at=now()+interval '1 day' WHERE source_task_id=$1", task)
	r, _ = read()
	if r.Items[0].CurrentFactStage == nil || string(*r.Items[0].CurrentFactStage) != "submitted" {
		t.Fatal("real submitted fact lost")
	}
	exec("UPDATE download_task SET state='unknown' WHERE id=$1", task)
	check("reservation_unknown", false, true)
	exec("UPDATE library_root SET health='offline' WHERE id=$1", root)
	check("root_offline", false, true)
	exec("UPDATE library_root SET health='online',generation=generation+1 WHERE id=$1", root)
	check("baseline_pending", false, true)
	exec("UPDATE library_root SET baseline_completed=true WHERE id=$1", root) // explicit validated new-generation baseline fixture
	beforeSnapshot, _ := read()
	// Hold the file read until after this snapshot has read the root; mutate root concurrently.
	lock, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = lock.Rollback(ctx) }()
	if _, e = lock.Exec(ctx, "LOCK library_file IN ACCESS EXCLUSIVE MODE"); e != nil {
		t.Fatal(e)
	}
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := make(chan *httptest.ResponseRecorder, 1)
	var worker sync.WaitGroup
	worker.Add(1)
	defer func() { cancel(); _ = lock.Rollback(ctx); worker.Wait() }()
	go func() {
		defer worker.Done()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+sub.Id.String()+"/status", nil).WithContext(requestCtx)
		r.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, r)
		result <- out
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var blocked bool
		if e = pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND query LIKE '%ListSubscriptionStatusFiles%' AND wait_event_type='Lock')").Scan(&blocked); e != nil {
			t.Fatal(e)
		}
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("status read did not reach the controlled lock")
		}
		time.Sleep(time.Millisecond * 5)
	}
	exec("UPDATE library_root SET health='offline',generation=generation+1 WHERE id=$1", root)
	if e = lock.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	response := <-result
	var coherent httpapi.SubscriptionStatusReport
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &coherent) != nil || coherent.Items[0].WaitReason == nil || *coherent.Items[0].WaitReason != "reservation_unknown" {
		t.Fatal("status mixed root versions across queries", response.Code)
	}
	if coherent.RootGeneration != beforeSnapshot.RootGeneration {
		t.Fatal("snapshot mixed root generation")
	}
	check("root_offline", false, true)
	exec("UPDATE library_root SET health='online',baseline_completed=true WHERE id=$1", root) // complete-baseline fixture after generation changed
	exec("UPDATE unit_reservation SET state='released' WHERE id=$1", reservation)
	check("no_indexers", false, false)
	exec("UPDATE subscription SET indexer_ids=jsonb_build_array($2::text),downloader_id=$3,intent_version=intent_version+1 WHERE id=$1", sub.Id, cand.IndexerID.String(), client)
	check("missing", false, false)
	exec("UPDATE indexer SET rate_limit_until=now()+interval '2 minutes' WHERE id=$1", cand.IndexerID)
	check("endpoint_backoff", false, false)
	budget, _ := read()
	if budget.EndpointRetryAt == nil || !budget.EndpointRetryAt.After(budget.ObservedAt) {
		t.Fatal("missing real endpoint retry budget")
	}
	exec("UPDATE indexer SET health='offline' WHERE id=$1", cand.IndexerID)
	check("endpoints_offline", false, false)
	exec("UPDATE indexer SET health='online',rate_limit_until=NULL,next_query_at=now() WHERE id=$1", cand.IndexerID)
	check("missing", false, false)
	// Object permissions are checked before reading facts, even for known IDs.
	exec("UPDATE subscription SET user_id=(SELECT id FROM app_user WHERE id<>$2 ORDER BY id LIMIT 1) WHERE id=$1", sub.Id, sub.UserId)
	_, code := read()
	if code != 403 {
		t.Fatal("foreign object allowed", code)
	}
	exec("UPDATE subscription SET user_id=$2 WHERE id=$1", sub.Id, sub.UserId)
	exec("ALTER TABLE unit_fact RENAME TO broken_status_fact")
	_, code = read()
	if code != 503 {
		t.Fatal("read fault became success", code)
	}
}
