package transfer

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"testing"
)

type repoProbe struct {
	prepare  func(context.Context, ports.MovieTransferRequest) (ports.PreparedMovieTransfer, error)
	execute  func(context.Context, uuid.UUID) error
	organize func(context.Context, ports.JobLease) error
}

func (r repoProbe) PrepareMovieTransfer(c context.Context, v ports.MovieTransferRequest) (ports.PreparedMovieTransfer, error) {
	return r.prepare(c, v)
}
func (r repoProbe) ExecuteTransfer(c context.Context, v uuid.UUID) error { return r.execute(c, v) }
func (r repoProbe) OrganizeCompletedDownload(c context.Context, v ports.JobLease) error {
	return r.organize(c, v)
}
func TestNarrowTransferCommandsPreserveAuthorityAndDoNotExecuteFailedPreparation(t *testing.T) {
	ctx := context.Background()
	op, root, unit, old, plan := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	calls := 0
	r := repoProbe{prepare: func(c context.Context, v ports.MovieTransferRequest) (ports.PreparedMovieTransfer, error) {
		if c != ctx || v.OperationID != op || v.RootID != root || v.UnitID != unit || v.OldFileID == nil || *v.OldFileID != old || v.SourcePath != "source" || v.Title != "Movie" || v.Year != 2020 {
			t.Fatal("command lost input")
		}
		return ports.PreparedMovieTransfer{Plan: ports.TransferPlan{ID: plan}}, nil
	}, execute: func(c context.Context, id uuid.UUID) error {
		calls++
		if c != ctx || id != plan {
			t.Fatal("wrong authority")
		}
		return ports.ErrTransferExecutionRejected
	}, organize: func(c context.Context, l ports.JobLease) error {
		if c != ctx || l.ID != plan || l.Owner != "owner" || l.Token != 7 {
			t.Fatal("lease changed")
		}
		return ports.ErrTransferReviewRequired
	}}
	s := NewService(r)
	if e := s.UpgradeMediaFile(ctx, op, root, unit, old, "source", "Movie", 2020, quality.Profile{}); !errors.Is(e, ports.ErrTransferExecutionRejected) || calls != 1 {
		t.Fatal("execution contract", e, calls)
	}
	if e := s.OrganizeDownload(ctx, ports.JobLease{ID: plan, Owner: "owner", Token: 7}); !errors.Is(e, ports.ErrTransferReviewRequired) {
		t.Fatal(e)
	}
	r.prepare = func(context.Context, ports.MovieTransferRequest) (ports.PreparedMovieTransfer, error) {
		return ports.PreparedMovieTransfer{}, ports.ErrTransferReviewRequired
	}
	s = NewService(r)
	if e := s.OrganizeMovie(ctx, op, root, unit, "source", "Movie", 2020, quality.Profile{}); !errors.Is(e, ports.ErrTransferReviewRequired) || calls != 1 {
		t.Fatal("failed prepare executed", e, calls)
	}
}
