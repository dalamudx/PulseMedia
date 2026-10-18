package postgres

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

var _ ports.UpgradeObservationRepository = (*UpgradeReservationStore)(nil)

func (s *UpgradeReservationStore) ReadFrozenUpgradeObservation(ctx context.Context, a *ports.Actor, r ports.UpgradePreviewRequest) (out *ports.FrozenUpgradeObservation, err error) {
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	defer func() { err = upgradeError(ctx, err) }()
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	q := db.New(tx)
	first, e := readUpgradePreview(ctx, q, a, r)
	if e != nil {
		return nil, e
	}
	sub, e := q.GetSubscriptionByID(ctx, r.SubscriptionID)
	if e != nil {
		return nil, e
	}
	if !sub.DownloaderID.Valid {
		return nil, ports.ErrUpgradeReservationConflict
	}
	clientID := uuid.UUID(sub.DownloaderID.Bytes)
	if _, e = tx.Exec(ctx, "SELECT id FROM download_client WHERE id=$1 FOR SHARE", clientID); e != nil {
		return nil, e
	}
	client, e := q.GetDownloadClientByID(ctx, clientID)
	if e != nil {
		return nil, e
	}
	last, e := readUpgradePreview(ctx, q, a, r)
	if e != nil {
		return nil, e
	}
	if first.Version != last.Version {
		return nil, ports.ErrUpgradeReservationConflict
	}
	out = &ports.FrozenUpgradeObservation{Preview: last.Version, ClientID: clientID, ClientConfigDigest: episodeClientStamp(client)}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
