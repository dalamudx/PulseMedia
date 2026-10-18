package app

import (
	"context"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/budget"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestDynamicTMDBCredentialRotationRetainsBudget(t *testing.T) {
	clk := clock.NewMock(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	gate := budget.NewMetadataBudget(clk)
	provider := &dynamicTMDBProvider{budget: gate}
	t.Setenv("TMDB_API_KEY", "synthetic-first")
	first := provider.getClient(context.Background())
	gate.RecordRateLimit(time.Minute)
	t.Setenv("TMDB_API_KEY", "synthetic-second")
	second := provider.getClient(context.Background())
	if first == second {
		t.Fatal("credential rotation did not replace client")
	}
	for _, client := range []ports.MetadataProvider{first, second} {
		ctx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, e := client.SearchMovies(ctx, "must-not-go-online", 0)
		stop()
		if rate, ok := ports.IsRateLimited(e); !ok || rate.RetryAfter != time.Minute {
			t.Fatal("rotation lost the shared admission budget", e)
		}
	}
	for _, client := range []tmdbMetadataClient{first, second} {
		ctx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
		snapshot, e := client.SearchMovieIdentityCandidates(ctx, "must-not-go-online", 1999)
		stop()
		if rate, ok := ports.IsRateLimited(e); !ok || rate.RetryAfter != time.Minute || snapshot.Complete || len(snapshot.Candidates) != 0 {
			t.Fatal("identity search lost shared budget or invented evidence", e)
		}
	}
}
