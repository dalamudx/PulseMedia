package ports

import "context"

type TVEpisodeGroupSummary struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         int    `json:"type"`
	EpisodeCount int    `json:"episode_count"`
	GroupCount   int    `json:"group_count"`
}
type TVEpisodeGroupEpisode struct {
	ID      int64 `json:"id"`
	Season  int   `json:"season"`
	Episode int   `json:"episode"`
	Order   int   `json:"order"`
}
type TVEpisodeGroupPart struct {
	ID       string                  `json:"id"`
	Name     string                  `json:"name"`
	Order    int                     `json:"order"`
	Episodes []TVEpisodeGroupEpisode `json:"episodes"`
}

// Explicit provider order is a numbering view, never an instruction to rewrite canonical units.
type TVEpisodeGroupSnapshot struct {
	SeriesID int64                 `json:"series_id"`
	Summary  TVEpisodeGroupSummary `json:"summary"`
	Parts    []TVEpisodeGroupPart  `json:"parts"`
}
type TVEpisodeGroupProvider interface {
	ListTVEpisodeGroups(context.Context, int64) ([]TVEpisodeGroupSummary, error)
	GetTVEpisodeGroup(context.Context, int64, string) (*TVEpisodeGroupSnapshot, error)
}
