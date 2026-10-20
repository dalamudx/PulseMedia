package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type DoubanSyncBindings struct {
	IndexerIDs   []uuid.UUID `json:"indexer_ids"`
	DownloaderID *uuid.UUID  `json:"downloader_id,omitempty"`
}

// Roots are optional while disabled. An enabled source requires at least one
// matching online root. ExpectedVersion zero is valid only for initial creation.
type DoubanSyncInput struct {
	DoubanUID       string              `json:"douban_uid"`
	Enabled         bool                `json:"enabled"`
	MovieRootID     *uuid.UUID          `json:"movie_root_id,omitempty"`
	TVRootID        *uuid.UUID          `json:"tv_root_id,omitempty"`
	ExpectedVersion int64               `json:"expected_version"`
	Bindings        *DoubanSyncBindings `json:"bindings,omitempty"`
}

type DoubanSyncConfig struct {
	ID          uuid.UUID           `json:"id"`
	OwnerID     uuid.UUID           `json:"owner_id"`
	DoubanUID   string              `json:"douban_uid"`
	Enabled     bool                `json:"enabled"`
	MovieRootID *uuid.UUID          `json:"movie_root_id,omitempty"`
	TVRootID    *uuid.UUID          `json:"tv_root_id,omitempty"`
	Version     int64               `json:"version"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
	Bindings    *DoubanSyncBindings `json:"bindings,omitempty"`
}

// A save receipt proves configuration persistence, not synchronization success.
type DoubanSyncConfigReceipt struct {
	CommandKey string           `json:"command_key"`
	Config     DoubanSyncConfig `json:"config"`
}

type DoubanSyncConfigRepository interface {
	ReadDoubanSyncConfig(context.Context, *Actor) (*DoubanSyncConfig, error)
	SaveDoubanSyncConfig(context.Context, *Actor, DoubanSyncInput, string) (*DoubanSyncConfigReceipt, error)
	ReadDoubanSyncConfigReceipt(context.Context, *Actor, string) (*DoubanSyncConfigReceipt, error)
}
