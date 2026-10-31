package ports

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/classification"
	"github.com/google/uuid"
	"time"
)

var (
	ErrInvalidClassification  = errors.New("invalid classification")
	ErrClassificationConflict = errors.New("classification version or target conflict")
	ErrClassificationEvidence = errors.New("insufficient classification evidence")
)

type ClassificationDocument struct {
	Version     int64                 `json:"version"`
	Rules       []classification.Rule `json:"rules"`
	PublishedAt *time.Time            `json:"published_at,omitempty"`
}
type ClassificationPublish struct {
	ExpectedVersion int64                 `json:"expected_version"`
	Rules           []classification.Rule `json:"rules"`
}
type ClassificationPreviewInput struct {
	Version      int64                 `json:"version"`
	MediaID      *uuid.UUID            `json:"media_id,omitempty"`
	MediaType    string                `json:"media_type,omitempty"`
	TMDBID       *int64                `json:"tmdb_id,omitempty"`
	TrustedFacts *classification.Facts `json:"-"`
}
type ClassificationPreview struct {
	Version      int64                 `json:"version"`
	PreviewStamp string                `json:"preview_stamp,omitempty"`
	ObservedAt   time.Time             `json:"observed_at"`
	Result       classification.Result `json:"result"`
}
type ClassificationRepository interface {
	ReadClassification(context.Context, *Actor) (*ClassificationDocument, error)
	PublishClassification(context.Context, *Actor, ClassificationPublish, string) (*ClassificationDocument, error)
	PreviewClassification(context.Context, *Actor, ClassificationPreviewInput) (*ClassificationPreview, error)
}
