package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
)

var ErrMovieRecognitionLease = errors.New("movie recognition work authorization expired")

type MovieRecognitionWork struct {
	Lease          JobLease
	RootID, FileID uuid.UUID
	Generation     int64
	RelPath        string
}

type MovieRecognitionScheduler interface {
	QueueDue(context.Context, int32) (int, error)
}
type MovieRecognitionRepository interface {
	MovieLinkRepository
	Review(context.Context, string) error
}
