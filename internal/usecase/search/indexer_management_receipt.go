package search

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// IndexerManagementReceiptService lets clients recover a lost response using
// only actor-bound command metadata, without caching credentials or secret URLs.
type IndexerManagementReceiptService struct {
	repo ports.IndexerManagementReceiptReader
}

func NewIndexerManagementReceiptService(repo ports.IndexerManagementReceiptReader) *IndexerManagementReceiptService {
	return &IndexerManagementReceiptService{repo: repo}
}

func (s *IndexerManagementReceiptService) Read(ctx context.Context, actor *ports.Actor, action ports.IndexerMutationAction, key string) (*ports.IndexerManagementReceipt, error) {
	if err := authorizeIndexerManagement(actor); err != nil {
		return nil, err
	}
	if !validIndexerManagementKey(key) {
		return nil, ports.ErrRejected
	}
	switch action {
	case ports.IndexerCreate, ports.IndexerUpdate, ports.IndexerDelete:
	default:
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	receipt, err := s.repo.ReadIndexerManagementReceipt(ctx, actor, action, key)
	if err != nil {
		return nil, safeIndexerManagementError(err)
	}
	if receipt == nil {
		return nil, ports.ErrUnavailable
	}
	return receipt, nil
}
