package httpapi_test

import (
	"context"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
)

func selectionSingles(t *testing.T, f *completeTVFixture, r *ports.TVSelectionRequest, episodes []int) {
	t.Helper()
	ctx := context.Background()
	mapping := postgres.NewEpisodeMappingStore(postgres.NewEpisodeSourceStore(postgres.NewTVRefreshStore(f.f.a.DBPool(), idgen.New())), idgen.New())
	reviewer := subscription.NewEpisodeCandidateReviewService(mapping)
	for _, ep := range episodes {
		id := uuid.New()
		title := []string{"Mapping.Browser.Show.2020.S01E01.1080p.WEB-DL.mkv", "Mapping.Browser.Show.2020.S01E02.1080p.WEB-DL.mkv", "Mapping.Browser.Show.2020.S01E03.1080p.WEB-DL.mkv"}[ep-1]
		f.exec(t, "INSERT INTO release_candidate(id,indexer_id,guid,title,link_encrypted,size_bytes,quality_profile,source_config_version) SELECT $1,indexer_id,$2,$3,link_encrypted,size_bytes,quality_profile,source_config_version FROM release_candidate WHERE id=$4", id, id.String(), title, f.candidate)
		f.exec(t, "INSERT INTO indexer_query_candidate(query_id,candidate_id) SELECT query_id,$1 FROM indexer_query_candidate WHERE candidate_id=$2", id, f.candidate)
		observed, e := mapping.ReadEpisodeCandidate(ctx, f.actor, f.f.sub.Id, id)
		if e != nil {
			t.Fatal(e)
		}
		receipt, e := reviewer.Review(ctx, f.actor, ports.EpisodeCandidateReviewCommand{SubscriptionID: f.f.sub.Id, CandidateID: id, ExpectedInputStamp: observed.InputStamp, Confirmed: true, Reason: "independently specified single episode"}, "selection-single-"+id.String())
		if e != nil {
			t.Fatal(e)
		}
		if receipt.UnitID == uuid.Nil {
			t.Fatal("empty review target")
		}
		r.Episodes = append(r.Episodes, ports.TVSelectionCandidateRef{CandidateID: id, ReviewID: receipt.ID})
	}
}
func TestTVSelectionObservationPGAlternatives(t *testing.T) {
	t.Run("singles-and-required-pack", func(t *testing.T) {
		f, svc, _, r := selectionFixture(t)
		selectionSingles(t, f, &r, []int{1, 2})
		before := selectionState(t, f)
		r.PackPolicy.Mode = quality.PackModePreferEpisodes
		p, e := svc.Preview(context.Background(), f.actor, r)
		selectionAssert(t, p, e, "episodes", "episode_preference")
		if len(p.Decision.CandidateIDs) != 2 {
			t.Fatal("incomplete singles")
		}
		r.Packs = nil
		r.PackPolicy.Mode = quality.PackModeRequirePack
		p, e = svc.Preview(context.Background(), f.actor, r)
		selectionAssert(t, p, e, "hold", "required_pack_unavailable")
		if before != selectionState(t, f) {
			t.Fatal("preview side effect")
		}
	})
	for _, mode := range []string{"protected", "physical-unknown", "unaired"} {
		t.Run(mode, func(t *testing.T) {
			f, svc, _, r := selectionFixture(t)
			switch mode {
			case "protected":
				f.exec(t, "INSERT INTO unit_reservation(id,unit_id,root_id,operation_id,candidate_id,purpose,state) SELECT $1,$2,root_id,$3,$4,'download','reserved' FROM subscription WHERE id=$5", uuid.New(), f.f.units[0], uuid.New(), f.candidate, f.f.sub.Id)
			case "physical-unknown":
				f.exec(t, "INSERT INTO acquisition_reservation(id,unit_id,operation_id,candidate_id,source_root_id,source_generation,source_stamp) SELECT $1,$2,$3,$4,root_id,r.generation,'physical-unknown' FROM subscription s JOIN library_root r ON r.id=s.root_id WHERE s.id=$5", uuid.New(), f.f.units[0], uuid.New(), f.candidate, f.f.sub.Id)
			case "unaired":
				ctx := context.Background()
				refresh := postgres.NewTVRefreshStore(f.f.a.DBPool(), idgen.New())
				sub, e := f.f.a.Queries().GetSubscriptionByID(ctx, f.f.sub.Id)
				if e != nil {
					t.Fatal(e)
				}
				input, _, e := refresh.ReadTVRefreshContext(ctx, f.actor, sub.ID, ports.TVRefreshCommand{ExpectedVersion: sub.IntentVersion}, "future-airing", "future-airing")
				if e != nil {
					t.Fatal(e)
				}
				future := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
				aired := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
				response := []ports.TVSeasonMetadata{{SeasonNumber: 1, Complete: true, Episodes: []ports.TVEpisodeMetadata{{TMDBEpisodeID: 1001, EpisodeNumber: 1, AirDate: &future}, {TMDBEpisodeID: 1002, EpisodeNumber: 2, AirDate: &aired}}}}
				if _, e = refresh.ApplyTVRefresh(ctx, f.actor, input, ports.TVRefreshCommand{ExpectedVersion: input.IntentVersion, Seasons: []int{1}}, response, "future-airing", "future-airing"); e != nil {
					t.Fatal(e)
				}
			}
			selectionProject(t, f)
			selectionSingles(t, f, &r, []int{1, 2})
			r.Packs = nil
			r.PackPolicy.Mode = quality.PackModePreferEpisodes
			before := selectionState(t, f)
			p, e := svc.Preview(context.Background(), f.actor, r)
			selectionAssert(t, p, e, "hold", "no_eligible_alternative")
			if before != selectionState(t, f) {
				t.Fatal("preview changed protections")
			}
		})
	}
}
func TestTVSelectionObservationPGNoncontiguous(t *testing.T) {
	f, svc, repo, r := selectionFixture(t)
	ctx := context.Background()
	refresh := postgres.NewTVRefreshStore(f.f.a.DBPool(), idgen.New())
	sub, e := f.f.a.Queries().GetSubscriptionByID(ctx, f.f.sub.Id)
	if e != nil {
		t.Fatal(e)
	}
	input, _, e := refresh.ReadTVRefreshContext(ctx, f.actor, sub.ID, ports.TVRefreshCommand{ExpectedVersion: sub.IntentVersion}, "noncontiguous", "noncontiguous")
	if e != nil {
		t.Fatal(e)
	}
	aired := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	// Fixed provider episode set {1,3}; no synthesized range or parser-generated expected set.
	response := []ports.TVSeasonMetadata{{SeasonNumber: 1, Complete: true, Episodes: []ports.TVEpisodeMetadata{{TMDBEpisodeID: 1001, EpisodeNumber: 1, AirDate: &aired}, {TMDBEpisodeID: 1003, EpisodeNumber: 3, AirDate: &aired}}}}
	if _, e = refresh.ApplyTVRefresh(ctx, f.actor, input, ports.TVRefreshCommand{ExpectedVersion: input.IntentVersion, Seasons: []int{1}}, response, "noncontiguous", "noncontiguous"); e != nil {
		t.Fatal(e)
	}
	f.exec(t, "UPDATE release_candidate SET title='Mapping.Browser.Show.2020.S01E01E03.1080p.WEB-DL.mkv' WHERE id=$1", f.candidate)
	preview := f.preview(t)
	review, e := f.svc.Review(ctx, f.actor, f.coverageFixture.command(preview), "noncontiguous-review")
	if e != nil {
		t.Fatal(e)
	}
	r.Packs[0].ReviewID = review.ID
	selectionProject(t, f)
	observed, e := repo.ReadTVSelection(ctx, f.actor, r)
	if e != nil {
		t.Fatal(e)
	}
	set := map[int]bool{}
	for _, u := range observed.Current.Canonical {
		set[u.Episode] = true
	}
	if len(set) != 2 || !set[1] || !set[3] || len(observed.Wanted) != 2 {
		t.Fatal("lost exact episode set", set)
	}
	p, e := svc.Preview(ctx, f.actor, r)
	selectionAssert(t, p, e, "pack", "only_eligible_alternative")
}

