package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
)

var ErrTVEpisodeUpgradeConflict = errors.New("TV episode upgrade evidence changed; protection retained")
var ErrTVEpisodeUpgradeObservation = errors.New("TV episode upgrade outcome requires observation; no replay permitted")

// Only server-read canonical identities and quality evidence can grant an upgrade.
type TVEpisodeUpgradePreviewRequest struct {
	SubscriptionID uuid.UUID `json:"-"`
	UnitID         uuid.UUID `json:"unit_id"`
	CurrentFileID  uuid.UUID `json:"current_file_id"`
	CandidateID    uuid.UUID `json:"candidate_id"`
}
type TVEpisodeUpgradeCommand struct {
	TVEpisodeUpgradePreviewRequest
	Action          string    `json:"action"`
	OperationID     uuid.UUID `json:"operation_id,omitempty"`
	ExpectedVersion string    `json:"expected_version"`
	Confirmed       bool      `json:"confirmed"`
}
type TVEpisodeUpgradeResult struct {
	Status           string    `json:"status"`
	OperationID      uuid.UUID `json:"operation_id,omitempty"`
	UnitID           uuid.UUID `json:"unit_id"`
	CurrentFileID    uuid.UUID `json:"current_file_id"`
	CandidateID      uuid.UUID `json:"candidate_id"`
	NewFileID        uuid.UUID `json:"new_file_id,omitempty"`
	Version          string    `json:"version"`
	Reason           string    `json:"reason,omitempty"`
	Comparable       bool      `json:"comparable"`
	Improvement      bool      `json:"improvement"`
	CurrentScore     *int      `json:"current_score,omitempty"`
	CandidateScore   *int      `json:"candidate_score,omitempty"`
	ExecutionAllowed bool      `json:"execution_allowed"`
}
type TVEpisodeUpgradeRepository interface {
	PreviewTVEpisodeUpgrade(context.Context, *Actor, TVEpisodeUpgradePreviewRequest) (*TVEpisodeUpgradeResult, error)
	CommandTVEpisodeUpgrade(context.Context, *Actor, TVEpisodeUpgradeCommand, string) (*TVEpisodeUpgradeResult, error)
}

// Native configuration is ephemeral and excluded from every public serialization.
// The original client snapshot must be checked by the repository before each call.
type TVEpisodeUpgradeNativeConfig struct {
	ClientID          uuid.UUID `json:"-"`
	ClientType        string    `json:"-"`
	URL               string    `json:"-"`
	Username          string    `json:"-"`
	PasswordEncrypted []byte    `json:"-"`
}
type TVEpisodeUpgradeNative interface {
	FetchTVEpisodeUpgrade(context.Context, string, []byte, []byte) (*AutomaticTVMetainfoProof, error)
	OpenTVEpisodeUpgradeClient(context.Context, TVEpisodeUpgradeNativeConfig) (TVEpisodeUpgradeClient, func(), error)
}

// A bounded byte sender and complete manifest reader share the Manager budget.
// Release is called only after the physical call and result writeback return.
// Production readers normalize verified native completion to State="completed".
type TVEpisodeUpgradeClient interface {
	AddTorrent(context.Context, AddTorrentParam) error
	ListTorrents(context.Context, string) ([]*TorrentInfo, error)
	GetTorrentFiles(context.Context, string) ([]TorrentFile, error)
}
