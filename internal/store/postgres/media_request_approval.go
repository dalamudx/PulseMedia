package postgres

import (
	"context"
	"errors"
	"slices"

	"github.com/dalamudx/PulseMedia/internal/domain/mediarequest"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func NewMediaRequestApprovalCreationStore(pool *pgxpool.Pool, ids ports.IDGen, fence ports.MediaRequestApprovalFence) *SubscriptionCreationStore {
	copied := fence
	return &SubscriptionCreationStore{pool: pool, ids: ids, requestFence: &copied}
}
func (s *SubscriptionCreationStore) requestCreationAuthorize(ctx context.Context, q *db.Queries, a *ports.Actor, in *ports.SubscriptionCreationInput, id ports.SubscriptionCreationIdentity, finishing bool) (db.MediaRequest, error) {
	var row db.MediaRequest
	f := s.requestFence
	if f == nil || f.RequestID == uuid.Nil || !mediaRequestKey(f.Key) || f.ExpectedVersion < 0 || a == nil || a.Role != ports.RoleAdmin {
		return row, ports.ErrForbidden
	}
	if err := lockAcquisitionUser(ctx, q, a); err != nil {
		return row, err
	}
	if id.Key != "media.request.approve:"+f.RequestID.String()+":"+f.Key || in != nil && !creationHash(id.RequestHash) {
		return row, ports.ErrMediaRequestConflict
	}
	row, err := q.LockMediaRequest(ctx, f.RequestID)
	if err != nil {
		return row, err
	}
	if _, err = mediaRequestOriginal(row); err != nil {
		return row, err
	}
	owner, err := q.LockAcquisitionCommandUser(ctx, row.OwnerID)
	if err != nil {
		return row, err
	}
	if !owner.IsActive {
		return row, ports.ErrForbidden
	}
	if in != nil {
		seasons := make([]int, len(row.Seasons))
		for i, n := range row.Seasons {
			seasons[i] = int(n)
		}
		if in.UserID == nil || *in.UserID != row.OwnerID || in.TMDBID == nil || *in.TMDBID != row.TmdbID || in.MediaID != nil || in.MediaType != row.MediaType || in.ScopeType != "all" || !slices.Equal(in.Seasons, seasons) || in.IncludeSpecials != row.IncludeSpecials || len(in.EpisodeKeys) > 0 || len(in.SelectedUnitIDs) > 0 || in.ExpectedIntentVersion != nil || in.RootID == nil || *in.RootID == uuid.Nil || len(in.IndexerIDs) == 0 || in.DownloaderID == nil || *in.DownloaderID == uuid.Nil {
			return row, ports.ErrMediaRequestConflict
		}
	}
	storedKey, err := creationKey(id.Key)
	if err != nil {
		return row, err
	}
	switch row.State {
	case mediarequest.Pending:
		if in == nil {
			return row, ports.ErrNotFound
		}
		if f.ExpectedVersion != row.Version {
			return row, ports.ErrMediaRequestConflict
		}
		isolated, e := q.GuardMediaScrapeIsolation(ctx)
		if e != nil {
			return row, e
		}
		if isolated {
			return row, ports.ErrUnavailable
		}
		// A prior ledger without the matching terminal request cannot be adopted.
		_, e = q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: storedKey})
		if finishing {
			if e != nil {
				return row, e
			}
		} else if e == nil {
			return row, ports.ErrUnavailable
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return row, e
		}
	case mediarequest.Approved:
		if !row.DecisionActorID.Valid || uuid.UUID(row.DecisionActorID.Bytes).String() != a.ID || !row.DecisionKey.Valid || row.DecisionKey.String != f.Key || !row.DecisionHash.Valid || f.ExpectedVersion > 0 && row.Version != f.ExpectedVersion+1 || in != nil && row.DecisionHash.String != id.RequestHash {
			return row, ports.ErrMediaRequestConflict
		}
		receipt, e := creationReceipt(ctx, q, a, storedKey, row.DecisionHash.String, &row.OwnerID)
		if e != nil {
			return row, e
		}
		if receipt == nil || !row.SubscriptionID.Valid || receipt.Subscription.ID != uuid.UUID(row.SubscriptionID.Bytes) {
			return row, ports.ErrUnavailable
		}
	default:
		return row, ports.ErrMediaRequestConflict
	}
	return row, nil
}
func (s *SubscriptionCreationStore) requestCreationFinish(ctx context.Context, q *db.Queries, a *ports.Actor, in ports.SubscriptionCreationInput, id ports.SubscriptionCreationIdentity, out *ports.SubscriptionCreationReceipt) error {
	row, err := s.requestCreationAuthorize(ctx, q, a, &in, id, true)
	if err != nil {
		return err
	}
	if row.State != mediarequest.Pending || out == nil || out.Subscription.UserID != row.OwnerID {
		return ports.ErrMediaRequestConflict
	}
	key, err := creationKey(id.Key)
	if err != nil {
		return err
	}
	original, err := creationReceipt(ctx, q, a, key, id.RequestHash, &row.OwnerID)
	if err != nil {
		return err
	}
	if original == nil || creationDigest(original) != creationDigest(out) {
		return ports.ErrUnavailable
	}
	row, err = q.CloseMediaRequest(ctx, db.CloseMediaRequestParams{RequestID: row.ID, ExpectedVersion: s.requestFence.ExpectedVersion, State: mediarequest.Approved, DecisionActorID: uuid.MustParse(a.ID), SubscriptionID: pgtype.UUID{Bytes: out.Subscription.ID, Valid: true}, DecisionKey: s.requestFence.Key, DecisionHash: id.RequestHash})
	if err != nil {
		return err
	}
	record, err := mediaRequestDTO(row)
	if err != nil {
		return err
	}
	if err = mediaRequestPublish(ctx, q, s.ids, a, record, "media_request.approved"); err != nil {
		return err
	}
	// Deferred failure injection may change permissions in this same transaction.
	// Flush it before the final live authorization check, with no writes afterward.
	if err = q.FlushMediaRequestConstraints(ctx); err != nil {
		return err
	}
	_, err = s.requestCreationAuthorize(ctx, q, a, &in, id, true)
	return err
}
