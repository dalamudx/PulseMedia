package postgres

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

// Implementation-only helper: caller already holds the execution authority and canonical locks.
func ResolveEpisodeFileNumbering(ctx context.Context, q *db.Queries, f ports.EpisodeExecutionSnapshot, title string) ([]media.NumberingRow, error) {
	a := &ports.Actor{ID: f.ActorID.String(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
	source, e := readEpisodeSourceContext(ctx, q, a, f.SubscriptionID)
	if e != nil {
		return nil, ports.ErrDownloadDemandChanged
	}
	canonical := []media.NumberingUnit{}
	for _, u := range source.Units {
		canonical = append(canonical, media.NumberingUnit{ID: u.ID, EpisodeID: u.EpisodeID, Season: u.Season, Episode: u.Episode, Key: u.Key, Version: u.Version, Retired: u.Retired})
	}
	rows, _ := f.ResolveNumbering(media.RecognizeRelease(title), canonical)
	if len(rows) != 1 || rows[0].UnitID != f.UnitID || rows[0].EpisodeID != f.EpisodeID || rows[0].UnitVersion != f.UnitVersion || rows[0].UnitKey != f.UnitKey {
		return nil, ports.ErrDownloadDemandChanged
	}
	return rows, nil
}
