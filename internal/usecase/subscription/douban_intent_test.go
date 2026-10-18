package subscription_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type doubanIntentMetadata struct {
	mockDoubanMetaProvider
	seasons []ports.TVSeasonDetails
}

func (m *doubanIntentMetadata) SearchMedia(context.Context, string, string, int) ([]*ports.MovieMetadata, error) {
	return []*ports.MovieMetadata{{TMDBID: 200, Title: "Wishlist TV", Year: 2020, MediaType: "tv"}}, nil
}
func (m *doubanIntentMetadata) GetTVDetails(context.Context, int64, string) (*ports.TVDetails, error) {
	return &ports.TVDetails{TMDBID: 200, Title: "Wishlist TV", Seasons: m.seasons}, nil
}

type doubanIntentTransport struct{}

func (doubanIntentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: req, Body: io.NopCloser(strings.NewReader(`{"interests":[{"subject":{"id":"tv-fixture","title":"Wishlist TV","year":"2020","type":"tv"}}]}`))}, nil
}
func TestDoubanSyncCreatesTVThroughIntentBoundary(t *testing.T) {
	svc, q, owner, _, root, _, _ := setupSubscriptionTest(t)
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: owner.String(), Role: ports.RoleMember}
	metadata := &doubanIntentMetadata{}
	sync := subscription.NewDoubanSyncService(svc, metadata, &http.Client{Transport: doubanIntentTransport{}})
	ctx := context.Background()
	assertAbsent := func() {
		t.Helper()
		if _, e := q.GetMediaByIdentity(ctx, db.GetMediaByIdentityParams{Namespace: "tmdb.tv", ExternalID: "200"}); !errors.Is(e, pgx.ErrNoRows) {
			t.Fatalf("partial TV identity: %v", e)
		}
	}
	count, e := sync.SyncUserWishlist(ctx, "fixture", actor, root)
	if e != nil || count != 0 {
		t.Fatalf("TV without metadata: %d %v", count, e)
	}
	assertAbsent()
	metadata.seasons = []ports.TVSeasonDetails{{SeasonNumber: 1, EpisodeCount: 2}}
	count, e = sync.SyncUserWishlist(ctx, "fixture", actor, uuid.New())
	if e != nil || count != 0 {
		t.Fatalf("invalid root: %d %v", count, e)
	}
	assertAbsent()
	count, e = sync.SyncUserWishlist(ctx, "fixture", actor, root)
	if e != nil || count != 1 {
		t.Fatalf("trusted TV: %d %v", count, e)
	}
	media, e := q.GetMediaByIdentity(ctx, db.GetMediaByIdentityParams{Namespace: "tmdb.tv", ExternalID: "200"})
	if e != nil {
		t.Fatal(e)
	}
	units, e := q.ListMediaUnits(ctx, media.ID)
	if e != nil || len(units) != 2 {
		t.Fatalf("trusted units: %+v %v", units, e)
	}
	for _, unit := range units {
		if unit.UnitKey != "S01E01" && unit.UnitKey != "S01E02" {
			t.Fatalf("TV became movie unit: %+v", unit)
		}
	}
	sub, e := q.GetSubscriptionByUserMediaRoot(ctx, db.GetSubscriptionByUserMediaRootParams{UserID: owner, MediaID: media.ID, RootID: root})
	if e != nil {
		t.Fatal(e)
	}
	wanted, e := q.ListWantedItemsForSubscription(ctx, sub.ID)
	if e != nil || len(wanted) != 2 {
		t.Fatalf("atomic wishlist expansion: %+v %v", wanted, e)
	}
}
