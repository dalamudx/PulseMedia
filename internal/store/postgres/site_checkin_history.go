package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/indexer"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SiteCheckinHistoryStore struct{ pool *pgxpool.Pool }

var _ ports.SiteCheckinHistoryReader = (*SiteCheckinHistoryStore)(nil)

func NewSiteCheckinHistoryStore(pool *pgxpool.Pool) *SiteCheckinHistoryStore {
	return &SiteCheckinHistoryStore{pool: pool}
}
func historyDate(day string) bool {
	t, e := time.Parse(time.DateOnly, day)
	return e == nil && t.Format(time.DateOnly) == day
}
func historyView(row db.ListSiteCheckinHistoryRow) (ports.SiteCheckinDayView, error) {
	v := ports.SiteCheckinDayView{ID: row.ID, IndexerID: row.IndexerID, ExpectedAccountID: row.ExpectedAccountID, Profile: row.Profile, Timezone: row.Timezone, CalendarDay: row.CalendarDay, DayStart: row.DayStart.Time, DayEnd: row.DayEnd.Time, State: row.Status, SafeFailureCode: row.SafeCode, Prepared: row.Prepared, ConfigVersion: row.ConfigVersion, SourceVersion: row.SourceVersion, RestoreAcknowledged: row.RestoreAcknowledged}
	if v.ID == uuid.Nil || v.IndexerID == uuid.Nil || !indexer.ValidSiteAccountID(v.ExpectedAccountID) || v.Profile != ports.NexusPHP1103CheckinProfile || !indexer.ValidSiteCheckinSchedule(v.Timezone, "00:00") || !historyDate(v.CalendarDay) || !row.DayStart.Valid || !row.DayEnd.Valid || row.DayStart.InfinityModifier != 0 || row.DayEnd.InfinityModifier != 0 || !v.DayStart.Before(v.DayEnd) || v.ConfigVersion < 1 || v.SourceVersion < 1 {
		return v, ports.ErrUnavailable
	}
	switch v.State {
	case "open", "prepared", "attended_observed", "review_required":
	default:
		return v, ports.ErrUnavailable
	}
	switch v.SafeFailureCode {
	case "", "auth_required", "blocked", "rate_limited", "bad_response", "unavailable":
	default:
		return v, ports.ErrUnavailable
	}
	if v.State == "prepared" && !v.Prepared || row.ObservedAt.Valid != (v.State == "attended_observed") {
		return v, ports.ErrUnavailable
	}
	if row.ObservedAt.Valid {
		at := row.ObservedAt.Time
		if row.ObservedAt.InfinityModifier != 0 || at.Before(v.DayStart) || !at.Before(v.DayEnd) {
			return v, ports.ErrUnavailable
		}
		v.ObservedAt = &at
	}
	return v, nil
}
func (s *SiteCheckinHistoryStore) ReadSiteCheckinHistory(ctx context.Context, actor *ports.Actor, source uuid.UUID, limit int32, cursor *ports.SiteCheckinHistoryCursor) (out *ports.SiteCheckinHistoryPage, err error) {
	defer func() {
		err = statsError(err)
		if err != nil {
			out = nil
		}
	}()
	a, err := checkinActor(actor)
	if err != nil {
		return nil, err
	}
	if source == uuid.Nil {
		return nil, ports.ErrRejected
	}
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return nil, ports.ErrRejected
	}
	position := ports.SiteCheckinHistoryCursor{CalendarDay: "2000-01-01"}
	if cursor != nil {
		position = *cursor
		if position.DayID == uuid.Nil || !historyDate(position.CalendarDay) {
			return nil, ports.ErrRejected
		}
	}
	if s == nil || s.pool == nil {
		return nil, ports.ErrUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	// History inspection remains available during restore isolation.
	if err = lockAcquisitionUser(ctx, q, a); err != nil {
		return nil, err
	}
	if _, err = q.GetSiteCheckinHistorySource(ctx, source); errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	if err = lockAcquisitionUser(ctx, q, a); err != nil {
		return nil, err
	}
	out = &ports.SiteCheckinHistoryPage{Rows: []ports.SiteCheckinDayView{}}
	config, err := q.GetSiteCheckinConfig(ctx, source)
	if err == nil {
		out.Config, err = checkinSnapshot(ctx, q, config)
		if err != nil {
			return nil, ports.ErrUnavailable
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	rows, err := q.ListSiteCheckinHistory(ctx, db.ListSiteCheckinHistoryParams{IndexerID: source, HasCursor: cursor != nil, CursorDay: position.CalendarDay, CursorID: position.DayID, PageLimit: limit + 1})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		v, e := historyView(row)
		if e != nil || v.IndexerID != source {
			return nil, ports.ErrUnavailable
		}
		out.Rows = append(out.Rows, v)
	}
	if len(out.Rows) > int(limit) {
		out.Rows = out.Rows[:limit]
		last := out.Rows[len(out.Rows)-1]
		out.Next = &ports.SiteCheckinHistoryCursor{CalendarDay: last.CalendarDay, DayID: last.ID}
	}
	if err = lockAcquisitionUser(ctx, q, a); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
