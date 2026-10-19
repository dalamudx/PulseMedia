package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// DownloadRateClientStamp fingerprints only configuration relevant to native observations.
func DownloadRateClientStamp(client db.DownloadClient) (string, error) {
	paths := map[string]string{}
	if len(client.RemotePathMap) != 0 {
		if json.Unmarshal(client.RemotePathMap, &paths) != nil {
			return "", ports.ErrDownloadTelemetryStale
		}
	}
	if paths == nil {
		paths = map[string]string{}
	}
	return downloadRateDigest(struct {
		ID                        uuid.UUID
		ClientType, URL, Username string
		PasswordEncrypted         []byte
		RemotePathMap             map[string]string
	}{client.ID, client.ClientType, client.Url, client.Username, client.PasswordEncrypted, paths}), nil
}

func downloadRateDigest(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func downloadRateTaskStamp(task db.DownloadTask) string {
	return downloadRateDigest(struct {
		ID, OperationID, ClientID uuid.UUID
		InfoHash, SavePath        string
	}{task.ID, task.OperationID, task.ClientID, task.InfoHash, task.SavePath})
}

func downloadRateTimeEqual(a, b pgtype.Timestamptz) bool {
	return a.Valid && b.Valid && a.InfinityModifier == pgtype.Finite && b.InfinityModifier == pgtype.Finite && a.Time.Equal(b.Time)
}

func downloadRateSourceValid(source string) bool {
	return source == "ordinary" || source == "automatic_tv" || source == "complete_tv" || source == "movie_upgrade"
}

// RecordDownloadRate runs inside the original business UoW after its task write.
// The caller must acquire the client before the task, and retain its original
// authority/lease/source checks. This helper neither grants work nor ends the tx.
func RecordDownloadRate(ctx context.Context, tx pgx.Tx, expectedTask db.DownloadTask, expectedClientStamp string, rate *int64, source string, nativeProgress ...*float64) error {
	if !downloadRateSourceValid(source) {
		return ports.ErrDownloadTelemetryStale
	}
	if tx == nil {
		return ports.ErrUnavailable
	}
	q := db.New(tx)
	client, err := q.LockDownloadClientForEdit(ctx, expectedTask.ClientID)
	if err != nil {
		return downloadRateReadError(err)
	}
	task, err := q.LockDownloadTask(ctx, expectedTask.ID)
	if err != nil {
		return downloadRateReadError(err)
	}
	if task.ID != expectedTask.ID || task.OperationID != expectedTask.OperationID || task.ClientID != expectedTask.ClientID || task.InfoHash != expectedTask.InfoHash || task.SavePath != expectedTask.SavePath || task.State != expectedTask.State || !downloadRateTimeEqual(task.UpdatedAt, expectedTask.UpdatedAt) {
		return ports.ErrDownloadTelemetryStale
	}
	stamp, err := DownloadRateClientStamp(client)
	if err != nil || stamp != expectedClientStamp {
		return ports.ErrDownloadTelemetryStale
	}
	currentSource, err := q.DownloadRateTaskSource(ctx, task.OperationID)
	if err != nil {
		return ports.ErrUnavailable
	}
	if currentSource != source {
		return ports.ErrDownloadTelemetryStale
	}
	sample := pgtype.Int8{}
	if rate != nil && *rate >= 0 {
		sample = pgtype.Int8{Int64: *rate, Valid: true}
	}
	progress := pgtype.Float8{}
	if len(nativeProgress) == 1 && nativeProgress[0] != nil {
		value := *nativeProgress[0]
		if !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1 {
			progress = pgtype.Float8{Float64: value, Valid: true}
		}
	}
	if err = q.UpsertDownloadRateObservation(ctx, db.UpsertDownloadRateObservationParams{
		TaskID: task.ID, ClientID: task.ClientID, TaskStamp: downloadRateTaskStamp(task), TaskUpdatedAt: task.UpdatedAt,
		ClientStamp: stamp, Source: source, DownloadRateBytesPerSecond: sample, ObservedProgress: progress,
	}); err != nil {
		return ports.ErrUnavailable
	}
	return nil
}

func downloadRateReadError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrDownloadTelemetryStale
	}
	return ports.ErrUnavailable
}

