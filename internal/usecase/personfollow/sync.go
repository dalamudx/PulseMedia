package personfollow

import (
	"context"
	"errors"
	"slices"
	"time"

	domain "github.com/dalamudx/PulseMedia/internal/domain/personfollow"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type SyncService struct {
	repo     ports.PersonFollowSyncRepository
	provider ports.PersonCreditsProvider
	creator  ports.PersonFollowSubscriptionCreator
}

func NewSyncService(repo ports.PersonFollowSyncRepository, provider ports.PersonCreditsProvider, creator ports.PersonFollowSubscriptionCreator) *SyncService {
	return &SyncService{repo, provider, creator}
}

func (s *SyncService) Execute(ctx context.Context, lease ports.JobLease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.repo == nil || s.provider == nil || s.creator == nil {
		return ports.ErrUnavailable
	}
	if lease.ID == uuid.Nil || lease.Owner == "" || lease.Token <= 0 {
		return ports.ErrRejected
	}
	permit, err := s.repo.Acquire(ctx, lease)
	if err != nil {
		return personSyncFailure(ctx, err)
	}
	if permit == nil {
		return ports.ErrUnavailable
	}
	defer permit.Close()
	work := permit.Snapshot()
	work.Config = copyPersonConfig(work.Config)
	if !personSyncWorkValid(work) {
		return ports.ErrRejected
	}
	if err = personSyncCheck(ctx, permit); err != nil {
		return err
	}
	if !work.SourceStored {
		credits, e := s.provider.GetPersonCredits(ctx, work.Config.Settings.PersonID, work.Config.Settings.Role)
		if err = personSyncCheck(ctx, permit); err != nil {
			return err
		}
		if e != nil {
			return personSyncFailure(ctx, e)
		}
		if credits.PersonID != work.Config.Settings.PersonID || credits.Role != work.Config.Settings.Role {
			return ports.ErrBadResponse
		}
		credits.Works, e = domain.NormalizeWorks(credits.Works)
		if e != nil {
			return ports.ErrBadResponse
		}
		if e = permit.StoreCredits(ctx, credits); e != nil {
			return personSyncFailure(ctx, e)
		}
	}
	if err = personSyncCheck(ctx, permit); err != nil {
		return err
	}
	items, err := permit.Items(ctx)
	if err != nil {
		return personSyncFailure(ctx, err)
	}
	own := slices.Clone(items)
	if !personSyncItemsValid(work, own) {
		return ports.ErrBadResponse
	}
	for _, item := range own {
		if item.State != "pending" {
			continue
		}
		if err = personSyncCheck(ctx, permit); err != nil {
			return err
		}
		root, tmdb := item.RootID, item.Observation.TMDBID
		input := ports.SubscriptionCreationInput{TMDBID: &tmdb, MediaType: item.Observation.MediaType, RootID: &root, QualityPolicyID: work.Config.Settings.QualityPolicyID, ScopeType: "all"}
		if bindings := work.Config.Settings.Clone().Bindings; bindings != nil {
			input.IndexerIDs, input.DownloaderID = bindings.IndexerIDs, bindings.DownloaderID
		}
		fence := ports.PersonFollowCreationFence{Lease: lease, RunID: work.RunID, ExecutionID: work.ExecutionID, ConfigID: work.Config.ID, ConfigVersion: work.Config.Version, ItemID: item.ID}
		actor := work.Actor
		receipt, createErr := s.creator.CreatePersonFollowSubscription(ctx, fence, &actor, input, item.CommandKey)
		if err = personSyncCheck(ctx, permit); err != nil {
			return err
		}
		if createErr != nil {
			if errors.Is(createErr, ports.ErrSubscriptionBindingsConflict) || errors.Is(createErr, ports.ErrInvalidSubscriptionBindings) || errors.Is(createErr, ports.ErrNotFound) || errors.Is(createErr, ports.ErrBadResponse) {
				if err = permit.ReviewItem(ctx, item.ID); err != nil {
					return personSyncFailure(ctx, err)
				}
				continue
			}
			return personSyncFailure(ctx, createErr)
		}
		if receipt == nil || receipt.Subscription.ID == uuid.Nil || receipt.Subscription.UserID != work.Config.OwnerID || receipt.Subscription.RootID != item.RootID || receipt.Subscription.MediaID == uuid.Nil {
			return ports.ErrBadResponse
		}
	}
	if err = personSyncCheck(ctx, permit); err != nil {
		return err
	}
	return personSyncFailure(ctx, permit.FinishPage(ctx))
}
func personSyncWorkValid(w ports.PersonFollowSyncWork) bool {
	actor, err := personActor(&w.Actor)
	return err == nil && personConfig(&w.Config, uuid.MustParse(actor.ID)) && w.Config.Settings.Enabled && w.RunID != uuid.Nil && w.ExecutionID != uuid.Nil && w.Start >= 0 && w.Start < domain.MaxWorks && w.Start%ports.PersonFollowPageSize == 0 && (w.SourceStored || w.Start == 0)
}
func personSyncItemsValid(w ports.PersonFollowSyncWork, items []ports.PersonFollowSyncItem) bool {
	if items == nil || len(items) > ports.PersonFollowPageSize {
		return false
	}
	seen := map[uuid.UUID]bool{}
	works := map[struct {
		kind string
		id   int64
	}]bool{}
	for _, item := range items {
		kind, err := w.Config.Settings.Classify(item.Observation)
		key := struct {
			kind string
			id   int64
		}{item.Observation.MediaType, item.Observation.TMDBID}
		if err != nil || item.ID == uuid.Nil || seen[item.ID] || works[key] || item.RootID != w.Config.Settings.RootFor(item.Observation.MediaType) || item.CommandKey != domain.CreationKey(w.Config.ID, w.Config.Version, item.RootID, item.Observation) {
			return false
		}
		seen[item.ID], works[key] = true, true
		switch item.State {
		case "pending", "created":
			if kind != "pending" {
				return false
			}
		case "excluded":
			if kind != "excluded" {
				return false
			}
		case "review_required":
			if kind != "pending" && kind != "review_required" {
				return false
			}
		default:
			return false
		}
	}
	return true
}
func personSyncCheck(ctx context.Context, permit ports.PersonFollowSyncPermit) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return personSyncFailure(ctx, permit.Check(ctx))
}
func personSyncFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	if rate, ok := ports.IsRateLimited(err); ok {
		delay := max(time.Duration(0), min(rate.RetryAfter, 24*time.Hour))
		return ports.NewRateLimitError(delay, "person metadata rate limit")
	}
	for _, known := range []error{ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrPersonFollowConflict, ports.ErrSubscriptionBindingsConflict, ports.ErrInvalidSubscriptionBindings, ports.ErrRejected, ports.ErrNotFound, ports.ErrBadResponse, ports.ErrUnavailable, ports.ErrTimeout, ports.ErrAuthRequired, ports.ErrBlocked} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ports.ErrUnavailable
}
