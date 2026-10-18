package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync/atomic"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// Its private identity is shared only by an explicitly assembled native repository/driver pair.
type MovieUpgradeNativeDispatchVerifier struct{ _ byte }

func NewMovieUpgradeNativeDispatchVerifier() *MovieUpgradeNativeDispatchVerifier {
	return &MovieUpgradeNativeDispatchVerifier{}
}

type movieUpgradeNativeDispatchProof struct {
	issuer     *MovieUpgradeNativeDispatchVerifier
	session    *movieUpgradeSendSession
	descriptor ports.MovieUpgradeNativeDescriptor
	binding    movieUpgradeDispatchBinding
	raw        []byte
	consumed   atomic.Bool
}

func (v *MovieUpgradeNativeDispatchVerifier) ConsumeMovieUpgradeDispatch(ctx context.Context, claim ports.MovieUpgradeNativeDispatchProof, expected ports.MovieUpgradeNativeDescriptor) (ports.MovieUpgradeNativePayload, error) {
	var zero ports.MovieUpgradeNativePayload
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	p, ok := claim.(*movieUpgradeNativeDispatchProof)
	if !ok || p == nil || p.issuer != v || p.session == nil || p.descriptor != expected || !p.consumed.CompareAndSwap(false, true) {
		return zero, preparationConflict()
	}
	s := p.session
	sum := sha256.Sum256(p.raw)
	if s.closed || len(s.dispatchExpected) == 0 || p.binding.dispatchID == [16]byte{} || len(p.raw) == 0 || len(p.raw) > ports.MaxTorrentMetainfoBytes || hex.EncodeToString(sum[:]) != expected.RawSHA256 || p.binding.send.RawSHA256 != expected.RawSHA256 || p.binding.send.OperationID != expected.OperationID || p.binding.send.TaskID != expected.TaskID {
		return zero, preparationConflict()
	}
	var valid bool
	err := s.conn.QueryRow(ctx, `SELECT to_jsonb(d)=$2::jsonb AND to_jsonb(a)=$3::jsonb
 AND d.dispatch_id=$4 AND d.task_id=$5 AND d.raw_sha256=decode($6,'hex')
 AND d.arm_sha256=decode($7,'hex') AND a.task_snapshot=to_jsonb(t)
 AND a.preparation_snapshot=to_jsonb(p) AND p.task_snapshot=to_jsonb(t)
 AND a.backend_pid=pg_backend_pid() AND a.backend_start=(SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid())
 AND a.session_nonce=$8 AND a.session_nonce::text=current_setting('pulsemedia.upgrade_send_nonce',true)
 AND a.lease_until>clock_timestamp() AND NOT EXISTS(SELECT 1 FROM movie_upgrade_send_result WHERE operation_id=a.operation_id)
 AND EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND granted AND mode='ExclusiveLock' AND objsubid=1 AND classid=((a.advisory_key>>32)&4294967295)::oid AND objid=(a.advisory_key&4294967295)::oid)
 FROM movie_upgrade_send_dispatch d JOIN movie_upgrade_send_authorization a USING(operation_id)
 JOIN movie_upgrade_preparation p USING(operation_id) JOIN download_task t ON t.id=a.task_id
 WHERE d.operation_id=$1`, expected.OperationID, s.dispatchExpected, s.armExpected, p.binding.dispatchID, expected.TaskID, expected.RawSHA256, p.binding.send.ArmSHA256, s.nonce).Scan(&valid)
	if err != nil {
		return zero, upgradeError(ctx, err)
	}
	if !valid {
		return zero, preparationConflict()
	}
	if err = s.checkMovieUpgradeSendJobPermit(ctx); err != nil {
		return zero, err
	}
	return ports.MovieUpgradeNativePayload{DispatchID: p.binding.dispatchID, Binding: p.binding.send, TorrentBytes: append([]byte(nil), p.raw...)}, nil
}
