package postgres

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These constructors enable display telemetry on schema 93. Historical callers
// retain their original constructors and frozen evidence contracts.
func NewAutomaticTVObservationStoreWithDownloadRates(pool *pgxpool.Pool, ids ports.IDGen) *AutomaticTVObservationStore {
	store := NewAutomaticTVObservationStore(pool, ids)
	store.recordRates = true
	return store
}

func NewCompleteTVTrackingStoreWithDownloadRates(pool *pgxpool.Pool, ids ports.IDGen) *CompleteTVTrackingStore {
	store := NewCompleteTVTrackingStore(pool, ids)
	store.recordRates = true
	return store
}

func NewUpgradePreparationStoreWithDownloadRates(pool *pgxpool.Pool, ids ports.IDGen, verifier ports.MovieUpgradeMetainfoVerifier, decoders ...ports.CredentialDecoder) (*UpgradePreparationStore, error) {
	store, err := NewUpgradePreparationStore(pool, ids, verifier, decoders...)
	if err != nil {
		return nil, err
	}
	store.recordRates = true
	return store, nil
}

// Original observers already hold their task/client and authority locks. The
// sample belongs to that same transaction, before its final lease/ledger checks.
func recordSpecialOperationRate(ctx context.Context, tx pgx.Tx, operationID, taskID, clientID uuid.UUID, source string, rate *int64, progress *float64) error {
	q := db.New(tx)
	task, err := q.GetDownloadTaskByOpID(ctx, operationID)
	if err != nil {
		return ports.ErrUnavailable
	}
	if task.ID != taskID || task.ClientID != clientID {
		return ports.ErrDownloadTelemetryStale
	}
	client, err := q.GetDownloadClientByID(ctx, clientID)
	if err != nil {
		return ports.ErrUnavailable
	}
	stamp, err := DownloadRateClientStamp(client)
	if err != nil {
		return ports.ErrUnavailable
	}
	if source != "movie_upgrade" && task.State != "tracking" {
		rate, progress = nil, nil
	}
	return RecordDownloadRate(ctx, tx, task, stamp, rate, source, progress)
}

func (s *AutomaticTVObservationStore) recordNativeRate(ctx context.Context, tx pgx.Tx, snapshot ports.AutomaticTVObservationSnapshot, remote ports.AutomaticTVDownloadObservation, known bool, state string) error {
	var rate *int64
	if known && state == "tracking" && len(remote.After) == 1 {
		rate = remote.After[0].DownloadRateBytesPerSecond
	}
	// Automatic TV progress remains derived only from validated byte evidence.
	return recordSpecialOperationRate(ctx, tx, snapshot.Request.OperationID, snapshot.Request.TaskID, snapshot.ClientID, "automatic_tv", rate, nil)
}

func (s *CompleteTVTrackingStore) recordNativeRate(ctx context.Context, tx pgx.Tx, snapshot ports.CompleteTVTrackingSnapshot, remote ports.CompleteTVTrackingObservation, state string) error {
	var rate *int64
	var progress *float64
	if state == "tracking" && len(remote.After) == 1 && remote.After[0] != nil {
		rate = remote.After[0].DownloadRateBytesPerSecond
		value := float64(remote.After[0].Progress)
		progress = &value
	}
	return recordSpecialOperationRate(ctx, tx, snapshot.Request.OperationID, snapshot.Request.TaskID, snapshot.ClientID, "complete_tv", rate, progress)
}

func (s *movieUpgradeTrackingSession) recordNativeRate(ctx context.Context, tx pgx.Tx, observed ports.MovieUpgradeVerifiedTracking) error {
	var rate *int64
	var progress *float64
	if observed.Outcome == "tracking" {
		rate, progress = observed.DownloadRateBytesPerSecond, observed.NativeProgress
	}
	return recordSpecialOperationRate(ctx, tx, s.base.operation, s.binding.TaskID, s.descriptor.Identity.ClientID, "movie_upgrade", rate, progress)
}
