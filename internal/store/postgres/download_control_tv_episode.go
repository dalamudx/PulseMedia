package postgres

import (
	"context"
	"encoding/json"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TVEpisodeUpgradeControlLock shares the original upgrade's physical exclusion.
func TVEpisodeUpgradeControlLock(op uuid.UUID) int64 { return tvUpgradeLock(op) }

func lockTVEpisodeUpgradeControlAuthority(ctx context.Context, tx pgx.Tx, task db.DownloadTask, decoder ports.CredentialDecoder) (DownloadControlAuthority, any, error) {
	out := DownloadControlAuthority{Scope: "tv-episode-upgrade"}
	var req ports.TVEpisodeUpgradePreviewRequest
	var frozen, preparedRaw, sendRaw []byte
	var version string
	var valid bool
	err := tx.QueryRow(ctx, `SELECT actor_id,subscription_id,unit_id,old_file_id,candidate_id,version,snapshot,valid_until>clock_timestamp() FROM tv_episode_upgrade_authority WHERE operation_id=$1 FOR SHARE`, task.OperationID).Scan(&out.OwnerID, &req.SubscriptionID, &req.UnitID, &req.CurrentFileID, &req.CandidateID, &version, &frozen, &valid)
	if err != nil {
		return out, nil, err
	}
	if !valid || task.State != "tracking" || task.CandidateID != req.CandidateID {
		return out, nil, ports.ErrDownloadControlConflict
	}
	original, err := automaticTVLockOwner(ctx, db.New(tx), out.OwnerID)
	if err != nil {
		return out, nil, err
	}
	// Reuse the dedicated current account/root/file/mapping/source/policy validator.
	current, err := readTVEpisodeUpgradePreview(ctx, tx, original, req, decoder)
	if err != nil {
		return out, nil, err
	}
	if current.Result.Reason != "" || !current.Result.Improvement || current.Result.Version != version || completeTVJSONDigest(current.Snapshot) != completeTVJSONDigest(frozen) || current.Client.ID != task.ClientID {
		return out, nil, ports.ErrDownloadControlConflict
	}
	if err = tvUpgradeProtection(ctx, tx, task.OperationID, req.UnitID, current.Root.ID, out.OwnerID.String()); err != nil {
		return out, nil, err
	}
	err = tx.QueryRow(ctx, `SELECT p.evidence,s.result FROM tv_episode_upgrade_phase p JOIN tv_episode_upgrade_phase s USING(operation_id) JOIN tv_episode_upgrade_return r ON r.operation_id=s.operation_id AND r.action=s.action WHERE p.operation_id=$1 AND p.action='prepare' AND s.action='send' AND r.status='confirmed' AND s.result->>'status'='tracking' AND NOT EXISTS(SELECT 1 FROM tv_episode_upgrade_phase x WHERE x.operation_id=p.operation_id AND (x.action IN ('organization-confirm','organize','retirement-confirm','retire') OR (x.action LIKE 'observe:%' AND x.result->>'status'='organization_review_required'))) AND NOT EXISTS(SELECT 1 FROM tv_episode_upgrade_permit x LEFT JOIN tv_episode_upgrade_return y USING(operation_id,action) WHERE x.operation_id=p.operation_id AND y.operation_id IS NULL)`, task.OperationID).Scan(&preparedRaw, &sendRaw)
	if err != nil {
		return out, nil, err
	}
	var prepared tvUpgradePrepared
	if json.Unmarshal(preparedRaw, &prepared) != nil || prepared.InfoHash != task.InfoHash || prepared.SavePath != task.SavePath || prepared.Total != task.TotalBytes || prepared.Target != current.Target {
		return out, nil, ports.ErrDownloadControlConflict
	}
	out.ClientStamp = episodeClientStamp(current.Client)
	return out, []any{version, json.RawMessage(frozen), json.RawMessage(preparedRaw), json.RawMessage(sendRaw)}, nil
}

func rejectTVEpisodeUpgradeControl(ctx context.Context, tx pgx.Tx, op uuid.UUID) error {
	var blocked bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM download_control_physical WHERE operation_id=$1) OR EXISTS(SELECT 1 FROM download_control_command c WHERE c.operation_id=$1 AND NOT EXISTS(SELECT 1 FROM download_control_confirmation f WHERE f.command_id=c.id))`, op).Scan(&blocked)
	if err != nil {
		return err
	}
	if blocked {
		return ports.ErrTVEpisodeUpgradeObservation
	}
	return nil
}
