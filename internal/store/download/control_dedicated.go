package download

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type controlQuery interface {
	db.DBTX
}

func dedicatedControlScope(ctx context.Context, q controlQuery, op uuid.UUID) (string, error) {
	var automatic, complete, movie, episode bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM automatic_tv_execution_authority WHERE operation_id=$1),EXISTS(SELECT 1 FROM complete_tv_execution_authority WHERE operation_id=$1),EXISTS(SELECT 1 FROM movie_upgrade_authority WHERE operation_id=$1),EXISTS(SELECT 1 FROM tv_episode_upgrade_authority WHERE operation_id=$1)`, op).Scan(&automatic, &complete, &movie, &episode)
	if err != nil {
		return "", err
	}
	scope := ""
	for name, present := range map[string]bool{"automatic-tv": automatic, "complete-tv": complete, "movie-upgrade": movie, "tv-episode-upgrade": episode} {
		if present {
			if scope != "" {
				return "", ports.ErrDownloadControlConflict
			}
			scope = name
		}
	}
	if scope != "tv-episode-upgrade" {
		if err := postgres.RejectTVEpisodeUpgradeOperation(ctx, q, op); err != nil {
			return "", err
		}
	}
	return scope, nil
}

// The authority's system demand belongs to its original subscription owner.
// It remains shared when a different administrator controls the physical task.
func controlPermissionRows(ctx context.Context, tx pgx.Tx, actor *ports.Actor, op uuid.UUID, rows []db.DownloadRequirement) ([]db.DownloadRequirement, error) {
	var owner, round uuid.UUID
	e := tx.QueryRow(ctx, `SELECT owner_id,round_id FROM automatic_tv_execution_authority WHERE operation_id=$1`, op).Scan(&owner, &round)
	if errors.Is(e, pgx.ErrNoRows) {
		return rows, nil
	}
	if e != nil {
		return nil, e
	}
	if actor.ID != owner.String() {
		return rows, nil
	}
	out := append([]db.DownloadRequirement(nil), rows...)
	for i := range out {
		if out[i].ActorType == "system" && out[i].ActorID == round.String() {
			out[i].ActorType = "user"
			out[i].ActorID = actor.ID
		}
	}
	return out, nil
}

func (m *Manager) acquireDedicatedControl(ctx context.Context, conn *postgres.PhysicalConnection, op uuid.UUID) (uuid.UUID, func(context.Context), error) {
	scope, e := dedicatedControlScope(ctx, conn, op)
	if e != nil {
		return uuid.Nil, nil, e
	}
	if scope == "" {
		return uuid.Nil, nil, nil
	}
	digest := sha256.Sum256([]byte("pulsemedia." + scope + ".physical.v1:" + op.String()))
	key := int64(binary.BigEndian.Uint64(digest[:8]))
	if scope == "tv-episode-upgrade" {
		key = postgres.TVEpisodeUpgradeControlLock(op)
	}
	var held bool
	if e = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1::bigint)`, key).Scan(&held); e != nil {
		return uuid.Nil, nil, e
	}
	if !held {
		return uuid.Nil, nil, ports.ErrDownloadControlConflict
	}
	token := m.idgen.NewUUID()
	release := func(work context.Context) {
		// A dead original session cannot authenticate physical return: retain its hold.
		_, _ = conn.Exec(work, `DELETE FROM download_control_physical WHERE operation_id=$1 AND token=$2`, op, token)
		var unlocked bool
		if e := conn.QueryRow(work, `SELECT pg_advisory_unlock($1::bigint)`, key).Scan(&unlocked); e != nil || !unlocked {
			_ = conn.Conn().Close(work)
		}
	}
	_, e = conn.Exec(ctx, `INSERT INTO download_control_physical(operation_id,token,scope,backend_pid,backend_start,advisory_key)VALUES($1,$2,$3,pg_backend_pid(),(SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid()),$4)`, op, token, scope, key)
	if e != nil {
		release(context.WithoutCancel(ctx))
		return uuid.Nil, nil, ports.ErrDownloadControlConflict
	}
	return token, release, nil
}

func validateControlAuthority(ctx context.Context, tx pgx.Tx, id uuid.UUID, a postgres.DownloadControlAuthority) error {
	var scope, digest string
	e := tx.QueryRow(ctx, `SELECT scope,digest FROM download_control_authority WHERE command_id=$1`, id).Scan(&scope, &digest)
	if a.Scope == "" && errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return ports.ErrDownloadControlConflict
	}
	if scope != a.Scope || digest != a.Digest {
		return ports.ErrDownloadControlConflict
	}
	return nil
}
