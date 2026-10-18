package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOwnTokenManagementHTTPPG(t *testing.T) {
	ctx := context.Background()
	a, _, _ := setupAuthHTTP(t)
	as := auth.NewService(postgres.NewAuthenticationStore(a.DBPool()), crypto.AuthenticationSecrets{}, a.Clock(), a.IDGen())
	_, session, e := as.SetupAdmin(ctx, "owned-token-manager", "OwnedTokenManagementFixture123!")
	if e != nil {
		t.Fatal(e)
	}
	svc := auth.NewTokenManagementService(postgres.NewTokenManagementStore(a.DBPool(), a.IDGen()), crypto.ReadTokenIssuer{}, a.Clock())
	h := httpapi.NewServerHandler(a, as, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetTokenManagementService(svc)
	router := httpapi.NewRouter(a, h, as, nil)
	request := func(credential, method, path, key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+credential)
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	type result struct {
		ports.TokenCommandResult
		Secret string `json:"secret"`
	}
	var results [8]result
	var failures [8]string
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := request(session, "POST", "/api/v1/auth/tokens", "own-create", `{"name":"owned read client"}`)
			if w.Code != 200 {
				failures[i] = fmt.Sprintf("status %d", w.Code)
				return
			}
			if e := json.Unmarshal(w.Body.Bytes(), &results[i]); e != nil {
				failures[i] = "invalid receipt"
			}
		}(i)
	}
	wg.Wait()
	var plain string
	var id uuid.UUID
	fresh := 0
	for i, r := range results {
		if failures[i] != "" {
			t.Fatalf("create[%d]: %s", i, failures[i])
		}
		if id == uuid.Nil {
			id = r.Item.ID
		}
		if id == uuid.Nil || r.Item.ID != id || len(r.Item.Scopes) != 1 || r.Item.Scopes[0] != "read" {
			t.Fatal("inconsistent same-key metadata")
		}
		if !r.Replayed {
			fresh++
			plain = r.Secret
		} else if r.Secret != "" {
			t.Fatal("replayed plaintext")
		}
	}
	if fresh != 1 || plain == "" {
		t.Fatal("creation did not return exactly one secret")
	}
	actor, e := as.AuthenticateToken(ctx, plain)
	if e != nil || actor.Type != ports.ActorTypeUser || actor.Scopes == nil {
		t.Fatal("created token identity invalid")
	}
	var hash []byte
	var count int
	var auditCount, eventCount, receiptCount int
	if e = a.DBPool().QueryRow(ctx, "SELECT token_hash FROM api_token WHERE id=$1", id).Scan(&hash); e != nil || !bytes.Equal(hash, crypto.HashToken(plain)) {
		t.Fatal("hash persistence mismatch")
	}
	if e = a.DBPool().QueryRow(ctx, "SELECT count(*) FROM api_token").Scan(&count); e != nil || count != 1 {
		t.Fatal("duplicate token")
	}
	if e = a.DBPool().QueryRow(ctx, "SELECT (SELECT count(*) FROM audit_log WHERE action='auth.token.created'),(SELECT count(*) FROM outbox_event WHERE event_type='auth.token.created'),(SELECT count(*) FROM idempotency_record WHERE idempotency_key='own-create')").Scan(&auditCount, &eventCount, &receiptCount); e != nil || auditCount != 1 || eventCount != 1 || receiptCount != 1 {
		t.Fatal("atomic publication missing")
	}
	var durable string
	if e = a.DBPool().QueryRow(ctx, "SELECT coalesce((SELECT string_agg(payload::text,'') FROM audit_log),'') || coalesce((SELECT string_agg(payload::text,'') FROM outbox_event),'') || coalesce((SELECT string_agg(response_body::text,'') FROM idempotency_record),'')").Scan(&durable); e != nil || strings.Contains(durable, plain) {
		t.Fatal("plaintext in durable command data")
	}
	replay := request(session, "POST", "/api/v1/auth/tokens", "own-create", `{"name":"owned read client"}`)
	if replay.Code != 200 || strings.Contains(replay.Body.String(), plain) || !strings.Contains(replay.Body.String(), `"replayed":true`) {
		t.Fatal("lost receipt recovery failed")
	}
	if w := request(session, "POST", "/api/v1/auth/tokens", "own-create", `{"name":"different"}`); w.Code != 409 {
		t.Fatalf("different same-key status %d", w.Code)
	}
	for _, extra := range []string{`"scopes":["system"]`, `"user_id":"` + uuid.NewString() + `"`, `"actor":{"type":"system"}`, `"token_hash":"override"`} {
		if w := request(session, "POST", "/api/v1/auth/tokens", "extra-"+extra, `{"name":"extra",`+extra+`}`); w.Code != 400 {
			t.Fatalf("extra authority status %d", w.Code)
		}
	}
	if w := request(plain, "GET", "/api/v1/auth/tokens", "", ""); w.Code != 403 {
		t.Fatal("API credential manages tokens")
	}
	if w := request(plain, "POST", "/api/v1/auth/tokens", "api-self", `{"name":"self"}`); w.Code != 403 {
		t.Fatal("API credential mints token")
	}
	listed := request(session, "GET", "/api/v1/auth/tokens", "", "")
	if listed.Code != 200 || strings.Contains(listed.Body.String(), plain) || !strings.Contains(listed.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("unsafe own list")
	}
	// A stale session Actor cannot read an old successful receipt after disabling the user.
	sessionActor, e := as.AuthenticateToken(ctx, session)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.DBPool().Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(sessionActor.ID)); e != nil {
		t.Fatal(e)
	}
	if _, secret, e := svc.Create(ctx, sessionActor, ports.TokenCommand{Name: "owned read client"}, "own-create"); e != ports.ErrForbidden || secret != "" {
		t.Fatal("disabled owner replay authorized")
	}
	if _, e = a.DBPool().Exec(ctx, "UPDATE app_user SET is_active=true WHERE id=$1", uuid.MustParse(sessionActor.ID)); e != nil {
		t.Fatal(e)
	}
	otherID := uuid.New()
	_, e = a.Queries().CreateUser(ctx, db.CreateUserParams{ID: otherID, Username: "token-other-member", PasswordHash: "disabled-test-hash", Role: ports.RoleMember, IsActive: true})
	if e != nil {
		t.Fatal(e)
	}
	otherPlain, otherHash, e := crypto.GenerateToken("pm_sess_")
	if e != nil {
		t.Fatal(e)
	}
	_, e = a.Queries().CreateSession(ctx, db.CreateSessionParams{ID: uuid.New(), UserID: otherID, TokenHash: otherHash, CsrfTokenHash: []byte("owned-csrf"), ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}})
	if e != nil {
		t.Fatal(e)
	}
	if w := request(otherPlain, "POST", "/api/v1/auth/tokens/"+id.String()+"/revoke", "foreign-revoke", ""); w.Code != 404 {
		t.Fatal("foreign revoke allowed")
	}
	if w := request(otherPlain, "GET", "/api/v1/auth/tokens", "", ""); w.Code != 200 || strings.Contains(w.Body.String(), id.String()) {
		t.Fatal("foreign list exposure")
	}
	if w := request(otherPlain, "POST", "/api/v1/auth/tokens", "member-create", `{"name":"member own"}`); w.Code != 200 {
		t.Fatal("member own creation denied")
	}
	// Fault at final receipt rolls back token, audit and event, with no secret in response.
	_, e = a.DBPool().Exec(ctx, `CREATE FUNCTION token_receipt_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.idempotency_key IN ('fail-create','fail-revoke') THEN RAISE EXCEPTION 'owned final receipt failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER token_receipt_fault BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION token_receipt_fail()`)
	if e != nil {
		t.Fatal(e)
	}
	w := request(session, "POST", "/api/v1/auth/tokens", "fail-create", `{"name":"rollback me"}`)
	if w.Code != 503 || strings.Contains(w.Body.String(), "secret") {
		t.Fatal("failed create returns success or secret")
	}
	if e = a.DBPool().QueryRow(ctx, "SELECT count(*) FROM api_token WHERE name='rollback me'").Scan(&count); e != nil || count != 0 {
		t.Fatal("failed token persisted")
	}
	if w = request(session, "POST", "/api/v1/auth/tokens/"+id.String()+"/revoke", "fail-revoke", ""); w.Code != 503 {
		t.Fatal("failed revoke accepted")
	}
	if _, e = as.AuthenticateToken(ctx, plain); e != nil {
		t.Fatal("failed revoke did not roll back")
	}
	if _, e = a.DBPool().Exec(ctx, "DROP TRIGGER token_receipt_fault ON idempotency_record; DROP FUNCTION token_receipt_fail()"); e != nil {
		t.Fatal(e)
	}
	w = request(session, "POST", "/api/v1/auth/tokens/"+id.String()+"/revoke", "own-revoke", "")
	if w.Code != 200 {
		t.Fatalf("revoke status %d", w.Code)
	}
	if _, e = as.AuthenticateToken(ctx, plain); e == nil {
		t.Fatal("revoked token still valid")
	}
	w = request(session, "POST", "/api/v1/auth/tokens/"+id.String()+"/revoke", "own-revoke", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal("revoke replay failure")
	}
	w = request(session, "POST", "/api/v1/auth/tokens", "own-create", `{"name":"owned read client"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "revoked_at") || strings.Contains(w.Body.String(), plain) {
		t.Fatal("old create receipt revived secret")
	}
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	if w = request(session, "POST", "/api/v1/auth/tokens", "past-expiry", `{"name":"expired","expires_at":"`+past+`"}`); w.Code != 400 {
		t.Fatal("past expiry accepted")
	}
	t.Log("eight-way create, hash-only data, metadata recovery, current permission, owner isolation, rollback and irreversible revocation verified")
}
