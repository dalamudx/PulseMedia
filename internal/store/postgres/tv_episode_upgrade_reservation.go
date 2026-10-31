package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (s *TVEpisodeUpgradeStore) reserveTVUpgrade(ctx context.Context, a *ports.Actor, c ports.TVEpisodeUpgradeCommand, key string) (*ports.TVEpisodeUpgradeResult, error) {
	r := c.TVEpisodeUpgradePreviewRequest
	if r.UnitID == uuid.Nil || r.CurrentFileID == uuid.Nil || r.CandidateID == uuid.Nil || !completeTVStamp(c.ExpectedVersion) {
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	q, sub, e := lockUpgradeCommand(ctx, tx, a, r.SubscriptionID)
	if e != nil {
		return nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "tv_episode_upgrade.command", AggregateID: a.ID + ":" + key}); e != nil {
		return nil, e
	}
	hash := coverageDigest([]any{"tv.episode-upgrade.command.v1", c})
	actorID, e := uuid.Parse(a.ID)
	if e != nil {
		return nil, ports.ErrForbidden
	}
	receipt, e := q.ReadTVEpisodeUpgradePhaseReceipt(ctx, db.ReadTVEpisodeUpgradePhaseReceiptParams{ActorID: actorID, RequestKey: key})
	oldHash, raw := receipt.RequestHash, receipt.Result
	if e == nil {
		var out ports.TVEpisodeUpgradeResult
		if oldHash != hash || json.Unmarshal(raw, &out) != nil || out.ExecutionAllowed {
			return nil, ports.ErrTVEpisodeUpgradeConflict
		}
		valid, e := q.CheckTVEpisodeUpgradeAuthorityActor(ctx, db.CheckTVEpisodeUpgradeAuthorityActorParams{OperationID: out.OperationID, ActorID: actorID, SubscriptionID: sub.ID})
		if e != nil {
			return nil, e
		}
		if !valid {
			return nil, ports.ErrTVEpisodeUpgradeConflict
		}
		if e = tx.Commit(ctx); e != nil {
			return nil, e
		}
		return &out, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	if _, e = q.LockAcquisitionRoot(ctx, sub.RootID); e != nil {
		return nil, e
	}
	if _, e = q.LockAcquisitionUnit(ctx, r.UnitID); e != nil {
		return nil, e
	}
	m, e := readTVEpisodeUpgradePreview(ctx, tx, a, r, s.decoder)
	if e != nil {
		return nil, e
	}
	if m.Result.Reason != "" || !m.Result.Improvement || m.Result.Version != c.ExpectedVersion {
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	var occupied bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM acquisition_reservation WHERE unit_id=$1 AND released_at IS NULL) OR EXISTS(SELECT 1 FROM unit_reservation WHERE unit_id=$1 AND state IN ('reserved','dispatched'))`, r.UnitID).Scan(&occupied); e != nil {
		return nil, e
	}
	if occupied {
		return nil, ports.ErrAcquisitionUnitAlreadyReserved
	}
	op := s.ids.NewUUID()
	if e = q.InsertTVEpisodeUpgradeAuthority(ctx, db.InsertTVEpisodeUpgradeAuthorityParams{OperationID: op, ActorID: actorID, SubscriptionID: sub.ID, UnitID: r.UnitID, RootID: sub.RootID, OldFileID: r.CurrentFileID, OldFactID: m.FactID, CandidateID: r.CandidateID, ClientID: m.Client.ID, Version: m.Result.Version, Snapshot: m.Snapshot, ValidUntil: pgtype.Timestamptz{Time: m.ValidUntil, Valid: true}}); e != nil {
		return nil, e
	}
	if _, e = q.CreateAcquisitionLane(ctx, db.CreateAcquisitionLaneParams{ID: s.ids.NewUUID(), UnitID: r.UnitID, OperationID: op, CandidateID: r.CandidateID, SourceRootID: sub.RootID, SourceGeneration: m.Root.Generation, SourceStamp: m.Result.Version}); e != nil {
		return nil, e
	}
	if _, e = q.CreateUnitReservation(ctx, db.CreateUnitReservationParams{ID: s.ids.NewUUID(), UnitID: r.UnitID, RootID: sub.RootID, Purpose: "upgrade", CandidateID: r.CandidateID, OperationID: op}); e != nil {
		return nil, e
	}
	fence, _ := json.Marshal(map[string]any{"goal": "tv_episode_upgrade", "kind": "tv_episode_upgrade_v1", "operation_id": op, "version": m.Result.Version})
	if _, e = q.CreateDownloadRequirement(ctx, db.CreateDownloadRequirementParams{ID: s.ids.NewUUID(), OperationID: op, UnitID: r.UnitID, RootID: sub.RootID, ActorType: string(a.Type), ActorID: a.ID, SubmissionFence: fence}); e != nil {
		return nil, e
	}
	out := *m.Result
	out.Status = "reserved_not_executable"
	out.OperationID = op
	x := &tvUpgradeSession{store: s, actor: a, key: key, hash: hash, authority: tvUpgradeAuthority{OperationID: op, Request: r, ActorID: actorID, Version: m.Result.Version}}
	if e = x.phase(ctx, tx, "reserve", &out, map[string]any{"version": m.Result.Version}); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); e != nil {
		return nil, e
	}
	current, e := readTVEpisodeUpgradePreview(ctx, tx, a, r, s.decoder)
	if e != nil {
		return nil, e
	}
	if current.Result.Version != m.Result.Version || current.Result.Reason != "" {
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	if e = tvUpgradeProtection(ctx, tx, op, r.UnitID, sub.RootID, a.ID); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return &out, nil
}
func tvUpgradeProtection(ctx context.Context, tx pgx.Tx, op, unit, root uuid.UUID, actor string) error {
	var valid bool
	e := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM acquisition_reservation WHERE unit_id=$2 AND released_at IS NULL)=1 AND (SELECT count(*) FROM acquisition_reservation WHERE operation_id=$1 AND unit_id=$2 AND source_root_id=$3 AND released_at IS NULL)=1 AND (SELECT count(*) FROM unit_reservation WHERE unit_id=$2 AND state IN ('reserved','dispatched'))=1 AND (SELECT count(*) FROM unit_reservation WHERE operation_id=$1 AND unit_id=$2 AND root_id=$3 AND purpose='upgrade' AND state IN ('reserved','dispatched'))=1 AND (SELECT count(*) FROM download_requirement WHERE operation_id=$1 AND unit_id=$2 AND root_id=$3 AND actor_id=$4 AND actor_type='user' AND state='pending' AND submission_fence->>'goal'='tv_episode_upgrade')=1`, op, unit, root, actor).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return ports.ErrTVEpisodeUpgradeConflict
	}
	return nil
}
