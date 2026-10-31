package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	usecaseSubscription "github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"sync"
	"testing"
	"time"
)

func tvEpisodeDiscoveryFixture(t *testing.T) (*tvUpgradeFixture, *postgres.TVEpisodeDiscoveryStore, ports.TVEpisodeDiscoveryUnit) {
	t.Helper()
	f := newTVUpgradeFixture(t)
	if _, e := usecaseSubscription.NewEvaluator(postgres.NewWantedProjectionStore(f.pool)).EvaluateWantedItem(t.Context(), f.wanted, quality.Policy{ID: "mp22", Revision: 1}); e != nil {
		t.Fatal("project satisfied imported episode", e)
	}
	return f, postgres.NewTVEpisodeDiscoveryStore(f.pool, idgen.New()), ports.TVEpisodeDiscoveryUnit{SubscriptionID: f.sub, UnitID: f.unit}
}
func tvEpisodeDiscoveryState(t *testing.T, f *tvUpgradeFixture) string {
	t.Helper()
	var state string
	e := f.pool.QueryRow(t.Context(), `SELECT jsonb_build_array((SELECT count(*) FROM download_task),(SELECT count(*) FROM unit_reservation),(SELECT count(*) FROM acquisition_reservation),(SELECT count(*) FROM download_requirement),(SELECT count(*) FROM search_round),(SELECT count(*) FROM tv_episode_upgrade_authority),(SELECT jsonb_agg(to_jsonb(f) ORDER BY f.id) FROM library_file f),(SELECT jsonb_agg(to_jsonb(f) ORDER BY f.id) FROM unit_fact f))::text`).Scan(&state)
	if e != nil {
		t.Fatal(e)
	}
	return state
}
func TestTVEpisodeDiscoveryCadenceRestartPG(t *testing.T) {
	f, s, key := tvEpisodeDiscoveryFixture(t)
	ctx := t.Context()
	before := tvEpisodeDiscoveryState(t, f)
	var wg sync.WaitGroup
	results := make(chan bool, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); ok, e := s.ScheduleTVEpisodeDiscovery(ctx, key); results <- ok; errs <- e }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	count := 0
	for ok := range results {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("created %d runs", count)
	}
	status, e := s.ReadTVEpisodeDiscovery(ctx, f.actor, key)
	if e != nil || status.SearchID == nil || status.CurrentFileID == nil || *status.CurrentFileID != f.request.CurrentFileID || status.UnitID != f.unit || status.ExecutionAllowed {
		t.Fatalf("status %+v %v", status, e)
	}
	if status.NextDiscoveryAt == nil || time.Until(*status.NextDiscoveryAt) < 5*time.Hour || time.Until(*status.NextDiscoveryAt) > 6*time.Hour {
		t.Fatal("cooldown", status)
	}
	restarted := usecaseSearch.NewTVEpisodeDiscoveryService(postgres.NewTVEpisodeDiscoveryStore(f.pool, idgen.New()))
	if n, e := restarted.RunBatch(ctx, 20); e != nil || n != 0 {
		t.Fatal("restart", n, e)
	}
	execIntent(t, f.pool, `UPDATE subscription_tv_episode_discovery SET next_discovery_at=clock_timestamp()-interval '1 second' WHERE subscription_id=$1 AND unit_id=$2`, f.sub, f.unit)
	if n, e := restarted.RunBatch(ctx, 20); e != nil || n != 1 {
		t.Fatal("next cycle", n, e)
	}
	next, e := s.ReadTVEpisodeDiscovery(ctx, f.actor, key)
	if e != nil || next.SearchID == nil || *next.SearchID == *status.SearchID {
		t.Fatal("new immutable run", next, e)
	}
	for _, sql := range []string{`DELETE FROM subscription_tv_episode_discovery_run WHERE search_id=$1`, `UPDATE subscription_tv_episode_discovery_run SET input_stamp=repeat('a',64) WHERE search_id=$1`, `TRUNCATE subscription_tv_episode_discovery_run CASCADE`} {
		if _, e := f.pool.Exec(ctx, sql, func() []any {
			if sql == `TRUNCATE subscription_tv_episode_discovery_run CASCADE` {
				return nil
			}
			return []any{*status.SearchID}
		}()...); e == nil {
			t.Fatal("immutable run changed", sql)
		}
	}
	if after := tvEpisodeDiscoveryState(t, f); after != before {
		t.Fatal("discovery changed acquisition authority or library")
	}
}
func TestTVEpisodeDiscoveryCurrentInputsPG(t *testing.T) {
	for _, name := range []string{"intent", "policy", "root", "root-generation", "permission", "role", "mode", "file", "scope", "identity", "episode", "air-date", "fact"} {
		t.Run(name, func(t *testing.T) {
			f, s, key := tvEpisodeDiscoveryFixture(t)
			ctx := t.Context()
			if ok, e := s.ScheduleTVEpisodeDiscovery(ctx, key); e != nil || !ok {
				t.Fatal("schedule", ok, e)
			}
			status, e := s.ReadTVEpisodeDiscovery(ctx, f.actor, key)
			if e != nil || status.SearchID == nil {
				t.Fatal(status, e)
			}
			id := *status.SearchID
			switch name {
			case "intent":
				execIntent(t, f.pool, `UPDATE subscription SET intent_version=intent_version+1 WHERE id=$1`, f.sub)
			case "policy":
				execIntent(t, f.pool, `UPDATE policy SET current_revision=current_revision+1 WHERE id='mp22'`)
			case "root":
				execIntent(t, f.pool, `UPDATE library_root SET health='offline' WHERE id=$1`, f.tv)
			case "root-generation":
				execIntent(t, f.pool, `UPDATE library_root SET generation=generation+1 WHERE id=$1`, f.tv)
			case "permission":
				execIntent(t, f.pool, `UPDATE app_user SET is_active=false WHERE id=$1`, f.actor.ID)
			case "role":
				execIntent(t, f.pool, `UPDATE app_user SET role='member' WHERE id=$1`, f.actor.ID)
			case "mode":
				execIntent(t, f.pool, `UPDATE subscription SET upgrade_mode='disabled' WHERE id=$1`, f.sub)
			case "file":
				execIntent(t, f.pool, `UPDATE library_file SET mapping_revision=mapping_revision+1 WHERE id=$1`, f.request.CurrentFileID)
			case "scope":
				execIntent(t, f.pool, `UPDATE subscription SET scope_type='all',seasons='[9]' WHERE id=$1`, f.sub)
			case "identity":
				execIntent(t, f.pool, `UPDATE media_identity SET external_id='991234' WHERE media_id=(SELECT media_id FROM subscription WHERE id=$1) AND namespace='tmdb.tv'`, f.sub)
			case "episode":
				execIntent(t, f.pool, `UPDATE media_unit SET episode_number=episode_number+1 WHERE id=$1`, f.unit)
			case "air-date":
				execIntent(t, f.pool, `UPDATE media_unit SET air_date=CURRENT_DATE+1 WHERE id=$1`, f.unit)
			case "fact":
				execIntent(t, f.pool, `UPDATE unit_fact SET expires_at=clock_timestamp()-interval '1 second' WHERE source_file_id=$1`, f.request.CurrentFileID)
			}
			tx, e := f.pool.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			e = postgres.AuthorizeTVEpisodeDiscoverySearch(ctx, db.New(tx), id)
			_ = tx.Rollback(ctx)
			if e == nil {
				t.Fatal("stale search authorized")
			}
			searches := postgres.NewInteractiveSearchStore(f.pool, idgen.New())
			if _, e = searches.ReadInteractiveSearch(ctx, f.actor, id); e == nil {
				t.Fatal("stale candidates exposed")
			}
			task := interactiveClaim(t, f.pool, &ports.InteractiveSearchHeader{ID: id}, f.indexer)
			if _, e = searches.LoadInteractiveSearchSource(ctx, task); e == nil {
				t.Fatal("stale source dispatched")
			}
		})
	}
}
func TestTVEpisodeDiscoveryIneligiblePG(t *testing.T) {
	for _, name := range []string{"not-aired", "unknown-air-date", "special", "absolute", "multi-unit", "ambiguous", "unknown-quality", "cutoff", "disabled", "scope", "not-satisfied", "protected"} {
		t.Run(name, func(t *testing.T) {
			f, s, key := tvEpisodeDiscoveryFixture(t)
			switch name {
			case "not-aired":
				execIntent(t, f.pool, `UPDATE media_unit SET air_date=CURRENT_DATE+1 WHERE id=$1`, f.unit)
			case "unknown-air-date":
				execIntent(t, f.pool, `UPDATE media_unit SET air_date=NULL WHERE id=$1`, f.unit)
			case "special":
				execIntent(t, f.pool, `UPDATE media_unit SET season_number=0,unit_key='S00E03' WHERE id=$1`, f.unit)
			case "absolute":
				execIntent(t, f.pool, `UPDATE media_unit SET absolute_number=3 WHERE id=$1`, f.unit)
			case "multi-unit":
				u := uuid.New()
				execIntent(t, f.pool, `INSERT INTO media_unit(id,media_id,unit_key,season_number,episode_number,metadata_version) SELECT $1,media_id,'S02E04',2,4,1 FROM media_unit WHERE id=$2`, u, f.unit)
				execIntent(t, f.pool, `INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, f.request.CurrentFileID, u)
			case "ambiguous":
				file := uuid.New()
				execIntent(t, f.pool, `INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) VALUES($1,$2,'another.mkv',100,1)`, file, f.tv)
				execIntent(t, f.pool, `INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, file, f.unit)
			case "unknown-quality":
				execIntent(t, f.pool, `UPDATE unit_fact SET quality_profile='{}' WHERE source_file_id=$1`, f.request.CurrentFileID)
			case "cutoff":
				execIntent(t, f.pool, `INSERT INTO policy_revision(id,policy_id,revision,rules) SELECT $1,policy_id,2,jsonb_set(rules,'{cutoff_score}','1') FROM policy_revision WHERE policy_id='mp22' AND revision=1`, uuid.New())
				execIntent(t, f.pool, `UPDATE policy SET current_revision=2 WHERE id='mp22'`)
				execIntent(t, f.pool, `UPDATE subscription SET quality_policy_revision=2 WHERE id=$1`, f.sub)
			case "disabled":
				execIntent(t, f.pool, `UPDATE subscription SET upgrade_mode='disabled' WHERE id=$1`, f.sub)
			case "scope":
				execIntent(t, f.pool, `UPDATE subscription SET scope_type='all',seasons='[9]' WHERE id=$1`, f.sub)
			case "not-satisfied":
				execIntent(t, f.pool, `UPDATE wanted_item SET is_satisfied=false WHERE id=$1`, f.wanted)
			case "protected":
				execIntent(t, f.pool, `INSERT INTO unit_reservation(id,unit_id,root_id,purpose,candidate_id,operation_id,state) VALUES($1,$2,$3,'upgrade',$4,$5,'reserved')`, uuid.New(), f.unit, f.tv, f.request.CandidateID, uuid.New())
			}
			if ok, e := s.ScheduleTVEpisodeDiscovery(t.Context(), key); e != nil || ok {
				t.Fatal("ineligible scheduled", ok, e)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM subscription_tv_episode_discovery_run`); n != 0 {
				t.Fatal("ineligible run", n)
			}
		})
	}
}

type tvEpisodeDiscoveryPageClient struct {
	calls int
	after func()
}

func (c *tvEpisodeDiscoveryPageClient) Search(ctx context.Context, k string, cats []int32, o, l int32) ([]*ports.ReleaseItem, error) {
	p, e := c.SearchPage(ctx, k, cats, o, l)
	if e != nil {
		return nil, e
	}
	return p.Items, nil
}
func (c *tvEpisodeDiscoveryPageClient) FetchTorrent(context.Context, string) ([]byte, error) {
	return nil, ports.ErrRejected
}
func (c *tvEpisodeDiscoveryPageClient) SearchPage(_ context.Context, k string, _ []int32, o, l int32) (*ports.IndexerPage, error) {
	c.calls++
	if k != "Trusted Native Series 2024 S02E03" || (o != 0 && o != 50) || l != 50 {
		return nil, ports.ErrBadResponse
	}
	count := 50
	if o == 50 {
		count = 1
	}
	total := int64(51)
	items := make([]*ports.ReleaseItem, 0, count)
	for i := range count {
		n := int(o) + i
		resolution := "1080p"
		if n == 50 {
			resolution = "2160p"
		}
		items = append(items, &ports.ReleaseItem{GUID: fmt.Sprintf("tv-discovery-%d", n), Title: fmt.Sprintf("Trusted.Native.Series.2024.S02E03.%s.WEB-DL.H264.mkv", resolution), DownloadURL: fmt.Sprintf("https://tv-source.test/item/%d", n), SizeBytes: 1234, Seeders: 5})
	}
	if c.after != nil {
		c.after()
		c.after = nil
	}
	return &ports.IndexerPage{Offset: o, Limit: l, OffsetVerified: true, Total: &total, Items: items}, nil
}
func TestTVEpisodeDiscoveryProductionSearchPreviewPG(t *testing.T) {
	f, s, key := tvEpisodeDiscoveryFixture(t)
	ctx := t.Context()
	before := tvEpisodeDiscoveryState(t, f)
	scheduler := usecaseSearch.NewTVEpisodeDiscoveryService(s)
	if n, e := scheduler.RunBatch(ctx, 20); e != nil || n != 1 {
		t.Fatal("tick", n, e)
	}
	status, e := scheduler.Read(ctx, f.actor, key.SubscriptionID, key.UnitID)
	if e != nil || status.SearchID == nil {
		t.Fatal(status, e)
	}
	searches := postgres.NewInteractiveSearchStore(f.pool, idgen.New())
	task := interactiveClaim(t, f.pool, &ports.InteractiveSearchHeader{ID: *status.SearchID}, f.indexer)
	lease, e := searches.LoadInteractiveSearchSource(ctx, task)
	if e != nil {
		t.Fatal("load", e)
	}
	codec := crypto.NewSecretProtector(bytes.Repeat([]byte{27}, 32))
	executor := usecaseSearch.NewService(postgres.NewSearchExecutionStore(f.pool, codec), idgen.New(), codec, 0)
	client := &tvEpisodeDiscoveryPageClient{}
	executor.RegisterClient(f.indexer, client)
	if e = executor.ExecuteInteractiveSearchSource(ctx, *lease); e != nil {
		t.Fatal("production executor", e)
	}
	if e = searches.FinishInteractiveSearchSource(ctx, *lease, "succeeded"); e != nil {
		t.Fatal("finish", e)
	}
	snapshot, e := usecaseSearch.NewInteractiveSearchService(searches, codec).Read(ctx, f.actor, *status.SearchID)
	if e != nil || snapshot == nil || len(snapshot.Candidates) != 51 || snapshot.TotalCandidates != 51 || client.calls != 2 || len(snapshot.Sources) != 1 || len(snapshot.Sources[0].QueryIDs) != 2 {
		t.Fatalf("snapshot %+v %v calls %d", snapshot, e, client.calls)
	}
	better, same := 0, 0
	for _, candidate := range snapshot.Candidates {
		if candidate.Candidate.Guid != "tv-discovery-50" && candidate.Candidate.Guid != "tv-discovery-0" {
			continue
		}
		req := ports.TVEpisodeUpgradePreviewRequest{SubscriptionID: f.sub, UnitID: f.unit, CurrentFileID: f.request.CurrentFileID, CandidateID: candidate.Candidate.ID}
		preview, e := f.service.Preview(ctx, f.actor, req)
		if e != nil || preview.ExecutionAllowed {
			t.Fatalf("existing explicit TV Preview %+v %v", preview, e)
		}
		if candidate.Candidate.Guid == "tv-discovery-50" {
			if !preview.Comparable || !preview.Improvement {
				raw, _ := json.Marshal(preview)
				t.Fatalf("page two better candidate %s", raw)
			}
			better++
		} else {
			if preview.Improvement {
				t.Fatal("same quality marked better")
			}
			same++
		}
	}
	if better != 1 || same != 1 {
		t.Fatal("comparison", better, same)
	}
	if pages := intentCount(t, f.pool, `SELECT count(*) FROM interactive_search_query a JOIN indexer_query_page p ON p.query_id=a.query_id WHERE a.search_id=$1`, *status.SearchID); pages != 2 {
		t.Fatal("pagination proof", pages)
	}
	if after := tvEpisodeDiscoveryState(t, f); after != before {
		t.Fatal("discovery or Preview changed acquisition/library")
	}
}
func TestTVEpisodeDiscoveryLateRevocationPG(t *testing.T) {
	f, s, key := tvEpisodeDiscoveryFixture(t)
	ctx := t.Context()
	if ok, e := s.ScheduleTVEpisodeDiscovery(ctx, key); e != nil || !ok {
		t.Fatal(ok, e)
	}
	status, e := s.ReadTVEpisodeDiscovery(ctx, f.actor, key)
	if e != nil || status.SearchID == nil {
		t.Fatal(status, e)
	}
	searches := postgres.NewInteractiveSearchStore(f.pool, idgen.New())
	task := interactiveClaim(t, f.pool, &ports.InteractiveSearchHeader{ID: *status.SearchID}, f.indexer)
	lease, e := searches.LoadInteractiveSearchSource(ctx, task)
	if e != nil {
		t.Fatal(e)
	}
	codec := crypto.NewSecretProtector(bytes.Repeat([]byte{27}, 32))
	executor := usecaseSearch.NewService(postgres.NewSearchExecutionStore(f.pool, codec), idgen.New(), codec, 0)
	client := &tvEpisodeDiscoveryPageClient{after: func() {
		execIntent(t, f.pool, `UPDATE subscription SET intent_version=intent_version+1 WHERE id=$1`, f.sub)
	}}
	executor.RegisterClient(f.indexer, client)
	if e = executor.ExecuteInteractiveSearchSource(ctx, *lease); e == nil {
		t.Fatal("late results accepted after intent change")
	}
	if _, e = searches.ReadInteractiveSearch(ctx, f.actor, *status.SearchID); e == nil {
		t.Fatal("stale result exposed")
	}
}
