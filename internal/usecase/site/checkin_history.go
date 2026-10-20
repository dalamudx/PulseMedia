package site

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/indexer"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type CheckinHistoryReader struct {
	repo ports.SiteCheckinHistoryReader
}

func NewCheckinHistoryReader(repo ports.SiteCheckinHistoryReader) *CheckinHistoryReader {
	return &CheckinHistoryReader{repo: repo}
}

type CheckinHistoryPage struct {
	Config     *ports.SiteCheckinConfig   `json:"config"`
	Rows       []ports.SiteCheckinDayView `json:"rows"`
	NextCursor string                     `json:"next_cursor"`
}

type checkinHistoryToken struct {
	Version     int       `json:"version"`
	SourceID    uuid.UUID `json:"source_id"`
	CalendarDay string    `json:"calendar_day"`
	DayID       uuid.UUID `json:"day_id"`
}

func checkinHistoryDay(day string) bool {
	t, err := time.Parse(time.DateOnly, day)
	return err == nil && t.Format(time.DateOnly) == day
}
func checkinHistoryEncode(source uuid.UUID, c ports.SiteCheckinHistoryCursor) string {
	b, _ := json.Marshal(checkinHistoryToken{1, source, c.CalendarDay, c.DayID})
	return base64.RawURLEncoding.EncodeToString(b)
}
func checkinHistoryDecode(source uuid.UUID, token string) (*ports.SiteCheckinHistoryCursor, error) {
	if token == "" {
		return nil, nil
	}
	if len(token) > 512 {
		return nil, ports.ErrRejected
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil {
		return nil, ports.ErrRejected
	}
	var v checkinHistoryToken
	if json.Unmarshal(b, &v) != nil || v.Version != 1 || v.SourceID != source || v.SourceID == uuid.Nil || v.DayID == uuid.Nil || !checkinHistoryDay(v.CalendarDay) {
		return nil, ports.ErrRejected
	}
	canonical, _ := json.Marshal(v)
	if !bytes.Equal(b, canonical) || base64.RawURLEncoding.EncodeToString(b) != token {
		return nil, ports.ErrRejected
	}
	return &ports.SiteCheckinHistoryCursor{CalendarDay: v.CalendarDay, DayID: v.DayID}, nil
}
func checkinHistoryRow(v ports.SiteCheckinDayView, source uuid.UUID) bool {
	if v.ID == uuid.Nil || v.IndexerID != source || !indexer.ValidSiteAccountID(v.ExpectedAccountID) || v.Profile != ports.NexusPHP1103CheckinProfile || !indexer.ValidSiteCheckinSchedule(v.Timezone, "00:00") || !checkinHistoryDay(v.CalendarDay) || v.DayStart.IsZero() || !v.DayStart.Before(v.DayEnd) || v.ConfigVersion < 1 || v.SourceVersion < 1 {
		return false
	}
	switch v.State {
	case "open", "prepared", "attended_observed", "review_required":
	default:
		return false
	}
	switch v.SafeFailureCode {
	case "", "auth_required", "blocked", "rate_limited", "bad_response", "unavailable":
	default:
		return false
	}
	if v.State == "prepared" && !v.Prepared {
		return false
	}
	if (v.ObservedAt != nil) != (v.State == "attended_observed") {
		return false
	}
	return v.ObservedAt == nil || (!v.ObservedAt.Before(v.DayStart) && v.ObservedAt.Before(v.DayEnd))
}
func checkinHistoryBefore(a, b ports.SiteCheckinHistoryCursor) bool {
	return a.CalendarDay < b.CalendarDay || a.CalendarDay == b.CalendarDay && bytes.Compare(a.DayID[:], b.DayID[:]) < 0
}

// Read returns only persisted history, including previous explicit account identities.
func (s *CheckinHistoryReader) Read(ctx context.Context, actor *ports.Actor, source uuid.UUID, limit int32, cursor string) (*CheckinHistoryPage, error) {
	a, err := accountStatsConfigActor(ctx, actor, source)
	if err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return nil, ports.ErrRejected
	}
	c, err := checkinHistoryDecode(source, cursor)
	if err != nil {
		return nil, err
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	page, err := s.repo.ReadSiteCheckinHistory(ctx, &a, source, limit, c)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, accountStatsConfigError(err)
	}
	if page == nil || len(page.Rows) > int(limit) {
		return nil, ports.ErrUnavailable
	}
	out := &CheckinHistoryPage{Rows: make([]ports.SiteCheckinDayView, len(page.Rows))}
	if page.Config != nil {
		if !checkinConfigValid(*page.Config, source) {
			return nil, ports.ErrUnavailable
		}
		config := *page.Config
		out.Config = &config
	}
	previous, err := checkinHistoryDecode(source, cursor)
	if err != nil {
		return nil, err
	}
	for i, row := range page.Rows {
		if !checkinHistoryRow(row, source) {
			return nil, ports.ErrUnavailable
		}
		position := ports.SiteCheckinHistoryCursor{CalendarDay: row.CalendarDay, DayID: row.ID}
		if previous != nil && !checkinHistoryBefore(position, *previous) {
			return nil, ports.ErrUnavailable
		}
		previous = &position
		out.Rows[i] = row
		if row.ObservedAt != nil {
			at := *row.ObservedAt
			out.Rows[i].ObservedAt = &at
		}
	}
	if page.Next != nil {
		if len(page.Rows) != int(limit) || previous == nil || *page.Next != *previous {
			return nil, ports.ErrUnavailable
		}
		out.NextCursor = checkinHistoryEncode(source, *page.Next)
	}
	return out, nil
}
