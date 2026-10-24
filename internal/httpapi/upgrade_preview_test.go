package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type upgradeFixture struct {
	a                               *app.App
	actor                           *ports.Actor
	admin, member                   string
	router                          http.Handler
	request                         ports.UpgradePreviewRequest
	unit, root, source, query, fact uuid.UUID
	svc                             *acquisition.UpgradePreviewService
}

type upgradeFixtureSetup func(*httpapi.ServerHandler, *app.App)

func newUpgradeFixture(t *testing.T, configure ...upgradeFixtureSetup) *upgradeFixture {
	t.Helper()
	return newUpgradeFixtureWithCutoff(t, 1000, configure...)
}

func newUpgradeFixtureWithCutoff(t *testing.T, cutoff int, configure ...upgradeFixtureSetup) *upgradeFixture {
	t.Helper()
	f := &upgradeFixture{}
	a, admin, member, root, unit, candidate, router, _ := setupOperationHTTP(t, func(h *httpapi.ServerHandler, a *app.App) {
		h.SetUpgradePreviewService(acquisition.NewUpgradePreviewService(postgres.NewUpgradePreviewStore(a.DBPool())))
		h.SetUpgradeReservationService(acquisition.NewUpgradeReservationService(postgres.NewUpgradeReservationStore(a.DBPool(), idgen.New())))
		for _, setup := range configure {
			setup(h, a)
		}
	})
	f.a = a
	f.admin = admin
	f.member = member
	f.root = root
	f.unit = unit
	f.router = router
	f.svc = acquisition.NewUpgradePreviewService(postgres.NewUpgradePreviewStore(a.DBPool()))
	var actorID, mediaID uuid.UUID
	if e := a.DBPool().QueryRow(context.Background(), "SELECT id FROM app_user WHERE username='admin'").Scan(&actorID); e != nil {
		t.Fatal(e)
	}
	if e := a.DBPool().QueryRow(context.Background(), "SELECT media_id FROM media_unit WHERE id=$1", unit).Scan(&mediaID); e != nil {
		t.Fatal(e)
	}
	f.actor = &ports.Actor{ID: actorID.String(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
	f.exec(t, "UPDATE media SET year=1999 WHERE id=$1", mediaID)
	f.exec(t, "UPDATE library_root SET health='online',baseline_completed=true WHERE id=$1", root)
	f.exec(t, "SELECT indexer_id FROM release_candidate WHERE id=$1", candidate)
	if e := a.DBPool().QueryRow(context.Background(), "SELECT indexer_id FROM release_candidate WHERE id=$1", candidate).Scan(&f.source); e != nil {
		t.Fatal(e)
	}
	f.setCandidate(t, candidate, "Fight.Club.1999.2160p.Bluray.x265")
	f.query = uuid.New()
	f.exec(t, "INSERT INTO indexer_query(id,indexer_id,query_type,keyword,config_version,state,finished_at) VALUES($1,$2,'search','Fight Club',1,'succeeded',clock_timestamp())", f.query, f.source)
	f.exec(t, "INSERT INTO indexer_query_candidate(query_id,candidate_id) VALUES($1,$2)", f.query, candidate)
	rules := quality.PolicyRules{CutoffScore: cutoff, ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300, quality.Res2160p: 400}, SourceOrder: map[quality.Source]int{quality.SourceBluray: 40}, PreferHDR: true}
	raw, _ := json.Marshal(rules)
	q := a.Queries()
	ctx := context.Background()
	if _, e := q.CreatePolicy(ctx, db.CreatePolicyParams{ID: "upgrade-test", Name: "Upgrade test"}); e != nil {
		t.Fatal(e)
	}
	if _, e := q.InsertPolicyRevision(ctx, db.InsertPolicyRevisionParams{ID: uuid.New(), PolicyID: "upgrade-test", Revision: 1, Rules: raw}); e != nil {
		t.Fatal(e)
	}
	ids, _ := json.Marshal([]uuid.UUID{f.source})
	sub, e := q.CreateSubscription(ctx, db.CreateSubscriptionParams{ID: uuid.New(), UserID: actorID, MediaID: mediaID, RootID: root, QualityPolicyID: "upgrade-test", QualityPolicyRevision: 1, IndexerIds: ids, ScopeType: "all"})
	if e != nil {
		t.Fatal(e)
	}
	file := uuid.New()
	f.exec(t, "INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) VALUES($1,$2,'original.mkv',100,1)", file, root)
	if e = q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file, UnitID: unit}); e != nil {
		t.Fatal(e)
	}
	old, _ := json.Marshal(media.Recognize("Fight.Club.1999.1080p.Bluray.x264").Quality)
	f.fact = uuid.New()
	if _, e = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: f.fact, UnitID: unit, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: file, Valid: true}, RootID: pgtype.UUID{Bytes: root, Valid: true}, QualityProfile: old}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.UpsertWantedItem(ctx, db.UpsertWantedItemParams{ID: uuid.New(), SubscriptionID: sub.ID, UnitID: unit, IntentVersion: 1}); e != nil {
		t.Fatal(e)
	}
	f.request = ports.UpgradePreviewRequest{SubscriptionID: sub.ID, CurrentFileID: file, CandidateID: candidate}
	f.project(t)
	return f
}
func (f *upgradeFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, e := f.a.DBPool().Exec(context.Background(), sql, args...); e != nil {
		t.Fatal(e)
	}
}
func (f *upgradeFixture) setCandidate(t *testing.T, id uuid.UUID, title string) {
	t.Helper()
	raw, _ := json.Marshal(media.Recognize(title).Quality)
	f.exec(t, "UPDATE release_candidate SET title=$2,quality_profile=$3 WHERE id=$1", id, title, raw)
}
func (f *upgradeFixture) project(t *testing.T) {
	t.Helper()
	rows, e := f.a.Queries().ListWantedItemsForSubscription(context.Background(), f.request.SubscriptionID)
	if e != nil {
		t.Fatal(e)
	}
	sub, e := f.a.Queries().GetSubscriptionByID(context.Background(), f.request.SubscriptionID)
	if e != nil {
		t.Fatal(e)
	}
	for _, w := range rows {
		if _, e = subscription.NewEvaluator(postgres.NewWantedProjectionStore(f.a.DBPool())).EvaluateWantedItem(context.Background(), w.ID, quality.Policy{ID: sub.QualityPolicyID, Revision: sub.QualityPolicyRevision}); e != nil {
			t.Fatal(e)
		}
	}
}
func (f *upgradeFixture) state(t *testing.T) string {
	t.Helper()
	var out strings.Builder
	for _, table := range []string{"subscription", "wanted_item", "library_file", "library_file_unit", "unit_fact", "release_candidate", "indexer_query", "indexer_query_candidate", "job", "download_task", "download_requirement", "unit_reservation", "acquisition_reservation", "transfer_plan", "transfer_step", "idempotency_record", "audit_log", "outbox_event", "candidate_rule_cache"} {
		var v string
		if e := f.a.DBPool().QueryRow(context.Background(), "SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM "+table+" t").Scan(&v); e != nil {
			t.Fatal(e)
		}
		_, _ = fmt.Fprintf(&out, "%s %x\n", table, sha256.Sum256([]byte(v)))
	}
	return out.String()
}
func (f *upgradeFixture) body() string {
	b := map[string]any{"current_file_id": f.request.CurrentFileID, "candidate_id": f.request.CandidateID}
	if f.request.Expected != nil {
		b["expected"] = f.request.Expected
	}
	raw, _ := json.Marshal(b)
	return string(raw)
}
func (f *upgradeFixture) call(body, cookie string, edit func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/v1/subscriptions/"+f.request.SubscriptionID.String()+"/upgrade-previews", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://"+r.Host)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: cookie})
	}
	if edit != nil {
		edit(r)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}
