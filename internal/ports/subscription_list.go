package ports

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrInvalidSubscriptionQuery = errors.New("invalid subscription query")

// SubscriptionListReader returns one authorized, bounded database snapshot.
type SubscriptionListReader interface {
	ReadSubscriptionList(context.Context, *Actor, SubscriptionListQuery) (*SubscriptionListPage, error)
}

type SubscriptionListQuery struct {
	Limit, Offset                   int
	Search, MediaType, TMDBID       string
	OwnOnly, Calendar, UnknownDates bool
	Start, End                      time.Time
}

type SubscriptionListPage struct {
	Items             []SubscriptionListEntry
	Total, Movies, TV int64
}

// SubscriptionListEntry is the list projection, independent of SQL and HTTP types.
type SubscriptionListEntry struct {
	ID, UserID, MediaID, RootID      uuid.UUID
	QualityPolicyID                  string
	QualityPolicyRevision            int64
	State                            string
	IntentVersion                    int64
	ScopeType                        string
	Title                            string
	Year                             *int
	MediaType                        string
	RootName                         string
	RootPath                         *string
	IndexerIDs                       []uuid.UUID
	DownloaderID                     *uuid.UUID
	TMDBExternalID                   string
	PosterURL                        *string
	TotalEpisodes, SatisfiedEpisodes int
	WantedItems                      []SubscriptionListWanted
}

type SubscriptionListWanted struct {
	ID, UnitID                          uuid.UUID
	UnitKey                             string
	IsSkipped, IsSatisfied              bool
	CurrentFactStage                    string
	UserSkipped, ScopeExcluded, Retired bool
	AirDate                             *time.Time
	AbsoluteNumber                      *int
	MetadataVersion                     int64
}
