package subscription_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
)

type supplementRefreshRepo struct {
	hint                   ports.TVRefreshContext
	receipt                *ports.TVRefreshReceipt
	readErr                error
	ordinary, supplemented int
	seasons                []ports.TVSeasonMetadata
	proofs                 []ports.TVDBSeasonSupplement
}

func (r *supplementRefreshRepo) ReadTVRefreshContext(context.Context, *ports.Actor, uuid.UUID, ports.TVRefreshCommand, string, string) (ports.TVRefreshContext, *ports.TVRefreshReceipt, error) {
	return r.hint, r.receipt, r.readErr
}
func (r *supplementRefreshRepo) ApplyTVRefresh(_ context.Context, _ *ports.Actor, _ ports.TVRefreshContext, _ ports.TVRefreshCommand, seasons []ports.TVSeasonMetadata, _, _ string) (*ports.TVRefreshReceipt, error) {
	r.ordinary++
	r.seasons = seasons
	return &ports.TVRefreshReceipt{MediaID: r.hint.MediaID}, nil
}
func (r *supplementRefreshRepo) ApplyTVRefreshWithSupplements(_ context.Context, _ *ports.Actor, _ ports.TVRefreshContext, _ ports.TVRefreshCommand, seasons []ports.TVSeasonMetadata, proofs []ports.TVDBSeasonSupplement, _, _ string) (*ports.TVRefreshReceipt, error) {
	r.supplemented++
	r.seasons, r.proofs = seasons, proofs
	return &ports.TVRefreshReceipt{MediaID: r.hint.MediaID}, nil
}

type refreshPrimary func(context.Context, int64, int, string) (*ports.TVSeasonMetadata, error)

func (f refreshPrimary) GetTVSeasonMetadata(ctx context.Context, id int64, s int, l string) (*ports.TVSeasonMetadata, error) {
	return f(ctx, id, s, l)
}

type refreshSupplement func(context.Context, int64, int, *ports.TVSeasonMetadata) (*ports.TVDBSeasonSupplement, error)

