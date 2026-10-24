package download

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type automaticTVHintTestCapture struct {
	request ports.AutomaticTVObservationHintRequest
}

func (c *automaticTVHintTestCapture) AutomaticTVObservationHintRequest() ports.AutomaticTVObservationHintRequest {
	return c.request
}

type automaticTVHintTestScheduler struct {
	schedule func(context.Context, ports.AutomaticTVObservationHintCapture) (*ports.AutomaticTVObservationHintScheduleResult, error)
}

func (s automaticTVHintTestScheduler) ResolveAutomaticTVObservationHint(context.Context, ports.AutomaticTVObservationHintRequest) (ports.AutomaticTVObservationHintCapture, error) {
	return nil, ports.ErrUnavailable
}
func (s automaticTVHintTestScheduler) ScheduleAutomaticTVObservationHint(ctx context.Context, c ports.AutomaticTVObservationHintCapture) (*ports.AutomaticTVObservationHintScheduleResult, error) {
	return s.schedule(ctx, c)
}

func automaticTVHintTestTarget() ports.DownloadWatchTarget {
	b := ports.DownloadWatchBinding{ClientID: uuid.New(), ClientStamp: "bound-client", ClientVersion: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)}
	request := ports.AutomaticTVObservationHintRequest{OperationID: uuid.New(), TaskID: uuid.New(), PriorJobID: uuid.New(), SendAttemptID: uuid.New(), Binding: b}
	return ports.DownloadWatchTarget{Binding: b, OperationID: request.OperationID, TaskID: request.TaskID, ClientID: b.ClientID, ClientStamp: b.ClientStamp, ClientVersion: b.ClientVersion, Kind: "automatic_tv_observation_hint", AutomaticTVHintCapture: &automaticTVHintTestCapture{request}}
}

func TestAutomaticTVHintWaitsSamePredecessor(t *testing.T) {
	target := automaticTVHintTestTarget()
	calls := 0
	scheduler := automaticTVHintTestScheduler{schedule: func(_ context.Context, c ports.AutomaticTVObservationHintCapture) (*ports.AutomaticTVObservationHintScheduleResult, error) {
		calls++
		if c != target.AutomaticTVHintCapture {
			t.Fatal("wait resolved a new capture")
		}
		if calls == 1 {
			return &ports.AutomaticTVObservationHintScheduleResult{RetryAfter: time.Millisecond}, nil
		}
		if calls > 2 {
			t.Fatal("queued capture called again")
		}
		return &ports.AutomaticTVObservationHintScheduleResult{Queued: true, JobID: uuid.New()}, nil
	}}
	out, err := NewAutomaticTVHintObserver(scheduler).ObserveDownloadWatchTarget(t.Context(), target)
	if err != nil || out != nil || calls != 2 {
		t.Fatal("queueing must not synthesize download/organization completion", out, err, calls)
	}
}

func TestAutomaticTVHintCancellationDuringDueWait(t *testing.T) {
	calls := 0
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	scheduler := automaticTVHintTestScheduler{schedule: func(context.Context, ports.AutomaticTVObservationHintCapture) (*ports.AutomaticTVObservationHintScheduleResult, error) {
		calls++
		return &ports.AutomaticTVObservationHintScheduleResult{RetryAfter: 2 * time.Second}, nil
	}}
	out, err := NewAutomaticTVHintObserver(scheduler).ObserveDownloadWatchTarget(ctx, automaticTVHintTestTarget())
	if !errors.Is(err, context.DeadlineExceeded) || out != nil || calls != 1 {
		t.Fatal("cancelled wait retried or published completion", out, err, calls)
	}
}

func TestAutomaticTVHintRejectsReboundTarget(t *testing.T) {
	for _, kind := range []string{"kind", "capture", "operation", "task", "client", "stamp", "version", "binding"} {
		t.Run(kind, func(t *testing.T) {
			target := automaticTVHintTestTarget()
			switch kind {
			case "kind":
				target.Kind = "ordinary"
			case "capture":
				target.AutomaticTVHintCapture = nil
			case "operation":
				target.OperationID = uuid.New()
			case "task":
				target.TaskID = uuid.New()
			case "client":
				target.ClientID = uuid.New()
			case "stamp":
				target.ClientStamp = "changed"
			case "version":
				target.ClientVersion = target.ClientVersion.Add(time.Second)
			case "binding":
				target.Binding.RootPath = "/different/root"
			}
			calls := 0
			scheduler := automaticTVHintTestScheduler{schedule: func(context.Context, ports.AutomaticTVObservationHintCapture) (*ports.AutomaticTVObservationHintScheduleResult, error) {
				calls++
				return nil, nil
			}}
			_, err := NewAutomaticTVHintObserver(scheduler).ObserveDownloadWatchTarget(t.Context(), target)
			if !errors.Is(err, ports.ErrDownloadWatchStale) || calls != 0 {
				t.Fatal("rebound target dispatched", err, calls)
			}
		})
	}
}

func TestAutomaticTVHintInvalidSchedulerReply(t *testing.T) {
	target := automaticTVHintTestTarget()
	for _, test := range []struct {
		name   string
		result *ports.AutomaticTVObservationHintScheduleResult
	}{
		{"nil", nil}, {"negative", &ports.AutomaticTVObservationHintScheduleResult{RetryAfter: -time.Millisecond}},
		{"beyond-original-gate", &ports.AutomaticTVObservationHintScheduleResult{RetryAfter: 2*time.Second + time.Nanosecond}},
		{"queued-without-id", &ports.AutomaticTVObservationHintScheduleResult{Queued: true}},
		{"prior-reused", &ports.AutomaticTVObservationHintScheduleResult{Queued: true, JobID: target.AutomaticTVHintCapture.AutomaticTVObservationHintRequest().PriorJobID}},
		{"queued-and-wait", &ports.AutomaticTVObservationHintScheduleResult{Queued: true, JobID: uuid.New(), RetryAfter: time.Second}},
		{"pending-with-id", &ports.AutomaticTVObservationHintScheduleResult{JobID: uuid.New(), RetryAfter: time.Millisecond}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := automaticTVHintTestScheduler{schedule: func(context.Context, ports.AutomaticTVObservationHintCapture) (*ports.AutomaticTVObservationHintScheduleResult, error) {
				return test.result, nil
			}}
			out, err := NewAutomaticTVHintObserver(s).ObserveDownloadWatchTarget(t.Context(), target)
			if !errors.Is(err, ports.ErrUnavailable) || out != nil {
				t.Fatal("invalid scheduler reply accepted", out, err)
			}
		})
	}
}

func TestAutomaticTVHintTerminalAndFailure(t *testing.T) {
	expected := errors.New("database unavailable")
	for _, failure := range []bool{false, true} {
		calls := 0
		s := automaticTVHintTestScheduler{schedule: func(context.Context, ports.AutomaticTVObservationHintCapture) (*ports.AutomaticTVObservationHintScheduleResult, error) {
			calls++
			if failure {
				return nil, expected
			}
			return &ports.AutomaticTVObservationHintScheduleResult{}, nil
		}}
		out, err := NewAutomaticTVHintObserver(s).ObserveDownloadWatchTarget(t.Context(), automaticTVHintTestTarget())
		if out != nil || calls != 1 || failure && !errors.Is(err, expected) || !failure && err != nil {
			t.Fatal("terminal/failure retried or published", out, err, calls)
		}
	}
	_, err := NewAutomaticTVHintObserver(nil).ObserveDownloadWatchTarget(t.Context(), automaticTVHintTestTarget())
	if !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("missing scheduler accepted", err)
	}
}