func upgradeResult(t *testing.T, w *httptest.ResponseRecorder) *ports.UpgradePreview {
	t.Helper()
	var out ports.UpgradePreview
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Status != "preview_not_executable" || out.ExecutionAllowed || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	for _, s := range []string{"original.mkv", "enc", "source_path", "receipt", "execution_authority"} {
		if strings.Contains(w.Body.String(), s) {
			t.Fatal("leaked internal material", s)
		}
	}
	return &out
}
func TestUpgradePreviewPGStableCookieNoWrites(t *testing.T) {
	f := newUpgradeFixture(t)
	before := f.state(t)
	p := upgradeResult(t, f.call(f.body(), f.admin, nil))
	if !p.Comparable || !p.Improvement || p.CurrentFactID != f.fact || p.SourceID != f.source || p.Version.InputStamp == "" {
		t.Fatal(p)
	}
	f.request.Expected = &p.Version
	p2 := upgradeResult(t, f.call(f.body(), f.admin, nil))
	if p.Version != p2.Version || p.Reason != p2.Reason {
		t.Fatal("unstable observation")
	}
	var satisfied bool
	if e := f.a.DBPool().QueryRow(context.Background(), "SELECT is_satisfied FROM wanted_item WHERE subscription_id=$1", f.request.SubscriptionID).Scan(&satisfied); e != nil || !satisfied {
		t.Fatal("fixture must already satisfy ordinary Wanted", e)
	}
	if before != f.state(t) {
		t.Fatal("read-only query changed business rows")
	}
}
func TestUpgradePreviewPGStrictScopeAndCredential(t *testing.T) {
	f := newUpgradeFixture(t)
	tokenSvc := auth.NewTokenManagementService(postgres.NewTokenManagementStore(f.a.DBPool(), idgen.New()), crypto.ReadTokenIssuer{}, f.a.Clock())
	_, secret, e := tokenSvc.Create(context.Background(), f.actor, ports.TokenCommand{Name: "upgrade read"}, "upgrade-read")
	if e != nil {
		t.Fatal(e)
	}
	before := f.state(t)
	body := f.body()
	cases := []struct {
		name, body, cookie string
		edit               func(*http.Request)
		want               int
	}{
		{"anonymous", body, "", nil, 401}, {"member", body, f.member, nil, 403}, {"api-token", body, "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+secret) }, 403}, {"csrf", body, f.admin, func(r *http.Request) { r.Header.Set("Origin", "http://other.invalid") }, 403},
		{"path", strings.TrimSuffix(body, "}") + `,"source_path":"/private/file"}`, f.admin, nil, 400}, {"quality", strings.TrimSuffix(body, "}") + `,"quality":{"resolution":"2160p"}}`, f.admin, nil, 400}, {"duplicate", strings.Replace(body, `"candidate_id":`, `"candidate_id":"`+uuid.NewString()+`","candidate_id":`, 1), f.admin, nil, 400}, {"case", strings.Replace(body, "candidate_id", "Candidate_id", 1), f.admin, nil, 400}, {"null", `{"candidate_id":null,"current_file_id":"` + f.request.CurrentFileID.String() + `"}`, f.admin, nil, 400}, {"tail", body + " {}", f.admin, nil, 400}, {"nil-id", strings.Replace(body, f.request.CurrentFileID.String(), uuid.Nil.String(), 1), f.admin, nil, 400}, {"wrong-file", strings.Replace(body, f.request.CurrentFileID.String(), uuid.NewString(), 1), f.admin, nil, 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := f.call(tc.body, tc.cookie, tc.edit)
			if w.Code != tc.want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
	if before != f.state(t) {
		t.Fatal("rejected query wrote business rows")
	}
}
func TestUpgradePreviewPGConservativeEvidence(t *testing.T) {
	for _, name := range []string{"title-reject", "group-reject", "cutoff", "equal", "lower", "same-tier-hdr", "old-quality", "old-candidate", "multiunit", "ambiguous", "candidate-tv", "candidate-pack", "candidate-identity", "source-expired", "source-config", "projection-stale", "policy-switch", "policy-current", "fact-expired", "root-offline", "tv-scope"} {
		t.Run(name, func(t *testing.T) {
			f := newUpgradeFixture(t)
			expectedReason := ""
			switch name {
			case "title-reject", "group-reject", "cutoff", "policy-current":
				var rules quality.PolicyRules
				var raw []byte
				if e := f.a.DBPool().QueryRow(context.Background(), "SELECT rules FROM policy_revision WHERE policy_id='upgrade-test' AND revision=1").Scan(&raw); e != nil {
					t.Fatal(e)
				}
				_ = json.Unmarshal(raw, &rules)
				if name == "title-reject" {
					rules.RejectTerms = []string{"fight"}
					expectedReason = "candidate_reject_term"
				}
				if name == "group-reject" {
					rules.RejectGroups = []string{"BAD"}
					f.setCandidate(t, f.request.CandidateID, "Fight.Club.1999.2160p.Bluray.x265-BAD")
					expectedReason = "candidate_reject_group"
				}
				if name == "cutoff" {
					rules.CutoffScore = 340
					expectedReason = "already_reached_cutoff"
				}
				raw, _ = json.Marshal(rules)
				f.exec(t, "INSERT INTO policy_revision(id,policy_id,revision,rules) VALUES($1,'upgrade-test',2,$2)", uuid.New(), raw)
				f.exec(t, "UPDATE policy SET current_revision=2 WHERE id='upgrade-test'")
				if name != "policy-current" {
					f.exec(t, "UPDATE subscription SET quality_policy_revision=2,intent_version=intent_version+1 WHERE id=$1", f.request.SubscriptionID)
					f.project(t)
				} else {
					expectedReason = "policy_input_changed"
				}
			case "equal":
				f.setCandidate(t, f.request.CandidateID, "Fight.Club.1999.1080p.Bluray.x264")
				expectedReason = "not_better"
			case "lower":
				raw, _ := json.Marshal(media.Recognize("Fight.Club.1999.2160p.Bluray.x265").Quality)
				f.exec(t, "UPDATE unit_fact SET quality_profile=$2 WHERE id=$1", f.fact, raw)
				f.setCandidate(t, f.request.CandidateID, "Fight.Club.1999.1080p.Bluray.x264")
				f.project(t)
				expectedReason = "not_better"
			case "same-tier-hdr":
				f.setCandidate(t, f.request.CandidateID, "Fight.Club.1999.1080p.Bluray.x265.HDR10")
				expectedReason = "same_tier_replacement_disabled"
			case "old-quality":
				f.exec(t, `UPDATE unit_fact SET quality_profile='{"resolution":"1080p","source":"Bluray"}' WHERE id=$1`, f.fact)
				f.project(t)
				expectedReason = "current_quality_unknown"
			case "old-candidate":
				f.exec(t, `UPDATE release_candidate SET quality_profile='{"resolution":"2160p","source":"Bluray"}' WHERE id=$1`, f.request.CandidateID)
				expectedReason = "candidate_quality_unknown"
			case "multiunit":
				var mid uuid.UUID
				if e := f.a.DBPool().QueryRow(context.Background(), "SELECT media_id FROM media_unit WHERE id=$1", f.unit).Scan(&mid); e != nil {
					t.Fatal(e)
				}
				other := uuid.New()
				f.exec(t, "INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,'other')", other, mid) // use a different media to keep the subscription standard
				f.exec(t, "INSERT INTO media(id,title,media_type) VALUES($1,'Other','movie')", uuid.New())
				f.exec(t, "UPDATE media_unit SET media_id=(SELECT id FROM media WHERE title='Other') WHERE id=$1", other)
				f.exec(t, "INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)", f.request.CurrentFileID, other)
				f.project(t)
				expectedReason = "current_file_multi_unit"
			case "ambiguous":
				other := uuid.New()
				f.exec(t, "INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) VALUES($1,$2,'another.mkv',100,1)", other, f.root)
				f.exec(t, "INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)", other, f.unit)
				f.project(t)
				expectedReason = "current_file_ambiguous"
			case "candidate-tv":
				f.setCandidate(t, f.request.CandidateID, "Fight.Club.S01E01.1999.2160p.Bluray.x265")
				expectedReason = "candidate_match_unproven"
			case "candidate-pack":
				f.setCandidate(t, f.request.CandidateID, "Fight.Club.1999.Complete.2160p.Bluray.x265")
				expectedReason = "candidate_match_unproven"
			case "candidate-identity":
				otherMedia, otherUnit := uuid.New(), uuid.New()
				f.exec(t, "INSERT INTO media(id,title,media_type) VALUES($1,'Other','movie')", otherMedia)
				f.exec(t, "INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,'movie')", otherUnit, otherMedia)
				f.exec(t, "UPDATE release_candidate SET matched_unit_id=$2 WHERE id=$1", f.request.CandidateID, otherUnit)
				expectedReason = "candidate_identity_mismatch"
			case "source-expired":
				f.exec(t, "UPDATE indexer_query SET finished_at=clock_timestamp()-interval '31 minutes' WHERE id=$1", f.query)
				expectedReason = "candidate_source_expired"
			case "source-config":
				f.exec(t, "UPDATE indexer SET config_version=config_version+1 WHERE id=$1", f.source)
				expectedReason = "source_input_changed"
			case "projection-stale":
				f.exec(t, "UPDATE media_unit SET facts_version=facts_version+1 WHERE id=$1", f.unit)
				expectedReason = "projection_input_changed"
			case "policy-switch":
				p := upgradeResult(t, f.call(f.body(), f.admin, nil))
				f.request.Expected = &p.Version
				f.exec(t, "UPDATE subscription SET intent_version=intent_version+1 WHERE id=$1", f.request.SubscriptionID)
				w := f.call(f.body(), f.admin, nil)
				if w.Code != 409 {
					t.Fatalf("%d %s", w.Code, w.Body.String())
				}
				return
			case "fact-expired":
				f.exec(t, "UPDATE unit_fact SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", f.fact)
				f.project(t)
				expectedReason = "current_fact_expired"
			case "root-offline":
				f.exec(t, "UPDATE library_root SET health='offline' WHERE id=$1", f.root)
				expectedReason = "root_unavailable"
			case "tv-scope":
				f.exec(t, "UPDATE media SET media_type='tv' WHERE id=(SELECT media_id FROM media_unit WHERE id=$1)", f.unit)
				expectedReason = "standard_single_movie_required"
			}
			before := f.state(t)
			p := upgradeResult(t, f.call(f.body(), f.admin, nil))
			if p.Improvement || !strings.Contains(p.Reason, expectedReason) {
				t.Fatalf("%s: %+v wanted %s", name, p, expectedReason)
			}
			if before != f.state(t) {
				t.Fatal("held assessment wrote state")
			}
		})
	}
}
func TestUpgradePreviewPGLockWaitAndCancellation(t *testing.T) {
	for _, mode := range []string{"unchanged", "revoked", "cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			f := newUpgradeFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tx, e := f.a.DBPool().Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, e = tx.Exec(ctx, "SELECT id FROM app_user WHERE id=$1 FOR UPDATE", uuid.MustParse(f.actor.ID)); e != nil {
				t.Fatal(e)
			}
			callctx, stop := context.WithCancel(ctx)
			defer stop()
			if mode == "timeout" {
				callctx, stop = context.WithTimeout(ctx, 250*time.Millisecond)
				defer stop()
			}
			done := make(chan error, 1)
			go func() { _, e := f.svc.Preview(callctx, f.actor, f.request); done <- e }()
			selectionWaitFor(t, func() bool {
				var waiting bool
				_ = f.a.DBPool().QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%app_user%')").Scan(&waiting)
				return waiting
			})
			if mode == "cancel" {
				stop()
			} else if mode != "timeout" {
				if mode == "revoked" {
					if _, e = tx.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(f.actor.ID)); e != nil {
						t.Fatal(e)
					}
				}
				if e = tx.Commit(ctx); e != nil {
					t.Fatal(e)
				}
			}
			select {
			case e = <-done:
			case <-ctx.Done():
				t.Fatal("waiter did not finish")
			}
			if mode == "unchanged" && e != nil || mode == "revoked" && !errors.Is(e, ports.ErrUnavailable) || mode == "cancel" && !errors.Is(e, context.Canceled) || mode == "timeout" && !errors.Is(e, ports.ErrUnavailable) {
				t.Fatal(mode, e)
			}
			if mode == "revoked" {
				if _, e = f.svc.Preview(ctx, f.actor, f.request); !errors.Is(e, ports.ErrForbidden) {
					t.Fatal("revoked account accepted", e)
				}
			}
		})
	}
}
func TestUpgradePreviewPGExpiryAfterBlockedLastRead(t *testing.T) {
	for _, kind := range []string{"source", "projection", "fact"} {
		t.Run(kind, func(t *testing.T) {
			f := newUpgradeFixture(t)
			// This test crosses a ten-second expiry while holding the final row lock.
			// Give only its preview connection a longer lock wait; production keeps 2s.
			previewConfig := f.a.DBPool().Config()
			configureConnection := previewConfig.AfterConnect
			previewConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
				if configureConnection != nil {
					if err := configureConnection(ctx, conn); err != nil {
						return err
					}
				}
				_, err := conn.Exec(ctx, "SET lock_timeout='15s'")
				return err
			}
			previewPool, err := pgxpool.NewWithConfig(t.Context(), previewConfig)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(previewPool.Close)
			f.svc = acquisition.NewUpgradePreviewService(postgres.NewUpgradePreviewStore(previewPool))
			var expires time.Time
			var update string
			var id uuid.UUID
			switch kind {
			case "source":
				update = "UPDATE indexer_query SET finished_at=clock_timestamp()-interval '30 minutes'+interval '10 seconds' WHERE id=$1 RETURNING finished_at+interval '30 minutes'"
				id = f.query
			case "projection":
				update = "UPDATE wanted_item SET projection_valid_until=clock_timestamp()+interval '10 seconds' WHERE subscription_id=$1 RETURNING projection_valid_until"
				id = f.request.SubscriptionID
			case "fact":
				update = "UPDATE unit_fact SET expires_at=clock_timestamp()+interval '10 seconds' WHERE id=$1 RETURNING expires_at"
				id = f.fact
			}
			if e := f.a.DBPool().QueryRow(t.Context(), update, id).Scan(&expires); e != nil {
				t.Fatal(e)
			}
			if kind == "fact" {
				f.project(t)
			}
			// Prove the fixture is usable before expiry, including its projection stamp.
			initial, e := f.svc.Preview(t.Context(), f.actor, f.request)
			if e != nil || initial == nil || !initial.Improvement {
				t.Fatal("pre-expiry fixture unavailable", kind, initial, e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			tx, e := f.a.DBPool().Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, e = tx.Exec(ctx, "SELECT id FROM release_candidate WHERE id=$1 FOR UPDATE", f.request.CandidateID); e != nil {
				t.Fatal(e)
			}
			done := make(chan *ports.UpgradePreview, 1)
			errs := make(chan error, 1)
			go func() { p, e := f.svc.Preview(ctx, f.actor, f.request); done <- p; errs <- e }()
			selectionWaitFor(t, func() bool {
				var waiting bool
				_ = f.a.DBPool().QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%LockUpgradeCandidate%')").Scan(&waiting)
				return waiting
			})
			// Wait on the actual database deadline after the last read is blocked.
			if _, e = f.a.DBPool().Exec(ctx, "SELECT pg_sleep_until($1::timestamptz+interval '50 milliseconds')", expires); e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			p := <-done
			e = <-errs
			if e != nil || p == nil || p.Improvement || !strings.Contains(p.Reason, "expired") {
				t.Fatal(kind, p, e)
			}
		})
	}
}
func TestUpgradePreviewPGBackendUnavailable(t *testing.T) {
	f := newUpgradeFixture(t)
	f.exec(t, "ALTER TABLE release_candidate RENAME TO upgrade_backend_missing")
	defer f.exec(t, "ALTER TABLE upgrade_backend_missing RENAME TO release_candidate")
	w := f.call(f.body(), f.admin, nil)
	if w.Code != 503 || strings.Contains(w.Body.String(), "upgrade_backend_missing") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
func TestUpgradePreviewAppNativeCookie(t *testing.T) {
	f := newUpgradeFixture(t)
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.SecretKey = f.a.Config().SecretKey
	if e := library.EnsureDefaultRoots(context.Background(), postgres.NewLibraryBootstrapStore(f.a.DBPool()), rootfs.NewFilesystem(), idgen.New(), cfg.DataDir); e != nil {
		t.Fatal(e)
	}
	// Future schedules isolate HTTP from unrelated startup scan/health work.
	f.exec(t, "INSERT INTO library_scan_schedule(root_id,next_run_at) SELECT id,clock_timestamp()+interval '1 hour' FROM library_root ON CONFLICT(root_id) DO UPDATE SET next_run_at=excluded.next_run_at")
	f.exec(t, "UPDATE indexer SET health_next_check_at=clock_timestamp()+interval '1 hour'")
	cfg.Database.URL = f.a.Config().Database.URL
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	cfg.Server.HTTPAddr = listener.Addr().String()
	_ = listener.Close()
	startup := &runtimeTVStartup{ready: make(chan struct{})}
	a, e := app.New(cfg, app.WithLogger(logging.NewLogger(startup, "info")))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case e := <-done:
				if e != nil {
					t.Error(e)
				}
			case <-time.After(20 * time.Second):
				t.Error("App drain timeout")
				_ = a.Close()
			}
		})
	}
	defer stop()
	select {
	case <-startup.ready:
	case e := <-done:
		t.Fatal(e)
	case <-time.After(20 * time.Second):
		t.Fatal("App startup timeout")
	}
	before := f.state(t)
	base := "http://" + cfg.Server.HTTPAddr
	r, e := http.NewRequest("POST", base+"/api/v1/subscriptions/"+f.request.SubscriptionID.String()+"/upgrade-previews", bytes.NewBufferString(f.body()))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", base)
	r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: f.admin})
	client := &http.Client{Timeout: 3 * time.Second}
	resp, e := client.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	w.Code = resp.StatusCode
	w.Header().Set("Cache-Control", resp.Header.Get("Cache-Control"))
	_, _ = w.Body.Write(raw)
	p := upgradeResult(t, w)
	if !p.Comparable || !p.Improvement {
		t.Fatalf("App assessment %+v", p)
	}
	if before != f.state(t) {
		var kinds string
		_ = f.a.DBPool().QueryRow(context.Background(), "SELECT coalesce(jsonb_agg(jsonb_build_object('kind',kind,'state',state))::text,'[]') FROM job").Scan(&kinds)
		t.Fatalf("App state changed, jobs=%s:\nbefore:\n%safter:\n%s", kinds, before, f.state(t))
	}
}
