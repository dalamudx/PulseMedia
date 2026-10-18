package ports

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/google/uuid"
)

// TVSelectionVersion identifies a trusted repository observation, never permission.
// ScopeStamp covers complete canonical/Wanted and availability inputs; configuration
// binds root, mapping, sources, client and individually observed candidate evidence.
// A request may carry this only as an optional expected-version fence.
type TVSelectionVersion struct {
	ActorID, SubscriptionID, MediaID uuid.UUID
	IntentVersion, MediaVersion      int64
	PolicyID                         string
	PolicyRevision                   int64
	ScopeStamp, ConfigurationStamp   string
	SelectionPolicyStamp             string
}

type TVSelectionCurrent struct {
	SourceTMDBID                        int64 // current parent provider identity
	Version                             TVSelectionVersion
	AccountAuthorized, ObjectAuthorized bool
	Active, RootReady, EndpointsReady   bool
	Canonical                           []EpisodeSourceUnit // complete current ordinary season, not just wanted
}

type TVSelectionWanted struct {
	UnitID                                uuid.UUID
	UnitVersion                           int64
	Selected, Missing, Aired, Unprotected bool
}

// Facts must be independently recomputed for each candidate, not copied from its
// historical review. EvidenceCurrent includes source/mapping/identity validity.
// The preview is conservative and accepts only standard numbering in this slice.
type TVSelectionCandidateFacts struct {
	CandidateID       uuid.UUID
	Version           TVSelectionVersion
	CurrentInputStamp string
	EvidenceCurrent   bool
	StandardNumbering bool
	Name              string
	Quality           quality.Profile
	Rows              []media.NumberingRow
}

type TVSelectionPack struct {
	Review               TVCoverageCandidateReviewReceipt
	CurrentMaterialStamp string
	Facts                TVSelectionCandidateFacts
}

type TVSelectionEpisode struct {
	Review EpisodeCandidateReviewReceipt
	Facts  TVSelectionCandidateFacts
}

type TVSelectionPreviewInput struct {
	Expected TVSelectionVersion
	Current  TVSelectionCurrent
	Policy   PolicyDocument
	// Explicit input only: published PolicyRules JSON has no pack-selection field.
	PackPolicy quality.PackPolicy
	Season     CompleteSeasonMetadata
	Wanted     []TVSelectionWanted // every canonical unit, including unselected/satisfied
	Packs      []TVSelectionPack
	Episodes   []TVSelectionEpisode
}

// TVSelectionCandidateRef identifies a persisted review; it carries no authority.
type TVSelectionCandidateRef struct{ CandidateID, ReviewID uuid.UUID }

// TVSelectionRequest contains only identity, explicit preferences and an optional fence.
type TVSelectionRequest struct {
	SubscriptionID  uuid.UUID
	Season          int
	PackPolicy      quality.PackPolicy
	Packs, Episodes []TVSelectionCandidateRef
	Expected        *TVSelectionVersion
}

// TVSelectionRepository assembles all evidence in one current authorized snapshot.
// Neither the observation nor any preview is acquisition authorization.
type TVSelectionRepository interface {
	ReadTVSelection(context.Context, *Actor, TVSelectionRequest) (*TVSelectionPreviewInput, error)
}

