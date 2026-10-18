package httpapi_test

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"testing"
)

func verifyPersistedEpisodeCandidate(t *testing.T, f *episodeMappingFixture, path string) {
	t.Helper()
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
	exec("INSERT INTO release_candidate(id,indexer_id,guid,title,link_encrypted,size_bytes,quality_profile,source_config_version) VALUES($1,$2,'fixture','[Team] Fixture - 1 [1080p].mkv',$3,1024,'{}',$4)", candidate, idx, []byte("fixture"), version)
	exec("INSERT INTO indexer_query(id,indexer_id,query_type,keyword,page,config_version,state,results_count,finished_at) VALUES($1,$2,'search','Fixture',1,$3,'succeeded',1,clock_timestamp())", query, idx, version)
	exec("INSERT INTO indexer_query_candidate(query_id,candidate_id) VALUES($1,$2)", query, candidate)
	endpoint := path + "/explain-release"
	read := func(reason string, known bool) subscription.MappedReleaseExplanation {
		t.Helper()
		w := f.call(f.admin, "POST", endpoint, "", map[string]any{"candidate_id": candidate})
		var out subscription.MappedReleaseExplanation
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Reason != reason || out.CoverageKnown != known || out.ExecutionAllowed || out.CandidateID == nil || *out.CandidateID != candidate || len(out.CandidateInputStamp) != 64 || out.SourceCurrent == nil || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("persisted candidate proof", w.Code, w.Body.String())
		}
		return out
	}
	unbound := read("source_not_bound", false)
	bound, _ := json.Marshal([]uuid.UUID{idx})
	exec("UPDATE subscription SET indexer_ids=$2,intent_version=intent_version+1 WHERE id=$1", f.sub.Id, bound)
	valid := read("mapped_coverage_only", true)
	if unbound.CandidateInputStamp == valid.CandidateInputStamp || !(*valid.SourceCurrent) {
		t.Fatal("binding source not frozen")
	}
	for _, body := range []map[string]any{{}, {"candidate_id": candidate, "name": "[Team] Fixture - 1 [1080p]"}} {
		if w := f.call(f.admin, "POST", endpoint, "", body); w.Code != 400 {
			t.Fatal("ambiguous input accepted", w.Code)
		}
	}
	if w := f.call(f.member, "POST", endpoint, "", map[string]any{"candidate_id": candidate}); w.Code != 403 {
		t.Fatal("member candidate read", w.Code)
	}
	if w := f.call(f.admin, "POST", endpoint, "", map[string]any{"candidate_id": uuid.New()}); w.Code != 404 {
		t.Fatal("unknown candidate", w.Code)
	}
	exec("UPDATE indexer_query SET finished_at=clock_timestamp()-interval '31 minutes' WHERE id=$1", query)
	read("source_observation_expired", false)
	exec("UPDATE indexer_query SET finished_at=clock_timestamp() WHERE id=$1", query)
	exec("UPDATE indexer SET config_version=config_version+1 WHERE id=$1", idx)
	changed := read("source_changed", false)
	if changed.CandidateInputStamp == valid.CandidateInputStamp || *changed.SourceCurrent {
		t.Fatal("source version continued old proof")
	}
	var jobs int
	if e := pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE kind='download_submission'").Scan(&jobs); e != nil || jobs != 0 {
		t.Fatal("read-only candidate created work", jobs, e)
	}
}
