package download

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"testing"
)

type watchObserverRepository struct {
	calls  int
	target ports.DownloadWatchTarget
	task   *ports.DownloadExecutionTask
	err    error
}

func (r *watchObserverRepository) ObserveDownloadWatchTarget(_ context.Context, target ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
	r.calls++
	r.target = target
	return r.task, r.err
}

func TestWatchObserverDelegatesBoundTarget(t *testing.T) {
	target := ports.DownloadWatchTarget{TaskID: uuid.New(), OperationID: uuid.New(), ClientStamp: "safe-stamp", TaskStamp: "task-stamp", Kind: "ordinary"}
	repo := &watchObserverRepository{task: &ports.DownloadExecutionTask{ID: target.TaskID}, err: ports.ErrDownloadWatchStale}
	got, err := NewWatchObserver(repo).ObserveDownloadWatchTarget(t.Context(), target)
	if got != repo.task || !errors.Is(err, repo.err) || repo.calls != 1 || repo.target != target {
		t.Fatal("delegate changed target or result")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = NewWatchObserver(repo).ObserveDownloadWatchTarget(ctx, target); !errors.Is(err, context.Canceled) || repo.calls != 1 {
		t.Fatal("canceled delegate dispatched")
	}
}