func (f refreshSupplement) ReadSeasonSupplement(ctx context.Context, id int64, s int, p *ports.TVSeasonMetadata) (*ports.TVDBSeasonSupplement, error) {
	return f(ctx, id, s, p)
}
func supplementRefreshInputs() (*supplementRefreshRepo, *ports.Actor, ports.TVRefreshCommand) {
	return &supplementRefreshRepo{hint: ports.TVRefreshContext{SubscriptionID: uuid.New(), MediaID: uuid.New(), TMDBID: 1396, IntentVersion: 1, MediaVersion: 2}}, &ports.Actor{ID: uuid.NewString(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}, ports.TVRefreshCommand{ExpectedVersion: 1, Seasons: []int{1}}
}
func serviceSupplement(season int) *ports.TVDBSeasonSupplement {
	ep := ports.TVEpisodeMetadata{TMDBEpisodeID: 707, EpisodeNumber: 7}
	return &ports.TVDBSeasonSupplement{TMDBSeriesID: 1396, TVDBSeriesID: 81189, CredentialVersion: 2, FallbackVersion: 3, Metadata: ports.TVSeasonMetadata{SeasonNumber: season, Episodes: []ports.TVEpisodeMetadata{ep}}, Added: []ports.TVDBEpisodeResolution{{TMDBSeriesID: 1396, TVDBEpisodeID: 9007, SeasonNumber: season, Episode: ep}}}
}
func TestTVDBRefreshServiceReceiptAndPermissionBeforeNetwork(t *testing.T) {
	for _, scenario := range []string{"receipt", "revoked", "member", "token"} {
		t.Run(scenario, func(t *testing.T) {
			repo, actor, command := supplementRefreshInputs()
			switch scenario {
			case "receipt":
				repo.receipt = &ports.TVRefreshReceipt{MediaID: repo.hint.MediaID}
			case "revoked":
				repo.readErr = ports.ErrForbidden
			case "member":
				actor.Role = ports.RoleMember
			case "token":
				actor.Scopes = []string{"admin"}
			}
			calls := 0
			primary := refreshPrimary(func(context.Context, int64, int, string) (*ports.TVSeasonMetadata, error) {
				calls++
				return nil, ports.ErrUnavailable
			})
			fallback := refreshSupplement(func(context.Context, int64, int, *ports.TVSeasonMetadata) (*ports.TVDBSeasonSupplement, error) {
				calls++
				return nil, ports.ErrUnavailable
			})
			result, err := subscription.NewTVRefreshServiceWithSupplements(repo, primary, fallback).Refresh(t.Context(), actor, repo.hint.SubscriptionID, command, "original-key")
			if scenario == "receipt" {
				if err != nil || result != repo.receipt {
					t.Fatal("original receipt not restored", err)
				}
			} else if !errors.Is(err, ports.ErrForbidden) {
				t.Fatal("permission boundary lost", err)
			}
			if calls != 0 || repo.ordinary+repo.supplemented != 0 {
				t.Fatal("receipt or revoked read performed work")
			}
		})
	}
}
func TestTVDBRefreshServicePrimaryAndSupplementBoundaries(t *testing.T) {
	for _, scenario := range []string{"supplement", "missing_primary", "empty_primary", "disabled", "unmapped", "primary_failure", "incomplete_primary", "fallback_failure", "late_configuration", "false_complete"} {
		t.Run(scenario, func(t *testing.T) {
			repo, actor, command := supplementRefreshInputs()
			sourceCalls := 0
			primary := refreshPrimary(func(_ context.Context, id int64, season int, language string) (*ports.TVSeasonMetadata, error) {
				if id != 1396 || season != 1 || language != "zh-CN" {
					t.Fatal("primary identity changed")
				}
				switch scenario {
				case "missing_primary":
					return nil, ports.ErrNotFound
				case "primary_failure":
					return nil, ports.ErrTimeout
				}
				p := &ports.TVSeasonMetadata{SeasonNumber: 1, Complete: true, Episodes: []ports.TVEpisodeMetadata{{TMDBEpisodeID: 701, EpisodeNumber: 1}}}
				if scenario == "empty_primary" {
					p.Episodes = nil
				}
				if scenario == "incomplete_primary" {
					p.Complete = false
				}
				return p, nil
			})
			fallback := refreshSupplement(func(_ context.Context, id int64, season int, p *ports.TVSeasonMetadata) (*ports.TVDBSeasonSupplement, error) {
				sourceCalls++
				if id != 1396 || season != 1 || (scenario == "missing_primary") != (p == nil) {
					t.Fatal("fallback identity or missing-primary contract changed")
				}
				switch scenario {
				case "disabled":
					return nil, nil
				case "unmapped":
					return nil, ports.ErrNotFound
				case "fallback_failure":
					return nil, ports.ErrUnavailable
				case "late_configuration":
					return nil, ports.ErrSubscriptionBindingsConflict
				}
				proof := serviceSupplement(season)
				if scenario == "false_complete" {
					proof.Metadata.Complete = true
				}
				return proof, nil
			})
			result, err := subscription.NewTVRefreshServiceWithSupplements(repo, primary, fallback).Refresh(t.Context(), actor, repo.hint.SubscriptionID, command, "new-key")
			switch scenario {
			case "supplement", "missing_primary", "empty_primary":
				if err != nil || result == nil || repo.supplemented != 1 || repo.ordinary != 0 || len(repo.proofs) != 1 || repo.seasons[0].Complete || repo.proofs[0].CredentialVersion != 2 {
					t.Fatal("independent proof not handed to narrow transaction", err)
				}
			case "disabled", "unmapped":
				if err != nil || result == nil || repo.ordinary != 1 || repo.supplemented != 0 || !repo.seasons[0].Complete {
					t.Fatal("ordinary primary contract changed", err)
				}
			default:
				if err == nil || result != nil || repo.ordinary+repo.supplemented != 0 {
					t.Fatal("invalid or stale data reached commit", err)
				}
			}
			if (scenario == "primary_failure" || scenario == "incomplete_primary") && sourceCalls != 0 {
				t.Fatal("failed primary validation invoked fallback")
			}
		})
	}
}
func TestTVDBRefreshServiceDefersCommitUntilEverySeason(t *testing.T) {
	repo, actor, command := supplementRefreshInputs()
	command.Seasons = []int{1, 2}
	calls := 0
	primary := refreshPrimary(func(context.Context, int64, int, string) (*ports.TVSeasonMetadata, error) {
		return nil, ports.ErrNotFound
	})
	fallback := refreshSupplement(func(_ context.Context, _ int64, season int, _ *ports.TVSeasonMetadata) (*ports.TVDBSeasonSupplement, error) {
		calls++
		if season == 2 {
			return nil, ports.ErrUnavailable
		}
		return serviceSupplement(season), nil
	})
	if _, err := subscription.NewTVRefreshServiceWithSupplements(repo, primary, fallback).Refresh(t.Context(), actor, repo.hint.SubscriptionID, command, "two-seasons"); err == nil {
		t.Fatal("failed second season accepted")
	}
	if calls != 2 || repo.ordinary+repo.supplemented != 0 {
		t.Fatal("first season committed separately")
	}
}
func TestTVDBRefreshServiceCancellationWaitsForRead(t *testing.T) {
	repo, actor, command := supplementRefreshInputs()
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	primary := refreshPrimary(func(context.Context, int64, int, string) (*ports.TVSeasonMetadata, error) {
		return nil, ports.ErrNotFound
	})
	fallback := refreshSupplement(func(context.Context, int64, int, *ports.TVSeasonMetadata) (*ports.TVDBSeasonSupplement, error) {
		close(entered)
		<-release
		return serviceSupplement(1), nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	service := subscription.NewTVRefreshServiceWithSupplements(repo, primary, fallback)
	go func() {
		_, err := service.Refresh(ctx, actor, repo.hint.SubscriptionID, command, "cancel")
		done <- err
	}()
	<-entered
	cancel()
	early := false
	select {
	case <-done:
		early = true
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if early {
		t.Fatal("logical cancellation detached physical read")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("late result accepted after cancellation", err)
	}
	if repo.ordinary+repo.supplemented != 0 {
		t.Fatal("cancelled read committed")
	}
}
