package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSubscriptionBindingsPersistAndReconfigureAtomically(t *testing.T) {
	a, adminToken, memberToken, root, _, _, router, _ := setupOperationHTTP(t)
	ctx := context.Background()
	q := a.Queries()
	pool := a.DBPool()
	movie, unit, idx, dl := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	enc, _ := crypto.Encrypt(make([]byte, 32), []byte("fixture"))
	if _, err := q.CreateMedia(ctx, db.CreateMediaParams{ID: movie, Title: "Public Movie", MediaType: "movie"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unit, MediaID: movie, UnitKey: "movie"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateIndexer(ctx, db.CreateIndexerParams{ID: idx, Name: "Public Indexer", Url: "https://fixture.invalid", ApiKeyEncrypted: enc, Categories: []byte(`[]`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: dl, Name: "Public Downloader", ClientType: "qbittorrent", Url: "https://fixture.invalid", PasswordEncrypted: enc, RemotePathMap: []byte(`[]`)}); err != nil {
		t.Fatal(err)
	}
	// Non-credential edits must preserve the current sealed value in one SQL write.
	edit, _ := json.Marshal(httpapi.CreateIndexerRequest{Name: "Public Indexer", Url: "https://fixture.invalid", ApiKey: ""})
	ereq := httptest.NewRequest(http.MethodPut, "/api/v1/indexers/"+idx.String(), bytes.NewReader(edit))
	ereq.Header.Set("Authorization", "Bearer "+adminToken)
	ereq.Header.Set("Content-Type", "application/json")
	ered := httptest.NewRecorder()
	router.ServeHTTP(ered, ereq)
	sealed, err := q.GetIndexerByID(ctx, idx)
	if ered.Code != 200 || err != nil || !bytes.Equal(sealed.ApiKeyEncrypted, enc) || string(sealed.Categories) != "[]" {
		t.Fatal("blank edit destroyed encrypted credentials/categories", ered.Code, err)
	}
	ids := []uuid.UUID{idx}
	body := httpapi.CreateSubscriptionRequest{MediaId: &movie, RootId: &root, IndexerIds: &ids, DownloaderId: &dl}
	call := func(body httpapi.CreateSubscriptionRequest) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+memberToken)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	rec := call(body)
	if rec.Code != 201 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var first httpapi.SubscriptionDetails
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.IndexerIds == nil || len(*first.IndexerIds) != 1 || (*first.IndexerIds)[0] != idx || first.DownloaderId == nil || *first.DownloaderId != dl {
		t.Fatal("public binding was dropped", first)
	}
	list := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions", nil)
	list.Header.Set("Authorization", "Bearer "+memberToken)
	listed := httptest.NewRecorder()
	router.ServeHTTP(listed, list)
	var collection struct {
		Items []httpapi.SubscriptionDetails `json:"items"`
	}
	if listed.Code != 200 || json.Unmarshal(listed.Body.Bytes(), &collection) != nil {
		t.Fatal("list failed", listed.Code)
	}
	found := false
	for _, item := range collection.Items {
		if item.Id == first.Id {
			found = item.IndexerIds != nil && len(*item.IndexerIds) == 1 && (*item.IndexerIds)[0] == idx && item.DownloaderId != nil && *item.DownloaderId == dl
		}
	}
	if !found {
		t.Fatal("refresh dropped endpoint bindings")
	}
	before, _ := q.GetSubscriptionByID(ctx, first.Id)
	// A failed wanted write must not leak a new binding/intent/success event or audit.
	if _, err := pool.Exec(ctx, "CREATE FUNCTION fail_scope() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$; CREATE TRIGGER fail_scope BEFORE INSERT OR UPDATE ON wanted_item FOR EACH ROW EXECUTE FUNCTION fail_scope()"); err != nil {
		t.Fatal(err)
	}
	empty := []uuid.UUID{}
	body.IndexerIds = &empty
	rec = call(body)
	if rec.Code != http.StatusServiceUnavailable || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"CREATE_UNAVAILABLE"`)) || bytes.Contains(rec.Body.Bytes(), []byte("injected")) {
		t.Fatal("failed wanted write must return a sanitized unavailable response", rec.Code, rec.Body.String())
	}
	after, _ := q.GetSubscriptionByID(ctx, first.Id)
	if after.IntentVersion != before.IntentVersion || string(after.IndexerIds) != string(before.IndexerIds) {
		t.Fatal("scope failure leaked edit", before, after)
	}
	var events, audits int
	if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM outbox_event WHERE aggregate_id=$1),(SELECT count(*) FROM audit_log WHERE target_id=$1)", first.Id.String()).Scan(&events, &audits); err != nil || events != 1 || audits != 1 {
		t.Fatal("partial success records", events, audits, err)
	}
	if _, err := pool.Exec(ctx, "DROP TRIGGER fail_scope ON wanted_item"); err != nil {
		t.Fatal(err)
	}
	rec = call(body)
	if rec.Code != 201 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var updated httpapi.SubscriptionDetails
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Id != first.Id || updated.IntentVersion != first.IntentVersion+1 || updated.IndexerIds == nil || len(*updated.IndexerIds) != 0 {
		t.Fatal("reconfigure was not atomic", updated)
	}
	wanted, err := q.ListWantedItemsForSubscription(ctx, updated.Id)
	if err != nil || len(wanted) != 1 || wanted[0].IntentVersion != int64(updated.IntentVersion) || wanted[0].IsSkipped {
		t.Fatal("wanted did not follow intent", wanted, err)
	}
	get := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+first.Id.String(), nil)
	get.Header.Set("Authorization", "Bearer "+memberToken)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, get)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var detail httpapi.SubscriptionDetails
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil || detail.DownloaderId == nil || *detail.DownloaderId != dl || detail.IndexerIds == nil || len(*detail.IndexerIds) != 0 {
		t.Fatal("GET does not observe bindings", detail, err)
	}
	if detail.Title == nil || *detail.Title != "Public Movie" || detail.MediaType == nil || *detail.MediaType != "movie" || detail.RootName == nil || detail.WantedItems == nil || len(*detail.WantedItems) != 1 || (*detail.WantedItems)[0].UnitKey == nil || *(*detail.WantedItems)[0].UnitKey != "movie" {
		t.Fatal("GET lost UI metadata or unit keys", detail)
	}
	// A failed unit-detail read is an unavailable request, never an empty UI success.
	if _, err := pool.Exec(ctx, "ALTER TABLE media_unit RENAME COLUMN unit_key TO broken_for_read_probe"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/subscriptions", "/api/v1/subscriptions/" + first.Id.String()} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+memberToken)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, r)
		if out.Code < 500 || bytes.Contains(out.Body.Bytes(), []byte("unit_key")) {
			t.Fatal("read failure became empty success or leaked SQL", path, out.Code)
		}
	}
}
