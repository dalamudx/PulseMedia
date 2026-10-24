package ports

import "context"

// Bangumi identity is independent of TMDB; a calendar item grants no execution authority.
type BangumiItem struct {
	ID        int64
	Title     string
	OrigTitle string
	AirDate   string // Native first broadcast date, never an episode air date; empty means unknown.
	PosterURL string
	Rating    *float64
}
type BangumiDay struct {
	DayID   int
	DayName string
	Items   []BangumiItem
}
type BangumiCalendarProvider interface {
	GetCalendar(context.Context) ([]BangumiDay, error)
}
