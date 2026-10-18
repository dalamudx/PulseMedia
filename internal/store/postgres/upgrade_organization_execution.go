package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type movieUpgradeOrganizationExecutionRepository struct {
	store *UpgradePreparationStore
	files ports.TransferFilesystem
}

func NewMovieUpgradeOrganizationExecutionRepository(s *UpgradePreparationStore, files ports.TransferFilesystem) (ports.MovieUpgradeOrganizationExecutionRepository, error) {
	if s == nil || files == nil {
		return nil, ports.ErrUnavailable
	}
	return &movieUpgradeOrganizationExecutionRepository{s, files}, nil
}

type organizationExecution struct {
	repo   *movieUpgradeOrganizationExecutionRepository
	base   *movieUpgradeSendSession
	lease  ports.JobLease
	actor  *ports.Actor
	permit json.RawMessage
}

func (r *movieUpgradeOrganizationExecutionRepository) ExecuteMovieUpgradeOrganization(ctx context.Context, a *ports.Actor, lease ports.JobLease) (out *ports.MovieUpgradeOrganizationExecutionResult, err error) {
	if err = episodeReviewActor(a); err != nil {
		return nil, err
	}
	if lease.ID == uuid.Nil || lease.Owner == "" || len(lease.Owner) > 130 || lease.Token <= 0 {
		return nil, preparationConflict()
	}
	defer func() {
		if !errors.Is(err, ports.ErrMovieUpgradeOrganizationReview) {
			err = upgradeError(ctx, err)
		}
	}()
	if out, err = r.ReadMovieUpgradeOrganizationExecutionReceipt(ctx, a, lease.ID); err != nil || out != nil {
		return out, err
	}
	var op uuid.UUID
	work, done := context.WithTimeout(ctx, 15*time.Second)
	err = r.store.pool.QueryRow(work, `SELECT operation_id FROM movie_upgrade_organization_job WHERE job_id=$1`, lease.ID).Scan(&op)
	done()
	if err != nil {
		return nil, err
	}
	work, done = context.WithTimeout(ctx, 15*time.Second)
	opened, err := r.store.OpenMovieUpgradeSendSession(work, op)
	done()
	if err != nil {
		if errors.Is(err, ports.ErrMovieUpgradeSendObservation) {
			return nil, ports.ErrMovieUpgradeOrganizationReview
		}
		return nil, err
	}
	base := opened.(*movieUpgradeSendSession)
	defer func() {
		if e := base.Close(context.WithoutCancel(ctx)); err == nil && e != nil {
			err = e
		}
	}()
	if err = base.acquire(ctx); err != nil {
		return nil, err
	}
	defer func() { <-base.gate }()
	actor := *a
	actor.Scopes = append([]string(nil), a.Scopes...)
	s := &organizationExecution{repo: r, base: base, lease: lease, actor: &actor}
	work, done = context.WithTimeout(ctx, 15*time.Second)
	tx, err := base.conn.Begin(work)
	if err != nil {
		done()
		return nil, err
	}
	var exists bool
	err = tx.QueryRow(work, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_organization_execution_permit WHERE operation_id=$1)`, op).Scan(&exists)
	if err == nil && exists {
		err = ports.ErrMovieUpgradeOrganizationReview
	}
	var b *organizationExecutionBasis
	if err == nil {
		b, err = s.load(work, tx)
	}
	if err == nil {
		err = tx.Commit(work)
	} else {
		upgradeRollback(work, tx)
	}
	done()
	if err != nil {
		return nil, err
	}
	roots, release, err := r.files.LockRoots(ctx, b.physical.RootPath, b.physical.SourceRoot)
	if err != nil {
		return nil, err
	}
	defer release()
	target, source := roots[b.physical.RootPath], roots[b.physical.SourceRoot]
	if target == nil || source == nil {
		return nil, ports.ErrFileIdentity
	}
	var observed time.Time
	if err = base.conn.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&observed); err != nil {
		return nil, err
	}
	if err = s.preflight(ctx, b, target, source); err != nil {
		return nil, err
	}
	if err = s.admit(ctx, b, observed); err != nil {
		return nil, err
	}
	// Revalidate after the persistent permit commits. An interrupted session leaves
	// the permit in place, including when the filesystem call never began.
	if err = s.ready(ctx); err != nil {
		return nil, err
	}
	var fields struct {
		Until time.Time `json:"lease_until"`
	}
	if json.Unmarshal(s.permit, &fields) != nil || fields.Until.IsZero() {
		return nil, preparationConflict()
	}
	call, stop := context.WithDeadline(ctx, fields.Until)
	callErr := r.files.Hardlink(call, source, b.physical.SourceRel, target, b.physical.TargetRel, b.physical.Source)
	stop()
	// Synchronous return is the proof. There is no public Record/Returned=true API.
	// Keep both kernel root slots while gathering evidence and committing the job.
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	evidence, status := s.returnEvidence(finish, b, target, source, callErr)
	return s.record(finish, evidence, status)
}
func (s *organizationExecution) preflight(ctx context.Context, b *organizationExecutionBasis, target, source ports.TransferRoot) error {
	p := b.physical
	root, e := target.Identity(ctx)
	if e != nil {
		return e
	}
	if root.Device != p.Root.Device || root.Inode != p.Root.Inode {
		return ports.ErrFileIdentity
	}
	if _, e = target.Inspect(ctx, b.marker); e != nil {
		return e
	}
	old, e := target.Inspect(ctx, p.OldRel)
	if e != nil {
		return e
	}
	if old != p.Old {
		return ports.ErrFileIdentity
	}
	current, e := source.Inspect(ctx, p.SourceRel)
	if e != nil {
		return e
	}
	if current != p.Source {
		return ports.ErrFileIdentity
	}
	if _, e = target.Inspect(ctx, p.TargetRel); e == nil {
		return ports.ErrFileConflict
	} else if !errors.Is(e, fs.ErrNotExist) {
		return e
	}
	return ctx.Err()
}
func (s *organizationExecution) returnEvidence(ctx context.Context, b *organizationExecutionBasis, target, source ports.TransferRoot, callErr error) (json.RawMessage, string) {
	root, e1 := target.Identity(ctx)
	old, e2 := target.Inspect(ctx, b.physical.OldRel)
	src, e3 := source.Inspect(ctx, b.physical.SourceRel)
	dst, e4 := target.Inspect(ctx, b.physical.TargetRel)
	_, e5 := target.Inspect(ctx, b.marker)
	status, reason := "manual_review", "file_evidence_changed"
	if callErr == nil && e1 == nil && e2 == nil && e3 == nil && e4 == nil && e5 == nil && root.Device == b.physical.Root.Device && root.Inode == b.physical.Root.Inode && old == b.physical.Old && src == b.physical.Source && dst == b.physical.Source {
		status, reason = "linked_pending_ingestion", "exact_link_returned"
	}
	if callErr != nil {
		reason = "file_operation_returned_error"
	}
	raw, _ := json.Marshal(map[string]any{"physical_returned": true, "root": root, "old": old, "source": src, "target": dst, "reason": reason})
	return raw, status
}
