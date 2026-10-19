package ports

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrAutomaticTVSearchDeferred = errors.New("automatic TV source budget or physical permit deferred")

// ErrAutomaticTVNotApplicable is returned only for an actual non-TV media target.
var ErrAutomaticTVNotApplicable = errors.New("wanted item belongs to a non-TV media")

// AutomaticTVRoundSnapshot is observation material for one standard aired
// episode. A round or a query alone never grants download permission.
type AutomaticTVRoundSnapshot struct {
	SchemaVersion       string           `json:"schema_version"`
	IdentityRuleVersion string           `json:"identity_rule_version"`
	ParserVersion       string           `json:"parser_version"`
	SubscriptionID      uuid.UUID        `json:"subscription_id"`
	OwnerID             uuid.UUID        `json:"owner_id"`
	WantedID            uuid.UUID        `json:"wanted_id"`
	IntentVersion       int64            `json:"intent_version"`
	PolicyID            string           `json:"policy_id"`
	PolicyRevision      int64            `json:"policy_revision"`
	Rules               json.RawMessage  `json:"rules"`
	MediaID             uuid.UUID        `json:"media_id"`
	MediaVersion        int64            `json:"media_version"`
	TMDBID              int64            `json:"tmdb_id"`
	Title               string           `json:"title"`
	OriginalTitle       string           `json:"original_title"`
	Year                int32            `json:"year"`
	UnitID              uuid.UUID        `json:"unit_id"`
	UnitKey             string           `json:"unit_key"`
	UnitVersion         int64            `json:"unit_version"`
	EpisodeID           int64            `json:"episode_id"`
	Season              int32            `json:"season"`
	Episode             int32            `json:"episode"`
	AirDate             time.Time        `json:"air_date"`
	MappingMode         string           `json:"mapping_mode"`
	MappingRevision     int64            `json:"mapping_revision"`
	RootID              uuid.UUID        `json:"root_id"`
	RootGeneration      int64            `json:"root_generation"`
	RootHealthRevision  int64            `json:"root_health_revision"`
	ClientID            uuid.UUID        `json:"client_id"`
	ClientStamp         string           `json:"client_stamp"`
	IndexerVersions     map[string]int64 `json:"indexer_versions"`
	ObservedAt          time.Time        `json:"observed_at"`
}

type AutomaticTVRoundStart struct {
	WantedID uuid.UUID
	Window   time.Duration
}

type AutomaticTVRound struct {
	ID           uuid.UUID
	State        string
	Deadline     time.Time
	Snapshot     AutomaticTVRoundSnapshot
	SnapshotHash string
}

type AutomaticTVSourceTask struct {
	Lease     JobLease  `json:"-"`
	RoundID   uuid.UUID `json:"round_id"`
	IndexerID uuid.UUID `json:"indexer_id"`
}

type AutomaticTVSourceWork struct {
	Task          AutomaticTVSourceTask
	SnapshotHash  string
	ConfigVersion int64
	Deadline      time.Time
	Query         TVEpisodeQuery
	SavedQueries  []uuid.UUID
	NextPage      int32
}

// AutomaticTVQueryBinding binds a single query to the original source job.
// ParametersHash is recomputed by the store from the persisted round and source.
type AutomaticTVQueryBinding struct {
	Task           AutomaticTVSourceTask
	SnapshotHash   string
	Query          TVEpisodeQuery
	ParametersHash string
}

type AutomaticTVSourceOutcome string

const (
	AutomaticTVSourceSucceeded AutomaticTVSourceOutcome = "succeeded"
	AutomaticTVSourcePartial   AutomaticTVSourceOutcome = "partial"
	AutomaticTVSourceFailed    AutomaticTVSourceOutcome = "failed"
	AutomaticTVSourceDeferred  AutomaticTVSourceOutcome = "deferred"
	AutomaticTVSourceTimeout   AutomaticTVSourceOutcome = "timeout"
)

type AutomaticTVRoundRepository interface {
	StartAutomaticTVRound(context.Context, AutomaticTVRoundStart) (*AutomaticTVRound, error)
	LoadAutomaticTVSource(context.Context, AutomaticTVSourceTask) (*AutomaticTVSourceWork, error)
	FinishAutomaticTVSource(context.Context, AutomaticTVSourceWork, AutomaticTVSourceOutcome) error
	RecoverAutomaticTVSources(context.Context, int32) (int, error)
}
