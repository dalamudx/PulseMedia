package ports

import "context"

// MetadataCastMember is display-only provider evidence. It grants no work identity.
type MetadataCastMember struct {
	TMDBID     int64
	Name       string
	Character  string
	ProfileURL string
}

type MetadataCredits struct {
	TMDBID    int64
	MediaType string
	Cast      []MetadataCastMember
}

// MetadataEpisodeDetails preserves the actual provider number, including sparse seasons.
// Empty names, dates and images remain unknown rather than being synthesized.
type MetadataEpisodeDetails struct {
	TMDBID        int64
	EpisodeNumber int
	Name          string
	Overview      string
	AirDate       string
	StillURL      string
}

type MetadataSeasonDetails struct {
	TMDBID       int64
	SeasonNumber int
	Episodes     []MetadataEpisodeDetails
	Complete     bool
}

// MetadataDetailBrowser is a read capability separate from frozen creation material.
// Implementations retain the physical metadata request budget until I/O returns.
type MetadataDetailBrowser interface {
	GetMediaCredits(context.Context, string, int64, string) (*MetadataCredits, error)
	GetTVSeasonDetails(context.Context, int64, int, string) (*MetadataSeasonDetails, error)
}
