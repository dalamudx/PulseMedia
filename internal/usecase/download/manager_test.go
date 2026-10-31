package download

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"testing"
)

type executionProbe struct {
	ports.DownloadExecutionRepository
	task                                      *ports.DownloadExecutionTask
	submitCount, reconcileCount, observeCount int
	ctx                                       context.Context
	op, client, candidate                     uuid.UUID
	lease                                     ports.JobLease
}

func (p *executionProbe) PrepareDownload(ctx context.Context, op, client, candidate uuid.UUID, path string, a ...ports.JobLease) (*ports.PreparedDownload, error) {
	p.submitCount++
	if ctx != p.ctx || op != p.op || client != p.client || candidate != p.candidate || path != "/configured-root" || len(a) != 1 || a[0] != p.lease {
		panic("submission command changed")
	}
	return &ports.PreparedDownload{Task: p.task}, ports.ErrDownloadReconcilePending
}
func (p *executionProbe) PrepareDownloadObservation(ctx context.Context, op uuid.UUID, reconcile bool, watch *ports.DownloadWatchTarget) (*ports.PreparedDownloadObservation, error) {
	if watch != nil {
		panic("unexpected watch")
	}
	if reconcile {
		p.reconcileCount++
	} else {
		p.observeCount++
	}
	if ctx != p.ctx || op != p.op {
		panic("observation authority changed")
	}
	return &ports.PreparedDownloadObservation{Task: p.task}, nil
}
func (p *executionProbe) ReconcileTask(ctx context.Context, op uuid.UUID) (*ports.DownloadExecutionTask, error) {
	p.reconcileCount++
	if ctx != p.ctx || op != p.op {
		panic("reconcile authority changed")
	}
	return p.task, nil
}
func (p *executionProbe) ObserveTask(ctx context.Context, op uuid.UUID) (*ports.DownloadExecutionTask, error) {
	p.observeCount++
	if ctx != p.ctx || op != p.op {
		panic("observe authority changed")
	}
	return p.task, nil
}
func TestDownloadUsecaseDoesNotRetryUnknownAndPreservesTaskAndLease(t *testing.T) {
	p := &executionProbe{ctx: context.Background(), op: uuid.New(), client: uuid.New(), candidate: uuid.New(), lease: ports.JobLease{ID: uuid.New(), Owner: "worker", Token: 7}}
	p.task = &ports.DownloadExecutionTask{ID: uuid.New(), OperationID: p.op, State: "unknown"}
	m := NewManager(p)
	task, e := m.SubmitDownload(p.ctx, p.op, p.client, p.candidate, "/configured-root", p.lease)
	if task != p.task || !errors.Is(e, ErrReconcilePending) || p.submitCount != 1 {
		t.Fatal("unknown result or lease lost", e)
	}
	if _, e = m.ReconcileTask(p.ctx, p.op); e != nil {
		t.Fatal(e)
	}
	if _, e = m.ObserveTask(p.ctx, p.op); e != nil {
		t.Fatal(e)
	}
	if p.submitCount != 1 || p.reconcileCount != 1 || p.observeCount != 1 {
		t.Fatal("observation performed another Add")
	}
}
