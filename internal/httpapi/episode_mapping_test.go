package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type episodeMappingFixture struct {
	a             *app.App
	admin, member string
	sub           httpapi.SubscriptionDetails
	units         []uuid.UUID
	router        http.Handler
	calls         *atomic.Int32
}

func setupEpisodeMappingFixture(t *testing.T) *episodeMappingFixture {
	t.Helper()
	ctx := context.Background()
	counter := &atomic.Int32{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counter.Add(1)
		if r.URL.Query().Get("api_key") != "fixture-mapping-key" {
			w.WriteHeader(403)
			return
		}
		switch r.URL.Path {
		case "/tv/100":
			_, _ = io.WriteString(w, `{"id":100,"name":"Mapping Browser Show","original_name":"Mapping Browser Show","overview":"Owned mapping fixture","first_air_date":"2020-01-01","number_of_seasons":1,"number_of_episodes":2,"seasons":[{"season_number":1,"episode_count":2}]}`)
		case "/tv/100/season/1":
			_, _ = io.WriteString(w, `{"season_number":1,"episodes":[{"id":1001,"season_number":1,"episode_number":1,"air_date":"2020-01-01"},{"id":1002,"season_number":1,"episode_number":2,"air_date":"2020-01-01"}]}`)
		case "/tv/100/episode_groups":
			_, _ = io.WriteString(w, `{"results":[{"id":"group1","name":"Fixture absolute","type":2,"episode_count":2,"group_count":1}]}`)
		case "/tv/episode_group/group1":
			_, _ = io.WriteString(w, `{"id":"group1","type":2,"episode_count":2,"group_count":1,"groups":[{"id":"part1","name":"Fixture sequence","order":0,"episodes":[{"id":1002,"season_number":1,"episode_number":2,"order":1},{"id":1001,"season_number":1,"episode_number":1,"order":0}]}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(provider.Close)
	a, admin, member, _, _, _, router, _ := setupOperationHTTP(t, func(h *httpapi.ServerHandler, a *app.App) {
		repo := postgres.NewEpisodeSourceStore(postgres.NewTVRefreshStore(a.DBPool(), idgen.New()))
		metadata := tmdb.NewClient("fixture-mapping-key", tmdb.WithBaseURL(provider.URL))
		source := subscription.NewEpisodeSourceService(repo, metadata)
		h.SetEpisodeSourceService(source)
		h.SetSubscriptionCreationService(subscription.NewSubscriptionCreationService(postgres.NewSubscriptionCreationStore(a.DBPool(), idgen.New()), metadata, metadata, crypto.NewSecretProtector(make([]byte, 32))))
		h.SetEpisodeMappingService(subscription.NewEpisodeMappingService(postgres.NewEpisodeMappingStore(repo, idgen.New()), source))
		h.SetEpisodeCandidateReviewService(subscription.NewEpisodeCandidateReviewService(postgres.NewEpisodeMappingStore(repo, idgen.New())))
		h.SetTVCoverageCandidateReviewService(subscription.NewTVCoverageCandidateReviewService(postgres.NewTVCoverageCandidateReviewStore(postgres.NewEpisodeMappingStore(repo, idgen.New()))))
		h.SetEpisodeAcquisitionService(acquisition.NewEpisodeService(postgres.NewEpisodeAcquisitionStore(a.DBPool(), idgen.New())))
		h.SetTransferRecoveryService(transfer.NewRecoveryService(transferstore.NewRepository(a.DBPool(), idgen.New(), outbox.New(a.DBPool(), idgen.New(), clock.New()), rootfs.NewFilesystem())))
	})
	q := a.Queries()
	root, m := uuid.New(), uuid.New()
	if _, e := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: root, Path: t.TempDir(), Name: "Mapping TV", MediaType: "tv"}); e != nil {
		t.Fatal(e)
	}
	if _, e := q.CreateMedia(ctx, db.CreateMediaParams{ID: m, Title: "Mapping Browser Show", MediaType: "tv"}); e != nil {
		t.Fatal(e)
	}
	if _, e := q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: uuid.New(), MediaID: m, Namespace: "tmdb.tv", ExternalID: "100"}); e != nil {
		t.Fatal(e)
	}
	units := []uuid.UUID{uuid.New(), uuid.New()}
	for i, id := range units {
		if _, e := q.CreateTVUnit(ctx, db.CreateTVUnitParams{ID: id, MediaID: m, UnitKey: []string{"S01E01", "S01E02"}[i], SeasonNumber: pgtype.Int4{Int32: 1, Valid: true}, EpisodeNumber: pgtype.Int4{Int32: int32(i + 1), Valid: true}}); e != nil {
			t.Fatal(e)
		}
		if _, e := q.BindTMDBEpisodeIdentity(ctx, db.BindTMDBEpisodeIdentityParams{UnitID: id, ExternalID: int64(1001 + i), TmdbSeriesID: 100}); e != nil {
			t.Fatal(e)
		}
	}
	f := &episodeMappingFixture{a: a, admin: admin, member: member, units: units, router: router, calls: counter}
	w := f.call(admin, "POST", "/api/v1/subscriptions", "mapping-subscription", httpapi.CreateSubscriptionRequest{MediaId: &m, RootId: &root})
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &f.sub) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	paused := f.call(admin, "PUT", "/api/v1/subscriptions/"+f.sub.Id.String()+"/control", "mapping-pause", map[string]any{"action": "state", "state": "paused", "expected_version": f.sub.IntentVersion})
	if paused.Code != 200 {
		t.Fatal(paused.Code)
	}
	// Mapping-source I/O assertions start after trusted catalog creation.
	counter.Store(0)
	return f
}
func (f *episodeMappingFixture) call(token, method, path, key string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}
func TestEpisodeMappingHTTPPublicationReplayAndCurrentVersion(t *testing.T) {
	f := setupEpisodeMappingFixture(t)
	path := "/api/v1/subscriptions/" + f.sub.Id.String() + "/episode-mapping"
	draft := ports.EpisodeMappingDraft{Mode: "absolute", GroupID: "group1"}
	if w := f.call(f.member, "POST", path+"/preview", "", draft); w.Code != 403 || f.calls.Load() != 0 {
		t.Fatal("member preview fetched source", w.Code)
	}
	w := f.call(f.admin, "POST", path+"/preview", "", draft)
	var p ports.EpisodeMappingPreview
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil || !p.CanPublish || len(p.Rows) != 2 {
		t.Fatal(w.Code, w.Body.String())
	}
	c := ports.EpisodeMappingCommand{Draft: draft, ExpectedInputStamp: p.InputStamp, ExpectedSourceHash: p.SourceHash, Confirmed: true}
	noAck := c
	noAck.Confirmed = false
	if w := f.call(f.admin, "POST", path, "no-ack", noAck); w.Code != 400 {
		t.Fatal("no ack published", w.Code)
	}
	first := f.call(f.admin, "POST", path, "publish-http-map", c)
	if first.Code != 200 || first.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal(first.Code, first.Body.String())
	}
	verifyPersistedEpisodeCandidate(t, f, path)
	readExecutionCounts := func() [6]int64 {
		var counts [6]int64
		if e := f.a.DBPool().QueryRow(context.Background(), "SELECT (SELECT count(*) FROM episode_execution_authority),(SELECT count(*) FROM acquisition_reservation),(SELECT count(*) FROM unit_reservation),(SELECT count(*) FROM download_requirement),(SELECT count(*) FROM download_task),(SELECT count(*) FROM job)").Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5]); e != nil {
			t.Fatal(e)
		}
		return counts
	}
	beforeExecution := readExecutionCounts()
	beforeExplain := f.calls.Load()
	for _, c := range []struct {
		name, reason string
		known        bool
	}{{"[Team] Fixture - 1 (1080p) [HASH].mkv", "mapped_coverage_only", true}, {"[Team] Fixture - 1 (480p) [HASH].mkv", "mapped_coverage_only", true}, {"[Team] Fixture - 3 (720p) [HASH].mkv", "absolute_number_unmapped", false}, {"[Team] Fixture - 1 (480p E2) [HASH].mkv", "episode_coverage_unresolved", false}, {"[Team] Fixture - 1 (sample) [HASH].mkv", "partial_episode_content", false}, {"[Team] Fixture - 1 (1080p) [HASH] S01E02.mkv", "conflicting_episode_coverage", false}, {"[Team] Fixture - 1 (1080p) [HASH] - 2.mkv", "conflicting_episode_coverage", false}, {"[Team] Fixture - 1 [1080p].mkv", "mapped_coverage_only", true}, {"[Team] Fixture - 1 [1080p] {tmdb-100}.mkv", "mapped_coverage_only", true}, {"[Team] Fixture - 1 [1080p] {tmdb-999}.mkv", "media_identity_conflict", false}, {"[Team] Fixture - 1 [1080p] {tmdb-100} {tmdb-999}.mkv", "media_identity_conflict", false}, {"[Team] Fixture - 3 [1080p].mkv", "absolute_number_unmapped", false}, {"[Team] Fixture - 1 [1080p] sample.mkv", "partial_episode_content", false}, {"[Team] Fixture - 1 [1080p] S01E02.mkv", "conflicting_episode_coverage", false}, {"[Team] Fixture - 1 [1080p] - 2.mkv", "conflicting_episode_coverage", false}, {"[Team] Fixture E2 - 1 [1080p].mkv", "conflicting_episode_coverage", false}} {
		w := f.call(f.admin, "POST", path+"/explain-release", "", map[string]string{"name": c.name})
		var out subscription.MappedReleaseExplanation
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.CoverageKnown != c.known || out.Reason != c.reason || out.ExecutionAllowed || out.MappingRevision != 1 || out.InputStamp == "" || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("mapped public response", w.Code, w.Body.String())
		}
		if c.known {
			if len(out.Rows) != 1 || out.Rows[0].UnitID != f.units[0] || out.Rows[0].UnitKey != "S01E01" {
				t.Fatal("explanation did not return exact original approved unit", c.name, out.Rows)
			}
		} else if len(out.Rows) != 0 {
			t.Fatal("uncertain coverage returned partial approved units", c.name, out.Rows)
		}

	}
	if afterExecution := readExecutionCounts(); afterExecution != beforeExecution {
		t.Fatal("read-only explanation created execution authority/protection/work", beforeExecution, afterExecution)
	}
	if f.calls.Load() != beforeExplain {
		t.Fatal("explanation reobserved remote source")
	}

	calls := f.calls.Load()
	repeat := f.call(f.admin, "POST", path, "publish-http-map", c)
	if repeat.Code != 200 || !bytes.Equal(first.Body.Bytes(), repeat.Body.Bytes()) || f.calls.Load() != calls {
		t.Fatal("lost receipt retry re-observed/mutated source")
	}
	if w := f.call(f.member, "POST", path, "publish-http-map", c); w.Code != 403 {
		t.Fatal("cached role elevation", w.Code)
	}
	if w := f.call(f.admin, "POST", path, "stale-http-map", c); w.Code != 409 {
		t.Fatal("stale preview overwritten", w.Code)
	}
	w = f.call(f.admin, "GET", path, "", nil)
	var state ports.EpisodeMappingState
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil || !state.Current || state.MappingRevision != 1 || state.ObservedAt.IsZero() {
		t.Fatal(w.Code, w.Body.String())
	}
	saved, e := f.a.Queries().GetSubscriptionByID(context.Background(), f.sub.Id)
	if e != nil || saved.State != "paused" || saved.IntentVersion != state.IntentVersion {
		t.Fatal("binding resumed or mixed intent", saved, e)
	}
	if _, e = f.a.DBPool().Exec(context.Background(), "UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=$1", f.units[1]); e != nil {
		t.Fatal(e)
	}
	w = f.call(f.admin, "GET", path, "", nil)
	if json.Unmarshal(w.Body.Bytes(), &state) != nil || state.Current || state.HoldReason != "canonical_units_changed" {
		t.Fatal("old valid mapping retained", w.Body.String())
	}
}
