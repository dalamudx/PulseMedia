package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

var ErrMoviePhysicalCheck = errors.New("movie file physical evidence unavailable")

// MovieFileCheck timestamps the start of physical observation using database time.
// RootIdentity belongs to the original complete, source-qualified scan.
type MovieFileCheck struct {
	StartedAt    time.Time
	ScanID       uuid.UUID
	RootIdentity FileIdentity
}

type CheckedMovieRecognitionRepository interface {
	MovieRecognitionRepository
	BeginFileCheck(context.Context, MovieFileSnapshot) (MovieFileCheck, error)
}
