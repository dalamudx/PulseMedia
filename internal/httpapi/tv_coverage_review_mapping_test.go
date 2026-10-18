package httpapi_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"testing"
)

func TestTVCoverageReviewNativePGMappingChange(t *testing.T) {
	f := newCoverageFixture(t)
	ctx := context.Background()
	c := f.command(f.preview(t))
	source := postgres.NewEpisodeSourceStore(postgres.NewTVRefreshStore(f.f.a.DBPool(), idgen.New()))
	mappings := subscription.NewEpisodeMappingService(postgres.NewEpisodeMappingStore(source, idgen.New()), subscription.NewEpisodeSourceService(source, nil))
	draft := ports.EpisodeMappingDraft{Mode: "standard"}
	preview, e := mappings.Preview(ctx, f.actor, f.f.sub.Id, draft)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = mappings.Publish(ctx, f.actor, f.f.sub.Id, ports.EpisodeMappingCommand{Draft: draft, ExpectedInputStamp: preview.InputStamp, ExpectedSourceHash: preview.SourceHash, Confirmed: true}, "coverage-standard-mapping"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.Review(ctx, f.actor, c, "changed-mapping"); !errors.Is(e, ports.ErrTVCoverageReviewConflict) {
		t.Fatal("mapping change did not invalidate review", e)
	}
}
