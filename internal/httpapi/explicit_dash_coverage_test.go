package httpapi_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestExplicitDashedCoverageExplainsExactUnitsWithoutExecution(t *testing.T) {
	f := setupEpisodeMappingFixture(t)
	ctx := context.Background()
	pool := f.a.DBPool()
	q := f.a.Queries()
	var parent uuid.UUID
	if e := pool.QueryRow(ctx, "SELECT media_id FROM media_unit WHERE id=$1", f.units[0]).Scan(&parent); e != nil {
		t.Fatal(e)
	}
	extra := map[int]uuid.UUID{}
	for _, ep := range []int{3, 5} {
		id := uuid.New()
		extra[ep] = id
		if _, e := q.CreateTVUnit(ctx, db.CreateTVUnitParams{ID: id, MediaID: parent, UnitKey: media.FormatEpisodeUnitKey(1, ep), SeasonNumber: pgtype.Int4{Int32: 1, Valid: true}, EpisodeNumber: pgtype.Int4{Int32: int32(ep), Valid: true}}); e != nil {
			t.Fatal(e)
		}
		if _, e := q.BindTMDBEpisodeIdentity(ctx, db.BindTMDBEpisodeIdentityParams{UnitID: id, ExternalID: int64(1000 + ep), TmdbSeriesID: 100}); e != nil {
			t.Fatal(e)
		}
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	idx, candidate, query := uuid.New(), uuid.New(), uuid.New()
	exec("INSERT INTO indexer(id,name,url,api_key_encrypted,categories,health) VALUES($1,'fixture','http://fixture.invalid',$2,'[]','online')", idx, []byte("fixture"))
	var version int64
	if e := pool.QueryRow(ctx, "SELECT config_version FROM indexer WHERE id=$1", idx).Scan(&version); e != nil {
		t.Fatal(e)
	}
	bound, _ := json.Marshal([]uuid.UUID{idx})
	exec("UPDATE subscription SET indexer_ids=$2,intent_version=intent_version+1 WHERE id=$1", f.sub.Id, bound)
	exec("INSERT INTO release_candidate(id,indexer_id,guid,title,link_encrypted,size_bytes,quality_profile,source_config_version) VALUES($1,$2,'fixture','Fixture.2020.S01E01-E03-E05.1080p.WEB-DL.mkv',$3,1024,'{}',$4)", candidate, idx, []byte("fixture"), version)
	exec("INSERT INTO indexer_query(id,indexer_id,query_type,keyword,page,config_version,state,results_count,finished_at) VALUES($1,$2,'search','Fixture',1,$3,'succeeded',1,clock_timestamp())", query, idx, version)
	exec("INSERT INTO indexer_query_candidate(query_id,candidate_id) VALUES($1,$2)", query, candidate)
	read := func() subscription.MappedReleaseExplanation {
		t.Helper()
		w := f.call(f.admin, "POST", "/api/v1/subscriptions/"+f.sub.Id.String()+"/episode-mapping/explain-release", "", map[string]any{"candidate_id": candidate})
		var out subscription.MappedReleaseExplanation
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.ExecutionAllowed {
			t.Fatal(w.Code, w.Body.String())
		}
		return out
	}
	counts := func() string {
		t.Helper()
		var result string
		if e := pool.QueryRow(ctx, "SELECT json_build_array((SELECT count(*) FROM job),(SELECT count(*) FROM acquisition_reservation),(SELECT count(*) FROM download_task),(SELECT count(*) FROM download_requirement),(SELECT count(*) FROM unit_reservation),(SELECT count(*) FROM episode_candidate_review))::text").Scan(&result); e != nil {
			t.Fatal(e)
		}
		return result
	}
	before := counts()
	out := read()
	if !out.CoverageKnown || out.Reason != "standard_multi_coverage_only" || out.TargetReason != "multi_unit_review_required" || len(out.Rows) != 3 {
		t.Fatal("explicit list lost or elevated coverage", out)
	}
	for i, id := range []uuid.UUID{f.units[0], extra[3], extra[5]} {
		if out.Rows[i].UnitID != id {
			t.Fatal("gaps expanded or canonical ID changed", out)
		}
	}
	w := f.call(f.admin, "POST", "/api/v1/subscriptions/"+f.sub.Id.String()+"/episode-candidate-reviews", "dash-list-review", map[string]any{"candidate_id": candidate, "expected_input_stamp": out.CandidateInputStamp, "confirmed": true, "reason": "复核明确三集"})
	if w.Code != 400 {
		t.Fatal("multi explanation became single approval", w.Code, w.Body.String())
	}
	exec("UPDATE release_candidate SET title='Fixture.2020.S01E01-E03-E04.1080p.WEB-DL.mkv' WHERE id=$1", candidate)
	unknown := read()
	if unknown.CoverageKnown || len(unknown.Rows) != 0 || unknown.Reason != "canonical_unit_unknown" {
		t.Fatal("unknown fourth canonical unit leaked partial authority", unknown)
	}
	exec("UPDATE release_candidate SET title='Fixture.2020.S01E01-E03-E05.Sample.mkv' WHERE id=$1", candidate)
	partial := read()
	if partial.CoverageKnown || len(partial.Rows) != 0 {
		t.Fatal("partial list elevated", partial)
	}
	if after := counts(); after != before {
		t.Fatal("explanation/rejected review manufactured execution", before, after)
	}
	var raw string
	if e := pool.QueryRow(ctx, "SELECT quality_profile::text FROM release_candidate WHERE id=$1", candidate).Scan(&raw); e != nil || raw != "{}" {
		t.Fatal("old persisted quality rewritten", raw, e)
	}
}
