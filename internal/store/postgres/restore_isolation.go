package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrRestoreIsolated = errors.New("restored database isolated: offline external reconciliation required")

type RestoreIsolationStatus struct {
	RestoreID *uuid.UUID
	Isolated  bool
}

// RestoreStatus reads database-wide state; missing schema/state fails closed.
func RestoreStatus(ctx context.Context, pool *pgxpool.Pool) (RestoreIsolationStatus, error) {
	var s RestoreIsolationStatus
	err := pool.QueryRow(ctx, `SELECT restore_id, isolated FROM restore_isolation WHERE singleton`).Scan(&s.RestoreID, &s.Isolated)
	return s, err
}

func RequireRestoreReconciled(ctx context.Context, pool *pgxpool.Pool) error {
	s, err := RestoreStatus(ctx, pool)
	if err != nil {
		return err
	}
	if s.Isolated {
		return ErrRestoreIsolated
	}
	return nil
}

// ActivateRestoreIsolation must run after restore, with the application stopped
// and the local data-directory lock held, before that lock is released. The
// caller must keep an unsuccessful restore unpublished. No filesystem path or
// config option can disable this database-wide fence.
func ActivateRestoreIsolation(ctx context.Context, pool *pgxpool.Pool, restoreID uuid.UUID) error {
	if restoreID == uuid.Nil {
		return errors.New("restore ID required")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var current *uuid.UUID
	var isolated bool
	if err = tx.QueryRow(ctx, `SELECT restore_id, isolated FROM restore_isolation WHERE singleton FOR UPDATE`).Scan(&current, &isolated); err != nil {
		return err
	}
	if isolated && current != nil && *current == restoreID {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `UPDATE restore_isolation SET restore_id=$1, isolated=true WHERE singleton`, restoreID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO restore_isolation_audit(restore_id,action,operator_name,evidence) VALUES($1,'activate','restore orchestrator','new database restored; runtime fenced')`, restoreID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type RestoreReconciliation struct {
	RestoreID          uuid.UUID
	Operator           string
	DownloaderEvidence string
	FilesystemEvidence string
	OrphanEvidence     string
}

// ReconcileRestoreIsolation is an offline operator action under the data-dir
// lock. It never rewrites jobs, leases, reservations, or physical permits.
// All runnable jobs and undelivered outbox records must first receive their
// individual reconciliation decisions; global unpause cannot replay them.
func ReconcileRestoreIsolation(ctx context.Context, pool *pgxpool.Pool, r RestoreReconciliation) error {
	if r.RestoreID == uuid.Nil || strings.TrimSpace(r.Operator) == "" || strings.TrimSpace(r.DownloaderEvidence) == "" || strings.TrimSpace(r.FilesystemEvidence) == "" || strings.TrimSpace(r.OrphanEvidence) == "" {
		return errors.New("restore ID, operator and all reconciliation evidence required")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var id *uuid.UUID
	var isolated bool
	if err = tx.QueryRow(ctx, `SELECT restore_id, isolated FROM restore_isolation WHERE singleton FOR UPDATE`).Scan(&id, &isolated); err != nil {
		return err
	}
	if !isolated || id == nil || *id != r.RestoreID {
		return errors.New("restore isolation version mismatch")
	}
	// Include work that periodic schedulers can create without a runnable job.
	// Tracking results remain eligible after the 30-second delay, so do not
	// use the current deadline to declare restored work reconciled.
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM job WHERE state IN ('queued','running'))
 OR EXISTS(SELECT 1 FROM outbox_delivery WHERE state IN ('pending','delivering','failed'))
 OR EXISTS(SELECT 1 FROM notification_batch WHERE state IN ('open','ready'))
 OR EXISTS(SELECT 1 FROM download_task WHERE state IN ('submitting','unknown','tracking'))
 OR EXISTS(SELECT 1 FROM movie_upgrade_send_result r JOIN movie_upgrade_preparation p USING(operation_id)
   WHERE r.status='unknown'
   AND NOT EXISTS(SELECT 1 FROM movie_upgrade_observation o WHERE o.operation_id=r.operation_id)
   AND NOT EXISTS(SELECT 1 FROM job j WHERE j.kind='movie_upgrade_observation'
     AND (j.payload->>'operation_id'=r.operation_id::text OR j.dedupe_key='movie-upgrade-observation:'||r.operation_id::text)))
 OR EXISTS(SELECT 1 FROM movie_upgrade_preparation p JOIN movie_upgrade_send_result sent USING(operation_id)
   LEFT JOIN LATERAL(SELECT o.observation_id,r.outcome,r.recorded_at
     FROM movie_upgrade_tracking_observation o LEFT JOIN movie_upgrade_tracking_result r USING(observation_id)
     WHERE o.operation_id=p.operation_id AND NOT EXISTS(SELECT 1 FROM movie_upgrade_tracking_observation child WHERE child.prior_observation_id=o.observation_id)) tip ON true
   WHERE sent.status='confirmed'
   AND (tip.observation_id IS NULL OR tip.outcome='tracking')
   AND NOT EXISTS(SELECT 1 FROM movie_upgrade_tracking_job b WHERE b.operation_id=p.operation_id AND b.prior_observation_id IS NOT DISTINCT FROM tip.observation_id))`).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return errors.New("unreconciled runnable jobs, deliveries, notification batches, download tasks or upgrade observations remain; resolve individually before release")
	}
	evidence := "downloaders: " + r.DownloaderEvidence + "\nfiles: " + r.FilesystemEvidence + "\norphans: " + r.OrphanEvidence
	if _, err = tx.Exec(ctx, `INSERT INTO restore_isolation_audit(restore_id,action,operator_name,evidence) VALUES($1,'reconcile',$2,$3)`, r.RestoreID, r.Operator, evidence); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE restore_isolation SET isolated=false WHERE singleton`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
