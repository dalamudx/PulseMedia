package postgres

import (
	"context"
	"encoding/json"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type preparationBasis struct {
	operation            uuid.UUID
	actor                *ports.Actor
	snapshot             upgradeFrozenSnapshot
	indexerDecoder       ports.CredentialDecoder
	authority            json.RawMessage
	lane                 json.RawMessage
	reserved             json.RawMessage
	required             json.RawMessage
	savePath             string
	target               media.MovieUpgradeTarget
	own                  ownedUpgradeRows
	rows                 map[string]json.RawMessage
	taskCount            int
	prepCount            int
	sendCommand, sendJob json.RawMessage
}

func (s *UpgradePreparationStore) loadPreparationBasis(
	ctx context.Context, tx pgx.Tx, a *ports.Actor, op uuid.UUID,
) (*preparationBasis, error) {
	return s.loadMovieUpgradeBasis(ctx, tx, a, op, false)
}

func (s *UpgradePreparationStore) loadMovieUpgradeBasis(
	ctx context.Context, tx pgx.Tx, a *ports.Actor, op uuid.UUID, prepared bool,
) (*preparationBasis, error) {
	q := db.New(tx)
	if err := q.LockDownloadAcquisition(ctx, op); err != nil {
		return nil, err
	}
	b := &preparationBasis{
		operation: op, actor: a, own: ownedUpgradeRows{},
		indexerDecoder: s.indexerDecoder,
		rows:           make(map[string]json.RawMessage),
	}
	var snapshot json.RawMessage
	err := tx.QueryRow(ctx, `
SELECT to_jsonb(a),a.snapshot
FROM movie_upgrade_authority a WHERE operation_id=$1 FOR SHARE`,
		op).Scan(&b.authority, &snapshot)
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(snapshot, &b.snapshot) != nil {
		return nil, preparationConflict()
	}
	f := b.snapshot
	v := f.Observation
	if f.Kind != "frozen_movie_upgrade_v1" || f.Goal != "upgrade" ||
		!f.Confirmed || !f.Mode.Enabled() ||
		v.ActorID != uuid.MustParse(a.ID) || v != f.Target.Version ||
		f.CurrentFileID != f.Target.CurrentFileID ||
		f.CurrentFactID != f.Target.CurrentFactID ||
		f.CandidateID != f.Target.CandidateID || f.SourceID != f.Target.SourceID ||
		f.RootGeneration <= 0 || f.UnitVersion != f.Target.Candidate.UnitVersion ||
		f.ParserVersion != media.EpisodeParserVersion ||
		f.EvaluatorVersion != media.CandidateDecisionVersion ||
		!completeTVStamp(f.ProtectedDigest) || !completeTVStamp(f.ClientDigest) {
		return nil, preparationConflict()
	}
	var matched bool
	err = tx.QueryRow(ctx, `
SELECT goal='upgrade' AND status='reserved_not_executable'
 AND actor_id=$2 AND subscription_id=$3 AND intent_version=$4
 AND unit_id=$5 AND root_id=$6 AND root_generation=$7
 AND old_file_id=$8 AND old_fact_id=$9 AND candidate_id=$10
 AND client_id=$11 AND policy_id=$12 AND policy_revision=$13
 AND snapshot=$14::jsonb
FROM movie_upgrade_authority WHERE operation_id=$1`,
		op, v.ActorID, v.SubscriptionID, v.IntentVersion, v.UnitID,
		v.RootID, f.RootGeneration, f.CurrentFileID, f.CurrentFactID,
		f.CandidateID, f.ClientID, v.PolicyID, v.PolicyRevision, snapshot,
	).Scan(&matched)
	if err != nil {
		return nil, err
	}
	if !matched {
		return nil, preparationConflict()
	}
	root, err := q.LockAcquisitionRoot(ctx, v.RootID)
	if err != nil {
		return nil, err
	}
	if root.Generation != f.RootGeneration || root.MediaType != "movie" {
		return nil, preparationConflict()
	}
	if _, err = q.LockScopeMediaUnits(ctx, v.MediaID); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx,
		"SELECT id FROM media WHERE id=$1 FOR SHARE", v.MediaID); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx,
		"SELECT media_id FROM media_identity WHERE media_id=$1 FOR SHARE",
		v.MediaID); err != nil {
		return nil, err
	}

	// These are the exact three owned rows excluded by upgradeEvidenceSQL.
	// Validate their semantics before the facts_version - 2 normalization.
	for _, table := range []string{
		"acquisition_reservation", "unit_reservation", "download_requirement",
	} {
		var id uuid.UUID
		var count int
		if err = tx.QueryRow(ctx,
			"SELECT count(*) FROM "+table+" WHERE operation_id=$1",
			op).Scan(&count); err != nil {
			return nil, err
		}
		if count != 1 {
			return nil, preparationConflict()
		}
		if err = tx.QueryRow(ctx,
			"SELECT id FROM "+table+" WHERE operation_id=$1 FOR UPDATE",
			op).Scan(&id); err != nil {
			return nil, err
		}
		if err = b.own.remember(ctx, tx, table, id); err != nil {
			return nil, err
		}
		var raw json.RawMessage
		if err = tx.QueryRow(ctx,
			"SELECT to_jsonb(x) FROM "+table+" x WHERE id=$1",
			id).Scan(&raw); err != nil {
			return nil, err
		}
		switch table {
		case "acquisition_reservation":
			b.lane = raw
		case "unit_reservation":
			b.reserved = raw
		case "download_requirement":
			b.required = raw
		}
	}
	if _, err = q.ListUpgradeFiles(ctx, db.ListUpgradeFilesParams{
		RootID: v.RootID, UnitID: v.UnitID,
	}); err != nil {
		return nil, err
	}
	// Match the reservation lock order: source, client, then candidate.
	if _, err = tx.Exec(ctx,
		"SELECT id FROM indexer WHERE id=$1 FOR SHARE", f.SourceID); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx,
		"SELECT id FROM download_client WHERE id=$1 FOR SHARE",
		f.ClientID); err != nil {
		return nil, err
	}
	client, err := q.GetDownloadClientByID(ctx, f.ClientID)
	if err != nil {
		return nil, err
	}
	if episodeClientStamp(client) != f.ClientDigest {
		return nil, preparationConflict()
	}
	if _, err = q.LockUpgradeCandidate(ctx, f.CandidateID); err != nil {
		return nil, err
	}
	b.savePath = root.Path
	if b.savePath == "" {
		return nil, preparationConflict()
	}
	b.target, err = preparationMovieTarget(ctx, q, b.snapshot)
	if err != nil {
		return nil, err
	}
	if prepared {
		if err = bindPreparedUpgradeBasis(ctx, tx, b); err != nil {
			return nil, err
		}
		if err = bindSendJobBasis(ctx, tx, b); err != nil {
			return nil, err
		}
	}
	// No preview reader: its own inflight rows are not a new input scope.
	if err = b.check(ctx, tx); err != nil {
		return nil, err
	}
	return b, nil
}
