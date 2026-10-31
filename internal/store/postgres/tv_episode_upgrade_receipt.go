package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *TVEpisodeUpgradeStore) ReadTVEpisodeUpgradeCommand(ctx context.Context, a *ports.Actor, sub uuid.UUID, key string) (out *ports.TVEpisodeUpgradeResult, err error) {
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	defer func() { err = tvUpgradeError(ctx, err) }()
	if sub == uuid.Nil || !upgradeKey(key) || len(key) > 128 {
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	if _, _, e = lockUpgradeCommand(ctx, tx, a, sub); e != nil {
		return nil, e
	}
	actorID, e := uuid.Parse(a.ID)
	if e != nil {
		return nil, ports.ErrForbidden
	}
	q := db.New(tx)
	raw, e := q.ReadTVEpisodeUpgradeCommandReceipt(ctx, db.ReadTVEpisodeUpgradeCommandReceiptParams{ActorID: actorID, RequestKey: key, SubscriptionID: sub})
	if e == nil {
		out = &ports.TVEpisodeUpgradeResult{}
		if json.Unmarshal(raw, out) != nil || out.ExecutionAllowed {
			return nil, ports.ErrTVEpisodeUpgradeConflict
		}
	} else if errors.Is(e, pgx.ErrNoRows) {
		out = &ports.TVEpisodeUpgradeResult{Status: "pending_requires_observation"}
		var row db.ReadTVEpisodeUpgradeUnresolvedPermitRow
		row, e = q.ReadTVEpisodeUpgradeUnresolvedPermit(ctx, db.ReadTVEpisodeUpgradeUnresolvedPermitParams{ActorID: actorID, RequestKey: key, SubscriptionID: sub})
		out.OperationID, out.UnitID, out.CurrentFileID, out.CandidateID, out.Version = row.OperationID, row.UnitID, row.OldFileID, row.CandidateID, row.Version
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, ports.ErrNotFound
		}
		if e != nil {
			return nil, e
		}
	} else {
		return nil, e
	}
	if _, _, e = lockUpgradeCommand(ctx, tx, a, sub); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
