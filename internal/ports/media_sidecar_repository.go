package ports

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

const MediaSidecarJobKind = "media_sidecar"

var (
	ErrMediaSidecarStale       = errors.New("media sidecar authority changed")
	ErrMediaSidecarUnavailable = errors.New("media sidecar baseline or metadata unavailable")
	ErrMediaSidecarResult      = errors.New("invalid media sidecar result")
)

// MediaSidecarPayload is a system continuation of a committed media.imported
// event. No path or user acquisition authority is accepted from the payload.
type MediaSidecarPayload struct {
	Version       int       `json:"version"`
	SourceEventID uuid.UUID `json:"source_event_id"`
	FileID        uuid.UUID `json:"file_id"`
	UnitID        uuid.UUID `json:"unit_id"`
}

// MediaSidecarMapping exposes all active mappings for the source, so the usecase
// can prove a canonical work directory without assuming every TV path has two parents.
type MediaSidecarMapping struct {
	UnitID, MediaID             uuid.UUID
	UnitKey                     string
	SeasonNumber, EpisodeNumber *int32
	MetadataVersion             int64
}

// MediaSidecarSnapshot contains DB evidence only. Binding is opaque and binds
// the current event, payload, root, file, mappings, facts and metadata in the DB.
// RootIdentity MUST come from the current generation's first completed scan;
// absence never authorizes bootstrapping from a filesystem observation.
type MediaSidecarSnapshot struct {
	Binding                                                     string
	Source                                                      MediaSidecarPayload
	RootID                                                      uuid.UUID
	RootPath, RootMediaType, MarkerFilename, RootHealth         string
	RootGeneration, RootHealthRevision                          int64
	RootIdentity                                                FileIdentity
	FileRelative, FileFingerprint                               string
	FileIdentity                                                FileIdentity
	FileMappingRevision, FileMutationSequence                   int64
	MediaID                                                     uuid.UUID
	MediaType, Title, OriginalTitle                             string
	Year                                                        *int32
	TMDBID                                                      int64
	MediaMetadataVersion, UnitMetadataVersion, UnitFactsVersion int64
	UnitKey                                                     string
	SeasonNumber, EpisodeNumber                                 *int32
	Mappings                                                    []MediaSidecarMapping
	// PosterPath is optional and only comes from the exact current TMDB identity
	// and media metadata version. It is an opaque provider path, never a URL.
	PosterPath string
}

// MediaSidecarArtifactResult binds an actual filesystem outcome to one allowed
// name: movie.nfo/tvshow.nfo, poster.jpg, logo.png, clearart.png, banner.jpg,
// or movie-only disc.png. Preserved regular entries never imply job ownership.
type MediaSidecarArtifactResult struct {
	Name      string        `json:"name"`
	Result    SidecarResult `json:"result"`
	Content   []byte        `json:"-"`
	Directory string        `json:"-"`
}

type MediaSidecarCompletion struct {
	Artifacts []MediaSidecarArtifactResult `json:"artifacts"`
}

// MediaSidecarRepository performs short fenced DB transactions only. The caller
// holds the physical root capability through all I/O, validates the configured
// root/marker/source/destination, calls Validate immediately before effects and
// awaits actual I/O completion before Complete. Uncertain outcomes become dead
// review_required jobs; they must never be replayed as claimed successful writes.
type MediaSidecarRepository interface {
	Load(context.Context, JobLease) (MediaSidecarSnapshot, error)
	Validate(context.Context, JobLease, MediaSidecarSnapshot) error
	Complete(context.Context, JobLease, MediaSidecarSnapshot, MediaSidecarCompletion) error
}
