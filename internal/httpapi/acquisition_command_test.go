package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAcquireMovieHTTPCommandCookieReplayAndErrors(t *testing.T) {
	ctx := context.Background()
	a, _, _ := setupAuthHTTP(t)
	q := a.Queries()
	as := auth.NewService(postgres.NewAuthenticationStore(a.DBPool()), crypto.AuthenticationSecrets{}, a.Clock(), a.IDGen())
	user, session, e := as.SetupAdmin(ctx, "acquire-http", "AcquisitionFixture123!")
	if e != nil {
		t.Fatal(e)
	}
	acq := acquisition.NewService(postgres.NewAcquisitionStore(a.DBPool(), a.IDGen(), a.Clock(), acquisition.SelectMovieRound))
	handler := httpapi.NewServerHandler(a, as, nil, acq, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	server := httptest.NewServer(httpapi.NewRouter(a, handler, as, nil))
	defer server.Close()
	root, media, unit, idx, candidate := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, e = q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: root, Path: t.TempDir(), MediaType: "movie", Name: "HTTP Movies"}); e != nil {
		t.Fatal(e)
	}
	if _, e = a.DBPool().Exec(ctx, "UPDATE library_root SET baseline_completed=true WHERE id=$1", root); e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateMedia(ctx, db.CreateMediaParams{ID: media, Title: "Fight Club", MediaType: "movie"}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unit, MediaID: media, UnitKey: "movie"}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateIndexer(ctx, db.CreateIndexerParams{ID: idx, Name: "HTTP source", Url: "https://example.test", ApiKeyEncrypted: []byte("cipher"), Categories: []byte("[]")}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{ID: candidate, IndexerID: idx, SourceConfigVersion: 1, Guid: "http-command", Title: "Fight.Club.1999.1080p.mkv", LinkEncrypted: []byte("cipher"), QualityProfile: []byte("{}")}); e != nil {
		t.Fatal(e)
	}
	send := func(key, origin, token string, id uuid.UUID) (int, string, string) {
		body, _ := json.Marshal(map[string]any{"unit_id": id, "root_id": root, "candidate_id": candidate})
		r, e := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/movies/acquire", bytes.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		r.Header.Set("Idempotency-Key", key)
		if token == "" {
			r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: session})
		} else {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer func() {
			if closeErr := response.Body.Close(); closeErr != nil {
				t.Error(closeErr)
			}
		}()
		raw, e := io.ReadAll(response.Body)
		if e != nil {
			t.Fatal(e)
		}
		return response.StatusCode, string(raw), response.Header.Get("Content-Type")
	}
	code, original, _ := send("http-once", server.URL, "", unit)
	if code != 202 {
		t.Fatal(code, original)
	}
	if _, e = a.DBPool().Exec(ctx, "UPDATE library_root SET health='offline' WHERE id=$1", root); e != nil {
		t.Fatal(e)
	}
	code, recovered, _ := send("http-once", server.URL, "", unit)
	if code != 202 || original != recovered {
		t.Fatal(code, recovered)
	}
	if code, body, content := send("http-once", server.URL, "", uuid.New()); code != 409 || !strings.Contains(content, "application/problem+json") || !strings.Contains(body, "IDEMPOTENCY_CONFLICT") {
		t.Fatal(code, body, content)
	}
	if code, body, _ := send("http-once", "https://foreign.example", "", unit); code != 403 {
		t.Fatal(code, body)
	}
	token := "synthetic-acquire-api"
	if _, e = a.DBPool().Exec(ctx, "INSERT INTO api_token(id,user_id,name,token_hash,scopes) VALUES($1,$2,'fixture',$3,'[\"read\"]')", uuid.New(), user.ID, crypto.HashToken(token)); e != nil {
		t.Fatal(e)
	}
	if code, body, _ := send("http-once", server.URL, token, unit); code != 403 {
		t.Fatal(code, body)
	}
	if _, e = a.DBPool().Exec(ctx, "UPDATE library_root SET health='online' WHERE id=$1", root); e != nil {
		t.Fatal(e)
	}
	secondMedia, secondUnit := uuid.New(), uuid.New()
	if _, e = q.CreateMedia(ctx, db.CreateMediaParams{ID: secondMedia, Title: "Fight Club", MediaType: "movie"}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: secondUnit, MediaID: secondMedia, UnitKey: "movie"}); e != nil {
		t.Fatal(e)
	}
	if _, e = a.DBPool().Exec(ctx, `CREATE FUNCTION http_acquisition_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic-hidden-database-secret'; END $$; CREATE TRIGGER http_acquisition_fault BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION http_acquisition_fault()`); e != nil {
		t.Fatal(e)
	}
	if code, body, content := send("http-next", server.URL, "", secondUnit); code != 503 || strings.Contains(body, "synthetic-hidden") || !strings.Contains(content, "application/problem+json") {
		t.Fatal(code, body, content)
	}
	var reservations, jobs, receipts int
	if e = a.DBPool().QueryRow(ctx, "SELECT (SELECT count(*) FROM unit_reservation),(SELECT count(*) FROM job),(SELECT count(*) FROM idempotency_record WHERE actor_id=$1)", user.ID.String()).Scan(&reservations, &jobs, &receipts); e != nil || reservations != 1 || jobs != 1 || receipts != 1 {
		t.Fatalf("partial effects %d/%d/%d %v", reservations, jobs, receipts, e)
	}
	if _, e = a.DBPool().Exec(ctx, "DROP TRIGGER http_acquisition_fault ON idempotency_record"); e != nil {
		t.Fatal(e)
	}
	if code, body, _ := send("http-next", server.URL, "", secondUnit); code != 202 {
		t.Fatal(code, body)
	}
}
