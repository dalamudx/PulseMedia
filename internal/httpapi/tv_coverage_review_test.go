package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type coverageFixture struct {
	f                  *episodeMappingFixture
	svc                *subscription.TVCoverageCandidateReviewService
	actor              *ports.Actor
	candidate, indexer uuid.UUID
	handler            http.Handler
}

func newCoverageFixture(t *testing.T, third ...bool) *coverageFixture {
	t.Helper()
	f := setupEpisodeMappingFixture(t)
	ctx := context.Background()
	p := f.a.DBPool()
	idx, candidate, query := uuid.New(), uuid.New(), uuid.New()
	run := func(sql string, args ...any) {
		t.Helper()
		if _, e := p.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	credential, err := crypto.Encrypt(make([]byte, 32), []byte("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	link, err := crypto.Encrypt(make([]byte, 32), []byte("http://fixture.invalid/torrent"))
	if err != nil {
		t.Fatal(err)
	}
	run("INSERT INTO indexer(id,name,url,api_key_encrypted,categories,health) VALUES($1,'coverage fixture','http://fixture.invalid',$2,'[]','online')", idx, credential)
	run("UPDATE subscription SET indexer_ids=jsonb_build_array($2::text),intent_version=intent_version+1 WHERE id=$1", f.sub.Id, idx.String())
	run("INSERT INTO release_candidate(id,indexer_id,guid,title,link_encrypted,size_bytes,quality_profile,source_config_version) SELECT $1,$2,'coverage','Mapping.Browser.Show.2020.S01E01E02.1080p.WEB-DL.mkv',$3,1024,'{}',config_version FROM indexer WHERE id=$2", candidate, idx, link)
	run("INSERT INTO indexer_query(id,indexer_id,query_type,keyword,page,config_version,state,results_count,finished_at) SELECT $1,$2,'search','fixture',1,config_version,'succeeded',1,clock_timestamp() FROM indexer WHERE id=$2", query, idx)
	run("INSERT INTO indexer_query_candidate(query_id,candidate_id) VALUES($1,$2)", query, candidate)
	// Independently specified provider response, not parser output or a generated count/range.
	seasons := []ports.TVSeasonMetadata{{SeasonNumber: 1, Complete: true, Episodes: []ports.TVEpisodeMetadata{{TMDBEpisodeID: 1001, EpisodeNumber: 1}, {TMDBEpisodeID: 1002, EpisodeNumber: 2}}}}
	if len(third) > 0 && third[0] {
		seasons[0].Episodes = append(seasons[0].Episodes, ports.TVEpisodeMetadata{TMDBEpisodeID: 1003, EpisodeNumber: 3})
		run("UPDATE release_candidate SET title='Mapping.Browser.Show.2020.S01E01E02E03.1080p.WEB-DL.mkv' WHERE id=$1", candidate)
	}
	authSvc := auth.NewService(postgres.NewAuthenticationStore(p), crypto.AuthenticationSecrets{}, clock.New(), idgen.New())
	actor, e := authSvc.AuthenticateToken(ctx, f.admin)
	if e != nil {
		t.Fatal(e)
	}
	saved, e := f.a.Queries().GetSubscriptionByID(ctx, f.sub.Id)
	if e != nil {
		t.Fatal(e)
	}
	refresh := postgres.NewTVRefreshStore(p, idgen.New())
	input, _, e := refresh.ReadTVRefreshContext(ctx, actor, f.sub.Id, ports.TVRefreshCommand{ExpectedVersion: saved.IntentVersion}, "coverage-metadata", "coverage-metadata")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = refresh.ApplyTVRefresh(ctx, actor, input, ports.TVRefreshCommand{ExpectedVersion: input.IntentVersion, Seasons: []int{1}}, seasons, "coverage-metadata", "coverage-metadata"); e != nil {
		t.Fatal(e)
	}
	mapping := postgres.NewEpisodeMappingStore(postgres.NewEpisodeSourceStore(refresh), idgen.New())
	svc := subscription.NewTVCoverageCandidateReviewService(postgres.NewTVCoverageCandidateReviewStore(mapping))
	return &coverageFixture{f, svc, actor, candidate, idx, f.router}
}
func (f *coverageFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, e := f.f.a.DBPool().Exec(context.Background(), sql, args...); e != nil {
		t.Fatal(e)
	}
}
func (f *coverageFixture) preview(t *testing.T) *ports.TVCoverageCandidateReviewObservation {
	t.Helper()
	v, e := f.svc.Preview(context.Background(), f.actor, f.f.sub.Id, f.candidate)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func (f *coverageFixture) command(v *ports.TVCoverageCandidateReviewObservation) ports.TVCoverageCandidateReviewCommand {
	return ports.TVCoverageCandidateReviewCommand{SubscriptionID: f.f.sub.Id, CandidateID: f.candidate, ExpectedInputStamp: v.InputStamp, Confirmed: true, Reason: "Reviewed explicit whole-season candidate only"}
}
func (f *coverageFixture) counts(t *testing.T) [10]int {
	t.Helper()
	var v [10]int
	e := f.f.a.DBPool().QueryRow(context.Background(), "SELECT (SELECT count(*) FROM job),(SELECT count(*) FROM download_task),(SELECT count(*) FROM unit_reservation),(SELECT count(*) FROM acquisition_reservation),(SELECT count(*) FROM download_requirement),((SELECT count(*) FROM episode_execution_authority)+(SELECT count(*) FROM transfer_plan)),(SELECT count(*) FROM tv_coverage_candidate_review),(SELECT count(*) FROM audit_log WHERE action='tv_coverage_candidate.reviewed'),(SELECT count(*) FROM outbox_event WHERE event_type='tv_coverage_candidate.reviewed'),(SELECT count(*) FROM idempotency_record WHERE resource_id IN (SELECT id::text FROM tv_coverage_candidate_review))").Scan(&v[0], &v[1], &v[2], &v[3], &v[4], &v[5], &v[6], &v[7], &v[8], &v[9])
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestTVCoverageReviewNativePGAtomicRecovery(t *testing.T) {
	f := newCoverageFixture(t)
	ctx := context.Background()
	v := f.preview(t)
	again := f.preview(t)
	if !v.CanReview || v.ExecutionAllowed || len(v.Rows) != 2 || v.Rows[0].UnitID != f.f.units[0] || v.Rows[1].UnitID != f.f.units[1] || v.InputStamp != again.InputStamp || v.MaterialStamp != again.MaterialStamp {
		t.Fatal("unstable or incorrect complete evidence", v, again)
	}
	before := f.counts(t)
	c := f.command(v)
	for _, table := range []string{"audit_log", "idempotency_record"} {
		t.Run("late-"+table, func(t *testing.T) {
			f.exec(t, "CREATE FUNCTION fail_coverage_write() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'late fixture failure'; END$$")
			f.exec(t, "CREATE TRIGGER fail_coverage_write BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION fail_coverage_write()")
			if _, e := f.svc.Review(ctx, f.actor, c, "coverage-recovery"); !errors.Is(e, ports.ErrUnavailable) {
				t.Fatal("failure not surfaced", e)
			}
			f.exec(t, "DROP TRIGGER fail_coverage_write ON "+table)
			f.exec(t, "DROP FUNCTION fail_coverage_write()")
			if got := f.counts(t); got != before {
				t.Fatal("late failure leaked writes", before, got)
			}
		})
	}
	var wg sync.WaitGroup
	receipts := make(chan *ports.TVCoverageCandidateReviewReceipt, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := f.svc.Review(ctx, f.actor, c, "coverage-recovery")
			receipts <- r
			errs <- e
		}()
	}
	wg.Wait()
	close(receipts)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var id uuid.UUID
	for r := range receipts {
		if r == nil || r.Status != "reviewed_not_executable" || r.MaterialStamp != v.MaterialStamp {
			t.Fatal(r)
		}
		if id != uuid.Nil && id != r.ID {
			t.Fatal("duplicate review")
		}
		id = r.ID
	}
	after := f.counts(t)
	for i := 0; i < 6; i++ {
		if after[i] != before[i] {
			t.Fatal("review created execution work", before, after)
		}
	}
	for i := 6; i < 10; i++ {
		if after[i] != before[i]+1 {
			t.Fatal("not exactly one transactional result", before, after)
		}
	}
	if _, e := f.f.a.DBPool().Exec(ctx, "UPDATE tv_coverage_candidate_review SET reason='resigned' WHERE id=$1", id); e == nil {
		t.Fatal("immutable review changed")
	}
	f.exec(t, "UPDATE release_candidate SET size_bytes=size_bytes+1 WHERE id=$1", f.candidate)
	if _, e := f.svc.Review(ctx, f.actor, c, "coverage-new"); !errors.Is(e, ports.ErrTVCoverageReviewConflict) {
		t.Fatal("stale review accepted", e)
	}
	r, e := f.svc.Review(ctx, f.actor, c, "coverage-recovery")
	if e != nil || r.ID != id || f.counts(t) != after {
		t.Fatal("lost receipt repeated writes", r, e)
	}
	// HTTP uses a real persisted session and the independently registered private path.
	body, _ := json.Marshal(map[string]any{"candidate_id": c.CandidateID, "expected_input_stamp": c.ExpectedInputStamp, "confirmed": true, "reason": c.Reason})
	req := httptest.NewRequest("POST", "/api/v1/subscriptions/"+f.f.sub.Id.String()+"/tv-coverage-candidate-reviews", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.f.admin)
	req.Header.Set("Idempotency-Key", "coverage-recovery")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	t.Logf("8 concurrent same-key commands: one review/audit/outbox/receipt; work counts unchanged; private HTTP=%d", w.Code)
}
func TestTVCoverageReviewNativePGInvalidEvidence(t *testing.T) {
	for _, name := range []string{"legacy-no-proof", "missing-middle", "extra", "retired", "unselected", "unknown-provider", "complete-title", "pack-title", "ova-title", "nonstandard"} {
		t.Run(name, func(t *testing.T) {
			f := newCoverageFixture(t, name == "missing-middle")
			v := f.preview(t)
			if !v.CanReview {
				t.Fatal("baseline", v.HoldReason)
			}
			switch name {
			case "legacy-no-proof":
				f.exec(t, "UPDATE media SET metadata_version=metadata_version+1 WHERE id=(SELECT media_id FROM subscription WHERE id=$1)", f.f.sub.Id)
			case "missing-middle":
				f.exec(t, "UPDATE release_candidate SET title='Mapping.Browser.Show.S01E01E03.1080p.WEB-DL.mkv' WHERE id=$1", f.candidate)
			case "extra":
				f.exec(t, "UPDATE release_candidate SET title='Mapping.Browser.Show.S01E01E02E03.1080p.WEB-DL.mkv' WHERE id=$1", f.candidate)
			case "retired":
				f.exec(t, "UPDATE media_unit SET retired=true,metadata_version=metadata_version+1 WHERE id=$1", f.f.units[1])
			case "unselected":
				f.exec(t, "UPDATE wanted_item SET is_skipped=true,user_skipped=true WHERE subscription_id=$1 AND unit_id=$2", f.f.sub.Id, f.f.units[1])
			case "unknown-provider":
				f.exec(t, "INSERT INTO media_unit(id,media_id,unit_key,season_number,episode_number) SELECT $1,media_id,'S01E03',1,3 FROM subscription WHERE id=$2", uuid.New(), f.f.sub.Id)
			case "complete-title", "pack-title", "ova-title":
				f.exec(t, "UPDATE release_candidate SET title=$2 WHERE id=$1", f.candidate, "Mapping.Browser.Show.S01E01E02."+name+".1080p.WEB-DL.mkv")
			case "nonstandard":
				f.exec(t, "INSERT INTO episode_mapping_revision(subscription_id,revision,media_id,media_version,intent_version,mode,input_stamp,source_hash,source_snapshot,mappings) SELECT id,1,media_id,(SELECT metadata_version FROM media WHERE id=media_id),intent_version+1,'manual',repeat('a',64),repeat('b',64),'{}',jsonb_build_array(jsonb_build_object('source_number',25,'unit_id',(SELECT id FROM media_unit WHERE media_id=subscription.media_id ORDER BY id LIMIT 1),'unit_key',(SELECT unit_key FROM media_unit WHERE media_id=subscription.media_id ORDER BY id LIMIT 1),'unit_version',(SELECT metadata_version FROM media_unit WHERE media_id=subscription.media_id ORDER BY id LIMIT 1))) FROM subscription WHERE id=$1", f.f.sub.Id)
				f.exec(t, "UPDATE subscription SET episode_mapping_revision=1,intent_version=intent_version+1 WHERE id=$1", f.f.sub.Id)
			}
			observed := f.preview(t)
			if observed.CanReview || observed.ExecutionAllowed {
				t.Fatal("invalid coverage became reviewable", name, observed.HoldReason, observed.Rows)
			}
			if _, e := f.svc.Review(context.Background(), f.actor, f.command(observed), "invalid"); !errors.Is(e, ports.ErrTVCoverageReviewConflict) {
				t.Fatal("invalid proof reviewed", e)
			}
		})
	}
}
func TestTVCoverageReviewNativePGStaleVersions(t *testing.T) {
	mutations := []struct{ name, sql string }{
		{"size", "UPDATE release_candidate SET size_bytes=size_bytes+1 WHERE id=$1"},
		{"cipher", "UPDATE release_candidate SET link_encrypted='changed'::bytea WHERE id=$1"},
		{"reference", "UPDATE release_candidate SET guid='changed' WHERE id=$1"},
		{"provider", "UPDATE media_identity SET external_id='101' WHERE media_id=(SELECT media_id FROM subscription WHERE id=$2) AND namespace='tmdb.tv'"},
		{"expired-source", "UPDATE indexer_query SET finished_at=clock_timestamp()-interval '31 minutes' WHERE indexer_id=(SELECT indexer_id FROM release_candidate WHERE id=$1)"},
		{"source", "UPDATE indexer SET config_version=config_version+1 WHERE id=(SELECT indexer_id FROM release_candidate WHERE id=$1)"},
		{"unit", "UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=(SELECT unit_id FROM wanted_item WHERE subscription_id=$2 ORDER BY unit_id LIMIT 1)"},
		{"metadata", "UPDATE media SET metadata_version=metadata_version+1 WHERE id=(SELECT media_id FROM subscription WHERE id=$2)"},
		{"policy", "UPDATE subscription SET quality_policy_revision=quality_policy_revision+1 WHERE id=$2"},
		{"intent", "UPDATE subscription SET intent_version=intent_version+1 WHERE id=$2"},
		{"root", "UPDATE library_root SET generation=generation+1 WHERE id=(SELECT root_id FROM subscription WHERE id=$2)"},
	}
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			f := newCoverageFixture(t)
			v := f.preview(t)
			sql := m.sql
			// Include both typed parameters even for one-parameter statements.
			sql = "WITH fixture AS (SELECT $1::uuid,$2::uuid) " + sql
			f.exec(t, sql, f.candidate, f.f.sub.Id)
			if _, e := f.svc.Review(context.Background(), f.actor, f.command(v), "stale-"+m.name); !errors.Is(e, ports.ErrTVCoverageReviewConflict) {
				t.Fatal("stale accepted", m.name, e)
			}
		})
	}
}
func TestTVCoverageReviewNativePGAccountFirst(t *testing.T) {
	f := newCoverageFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := f.command(f.preview(t))
	p := f.f.a.DBPool()
	tx, e := p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, e = tx.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(f.actor.ID)); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() { _, e := f.svc.Review(ctx, f.actor, c, "blocked-account"); result <- e }()
	// A bounded database wait verifies the command is actually waiting on the account.
	deadline := time.Now().Add(3 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		if e = p.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%app_user%FOR%SHARE%')").Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("account lock wait not observed")
	}
	probe, e := p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = probe.Exec(ctx, "SELECT id FROM library_root WHERE id=(SELECT root_id FROM subscription WHERE id=$1) FOR UPDATE NOWAIT", f.f.sub.Id); e != nil {
		t.Fatal("root held while waiting for account", e)
	}
	_ = probe.Rollback(ctx)
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-result; !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("revoked account wrote review", e)
	}
	t.Log("account lock wait observed; root NOWAIT succeeds; invalidated account rejects")
}
