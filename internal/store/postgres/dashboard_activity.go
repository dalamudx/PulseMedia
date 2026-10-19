package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DashboardActivityStore struct{ pool *pgxpool.Pool }

var _ ports.DashboardActivityReader = (*DashboardActivityStore)(nil)

func NewDashboardActivityStore(pool *pgxpool.Pool) *DashboardActivityStore {
	return &DashboardActivityStore{pool: pool}
}

func (s *DashboardActivityStore) ReadDashboardActivity(ctx context.Context, actor *ports.Actor, limit int) (*ports.DashboardActivityPage, error) {
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	if actor.Type != ports.ActorTypeUser ||
		(actor.Role != ports.RoleAdmin && actor.Role != ports.RoleMember) ||
		(actor.Scopes != nil && !slices.Contains(actor.Scopes, "read")) {
		return nil, ports.ErrForbidden
	}
	if _, err := uuid.Parse(actor.ID); err != nil {
		return nil, ports.ErrForbidden
	}
	if limit == 0 {
		limit = 6
	}
	if limit < 1 || limit > 12 {
		return nil, ports.ErrInvalidOperationQuery
	}
	if s == nil || s.pool == nil {
		return nil, ports.ErrUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if err = operationReadPermission(ctx, q, actor); err != nil {
		if errors.Is(err, ports.ErrForbidden) {
			return nil, ports.ErrForbidden
		}
		return nil, ports.ErrUnavailable
	}
	observed, err := q.LibraryOverviewSnapshotTime(ctx)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	rows, err := q.ListVisibleDashboardOperationSummaries(ctx, db.ListVisibleDashboardOperationSummariesParams{
		IsAdmin: actor.IsAdmin(), ActorID: actor.ID, PageLimit: int32(limit + 1),
	})
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	out := &ports.DashboardActivityPage{Items: make([]ports.DashboardActivity, 0, limit), More: len(rows) > limit, ObservedAt: observed.Time}
	if out.More {
		rows = rows[:limit]
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.OperationID)
		out.Items = append(out.Items, ports.DashboardActivity{OperationID: row.OperationID, Title: row.Title, Status: row.Status})
	}
	// One batch in the same snapshot; cumulative native counters never supply a ratio.
	progress, err := q.ListDashboardActivityProgress(ctx, ids)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	byID := make(map[uuid.UUID]db.ListDashboardActivityProgressRow, len(progress))
	for _, row := range progress {
		byID[row.OperationID] = row
	}
	for i := range out.Items {
		item := &out.Items[i]
		row, ok := byID[item.OperationID]
		if !ok || row.State != "tracking" || !row.ContentPath.Valid || row.ContentPath.String == "" || row.Error.Valid || row.TotalBytes <= 0 {
			continue
		}
		value := float64(row.Progress)
		source := "native_percent"
		stamp := row.UpdatedAt
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
			source = "verified_bytes"
			stamp = row.ObservationCreatedAt
		} else if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
			continue
		}
		item.Progress = &value
		item.ProgressSource = source
		updated := stamp.Time
		item.UpdatedAt = &updated
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, ports.ErrUnavailable
	}
	return out, nil
}