// ReadOperationTelemetry enriches only IDs already authorized in the caller's RR snapshot.
func ReadOperationTelemetry(ctx context.Context, q *db.Queries, ids []uuid.UUID) (map[uuid.UUID]ports.OperationTelemetry, error) {
	if len(ids) > 100 {
		return nil, ports.ErrInvalidOperationQuery
	}
	out := make(map[uuid.UUID]ports.OperationTelemetry, len(ids))
	for _, id := range ids {
		out[id] = ports.OperationTelemetry{RateStatus: "unknown"}
	}
	if len(ids) == 0 {
		return out, nil
	}
	if q == nil {
		return nil, ports.ErrUnavailable
	}
	progress, err := q.ListDashboardActivityProgress(ctx, ids)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	for _, row := range progress {
		item, ok := out[row.OperationID]
		if !ok || row.State != "tracking" || !row.ContentPath.Valid || row.ContentPath.String == "" || row.Error.Valid || row.TotalBytes <= 0 {
			continue
		}
		value, source, stamp := float64(row.Progress), "native_percent", row.UpdatedAt
		if row.AutomaticTv {
			if row.ObservationTaskID != row.TaskID || row.ObservationState != "tracking" || row.ReviewRequired || !row.ObservationCreatedAt.Valid || row.DownloadedBytes < 0 || row.DownloadedBytes > row.TotalBytes {
				continue
			}
			var tasks []struct {
				TotalBytes, VerifiedBytes *int64
				ContentPath               string
			}
			if json.Unmarshal([]byte(row.VerifiedTasks), &tasks) != nil || len(tasks) != 1 || tasks[0].TotalBytes == nil || tasks[0].VerifiedBytes == nil || *tasks[0].TotalBytes != row.TotalBytes || *tasks[0].VerifiedBytes != row.DownloadedBytes || tasks[0].ContentPath != row.ContentPath.String {
				continue
			}
			value = float64(row.DownloadedBytes) / float64(row.TotalBytes)
			if row.DownloadedBytes < row.TotalBytes && value >= 1 {
				value = math.Nextafter(1, 0)
			}
			source, stamp = "verified_bytes", row.ObservationCreatedAt
		} else if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
			continue
		}
		item.Progress, item.ProgressSource = &value, source
		at := stamp.Time
		item.ProgressObservedAt = &at
		out[row.OperationID] = item
	}
	rates, err := q.ListOperationDownloadRates(ctx, ids)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	for _, row := range rates {
		task := row.DownloadTask
		item, ok := out[task.OperationID]
		if !ok {
			continue
		}
		// Dedicated tracking preserves frozen task counters. Its progress must
		// come from the latest matching display observation, never those counters.
		if row.CurrentSource == "complete_tv" || row.CurrentSource == "movie_upgrade" {
			item.Progress, item.ProgressObservedAt, item.ProgressSource = nil, nil, ""
			out[task.OperationID] = item
		}
		if (task.State != "tracking" && row.CurrentSource != "movie_upgrade") || row.TaskStamp != downloadRateTaskStamp(task) || !downloadRateTimeEqual(task.UpdatedAt, row.TaskUpdatedAt) || !downloadRateSourceValid(row.Source) || row.Source != row.CurrentSource {
			continue
		}
		stamp, e := DownloadRateClientStamp(row.DownloadClient)
		if e != nil || stamp != row.ClientStamp || !row.ObservedAt.Valid || row.ObservedAt.InfinityModifier != pgtype.Finite || row.ObservedAt.Time.IsZero() || !row.SnapshotAt.Valid || row.SnapshotAt.InfinityModifier != pgtype.Finite || row.ObservedAt.Time.After(row.SnapshotAt.Time) {
			continue
		}
		at := row.ObservedAt.Time
		if (row.CurrentSource == "complete_tv" || row.CurrentSource == "movie_upgrade") && row.ObservedProgress.Valid && !math.IsNaN(row.ObservedProgress.Float64) && !math.IsInf(row.ObservedProgress.Float64, 0) && row.ObservedProgress.Float64 >= 0 && row.ObservedProgress.Float64 <= 1 {
			progress := row.ObservedProgress.Float64
			item.Progress, item.ProgressSource, item.ProgressObservedAt = &progress, "native_percent", &at
		}
		item.RateObservedAt = &at
		if row.DownloadRateBytesPerSecond.Valid && row.DownloadRateBytesPerSecond.Int64 >= 0 {
			value := strconv.FormatInt(row.DownloadRateBytesPerSecond.Int64, 10)
			item.DownloadRateBytesPerSecond = &value
			item.RateStatus = "current"
			if row.SnapshotAt.Time.Sub(at) > 45*time.Second {
				item.RateStatus = "stale"
			}
		}
		out[task.OperationID] = item
	}
	return out, nil
}
