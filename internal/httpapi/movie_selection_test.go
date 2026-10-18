package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type selectedHTTPMetadata struct {
	ports.MetadataProvider
	calls atomic.Int32
}

func (p *selectedHTTPMetadata) GetMovieDetails(context.Context, int64, string) (*ports.MovieMetadata, error) {
	p.calls.Add(1)
	return &ports.MovieMetadata{TMDBID: 550, MediaType: "movie", Title: "Fight Club", Year: 1999}, nil
}
func TestSelectedMovieHTTPExplicitIdentityReplayAndCSRF(t *testing.T) {
	provider := &selectedHTTPMetadata{}
	a, admin, member, root, _, _, router, _ := setupOperationHTTP(t, func(h *httpapi.ServerHandler, a *app.App) {
		h.SetMovieSelectionService(acquisition.NewMovieSelectionService(postgres.NewAcquisitionStore(a.DBPool(), idgen.New(), clock.New(), acquisition.SelectMovieRound), provider, clock.New()))
	})
	ctx := context.Background()
	q := db.New(a.DBPool())
	indexer, candidate, client, queryID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, e := q.CreateIndexer(ctx, db.CreateIndexerParams{ID: indexer, Name: "HTTP chosen source", Url: "https://example.test", ApiKeyEncrypted: []byte("cipher"), Categories: []byte(`[]`)}); e != nil {
		t.Fatal(e)
	}
	if _, e := q.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{ID: candidate, IndexerID: indexer, Title: "Fight.Club.1999.1080p.Bluray.x264", Guid: "http-explicit", SourceConfigVersion: 1, SizeBytes: 1024, LinkEncrypted: []byte("cipher"), QualityProfile: []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v2","codec":"H264","codec_parser_version":"codec.tokens.v2"}`)}); e != nil {
		t.Fatal(e)
	}
	if _, e := q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: client, Name: "HTTP chosen client", ClientType: "qbittorrent", Url: "http://127.0.0.1:8080", PasswordEncrypted: []byte("cipher"), RemotePathMap: []byte(`{}`)}); e != nil {
		t.Fatal(e)
	}
	if _, e := a.DBPool().Exec(ctx, "INSERT INTO indexer_query(id,indexer_id,query_type,keyword,config_version,state,finished_at) VALUES($1,$2,'search','Fight Club',1,'succeeded',clock_timestamp())", queryID, indexer); e != nil {
		t.Fatal(e)
	}
	if e := q.LinkQueryCandidate(ctx, db.LinkQueryCandidateParams{QueryID: queryID, CandidateID: candidate}); e != nil {
		t.Fatal(e)
	}
	selection := ports.MovieSelection{TMDBID: 550, RootID: root, CandidateID: candidate, DownloaderID: client, PolicyID: "default"}
	send := func(path, token, key string, body any, cookie bool) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		if cookie {
			req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: token})
		} else if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	if w := send("/api/v1/acquisitions/selected-movie/preview", "", "", selection, false); w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	p := send("/api/v1/acquisitions/selected-movie/preview", member, "", selection, false)
	if p.Code != 200 {
		t.Fatal(p.Code, p.Body.String())
	}
	var preview ports.MovieSelectionPreview
	if json.Unmarshal(p.Body.Bytes(), &preview) != nil || preview.Decision != "accept" {
		t.Fatal(p.Body.String())
	}
	command := ports.MovieSelectionCommand{Selection: selection, ExpectedInputStamp: preview.InputStamp, Confirmed: true}
	var originalQuality string
	if e := a.DBPool().QueryRow(ctx, "SELECT quality_profile::text FROM release_candidate WHERE id=$1", candidate).Scan(&originalQuality); e != nil {
		t.Fatal(e)
	}
	for _, dated := range []string{
		"Fight.Club.1999.02.14.1080p.Bluray.x264.{tmdb-550}",
		"Fight.Club.19990214.1080p.Bluray.x264.{tmdb-550}",
		"Fight.Club.1999.02.30.1080p.Bluray.x264.{tmdb-550}",
	} {
		if _, e := a.DBPool().Exec(ctx, "UPDATE release_candidate SET title=$2 WHERE id=$1", candidate, dated); e != nil {
			t.Fatal(e)
		}
		datedResponse := send("/api/v1/acquisitions/selected-movie/preview", member, "", selection, false)
		var datedPreview ports.MovieSelectionPreview
		if datedResponse.Code != 200 || json.Unmarshal(datedResponse.Body.Bytes(), &datedPreview) != nil || datedPreview.Decision == "accept" {
			t.Fatal("calendar evidence accepted despite explicit movie ID", datedResponse.Code, datedResponse.Body.String())
		}
		if w := send("/api/v1/acquisitions/selected-movie", member, "calendar-stale", command, false); w.Code != 409 || !bytes.Contains(w.Body.Bytes(), []byte(`"code":"VERSION_CONFLICT"`)) {
			t.Fatal("old accepted preview survived dated resource change", w.Code, w.Body.String())
		}
		currentDated := ports.MovieSelectionCommand{Selection: selection, ExpectedInputStamp: datedPreview.InputStamp, Confirmed: true}
		if w := send("/api/v1/acquisitions/selected-movie", member, "calendar-reject", currentDated, false); w.Code != 409 || !bytes.Contains(w.Body.Bytes(), []byte(`"code":"UNSUPPORTED_COVERAGE"`)) {
			t.Fatal("current calendar-only preview obtained movie execution", w.Code, w.Body.String())
		}
		var work int
		if e := a.DBPool().QueryRow(ctx, "SELECT (SELECT count(*) FROM job WHERE kind='download_submission')+(SELECT count(*) FROM unit_reservation)+(SELECT count(*) FROM download_requirement)").Scan(&work); e != nil || work != 0 {
			t.Fatal("rejected calendar created task/protection/demand", work, e)
		}
		var stored string
		if e := a.DBPool().QueryRow(ctx, "SELECT quality_profile::text FROM release_candidate WHERE id=$1", candidate).Scan(&stored); e != nil || stored != originalQuality {
			t.Fatal("calendar rejection rewrote stored quality", e)
		}
	}
	if _, e := a.DBPool().Exec(ctx, "UPDATE release_candidate SET title='Fight.Club.1999.1080p.Bluray.x264' WHERE id=$1", candidate); e != nil {
		t.Fatal(e)
	}
	p = send("/api/v1/acquisitions/selected-movie/preview", member, "", selection, false)
	if p.Code != 200 || json.Unmarshal(p.Body.Bytes(), &preview) != nil || preview.Decision != "accept" {
		t.Fatal("valid movie did not recover", p.Code, p.Body.String())
	}
	command.ExpectedInputStamp = preview.InputStamp

	if w := send("/api/v1/acquisitions/selected-movie", member, "", command, false); w.Code != 400 {
		t.Fatal("missing key", w.Code, w.Body.String())
	}
	if w := send("/api/v1/acquisitions/selected-movie", member, "http-selected", command, true); w.Code != 403 {
		t.Fatal("cookie CSRF", w.Code)
	}
	first := send("/api/v1/acquisitions/selected-movie", member, "http-selected", command, false)
	if first.Code != 202 {
		t.Fatal(first.Code, first.Body.String())
	}
	calls := provider.calls.Load()
	second := send("/api/v1/acquisitions/selected-movie", member, "http-selected", command, false)
	if second.Code != 202 || second.Body.String() != first.Body.String() || provider.calls.Load() != calls {
		t.Fatal("unstable/network replay", second.Code, second.Body.String())
	}
	if first.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("cacheable receipt")
	}
	command.Selection.RootID = uuid.New()
	if w := send("/api/v1/acquisitions/selected-movie", member, "http-selected", command, false); w.Code != 404 {
		t.Fatal("root permission/resource before replay", w.Code, w.Body.String())
	}
	_ = admin
	var jobs int
	if e := a.DBPool().QueryRow(ctx, "SELECT count(*) FROM job WHERE kind='download_submission'").Scan(&jobs); e != nil || jobs != 1 {
		t.Fatal("duplicate work", jobs, e)
	}
}
