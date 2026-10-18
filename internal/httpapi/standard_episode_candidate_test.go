package httpapi_test

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestPersistedStandardEpisodeUsesCanonicalProviderIdentity(t *testing.T) {
	f := setupEpisodeMappingFixture(t)
	ctx := context.Background()
	pool := f.a.DBPool()
	idx, candidate, query := uuid.New(), uuid.New(), uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec("INSERT INTO indexer(id,name,url,api_key_encrypted,categories,health) VALUES($1,'fixture','http://fixture.invalid',$2,'[]','online')", idx, []byte("fixture"))
	var version int64
	if e := pool.QueryRow(ctx, "SELECT config_version FROM indexer WHERE id=$1", idx).Scan(&version); e != nil {
		t.Fatal(e)
	}
	bound, _ := json.Marshal([]uuid.UUID{idx})
	exec("UPDATE subscription SET indexer_ids=$2,intent_version=intent_version+1 WHERE id=$1", f.sub.Id, bound)
	exec("INSERT INTO release_candidate(id,indexer_id,guid,title,link_encrypted,size_bytes,quality_profile,source_config_version) VALUES($1,$2,'fixture','Fixture.2020.S01E01.1080p.WEB-DL.mkv',$3,1024,'{}',$4)", candidate, idx, []byte("fixture"), version)
	exec("INSERT INTO indexer_query(id,indexer_id,query_type,keyword,page,config_version,state,results_count,finished_at) VALUES($1,$2,'search','Fixture',1,$3,'succeeded',1,clock_timestamp())", query, idx, version)
	exec("INSERT INTO indexer_query_candidate(query_id,candidate_id) VALUES($1,$2)", query, candidate)
	read := func() subscription.MappedReleaseExplanation {
		t.Helper()
		w := f.call(f.admin, "POST", "/api/v1/subscriptions/"+f.sub.Id.String()+"/episode-mapping/explain-release", "", map[string]any{"candidate_id": candidate})
		var out subscription.MappedReleaseExplanation
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.ExecutionAllowed {
			t.Fatal("standard candidate", w.Code, w.Body.String())
		}
		return out
	}
	valid := read()
	if valid.Reason != "standard_coverage_only" || !valid.CoverageKnown || len(valid.Rows) != 1 || valid.Rows[0].UnitID != f.units[0] || valid.Rows[0].EpisodeID != 1001 || valid.MappingRevision != 0 {
		t.Fatal("canonical standard identity lost", valid)
	}
	if valid.QualityDecision != "hold" || valid.QualityReason != "quality_unknown" || valid.TargetReason == "" {
		t.Fatal("unknown persisted quality or target elevated", valid)
	}
	exec("UPDATE release_candidate SET title='Fixture.2020.S01E01E02.1080p.WEB-DL.mkv' WHERE id=$1", candidate)
	multi := read()
	if !multi.CoverageKnown || multi.Reason != "standard_multi_coverage_only" || len(multi.Rows) != 2 || multi.Rows[0].UnitID != f.units[0] || multi.Rows[1].UnitID != f.units[1] || multi.TargetReason != "multi_unit_review_required" || multi.ExecutionAllowed {
		t.Fatal("complete multi explanation", multi)
	}
	for _, key := range []string{"multi-review", "multi-review-again"} {
		w := f.call(f.admin, "POST", "/api/v1/subscriptions/"+f.sub.Id.String()+"/episode-candidate-reviews", key, map[string]any{"candidate_id": candidate, "expected_input_stamp": multi.CandidateInputStamp, "confirmed": true, "reason": "确认当前多集资源"})
		if w.Code != 400 {
			t.Fatal("multi explanation elevated single review", w.Code, w.Body.String())
		}
	}
	exec("UPDATE release_candidate SET title='Fixture.2020.S01E01-E03.1080p.WEB-DL.mkv' WHERE id=$1", candidate)
	missing := read()
	if missing.CoverageKnown || len(missing.Rows) != 0 || missing.Reason != "canonical_unit_unknown" {
		t.Fatal("unknown third unit returned partial coverage", missing)
	}
	// Four-digit syntax cannot create a provider unit or partially resolve a list.
	counts := func() [8]int {
		t.Helper()
		var n [8]int
		if e := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM media_unit),(SELECT count(*) FROM episode_candidate_review),(SELECT count(*) FROM download_task),(SELECT count(*) FROM job),(SELECT count(*) FROM unit_reservation),(SELECT count(*) FROM acquisition_reservation),(SELECT count(*) FROM download_requirement),(SELECT count(*) FROM transfer_plan)").Scan(&n[0], &n[1], &n[2], &n[3], &n[4], &n[5], &n[6], &n[7]); e != nil {
			t.Fatal(e)
		}
		return n
	}
	beforeHigh := counts()
	for _, token := range []string{"S01E1056", "1x1056", "S01E1056E1058", "S01E1056-E1058-E1060", "S01E01E1056", "S01E10000", "S01E1056.first-half"} {
		exec("UPDATE release_candidate SET title=$2 WHERE id=$1", candidate, "Fixture.2020."+token+".1080p.WEB-DL.x264.mkv")
		unknown := read()
		if unknown.CoverageKnown || len(unknown.Rows) != 0 || unknown.ExecutionAllowed {
			t.Fatal("unmatched high coverage resolved, truncated or inferred", token, unknown)
		}
		if token == "S01E1056" && unknown.Reason != "canonical_unit_unknown" {
			t.Fatal("explicit high episode was not parsed as canonical", unknown)
		}
		if w := f.call(f.admin, "POST", "/api/v1/subscriptions/"+f.sub.Id.String()+"/episode-candidate-reviews", "high-review-"+token, map[string]any{"candidate_id": candidate, "expected_input_stamp": unknown.CandidateInputStamp, "confirmed": true, "reason": "尝试确认没有有效标准单元的资源"}); w.Code != 400 {
			t.Fatal("unknown coverage review accepted", token, w.Code, w.Body.String())
		}
		if after := counts(); after != beforeHigh {
			t.Fatal("unknown high coverage wrote units or protected work", token, beforeHigh, after)
		}
	}
	exec("UPDATE release_candidate SET title='Fixture.2020.S01E01.1080p.WEB-DL.mkv' WHERE id=$1", candidate)

	reviewPath := "/api/v1/subscriptions/" + f.sub.Id.String() + "/episode-candidate-reviews"
	reviewBody := map[string]any{"candidate_id": candidate, "expected_input_stamp": valid.CandidateInputStamp, "confirmed": true, "reason": "确认当前资源对应选定作品单集"}
	if w := f.call(f.member, "POST", reviewPath, "review-member", reviewBody); w.Code != 403 {
		t.Fatal("member review authorized", w.Code)
	}
	exec("CREATE FUNCTION fail_episode_review_receipt() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'fixture rollback';END$$")
	exec("CREATE TRIGGER fail_episode_review_receipt BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION fail_episode_review_receipt()")
	if w := f.call(f.admin, "POST", reviewPath, "episode-review", reviewBody); w.Code != 503 {
		t.Fatal("receipt failure not atomic", w.Code, w.Body.String())
	}
	var rolled int
	if e := pool.QueryRow(ctx, "SELECT count(*) FROM episode_candidate_review").Scan(&rolled); e != nil || rolled != 0 {
		t.Fatal("review survived receipt failure", rolled, e)
	}
	exec("DROP TRIGGER fail_episode_review_receipt ON idempotency_record")
	responses := make(chan *httptest.ResponseRecorder, 8)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			responses <- f.call(f.admin, "POST", reviewPath, "episode-review", reviewBody)
		}()
	}
	group.Wait()
	close(responses)
	var reviewID uuid.UUID
	for w := range responses {
		var r struct {
			ID     uuid.UUID `json:"id"`
			Status string    `json:"status"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &r) != nil || r.Status != "reviewed_not_executable" || r.ID == uuid.Nil {
			t.Fatal("review replay", w.Code, w.Body.String())
		}
		if reviewID != uuid.Nil && reviewID != r.ID {
			t.Fatal("duplicate review IDs")
		}
		reviewID = r.ID
	}
	if e := pool.QueryRow(ctx, "SELECT count(*) FROM episode_candidate_review").Scan(&rolled); e != nil || rolled != 1 {
		t.Fatal("duplicate or lost review", rolled, e)
	}
	if _, e := pool.Exec(ctx, "UPDATE episode_candidate_review SET input_stamp=repeat('0',64) WHERE id=$1", reviewID); e == nil {
		t.Fatal("review evidence resigned")
	}
	exec("UPDATE release_candidate SET size_bytes=size_bytes+1 WHERE id=$1", candidate)
	if w := f.call(f.admin, "POST", reviewPath, "episode-review-stale", reviewBody); w.Code != 409 {
		t.Fatal("old snapshot approved", w.Code, w.Body.String())
	}
	if w := f.call(f.admin, "POST", reviewPath, "episode-review", reviewBody); w.Code != 200 {
		t.Fatal("lost ACK not original review", w.Code, w.Body.String())
	}

	if valid.IdentityDecision != "hold" || valid.IdentityReason != "media_identity_unproven" {
		t.Fatal("unknown parent year authorized identity", valid)
	}
	exec("UPDATE media SET title='Fixture',year=2020,metadata_version=metadata_version+1 WHERE id=$1", valid.MediaID)
	suggested := read()
	if suggested.IdentityDecision != "suggested" || suggested.IdentityReason != "exact_title_year_review" || suggested.CandidateInputStamp == valid.CandidateInputStamp || suggested.ExecutionAllowed {
		t.Fatal("parent metadata not snapshot review evidence", suggested)
	}
	exec("UPDATE media SET year=2021,metadata_version=metadata_version+1 WHERE id=$1", valid.MediaID)
	conflict := read()
	if conflict.IdentityDecision != "reject" || conflict.IdentityReason != "media_year_conflict" || conflict.ExecutionAllowed {
		t.Fatal("remake-year contradiction authorized", conflict)
	}
	exec("UPDATE media SET year=2020,metadata_version=metadata_version+1 WHERE id=$1", valid.MediaID)
	profile, _ := json.Marshal(quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceBluray, SourceParserVersion: quality.SourceTokenParserVersion, Codec: quality.CodecH264, CodecParserVersion: quality.CodecTokenParserVersion})
	exec("UPDATE release_candidate SET quality_profile=$2 WHERE id=$1", candidate, profile)
	qualified := read()
	if qualified.QualityDecision != "allow" || qualified.CandidateInputStamp == valid.CandidateInputStamp || qualified.ExecutionAllowed {
		t.Fatal("quality not separate/current", qualified)
	}
	// An old source substring is not current evidence even with a valid resolution.
	oldSource := []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray"}`)
	exec("UPDATE release_candidate SET quality_profile=$2 WHERE id=$1", candidate, oldSource)
	unknownSource := read()
	if unknownSource.QualityDecision == "allow" || unknownSource.ExecutionAllowed {
		t.Fatal("legacy source authorized candidate", unknownSource)
	}
	legacyHDRWithCurrentBase := []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"WEB-DL","source_parser_version":"source.tokens.v2","codec":"H264","codec_parser_version":"codec.tokens.v2","hdr":"DV"}`)
	exec("UPDATE release_candidate SET quality_profile=$2 WHERE id=$1", candidate, legacyHDRWithCurrentBase)
	legacyRead := read()
	if legacyRead.QualityDecision != "allow" || legacyRead.ExecutionAllowed {
		t.Fatal("legacy HDR removed base facts or gave permission", legacyRead)
	}
	var persistedQuality []byte
	if e := pool.QueryRow(ctx, "SELECT quality_profile FROM release_candidate WHERE id=$1", candidate).Scan(&persistedQuality); e != nil {
		t.Fatal(e)
	}
	var stored quality.Profile
	if e := json.Unmarshal(persistedQuality, &stored); e != nil || stored.HDR != "" {
		t.Fatal("old persisted HDR remained a fact", stored, e)
	}
	var original map[string]any
	if e := json.Unmarshal(persistedQuality, &original); e != nil || original["hdr"] != "DV" {
		t.Fatal("read rewrote original quality history", original, e)
	}
	rules := quality.PolicyRules{PreferHDR: true, CutoffScore: 25, ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 10}, SourceOrder: map[quality.Source]int{quality.SourceWEBDL: 5}}
	if evaluated := quality.EvaluateCandidate(rules, "Fixture.2020.S01E01", stored); evaluated.Score != 15 || evaluated.MeetsCutoff {
		t.Fatal("stored old HDR got rank/cutoff", evaluated)
	}
	exec("UPDATE release_candidate SET quality_profile=$2 WHERE id=$1", candidate, profile)
	qualified = read()

	// Missing historical group cannot bypass an exact negative raw-title suffix.
	groupPolicy := "dotted-group-" + uuid.NewString()
	groupRules, err := json.Marshal(quality.PolicyRules{ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300}, SourceOrder: map[quality.Source]int{quality.SourceBluray: 40}, RejectGroups: []string{"S.V", "BEN.THE.MEN"}})
	if err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO policy(id,name,current_revision) VALUES($1,'dotted group deny',1)", groupPolicy)
	exec("INSERT INTO policy_revision(id,policy_id,revision,rules) VALUES($1,$2,1,$3)", uuid.New(), groupPolicy, groupRules)
	exec("UPDATE subscription SET quality_policy_id=$2,quality_policy_revision=1,intent_version=intent_version+1 WHERE id=$1", f.sub.Id, groupPolicy)
	exec("UPDATE release_candidate SET title='Fixture.2020.S01E01.1080p.BluRay.x264-S.V.mkv' WHERE id=$1", candidate)
	var beforeRaw, afterRaw string
	if e := pool.QueryRow(ctx, "SELECT quality_profile::text FROM release_candidate WHERE id=$1", candidate).Scan(&beforeRaw); e != nil {
		t.Fatal(e)
	}
	deniedGroup := read()
	if deniedGroup.QualityDecision != "reject" || deniedGroup.ExecutionAllowed || deniedGroup.CandidateInputStamp == qualified.CandidateInputStamp {
		t.Fatal("missing group bypassed negative raw title", deniedGroup)
	}
	if e := pool.QueryRow(ctx, "SELECT quality_profile::text FROM release_candidate WHERE id=$1", candidate).Scan(&afterRaw); e != nil || afterRaw != beforeRaw {
		t.Fatal("raw negative re-signed historical quality", e)
	}
	var rawGroup map[string]any
	if e := json.Unmarshal([]byte(afterRaw), &rawGroup); e != nil || rawGroup["group"] != nil {
		t.Fatal("missing old group was certified", e)
	}
	exec("UPDATE release_candidate SET title='Fixture.2020.S01E01.1080p.BluRay.x264-BEN.THE.MEN.mkv' WHERE id=$1", candidate)
	unknownDottedGroup := read()
	if unknownDottedGroup.QualityDecision != "reject" || unknownDottedGroup.ExecutionAllowed {
		t.Fatal("unknown structured dotted group bypassed raw rejection", unknownDottedGroup)
	}
	if e := pool.QueryRow(ctx, "SELECT quality_profile::text FROM release_candidate WHERE id=$1", candidate).Scan(&afterRaw); e != nil || afterRaw != beforeRaw {
		t.Fatal("dotted-group rejection re-signed old observation", e)
	}
	for _, tag := range []string{"{tmdb-550}", "[imdb-tt0137523]", "(tt0137523)", "imdb-tt0137523", "tt0137523", "{tmdb-550}[ettv]", "[imdb-tt0137523][unreviewed]", "imdb-tt0137523[ettv]"} {
		exec("UPDATE release_candidate SET title=$2 WHERE id=$1", candidate, "Fixture.2020.S01E01.1080p.BluRay.x264-S.V."+tag+".mkv")
		identityGroup := read()
		if identityGroup.QualityDecision != "reject" || identityGroup.ExecutionAllowed {
			t.Fatal("external ID masked historical raw-group rejection", tag, identityGroup)
		}
		if e := pool.QueryRow(ctx, "SELECT quality_profile::text FROM release_candidate WHERE id=$1", candidate).Scan(&afterRaw); e != nil || afterRaw != beforeRaw {
			t.Fatal("identity decoration re-signed historical quality", tag, e)
		}
	}
	for _, tag := range []string{"[unreviewed-other]", "[nested[unreviewed-other]]", "[unreviewed-other][ettv]"} {
		exec("UPDATE release_candidate SET title=$2 WHERE id=$1", candidate, "Fixture.2020.S01E01.1080p.BluRay.x264-S.V"+tag+".mkv")
		denied := read()
		if denied.QualityDecision != "reject" || denied.ExecutionAllowed {
			t.Fatal("hyphenated unknown tag masked historical raw-group rejection", tag, denied)
		}
		if e := pool.QueryRow(ctx, "SELECT quality_profile::text FROM release_candidate WHERE id=$1", candidate).Scan(&afterRaw); e != nil || afterRaw != beforeRaw {
			t.Fatal("hyphenated tag rejection re-signed historical quality", tag, e)
		}
	}
	for _, tail := range []string{" mkv", " MP4", " mkv[ettv]"} {
		exec("UPDATE release_candidate SET title=$2 WHERE id=$1", candidate, "Fixture.2020.S01E01.1080p.BluRay.x264-S.V"+tail)
		denied := read()
		if denied.QualityDecision != "reject" || denied.ExecutionAllowed {
			t.Fatal("bare container hid historical raw group", tail, denied)
		}
		if e := pool.QueryRow(ctx, "SELECT quality_profile::text FROM release_candidate WHERE id=$1", candidate).Scan(&afterRaw); e != nil || afterRaw != beforeRaw {
			t.Fatal("container rejection re-signed historical quality", e)
		}
	}
	exec("UPDATE release_candidate SET title='Fixture.2020.S01E01.1080p.BluRay.x264-S.V2.mkv' WHERE id=$1", candidate)
	qualified = read()
	if qualified.QualityDecision != "allow" || qualified.ExecutionAllowed {
		t.Fatal("unrelated group denied", qualified)
	}

	exec("UPDATE wanted_item SET user_skipped=true,is_skipped=true WHERE subscription_id=$1 AND unit_id=$2", f.sub.Id, f.units[0])
	skipped := read()
	if skipped.TargetReason != "skipped" || skipped.CandidateInputStamp == qualified.CandidateInputStamp || !skipped.CoverageKnown || skipped.ExecutionAllowed {
		t.Fatal("skip lost or numbering promoted permission", skipped)
	}
	exec("UPDATE media_unit SET retired=true,metadata_version=metadata_version+1 WHERE id=$1", f.units[0])
	changed := read()
	if changed.CoverageKnown || len(changed.Rows) != 0 || changed.Reason != "canonical_unit_retired" || changed.CandidateInputStamp == valid.CandidateInputStamp {
		t.Fatal("retired canonical proof continued", changed)
	}
	if f.calls.Load() != 0 {
		t.Fatal("standard explanation fetched group or metadata")
	}
	var jobs int
	if e := pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE kind='download_submission'").Scan(&jobs); e != nil || jobs != 0 {
		t.Fatal("read-only standard candidate executed", jobs, e)
	}
}
