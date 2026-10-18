package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestVersionedBindings(t *testing.T) {
	a, _, token, root, _, _, router, _ := setupOperationHTTP(t)
	ctx := context.Background()
	q := a.Queries()
	pool := a.DBPool()
	media, unit, idx, dl := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, e := q.CreateMedia(ctx, db.CreateMediaParams{ID: media, Title: "Bindings Movie", MediaType: "movie"}); e != nil {
		t.Fatal(e)
	}
	if _, e := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unit, MediaID: media, UnitKey: "movie"}); e != nil {
		t.Fatal(e)
	}
	if _, e := q.CreateIndexer(ctx, db.CreateIndexerParams{ID: idx, Name: "Bindings Indexer", Url: "https://fixture.invalid", ApiKeyEncrypted: []byte("fixture"), Categories: []byte(`[]`)}); e != nil {
		t.Fatal(e)
	}
	if _, e := q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: dl, Name: "Bindings Downloader", ClientType: "qbittorrent", Url: "https://fixture.invalid", PasswordEncrypted: []byte("fixture"), RemotePathMap: []byte(`[]`)}); e != nil {
		t.Fatal(e)
	}
	call := func(method, path, key string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		return rec
	}
	create := call(http.MethodPost, "/api/v1/subscriptions", "", httpapi.CreateSubscriptionRequest{MediaId: &media, RootId: &root})
	if create.Code != 201 {
		t.Fatal(create.Code, create.Body.String())
	}
	var created httpapi.SubscriptionDetails
	if e := json.Unmarshal(create.Body.Bytes(), &created); e != nil {
		t.Fatal(e)
	}
	paused, e := q.UpdateSubscriptionState(ctx, db.UpdateSubscriptionStateParams{ID: created.Id, State: "paused"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = q.SetWantedItemSkipped(ctx, db.SetWantedItemSkippedParams{SubscriptionID: created.Id, UnitID: unit, IsSkipped: true}); e != nil {
		t.Fatal(e)
	}
	path := "/api/v1/subscriptions/" + created.Id.String() + "/bindings"
	body := httpapi.SubscriptionBindingsRequest{ExpectedVersion: paused.IntentVersion, IndexerIds: []uuid.UUID{idx}, DownloaderId: &dl}
	// Success audit fault is late in the UoW: bindings and wanted writes must roll back too.
	if _, e = pool.Exec(ctx, `CREATE FUNCTION fail_binding_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='subscription.bindings_configured' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_binding_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fail_binding_audit()`); e != nil {
		t.Fatal(e)
	}
	failed := call(http.MethodPut, path, "binding-once", body)
	if failed.Code != 503 {
		t.Fatal("fault falsely acknowledged", failed.Code, failed.Body.String())
	}
	after, e := q.GetSubscriptionByID(ctx, created.Id)
	if e != nil || after.IntentVersion != paused.IntentVersion || after.DownloaderID.Valid {
		t.Fatal("partial bindings leaked", after, e)
	}
	var commands int
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM idempotency_record WHERE idempotency_key='binding-once'").Scan(&commands); e != nil || commands != 0 {
		t.Fatal("false replay result", commands, e)
	}
	if _, e = pool.Exec(ctx, "DROP TRIGGER fail_binding_audit ON audit_log"); e != nil {
		t.Fatal(e)
	}
	// Ten HTTP contenders with the same request all get one stable receipt.
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 10)
	for range 10 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- call(http.MethodPut, path, "binding-once", body) }()
	}
	wg.Wait()
	close(results)
	for rec := range results {
		var info httpapi.SubscriptionBindingsDetails
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &info) != nil || info.IntentVersion != paused.IntentVersion+1 || info.State != "paused" {
			t.Fatal("unstable replay", rec.Code, rec.Body.String())
		}
	}
	after, e = q.GetSubscriptionByID(ctx, created.Id)
	if e != nil || after.IntentVersion != paused.IntentVersion+1 || after.ScopeType != paused.ScopeType || after.State != "paused" {
		t.Fatal("bindings changed unrelated intent", after, e)
	}
	wanteds, e := q.ListWantedItemsForSubscription(ctx, created.Id)
	if e != nil || len(wanteds) != 1 || !wanteds[0].IsSkipped || wanteds[0].IntentVersion != after.IntentVersion {
		t.Fatal("wanted skip/version not preserved", wanteds, e)
	}
	var events, audits int
	if e = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM outbox_event WHERE event_type='subscription.bindings_configured' AND aggregate_id=$1),(SELECT count(*) FROM audit_log WHERE action='subscription.bindings_configured' AND target_id=$1)`, created.Id.String()).Scan(&events, &audits); e != nil || events != 1 || audits != 1 {
		t.Fatal("duplicate success", events, audits, e)
	}
	if r := call(http.MethodPut, path, "stale-version", body); r.Code != 409 {
		t.Fatal("stale intent accepted", r.Code, r.Body.String())
	}
	changed := body
	changed.IndexerIds = []uuid.UUID{}
	if r := call(http.MethodPut, path, "binding-once", changed); r.Code != 409 {
		t.Fatal("command conflict accepted", r.Code, r.Body.String())
	}
	service := subscription.NewBindingService(postgres.NewSubscriptionBindingStore(pool, idgen.New()))
	foreign := &ports.Actor{ID: uuid.NewString(), Role: ports.RoleMember}
	if _, e = service.Configure(ctx, foreign, created.Id, ports.SubscriptionBindingsCommand{ExpectedVersion: after.IntentVersion, IndexerIDs: []uuid.UUID{}}, "binding-once"); e != ports.ErrForbidden {
		t.Fatal("foreign object/replay allowed", e)
	}
	changed.ExpectedVersion = after.IntentVersion
	changed.IndexerIds = []uuid.UUID{uuid.New()}
	if r := call(http.MethodPut, path, "missing-endpoint", changed); r.Code != 400 {
		t.Fatal("unknown endpoint allowed", r.Code, r.Body.String())
	}
	changed.IndexerIds = []uuid.UUID{}
	changed.DownloaderId = nil
	if r := call(http.MethodPut, path, "clear-bindings", changed); r.Code != 200 {
		t.Fatal("explicit clear failed", r.Code, r.Body.String())
	}
	cleared, e := q.GetSubscriptionByID(ctx, created.Id)
	if e != nil || cleared.DownloaderID.Valid || string(cleared.IndexerIds) != "[]" || cleared.State != "paused" {
		t.Fatal("clear changed state", fmt.Sprint(cleared), e)
	}
}
