package ports

import (
	"context"
	"time"
)

// TVDB facts retain their own identity and numbering; they do not grant canonical unit updates.
type TVDBRemoteIdentity struct {
	ID         string `json:"id"`
	Type       int64  `json:"type"`
	SourceName string `json:"sourceName"`
}
type TVDBSeasonDetails struct {
	TVDBID       int64
	SeasonNumber int
	TypeID       int64
	Numbering    string
	Name         string
	EpisodeCount *int
}
type TVDBSeriesDetails struct {
	TVDBID           int64
	Title            string
	OriginalLanguage string
	Year             int
	Overview         string
	Seasons          []TVDBSeasonDetails
	RemoteIDs        []TVDBRemoteIdentity
}
type TVDBTranslation struct {
	TVDBID   int64
	Language string
	Title    string
	Overview string
}
type TVDBEpisode struct {
	TVDBID         int64
	SeasonNumber   int
	EpisodeNumber  int
	Title          string
	AirDate        *time.Time
	AbsoluteNumber *int
}
type TVDBSeasonMetadata struct {
	TVDBID       int64
	SeasonNumber int
	Numbering    string
	Episodes     []TVDBEpisode
	Complete     bool
}
type TVDBProvider interface {
	GetSeriesDetails(context.Context, int64) (*TVDBSeriesDetails, error)
	GetSeriesTranslation(context.Context, int64, string) (*TVDBTranslation, error)
	GetSeasonMetadata(context.Context, int64, int) (*TVDBSeasonMetadata, error)
}
