package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

type IndexerHealthJob struct {
	ID            uuid.UUID
	Owner         string
	Token         int64
	IndexerID     uuid.UUID `json:"indexer_id"`
	ConfigVersion int64     `json:"config_version"`
}
type IndexerHealthInfo struct {
	ID                        uuid.UUID
	Name, URL, Health, Reason string
	ConfigVersion             int64
	CreatedAt                 time.Time
	CheckedAt, ObservedAt     *time.Time
	NextCheckAt               time.Time
	Failures                  int32
}
type IndexerHealthRepository interface {
	ListHealth(context.Context) ([]IndexerHealthInfo, error)
	QueueHealth(context.Context, int32) (int, error)
	CheckHealth(context.Context, IndexerHealthJob) (bool, error)
	CompleteHealth(context.Context, IndexerHealthJob, *uuid.UUID) error
	ReportManualHealth(context.Context, uuid.UUID, int64, uuid.UUID) error
}
type IndexerProbe interface {
	ProbeIndexer(context.Context, IndexerHealthJob) (*IndexerQueryResult, error)
}
