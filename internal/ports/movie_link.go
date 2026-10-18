package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
)

var ErrMovieLinkStale = errors.New("movie file source changed")
var ErrMovieLinkConflict = errors.New("movie file requires identification review")

// MovieFileSnapshot freezes the registered file and root before metadata I/O.
type MovieFileSnapshot struct {
	Root                            ScanRootSnapshot
	FileID                          uuid.UUID
	RelPath                         string
	SizeBytes, MtimeNS, MutationSeq int64
	Fingerprint                     string
	FingerprintKnown                bool
	MappingRevision                 int64
	MappingStamp                    []byte
}
type LinkedMovie struct {
	ID                   uuid.UUID
	Title, OriginalTitle string
	Year                 int
	MediaType            string
	MetadataVersion      int64
}
type LinkedMovieUnit struct {
	ID, MediaID uuid.UUID
	UnitKey     string
}
type MovieLinkResult struct {
	Media LinkedMovie
	Unit  LinkedMovieUnit
}
type MovieLinkInput struct {
	Source        MovieFileSnapshot
	Metadata      MovieMetadata
	QualityJSON   []byte
	PhysicalCheck *MovieFileCheck
}
type MovieLinkRepository interface {
	LoadSource(context.Context, uuid.UUID, string) (MovieFileSnapshot, error)
	Link(context.Context, MovieLinkInput) (MovieLinkResult, error)
}
