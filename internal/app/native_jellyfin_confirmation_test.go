package app

import (
	"context"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/adapter/mediaserver/jellyfin"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/mediaserver"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeJellyfinConfirmationRequiresExactPathProof(t *testing.T) {
	f := organizationSetup(t)
	ctx := context.Background()
	fileID := uuid.New()
	if _, e := f.q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: fileID, RootID: f.root, RelPath: "movie.mkv", SizeBytes: 18, MtimeNs: time.Now().UnixNano(), LastMutationSeq: 1}); e != nil {
		t.Fatal(e)
	}
	if e := f.q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: fileID, UnitID: f.unit}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: f.unit, Stage: "imported", RootID: pgtype.UUID{Bytes: f.root, Valid: true}, SourceFileID: pgtype.UUID{Bytes: fileID, Valid: true}, QualityProfile: []byte(`{"legacy":"unchanged"}`)}); e != nil {
		t.Fatal(e)
	}
	var body atomic.Value
	body.Store(`{}`)
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/Items" || q.Has("Path") || q.Get("Recursive") != "true" || q.Get("MediaTypes") != "Video" || q.Get("Fields") != "Path" || q.Get("EnableTotalRecordCount") != "true" || r.Header.Get("Authorization") != `MediaBrowser Token="fixture"` {
			t.Error("unexpected lookup")
		}
		_, _ = fmt.Fprint(w, body.Load().(string))
	}))
	defer native.Close()
	serverID := uuid.New()
	mapping := []byte(fmt.Sprintf(`{%q:"/movies"}`, f.target))
	if _, e := f.q.CreateMediaServer(ctx, db.CreateMediaServerParams{ID: serverID, Name: "Native", ServerType: "jellyfin", Url: native.URL, ApiKeyEncrypted: []byte("fixture"), PathMapping: mapping}); e != nil {
		t.Fatal(e)
	}
	svc := mediaserver.NewSyncService(nil, postgres.NewMediaConfirmationRepository(f.pool, f.gen))
	svc.RegisterClient(serverID, jellyfin.NewClient(native.URL, "fixture", nil))
	for _, response := range []string{`{}`, `{"Items":null,"TotalRecordCount":0}`, `{"Items":[],"TotalRecordCount":0}`, `{"Items":[{"Id":"other","Path":"/wrong/movie.mkv"}],"TotalRecordCount":1}`, `{"Items":[{"Id":"one"}],"TotalRecordCount":1}`, `{"Items":[{"Id":"one","Path":"/movies/movie.mkv"},{"Id":"two","Path":"/movies/movie.mkv"}],"TotalRecordCount":2}`, `{"Items":[{"Id":"one","Path":"/movies/movie.mkv"}],"TotalRecordCount":2}`} {
		body.Store(response)
		if _, e := svc.ConfirmFile(ctx, serverID, fileID, f.unit); e == nil {
			t.Fatal("invalid path proof confirmed file")
		}
		facts, e := f.q.ListActiveFactsForUnit(ctx, f.unit)
		if e != nil || len(facts) != 1 || facts[0].Stage != "imported" || string(facts[0].QualityProfile) != `{"legacy": "unchanged"}` {
			t.Fatal("original imported fact changed", e, facts)
		}
	}
	body.Store(`{"Items":[{"Id":"unrelated","Path":"/movies/other.mkv"},{"Id":"one","Path":"/movies/movie.mkv"}],"TotalRecordCount":2}`)
	fact, e := svc.ConfirmFile(ctx, serverID, fileID, f.unit)
	if e != nil || fact.Stage != "confirmed" || fact.SourceFileID != fileID {
		t.Fatal("valid originalfile confirmation failed", e)
	}
}
