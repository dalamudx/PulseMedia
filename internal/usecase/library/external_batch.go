package library

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strconv"
)

type ExternalBatchService struct {
	repo          ports.ExternalBatchRepository
	reviews       ports.ExternalBatchReviews
	organizations ports.ExternalBatchOrganizations
}

func NewExternalBatchService(repo ports.ExternalBatchRepository, reviews ports.ExternalBatchReviews, organizations ports.ExternalBatchOrganizations) *ExternalBatchService {
	return &ExternalBatchService{repo: repo, reviews: reviews, organizations: organizations}
}
func (s *ExternalBatchService) Create(ctx context.Context, a *ports.Actor, scope uuid.UUID, in ports.ExternalBatchInput, key string) (*ports.ExternalBatchView, error) {
	if _, err := untrackedReviewActor(ctx, a, scope, scope, key); err != nil {
		return nil, err
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	if len(in.Rows) < 1 || len(in.Rows) > ports.ExternalBatchLimit {
		return nil, ports.ErrRejected
	}
	files, targets := map[uuid.UUID]bool{}, map[string]bool{}
	for _, r := range in.Rows {
		if r.FileID == uuid.Nil || !untrackedReviewConfirmationInput(r.Confirmation) || files[r.FileID] || targets[r.Confirmation.TargetRelativePath] {
			return nil, ports.ErrRejected
		}
		files[r.FileID] = true
		targets[r.Confirmation.TargetRelativePath] = true
	}
	b, err := s.repo.CreateExternalBatch(ctx, a, scope, in, key)
	if err != nil {
		return nil, err
	}
	return s.view(ctx, a, b)
}
func (s *ExternalBatchService) Read(ctx context.Context, a *ports.Actor, scope, id uuid.UUID, key string) (*ports.ExternalBatchView, error) {
	b, err := s.manifest(ctx, a, scope, id, key)
	if err != nil {
		return nil, err
	}
	return s.view(ctx, a, b)
}
func (s *ExternalBatchService) manifest(ctx context.Context, a *ports.Actor, scope, id uuid.UUID, key string) (*ports.ExternalBatch, error) {
	if _, err := untrackedReviewActor(ctx, a, scope, scope, "read"); err != nil {
		return nil, err
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	if (id == uuid.Nil) == (key == "") || key != "" && !untrackedScopeKey(key) {
		return nil, ports.ErrRejected
	}
	b, err := s.repo.ReadExternalBatch(ctx, a, scope, id, key)
	if err != nil {
		return nil, err
	}
	if b == nil || b.ID == uuid.Nil || b.ActorID.String() != a.ID || b.ScopeID != scope || b.ExecutionAllowed || len(b.Rows) < 1 || len(b.Rows) > ports.ExternalBatchLimit {
		return nil, ports.ErrUnavailable
	}
	return b, nil
}
func externalBatchKey(id uuid.UUID, row int, kind string) string {
	return "external-batch:" + id.String() + ":" + strconv.Itoa(row) + ":" + kind
}
func (s *ExternalBatchService) Confirm(ctx context.Context, a *ports.Actor, scope, id uuid.UUID, row int) (*ports.UntrackedConfirmationReceipt, error) {
	b, err := s.manifest(ctx, a, scope, id, "")
	if err != nil {
		return nil, err
	}
	if row < 0 || row >= len(b.Rows) {
		return nil, ports.ErrRejected
	}
	if s.reviews == nil {
		return nil, ports.ErrUnavailable
	}
	r := b.Rows[row]
	return s.reviews.Confirm(ctx, a, scope, r.Selection.FileID, r.Selection.Confirmation, externalBatchKey(id, row, "confirm"))
}
func (s *ExternalBatchService) Organize(ctx context.Context, a *ports.Actor, scope, id uuid.UUID, row int) (*ports.UntrackedOrganizationReceipt, error) {
	b, err := s.manifest(ctx, a, scope, id, "")
	if err != nil {
		return nil, err
	}
	if row < 0 || row >= len(b.Rows) {
		return nil, ports.ErrRejected
	}
	if s.reviews == nil || s.organizations == nil {
		return nil, ports.ErrUnavailable
	}
	r := b.Rows[row]
	c, err := s.reviews.ConfirmationReceipt(ctx, a, scope, r.Selection.FileID, externalBatchKey(id, row, "confirm"))
	if err != nil {
		return nil, err
	}
	if c == nil || c.Material.Input != r.Selection.Confirmation {
		return nil, ports.ErrUnavailable
	}
	return s.organizations.Organize(ctx, a, scope, r.Selection.FileID, ports.UntrackedOrganizationInput{ConfirmationID: c.Material.ID}, externalBatchKey(id, row, "organize"))
}
func (s *ExternalBatchService) view(ctx context.Context, a *ports.Actor, b *ports.ExternalBatch) (*ports.ExternalBatchView, error) {
	if b == nil || b.ActorID.String() != a.ID || b.ExecutionAllowed || len(b.Rows) < 1 || len(b.Rows) > ports.ExternalBatchLimit || s.reviews == nil || s.organizations == nil {
		return nil, ports.ErrUnavailable
	}
	out := &ports.ExternalBatchView{Batch: *b, Status: make([]ports.ExternalBatchRowStatus, len(b.Rows))}
	for i, r := range b.Rows {
		c, err := s.reviews.ConfirmationReceipt(ctx, a, b.ScopeID, r.Selection.FileID, externalBatchKey(b.ID, i, "confirm"))
		if err != nil && !errors.Is(err, ports.ErrNotFound) {
			return nil, err
		}
		out.Status[i].Confirmation = c
		o, err := s.organizations.Receipt(ctx, a, b.ScopeID, r.Selection.FileID, externalBatchKey(b.ID, i, "organize"))
		if err != nil && !errors.Is(err, ports.ErrNotFound) {
			return nil, err
		}
		out.Status[i].Organization = o
		if o != nil {
			p, e := s.organizations.Read(ctx, a, b.ScopeID, r.Selection.FileID, o.Operation.ID)
			if e != nil {
				return nil, e
			}
			out.Status[i].Progress = p
		}
	}
	return out, nil
}
