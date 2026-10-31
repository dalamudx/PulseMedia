package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type canonicalReviewHTTPFake struct {
	*untrackedReviewHTTPFake
	options     []ports.UntrackedReviewEpisodeOption
	optionCalls int
	tmdb        int64
	season      int
}

func (f *canonicalReviewHTTPFake) EpisodeOptions(_ context.Context, a *ports.Actor, scope, file uuid.UUID, tmdb int64, season int) ([]ports.UntrackedReviewEpisodeOption, error) {
	f.track(a, scope, file, "")
	f.optionCalls++
	f.tmdb, f.season = tmdb, season
	return f.options, f.err
}

func TestUntrackedReviewCanonicalHTTPTransport(t *testing.T) {
	parts := []string{untrackedScopeID, "discoveries", untrackedReviewFileID, "episode-options"}
	for _, tc := range []struct {
		name, method, query, body, key string
		valid                          bool
	}{
		{"valid", "GET", "tmdb_id=123&season=1", "", "", true},
		{"head", "HEAD", "tmdb_id=123&season=1", "", "", false},
		{"default", "OPTIONS", "tmdb_id=123&season=1", "", "", false},
		{"post", "POST", "tmdb_id=123&season=1", "", "", false},
		{"put", "PUT", "tmdb_id=123&season=1", "", "", false},
		{"delete", "DELETE", "tmdb_id=123&season=1", "", "", false},
		{"extra", "GET", "tmdb_id=123&season=1&other=x", "", "", false},
		{"duplicate", "GET", "tmdb_id=123&season=1&season=1", "", "", false},
		{"missing", "GET", "tmdb_id=123", "", "", false},
		{"leading_zero", "GET", "tmdb_id=0123&season=1", "", "", false},
		{"special", "GET", "tmdb_id=123&season=0", "", "", false},
		{"overflow", "GET", "tmdb_id=999999999999999999999&season=1", "", "", false},
		{"body", "GET", "tmdb_id=123&season=1", "x", "", false},
		{"key", "GET", "tmdb_id=123&season=1", "", "unexpected", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, untrackedReviewPath+"/episode-options?"+tc.query, strings.NewReader(tc.body))
			if tc.key != "" {
				r.Header.Set("Idempotency-Key", tc.key)
			}
			if got := validUntrackedReviewEpisodeOptionsTransport(r, parts); got != tc.valid {
				t.Fatalf("got=%t want=%t", got, tc.valid)
			}
		})
	}
	if !untrackedReviewEpisodeOptionsPath(parts) {
		t.Fatal("route not recognized")
	}
}

func TestUntrackedReviewCanonicalHTTPOptions(t *testing.T) {
	s, old := untrackedReviewHTTPFixture()
	fake := &canonicalReviewHTTPFake{untrackedReviewHTTPFake: old, options: []ports.UntrackedReviewEpisodeOption{{CanonicalUnitID: uuid.NewString(), MediaMetadataVersion: 7, UnitMetadataVersion: 9, Season: 1, Episode: 7, TMDBEpisodeID: 1007, UnitKey: "S01E07", Title: "Series"}}}
	s.SetUntrackedReviewService(fake)
	actor := untrackedScopeAdmin()
	ctx := context.WithValue(t.Context(), actorKey, actor)
	request := ReadUntrackedReviewEpisodeOptionsRequestObject{ScopeId: uuid.MustParse(untrackedScopeID), FileId: uuid.MustParse(untrackedReviewFileID), Params: ReadUntrackedReviewEpisodeOptionsParams{TmdbId: 123, Season: 1}}
	response, err := s.ReadUntrackedReviewEpisodeOptions(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := response.(ReadUntrackedReviewEpisodeOptions200JSONResponse)
	if !ok || len(result.Items) != 1 || result.Items[0].TmdbEpisodeId != 1007 || fake.optionCalls != 1 || fake.tmdb != 123 || fake.season != 1 {
		t.Fatal("canonical options contract", response)
	}
	// Every non-GET method must stop in the actual production outer guard.
	h := untrackedReviewHTTPRouter(s, actor)
	for _, method := range []string{http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		w := untrackedScopeCall(h, method, untrackedReviewPath+"/episode-options?tmdb_id=123&season=1", "")
		if w.Code < 400 || fake.optionCalls != 1 {
			t.Fatalf("method %s bypassed guard status=%d calls=%d", method, w.Code, fake.optionCalls)
		}
	}
	w := untrackedScopeCall(h, http.MethodGet, untrackedReviewPath+"/episode-options?tmdb_id=123&season=1", "")
	untrackedReviewHTTPStatus(t, w, 200)
	if fake.optionCalls != 2 {
		t.Fatal("production options route not wired")
	}
}
