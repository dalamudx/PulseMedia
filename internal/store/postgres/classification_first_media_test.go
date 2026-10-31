package postgres_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/classification"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	classcase "github.com/dalamudx/PulseMedia/internal/usecase/classification"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"testing"
)

type firstClassificationProvider struct {
	ports.MetadataProvider
	calls  int
	genres []string
	hook   func()
}

func (p *firstClassificationProvider) GetMovieDetails(ctx context.Context, id int64, lang string) (*ports.MovieMetadata, error) {
	p.calls++
	if p.hook != nil {
		p.hook()
	}
	return &ports.MovieMetadata{TMDBID: id, MediaType: "movie", Title: "First trusted work", Genres: p.genres, OriginalLanguage: "en", Countries: []string{"US"}}, nil
}
func (p *firstClassificationProvider) GetTVDetails(ctx context.Context, id int64, lang string) (*ports.TVDetails, error) {
	p.calls++
	return &ports.TVDetails{TMDBID: id, Title: "First trusted TV", Genres: p.genres, OriginalLanguage: "en", Countries: []string{"US"}, Seasons: []ports.TVSeasonDetails{{SeasonNumber: 1, EpisodeCount: 1}}}, nil
}
func (p *firstClassificationProvider) GetTVSeasonMetadata(context.Context, int64, int, string) (*ports.TVSeasonMetadata, error) {
	season := creationSeason(1, 1)
	return &season, nil
}
func TestClassificationFirstTMDBPreviewCreationPG(t *testing.T) {
	for _, scenario := range []string{"tv_create_and_original_key_recovery", "create_and_original_key_recovery", "changed_facts_same_target", "policy_changed_during_provider_read", "root_generation_changed", "account_revoked_during_provider_read", "tampered_stamp"} {
		t.Run(scenario, func(t *testing.T) {
			f := newCreationFixture(t, 131)
			ctx := t.Context()
			if _, e := f.pool.Exec(ctx, `UPDATE app_user SET role='admin' WHERE id=$1`, f.actor.ID); e != nil {
				t.Fatal(e)
			}
			f.actor.Role = ports.RoleAdmin
			kind := "movie"
			if scenario == "tv_create_and_original_key_recovery" {
				kind = "tv"
			}
			target := idgen.New().NewUUID()
			if _, e := db.New(f.pool).CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: target, Name: "Drama", Path: f.path + "/classified", MediaType: kind}); e != nil {
				t.Fatal(e)
			}
			store := postgres.NewClassificationStore(f.pool, idgen.New())
			rules := []classification.Rule{{ID: "drama", MediaType: kind, RootID: target, Genres: []string{"Drama"}, Languages: []string{"en"}, Countries: []string{"US"}}}
			if _, e := store.PublishClassification(ctx, f.actor, ports.ClassificationPublish{Rules: rules}, "initial"); e != nil {
				t.Fatal(e)
			}
			provider := &firstClassificationProvider{genres: []string{"Drama"}}
			service := classcase.NewServiceWithMetadata(store, provider)
			input := creationInput(8901, kind)
			before := f.snapshot(t)
			preview, e := service.Preview(ctx, f.actor, ports.ClassificationPreviewInput{Version: 1, TMDBID: input.TMDBID, MediaType: kind})
			if e != nil || preview.Result.Status != "matched" || preview.Result.RootID == nil || *preview.Result.RootID != target || len(preview.PreviewStamp) != 64 {
				t.Fatal(preview, e)
			}
			if f.snapshot(t) != before || provider.calls != 1 {
				t.Fatal("preview wrote business state or missed native details")
			}
			var n int
			if e = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM media_classification_fact)+(SELECT count(*) FROM subscription_classification_choice)+(SELECT count(*) FROM media)`).Scan(&n); e != nil || n != 0 {
				t.Fatal("preview persisted metadata", n, e)
			}
			version := int64(1)
			input.ClassificationVersion = &version
			input.ClassificationPreviewStamp = preview.PreviewStamp
			input.ClassificationPreviewRootID = preview.Result.RootID
			switch scenario {
			case "changed_facts_same_target":
				provider.genres = []string{"Drama", "Comedy"}
			case "policy_changed_during_provider_read":
				provider.hook = func() {
					provider.hook = nil
					if _, e := store.PublishClassification(ctx, f.actor, ports.ClassificationPublish{ExpectedVersion: 1, Rules: rules}, "changed"); e != nil {
						t.Fatal(e)
					}
					before = f.snapshot(t)
				}
			case "root_generation_changed":
				if _, e = f.pool.Exec(ctx, `UPDATE library_root SET generation=generation+1 WHERE id=$1`, target); e != nil {
					t.Fatal(e)
				}
			case "account_revoked_during_provider_read":
				provider.hook = func() {
					provider.hook = nil
					if _, e := f.pool.Exec(ctx, `UPDATE app_user SET is_active=false WHERE id=$1`, f.actor.ID); e != nil {
						t.Fatal(e)
					}
				}
			case "tampered_stamp":
				input.ClassificationPreviewStamp = "0000000000000000000000000000000000000000000000000000000000000000"
			}
			creator := subscription.NewSubscriptionCreationService(f.store, provider, provider, artworkCreationSecrets{})
			result, e := creator.Create(ctx, f.actor, input, "create-first")
			if scenario != "create_and_original_key_recovery" && scenario != "tv_create_and_original_key_recovery" {
				if e == nil {
					t.Fatal("stale preview authorized", result)
				}
				if scenario != "account_revoked_during_provider_read" && !errors.Is(e, ports.ErrClassificationConflict) && !errors.Is(e, ports.ErrSubscriptionBindingsConflict) {
					t.Fatal("unexpected failure", e)
				}
				if f.snapshot(t) != before {
					t.Fatal("failed comparison published media/subscription/jobs/receipt")
				}
				return
			}
			if e != nil || result == nil || result.Subscription.RootID != target || len(result.Wanted) != 1 || provider.calls != 2 {
				t.Fatal(result, e, provider.calls)
			}
			var facts, choices int
			if e = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM media_classification_fact),(SELECT count(*) FROM subscription_classification_choice WHERE root_id=$1 AND policy_version=1)`, target).Scan(&facts, &choices); e != nil || facts != 1 || choices != 1 {
				t.Fatal(facts, choices, e)
			}
			stable := f.snapshot(t)
			provider.genres = []string{"Changed"}
			again, e := creator.Create(ctx, f.actor, input, "create-first")
			if e != nil || again.Subscription.ID != result.Subscription.ID || provider.calls != 2 || f.snapshot(t) != stable {
				t.Fatal("original key fetched again or redirected", again, e, provider.calls)
			}
		})
	}
}
