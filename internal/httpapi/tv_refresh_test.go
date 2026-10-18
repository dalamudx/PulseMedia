package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestTVRefreshHTTPRealProviderReceiptAndScope(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	var incomplete atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if (r.URL.Path != "/tv/100/season/1" && r.URL.Path != "/tv/100/season/0") || r.URL.Query().Get("api_key") != "fixture-episode-key" {
			w.WriteHeader(403)
			return
		}
		if r.URL.Path == "/tv/100/season/0" {
			_, _ = io.WriteString(w, `{"season_number":0,"episodes":[{"id":1003,"season_number":0,"episode_number":1,"air_date":null}]}`)
			return
		}
		if incomplete.Load() {
			_, _ = io.WriteString(w, `{"season_number":1,"episodes":[]}`)
			return
		}
		_, _ = io.WriteString(w, `{"season_number":1,"episodes":[{"id":1001,"season_number":1,"episode_number":1,"air_date":"2099-01-01"},{"id":1002,"season_number":1,"episode_number":2,"air_date":null}]}`)
	}))
	defer provider.Close()
	a, admin, member, _, _, _, router, _ := setupOperationHTTP(t, func(handler *httpapi.ServerHandler, a *app.App) {
		handler.SetTVRefreshService(subscription.NewTVRefreshService(postgres.NewTVRefreshStore(a.DBPool(), idgen.New()), tmdb.NewClient("fixture-episode-key", tmdb.WithBaseURL(provider.URL))))
	})
	root, m, unit := uuid.New(), uuid.New(), uuid.New()
	if _, e := a.Queries().CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: root, Path: t.TempDir(), Name: "TV refresh", MediaType: "tv"}); e != nil {
		t.Fatal(e)
	}
	if _, e := a.Queries().CreateMedia(ctx, db.CreateMediaParams{ID: m, Title: "HTTP Refresh", MediaType: "tv"}); e != nil {
		t.Fatal(e)
	}
	if _, e := a.Queries().CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: uuid.New(), MediaID: m, Namespace: "tmdb.tv", ExternalID: "100"}); e != nil {
		t.Fatal(e)
	}
	if _, e := a.Queries().CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unit, MediaID: m, UnitKey: "S01E01"}); e != nil {
		t.Fatal(e)
	}
	call := func(token, method, path, key string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, r)
		return out
	}
	created := call(admin, "POST", "/api/v1/subscriptions", "", httpapi.CreateSubscriptionRequest{MediaId: &m, RootId: &root})
	var sub httpapi.SubscriptionDetails
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &sub) != nil {
		t.Fatal(created.Code)
	}
	adminUser, err := a.Queries().GetUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: adminUser.ID.String(), Role: ports.RoleAdmin}
	sourceStore := postgres.NewEpisodeSourceStore(postgres.NewTVRefreshStore(a.DBPool(), idgen.New()))
	readSource := func() *ports.EpisodeSourceContext {
		t.Helper()
		result, err := sourceStore.ReadEpisodeSourceContext(ctx, actor, sub.Id)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if legacy := readSource(); len(legacy.CompleteSeasons) != 0 {
		t.Fatal("legacy unit rows minted complete-season evidence", legacy)
	}
	path := "/api/v1/subscriptions/" + sub.Id.String() + "/metadata/refresh"
	body := map[string]any{"expected_version": sub.IntentVersion, "seasons": []int{1}}
	if memberReply := call(member, "POST", path, "refresh", body); memberReply.Code != 403 || calls.Load() != 0 {
		t.Fatal("member request reached provider", memberReply.Code, calls.Load())
	}
	first := call(admin, "POST", path, "refresh", body)
	var receipt httpapi.TVRefreshReceipt
	if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &receipt) != nil || receipt.ObservedEpisodes != 2 || first.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("missing real refresh", first.Code, receipt)
	}
	repeat := call(admin, "POST", path, "refresh", body)
	if repeat.Code != 200 || calls.Load() != 1 || !bytes.Equal(first.Body.Bytes(), repeat.Body.Bytes()) {
		t.Fatal("replayed network/unstable receipt", repeat.Code, calls.Load())
	}
	foreignKey := call(member, "POST", path, "refresh", body)
	if foreignKey.Code != 403 {
		t.Fatal("member replay elevated", foreignKey.Code)
	}
	var downloads int
	if e := a.DBPool().QueryRow(ctx, "SELECT count(*) FROM download_task").Scan(&downloads); e != nil || downloads != 0 {
		t.Fatal("metadata refreshed by fake download", downloads, e)
	}
	units, e := a.Queries().LockScopeMediaUnits(ctx, m)
	if e != nil || len(units) != 2 {
		t.Fatal(units, e)
	}
	source := readSource()
	if len(source.CompleteSeasons) != 1 || source.CompleteSeasons[0].MediaVersion != receipt.MediaVersion || source.CompleteSeasons[0].SourceTMDBID != 100 || len(source.CompleteSeasons[0].Units) != 2 || source.CompleteSeasons[0].Units[0].UnitID != unit || source.CompleteSeasons[0].Units[0].EpisodeNumber != 1 || source.CompleteSeasons[0].Units[1].EpisodeNumber != 2 {
		t.Fatal("complete provider set not frozen", source)
	}
	proofs, err := a.Queries().ListTVSeasonMetadataSnapshots(ctx, db.ListTVSeasonMetadataSnapshotsParams{MediaID: m, MediaVersion: receipt.MediaVersion})
	if err != nil || len(proofs) != 1 {
		t.Fatal("same-key replay duplicated or lost proof", proofs, err)
	}
	original := append([]byte(nil), proofs[0].Units...)
	if _, err = a.DBPool().Exec(ctx, `CREATE FUNCTION reject_season_proof_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.idempotency_key='proof-rollback' THEN RAISE EXCEPTION 'injected receipt tail failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_season_proof_receipt BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION reject_season_proof_receipt()`); err != nil {
		t.Fatal(err)
	}
	if failed := call(admin, "POST", path, "proof-rollback", body); failed.Code != 503 {
		t.Fatal("receipt-tail failure accepted", failed.Code)
	}
	if after := readSource(); after.InputStamp != source.InputStamp || after.MediaVersion != source.MediaVersion || len(after.CompleteSeasons) != 1 {
		t.Fatal("late failure leaked metadata/proof", after)
	}
	var historyCount int
	if err = a.DBPool().QueryRow(ctx, "SELECT count(*) FROM tv_season_metadata_snapshot WHERE media_id=$1", m).Scan(&historyCount); err != nil || historyCount != 1 {
		t.Fatal("rollback leaked proof history", historyCount, err)
	}
	incomplete.Store(true)
	if failed := call(admin, "POST", path, "incomplete-proof", body); failed.Code != 503 {
		t.Fatal("incomplete response accepted", failed.Code)
	}
	if after := readSource(); after.InputStamp != source.InputStamp {
		t.Fatal("partial response minted proof", after)
	}
	incomplete.Store(false)
	if _, err = a.DBPool().Exec(ctx, "UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=$1", unit); err != nil {
		t.Fatal(err)
	}
	if after := readSource(); len(after.CompleteSeasons) != 0 || after.InputStamp == source.InputStamp {
		t.Fatal("stale unit version retained completeness", after)
	}
	if refreshed := call(admin, "POST", path, "refresh-next-proof", body); refreshed.Code != 200 {
		t.Fatal("fresh proof failed", refreshed.Code)
	}
	if after := readSource(); len(after.CompleteSeasons) != 1 || after.MediaVersion <= source.MediaVersion {
		t.Fatal("fresh full set did not recover proof", after)
	}
	oldProofs, err := a.Queries().ListTVSeasonMetadataSnapshots(ctx, db.ListTVSeasonMetadataSnapshotsParams{MediaID: m, MediaVersion: receipt.MediaVersion})
	if err != nil || len(oldProofs) != 1 || !bytes.Equal(original, oldProofs[0].Units) {
		t.Fatal("historical proof rewritten", oldProofs, err)
	}
	if reply := call(admin, "POST", path, "special-proof", map[string]any{"expected_version": sub.IntentVersion, "seasons": []int{0}}); reply.Code != 200 {
		t.Fatal("special metadata refresh failed", reply.Code)
	}
	if after := readSource(); len(after.CompleteSeasons) != 0 {
		t.Fatal("special or prior-version season gained completeness", after)
	}
	if err = a.DBPool().QueryRow(ctx, "SELECT count(*) FROM tv_season_metadata_snapshot WHERE media_id=$1 AND season_number=0", m).Scan(&historyCount); err != nil || historyCount != 0 {
		t.Fatal("special season minted proof history", historyCount, err)
	}
}
