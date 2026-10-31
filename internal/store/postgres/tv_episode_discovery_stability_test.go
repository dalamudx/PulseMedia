package postgres_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	usecaseSubscription "github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"testing"
)

func TestTVEpisodeDiscoveryStableScanProgressPG(t *testing.T) {
	f, s, key := tvEpisodeDiscoveryFixture(t)
	ctx := t.Context()
	if ok, e := s.ScheduleTVEpisodeDiscovery(ctx, key); e != nil || !ok {
		t.Fatal(ok, e)
	}
	before, e := s.ReadTVEpisodeDiscovery(ctx, f.actor, key)
	if e != nil || before.SearchID == nil {
		t.Fatal(before, e)
	}
	execIntent(t, f.pool, `UPDATE library_root SET scan_token = 1 + scan_token,mutation_seq=mutation_seq+1,updated_at=clock_timestamp() WHERE id=$1`, f.tv)
	execIntent(t, f.pool, `UPDATE media_unit SET facts_version=facts_version+1,updated_at=clock_timestamp() WHERE id=$1`, f.unit)
	if _, e := usecaseSubscription.NewEvaluator(postgres.NewWantedProjectionStore(f.pool)).EvaluateWantedItem(ctx, f.wanted, quality.Policy{ID: "mp22", Revision: 1}); e != nil {
		t.Fatal(e)
	}
	after, e := s.ReadTVEpisodeDiscovery(ctx, f.actor, key)
	if e != nil || after.SearchID == nil || *after.SearchID != *before.SearchID || after.ExecutionAllowed {
		t.Fatal("incidental scan/evaluation invalidated frozen unit", after, e)
	}
	if _, e := postgres.NewInteractiveSearchStore(f.pool, idgen.New()).ReadInteractiveSearch(ctx, f.actor, *before.SearchID); e != nil {
		t.Fatal("stable source snapshot", e)
	}
}
