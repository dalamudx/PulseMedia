package ports

import "context"

// TVEpisodeQuery is the complete, non-secret provider request. Its catalog
// identity is frozen as proof material even when the provider accepts q only.
// Query parameters and pagination are part of query identity; credentials are not.
type TVEpisodeQuery struct {
	SchemaVersion   string  `json:"schema_version"`
	Mode            string  `json:"mode"`
	SeriesNamespace string  `json:"series_namespace"`
	SeriesID        int64   `json:"series_id"`
	Keyword         string  `json:"keyword"`
	Season          int32   `json:"season"`
	Episode         int32   `json:"episode"`
	Categories      []int32 `json:"categories"`
	Offset          int32   `json:"offset"`
	Limit           int32   `json:"limit"`
}

// TVEpisodePageClient never silently falls back to movie or untyped search.
// The caller owns persistent budget, job authority and the physical permit.
type TVEpisodePageClient interface {
	SearchTVEpisodePage(context.Context, TVEpisodeQuery) (*IndexerPage, error)
}
