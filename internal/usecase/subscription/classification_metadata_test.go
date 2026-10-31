package subscription_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"testing"
)

func TestCreationFreezesTrustedClassificationMetadata(t *testing.T) {
	genres := []string{"剧情"}
	countries := []string{"CN"}
	provider := &creationMetadataProvider{movie: func(context.Context, int64, string) (*ports.MovieMetadata, error) {
		return &ports.MovieMetadata{TMDBID: 91, MediaType: "movie", Title: "电影", Genres: genres, OriginalLanguage: "zh", Countries: countries}, nil
	}}
	out, e := subscription.PrepareSubscriptionCreationMetadata(context.Background(), provider, nil, "movie", 91, nil, false)
	if e != nil {
		t.Fatal(e)
	}
	genres[0] = "changed"
	countries[0] = "US"
	f := out.ClassificationFacts
	if f == nil || f.Genres[0] != "剧情" || f.Languages[0] != "zh" || f.Countries[0] != "CN" {
		t.Fatal(f)
	}
}
