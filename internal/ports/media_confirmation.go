package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
)

var ErrMediaFileRetired = errors.New("cannot confirm retired library file")

type MediaServerConnection struct {
	ID           uuid.UUID
	Kind, URL    string
	EncryptedKey []byte
}
type MediaConfirmationInput struct {
	Server                       MediaServerConnection
	FileID, UnitID, RootID       uuid.UUID
	LocalPath, RemotePath, Stamp string
}
type MediaConfirmationFact struct {
	ID, UnitID, SourceFileID, RootID uuid.UUID
	Stage                            string
}
type MediaConfirmationRepository interface {
	MediaRefreshRepository
	LoadMediaServerConnection(context.Context, uuid.UUID) (MediaServerConnection, error)
	LoadMediaConfirmation(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*MediaConfirmationInput, error)
	CommitMediaConfirmation(context.Context, MediaConfirmationInput, string) (*MediaConfirmationFact, error)
}

type MediaRefreshInput struct {
	Confirmation MediaConfirmationInput
	DeliveryID   uuid.UUID
	Owner        string
	Token        int64
}
type MediaRefreshRepository interface {
	AcquireMediaRefreshPermit(context.Context, uuid.UUID) (func(), error)
	LoadMediaRefresh(context.Context, uuid.UUID, uuid.UUID, string, int64) (*MediaRefreshInput, error)
	ValidateMediaRefresh(context.Context, MediaRefreshInput) error
	FinishMediaRefresh(context.Context, MediaRefreshInput) error
}
