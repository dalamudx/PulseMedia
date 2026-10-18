package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"io"
	"time"
)

func TestSubscriptionCreationVerifiedMetadataAndAtomicRollback(t *testing.T) {
	ctx := context.Background()
	var mode atomic.Int32
	var liveApp *App
	var userID string
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var id int64
		_, _ = fmt.Sscanf(r.URL.Path, "/movie/%d", &id)
		if mode.Load() == 1 {
			id++
		}
		if mode.Load() == 2 {
			if _, e := liveApp.DBPool().Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", userID); e != nil {
				t.Error(e)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":%d,"title":"Canonical Movie","original_title":"Canonical Movie","release_date":"2020-01-01","overview":"Complete native movie details"}`, id)
	}))
	defer native.Close()
	f := organizationSetup(t)
	a := &App{dbPool: f.pool, idgen: f.gen, clock: clock.New(), logger: logging.NewLogger(io.Discard, "error")}
	liveApp = a
	u, e := f.q.CreateUser(ctx, db.CreateUserParams{ID: uuid.New(), Username: "creation-owner", PasswordHash: "not-used-for-login", Role: ports.RoleMember, IsActive: true})
	if e != nil {
		t.Fatal(e)
	}
	userID = u.ID.String()
	root := f.root
	subscriptionStore := postgres.NewSubscriptionIntentStore(f.pool, f.gen)
	h := httpapi.NewServerHandler(a, nil, nil, nil, nil, tmdb.NewClient("fixture-key", tmdb.WithBaseURL(native.URL)), subscription.NewService(subscriptionStore, subscriptionStore, postgres.NewSubscriptionControlStore(f.pool, f.gen), subscriptionStore), nil, nil, nil, nil, nil, outboxTestKey)
	token := "pm_sess_creation_fixture"
	if _, e := f.q.CreateSession(ctx, db.CreateSessionParams{ID: uuid.New(), UserID: u.ID, TokenHash: crypto.HashToken(token), CsrfTokenHash: []byte("fixture"), ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); e != nil {
		t.Fatal(e)
	}
	router := httpapi.NewRouter(a, h, auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, a.clock, f.gen), nil)
	snapshot := func() string {
		var value string
		e := a.DBPool().QueryRow(ctx, `SELECT jsonb_build_array((SELECT count(*) FROM media),(SELECT count(*) FROM media_identity),(SELECT count(*) FROM media_unit),(SELECT count(*) FROM subscription),(SELECT count(*) FROM wanted_item),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM outbox_event))::text`).Scan(&value)
		if e != nil {
			t.Fatal(e)
		}
		return value
	}
	call := func(id int64) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"tmdb_id": id, "media_type": "movie", "title": "Forged browser title", "year": 1900, "root_id": root, "scope_type": "all"})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	before := snapshot()
	mode.Store(1)
	r := call(991001)
	if r.Code != 503 || snapshot() != before {
		t.Fatal("invalid native identity wrote state", r.Code, r.Body.String())
	}
	mode.Store(2)
	r = call(991002)
	if r.Code != 403 || snapshot() != before {
		t.Fatal("expired actor after network wrote state", r.Code, r.Body.String())
	}
	if _, e := a.DBPool().Exec(ctx, "UPDATE app_user SET is_active=true WHERE id=$1", userID); e != nil {
		t.Fatal(e)
	}
	mode.Store(0)
	if _, e := a.DBPool().Exec(ctx, `CREATE FUNCTION reject_subscription_creation_audit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='subscription.created' THEN RAISE EXCEPTION 'fixture-sensitive-error';END IF;RETURN NEW;END$$;CREATE TRIGGER reject_subscription_creation_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_subscription_creation_audit()`); e != nil {
		t.Fatal(e)
	}
	r = call(991003)
	if r.Code != 503 || !strings.HasPrefix(r.Header().Get("Content-Type"), "application/problem+json") || strings.Contains(r.Body.String(), "fixture-sensitive-error") || snapshot() != before {
		t.Fatal("creation tail did not rollback safely", r.Code, r.Body.String())
	}
	if _, e := a.DBPool().Exec(ctx, `DROP TRIGGER reject_subscription_creation_audit ON audit_log;DROP FUNCTION reject_subscription_creation_audit()`); e != nil {
		t.Fatal(e)
	}
	r = call(991003)
	if r.Code != 201 {
		t.Fatal("same identity retry", r.Code, r.Body.String())
	}
	var title string
	var year int
	if e := a.DBPool().QueryRow(ctx, `SELECT m.title,m.year FROM media m JOIN media_identity i ON i.media_id=m.id WHERE i.namespace='tmdb.movie' AND i.external_id='991003'`).Scan(&title, &year); e != nil || title != "Canonical Movie" || year != 2020 {
		t.Fatal("browser hints replaced native identity", title, year, e)
	}
}
