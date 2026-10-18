package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestTVMetadataBrowserHarness(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit disposable browser fixture required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var calls atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/tv/100/season/1" || r.URL.Query().Get("api_key") != "fixture-episode-key" {
			w.WriteHeader(403)
			return
		}
		_, _ = io.WriteString(w, `{"season_number":1,"episodes":[{"id":1001,"season_number":1,"episode_number":1,"air_date":"2099-01-01"},{"id":1002,"season_number":1,"episode_number":2,"air_date":null}]}`)
	}))
	defer source.Close()
	a, admin, _, _, _, _, router, _ := setupOperationHTTP(t, func(handler *httpapi.ServerHandler, a *app.App) {
		handler.SetTVRefreshService(subscription.NewTVRefreshService(postgres.NewTVRefreshStore(a.DBPool(), idgen.New()), tmdb.NewClient("fixture-episode-key", tmdb.WithBaseURL(source.URL))))
	})
	root, m, unit := uuid.New(), uuid.New(), uuid.New()
	if _, e := a.Queries().CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: root, Path: t.TempDir(), Name: "TV", MediaType: "tv"}); e != nil {
		t.Fatal(e)
	}
	if _, e := a.Queries().CreateMedia(ctx, db.CreateMediaParams{ID: m, Title: "Metadata Browser Show", MediaType: "tv"}); e != nil {
		t.Fatal(e)
	}
	if _, e := a.Queries().CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: uuid.New(), MediaID: m, Namespace: "tmdb.tv", ExternalID: "100"}); e != nil {
		t.Fatal(e)
	}
	if _, e := a.Queries().CreateTVUnit(ctx, db.CreateTVUnitParams{ID: unit, MediaID: m, UnitKey: "S01E01", SeasonNumber: pgtype.Int4{Int32: 1, Valid: true}, EpisodeNumber: pgtype.Int4{Int32: 1, Valid: true}}); e != nil {
		t.Fatal(e)
	}
	call := func(method, path, key string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+admin)
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, r)
		return out
	}
	created := call("POST", "/api/v1/subscriptions", "", httpapi.CreateSubscriptionRequest{MediaId: &m, RootId: &root})
	var sub httpapi.SubscriptionDetails
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &sub) != nil {
		t.Fatal(created.Code)
	}
	paused := call("PUT", "/api/v1/subscriptions/"+sub.Id.String()+"/control", "pause", map[string]any{"action": "state", "state": "paused", "expected_version": sub.IntentVersion})
	if paused.Code != 200 {
		t.Fatal(paused.Code)
	}
	server := httptest.NewServer(router)
	defer server.Close()
	script, err := filepath.Abs("../../web/tests/tv-metadata-browser.mjs")
	if err != nil {
		t.Fatal(err)
	}
	cdp := startOwnedTVMetadataChrome(t, ctx)
	artifactDir := os.Getenv("PULSE_SUBSCRIPTION_UI_ARTIFACT_DIR")
	if artifactDir == "" {
		artifactDir = t.TempDir()
	}
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.Env = append(os.Environ(), "PULSE_UI_CDP_URL="+cdp, "PULSE_UI_FIXTURE=1", "PULSE_TV_UI_BASE="+server.URL, "PULSE_TV_UI_SUB="+sub.Id.String(), "PULSE_TV_UI_OUTPUT="+artifactDir)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("metadata browser failed: %v %s", err, raw)
	}
	t.Log(string(raw))
	if calls.Load() != 1 {
		t.Fatal("committed browser retry performed extra provider GET", calls.Load())
	}
}
