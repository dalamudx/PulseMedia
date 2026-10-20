package watchlist

import (
	"context"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type SyncService struct {
	repo     ports.DoubanWishSyncRepository
	provider ports.DoubanWishProvider
	identity *IdentityResolver
	creator  ports.DoubanWishSubscriptionCreator
}

func NewSyncService(repo ports.DoubanWishSyncRepository, provider ports.DoubanWishProvider, metadata ports.WatchlistIdentityMetadata, creator ports.DoubanWishSubscriptionCreator) *SyncService {
	return &SyncService{repo: repo, provider: provider, identity: NewIdentityResolver(metadata), creator: creator}
}

// Execute processes one persisted page and waits for every provider call to
// return before releasing the physical permit. Previously created items remain
// durable if a later item fails; their creation results share the business tx.
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
		return syncError(ctx, err)
	}
	if permit == nil {
		return ports.ErrUnavailable
	}
	defer permit.Close()
	work := cloneSyncWork(permit.Snapshot())
	if !validWork(work) {
		return ports.ErrRejected
	}
	if err = syncCheck(ctx, permit); err != nil {
		return err
	}
	page, err := s.provider.ReadWishPage(ctx, work.Config.DoubanUID, work.MediaType, work.Start)
	if err != nil {
		return syncError(ctx, err)
	}
	if err = syncCheck(ctx, permit); err != nil {
		return err
	}
	items, err := permit.StorePage(ctx, cloneWishPage(page))
	if err != nil {
		return syncError(ctx, err)
	}
	// Never allow a callback to alter the private work or source snapshots.
	own := make([]ports.DoubanWishSyncItem, len(items))
	for i, item := range items {
		own[i] = item
		own[i].Observation = cloneWishItem(item.Observation)
	}
	if len(own) > ports.DoubanWishPageSize {
		return ports.ErrBadResponse
	}
	seen := make(map[uuid.UUID]bool, len(own))
	for _, item := range own {
		if item.ID == uuid.Nil || seen[item.ID] || item.Observation.MediaType != work.MediaType || !validSyncKey(item.CommandKey) {
			return ports.ErrRejected
		}
		seen[item.ID] = true
		switch item.State {
		case "created", "unresolved", "incomplete", "ambiguous", "conflict", "unavailable":
			continue
		case "pending", "matched":
		default:
			return ports.ErrRejected
		}
		if err = syncCheck(ctx, permit); err != nil {
			return err
		}
		identity, lookupErr := s.identity.Resolve(ctx, cloneWishItem(item.Observation))
		if err = syncCheck(ctx, permit); err != nil {
			return err
		}
		if lookupErr != nil {
			if err = permit.FinishItem(ctx, item.ID, "unavailable"); err != nil {
				return syncError(ctx, err)
			}
			return syncError(ctx, lookupErr)
		}
		match := ports.DoubanWishMatch{State: string(identity.State), TMDBID: identity.TMDBID, MediaType: identity.MediaType}
		if err = permit.StoreIdentity(ctx, item.ID, match); err != nil {
			return syncError(ctx, err)
		}
		if identity.State != IdentityMatched {
			continue
		}
		if err = syncCheck(ctx, permit); err != nil {
			return err
		}
		root := work.Config.MovieRootID
		if work.MediaType == "tv" {
			root = work.Config.TVRootID
		}
		rootID, tmdbID := *root, identity.TMDBID
		input := ports.SubscriptionCreationInput{TMDBID: &tmdbID, MediaType: work.MediaType, RootID: &rootID, QualityPolicyID: "default", ScopeType: "all"}
		if work.Config.Bindings != nil {
			bindings := cloneBindings(work.Config.Bindings)
			input.IndexerIDs, input.DownloaderID = bindings.IndexerIDs, bindings.DownloaderID
		}
		fence := ports.DoubanWishCreationFence{Lease: lease, RunID: work.RunID, ConfigID: work.Config.ID, ConfigVersion: work.Config.Version, ItemID: item.ID, ExecutionID: work.ExecutionID}
		actor := work.Actor
		receipt, createErr := s.creator.CreateDoubanWishSubscription(ctx, fence, &actor, input, item.CommandKey)
		if err = syncCheck(ctx, permit); err != nil {
			return err
		}
		if createErr != nil {
			if errors.Is(createErr, ports.ErrSubscriptionBindingsConflict) || errors.Is(createErr, ports.ErrInvalidSubscriptionBindings) {
				if err = permit.FinishItem(ctx, item.ID, "conflict"); err != nil {
					return syncError(ctx, err)
				}
				continue
			}
			return syncError(ctx, createErr)
		}
		if receipt == nil || receipt.Subscription.UserID != work.Config.OwnerID || receipt.Subscription.RootID != rootID {
			return ports.ErrUnavailable
		}
		// The creator persisted this result with the subscription. There is no
		// separate success callback that could lose the sync/business association.
	}
	if err = syncCheck(ctx, permit); err != nil {
		return err
	}
	return syncError(ctx, permit.FinishPage(ctx))
}

func validWork(w ports.DoubanWishSyncWork) bool {
	_, err := syncActor(&w.Actor)
	if err != nil || w.RunID == uuid.Nil || w.ExecutionID == uuid.Nil || !validConfig(w.Config, w.Actor.ID) || !w.Config.Enabled || w.Start < 0 || w.Start >= ports.MaxDoubanWishStart || w.Start%ports.DoubanWishPageSize != 0 {
		return false
	}
	return w.MediaType == "movie" && w.Config.MovieRootID != nil || w.MediaType == "tv" && w.Config.TVRootID != nil
}
func cloneSyncWork(w ports.DoubanWishSyncWork) ports.DoubanWishSyncWork {
	w.Config = cloneConfig(w.Config)
	return w
}
func cloneWishItem(item ports.DoubanWishItem) ports.DoubanWishItem {
	item.Titles = append([]string(nil), item.Titles...)
	return item
}
func cloneWishPage(p ports.DoubanWishPage) ports.DoubanWishPage {
	own := make([]ports.DoubanWishItem, len(p.Items))
	for i, item := range p.Items {
		own[i] = cloneWishItem(item)
	}
	p.Items = own
	if p.NextStart != nil {
		next := *p.NextStart
		p.NextStart = &next
	}
	return p
}
func syncCheck(ctx context.Context, p ports.DoubanWishSyncPermit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return syncError(ctx, p.Check(ctx))
}
func syncError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	for _, safe := range []error{ports.ErrRejected, ports.ErrForbidden, ports.ErrUnauthorized, ports.ErrNotFound, ports.ErrUnavailable, ports.ErrSubscriptionBindingsConflict, ports.ErrInvalidSubscriptionBindings, ports.ErrBadResponse, ports.ErrDoubanWishAccessDenied, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return identityError(err)
}
