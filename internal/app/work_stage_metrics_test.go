package app

import (
	"context"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
)

func TestWorkStageMetricsConcurrentAndIsolated(t *testing.T) {
	var recorder, other workStageRecorder
	var group sync.WaitGroup
	for range 100 {
		group.Go(func() {
			finish := recorder.begin(ports.StageDownloadSubmission)
			_ = recorder.snapshot()
			finish()
			finish()
		})
	}
	group.Wait()
	s := recorder.snapshot()[ports.StageDownloadSubmission]
	if s.InFlight != 0 || s.Returned != 100 || s.Seconds <= 0 || s.Buckets[6] != 100 {
		t.Fatal(s)
	}
	if other.snapshot() != ([ports.WorkStageCount]ports.WorkStageMetrics{}) {
		t.Fatal("metrics leaked between App instances")
	}
	for i := 1; i < len(s.Buckets); i++ {
		if s.Buckets[i] < s.Buckets[i-1] {
			t.Fatal("noncumulative buckets")
		}
	}
}

type metricPhysicalGate struct {
	ports.TransferFilesystem
	reached, release chan struct{}
}

func (g *metricPhysicalGate) Hardlink(ctx context.Context, source ports.TransferRoot, sourceRel string, target ports.TransferRoot, targetRel string, identity ports.FileIdentity) error {
	if err := g.TransferFilesystem.Hardlink(ctx, source, sourceRel, target, targetRel, identity); err != nil {
		return err
	}
	close(g.reached)
	<-g.release
	return nil
}
func TestWorkStageMetricsWaitForPhysicalReturnPG(t *testing.T) {
	f := organizationSetup(t)
	f.observe(t)
	gate := &metricPhysicalGate{TransferFilesystem: rootfs.NewFilesystem(), reached: make(chan struct{}), release: make(chan struct{})}
	svc := transfer.NewService(transferstore.NewRepository(f.pool, f.gen, outbox.New(f.pool, f.gen, clock.New()), gate, transfer.ExecutePreparedPlan))
	a := &App{dbPool: f.pool, idgen: f.gen, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	done := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate.release) })
	go func() { defer close(done); a.runOrganizationWorker(ctx, f.queue, svc) }()
	defer func() { cancel(); release(); <-done }()
	select {
	case <-gate.reached:
	case <-ctx.Done():
		t.Fatal("worker did not reach physical operation")
	}
	read := func() ports.WorkStageMetrics {
		t.Helper()
		snapshot, err := a.RuntimeMetricsReader().ReadRuntimeMetrics(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return snapshot.Stages[ports.StageDownloadOrganization]
	}
	cancel()
	if s := read(); s.InFlight != 1 || s.Returned != 0 {
		t.Fatal("cancellation hid executing call", s)
	}
	release()
	<-done
	if s := read(); s.InFlight != 0 || s.Returned != 1 || s.Seconds <= 0 {
		t.Fatal("physical return not recorded", s)
	}
	if f.count(t, "unit_fact") != 0 {
		t.Fatal("cancelled execution falsely committed")
	}
}

func TestWorkStageMetricsMoviePipelinePG(t *testing.T) {
	runFullMoviePipelineLifecycle(t, false, "", "", false, false, true, func(_ *organizationFixture, _ *ports.Actor, _ uuid.UUID, a *App) func() {
		return func() {
			values := a.stages.snapshot()
			if values[ports.StageDownloadReconcile].Returned+values[ports.StageDownloadObserve].Returned == 0 {
				t.Fatal("production download observation not recorded", values)
			}
			for _, stage := range []ports.WorkStage{ports.StageDownloadSubmission, ports.StageDownloadOrganization} {
				s := values[stage]
				if s.Returned == 0 || s.InFlight != 0 || s.Seconds <= 0 {
					t.Fatalf("production stage %s not recorded: %+v", ports.WorkStageLabels()[stage], s)
				}
			}
		}
	})
}

func TestWorkStageMetricsDownloadObservationPG(t *testing.T) {
	f := organizationSetup(t)
	a := &App{dbPool: f.pool, idgen: f.gen, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	done := make(chan struct{})
	go func() { defer close(done); a.runDownloadWorker(ctx, f.queue, f.mgr) }()
	defer func() { cancel(); <-done }()
	waitOutboxCondition(t, ctx, func() bool { return a.stages.snapshot()[ports.StageDownloadObserve].Returned > 0 })
	cancel()
	<-done
	s := a.stages.snapshot()[ports.StageDownloadObserve]
	if s.InFlight != 0 || s.Returned != 1 || s.Seconds <= 0 {
		t.Fatal(s)
	}
	task, err := f.q.GetDownloadTaskByOpID(t.Context(), f.op)
	if err != nil || task.State != "completed" || task.OrganizationState != "queued" {
		t.Fatal(task, err)
	}
	if f.count(t, "unit_fact") != 0 {
		t.Fatal("observation conflated with import")
	}
}

func TestWorkStageMetricsWatchWaitsForPhysicalReturn(t *testing.T) {
	for _, kind := range []string{"ordinary", "complete_tv_tracking_hint"} {
		t.Run(kind, func(t *testing.T) {
			b := watchBinding()
			repo := watchRepo(b)
			target := watchTarget(b)
			target.Kind = kind
			repo.resolve = func(context.Context, ports.DownloadWatchBinding, ports.RootChangeHint, ports.DownloadWatchTargetQuery) (ports.DownloadWatchTargetPage, error) {
				return ports.DownloadWatchTargetPage{Targets: []ports.DownloadWatchTarget{target}, Complete: true}, nil
			}
			a := &App{}
			entered, canceled, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			ctx, cancel := context.WithCancel(t.Context())
			unblock := sync.OnceFunc(func() { close(release) })
			t.Cleanup(func() { cancel(); unblock(); watchReceive(t, done) })
			go func() {
				defer close(done)
				a.runDownloadWatch(ctx, repo, watchObserverFake(func(c context.Context, _ ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
					close(entered)
					<-c.Done()
					close(canceled)
					<-release
					return nil, c.Err()
				}), watchFS(), nil, downloadWatchOptions{refresh: time.Hour})
			}()
			watchReceive(t, entered)
			cancel()
			watchReceive(t, canceled)
			expected := int64(0)
			if kind == "ordinary" {
				expected = 1
			}
			s := a.stages.snapshot()[ports.StageDownloadObserve]
			if s.InFlight != expected || s.Returned != 0 {
				t.Fatal("cancel ended physical observation", s)
			}
			unblock()
			watchReceive(t, done)
			s = a.stages.snapshot()[ports.StageDownloadObserve]
			if s.InFlight != 0 || s.Returned != uint64(expected) {
				t.Fatal("scope or return accounting", s)
			}
		})
	}
}
