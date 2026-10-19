package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// SubscriptionListReader loads the complete authorized list in one database snapshot.
// The current account is locked and revalidated before reading business data.
// Paths are present only for current administrators. External I/O follows the snapshot.
type SubscriptionListReader interface {
	ReadSubscriptionList(context.Context, *Actor) ([]SubscriptionListEntry, error)
}

// SubscriptionListEntry is the list projection, independent of SQL and HTTP types.
type SubscriptionListEntry struct {
	ID, UserID, MediaID, RootID uuid.UUID
	QualityPolicyID             string
	QualityPolicyRevision       int64
	State                       string
	IntentVersion               int64
	ScopeType                   string
	Title                       string
	Year                        *int
	MediaType                   string
	RootName                    string
	RootPath                    *string
	IndexerIDs                  []uuid.UUID
	DownloaderID                *uuid.UUID
	TMDBExternalID              string
	WantedItems                 []SubscriptionListWanted
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
