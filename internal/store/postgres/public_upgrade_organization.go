package postgres

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"time"
)

type PublicMovieUpgradeOrganizationStore struct {
	store *UpgradePreparationStore
	files ports.RootFilesystem
}

func NewPublicMovieUpgradeOrganizationStore(s *UpgradePreparationStore, f ports.RootFilesystem) (*PublicMovieUpgradeOrganizationStore, error) {
	if s == nil || f == nil {
		return nil, ports.ErrUnavailable
	}
	return &PublicMovieUpgradeOrganizationStore{store: s, files: f}, nil
}
func (r *PublicMovieUpgradeOrganizationStore) scoped(sub uuid.UUID) (*UpgradePreparationStore, error) {
	if sub == uuid.Nil {
		return nil, preparationConflict()
	}
	s := *r.store
	s.subscriptionScope = sub
	return &s, nil
}
func (r *PublicMovieUpgradeOrganizationStore) ReviewMovieUpgradeOrganization(ctx context.Context, a *ports.Actor, sub, op uuid.UUID) (*ports.MovieUpgradeOrganizationReview, error) {
	s, e := r.scoped(sub)
	if e != nil {
		return nil, e
	}
	repo, e := NewMovieUpgradeOrganizationRepository(s)
	if e != nil {
		return nil, e
	}
	return repo.ReadMovieUpgradeOrganization(ctx, a, op)
}
func (r *PublicMovieUpgradeOrganizationStore) AuthorizeMovieUpgradeOrganization(ctx context.Context, a *ports.Actor, sub uuid.UUID, c ports.AuthorizeMovieUpgradeOrganization, key string) (*ports.MovieUpgradeOrganizationAuthorization, error) {
	s, e := r.scoped(sub)
	if e != nil {
		return nil, e
	}
	repo, e := NewMovieUpgradeOrganizationRepository(s)
	if e != nil {
		return nil, e
	}
	return repo.AuthorizeMovieUpgradeOrganization(ctx, a, c, key)
}
func (r *PublicMovieUpgradeOrganizationStore) PrepareMovieUpgradeOrganization(ctx context.Context, a *ports.Actor, sub uuid.UUID, c ports.PrepareMovieUpgradeOrganization, key string) (*ports.MovieUpgradeOrganizationPreparation, error) {
	s, e := r.scoped(sub)
	if e != nil {
		return nil, e
	}
	repo, e := NewMovieUpgradeOrganizationPreparationRepository(s, r.files)
	if e != nil {
		return nil, e
	}
	return repo.PrepareMovieUpgradeOrganization(ctx, a, c, key)
}
func (r *PublicMovieUpgradeOrganizationStore) QueueMovieUpgradeOrganization(ctx context.Context, a *ports.Actor, sub uuid.UUID, c ports.QueueMovieUpgradeOrganization, key string) (*ports.MovieUpgradeOrganizationJobReceipt, error) {
	s, e := r.scoped(sub)
	if e != nil {
		return nil, e
	}
	repo, e := NewMovieUpgradeOrganizationJobRepository(s)
	if e != nil {
		return nil, e
	}
	return repo.QueueMovieUpgradeOrganization(ctx, a, c, key)
}
func (r *PublicMovieUpgradeOrganizationStore) ReadMovieUpgradeOrganizationState(ctx context.Context, a *ports.Actor, sub, op uuid.UUID) (out *ports.MovieUpgradeOrganizationState, err error) {
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	if op == uuid.Nil {
		return nil, preparationConflict()
	}
	s, e := r.scoped(sub)
	if e != nil {
		return nil, e
	}
	defer func() { err = upgradeError(ctx, err) }()
	work, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, e := s.pool.BeginTx(work, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(work, tx)
	if _, _, e = s.authorizePreparation(work, tx, a, op); e != nil {
		return nil, e
	}
	raw, e := db.New(tx).ReadMovieUpgradeOrganizationState(work, op)
	if e != nil {
		return nil, e
	}
	out = new(ports.MovieUpgradeOrganizationState)
	if e = json.Unmarshal(raw, out); e != nil {
		return nil, e
	}
	if out.OperationID != op || out.ExecutionAllowed || out.CurrentFileID == uuid.Nil {
		return nil, preparationConflict()
	}
	if _, _, e = s.authorizePreparation(work, tx, a, op); e != nil {
		return nil, e
	}
	if e = tx.Commit(work); e != nil {
		return nil, e
	}
	return out, nil
}

var _ ports.PublicMovieUpgradeOrganizationRepository = (*PublicMovieUpgradeOrganizationStore)(nil)
